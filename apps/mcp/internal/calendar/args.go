package calendar

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// ─────────────────────────────────────────────
// Annotations (KB architecture/mcp-tool-catalog.md §1)
// ─────────────────────────────────────────────

// hintClass is a catalog hint class: R (read), C (create), U (update,
// idempotent) or X (delete). Every tool sets all four hints explicitly:
// read-only agent tokens may only call tools with readOnlyHint true.
type hintClass int

const (
	classR hintClass = iota
	classC
	classU
	classX
)

// toolOptions prepends the title and the four annotation hints for class to
// opts, so every tool in this package declares them the same way.
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
// Times and dates
// ─────────────────────────────────────────────

// naiveLayouts are datetimes without an offset, which the tools refuse: the
// family timezone is not guessed for a full timestamp.
var naiveLayouts = []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"}

// isNaive reports whether s is a datetime without an offset.
func isNaive(s string, loc *time.Location) bool {
	for _, layout := range naiveLayouts {
		if _, err := time.ParseInLocation(layout, strings.TrimSpace(s), loc); err == nil {
			return true
		}
	}
	return false
}

// parseInstant reads an ISO 8601 datetime with an offset. A naive datetime
// gets an error whose fix and example add the family's offset for that
// local time; a bare date tells the model to use all_day.
func parseInstant(field, s string, loc *time.Location) (time.Time, *mcp.CallToolResult) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range naiveLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			fixed := t.Format(time.RFC3339)
			return time.Time{}, respond.Invalid(field,
				fmt.Sprintf("%s %q has no UTC offset.", field, s),
				fmt.Sprintf("Add the family's offset (timezone %s): %s.", loc.String(), fixed),
				map[string]any{field: fixed})
		}
	}
	if d, err := time.ParseInLocation(dateLayout, s, loc); err == nil {
		example := time.Date(d.Year(), d.Month(), d.Day(), 9, 0, 0, 0, loc).Format(time.RFC3339)
		return time.Time{}, respond.Invalid(field,
			fmt.Sprintf("%s %q is a date without a time.", field, s),
			"Pass a time with an offset, or set all_day:true to use plain dates.",
			map[string]any{field: example})
	}
	return time.Time{}, respond.Invalid(field,
		fmt.Sprintf("%s %q is not an ISO 8601 time.", field, s),
		"Pass the time as YYYY-MM-DDTHH:MM:SS with an offset.",
		map[string]any{field: exampleTime(loc)})
}

// exampleTime is tomorrow 09:00 in loc, for error examples.
func exampleTime(loc *time.Location) string {
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day()+1, 9, 0, 0, 0, loc).Format(time.RFC3339)
}

// exampleDate is today in loc as YYYY-MM-DD, for error examples.
func exampleDate(loc *time.Location) string { return time.Now().In(loc).Format(dateLayout) }

// parseDay reads a YYYY-MM-DD date as local midnight in loc.
func parseDay(field, s string, loc *time.Location) (time.Time, *mcp.CallToolResult) {
	d, err := time.ParseInLocation(dateLayout, strings.TrimSpace(s), loc)
	if err != nil {
		return time.Time{}, respond.Invalid(field,
			fmt.Sprintf("%s %q is not a date.", field, s),
			fmt.Sprintf("Pass %s as YYYY-MM-DD.", field),
			map[string]any{field: exampleDate(loc)})
	}
	return d, nil
}

// parseBound reads one end of a from/to range: a YYYY-MM-DD date (local
// midnight; an inclusive "to" date covers that whole day) or a datetime with
// an offset (exact; "to" is exclusive).
func parseBound(field, s string, isTo bool, loc *time.Location) (time.Time, *mcp.CallToolResult) {
	s = strings.TrimSpace(s)
	if d, err := time.ParseInLocation(dateLayout, s, loc); err == nil {
		if isTo {
			return d.AddDate(0, 0, 1), nil
		}
		return d, nil
	}
	t, bad := parseInstant(field, s, loc)
	if bad != nil && isNaive(s, loc) {
		return time.Time{}, bad // its fix adds the family's offset
	}
	if bad != nil {
		return time.Time{}, respond.Invalid(field,
			fmt.Sprintf("%s %q is not a date or a time with an offset.", field, s),
			fmt.Sprintf("Pass %s as YYYY-MM-DD, or as YYYY-MM-DDTHH:MM:SS with an offset.", field),
			map[string]any{field: exampleDate(loc)})
	}
	return t, nil
}

// todayIn is local midnight of today in loc.
func todayIn(loc *time.Location) time.Time {
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
}

// describeWhen is a short human time for summaries and hints, e.g.
// "Wed Nov 18, 9:00 AM–10:00 AM" or "Wed Nov 18 (all day)".
func describeWhen(ev Event, loc *time.Location) string {
	start, end, err := parseEventTime(ev)
	if err != nil {
		return ev.Start
	}
	start, end = start.In(loc), end.In(loc)
	if ev.AllDay {
		last := end.AddDate(0, 0, -1)
		if !last.After(start) {
			return start.Format("Mon Jan 2") + " (all day)"
		}
		return start.Format("Mon Jan 2") + "–" + last.Format("Mon Jan 2") + " (all day)"
	}
	if start.YearDay() == end.YearDay() && start.Year() == end.Year() {
		return start.Format("Mon Jan 2, 3:04 PM") + "–" + end.Format("3:04 PM")
	}
	return start.Format("Mon Jan 2, 3:04 PM") + " – " + end.Format("Mon Jan 2, 3:04 PM")
}

