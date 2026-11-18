// Package asset provides the MCP asset tools (catalog v2, KB
// architecture/mcp-tool-catalog.md "asset"): asset_find, asset_create,
// asset_update and asset_delete. Every result is built with internal/respond
// and every tool declares all four annotation hints and an outputSchema.
// All operations are scoped to the authenticated household via JWT claims.
//
// Location follows domain model v2 (migration 038): an asset belongs to the
// family; a room and a property are both optional (jewelry, a laptop). When
// it is in a room its property_id is the room's property (the
// assets_derive_property trigger enforces this; the tools compute the same
// thing up front so they can explain it). Deleting a property deletes its
// assets; deleting a room leaves the asset in the property.
package asset

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/search"
	"home-os/mcp/internal/server"
)

// Pagination limits shared by every catalog find tool.
const (
	defaultLimit = 25
	maxLimit     = 100
)

// Column limits from the assets table (003, 031).
const (
	maxNameLen     = 255 // name, manufacturer, model, serial_number
	maxCategoryLen = 100
	// NUMERIC(12,2) holds at most 9,999,999,999.99.
	maxAmount = 9_999_999_999.99
)

// exampleAssetID is the UUID used in error examples.
const exampleAssetID = "3f6b2c1d-8e4a-4b7f-9c2d-5a1e0f7b6c3d"

// toolHandler is the signature every asset tool handler has.
type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// searchIndexer is the minimal interface for indexing documents into
// Typesense. search.Indexer implements it; nil disables indexing.
type searchIndexer interface {
	IndexDocument(ctx context.Context, doc search.Document) error
}

// assetSelect loads assets with their property and room names. Scanned by
// scanAsset.
// The two money columns (purchase price/value) and purchase_date are each
// wrapped in `CASE WHEN $1 THEN ... END`, where $1 is the caller's
// auth.CanSeeMoney: for a role without money the query yields NULL and the
// family's finance never enters the process. Every query on this constant must
// therefore pass withMoney first (see loadAsset and scanAsset).
const assetSelect = `SELECT a.id, a.household_id, a.property_id, a.room_id, a.name, a.category,
	       a.manufacturer, a.model, a.serial_number,
	       CASE WHEN $1::bool THEN a.purchase_date END,
	       CASE WHEN $1::bool THEN a.purchase_price::float8 END,
	       CASE WHEN $1::bool THEN a.current_value::float8 END,
	       a.warranty_expiry,
	       a.created_at, a.updated_at, p.name, r.name
	FROM assets a
	LEFT JOIN properties p ON p.id = a.property_id
	LEFT JOIN rooms r ON r.id = a.room_id`

// assetRow is an assets row as the tools load it.
type assetRow struct {
	ID             uuid.UUID
	HouseholdID    uuid.UUID
	PropertyID     *uuid.UUID
	RoomID         *uuid.UUID
	Name           string
	Category       *string
	Manufacturer   *string
	Model          *string
	SerialNumber   *string
	PurchaseDate   *time.Time
	PurchasePrice  *float64
	CurrentValue   *float64
	WarrantyExpiry *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	PropertyName   *string
	RoomName       *string
}

// scanAsset reads one row of assetSelect.
func scanAsset(row pgx.Row) (assetRow, error) {
	var r assetRow
	err := row.Scan(&r.ID, &r.HouseholdID, &r.PropertyID, &r.RoomID, &r.Name, &r.Category,
		&r.Manufacturer, &r.Model, &r.SerialNumber, &r.PurchaseDate,
		&r.PurchasePrice, &r.CurrentValue, &r.WarrantyExpiry,
		&r.CreatedAt, &r.UpdatedAt, &r.PropertyName, &r.RoomName)
	return r, err
}

// querier is what loadAsset and householdToday need (a pool or a tx).
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// loadAsset loads one asset of the household, pgx.ErrNoRows when there is none.
// withMoney (the caller's auth.CanSeeMoney) is the query's $1: false makes it
// yield no money at all.
func loadAsset(ctx context.Context, q querier, id uuid.UUID, householdID string, withMoney bool) (assetRow, error) {
	return scanAsset(q.QueryRow(ctx, assetSelect+` WHERE a.id = $2 AND a.household_id = $3`, withMoney, id, householdID))
}

