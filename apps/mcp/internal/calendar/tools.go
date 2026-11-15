// Package calendar provides MCP tools for calendar operations.
// Each tool function accepts a *pgxpool.Pool and returns a tool definition
// plus handler function. Handlers extract JWT claims from context to scope
// all queries by household_id.
package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/bill"
	"home-os/mcp/internal/server"
)

// Calendar represents a calendar collection returned by list_calendars.
type Calendar struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Color      *string `json:"color,omitempty"`
	PropertyID *string `json:"property_id,omitempty"`
}

// Event represents a calendar event parsed from calendar_objects + ical_data JSON.
type Event struct {
	ID           string `json:"id"`
	CalendarID   string `json:"calendar_id"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Start        string `json:"start"`
	End          string `json:"end"`
	AllDay       bool   `json:"all_day"`
	Location     string `json:"location"`
	Color        string `json:"color"`
	EventType    string `json:"event_type"`
	CalendarName string `json:"calendar_name,omitempty"`
	// LocalDate (YYYY-MM-DD) and Weekday ("Wednesday") are the event's
	// start day in the household timezone, computed so callers never have
	// to derive the weekday themselves. Empty when start is unreadable.
	LocalDate string `json:"local_date"`
	Weekday   string `json:"weekday"`
}

// eventJSON is the JSON structure stored in ical_data column.
type eventJSON struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Start       string `json:"start"`
	End         string `json:"end"`
	AllDay      bool   `json:"all_day"`
	Location    string `json:"location"`
	Color       string `json:"color"`
	EventType   string `json:"event_type"`
}

// AvailableSlot represents a free time slot returned by find_available_slots.
// Date is the local calendar day (YYYY-MM-DD) the slot falls on; Start and
// End are RFC 3339 in the household's offset.
type AvailableSlot struct {
	Date  string `json:"date"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// ConflictEvent represents a conflicting event returned by check_conflicts.
type ConflictEvent struct {
	ID           string `json:"id"`
	CalendarID   string `json:"calendar_id"`
	Title        string `json:"title"`
	Start        string `json:"start"`
	End          string `json:"end"`
	CalendarName string `json:"calendar_name,omitempty"`
	LocalDate    string `json:"local_date"`
	Weekday      string `json:"weekday"`
}

// createEventResponse is create_event's result: the new event's id and the
// existing events it overlaps (never null; empty when there are none).
type createEventResponse struct {
	ID             string          `json:"id"`
	Conflicts      []ConflictEvent `json:"conflicts"`
	ConflictsError string          `json:"conflicts_error,omitempty"`
}

// ─────────────────────────────────────────────
// Helper: extract a string param from the tool request
// ─────────────────────────────────────────────

func getStringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func getBoolArg(args map[string]any, key string, defaultVal bool) bool {
	if args == nil {
		return defaultVal
	}
	v, ok := args[key]
	if !ok || v == nil {
		return defaultVal
	}
	b, ok := v.(bool)
	if !ok {
		return defaultVal
	}
	return b
}

func getFloat64Arg(args map[string]any, key string, defaultVal float64) float64 {
	if args == nil {
		return defaultVal
	}
	v, ok := args[key]
	if !ok || v == nil {
		return defaultVal
	}
	f, ok := v.(float64)
	if !ok {
		return defaultVal
	}
	return f
}

func getStringSliceArg(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// ─────────────────────────────────────────────
// Helper: parse event from calendar_objects row
// ─────────────────────────────────────────────

// eventRow holds the raw columns from calendar_objects joined with calendars.
type eventRow struct {
	ID           string
	CalendarID   string
	IcalData     string
	EventType    string
	CalendarName string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// parseEventFromRow decodes a calendar_objects row. All-day events come back
// as household-local midnights (see localizeAllDay).
func parseEventFromRow(row eventRow, loc *time.Location) (*Event, error) {
	var ej eventJSON
	if err := json.Unmarshal([]byte(row.IcalData), &ej); err != nil {
		return nil, fmt.Errorf("parse ical_data: %w", err)
	}
	ev := &Event{
		ID:           row.ID,
		CalendarID:   row.CalendarID,
		Title:        ej.Title,
		Description:  ej.Description,
		Start:        ej.Start,
		End:          ej.End,
		AllDay:       ej.AllDay,
		Location:     ej.Location,
		Color:        ej.Color,
		EventType:    row.EventType,
		CalendarName: row.CalendarName,
	}
	localizeAllDay(ev, loc)
	setLocalDay(ev, loc)
	return ev, nil
}

// ─────────────────────────────────────────────
// Helper: marshal an event's "editable fields" into ical_data JSON
// ─────────────────────────────────────────────

func marshalEventFields(title, description, start, end, location string, allDay bool) (string, error) {
	ej := eventJSON{
		Title:       title,
		Description: description,
		Start:       start,
		End:         end,
		AllDay:      allDay,
		Location:    location,
	}
	b, err := json.Marshal(ej)
	if err != nil {
		return "", fmt.Errorf("marshal event: %w", err)
	}
	return string(b), nil
}

// mergeEventFields merges the provided (non-nil) fields into an existing
// ical_data JSON document. It works on the raw key/value map rather than
// eventJSON so keys this package does not model survive the round-trip —
// the CalDAV service persists `sequence` there, and dropping it would make
// Apple Calendar miss revisions. When `sequence` is present it is
// incremented, as the CalDAV PUT handler does (RFC 5545 §3.8.7.4).
func mergeEventFields(existingIcalData string, title, description, start, end, location *string, allDay *bool) (string, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(existingIcalData), &doc); err != nil {
		return "", fmt.Errorf("parse existing ical_data: %w", err)
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	set := func(key string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", key, err)
		}
		doc[key] = b
		return nil
	}
	for _, f := range []struct {
		key string
		val *string
	}{
		{"title", title},
		{"description", description},
		{"start", start},
		{"end", end},
		{"location", location},
	} {
		if f.val != nil {
			if err := set(f.key, *f.val); err != nil {
				return "", err
			}
		}
	}
	if allDay != nil {
		if err := set("all_day", *allDay); err != nil {
			return "", err
		}
	}
	if raw, ok := doc["sequence"]; ok {
		var seq int
		if err := json.Unmarshal(raw, &seq); err == nil {
			if err := set("sequence", seq+1); err != nil {
				return "", err
			}
		}
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshal merged event: %w", err)
	}
	return string(b), nil
}

// resolveUpdatedTimes works out an event's new start and end from its stored
// times and the optional new values passed to update_event:
//
//   - both given: stored as given;
//   - only start given: end moves with it, keeping the original duration;
//   - only end given: start is unchanged;
//   - neither: unchanged.
//
// The result must have end after start. Times are returned in UTC; callers
// store them as RFC 3339, the convention the API and CalDAV service share.
func resolveUpdatedTimes(oldStart, oldEnd string, newStart, newEnd *string) (time.Time, time.Time, error) {
	var start, end time.Time
	var err error

	if newStart != nil {
		if start, err = time.Parse(time.RFC3339, *newStart); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start_time format (use ISO 8601 with an offset, e.g. 2026-08-03T14:00:00-05:00): %w", err)
		}
	}
	if newEnd != nil {
		if end, err = time.Parse(time.RFC3339, *newEnd); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end_time format (use ISO 8601 with an offset, e.g. 2026-08-03T15:00:00-05:00): %w", err)
		}
	}

	if newStart == nil || newEnd == nil {
		storedStart, errS := time.Parse(time.RFC3339, oldStart)
		storedEnd, errE := time.Parse(time.RFC3339, oldEnd)
		switch {
		case newStart == nil && newEnd == nil:
			if errS != nil || errE != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("stored event has unreadable start/end; pass start_time and end_time")
			}
			start, end = storedStart, storedEnd
		case newStart == nil: // end only
			if errS != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("stored event has an unreadable start; pass start_time too")
			}
			start = storedStart
		default: // start only: keep the original duration
			if errS != nil || errE != nil || !storedEnd.After(storedStart) {
				return time.Time{}, time.Time{}, fmt.Errorf("cannot keep the event's duration (stored start/end unreadable); pass end_time too")
			}
			end = start.Add(storedEnd.Sub(storedStart))
		}
	}

	if !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("end_time must be after start_time")
	}
	return start.UTC(), end.UTC(), nil
}

