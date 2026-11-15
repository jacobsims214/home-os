// Package vendors provides the MCP contact tools (catalog v2, KB
// architecture/mcp-tool-catalog.md "contact: vendors, providers and
// services, found by trade"). Agents call vendors "contacts"; the table
// stays vendors. Stage 1 has one tool, contact_find, which replaces
// list_vendors and get_vendor. contact_create, contact_update and
// contact_delete are Stage 2 (#4466).
//
// Every result is built with internal/respond, and every tool declares all
// four annotation hints and an outputSchema with string ids. All operations
// are scoped to the authenticated household via JWT claims.
//
// vendors.property_id is not read or returned: 040 drops it and the API
// stopped reading it in #4449. Trades come from 043's vendors.trades.
package vendors

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// Pagination limits shared by every catalog find tool.
const (
	defaultLimit = 25
	maxLimit     = 100
)

// toolHandler is the signature every contact tool handler has.
type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// contactCounts is what in the family points at a contact.
type contactCounts struct {
	Bills           int `json:"bills" jsonschema_description:"Bills paid to this contact."`
	OpenMaintenance int `json:"open_maintenance" jsonschema_description:"Upkeep tasks not done yet that this contact does."`
	Pets            int `json:"pets" jsonschema_description:"Pets whose vet this contact is."`
}

// contactData is one contact in contact_find's output. IDs are strings (a
// uuid.UUID would be advertised as a byte array in the outputSchema).
type contactData struct {
	ID        string        `json:"id" jsonschema_description:"The contact_id to pass to bill_create, maintenance_create and others."`
	Name      string        `json:"name"`
	Trades    []string      `json:"trades" jsonschema_description:"What they do, lower case, e.g. [\"plumber\"] or [\"vet\"]. May be empty."`
	Specialty *string       `json:"specialty" jsonschema_description:"Free-text specialty as the family typed it; null when not set."`
	Phone     *string       `json:"phone"`
	Email     *string       `json:"email"`
	Website   *string       `json:"website"`
	Notes     *string       `json:"notes"`
	Counts    contactCounts `json:"counts"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// label is the contact's name with its trades (or specialty), e.g.
// "Joe's Plumbing (plumber)".
func (d contactData) label() string {
	switch {
	case len(d.Trades) > 0:
		return fmt.Sprintf("%s (%s)", d.Name, strings.Join(d.Trades, ", "))
	case d.Specialty != nil:
		return fmt.Sprintf("%s (%s)", d.Name, *d.Specialty)
	default:
		return d.Name
	}
}

// reach is how to reach the contact, e.g. "phone 555-0101, email a@b.c".
func (d contactData) reach() string {
	var parts []string
	if d.Phone != nil {
		parts = append(parts, "phone "+*d.Phone)
	}
	if d.Email != nil {
		parts = append(parts, "email "+*d.Email)
	}
	if d.Website != nil {
		parts = append(parts, "website "+*d.Website)
	}
	if len(parts) == 0 {
		return "no phone, email or website saved"
	}
	return strings.Join(parts, ", ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// notSignedIn is the result for a call without household claims.
func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}

// annotations sets all four hints (catalog §1): class R (find), C (create),
// U (update) or X (delete). openWorldHint is always false.
func annotations(title string, readOnly, destructive, idempotent bool) []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithTitleAnnotation(title),
		mcp.WithReadOnlyHintAnnotation(readOnly),
		mcp.WithDestructiveHintAnnotation(destructive),
		mcp.WithIdempotentHintAnnotation(idempotent),
		mcp.WithOpenWorldHintAnnotation(false),
	}
}

// --- trades ---

// tradeSynonyms maps common words for a trade to the stored trade, the same
// vocabulary 043 backfilled from specialty. Small models ask for
// "plumbing" or "veterinarian" as often as "plumber" or "vet".
var tradeSynonyms = map[string]string{
	"heating": "hvac", "heating_&_cooling": "hvac", "heating_and_cooling": "hvac", "ac": "hvac",
	"plumbing":   "plumber",
	"electrical": "electrician", "electric": "electrician",
	"landscaping": "landscaper", "lawn_care": "landscaper", "lawn": "landscaper", "gardener": "landscaper",
	"roofing":      "roofer",
	"exterminator": "pest_control", "pest": "pest_control",
	"cleaning": "cleaner", "house_cleaning": "cleaner", "housekeeper": "cleaner",
	"pool_service": "pool",
	"auto_repair":  "mechanic",
	"veterinarian": "vet", "veterinary": "vet", "animal_hospital": "vet",
	"pediatrics": "pediatrician",
	"physician":  "doctor",
	"dental":     "dentist",
}

// normalizeTrade turns user text into the stored trade form: lower case,
// spaces and hyphens as underscores, synonyms mapped ("Pest Control" ->
// pest_control, "Plumbing" -> plumber).
func normalizeTrade(s string) string {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.Join(strings.FieldsFunc(t, func(r rune) bool { return r == ' ' || r == '-' || r == '_' }), "_")
	if mapped, ok := tradeSynonyms[t]; ok {
		return mapped
	}
	return t
}

// --- argument parsing ---

// argError is a bad argument: which field, what is wrong, how to fix it.
type argError struct {
	field, message, fix string
}

func (e *argError) Error() string { return e.message }

func badArg(field, message, fix string) error {
	return &argError{field: field, message: message, fix: fix}
}

// invalid turns a parse error into an isError result with a fix and a
// valid example.
func invalid(err error, example map[string]any) *mcp.CallToolResult {
	var ae *argError
	if errors.As(err, &ae) {
		return respond.Invalid(ae.field, ae.message, ae.fix, example)
	}
	return respond.Invalid("", err.Error(), "Correct the arguments and call again.", example)
}

// given reports whether args has a non-null value for key.
func given(args map[string]any, key string) bool {
	v, present := args[key]
	return present && v != nil
}

// withAliases returns a copy of args with old parameter names moved to their
// catalog names (a catalog name that is present wins).
func withAliases(args map[string]any, aliases map[string]string) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	for old, name := range aliases {
		if v, ok := out[old]; ok {
			if !given(out, name) {
				out[name] = v
			}
			delete(out, old)
		}
	}
	return out
}

// optionalString returns the trimmed string argument, or nil when it is
// absent, null or blank. A non-string value is an error.
func optionalString(args map[string]any, key string) (*string, error) {
	v, present := args[key]
	if !present || v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, badArg(key, key+" must be a string.", fmt.Sprintf("Pass %s as text in quotes.", key))
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	return &s, nil
}

// wholeNumber reads an integer argument in [lo, hi]; ok is false when it is
// absent or null. Numbers sent as numeric strings are accepted, because
// small models often quote them.
func wholeNumber(args map[string]any, key string, lo, hi int) (int, bool, error) {
	v, present := args[key]
	if !present || v == nil {
		return 0, false, nil
	}
	bad := badArg(key, fmt.Sprintf("%s must be a whole number from %d to %d.", key, lo, hi),
		fmt.Sprintf("Pass %s as a whole number between %d and %d.", key, lo, hi))
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case int:
		n = float64(x)
	case int64:
		n = float64(x)
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, false, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false, bad
		}
		n = f
	default:
		return 0, false, bad
	}
	if math.IsNaN(n) || n != math.Trunc(n) || n < float64(lo) || n > float64(hi) {
		return 0, false, bad
	}
	return int(n), true, nil
}

// escapeLike escapes ILIKE wildcards so query matches literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