// assetMoney is every money field of an asset in one struct, so hiding money
// from a role is one decision (assetRow.data) rather than a check per field or
// per handler (#4808, the pattern of #4806). It is embedded in assetData, so
// the fields serialize at the top level next to name and category.
//
// Every field is a pointer with `omitzero`: when the struct is left zero (a
// role that may not see money) the fields are ABSENT from the JSON rather than
// null. A field added here is hidden from housesitters and vendors
// automatically. This is the asset half #4806 left open: ResAssets read is
// rolesEveryone, so a sitter or a vendor may list the family's things and was
// told what each one cost and is worth.
type assetMoney struct {
	PurchaseDate  *string  `json:"purchase_date,omitzero" jsonschema_description:"YYYY-MM-DD."`
	PurchasePrice *float64 `json:"purchase_price,omitzero" jsonschema_description:"Price paid, in the family's currency."`
	CurrentValue  *float64 `json:"current_value,omitzero" jsonschema_description:"What it is worth now, in the family's currency."`
}

// assetData is one asset in every asset tool's output. IDs are strings (a
// uuid.UUID would be advertised as a byte array in the outputSchema). Money is
// embedded and filled only for a role that may see it.
type assetData struct {
	ID                 string    `json:"id" jsonschema_description:"The asset_id to pass to asset_update or asset_delete."`
	Name               string    `json:"name"`
	Category           *string   `json:"category"`
	Manufacturer       *string   `json:"manufacturer"`
	Model              *string   `json:"model"`
	SerialNumber       *string   `json:"serial_number"`
	PropertyID         *string   `json:"property_id" jsonschema_description:"The home or other property it is in; null when it is not tied to one."`
	PropertyName       *string   `json:"property_name"`
	RoomID             *string   `json:"room_id" jsonschema_description:"The room it is in; null when it is not in a room."`
	RoomName           *string   `json:"room_name"`
	WarrantyExpiry     *string   `json:"warranty_expiry" jsonschema_description:"Last day of the warranty, YYYY-MM-DD."`
	WarrantyWeekday    *string   `json:"warranty_weekday" jsonschema_description:"Weekday of warranty_expiry, e.g. Thu."`
	WarrantyActive     *bool     `json:"warranty_active" jsonschema_description:"true while the warranty runs (today in the family's timezone); null without warranty_expiry."`
	WarrantyDaysLeft   *int      `json:"warranty_days_left" jsonschema_description:"Days from today to warranty_expiry; negative once it has expired."`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	Duplicate          bool      `json:"duplicate,omitempty" jsonschema_description:"asset_create only: true when the same asset was created minutes ago and was returned instead."`
	UpkeepTasksDeleted *int      `json:"upkeep_tasks_deleted,omitempty" jsonschema_description:"asset_delete only: upkeep tasks deleted with the asset."`
	DocumentsMoved     *int      `json:"documents_moved,omitempty" jsonschema_description:"asset_delete only: documents moved to the family."`
	NotesDeleted       *int      `json:"notes_deleted,omitempty" jsonschema_description:"asset_delete only: notes about the asset that were deleted."`

	assetMoney
}