// resolveUpdatedAllDay works out the stored start/end of an event that is
// all-day after update_event, from its stored times and the optional new
// values:
//
//   - end given: the new (or stored) start through that end;
//   - only start given: the event moves, keeping its number of days;
//   - neither: the stored times snapped to whole days (used when all_day is
//     switched on for a timed event, or to repair a non-normalised one).
//
// A timed event's stored UTC instants are read in the household timezone
// first, so 9pm on 11/18 in New York (02:00Z on 11/19) becomes 11/18.
func resolveUpdatedAllDay(existing eventJSON, loc *time.Location, newStart, newEnd *string) (string, string, error) {
	if !existing.AllDay {
		for _, p := range []*string{&existing.Start, &existing.End} {
			if t, err := time.Parse(time.RFC3339, *p); err == nil {
				*p = t.In(loc).Format(time.RFC3339)
			}
		}
	}
	start := existing.Start
	if newStart != nil {
		start = *newStart
	}
	var first, endExcl time.Time
	var err error
	switch {
	case newEnd != nil:
		first, endExcl, err = allDayRange(start, *newEnd)
	case newStart != nil:
		days := 1
		if oldFirst, oldEndExcl, oerr := allDayRange(existing.Start, existing.End); oerr == nil {
			days = int(oldEndExcl.Sub(oldFirst).Hours()/24 + 0.5)
		}
		if first, _, err = civilDate(start); err != nil {
			err = fmt.Errorf("invalid start_time: %w", err)
		} else {
			endExcl = first.AddDate(0, 0, days)
		}
	default:
		if first, endExcl, err = allDayRange(existing.Start, existing.End); err != nil {
			err = fmt.Errorf("stored event has unreadable start/end; pass start_time and end_time")
		}
	}
	if err != nil {
		return "", "", err
	}
	s, e := storeAllDay(first, endExcl)
	return s, e, nil
}

// ─────────────────────────────────────────────
// Helper: verify a calendar belongs to the household
// ─────────────────────────────────────────────

func verifyCalendarOwnership(ctx context.Context, pool *pgxpool.Pool, calendarID, householdID string) error {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM calendars WHERE id = $1 AND household_id = $2)`,
		calendarID, householdID,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("verify calendar: %w", err)
	}
	if !exists {
		return fmt.Errorf("calendar not found")
	}
	return nil
}

// ─────────────────────────────────────────────
// Helper: get calendar_id for an event and verify household ownership
// ─────────────────────────────────────────────

func getEventCalendarID(ctx context.Context, pool *pgxpool.Pool, eventID string) (string, error) {
	var calendarID string
	err := pool.QueryRow(ctx,
		`SELECT calendar_id FROM calendar_objects WHERE id = $1`,
		eventID,
	).Scan(&calendarID)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("event not found")
	}
	if err != nil {
		return "", fmt.Errorf("get event calendar: %w", err)
	}
	return calendarID, nil
}

// ─────────────────────────────────────────────
// Helper: record a calendar_changes entry and bump ctag in a transaction
// ─────────────────────────────────────────────

func recordChangeAndBumpCtag(ctx context.Context, tx pgx.Tx, calendarID, eventUID, action string) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO calendar_changes (calendar_id, event_uid, action) VALUES ($1, $2, $3)`,
		calendarID, eventUID, action,
	); err != nil {
		return fmt.Errorf("record change: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE calendars SET ctag = gen_random_uuid()::text, updated_at = NOW() WHERE id = $1`,
		calendarID,
	); err != nil {
		return fmt.Errorf("bump ctag: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────
// Helper: build a time.Time from a YYYY-MM-DD string at the start of day
// in the given location (e.g. the household's configured timezone).
// ─────────────────────────────────────────────

func parseDate(dateStr string, loc *time.Location) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", dateStr, loc)
}

// ─────────────────────────────────────────────
// Helper: load the household's configured timezone from the database.
// Returns UTC as a safe fallback (with a warning log) if the timezone
// column is empty or not a valid IANA timezone name.
// ─────────────────────────────────────────────

func loadHouseholdLocation(ctx context.Context, pool *pgxpool.Pool, householdID string) *time.Location {
	var tz string
	err := pool.QueryRow(ctx,
		`SELECT timezone FROM households WHERE id = $1`, householdID,
	).Scan(&tz)
	if err != nil {
		slog.Warn("loadHouseholdLocation: query failed, falling back to UTC", "household_id", householdID, "error", err)
		return time.UTC
	}
	if tz == "" || tz == "UTC" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		slog.Warn("loadHouseholdLocation: invalid timezone, falling back to UTC", "household_id", householdID, "timezone", tz, "error", err)
		return time.UTC
	}
	return loc
}

// ─────────────────────────────────────────────
// Helper: query events for a household, optionally filtered by calendar IDs
// and date range. Date filtering is done in SQL via JSONB extraction of
// ical_data.start/end timestamps. Callers may still use eventOverlapsRange
// as a correctness backstop.
// ─────────────────────────────────────────────

func queryEvents(ctx context.Context, pool *pgxpool.Pool, householdID string, loc *time.Location, calendarIDs []string, startDate, endDate time.Time) ([]Event, error) {
	// Base query: join calendar_objects with calendars for household scoping
	query := `SELECT co.id, co.calendar_id, co.ical_data, co.event_type, c.name AS calendar_name, co.created_at, co.updated_at
		FROM calendar_objects co
		JOIN calendars c ON c.id = co.calendar_id
		WHERE c.household_id = $1`
	args := []any{householdID}
	argIdx := 2

	// If specific calendar IDs are provided, filter by them
	if len(calendarIDs) > 0 {
		placeholders := make([]string, len(calendarIDs))
		for i, calID := range calendarIDs {
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			args = append(args, calID)
			argIdx++
		}
		query += fmt.Sprintf(" AND co.calendar_id IN (%s)", strings.Join(placeholders, ","))
	}

	// Filter by date range using JSONB extraction from ical_data.
	// Overlap condition: event start < range end AND event end > range start.
	// This narrows the result set in SQL; callers keep Go-side filtering as
	// the exact check. The window is widened by allDaySlack because all-day
	// events are stored as UTC midnights but compared as local days.
	if !startDate.IsZero() && !endDate.IsZero() {
		query += fmt.Sprintf(" AND (co.ical_data::jsonb->>'start')::timestamptz < $%d", argIdx)
		args = append(args, endDate.Add(allDaySlack))
		argIdx++
		query += fmt.Sprintf(" AND (co.ical_data::jsonb->>'end')::timestamptz > $%d", argIdx)
		args = append(args, startDate.Add(-allDaySlack))
		argIdx++
	}

	query += " ORDER BY co.created_at"

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var row eventRow
		if err := rows.Scan(&row.ID, &row.CalendarID, &row.IcalData, &row.EventType, &row.CalendarName, &row.CreatedAt, &row.UpdatedAt); err != nil {
			slog.Warn("calendar: query events: scan error", "error", err)
			continue
		}
		ev, err := parseEventFromRow(row, loc)
		if err != nil {
			slog.Warn("calendar: query events: parse error", "event_id", row.ID, "error", err)
			continue
		}
		events = append(events, *ev)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}

	return events, nil
}

