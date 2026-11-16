// Package notification provides the MCP notification tools (catalog v2, KB
// architecture/mcp-tool-catalog.md "notification"): notification_find, which
// replaces list_notifications and get_unread_count, and
// notification_mark_read. Every result is built with internal/respond, and
// every tool declares all four annotation hints and an outputSchema.
//
// Visibility: a notification belongs to a family (household_id) and is
// either family-wide (user_id NULL: bill_due, maintenance_due) or personal
// to one member (user_id set: calendar_alarm writes one row per member). An
// agent token acts as its member, so the caller sees the family-wide rows
// plus their own personal rows, never another member's personal ones.
//
// Read state lives in notifications.read_at, one value per row. Marking a
// family-wide notification read therefore marks it read for the whole
// family; the mark-read summary says so. Per-member read state for
// family-wide rows needs a schema change (a notification_reads table).
package notification

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// Pagination limits shared by every catalog find tool.
const (
	defaultLimit = 25
	maxLimit     = 100
)

// Notification scopes in output.
const (
	scopeFamily   = "family"   // user_id NULL: everyone in the family sees it
	scopePersonal = "personal" // user_id = the caller
)

// toolHandler is the signature every notification tool handler has.
type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// notificationData is one notification in output. IDs are strings (a
// uuid.UUID would be advertised as a byte array in the outputSchema).
type notificationData struct {
	ID        string  `json:"id" jsonschema_description:"The notification_id, for notification_mark_read notification_ids."`
	Type      string  `json:"type" jsonschema_description:"What kind, e.g. bill_due, maintenance_due, calendar_alarm."`
	Title     string  `json:"title"`
	Body      *string `json:"body"`
	Scope     string  `json:"scope" jsonschema_description:"family: everyone sees it. personal: only you."`
	Read      bool    `json:"read"`
	ReadAt    *string `json:"read_at" jsonschema_description:"When it was read, ISO 8601 in the family timezone; null if unread."`
	CreatedAt string  `json:"created_at" jsonschema_description:"When it was sent, ISO 8601 in the family timezone."`
	LocalDate string  `json:"local_date" jsonschema_description:"The day it was sent, YYYY-MM-DD in the family timezone."`
	Weekday   string  `json:"weekday" jsonschema_description:"Weekday it was sent, e.g. Thu."`
	AboutType *string `json:"about_type" jsonschema_description:"What it is about, e.g. bill or maintenance; null if nothing."`
	AboutID   *string `json:"about_id" jsonschema_description:"The id of what it is about; null if nothing."`
	Open      *string `json:"open" jsonschema_description:"The tool call that reads what it is about; null if none."`
}

// aboutKinds maps a notifications.entity_type to its catalog about_type and
// the call that opens it (empty when no tool reads it by id).
var aboutKinds = map[string]struct{ about, open string }{
	"bill":             {"bill", "bill_find id=%s"},
	"maintenance_task": {"maintenance", "maintenance_find id=%s"},
	"loan":             {"loan", "loan_find id=%s"},
	"asset":            {"asset", "asset_find id=%s"},
	"vehicle":          {"vehicle", "vehicle_find id=%s"},
	"pet":              {"pet", "pet_find id=%s"},
	"property":         {"property", "property_find id=%s"},
	"vendor":           {"contact", "contact_find id=%s"},
	"note":             {"note", "note_get note_id=%s"},
	"person":           {"person", "person_find id=%s"},
	// item_changed rows for a list item point at its list (migration 070:
	// list_item is not an entity kind); meal ratings point at the meal plan
	// and person preferences at the person.
	"list": {"list", "list_get list_id=%s"},
	// calendar_alarm reminders (worker, #4443) and item_changed rows for
	// events (migration 054) point at the calendar object. The alarm's
	// per-firing dedupe key lives in notifications.dedupe_key (056).
	"calendar_event": {"event", "event_find id=%s"},
}

// row is one notifications row as scanned.
type row struct {
	id         uuid.UUID
	typ        string
	title      string
	body       *string
	familyWide bool
	readAt     *time.Time
	createdAt  time.Time
	entityType *string
	entityID   *uuid.UUID
}