// data builds the output for r as of today (household-local midnight UTC).
// withMoney (the caller's auth.CanSeeMoney) decides the money block: false
// leaves it zero, so the amounts are absent.
func (r assetRow) data(today time.Time, withMoney bool) assetData {
	d := assetData{
		ID: r.ID.String(), Name: r.Name, Category: r.Category, Manufacturer: r.Manufacturer,
		Model: r.Model, SerialNumber: r.SerialNumber,
		PropertyID: uuidString(r.PropertyID), PropertyName: r.PropertyName,
		RoomID: uuidString(r.RoomID), RoomName: r.RoomName,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if withMoney {
		d.assetMoney = assetMoney{PurchasePrice: r.PurchasePrice, CurrentValue: r.CurrentValue}
	}
	if r.PropertyID == nil {
		d.PropertyName = nil
	}
	if r.RoomID == nil {
		d.RoomName = nil
	}
	if withMoney && r.PurchaseDate != nil {
		d.PurchaseDate = dateOnly(*r.PurchaseDate)
	}
	if r.WarrantyExpiry != nil {
		w := *r.WarrantyExpiry
		d.WarrantyExpiry, d.WarrantyWeekday = dateOnly(w), weekday(w)
		days := int(math.Round(w.Sub(today).Hours() / 24))
		active := days >= 0
		d.WarrantyDaysLeft, d.WarrantyActive = &days, &active
	}
	return d
}

// where is a short phrase for summaries: "in the Kitchen at Maple St", "at
// Maple St", or "" when the asset is not tied to a place.
func (d assetData) where() string {
	switch {
	case d.RoomName != nil && d.PropertyName != nil:
		return fmt.Sprintf(" in the %s at %s", *d.RoomName, *d.PropertyName)
	case d.RoomName != nil:
		return " in the " + *d.RoomName
	case d.PropertyName != nil:
		return " at " + *d.PropertyName
	}
	return ""
}

// describe is a short phrase for summaries, e.g. "Bosch SHX78 (appliance)
// in the Kitchen at Maple St, warranty until Thu Dec 3, 2026".
func (d assetData) describe() string {
	s := d.Name
	if maker := strings.TrimSpace(deref(d.Manufacturer) + " " + deref(d.Model)); maker != "" {
		s += " — " + maker
	}
	if d.Category != nil {
		s += " (" + *d.Category + ")"
	}
	s += d.where()
	if d.WarrantyExpiry != nil {
		if t, err := time.Parse(time.DateOnly, *d.WarrantyExpiry); err == nil {
			if *d.WarrantyActive {
				s += ", warranty until " + t.Format("Mon Jan 2, 2006")
			} else {
				s += ", warranty expired " + t.Format("Mon Jan 2, 2006")
			}
		}
	}
	return s
}

// householdToday returns today's date in the household's timezone as
// midnight UTC, so it compares with dates scanned from DATE columns. An
// empty or unknown timezone falls back to UTC.
func householdToday(ctx context.Context, q querier, householdID string) (time.Time, error) {
	var tz string
	if err := q.QueryRow(ctx, `SELECT COALESCE(timezone, '') FROM households WHERE id = $1`, householdID).Scan(&tz); err != nil {
		return time.Time{}, fmt.Errorf("load household timezone: %w", err)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	y, m, d := time.Now().In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC), nil
}

// indexAsset upserts the asset into the Typesense household_search
// collection. Failures are logged, never returned: the tool call succeeds
// regardless of indexing. A nil indexer (Typesense not configured) skips it.
func indexAsset(idx searchIndexer, r assetRow) {
	if idx == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			slog.Warn("asset: panic during search indexing", "panic", p)
		}
	}()
	var body []string
	for _, s := range []*string{r.Category, r.Manufacturer, r.Model, r.SerialNumber, r.RoomName} {
		if s != nil && *s != "" {
			body = append(body, *s)
		}
	}
	doc := search.Document{
		ID:          "asset-" + r.ID.String(),
		HouseholdID: r.HouseholdID.String(),
		EntityType:  "asset",
		EntityID:    r.ID.String(),
		Title:       r.Name,
		Body:        strings.Join(body, " "),
		CreatedAt:   r.CreatedAt.Unix(),
		UpdatedAt:   r.UpdatedAt.Unix(),
		PropertyID:  uuidString(r.PropertyID),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := idx.IndexDocument(ctx, doc); err != nil {
		slog.Warn("asset: failed to index into search", "id", r.ID, "error", err)
	}
}

func dateOnly(t time.Time) *string {
	s := t.Format(time.DateOnly)
	return &s
}