// findConflicts returns the household's events (optionally limited to
// calendarIDs) whose [start, end) overlaps [start, end), sorted by start
// instant. excludeID skips one event (create_event passes the event it just
// made). It is shared by check_conflicts and create_event so both agree on
// what a conflict is. All-day events come back from queryEvents as
// household-local days, so one on 11/18 conflicts with any range touching
// 11/18 household time and none on 11/19. Always returns a non-nil slice.
func findConflicts(ctx context.Context, pool *pgxpool.Pool, householdID string, loc *time.Location, calendarIDs []string, start, end time.Time, excludeID string) ([]ConflictEvent, error) {
	events, err := queryEvents(ctx, pool, householdID, loc, calendarIDs, start, end)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}

	conflicts := make([]ConflictEvent, 0)
	for _, ev := range events {
		if ev.ID == excludeID || !eventOverlapsRange(ev, start, end) {
			continue
		}
		conflicts = append(conflicts, ConflictEvent{
			ID:           ev.ID,
			CalendarID:   ev.CalendarID,
			Title:        ev.Title,
			Start:        ev.Start,
			End:          ev.End,
			CalendarName: ev.CalendarName,
			LocalDate:    ev.LocalDate,
			Weekday:      ev.Weekday,
		})
	}

	slices.SortStableFunc(conflicts, func(a, b ConflictEvent) int {
		switch {
		case startsBefore(a.Start, b.Start):
			return -1
		case startsBefore(b.Start, a.Start):
			return 1
		}
		return 0
	})
	return conflicts, nil
}

// ─────────────────────────────────────────────
// Helper: filter events that overlap a given date range (inclusive of boundaries)
// ─────────────────────────────────────────────

func eventOverlapsRange(ev Event, start, end time.Time) bool {
	evStart, err := time.Parse(time.RFC3339, ev.Start)
	if err != nil {
		return false
	}
	evEnd, err := time.Parse(time.RFC3339, ev.End)
	if err != nil {
		return false
	}
	// Overlap: event starts before the range ends AND event ends after the range starts
	return evStart.Before(end) && evEnd.After(start)
}

func parseEventTime(ev Event) (time.Time, time.Time, error) {
	start, err := time.Parse(time.RFC3339, ev.Start)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse start: %w", err)
	}
	end, err := time.Parse(time.RFC3339, ev.End)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse end: %w", err)
	}
	return start, end, nil
}

// ─────────────────────────────────────────────
// Helper: all-day events
// ─────────────────────────────────────────────
//
// All-day events are stored the way the CalDAV service stores
// DTSTART;VALUE=DATE: the calendar date at 00:00 UTC, with an exclusive end
// at 00:00 UTC of the day after the last day, e.g.
//
//	{"all_day":true,"start":"2026-11-18T00:00:00Z","end":"2026-11-19T00:00:00Z"}
//
// The CalDAV service writes VALUE=DATE from the date written in the stored
// string, so a phone shows the event on 11/18 whatever the household's
// timezone. Storing local midnight as an instant instead would shift the
// date for households east of UTC.
//
// The tools read the date as written (ignoring clock and offset) and treat
// it as a household-local day: [local 00:00, local 00:00 of the end date).

const dateLayout = "2006-01-02"

// civilDate reads the calendar date exactly as written in s, accepting
// RFC 3339, a naive local datetime or a bare YYYY-MM-DD. The offset, when
// present, is ignored for the date: "2026-11-18T00:00:00Z" is 11/18, not
// 11/17 evening in New York. midnight reports whether s spells out a
// 00:00:00 clock time (a bare date does not).
func civilDate(s string) (date time.Time, midnight bool, err error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04", dateLayout} {
		t, perr := time.Parse(layout, s)
		if perr != nil {
			continue
		}
		h, m, sec := t.Clock()
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC),
			layout != dateLayout && h == 0 && m == 0 && sec == 0 && t.Nanosecond() == 0, nil
	}
	return time.Time{}, false, fmt.Errorf("%q is not a date (YYYY-MM-DD) or ISO 8601 time", s)
}

// allDayRange normalises an all-day event's start and end to whole days.
// The returned dates are UTC midnights: first is the first day and
// endExcl the day after the last day.
//
// The end is exclusive when it is a 00:00 time after the start date, as in
// iCalendar and in what is stored ("2026-11-19T00:00:00Z"). Otherwise it
// names the last day: a bare date "2026-12-05" or any other clock time
// includes 12/5, so 09:00–23:59 on 11/18, or 11/18–11/18, is the single
// day 11/18. An empty end means one day.
func allDayRange(start, end string) (first, endExcl time.Time, err error) {
	first, _, err = civilDate(start)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start_time: %w", err)
	}
	if end == "" {
		return first, first.AddDate(0, 0, 1), nil
	}
	last, midnight, err := civilDate(end)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid end_time: %w", err)
	}
	if last.Before(first) {
		return time.Time{}, time.Time{}, fmt.Errorf("end_time must not be before start_time")
	}
	if midnight && last.After(first) {
		return first, last, nil
	}
	return first, last.AddDate(0, 0, 1), nil
}

// storeAllDay formats normalised all-day dates as stored in ical_data.
func storeAllDay(first, endExcl time.Time) (string, string) {
	return first.Format(time.RFC3339), endExcl.Format(time.RFC3339)
}

// localizeAllDay rewrites an all-day event's start/end as household-local
// midnights (e.g. 2026-11-18T00:00:00-05:00 to 2026-11-19T00:00:00-05:00)
// so date filtering and callers see the right day. Timed events and
// unreadable dates are left as stored.
func localizeAllDay(ev *Event, loc *time.Location) {
	if !ev.AllDay {
		return
	}
	first, endExcl, err := allDayRange(ev.Start, ev.End)
	if err != nil {
		return
	}
	ev.Start = time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, loc).Format(time.RFC3339)
	ev.End = time.Date(endExcl.Year(), endExcl.Month(), endExcl.Day(), 0, 0, 0, 0, loc).Format(time.RFC3339)
}

// setLocalDay fills ev.LocalDate and ev.Weekday from the start instant in
// the household timezone. Call it after localizeAllDay, so an all-day event's
// start is already the household-local midnight of its first day. A timed
// event at 23:30 New York (04:30Z the next day) is dated by the New York day.
func setLocalDay(ev *Event, loc *time.Location) {
	ev.LocalDate, ev.Weekday = "", ""
	start, err := time.Parse(time.RFC3339, ev.Start)
	if err != nil {
		return
	}
	local := start.In(loc)
	ev.LocalDate = local.Format("2006-01-02")
	ev.Weekday = local.Weekday().String()
}

// LoadHouseholdLocation returns the household's configured timezone, or UTC
// when it is empty, "UTC", invalid or unreadable. Exported for other event
// surfaces (search) so they resolve the zone exactly as the calendar tools.
func LoadHouseholdLocation(ctx context.Context, pool *pgxpool.Pool, householdID string) *time.Location {
	return loadHouseholdLocation(ctx, pool, householdID)
}