// data turns a row into output, with times in loc.
func (r row) data(loc *time.Location) notificationData {
	created := r.createdAt.In(loc)
	d := notificationData{
		ID:        r.id.String(),
		Type:      r.typ,
		Title:     r.title,
		Body:      r.body,
		Scope:     scopePersonal,
		Read:      r.readAt != nil,
		CreatedAt: created.Format(time.RFC3339),
		LocalDate: created.Format(time.DateOnly),
		Weekday:   created.Format("Mon"),
	}
	if r.familyWide {
		d.Scope = scopeFamily
	}
	if r.readAt != nil {
		s := r.readAt.In(loc).Format(time.RFC3339)
		d.ReadAt = &s
	}
	d.AboutType, d.AboutID, d.Open = about(r.entityType, r.entityID)
	return d
}

// about maps the entity reference to catalog words. Kinds outside the
// catalog map come back raw with no open hint; household is the family
// itself (no id).
func about(entityType *string, entityID *uuid.UUID) (aboutType, aboutID, open *string) {
	if entityType == nil || *entityType == "" {
		return nil, nil, nil
	}
	if *entityType == "household" {
		f := "family"
		return &f, nil, nil
	}
	kind := *entityType
	var o string
	if k, ok := aboutKinds[kind]; ok {
		kind = k.about
		if entityID != nil {
			o = fmt.Sprintf(k.open, entityID.String())
		}
	}
	aboutType = &kind
	if entityID != nil {
		id := entityID.String()
		aboutID = &id
	}
	if o != "" {
		open = &o
	}
	return aboutType, aboutID, open
}

// callerUser returns the caller's user id as a UUID, or nil when the token
// carries none (local dev mode uses "local-dev"): such a caller sees only
// family-wide notifications.
func callerUser(userID string) *uuid.UUID {
	id, err := uuid.Parse(userID)
	if err != nil {
		return nil
	}
	return &id
}

// querier is the subset of pgxpool.Pool and pgx.Tx the queries use.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// counts returns the caller's unread and total visible notifications; role
// is their current role (callerRole), so the counts match what find lists.
func counts(ctx context.Context, q querier, householdID string, userID *uuid.UUID, role string) (unread, total int, err error) {
	err = q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE n.read_at IS NULL), count(*)
		FROM notifications n WHERE `+visibleSQL, householdID, userID, role).Scan(&unread, &total)
	return unread, total, err
}

// UnreadCount returns how many notifications the member userID has unread:
// family-wide ones plus their own personal ones, limited to what their
// current role may see (visibility.go). family_overview's
// unread_notifications (#4385) uses it so both tools agree.
func UnreadCount(ctx context.Context, q querier, householdID, userID string) (int, error) {
	uid := callerUser(userID)
	role, err := callerRole(ctx, q, householdID, uid)
	if err != nil {
		return 0, err
	}
	unread, _, err := counts(ctx, q, householdID, uid, role)
	return unread, err
}

// loadLocation returns the family's timezone, or UTC when it is empty or
// invalid (the same fallback as the calendar tools).
func loadLocation(ctx context.Context, q querier, householdID string) (*time.Location, error) {
	var tz *string
	if err := q.QueryRow(ctx, `SELECT timezone FROM households WHERE id = $1`, householdID).Scan(&tz); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.UTC, nil
		}
		return nil, err
	}
	if tz == nil || *tz == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(*tz)
	if err != nil {
		return time.UTC, nil
	}
	return loc, nil
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

// annotations sets all four hints (catalog §1): class R (find) or U
// (update). openWorldHint is always false.
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

// optionalBool reads a boolean argument; absent, null or "" is def. The
// strings "true"/"false" (any case) are accepted, because small models often
// quote them.
func optionalBool(args map[string]any, key string, def bool) (bool, error) {
	v, present := args[key]
	if !present || v == nil {
		return def, nil
	}
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "":
			return def, nil
		case "true", "yes", "1":
			return true, nil
		case "false", "no", "0":
			return false, nil
		}
	}
	return def, badArg(key, fmt.Sprintf("%s must be true or false.", key),
		fmt.Sprintf("Pass %s as true or false, without quotes.", key))
}

// wholeNumber reads an integer argument in [lo, hi]; ok is false when it is
// absent or null. Numbers sent as numeric strings are accepted.
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
