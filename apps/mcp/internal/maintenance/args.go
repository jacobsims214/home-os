package maintenance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// ─────────────────────────────────────────────
// Annotations (KB architecture/mcp-tool-catalog.md §1)
// ─────────────────────────────────────────────

// hintClass is a catalog hint class: R (read), C (create), U (update,
// idempotent) or X (delete).
type hintClass int

const (
	classR hintClass = iota
	classC
	classU
	classX
)

// toolOptions prepends the title and all four annotation hints for class to
// opts. Read-only agent tokens may only call tools with readOnlyHint true, so
// every tool sets them explicitly.
func toolOptions(class hintClass, title string, opts ...mcp.ToolOption) []mcp.ToolOption {
	readOnly, destructive, idempotent := false, false, false
	switch class {
	case classR:
		readOnly, idempotent = true, true
	case classU:
		idempotent = true
	case classX:
		destructive, idempotent = true, true
	}
	return append([]mcp.ToolOption{
		mcp.WithTitleAnnotation(title),
		mcp.WithReadOnlyHintAnnotation(readOnly),
		mcp.WithDestructiveHintAnnotation(destructive),
		mcp.WithIdempotentHintAnnotation(idempotent),
		mcp.WithOpenWorldHintAnnotation(false),
	}, opts...)
}

// authRequired is returned when the request carries no claims.
func authRequired() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "Authentication required.",
		Fix:     "Reconnect with a valid Home OS token, then retry.",
	})
}

// ─────────────────────────────────────────────
// Limits and examples
// ─────────────────────────────────────────────

const (
	defaultLimit     = 25
	maxLimit         = 100
	maxDueWithinDays = 366
	// maxNameRunes is maintenance_tasks.name VARCHAR(255).
	maxNameRunes = 255
	// duplicateWindow is the §1 loop guard window for maintenance_create.
	duplicateWindow = 10 * time.Minute
	cursorPrefix    = "maintenance:"
	exampleTaskID   = "7c1e2a9b-4d3f-4a8e-b6c5-2f9d0e1a3b4c"
	exampleUUID     = "3f2b8c1e-5d4a-4b7e-9c2a-1e6f0a9b8d7c"
)

// ─────────────────────────────────────────────
// Argument readers. Small models send "" for optional params they don't
// use, so an empty string always means "not given".
// ─────────────────────────────────────────────

// str returns args[key] trimmed, or "" when absent or not a string.
func str(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

// firstStr returns the first non-empty string among keys (hidden aliases).
func firstStr(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(args, k); s != "" {
			return s
		}
	}
	return ""
}

// errNotNumber means the argument is present but not a number.
var errNotNumber = errors.New("not a number")

// number reads a numeric argument sent as a JSON number or a numeric
// string. ok is false when the argument is absent or "".
func number(args map[string]any, key string) (v float64, ok bool, err error) {
	switch x := args[key].(type) {
	case nil:
		return 0, false, nil
	case float64:
		return x, true, nil
	case int:
		return float64(x), true, nil
	case int64:
		return float64(x), true, nil
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return 0, true, errNotNumber
		}
		return f, true, nil
	case string:
		s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(x), "$"))
		if s == "" {
			return 0, false, nil
		}
		f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
		if err != nil {
			return 0, true, errNotNumber
		}
		return f, true, nil
	default:
		return 0, true, errNotNumber
	}
}

// wholeNumber reads an optional whole number within [lo, hi].
func wholeNumber(args map[string]any, key string, lo, hi int, example map[string]any) (int, bool, *mcp.CallToolResult) {
	f, ok, err := number(args, key)
	if !ok {
		return 0, false, nil
	}
	if err != nil || math.IsNaN(f) || f != math.Trunc(f) || f < float64(lo) || f > float64(hi) {
		return 0, false, respond.Invalid(key,
			fmt.Sprintf("%s must be a whole number from %d to %d.", key, lo, hi),
			fmt.Sprintf("Pass %s as a number, e.g. %v, or leave it out.", key, example[key]), example)
	}
	return int(f), true, nil
}

