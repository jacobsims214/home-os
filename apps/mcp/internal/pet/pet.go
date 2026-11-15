// Package pet provides the MCP pet tools (catalog v2, KB
// architecture/mcp-tool-catalog.md "pet"). Stage 1 has one tool, pet_find,
// which replaces list_pets and get_pet. Every result is built with
// internal/respond, and every tool declares all four annotation hints and an
// outputSchema. Pets belong to the family: all operations are scoped to the
// authenticated household via JWT claims.
//
// The vet follows migration 043: pets.vet_vendor_id links a family vendor
// (a contact, usually with trade 'vet'). The old pets.vet_name / vet_phone
// columns stay (056, #4601, kept them: api, mcp and ui still use them), so a
// pet with no linked vendor falls back to them. The migration that drops them
// should delete that fallback (vetLegacy) with the columns.
package pet

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

// Where a pet's vet came from.
const (
	vetContact = "contact" // pets.vet_vendor_id -> vendors
	vetLegacy  = "legacy"  // pets.vet_name / vet_phone (kept by 056)
)

// toolHandler is the signature every pet tool handler has.
type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// vetData is a pet's vet. IDs are strings (a uuid.UUID would be advertised
// as a byte array in the outputSchema).
type vetData struct {
	ContactID *string `json:"contact_id" jsonschema_description:"The vet's contact_id for contact_find id=...; null for an old unlinked vet."`
	Name      string  `json:"name"`
	Phone     *string `json:"phone"`
	Email     *string `json:"email"`
	Source    string  `json:"source" jsonschema_description:"contact: a saved contact. legacy: only a name/phone typed on the pet."`
}

// petData is one pet in pet_find's output.
type petData struct {
	ID                string    `json:"id" jsonschema_description:"The pet_id; also about_id with about_type=pet for notes and documents."`
	Name              string    `json:"name"`
	Species           *string   `json:"species" jsonschema_description:"e.g. dog, cat."`
	Breed             *string   `json:"breed"`
	DateOfBirth       *string   `json:"date_of_birth" jsonschema_description:"Birthday, YYYY-MM-DD; null when not known."`
	Age               *int      `json:"age" jsonschema_description:"Age in whole years in the family timezone; null without a birthday."`
	Vet               *vetData  `json:"vet" jsonschema_description:"The pet's vet; null when none is saved."`
	MicrochipID       *string   `json:"microchip_id"`
	RegistrationID    *string   `json:"registration_id"`
	InsuranceProvider *string   `json:"insurance_provider"`
	InsurancePolicy   *string   `json:"insurance_policy"`
	Notes             *string   `json:"notes"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// describe is a short phrase for summaries, e.g. "dog (Beagle), 4 years
// old, vet Dr. Paws".
func (d petData) describe() string {
	var parts []string
	switch {
	case d.Species != nil && d.Breed != nil:
		parts = append(parts, fmt.Sprintf("%s (%s)", *d.Species, *d.Breed))
	case d.Species != nil:
		parts = append(parts, *d.Species)
	case d.Breed != nil:
		parts = append(parts, *d.Breed)
	}
	if d.Age != nil {
		parts = append(parts, plural(*d.Age, "year")+" old")
	}
	if d.DateOfBirth != nil {
		parts = append(parts, "born "+*d.DateOfBirth)
	}
	if d.Vet != nil {
		parts = append(parts, "vet "+d.Vet.Name)
	} else {
		parts = append(parts, "no vet saved")
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