func weekday(t time.Time) *string {
	s := t.Weekday().String()[:3]
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func uuidString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// notSignedIn is the result for a call without household claims.
func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}

// householdOf returns the caller's household id, or nil when the call has
// no household claims.
func householdOf(ctx context.Context) *string {
	c := server.ClaimsFromContext(ctx)
	if c == nil || c.HouseholdID == "" {
		return nil
	}
	return &c.HouseholdID
}

// assetRole resolves the caller's current role the way the property, vehicle,
// loan and people tools do: re-read from memberships for the claims' user and
// household, so a demotion hides money at once and a removed member gets
// nothing. Without a users.id (auth-disabled local development) the claims' role
// is used; an agent token always resolves to a users.id, so one that cannot be
// read fails closed. The handler has already checked for claims.
func assetRole(ctx context.Context, q querier, householdID string) (string, error) {
	c := server.ClaimsFromContext(ctx)
	if c == nil {
		return "", errNoClaims
	}
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.AgentTokenID != "" {
			return "", errNoClaims
		}
		return c.Role, nil // auth-disabled local mode: no users.id
	}
	var role string
	err = q.QueryRow(ctx,
		`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`, householdID, uid).Scan(&role)
	if err != nil {
		return "", err
	}
	return role, nil
}

// errNoClaims is the refusal assetRole reports when the claims cannot be
// resolved to a member; the handler answers respond.Failed("check your access").
var errNoClaims = errors.New("not signed in to a household")

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
// catalog names; a catalog name that is present wins.
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

// uuidFixes says where to get each id parameter.
var uuidFixes = map[string]string{
	"asset_id":    "Pass the asset's id (a UUID) from asset_find, not its name.",
	"id":          "Pass an asset's id (a UUID) from asset_find, or use query to search by name.",
	"property_id": "Pass the property's id (a UUID) from property_find, not its name.",
	"room_id":     "Pass the room's id (a UUID) from property_find id=<property_id>, not its name.",
}

// optionalUUID parses an optional UUID string argument.
func optionalUUID(args map[string]any, key string) (*uuid.UUID, error) {
	s, err := optionalString(args, key)
	if err != nil || s == nil {
		return nil, err
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		fix := uuidFixes[key]
		if fix == "" {
			fix = "Pass a UUID."
		}
		return nil, badArg(key, fmt.Sprintf("%s must be a UUID; got %q.", key, *s), fix)
	}
	return &id, nil
}

// number reads a numeric argument; ok is false when absent, null or blank.
// Numbers sent as numeric strings ("899.99") are accepted, because small
// models often quote them.
func number(args map[string]any, key string) (val float64, ok bool, err error) {
	v, present := args[key]
	if !present || v == nil {
		return 0, false, nil
	}
	notNumber := badArg(key, key+" must be a number.", fmt.Sprintf("Pass %s as a number without quotes or $.", key))
	switch n := v.(type) {
	case float64:
		val = n
	case int:
		val = float64(n)
	case int64:
		val = float64(n)
	case string:
		s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(n), "$"))
		if s == "" {
			return 0, false, nil
		}
		f, perr := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
		if perr != nil {
			return 0, false, notNumber
		}
		val = f
	default:
		return 0, false, notNumber
	}
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return 0, false, badArg(key, key+" must be a finite number.", fmt.Sprintf("Pass %s as a plain number.", key))
	}
	return val, true, nil
}

// wholeNumber reads an integer argument in [lo, hi].
func wholeNumber(args map[string]any, key string, lo, hi int) (int, bool, error) {
	n, ok, err := number(args, key)
	if err != nil || !ok {
		return 0, ok, err
	}
	if n != math.Trunc(n) || n < float64(lo) || n > float64(hi) {
		return 0, false, badArg(key, fmt.Sprintf("%s must be a whole number from %d to %d.", key, lo, hi),
			fmt.Sprintf("Pass %s as a whole number between %d and %d.", key, lo, hi))
	}
	return int(n), true, nil
}

// escapeLike escapes ILIKE wildcards so query matches literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