// money reads an optional non-negative amount, rounded to cents.
func money(args map[string]any, key string, example map[string]any) (*float64, *mcp.CallToolResult) {
	f, ok, err := number(args, key)
	if !ok {
		return nil, nil
	}
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1e10 {
		return nil, respond.Invalid(key, fmt.Sprintf("%s must be a number of 0 or more.", key),
			fmt.Sprintf("Pass %s as a plain number in the family's currency, e.g. 89.50.", key), example)
	}
	v := math.Round(f*100) / 100
	return &v, nil
}

// pageArgs reads limit and cursor.
func pageArgs(args map[string]any) (limit, offset int, bad *mcp.CallToolResult) {
	limit, ok, bad := wholeNumber(args, "limit", 1, maxLimit, map[string]any{"limit": defaultLimit})
	if bad != nil {
		return 0, 0, bad
	}
	if !ok {
		limit = defaultLimit
	}
	c := str(args, "cursor")
	if c == "" {
		return limit, 0, nil
	}
	off, err := decodeCursor(c)
	if err != nil {
		return 0, 0, respond.Invalid("cursor", "cursor is not one this tool returned.",
			"Pass the cursor exactly as given in the previous result, or omit it for the first page.", nil)
	}
	return limit, off, nil
}

func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(cursorPrefix + strconv.Itoa(offset)))
}

func decodeCursor(c string) (int, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, err
	}
	s, ok := strings.CutPrefix(string(b), cursorPrefix)
	if !ok {
		return 0, errors.New("bad cursor")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, errors.New("bad cursor")
	}
	return n, nil
}

// parseID checks that value is a UUID; findTool is where a valid id comes from.
func parseID(field, value, findTool string) (uuid.UUID, *mcp.CallToolResult) {
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, respond.Invalid(field,
			fmt.Sprintf("%s %q is not an id.", field, value),
			fmt.Sprintf("Pass the id string from %s.", findTool),
			map[string]any{field: exampleUUID})
	}
	return id, nil
}

// taskName trims and checks a task name.
func taskName(raw string, example map[string]any) (string, *mcp.CallToolResult) {
	name := strings.Join(strings.Fields(raw), " ")
	if name == "" {
		return "", respond.Invalid("name", "name is required.",
			"Pass a short name for the job, e.g. \"Replace furnace filter\".", example)
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return "", respond.Invalid("name", fmt.Sprintf("name is longer than %d characters.", maxNameRunes),
			"Shorten the name and put details in description.", example)
	}
	return name, nil
}

// ─────────────────────────────────────────────
// Dates. Dates are YYYY-MM-DD in the family's timezone and are handled as
// civil dates (midnight UTC), the way pgx scans a DATE column.
// ─────────────────────────────────────────────

// civil returns t's calendar date as midnight UTC.
func civil(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// todayIn is today's date in loc, as a civil date.
func todayIn(loc *time.Location) time.Time { return civil(time.Now().In(loc)) }

// parseDate reads a YYYY-MM-DD date argument.
func parseDate(field, raw string, loc *time.Location) (time.Time, *mcp.CallToolResult) {
	d, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		// Accept a full timestamp by taking its family-local date.
		if t, terr := time.Parse(time.RFC3339, raw); terr == nil {
			return civil(t.In(loc)), nil
		}
		return time.Time{}, respond.Invalid(field,
			fmt.Sprintf("%s %q is not a date.", field, raw),
			fmt.Sprintf("Pass %s as YYYY-MM-DD in the family's timezone (%s).", field, loc.String()),
			map[string]any{field: todayIn(loc).AddDate(0, 0, 7).Format(time.DateOnly)})
	}
	return d, nil
}

// shortWeekday is "Thu" for a Thursday.
func shortWeekday(t time.Time) string { return t.Weekday().String()[:3] }

// describeDate is "Thu 2026-11-20".
func describeDate(t time.Time) string { return shortWeekday(t) + " " + t.Format(time.DateOnly) }

// daysBetween returns whole days from a to b (civil dates).
func daysBetween(a, b time.Time) int { return int(math.Round(b.Sub(a).Hours() / 24)) }

func ptr[T any](v T) *T { return &v }

// ─────────────────────────────────────────────
// Subjects (domain model v2, KB architecture/domain-model-v2-triggers.md):
// a task is about at most one thing. A vehicle excludes property and asset;
// property_id is derived from asset_id by the derive_subject_property
// trigger, so an asset task is written with property_id NULL.
// ─────────────────────────────────────────────