// LocalDay places a stored event start in the household timezone, using the
// same rules as the calendar tools: an all-day event is dated by the date as
// written in start and returned as local midnight of its first day; a timed
// event's instant is formatted in loc (RFC 3339 with loc's offset).
// localDate (YYYY-MM-DD) and weekday ("Wednesday") are the household-local
// day of that start. An unparseable start comes back unchanged with empty
// localDate and weekday.
func LocalDay(start, end string, allDay bool, loc *time.Location) (localStart, localDate, weekday string) {
	ev := &Event{Start: start, End: end, AllDay: allDay}
	localizeAllDay(ev, loc)
	setLocalDay(ev, loc)
	if ev.LocalDate == "" {
		return start, "", ""
	}
	if !allDay {
		if t, err := time.Parse(time.RFC3339, ev.Start); err == nil {
			ev.Start = t.In(loc).Format(time.RFC3339)
		}
	}
	return ev.Start, ev.LocalDate, ev.Weekday
}

// startsBefore orders RFC 3339 start times by instant. Stored times mix
// offsets (timed events in UTC, all-day events as local midnights), so a
// string comparison is wrong; unparseable values fall back to it.
func startsBefore(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	if errA != nil || errB != nil {
		return a < b
	}
	return ta.Before(tb)
}

// allDaySlack widens the SQL date window so all-day events, stored as UTC
// midnights but read as local days, are not cut off by the timestamptz
// comparison. UTC offsets reach ±14h; eventOverlapsRange does the exact check.
const allDaySlack = 48 * time.Hour

// ─────────────────────────────────────────────
// 1. list_calendars
// ─────────────────────────────────────────────

// NewListCalendarsTool creates the list_calendars tool and its handler.
func NewListCalendarsTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("list_calendars",
		mcp.WithDescription("List all calendars for the current household"),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		rows, err := pool.Query(ctx,
			`SELECT id, name, type, color, property_id
			 FROM calendars WHERE household_id = $1 ORDER BY name`,
			claims.HouseholdID,
		)
		if err != nil {
			slog.Error("list_calendars: query error", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("query error: %v", err)), nil
		}
		defer rows.Close()

		calendars := make([]Calendar, 0)
		for rows.Next() {
			var cal Calendar
			if err := rows.Scan(&cal.ID, &cal.Name, &cal.Type, &cal.Color, &cal.PropertyID); err != nil {
				slog.Error("list_calendars: scan error", "error", err)
				continue
			}
			calendars = append(calendars, cal)
		}

		if err := rows.Err(); err != nil {
			slog.Error("list_calendars: iterate error", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("iterate calendars: %v", err)), nil
		}

		data, err := json.Marshal(calendars)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}

	return tool, handler
}

// ─────────────────────────────────────────────
// 2. list_events
// ─────────────────────────────────────────────

// NewListEventsTool creates the list_events tool and its handler.
func NewListEventsTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("list_events",
		mcp.WithDescription("List events across calendars, optionally filtered by calendar, start date, and end date"),
		mcp.WithString("calendar_id", mcp.Description("Optional calendar ID to filter by")),
		mcp.WithString("start_date", mcp.Description("Start date (YYYY-MM-DD). Defaults to beginning of current month.")),
		mcp.WithString("end_date", mcp.Description("End date (YYYY-MM-DD). Defaults to start_date + 1 month.")),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)

		args := req.GetArguments()
		calendarID := getStringArg(args, "calendar_id")

		// Determine date range in the household's timezone
		now := time.Now().In(loc)
		startDateStr := getStringArg(args, "start_date")
		var startDate time.Time
		if startDateStr != "" {
			var err error
			startDate, err = parseDate(startDateStr, loc)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid start_date format (use YYYY-MM-DD): %v", err)), nil
			}
		} else {
			// Default to beginning of current month in household timezone
			startDate = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		}

		endDateStr := getStringArg(args, "end_date")
		var endDate time.Time
		if endDateStr != "" {
			var err error
			endDate, err = parseDate(endDateStr, loc)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid end_date format (use YYYY-MM-DD): %v", err)), nil
			}
			// end_date is inclusive: the range ends at the next local
			// midnight (AddDate, not +24h, so DST days are covered exactly).
			endDate = endDate.AddDate(0, 0, 1)
		} else {
			// Default to start_date + 1 month
			endDate = startDate.AddDate(0, 1, 0)
		}

		// Build query: start with household-scoped calendars
		query := `SELECT co.id, co.calendar_id, co.ical_data, co.event_type, c.name AS calendar_name, co.created_at, co.updated_at
			FROM calendar_objects co
			JOIN calendars c ON c.id = co.calendar_id
			WHERE c.household_id = $1`
		queryArgs := []any{claims.HouseholdID}

		if calendarID != "" {
			query += " AND co.calendar_id = $2"
			queryArgs = append(queryArgs, calendarID)
		}

		query += " ORDER BY co.created_at"

		rows, err := pool.Query(ctx, query, queryArgs...)
		if err != nil {
			slog.Error("list_events: query error", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("query error: %v", err)), nil
		}
		defer rows.Close()

		events := make([]Event, 0)
		for rows.Next() {
			var row eventRow
			if err := rows.Scan(&row.ID, &row.CalendarID, &row.IcalData, &row.EventType, &row.CalendarName, &row.CreatedAt, &row.UpdatedAt); err != nil {
				slog.Error("list_events: scan error", "error", err)
				continue
			}
			ev, err := parseEventFromRow(row, loc)
			if err != nil {
				slog.Warn("list_events: parse error", "event_id", row.ID, "error", err)
				continue
			}
			// Filter by date range in Go
			if eventOverlapsRange(*ev, startDate, endDate) {
				events = append(events, *ev)
			}
		}

		if err := rows.Err(); err != nil {
			slog.Error("list_events: iterate error", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("iterate events: %v", err)), nil
		}

		data, err := json.Marshal(events)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}

	return tool, handler
}

// ─────────────────────────────────────────────
// 3. create_event
// ─────────────────────────────────────────────

