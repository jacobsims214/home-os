// Package property provides the MCP property tools (catalog v2, KB
// architecture/mcp-tool-catalog.md "property / room"). Stage 1 has one
// tool, property_find, which replaces list_properties and get_property.
// Stage 2 (#4462) adds property_create, property_update, property_delete
// and room_delete (write.go, room.go).
// Every result is built with internal/respond, and every tool declares all
// four annotation hints and an outputSchema. All operations are scoped to
// the authenticated household via JWT claims.
//
// Rooms follow domain model v2 (038): rooms.household_id exists but stays
// nullable until 056, so rooms are always scoped through their property's
// household_id, which is correct before and after 056. Mortgages are not
// read from the old properties.mortgage_* columns: 044 copied them into
// loans(property_id), so the tools point to loan_find instead.
package property

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// Pagination limits shared by every catalog find tool.
const (
	defaultLimit = 25
	maxLimit     = 100
)

// querier is what both a pool and a transaction offer.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// toolHandler is the signature every property tool handler has.
type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// roomData is one room inside a property. IDs are strings (a uuid.UUID
// would be advertised as a byte array in the outputSchema).
type roomData struct {
	ID         string `json:"id" jsonschema_description:"The room_id to pass to asset_find or asset_create."`
	Name       string `json:"name"`
	Floor      *int   `json:"floor" jsonschema_description:"Floor number; null when not set."`
	AssetCount int    `json:"asset_count" jsonschema_description:"Things recorded in this room."`
}

// propertyMoney is every money field of a property in one struct, so hiding
// money from a role is one decision (propertyRow.data) rather than a check per
// field or per handler. It is embedded in propertyData, so the fields serialize
// at the top level next to id and name, exactly as the API's propertyMoney does
// (#4806).
//
// Every field is a pointer with `omitzero`: when the struct is left zero (a
// role that may not see money) the fields are ABSENT from the JSON rather than
// null, so a model can never read "purchase_price: null" as a value. A field
// added here is hidden from housesitters and vendors automatically.
// PurchaseDate is not an amount, but it belongs to the same financial record.
type propertyMoney struct {
	PurchaseDate  *string  `json:"purchase_date,omitzero" jsonschema_description:"YYYY-MM-DD."`
	PurchasePrice *float64 `json:"purchase_price,omitzero" jsonschema_description:"Price paid, in the family's currency."`
	CurrentValue  *float64 `json:"current_value,omitzero" jsonschema_description:"Estimated value today, in the family's currency."`
}

// propertyMoneyCounts is the money part of propertyCounts: how many bills and
// loans the property carries. Those counts are money too (a housesitter was
// told how many bills and loans the family has, and findNext pointed them at
// loan_find), so they sit in their own embedded struct with the same rule.
type propertyMoneyCounts struct {
	Bills *int `json:"bills,omitzero" jsonschema_description:"Bills tied to the property (electric, tax, insurance)."`
	Loans *int `json:"loans,omitzero" jsonschema_description:"Loans on the property or its assets, e.g. the mortgage (loan_find property_id=...)."`
}

// propertyCounts is how much is recorded about a property.
type propertyCounts struct {
	Rooms           int `json:"rooms"`
	Assets          int `json:"assets" jsonschema_description:"Things in the property, in a room or not (asset_find property_id=...)."`
	OpenMaintenance int `json:"open_maintenance" jsonschema_description:"Upkeep tasks not done yet (maintenance_find property_id=...)."`
	propertyMoneyCounts
}

// propertyData is one property in property_find's output. The money fields are
// embedded, so they serialize at the top level: filled only when the caller's
// role may see money, absent otherwise.
type propertyData struct {
	ID           string         `json:"id" jsonschema_description:"The property_id to pass to asset_find, loan_find, bill_create and others."`
	Name         string         `json:"name"`
	Address      *string        `json:"address"`
	PropertyType *string        `json:"property_type" jsonschema_description:"e.g. house, condo, rental, land."`
	Notes        *string        `json:"notes"`
	Rooms        []roomData     `json:"rooms" jsonschema_description:"Every room, by floor then name. A room_id from search_all opens here."`
	Counts       propertyCounts `json:"counts"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`

	propertyMoney

	// Set only by the write tools.
	Duplicate    bool     `json:"duplicate,omitempty" jsonschema_description:"property_create: true when this property was added minutes ago and is returned instead of a copy."`
	Changed      []string `json:"changed,omitempty" jsonschema_description:"property_update: the params that changed something."`
	RoomsAdded   []string `json:"rooms_added,omitempty" jsonschema_description:"Rooms this call added."`
	RoomsSkipped []string `json:"rooms_skipped,omitempty" jsonschema_description:"Room names the property already had; nothing was added for them."`
}

// describe is a short phrase for summaries, e.g. "at 12 Elm St, 5 rooms,
// 14 assets, value $450,000.00". The money parts are simply absent for a role
// that may not see money: the fields they would read are unset.
func (d propertyData) describe() string {
	var parts []string
	if d.Address != nil {
		parts = append(parts, "at "+*d.Address)
	}
	parts = append(parts, plural(d.Counts.Rooms, "room"), plural(d.Counts.Assets, "asset"))
	if n := d.Counts.loans(); n > 0 {
		parts = append(parts, plural(n, "loan"))
	}
	if d.CurrentValue != nil {
		parts = append(parts, "value "+money(*d.CurrentValue))
	}
	return strings.Join(parts, ", ")
}

// loans is the loan count, 0 when it is hidden from the caller's role.
func (c propertyCounts) loans() int {
	if c.Loans == nil {
		return 0
	}
	return *c.Loans
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// money formats an amount for summaries with thousands separators, e.g.
// "$450,000.00".
func money(v float64) string {
	neg := v < 0
	s := strconv.FormatFloat(math.Abs(v), 'f', 2, 64)
	whole, frac := s[:len(s)-3], s[len(s)-3:]
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	out := "$" + b.String() + frac
	if neg {
		out = "-" + out
	}
	return out
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