// subject is a task's typed subject.
type subject struct {
	PropertyID *uuid.UUID
	AssetID    *uuid.UUID
	VehicleID  *uuid.UUID
}

func (s subject) given() bool { return s.PropertyID != nil || s.AssetID != nil || s.VehicleID != nil }

// querier is satisfied by *pgxpool.Pool and pgx.Tx.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// readSubject parses and checks property_id / asset_id / vehicle_id against
// the household: each must exist in this family, a vehicle excludes the
// other two, and a property passed with an asset must be the asset's own.
// The returned subject has PropertyID nil when an asset is given, so the
// trigger derives it.
func readSubject(ctx context.Context, q querier, householdID string, args map[string]any) (subject, *mcp.CallToolResult) {
	var s subject
	type field struct {
		name, table, finder string
		dst                 **uuid.UUID
	}
	fields := []field{
		{"property_id", "properties", "property_find", &s.PropertyID},
		{"asset_id", "assets", "asset_find", &s.AssetID},
		{"vehicle_id", "vehicles", "vehicle_find", &s.VehicleID},
	}
	for _, f := range fields {
		raw := str(args, f.name)
		if raw == "" {
			continue
		}
		id, bad := parseID(f.name, raw, f.finder)
		if bad != nil {
			return subject{}, bad
		}
		*f.dst = &id
	}
	if s.VehicleID != nil && (s.PropertyID != nil || s.AssetID != nil) {
		return subject{}, respond.Invalid("vehicle_id",
			"A task is about one thing: vehicle_id can't be combined with property_id or asset_id.",
			"Pass vehicle_id alone for car jobs, or property_id/asset_id for the house.",
			map[string]any{"name": "Oil change", "due_date": "2026-12-01", "vehicle_id": exampleUUID})
	}
	for _, f := range fields {
		if *f.dst == nil {
			continue
		}
		var exists bool
		var assetProperty *uuid.UUID
		var err error
		if f.table == "assets" {
			err = q.QueryRow(ctx, `SELECT TRUE, property_id FROM assets WHERE id = $1 AND household_id = $2`,
				**f.dst, householdID).Scan(&exists, &assetProperty)
		} else {
			err = q.QueryRow(ctx, fmt.Sprintf(`SELECT TRUE FROM %s WHERE id = $1 AND household_id = $2`, f.table),
				**f.dst, householdID).Scan(&exists)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			thing := strings.TrimSuffix(f.name, "_id")
			return subject{}, respond.Invalid(f.name,
				fmt.Sprintf("No %s with id %s was found in this family.", thing, (**f.dst).String()),
				fmt.Sprintf("Call %s to get a valid %s.", f.finder, f.name), nil)
		}
		if err != nil {
			return subject{}, respond.Failed("check "+f.name, err)
		}
		if f.table == "assets" && s.PropertyID != nil {
			if assetProperty == nil || *assetProperty != *s.PropertyID {
				return subject{}, respond.Invalid("property_id",
					"property_id is not where that asset is; the task's property comes from the asset.",
					"Pass asset_id alone (its property is filled in), or property_id alone.",
					map[string]any{"name": "Replace filter", "due_date": "2026-12-01", "asset_id": (*s.AssetID).String()})
			}
		}
	}
	if s.AssetID != nil {
		s.PropertyID = nil
	}
	return s, nil
}

// readContact parses contact_id (alias vendor_id) and checks it is a
// vendor of this family.
func readContact(ctx context.Context, q querier, householdID string, args map[string]any) (*uuid.UUID, *mcp.CallToolResult) {
	raw := firstStr(args, "contact_id", "vendor_id")
	if raw == "" {
		return nil, nil
	}
	id, bad := parseID("contact_id", raw, "contact_find")
	if bad != nil {
		return nil, bad
	}
	var exists bool
	err := q.QueryRow(ctx, `SELECT TRUE FROM vendors WHERE id = $1 AND household_id = $2`, id, householdID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, respond.Invalid("contact_id",
			fmt.Sprintf("No contact with id %s was found in this family.", id),
			"Call contact_find to get a valid contact_id, or leave it out.", nil)
	}
	if err != nil {
		return nil, respond.Failed("check contact_id", err)
	}
	return &id, nil
}