// ─────────────────────────────────────────────
// Ids, calendars and pagination
// ─────────────────────────────────────────────

// checkUUID validates an id argument, so a bad value becomes a corrective
// error rather than a SQL error.
func checkUUID(field, value, findTool string) *mcp.CallToolResult {
	if _, err := uuid.Parse(strings.TrimSpace(value)); err != nil {
		return respond.Invalid(field,
			fmt.Sprintf("%s %q is not an id.", field, value),
			fmt.Sprintf("Pass the id (a UUID) from %s.", findTool),
			map[string]any{field: "3f2b8c1e-7a4d-4e0b-9c2a-1d5e6f7a8b9c"})
	}
	return nil
}

// querier is satisfied by *pgxpool.Pool and pgx.Tx.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// resolveCalendar finds one of the household's calendars by id or by exact
// name (case-insensitive). An empty ref picks the family calendar (type
// family, preferring one named "Family"). Failures are corrective results:
// an unknown name lists the calendars there are, and an ambiguous name lists
// the candidates with their ids.
func resolveCalendar(ctx context.Context, q querier, householdID, field, ref string) (Calendar, *mcp.CallToolResult) {
	ref = strings.TrimSpace(ref)
	var sql string
	var args []any
	switch {
	case ref == "":
		sql = `SELECT id::text, name, type::text, color, property_id::text FROM calendars
		        WHERE household_id = $1 AND type = 'family'
		        ORDER BY (lower(name) = 'family') DESC, created_at LIMIT 1`
		args = []any{householdID}
	case isUUID(ref):
		sql = `SELECT id::text, name, type::text, color, property_id::text FROM calendars
		        WHERE household_id = $1 AND id = $2`
		args = []any{householdID, ref}
	default:
		sql = `SELECT id::text, name, type::text, color, property_id::text FROM calendars
		        WHERE household_id = $1 AND lower(name) = lower($2) ORDER BY created_at`
		args = []any{householdID, ref}
	}
	matches, err := scanCalendars(ctx, q, sql, args...)
	if err != nil {
		return Calendar{}, respond.Failed("look up the calendar", err)
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		if ref == "" {
			return Calendar{}, respond.Invalid(field, "The family has no Family calendar.",
				"Pass calendar_id with a calendar name or id from calendar_find.",
				map[string]any{field: "Kids"})
		}
		names := calendarNames(ctx, q, householdID)
		return Calendar{}, respond.Invalid(field,
			fmt.Sprintf("No calendar %q was found.", ref),
			fmt.Sprintf("Pass one of the family's calendars by name or id: %s.", names),
			map[string]any{field: "Family"})
	default:
		cands := make([]string, len(matches))
		for i, c := range matches {
			cands[i] = fmt.Sprintf("%s (%s)", c.Name, c.ID)
		}
		return Calendar{}, respond.Invalid(field,
			fmt.Sprintf("%d calendars are named %q.", len(matches), ref),
			"Pass calendar_id as one of these ids: "+strings.Join(cands, ", ")+".",
			map[string]any{field: matches[0].ID})
	}
}

// resolveCalendars resolves each ref in refs (names or ids) to a calendar id.
func resolveCalendars(ctx context.Context, q querier, householdID, field string, refs []string) ([]string, *mcp.CallToolResult) {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref) == "" {
			continue
		}
		cal, bad := resolveCalendar(ctx, q, householdID, field, ref)
		if bad != nil {
			return nil, bad
		}
		ids = append(ids, cal.ID)
	}
	return ids, nil
}

func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

func scanCalendars(ctx context.Context, q querier, sql string, args ...any) ([]Calendar, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Calendar, 0)
	for rows.Next() {
		var c Calendar
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.Color, &c.PropertyID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// calendarNames lists the household's calendar names for error fixes.
func calendarNames(ctx context.Context, q querier, householdID string) string {
	cals, err := scanCalendars(ctx, q, `SELECT id::text, name, type::text, color, property_id::text
		FROM calendars WHERE household_id = $1 ORDER BY name`, householdID)
	if err != nil || len(cals) == 0 {
		return "call calendar_find to list them"
	}
	names := make([]string, len(cals))
	for i, c := range cals {
		names[i] = fmt.Sprintf("%q", c.Name)
	}
	return strings.Join(names, ", ")
}

const (
	defaultLimit = 25
	maxLimit     = 100
	cursorPrefix = "evt:"
)

// pageArgs reads limit (default 25, 1–100) and the opaque cursor, which
// encodes the offset of the next page.
func pageArgs(args map[string]any) (limit, offset int, bad *mcp.CallToolResult) {
	f := getFloat64Arg(args, "limit", defaultLimit)
	if f != float64(int(f)) || f < 1 || f > maxLimit {
		return 0, 0, respond.Invalid("limit", fmt.Sprintf("limit %v must be a whole number from 1 to %d.", f, maxLimit),
			"Omit limit for 25, or pass a number up to 100.", map[string]any{"limit": defaultLimit})
	}
	c := strings.TrimSpace(getStringArg(args, "cursor"))
	if c == "" {
		return int(f), 0, nil
	}
	if off, err := decodeCursor(c); err == nil {
		return int(f), off, nil
	}
	return 0, 0, respond.Invalid("cursor", "cursor is not one this tool returned.",
		"Pass the cursor exactly as given in the previous result, or omit it for the first page.", nil)
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