// NewCreateEventTool creates the create_event tool and its handler.
func NewCreateEventTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("create_event",
		mcp.WithDescription("Create a new calendar event. Records a calendar_changes entry and bumps the calendar's ctag for CalDAV sync. Returns {\"id\", \"conflicts\"}: conflicts lists existing events on any household calendar that overlap the new one (same rule as check_conflicts). The event is created either way; tell the user about any conflicts."),
		mcp.WithString("calendar_id", mcp.Description("Calendar ID to create the event in"), mcp.Required()),
		mcp.WithString("title", mcp.Description("Event title"), mcp.Required()),
		mcp.WithString("start_time", mcp.Description("Start time (ISO 8601, e.g. 2026-08-03T14:00:00-05:00). For all_day events a date (2026-08-03) is enough; any clock time is ignored."), mcp.Required()),
		mcp.WithString("end_time", mcp.Description("End time (ISO 8601, e.g. 2026-08-03T15:00:00-05:00). For all_day events give the last day (2026-08-05, or any time on it), or the exclusive next-day midnight (2026-08-06T00:00:00)."), mcp.Required()),
		mcp.WithBoolean("all_day", mcp.Description("Whether this is an all-day event (default false). All-day events cover whole days in the household timezone; clock times are dropped.")),
		mcp.WithString("description", mcp.Description("Optional description")),
		mcp.WithString("location", mcp.Description("Optional location")),
		mcp.WithString("event_type", mcp.Description("Optional event type (default 'event')")),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		args := req.GetArguments()
		calendarID := getStringArg(args, "calendar_id")
		title := getStringArg(args, "title")
		startTime := getStringArg(args, "start_time")
		endTime := getStringArg(args, "end_time")
		allDay := getBoolArg(args, "all_day", false)
		description := getStringArg(args, "description")
		location := getStringArg(args, "location")
		eventType := getStringArg(args, "event_type")
		if eventType == "" {
			eventType = "event"
		}

		// Validate required fields
		if calendarID == "" {
			return mcp.NewToolResultError("calendar_id is required"), nil
		}
		if title == "" {
			return mcp.NewToolResultError("title is required"), nil
		}
		if startTime == "" {
			return mcp.NewToolResultError("start_time is required"), nil
		}
		if endTime == "" {
			return mcp.NewToolResultError("end_time is required"), nil
		}

		// Validate the calendar belongs to the household
		if err := verifyCalendarOwnership(ctx, pool, calendarID, claims.HouseholdID); err != nil {
			return mcp.NewToolResultError("calendar not found"), nil
		}

		// Validate time formats and ordering. All-day events are normalised
		// to whole days (UTC-midnight dates, exclusive end; see allDayRange).
		// Timed events are stored as UTC RFC 3339, as the API and CalDAV do.
		// rangeStart/rangeEnd is the span the event occupies, used for the
		// conflict check: all-day events cover household-local days, the
		// same reading localizeAllDay gives existing all-day events.
		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)
		var storedStart, storedEnd string
		var rangeStart, rangeEnd time.Time
		if allDay {
			first, endExcl, err := allDayRange(startTime, endTime)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			storedStart, storedEnd = storeAllDay(first, endExcl)
			rangeStart = time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, loc)
			rangeEnd = time.Date(endExcl.Year(), endExcl.Month(), endExcl.Day(), 0, 0, 0, 0, loc)
		} else {
			startT, err := time.Parse(time.RFC3339, startTime)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid start_time format (use ISO 8601): %v", err)), nil
			}
			endT, err := time.Parse(time.RFC3339, endTime)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid end_time format (use ISO 8601): %v", err)), nil
			}
			if !endT.After(startT) {
				return mcp.NewToolResultError("end_time must be after start_time"), nil
			}
			storedStart, storedEnd = startT.UTC().Format(time.RFC3339), endT.UTC().Format(time.RFC3339)
			rangeStart, rangeEnd = startT, endT
		}

		// Generate IDs
		eventID := uuid.New().String()
		uid := eventID
		etag := uuid.New().String()

		// Build ical_data JSON
		icalData, err := marshalEventFields(title, description, storedStart, storedEnd, location, allDay)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
		}

		// Execute in transaction
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("begin transaction: %v", err)), nil
		}
		defer tx.Rollback(ctx)

		_, err = tx.Exec(ctx,
			`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, etag, event_type)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			eventID, calendarID, uid, icalData, etag, eventType,
		)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("insert event: %v", err)), nil
		}

		if err := recordChangeAndBumpCtag(ctx, tx, calendarID, uid, "create"); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("record change: %v", err)), nil
		}

		if err := tx.Commit(ctx); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("commit: %v", err)), nil
		}

		slog.Info("create_event: created", "event_id", eventID, "calendar_id", calendarID)

		// Report overlapping events on every household calendar. The event
		// is already committed, so a failed lookup must not fail the call:
		// it returns an empty list plus conflicts_error instead.
		resp := createEventResponse{ID: eventID}
		conflicts, err := findConflicts(ctx, pool, claims.HouseholdID, loc, nil, rangeStart, rangeEnd, eventID)
		if err != nil {
			slog.Warn("create_event: conflict check failed", "event_id", eventID, "error", err)
			conflicts = []ConflictEvent{}
			resp.ConflictsError = "conflict check failed; call check_conflicts to verify"
		}
		resp.Conflicts = conflicts

		data, err := json.Marshal(resp)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}

	return tool, handler
}

// ─────────────────────────────────────────────
// 4. update_event
// ─────────────────────────────────────────────

// recurringEditRefusal is the tool error update_event returns for a
// repeating event. It tells the model what to do next rather than retry.
const recurringEditRefusal = "This event repeats (it is a recurring series), and repeating events can't be edited from here yet: " +
	"changing it would turn the whole series into a single event. Nothing was changed. " +
	"Ask the user to make this change in their phone's calendar app. " +
	"Only if the user agrees, you can instead delete the series with delete_event and create a one-off event with create_event."

// recurringDeleteNote is added to delete_event's result for a repeating event.
const recurringDeleteNote = "This was a repeating event: the whole series was deleted, not just one occurrence."

// isRecurringEvent reports whether a calendar_objects row is part of a
// recurring series: its rrule column is set, or its raw iCalendar carries an
// RRULE, RDATE or RECURRENCE-ID property (an override of one occurrence).
// Both columns are NULL for rows written before migration 036.
func isRecurringEvent(rrule, icalRaw *string) bool {
	if rrule != nil && strings.TrimSpace(*rrule) != "" {
		return true
	}
	if icalRaw == nil || *icalRaw == "" {
		return false
	}
	for _, line := range strings.Split(*icalRaw, "\n") {
		// A line starting with a space or tab continues a folded line
		// (RFC 5545 3.1), e.g. a DESCRIPTION mentioning "RRULE:"; skip it.
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		name := strings.ToUpper(strings.TrimRight(line, "\r"))
		// Property name ends at ':' (value) or ';' (parameters).
		if i := strings.IndexAny(name, ":;"); i >= 0 {
			name = name[:i]
		}
		switch name {
		case "RRULE", "RDATE", "RECURRENCE-ID":
			return true
		}
	}
	return false
}

// NewUpdateEventTool creates the update_event tool and its handler.
func NewUpdateEventTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("update_event",
		mcp.WithDescription("Update an existing calendar event. All fields except event_id are optional — only provided fields are updated. Moving start_time without end_time keeps the event's duration. Returns the updated event. Repeating events can't be edited here yet: ask the user to change them in their phone's calendar app."),
		mcp.WithString("event_id", mcp.Description("Event ID to update"), mcp.Required()),
		mcp.WithString("title", mcp.Description("New title")),
		mcp.WithString("start_time", mcp.Description("New start time (ISO 8601). For all_day events a date (2026-08-03) is enough.")),
		mcp.WithString("end_time", mcp.Description("New end time (ISO 8601). Omit to keep the duration when moving start_time. For all_day events give the last day or the exclusive next-day midnight.")),
		mcp.WithBoolean("all_day", mcp.Description("Whether this is an all-day event. Setting it true snaps the event to whole days in the household timezone; setting it false needs start_time and end_time.")),
		mcp.WithString("description", mcp.Description("New description")),
		mcp.WithString("location", mcp.Description("New location")),
		mcp.WithString("event_type", mcp.Description("New event type")),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		args := req.GetArguments()
		eventID := getStringArg(args, "event_id")
		if eventID == "" {
			return mcp.NewToolResultError("event_id is required"), nil
		}

		// Get the event's calendar and verify household ownership
		calendarID, err := getEventCalendarID(ctx, pool, eventID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := verifyCalendarOwnership(ctx, pool, calendarID, claims.HouseholdID); err != nil {
			// Return not found rather than "not your calendar" to avoid leaking info
			return mcp.NewToolResultError("event not found"), nil
		}

		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)

		// Load the existing row. uid is what CalDAV clients know the event by
		// and what calendar_changes must record; it differs from id for
		// events created over CalDAV.
		var existingIcalData, eventUID string
		var rrule, icalRaw *string
		err = pool.QueryRow(ctx,
			`SELECT ical_data, uid, rrule, ical_raw FROM calendar_objects WHERE id = $1`,
			eventID,
		).Scan(&existingIcalData, &eventUID, &rrule, &icalRaw)
		if err != nil {
			return mcp.NewToolResultError("event not found"), nil
		}
		// Writes here only touch the JSON in ical_data; CalDAV then
		// regenerates the event without RRULE/EXDATE, so editing a series
		// would silently flatten it to one event on every phone. Refuse
		// until recurrence-aware writes exist (#4465).
		if isRecurringEvent(rrule, icalRaw) {
			return mcp.NewToolResultError(recurringEditRefusal), nil
		}
		var existing eventJSON
		if err := json.Unmarshal([]byte(existingIcalData), &existing); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("parse stored event: %v", err)), nil
		}

		// Collect optional field pointers. description and location may be
		// cleared by passing an empty string.
		var titlePtr, descPtr, startPtr, endPtr, locPtr *string
		var allDayPtr *bool

		if v := getStringArg(args, "title"); v != "" {
			titlePtr = &v
		}
		if args["description"] != nil {
			v := getStringArg(args, "description")
			descPtr = &v
		}
		if v := getStringArg(args, "start_time"); v != "" {
			startPtr = &v
		}
		if v := getStringArg(args, "end_time"); v != "" {
			endPtr = &v
		}
		if args["location"] != nil {
			v := getStringArg(args, "location")
			locPtr = &v
		}
		if v, ok := args["all_day"]; ok && v != nil {
			if b, ok := v.(bool); ok {
				allDayPtr = &b
			}
		}

		// Work out the new start/end: an explicit end_time is stored as
		// given; a start-only move keeps the original duration. All-day
		// events are always re-normalised to whole days.
		allDay := existing.AllDay
		if allDayPtr != nil {
			allDay = *allDayPtr
		}
		var newStartStr, newEndStr *string
		switch {
		case allDay:
			s, e, err := resolveUpdatedAllDay(existing, loc, startPtr, endPtr)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			newStartStr, newEndStr = &s, &e
		case existing.AllDay && startPtr == nil && endPtr == nil:
			return mcp.NewToolResultError("pass start_time and end_time when turning off all_day"), nil
		case startPtr != nil || endPtr != nil:
			newStart, newEnd, err := resolveUpdatedTimes(existing.Start, existing.End, startPtr, endPtr)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			s, e := newStart.Format(time.RFC3339), newEnd.Format(time.RFC3339)
			newStartStr, newEndStr = &s, &e
		}

		mergedIcalData, err := mergeEventFields(existingIcalData, titlePtr, descPtr, newStartStr, newEndStr, locPtr, allDayPtr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("merge event data: %v", err)), nil
		}

		newEtag := uuid.New().String()

		// Execute in transaction
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("begin transaction: %v", err)), nil
		}
		defer tx.Rollback(ctx)

		// event_type is only changed when provided (COALESCE keeps it otherwise).
		var eventTypeArg *string
		if v := getStringArg(args, "event_type"); v != "" {
			eventTypeArg = &v
		}
		row := eventRow{ID: eventID}
		err = tx.QueryRow(ctx,
			`UPDATE calendar_objects co
			    SET ical_data = $1, etag = $2, event_type = COALESCE($3, co.event_type), updated_at = NOW()
			   FROM calendars c
			  WHERE co.id = $4 AND c.id = co.calendar_id
			RETURNING co.calendar_id, co.ical_data, co.event_type, c.name, co.created_at, co.updated_at`,
			mergedIcalData, newEtag, eventTypeArg, eventID,
		).Scan(&row.CalendarID, &row.IcalData, &row.EventType, &row.CalendarName, &row.CreatedAt, &row.UpdatedAt)
		if err == pgx.ErrNoRows {
			return mcp.NewToolResultError("event not found"), nil
		}
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update event: %v", err)), nil
		}

		// Record change (by uid, which CalDAV sync-collection reports) and bump ctag
		if err := recordChangeAndBumpCtag(ctx, tx, calendarID, eventUID, "update"); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("record change: %v", err)), nil
		}

		if err := tx.Commit(ctx); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("commit: %v", err)), nil
		}

		updated, err := parseEventFromRow(row, loc)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("event updated but could not be read back: %v", err)), nil
		}
		out, err := json.Marshal(updated)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("event updated but could not be encoded: %v", err)), nil
		}

		slog.Info("update_event: updated", "event_id", eventID, "uid", eventUID)
		return mcp.NewToolResultText(string(out)), nil
	}

	return tool, handler
}

// ─────────────────────────────────────────────
// 5. delete_event
// ─────────────────────────────────────────────

// NewDeleteEventTool creates the delete_event tool and its handler.
func NewDeleteEventTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("delete_event",
		mcp.WithDescription("Delete a calendar event. For a repeating event this deletes the whole series, not one occurrence. Records a calendar_changes entry (action='delete') and bumps the calendar's ctag for CalDAV sync."),
		mcp.WithString("event_id", mcp.Description("Event ID to delete"), mcp.Required()),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		args := req.GetArguments()
		eventID := getStringArg(args, "event_id")
		if eventID == "" {
			return mcp.NewToolResultError("event_id is required"), nil
		}

		// Get the event's calendar and verify household ownership
		calendarID, err := getEventCalendarID(ctx, pool, eventID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := verifyCalendarOwnership(ctx, pool, calendarID, claims.HouseholdID); err != nil {
			return mcp.NewToolResultError("event not found"), nil
		}

		// Execute in transaction
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("begin transaction: %v", err)), nil
		}
		defer tx.Rollback(ctx)

		// Delete the event and get the uid for the change record
		var eventUID string
		var rrule, icalRaw *string
		err = tx.QueryRow(ctx,
			`DELETE FROM calendar_objects WHERE id = $1 RETURNING uid, rrule, ical_raw`,
			eventID,
		).Scan(&eventUID, &rrule, &icalRaw)
		if err == pgx.ErrNoRows {
			return mcp.NewToolResultError("event not found"), nil
		}
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("delete event: %v", err)), nil
		}

		// Record the delete change (DB constraint allows 'create', 'update', 'delete')
		if err := recordChangeAndBumpCtag(ctx, tx, calendarID, eventUID, "delete"); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("record change: %v", err)), nil
		}

		if err := tx.Commit(ctx); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("commit: %v", err)), nil
		}

		if isRecurringEvent(rrule, icalRaw) {
			slog.Info("delete_event: deleted series", "event_id", eventID)
			out, err := json.Marshal(map[string]any{"success": true, "series_deleted": true, "note": recurringDeleteNote})
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("event deleted but result could not be encoded: %v", err)), nil
			}
			return mcp.NewToolResultText(string(out)), nil
		}
		slog.Info("delete_event: deleted", "event_id", eventID)
		return mcp.NewToolResultText(`{"success":true}`), nil
	}

	return tool, handler
}

// ─────────────────────────────────────────────
// 6. find_available_slots
// ─────────────────────────────────────────────

// NewFindAvailableSlotsTool creates the find_available_slots tool and its handler.
//
// Every day's window and query bounds are built with time.Date in the
// household's timezone, so a DST day is 23 or 25 hours long, and every
// returned time carries the household's offset.
func NewFindAvailableSlotsTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("find_available_slots",
		mcp.WithDescription("Find available time slots on a date or an inclusive date range, in the household's timezone. To search several days (e.g. a whole week), make ONE call with date as the first day and end_date as the last day; do not call once per day. Available hours default to 8am-8pm local (start_hour/end_hour override them). Checks timed events across the specified calendars; all-day events (bills, birthdays, holidays, trips) do not block slots. Returns [{date, start, end}] with RFC 3339 times in the household's offset, ordered by start."),
		mcp.WithString("date", mcp.Description("First (or only) date to check (YYYY-MM-DD)"), mcp.Required()),
		mcp.WithNumber("duration_minutes", mcp.Description("Minimum slot duration in minutes"), mcp.Required()),
		mcp.WithString("end_date", mcp.Description(fmt.Sprintf("Optional last date of the range, inclusive (YYYY-MM-DD). Defaults to date. At most %d days.", maxSlotRangeDays))),
		mcp.WithNumber("start_hour", mcp.Description(fmt.Sprintf("Optional local hour the available window opens, 0-23 (default %d)", defaultSlotStartHour))),
		mcp.WithNumber("end_hour", mcp.Description(fmt.Sprintf("Optional local hour the available window closes, 1-24, after start_hour (default %d)", defaultSlotEndHour))),
		mcp.WithArray("calendar_ids", mcp.Description("Optional list of calendar IDs to check (checks all calendars if omitted)")),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)

		args := req.GetArguments()
		dateStr := getStringArg(args, "date")
		if dateStr == "" {
			return mcp.NewToolResultError("date is required (YYYY-MM-DD)"), nil
		}
		firstDay, err := parseDate(dateStr, loc)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid date format (use YYYY-MM-DD): %v", err)), nil
		}

		lastDay := firstDay
		if endDateStr := getStringArg(args, "end_date"); endDateStr != "" {
			lastDay, err = parseDate(endDateStr, loc)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid end_date format (use YYYY-MM-DD): %v", err)), nil
			}
		}
		days := daysInRange(firstDay, lastDay, loc)
		if len(days) == 0 {
			return mcp.NewToolResultError("end_date must not be before date"), nil
		}
		if len(days) > maxSlotRangeDays {
			return mcp.NewToolResultError(fmt.Sprintf("date range is %d days; at most %d are allowed", len(days), maxSlotRangeDays)), nil
		}

		durationMin := getFloat64Arg(args, "duration_minutes", 30)
		if durationMin <= 0 {
			return mcp.NewToolResultError("duration_minutes must be positive"), nil
		}
		duration := time.Duration(durationMin * float64(time.Minute))

		startHour, endHour, err := slotHours(args)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		calendarIDs := getStringSliceArg(args, "calendar_ids")

		// Query from local midnight of the first day to local midnight after
		// the last day. time.Date normalises day+1, so DST days are covered
		// exactly (no midnight+24h drift).
		queryStart := days[0]
		last := days[len(days)-1]
		queryEnd := time.Date(last.Year(), last.Month(), last.Day()+1, 0, 0, 0, 0, loc)

		events, err := queryEvents(ctx, pool, claims.HouseholdID, loc, calendarIDs, queryStart, queryEnd)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("query events: %v", err)), nil
		}

		busy := make([]timeInterval, 0, len(events))
		for _, ev := range events {
			// All-day events (bills, birthdays, trips) are free time, as in
			// calendar apps. Only free-time search skips them: check_conflicts
			// and create_event's conflicts (findConflicts) still report them.
			if ev.AllDay {
				continue
			}
			evStart, evEnd, err := parseEventTime(ev)
			if err != nil || !evEnd.After(evStart) {
				continue
			}
			busy = append(busy, timeInterval{start: evStart, end: evEnd})
		}

		slots := make([]AvailableSlot, 0)
		for _, day := range days {
			windowStart := time.Date(day.Year(), day.Month(), day.Day(), startHour, 0, 0, 0, loc)
			windowEnd := time.Date(day.Year(), day.Month(), day.Day(), endHour, 0, 0, 0, loc)
			slots = append(slots, freeSlots(busy, windowStart, windowEnd, duration, day.Format("2006-01-02"), loc)...)
		}

		data, err := json.Marshal(slots)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}

	return tool, handler
}

const (
	defaultSlotStartHour = 8
	defaultSlotEndHour   = 20
	maxSlotRangeDays     = 31
)

// timeInterval is a half-open [start, end) busy interval.
type timeInterval struct {
	start time.Time
	end   time.Time
}

// slotHours reads and validates the optional start_hour/end_hour args.
func slotHours(args map[string]any) (int, int, error) {
	startF := getFloat64Arg(args, "start_hour", defaultSlotStartHour)
	endF := getFloat64Arg(args, "end_hour", defaultSlotEndHour)
	if startF != float64(int(startF)) || endF != float64(int(endF)) {
		return 0, 0, fmt.Errorf("start_hour and end_hour must be whole hours")
	}
	start, end := int(startF), int(endF)
	if start < 0 || start > 23 {
		return 0, 0, fmt.Errorf("start_hour must be between 0 and 23")
	}
	if end < 1 || end > 24 {
		return 0, 0, fmt.Errorf("end_hour must be between 1 and 24")
	}
	if end <= start {
		return 0, 0, fmt.Errorf("end_hour must be after start_hour")
	}
	return start, end, nil
}

// daysInRange returns local midnight of every calendar day from first to
// last inclusive (both already at local midnight in loc). It steps by
// calendar day via time.Date, never by 24h, so DST transitions don't skip
// or repeat a day. Returns nil when last is before first.
func daysInRange(first, last time.Time, loc *time.Location) []time.Time {
	var days []time.Time
	for d := first; !d.After(last); d = time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, loc) {
		days = append(days, d)
		if len(days) > maxSlotRangeDays {
			break // caller rejects; stop early on huge ranges
		}
	}
	return days
}

// freeSlots returns the gaps of at least duration inside [windowStart,
// windowEnd) that no busy interval covers. Every time is formatted in loc,
// so slot offsets are consistent whatever offset the events were stored in.
func freeSlots(busy []timeInterval, windowStart, windowEnd time.Time, duration time.Duration, date string, loc *time.Location) []AvailableSlot {
	clipped := make([]timeInterval, 0, len(busy))
	for _, b := range busy {
		s, e := b.start, b.end
		if s.Before(windowStart) {
			s = windowStart
		}
		if e.After(windowEnd) {
			e = windowEnd
		}
		if !s.Before(e) {
			continue // outside the window
		}
		clipped = append(clipped, timeInterval{start: s, end: e})
	}
	sort.Slice(clipped, func(i, j int) bool { return clipped[i].start.Before(clipped[j].start) })

	slots := make([]AvailableSlot, 0)
	emit := func(from, to time.Time) {
		if to.Sub(from) >= duration {
			slots = append(slots, AvailableSlot{
				Date:  date,
				Start: from.In(loc).Format(time.RFC3339),
				End:   to.In(loc).Format(time.RFC3339),
			})
		}
	}

	cursor := windowStart
	for _, b := range clipped {
		if b.start.After(cursor) {
			emit(cursor, b.start)
		}
		if b.end.After(cursor) {
			cursor = b.end
		}
	}
	if windowEnd.After(cursor) {
		emit(cursor, windowEnd)
	}
	return slots
}

// ─────────────────────────────────────────────
// 7. get_daily_briefing
// ─────────────────────────────────────────────

// NewGetDailyBriefingTool creates the get_daily_briefing tool and its handler.
func NewGetDailyBriefingTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("get_daily_briefing",
		mcp.WithDescription("Get a formatted daily briefing with events, maintenance tasks due, and bills due for a given date"),
		mcp.WithString("date", mcp.Description("Date (YYYY-MM-DD). Defaults to today.")),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)

		args := req.GetArguments()
		dateStr := getStringArg(args, "date")
		var date time.Time
		if dateStr != "" {
			var err error
			date, err = parseDate(dateStr, loc)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid date format (use YYYY-MM-DD): %v", err)), nil
			}
		} else {
			now := time.Now().In(loc)
			date = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		}

		dayStart := date
		dayEnd := date.AddDate(0, 0, 1) // next local midnight, DST-safe
		dateDisplay := date.Format("Monday, January 2, 2006")

		var briefing strings.Builder
		briefing.WriteString(fmt.Sprintf("# Daily Briefing — %s\n\n", dateDisplay))

		// ── Events ──
		events, eventsErr := queryEvents(ctx, pool, claims.HouseholdID, loc, nil, dayStart, dayEnd)
		if eventsErr != nil {
			slog.Error("get_daily_briefing: query events error", "error", eventsErr)
		}

		// Filter events to this date
		todaysEvents := make([]Event, 0)
		if eventsErr == nil {
			for _, ev := range events {
				if eventOverlapsRange(ev, dayStart, dayEnd) {
					todaysEvents = append(todaysEvents, ev)
				}
			}
		}

		// Sort events by start time
		sort.SliceStable(todaysEvents, func(i, j int) bool {
			return startsBefore(todaysEvents[i].Start, todaysEvents[j].Start)
		})

		briefing.WriteString(fmt.Sprintf("## 📅 Events (%d)\n\n", len(todaysEvents)))
		if eventsErr != nil {
			briefing.WriteString("⚠️ Could not load events (database error) — do not assume the day is free.\n\n")
		} else if len(todaysEvents) == 0 {
			briefing.WriteString("No events scheduled for today.\n\n")
		} else {
			for _, ev := range todaysEvents {
				startT, err := time.Parse(time.RFC3339, ev.Start)
				timeStr := ev.Start
				if err == nil {
					timeStr = startT.In(loc).Format("3:04 PM")
				}
				if ev.AllDay {
					timeStr = "All day"
				}
				calInfo := ""
				if ev.CalendarName != "" {
					calInfo = fmt.Sprintf(" [%s]", ev.CalendarName)
				}
				// Name the start day explicitly: an event that began on an
				// earlier day (multi-day trip) would otherwise read as today.
				dayInfo := ""
				if ev.Weekday != "" {
					dayInfo = fmt.Sprintf(" (%s %s)", ev.Weekday, ev.LocalDate)
				}
				briefing.WriteString(fmt.Sprintf("- **%s**%s — %s%s\n", timeStr, dayInfo, ev.Title, calInfo))
				if ev.Description != "" {
					briefing.WriteString(fmt.Sprintf("  %s\n", ev.Description))
				}
				if ev.Location != "" {
					briefing.WriteString(fmt.Sprintf("  📍 %s\n", ev.Location))
				}
			}
			briefing.WriteString("\n")
		}

		// ── Maintenance Tasks Due ──
		briefing.WriteString("## 🔧 Maintenance Tasks Due\n\n")
		mtRows, mtErr := pool.Query(ctx,
			`SELECT name, description, status, property_id
			 FROM maintenance_tasks
			 WHERE household_id = $1 AND due_date = $2
			 ORDER BY name`,
			claims.HouseholdID, date,
		)
		if mtErr != nil {
			slog.Error("get_daily_briefing: query maintenance error", "error", mtErr)
		}

		mtCount := 0
		if mtRows != nil {
			defer mtRows.Close()
			for mtRows.Next() {
				mtCount++
				var name, status string
				var description, propertyID *string
				if err := mtRows.Scan(&name, &description, &status, &propertyID); err != nil {
					slog.Error("get_daily_briefing: scan maintenance error", "error", err)
					continue
				}
				briefing.WriteString(fmt.Sprintf("- **%s** [%s]", name, status))
				if description != nil && *description != "" {
					briefing.WriteString(fmt.Sprintf(" — %s", *description))
				}
				briefing.WriteString("\n")
			}
			if err := mtRows.Err(); err != nil {
				slog.Error("get_daily_briefing: iterate maintenance error", "error", err)
			}
		}

		if mtErr != nil {
			briefing.WriteString("⚠️ Could not load maintenance tasks (database error) — do not assume no tasks are due.\n\n")
		} else if mtCount == 0 {
			briefing.WriteString("No maintenance tasks due today.\n\n")
		} else {
			briefing.WriteString("\n")
		}

		// ── Bills Due ──
		briefing.WriteString("## 💰 Bills Due\n\n")
		bills, billErr := billsDueOn(ctx, pool, claims.HouseholdID, date)
		if billErr != nil {
			slog.Error("get_daily_briefing: query bills error", "error", billErr)
			briefing.WriteString("⚠️ Could not load bills (database error) — do not assume no bills are due.\n")
		} else if len(bills) == 0 {
			briefing.WriteString("No bills due today.\n")
		} else {
			for _, b := range bills {
				catInfo := ""
				if b.category != "" {
					catInfo = fmt.Sprintf(" [%s]", b.category)
				}
				briefing.WriteString(fmt.Sprintf("- **%s** — $%.2f%s\n", b.name, b.amount, catInfo))
			}
		}

		return mcp.NewToolResultText(briefing.String()), nil
	}

	return tool, handler
}

// briefingBill is a bill listed in the daily briefing.
type briefingBill struct {
	name, category string
	amount         float64
}

// billsDueOn returns the household's bills whose due date is date (a local
// calendar day) and that are not yet paid for that due date, by name.
//
// It applies bill.BillDue, the rule list_bills and get_upcoming_bills use,
// so the briefing agrees with them: due_day is clamped to the month (31 is
// due Nov 30) and paid_date only hides the bill when it covers this due date
// (a payment for last month's bill does not). As of the briefing date, the
// first unpaid due date on or after it is the briefing date exactly when the
// bill is due that day and unpaid.
func billsDueOn(ctx context.Context, pool *pgxpool.Pool, householdID string, date time.Time) ([]briefingBill, error) {
	y, m, d := date.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC) // bill dates are midnight UTC
	rows, err := pool.Query(ctx,
		`SELECT name, COALESCE(amount, 0), COALESCE(category, ''), due_day, paid_date
		 FROM bills
		 WHERE household_id = $1 AND due_day BETWEEN 1 AND 31
		 ORDER BY name`,
		householdID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []briefingBill
	for rows.Next() {
		var b briefingBill
		var dueDay int
		var paidDate *time.Time
		if err := rows.Scan(&b.name, &b.amount, &b.category, &dueDay, &paidDate); err != nil {
			return nil, fmt.Errorf("scan bill: %w", err)
		}
		if bill.BillDue(day, dueDay, paidDate).Next.Equal(day) {
			out = append(out, b)
		}
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────
// 8. check_conflicts
// ─────────────────────────────────────────────

// NewCheckConflictsTool creates the check_conflicts tool and its handler.
func NewCheckConflictsTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("check_conflicts",
		mcp.WithDescription("Check for conflicting events in a given time range. Returns events that overlap the proposed time."),
		mcp.WithString("start_time", mcp.Description("Proposed start time (ISO 8601)"), mcp.Required()),
		mcp.WithString("end_time", mcp.Description("Proposed end time (ISO 8601)"), mcp.Required()),
		mcp.WithArray("calendar_ids", mcp.Description("Optional list of calendar IDs to check (checks all calendars if omitted)")),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		args := req.GetArguments()
		startTimeStr := getStringArg(args, "start_time")
		endTimeStr := getStringArg(args, "end_time")

		if startTimeStr == "" {
			return mcp.NewToolResultError("start_time is required"), nil
		}
		if endTimeStr == "" {
			return mcp.NewToolResultError("end_time is required"), nil
		}

		proposedStart, err := time.Parse(time.RFC3339, startTimeStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid start_time format (use ISO 8601): %v", err)), nil
		}
		proposedEnd, err := time.Parse(time.RFC3339, endTimeStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid end_time format (use ISO 8601): %v", err)), nil
		}
		if !proposedEnd.After(proposedStart) {
			return mcp.NewToolResultError("end_time must be after start_time"), nil
		}

		calendarIDs := getStringSliceArg(args, "calendar_ids")
		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)

		conflicts, err := findConflicts(ctx, pool, claims.HouseholdID, loc, calendarIDs, proposedStart, proposedEnd, "")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, err := json.Marshal(conflicts)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}

	return tool, handler
}
