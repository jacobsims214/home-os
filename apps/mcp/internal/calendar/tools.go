// Package calendar provides the MCP calendar tools (v2 catalog: calendar_find,
// calendar_create, event_find, event_create, event_update, event_delete,
// calendar_find_free_time, agenda_get). Results are built with
// internal/respond.
//
// Each tool function accepts a *pgxpool.Pool and returns a tool definition
// plus handler function. Handlers extract JWT claims from context to scope
// all queries by household_id.
package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/bill"
	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// Calendar is one of the family's calendars, returned by calendar_find.
type Calendar struct {
	ID         string  `json:"id" jsonschema_description:"Calendar id; pass it (or the name) as calendar_id."`
	Name       string  `json:"name"`
	Type       string  `json:"type" jsonschema_description:"family, bills, maintenance, vehicles, properties or custom."`
	Color      *string `json:"color,omitempty"`
	PropertyID *string `json:"property_id,omitempty" jsonschema_description:"The property this calendar belongs to, if any."`
}

// Event is a calendar event parsed from calendar_objects + ical_data JSON.
// Start and End are RFC 3339 in the family timezone's offset; an all-day
// event runs from local midnight of its first day to local midnight after
// its last day.
type Event struct {
	ID           string `json:"id" jsonschema_description:"Event id; pass it as event_id to event_update or event_delete."`
	CalendarID   string `json:"calendar_id"`
	CalendarName string `json:"calendar_name,omitempty"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Start        string `json:"start" jsonschema_description:"Start, RFC 3339 in the family timezone."`
	End          string `json:"end" jsonschema_description:"End (exclusive), RFC 3339 in the family timezone."`
	AllDay       bool   `json:"all_day"`
	Location     string `json:"location"`
	Color        string `json:"color"`
	EventType    string `json:"event_type"`
	// LocalDate (YYYY-MM-DD) and Weekday ("Wednesday") are the event's
	// start day in the household timezone, computed so callers never have
	// to derive the weekday themselves. Empty when start is unreadable.
	LocalDate string `json:"local_date" jsonschema_description:"Start day in the family timezone (YYYY-MM-DD)."`
	Weekday   string `json:"weekday" jsonschema_description:"Weekday of local_date, e.g. Wednesday."`
	Recurring bool   `json:"recurring" jsonschema_description:"true for a repeating series; edit it with scope this, following or all."`
	// Recurrence, reminders and people (#4465), read from ical_raw while it
	// is current for the JSON (the rule CalDAV serves by).
	Repeat          *Repeat       `json:"repeat,omitempty" jsonschema_description:"How the event repeats; absent for one-off events."`
	RRule           string        `json:"rrule,omitempty" jsonschema_description:"The RFC 5545 RRULE phones see."`
	ExceptDates     []string      `json:"except_dates,omitempty" jsonschema_description:"Skipped occurrence dates (YYYY-MM-DD)."`
	ReminderMinutes []int         `json:"reminder_minutes,omitempty" jsonschema_description:"Phone alerts, minutes before the start."`
	People          []EventPerson `json:"people,omitempty" jsonschema_description:"Family members the event is for."`
	RecurrenceID    string        `json:"recurrence_id,omitempty" jsonschema_description:"For one occurrence: its original start; pass it as occurrence_date."`

	// EntityType is calendar_objects.entity_type: what the event is a mirror
	// of ('bill', 'maintenance_task', a person or pet link, or nothing). It is
	// never serialized; the role rule uses it to classify an event by what it
	// mirrors (#4807), because a bill-linked event on the Family calendar still
	// carries the bill's name.
	EntityType string `json:"-"`

	// billLike marks an entry that MAKES A CLAIM ABOUT MONEY and is not the
	// bills module's record of that money (#4943): no entity link, not a
	// bill's mirror, and either a stated amount or a place on a Bills calendar.
	// Such an entry is what a briefing read as "the bill is due"; the
	// briefings leave it out of the events list and report it as a bill-like
	// entry instead (see billlook.go). A title that only says the word "bill"
	// ("Electric bill" as a person's own note) stays an ordinary event: it
	// claims no amount and no due date, and bill_like_entries names it anyway.
	// Never serialized.
	billLike bool
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

// AvailableSlot is a free time slot returned by calendar_find_free_time.
// Start and End are RFC 3339 in the household's offset.
type AvailableSlot struct {
	LocalDate string `json:"local_date" jsonschema_description:"Day of the slot in the family timezone (YYYY-MM-DD)."`
	Weekday   string `json:"weekday"`
	Start     string `json:"start"`
	End       string `json:"end"`
}

// ConflictEvent is an existing event that overlaps a new or moved one.
type ConflictEvent struct {
	ID           string `json:"id"`
	CalendarID   string `json:"calendar_id"`
	CalendarName string `json:"calendar_name,omitempty"`
	Title        string `json:"title"`
	Start        string `json:"start"`
	End          string `json:"end"`
	AllDay       bool   `json:"all_day"`
	LocalDate    string `json:"local_date"`
	Weekday      string `json:"weekday"`
}

// eventWriteResult is event_create's and event_update's data: the event as
// stored and the other events it overlaps (never null; empty when none, and
// always empty for an update that did not change the event's time).
type eventWriteResult struct {
	Event          Event           `json:"event"`
	Conflicts      []ConflictEvent `json:"conflicts" jsonschema_description:"Other events overlapping this one. The event is saved either way; tell the user."`
	ConflictsError string          `json:"conflicts_error,omitempty" jsonschema_description:"Set when the overlap check failed; check with event_find."`
	Duplicate      bool            `json:"duplicate,omitempty" jsonschema_description:"true when an identical event made in the last 10 minutes was returned instead of a new one."`
	// Scope is set for an edit of a repeating event.
	Scope string `json:"scope,omitempty" jsonschema_description:"Which occurrences changed: this, following or all."`
	// OriginalEventID is set for scope following: the series was split and
	// event is the new part.
	OriginalEventID string `json:"original_event_id,omitempty" jsonschema_description:"scope following: the event that keeps the earlier occurrences."`
	Note            string `json:"note,omitempty"`
}

// eventDeleteResult is event_delete's data: the event that was deleted, or
// for one occurrence (or the following ones) the series as it now stands.
type eventDeleteResult struct {
	Event          Event  `json:"event"`
	SeriesDeleted  bool   `json:"series_deleted" jsonschema_description:"true when a whole repeating series was deleted."`
	Scope          string `json:"scope,omitempty" jsonschema_description:"this, following or all for a repeating event."`
	OccurrenceDate string `json:"occurrence_date,omitempty" jsonschema_description:"The occurrence removed (scope this) or where the series now ends (following)."`
	Note           string `json:"note,omitempty"`
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
	EntityType   *string
	CalendarName string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Rrule        *string
	IcalRaw      *string
	// CalendarType is calendars.type ('family', 'bills', 'maintenance',
	// 'vehicles', 'properties', 'custom'): the calendar's kind, which together
	// with EntityType (the record the event mirrors, #4807) says what kind of
	// record the event is for the role view (#4808).
	CalendarType string
	// SupersededAt is when Home OS retired this entry in place because a bill
	// with the same name and amount is the record for it (#4943, migration
	// 073). A superseded entry is not deleted — its text, notes and people
	// survive — but every live read skips it, so the Bills calendar shows the
	// bill's own projection once and no briefing can read a stale "due".
	SupersededAt     *time.Time
	SupersededReason *string
	SupersededBy     *string
}

// parseEventFromRow decodes a calendar_objects row. All-day events come back
// as household-local midnights (see localizeAllDay) and timed events in the
// household's offset, so every tool shows the same local times.
func parseEventFromRow(row eventRow, loc *time.Location) (*Event, error) {
	ev, _, err := parseRow(row, loc)
	return ev, err
}

// parseRow is parseEventFromRow plus the stored object the event was read
// from (nil obj for rows without rrule/ical_raw), so range reads can expand
// a series into its occurrences without parsing the raw twice.
func parseRow(row eventRow, loc *time.Location) (*Event, storedSeries, error) {
	var series storedSeries
	var ej eventJSON
	if err := json.Unmarshal([]byte(row.IcalData), &ej); err != nil {
		return nil, series, fmt.Errorf("parse ical_data: %w", err)
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
	if row.EntityType != nil {
		ev.EntityType = *row.EntityType
	}
	// A money claim nobody keeps up to date (#4943): nothing is the record for
	// it, it is not a bill's own mirror, and it states an amount or sits on a
	// Bills calendar. See billlook.go.
	ev.billLike = row.SupersededAt == nil && row.EntityType == nil &&
		row.EventType != bill.CalendarEventType &&
		claimsMoney(ej.Title, row.CalendarType)
	if row.Rrule != nil || row.IcalRaw != nil {
		series = loadSeries(ej, row.ID, row.Rrule, row.IcalRaw)
		ev.Recurring = series.recurring() || (series.locked && isRecurringEvent(row.Rrule, row.IcalRaw))
		var doc map[string]json.RawMessage
		_ = json.Unmarshal([]byte(row.IcalData), &doc)
		attachSeries(ev, series, doc, loc)
	} else if strings.Contains(row.IcalData, `"people"`) {
		var doc struct {
			People []EventPerson `json:"people"`
		}
		if json.Unmarshal([]byte(row.IcalData), &doc) == nil {
			ev.People = doc.People
		}
	}
	localizeAllDay(ev, loc)
	if !ev.AllDay {
		for _, p := range []*string{&ev.Start, &ev.End} {
			if t, err := time.Parse(time.RFC3339, *p); err == nil {
				*p = t.In(loc).Format(time.RFC3339)
			}
		}
	}
	setLocalDay(ev, loc)
	return ev, series, nil
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
// times and the optional new values passed to event_update:
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
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start format (use ISO 8601 with an offset, e.g. 2026-08-03T14:00:00-05:00): %w", err)
		}
	}
	if newEnd != nil {
		if end, err = time.Parse(time.RFC3339, *newEnd); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end format (use ISO 8601 with an offset, e.g. 2026-08-03T15:00:00-05:00): %w", err)
		}
	}

	if newStart == nil || newEnd == nil {
		storedStart, errS := time.Parse(time.RFC3339, oldStart)
		storedEnd, errE := time.Parse(time.RFC3339, oldEnd)
		switch {
		case newStart == nil && newEnd == nil:
			if errS != nil || errE != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("stored event has unreadable start/end; pass start and end")
			}
			start, end = storedStart, storedEnd
		case newStart == nil: // end only
			if errS != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("stored event has an unreadable start; pass start too")
			}
			start = storedStart
		default: // start only: keep the original duration
			if errS != nil || errE != nil || !storedEnd.After(storedStart) {
				return time.Time{}, time.Time{}, fmt.Errorf("cannot keep the event's duration (stored start/end unreadable); pass end too")
			}
			end = start.Add(storedEnd.Sub(storedStart))
		}
	}

	if !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("end must be after start")
	}
	return start.UTC(), end.UTC(), nil
}

// resolveUpdatedAllDay works out the stored start/end of an event that is
// all-day after event_update, from its stored times and the optional new
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
			err = fmt.Errorf("invalid start: %w", err)
		} else {
			endExcl = first.AddDate(0, 0, days)
		}
	default:
		if first, endExcl, err = allDayRange(existing.Start, existing.End); err != nil {
			err = fmt.Errorf("stored event has unreadable start/end; pass start and end")
		}
	}
	if err != nil {
		return "", "", err
	}
	s, e := storeAllDay(first, endExcl)
	return s, e, nil
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
// and date range. Date filtering is done in SQL on calendar_objects.starts_at
// / ends_at (035). Callers may still use eventOverlapsRange as a correctness
// backstop.
// ─────────────────────────────────────────────

// eventColumns selects an eventRow from calendar_objects co JOIN calendars c;
// scanEventRow reads it.
// eventColumns selects an eventRow from calendar_objects co JOIN calendars c.
// c.type is the calendar's kind ('family', 'bills', 'maintenance', 'vehicles',
// 'properties', 'custom'), which the role view needs to decide whether the
// caller may see this event at all (#4808); co.entity_type is the record the
// event mirrors (#4807).
const eventColumns = `co.id::text, co.calendar_id::text, co.ical_data, co.event_type, co.entity_type::text, c.name, co.created_at, co.updated_at, co.rrule, co.ical_raw, c.type::text, co.superseded_at, co.superseded_reason, co.superseded_by::text`

func scanEventRow(row pgx.Row) (eventRow, error) {
	var r eventRow
	err := row.Scan(&r.ID, &r.CalendarID, &r.IcalData, &r.EventType, &r.EntityType, &r.CalendarName, &r.CreatedAt, &r.UpdatedAt,
		&r.Rrule, &r.IcalRaw, &r.CalendarType, &r.SupersededAt, &r.SupersededReason, &r.SupersededBy)
	return r, err
}

// queryEvents returns the household's events in [startDate, endDate) with
// repeating series expanded into their occurrences (see expandSeries); see
// queryOccurrences.
func queryEvents(ctx context.Context, pool querier, householdID string, loc *time.Location, calendarIDs []string, startDate, endDate time.Time) ([]Event, error) {
	events, _, err := queryOccurrences(ctx, pool, householdID, loc, calendarIDs, startDate, endDate)
	return events, err
}

// queryOccurrences reads the events that may overlap [startDate, endDate)
// and expands every repeating series into one Event per occurrence in that
// range (skipped dates left out, moved occurrences at their new time). The
// SQL window is a superset: callers keep eventOverlapsRange as the exact
// check. truncated names the series that had more than maxSeriesOccurrences
// occurrences in the range, of which only the first ones are returned. With
// a zero range, series come back unexpanded.
func queryOccurrences(ctx context.Context, pool querier, householdID string, loc *time.Location, calendarIDs []string, startDate, endDate time.Time) (events []Event, truncated []string, err error) {
	return queryOccurrencesAs(ctx, pool, eventView{role: roleFamily}, householdID, loc, calendarIDs, startDate, endDate)
}

// roleFamily is the role that sees every event, used by the reads that serve a
// family-only write (event_create's read-back, event_update's, event_delete's)
// or an internal caller whose caller already passed the policy (the API's
// RequirePolicy is per route). It is a role name from the policy table, not a
// bypass: a read that serves a caller goes through queryOccurrencesAs with that
// caller's view.
const roleFamily = "family_member"

// queryOccurrencesAs is queryOccurrences for one role: rows the role may not
// see are dropped before parsing, so a bill mirror's amount never even reaches
// the process, and every occurrence of a hidden series goes with its row.
func queryOccurrencesAs(ctx context.Context, pool querier, view eventView, householdID string, loc *time.Location, calendarIDs []string, startDate, endDate time.Time) (events []Event, truncated []string, err error) {
	// Base query: join calendar_objects with calendars for household scoping
	query := `SELECT ` + eventColumns + `
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

	// Filter by date range on the stored instants (migration 035:
	// starts_at/ends_at, trigger-maintained from ical_data's start/end, NULL
	// when unparseable), served by idx_calendar_objects_calendar_starts.
	// Overlap condition: event start < range end AND event end > range start.
	// This narrows the result set in SQL; callers keep Go-side filtering as
	// the exact check. The window is widened by allDaySlack because all-day
	// events are stored as UTC midnights but compared as local days. A
	// recurring series is kept whenever it starts before the range end:
	// starts_at is the series start, and its later occurrences can fall in
	// the range even though the first one ended long ago. A series recurs
	// either through the rrule column or through an RDATE list, which lives
	// in ical_raw and nowhere else -- an RDATE-only series has rrule NULL --
	// so the raw is tested too (#4871). Property names are case-insensitive
	// in RFC 5545, hence ILIKE.
	if !startDate.IsZero() && !endDate.IsZero() {
		query += fmt.Sprintf(" AND co.starts_at < $%d", argIdx)
		args = append(args, endDate.Add(allDaySlack))
		argIdx++
		query += fmt.Sprintf(" AND (co.ends_at > $%d OR co.rrule IS NOT NULL OR co.ical_raw ILIKE '%%RDATE%%')", argIdx)
		args = append(args, startDate.Add(-allDaySlack))
		argIdx++
	}

	query += " ORDER BY co.created_at"

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	expand := !startDate.IsZero() && !endDate.IsZero()
	for rows.Next() {
		row, err := scanEventRow(rows)
		if err != nil {
			slog.Warn("calendar: query events: scan error", "error", err)
			continue
		}
		// A superseded entry is retired: a bill with the same name and amount
		// is the record for it (#4943, migration 073). It is not deleted, so
		// its notes, people and text survive and an undo is one UPDATE, but no
		// live read shows it — that is what keeps a stale "Electric bill due —
		// $180" out of a briefing after the bill was paid.
		if row.SupersededAt != nil {
			continue
		}
		// The role view drops the row before it is parsed: a bill mirror's
		// amount, category, autopay flag and payment URL never enter the
		// process for a role that may not see money (#4808).
		if !view.rowVisible(row) {
			continue
		}
		ev, series, err := parseRow(row, loc)
		if err != nil {
			slog.Warn("calendar: query events: parse error", "event_id", row.ID, "error", err)
			continue
		}
		if !expand || !series.recurring() {
			events = append(events, *ev)
			continue
		}
		occs, cut := expandSeries(*ev, series, startDate, endDate, loc)
		events = append(events, occs...)
		if cut {
			truncated = append(truncated, ev.Title)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate events: %w", err)
	}

	return events, truncated, nil
}

// findConflicts returns the household's events (optionally limited to
// calendarIDs) whose [start, end) overlaps [start, end), sorted by start
// instant. excludeID skips one event (event_create passes the event it just
// made). It is shared by event_find and event_create so both agree on
// what a conflict is. All-day events come back from queryEvents as
// household-local days, so one on 11/18 conflicts with any range touching
// 11/18 household time and none on 11/19. Always returns a non-nil slice.
func findConflicts(ctx context.Context, pool querier, householdID string, loc *time.Location, calendarIDs []string, start, end time.Time, excludeID string) ([]ConflictEvent, error) {
	return findConflictsAs(ctx, pool, eventView{role: roleFamily}, householdID, loc, calendarIDs, start, end, excludeID)
}

// findConflictsAs is findConflicts for one role: a bill's or an upkeep job's
// mirror event is not a scheduling conflict, and it must not be named to a role
// that may not see it either.
func findConflictsAs(ctx context.Context, pool querier, view eventView, householdID string, loc *time.Location, calendarIDs []string, start, end time.Time, excludeID string) ([]ConflictEvent, error) {
	events, _, err := queryOccurrencesAs(ctx, pool, view, householdID, loc, calendarIDs, start, end)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}

	conflicts := make([]ConflictEvent, 0)
	for _, ev := range events {
		if ev.ID == excludeID || isBillEvent(ev) || ev.billLike || !eventOverlapsRange(ev, start, end) {
			continue
		}
		conflicts = append(conflicts, ConflictEvent{
			ID:           ev.ID,
			CalendarID:   ev.CalendarID,
			Title:        ev.Title,
			Start:        ev.Start,
			End:          ev.End,
			AllDay:       ev.AllDay,
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
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start: %w", err)
	}
	if end == "" {
		return first, first.AddDate(0, 0, 1), nil
	}
	last, midnight, err := civilDate(end)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid end: %w", err)
	}
	if last.Before(first) {
		return time.Time{}, time.Time{}, fmt.Errorf("end must not be before start")
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

// EventsBetween returns the household's events overlapping [start, end),
// sorted by start instant, exactly as agenda_get lists them (all-day events
// read as household-local days in loc). Exported for family_overview's
// next_days (#4385) so both tools agree. Bills' own calendar events are
// left out (isBillEvent), and so is every record the role may not see:
// family_overview passes the caller's role through (#4808).
func EventsBetween(ctx context.Context, pool *pgxpool.Pool, role, householdID string, loc *time.Location, start, end time.Time) ([]Event, error) {
	events, _, err := queryOccurrencesAs(ctx, pool, eventView{role: role}, householdID, loc, nil, start, end)
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(events))
	for _, ev := range events {
		// A bill-like entry (#4943) is not an appointment: it claims a bill,
		// and family_overview reports it under the bills guard instead
		// (BillLikeEntriesBetween). Leaving it here is exactly how a briefing
		// came to say a paid bill was "still unpaid" (war game R1 G2).
		if !isBillEvent(ev) && !ev.billLike && eventOverlapsRange(ev, start, end) {
			out = append(out, ev)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return startsBefore(out[i].Start, out[j].Start) })
	return out, nil
}

// isBillEvent reports whether ev is a bill's mirror event on the Bills
// calendar (#4485, bill.syncBillEvent). agenda_get and family_overview list
// bills in their own bills sections (with paid state), and an all-day due
// date is not a scheduling conflict, so those leave these events out;
// event_find still shows them.
func isBillEvent(ev Event) bool { return ev.EventType == bill.CalendarEventType }

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

// handlerFunc is the mcp-go tool handler signature.
type handlerFunc = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// ─────────────────────────────────────────────
// calendar_find
// ─────────────────────────────────────────────

// NewCalendarFindTool creates calendar_find (replaces list_calendars).
func NewCalendarFindTool(pool *pgxpool.Pool) (mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("calendar_find", toolOptions(classR, "Find calendars",
		mcp.WithDescription("List the family's calendars (id, name, color, whose it is). You can also pass a calendar name wherever calendar_id is asked for."),
		respond.ListSchema[Calendar](),
	)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return authRequired(), nil
		}
		// A calendar is listed only when the role may see its kind: the Bills
		// calendar is money and is not a sitter's business (#4808).
		view, bad := eventViewFor(ctx, pool, claims)
		if bad != nil {
			return bad, nil
		}
		cals, err := scanCalendars(ctx, pool,
			`SELECT id::text, name, type::text, color, property_id::text
			   FROM calendars WHERE household_id = $1 ORDER BY name`, claims.HouseholdID)
		if err != nil {
			slog.Error("calendar_find: query error", "error", err)
			return respond.Failed("list calendars", err), nil
		}
		cals = view.keepCalendars(cals)
		if len(cals) == 0 {
			return respond.List("The family has no calendars yet.", cals, respond.Page{}), nil
		}
		names := make([]string, len(cals))
		for i, c := range cals {
			names[i] = c.Name
		}
		return respond.List(fmt.Sprintf("Found %d calendars: %s.", len(cals), strings.Join(names, ", ")), cals, respond.Page{},
			fmt.Sprintf("Pass a calendar name (e.g. %q) or id as calendar_id to event_create or event_find.", cals[0].Name)), nil
	}
	return tool, handler
}

// ─────────────────────────────────────────────
// event_find
// ─────────────────────────────────────────────

const maxFindRangeDays = 366

// NewEventFindTool creates event_find (replaces list_events and
// check_conflicts: from/to = a proposed time lists what it overlaps).
func NewEventFindTool(pool *pgxpool.Pool) (mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("event_find", toolOptions(classR, "Find events",
		mcp.WithDescription("Find events overlapping a range, by text or calendar. Use from/to = a proposed time to check conflicts. Pass id for one event. Times are in the family timezone."),
		mcp.WithString("id", mcp.Description("One event's id; the other filters are ignored")),
		mcp.WithString("from", mcp.Description("Range start: date or time with offset, e.g. 2026-11-18. Default today")),
		mcp.WithString("to", mcp.Description("Range end: a date is inclusive, a time exclusive. Default from + 7 days")),
		mcp.WithString("query", mcp.Description("Words to match in title, description or location, e.g. dentist")),
		mcp.WithString("calendar_id", mcp.Description("Calendar name or id, e.g. Family")),
		mcp.WithNumber("limit", mcp.Description("Max events to return (default 25, max 100)"), mcp.Min(1), mcp.Max(maxLimit)),
		mcp.WithString("cursor", mcp.Description("Cursor from the previous result for the next page")),
		respond.ListSchema[Event](),
	)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return authRequired(), nil
		}
		// The caller's role, re-read from memberships per call, decides which
		// records are visible: a bill's mirror event carries the bill's name and
		// "Amount: $X" and is not shown to a role that may not see money (#4808).
		view, bad := eventViewFor(ctx, pool, claims)
		if bad != nil {
			return bad, nil
		}
		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)
		args := req.GetArguments()

		if id := strings.TrimSpace(getStringArg(args, "id")); id != "" {
			if bad := checkUUID("id", id, "event_find"); bad != nil {
				return bad, nil
			}
			r, err := scanEventRow(pool.QueryRow(ctx,
				`SELECT `+eventColumns+` FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id
				  WHERE c.household_id = $1 AND co.id = $2`, claims.HouseholdID, id))
			if errors.Is(err, pgx.ErrNoRows) {
				return respond.NotFound("event", id, "Call event_find with from/to or query to find the event_id."), nil
			}
			if err != nil {
				return respond.Failed("read the event", err), nil
			}
			// A retired entry (#4943) is absent here too, with the same fix a
			// deleted one gets: it is not bill state and not an appointment.
			if r.SupersededAt != nil {
				return respond.NotFound("event", id,
					"That calendar entry was retired because a bill is the record for it; use bill_find for the bill."), nil
			}
			// A record the role may not see is treated as absent, like a
			// calendar of another household (#4807's denial semantics).
			if !view.rowVisible(r) {
				return respond.NotFound("event", id, "Call event_find with from/to or query to find the event_id."), nil
			}
			ev, err := parseEventFromRow(r, loc)
			if err != nil {
				return respond.Failed("read the event", err), nil
			}
			return respond.List(fmt.Sprintf("Found %q, %s.", ev.Title, describeWhen(*ev, loc)), []Event{*ev}, respond.Page{},
				eventHints(*ev)...), nil
		}

		limit, offset, bad := pageArgs(args)
		if bad != nil {
			return bad, nil
		}
		from := todayIn(loc)
		if s := getStringArg(args, "from"); strings.TrimSpace(s) != "" {
			if from, bad = parseBound("from", s, false, loc); bad != nil {
				return bad, nil
			}
		}
		to := from.AddDate(0, 0, 7)
		if s := getStringArg(args, "to"); strings.TrimSpace(s) != "" {
			if to, bad = parseBound("to", s, true, loc); bad != nil {
				return bad, nil
			}
		}
		if !to.After(from) {
			return respond.Invalid("to", "to must be after from.",
				"Pass a later to. A date-only to includes that whole day, so from=to=2026-11-18 is one day.",
				map[string]any{"from": from.In(loc).Format(dateLayout), "to": from.In(loc).Format(dateLayout)}), nil
		}
		if to.Sub(from) > maxFindRangeDays*24*time.Hour {
			return respond.Invalid("to", fmt.Sprintf("The range is longer than %d days.", maxFindRangeDays),
				"Search a shorter range, or use query to find a specific event.",
				map[string]any{"from": from.In(loc).Format(dateLayout), "to": from.AddDate(0, 1, 0).In(loc).Format(dateLayout)}), nil
		}

		var calendarIDs []string
		calRef := strings.TrimSpace(getStringArg(args, "calendar_id"))
		if calRef != "" {
			cal, bad := resolveCalendar(ctx, pool, claims.HouseholdID, "calendar_id", calRef)
			if bad != nil {
				return bad, nil
			}
			calendarIDs = []string{cal.ID}
		}

		events, truncated, err := queryOccurrencesAs(ctx, pool, view, claims.HouseholdID, loc, calendarIDs, from, to)
		if err != nil {
			slog.Error("event_find: query error", "error", err)
			return respond.Failed("find events", err), nil
		}
		query := getStringArg(args, "query")
		matched := make([]Event, 0, len(events))
		for _, ev := range events {
			if eventOverlapsRange(ev, from, to) && matchesQuery(ev, query) {
				matched = append(matched, ev)
			}
		}
		slices.SortStableFunc(matched, func(a, b Event) int {
			switch {
			case startsBefore(a.Start, b.Start):
				return -1
			case startsBefore(b.Start, a.Start):
				return 1
			}
			return 0
		})

		total := len(matched)
		lo := min(offset, total)
		hi := min(lo+limit, total)
		page := matched[lo:hi]

		where := describeRange(from, to, loc)
		if strings.TrimSpace(query) != "" {
			where += fmt.Sprintf(" matching %q", strings.TrimSpace(query))
		}
		var summary string
		switch {
		case total == 0:
			summary = fmt.Sprintf("No events %s.", where)
		case len(page) == total:
			summary = fmt.Sprintf("Found %d %s %s.", total, plural(total, "event", "events"), where)
		default:
			summary = fmt.Sprintf("Showing events %d–%d of %d %s.", lo+1, hi, total, where)
		}
		// Page.Total counts this page plus what is left after it, so the
		// last page is not marked truncated.
		p := respond.Page{Total: total - lo}
		var next []string
		if hi < total {
			p.NextCursor = respond.EncodeCursor(hi)
			p.Narrow = fmt.Sprintf("%d more. Pass next_cursor as cursor with the same filters, or narrow from/to, query or calendar_id.", total-hi)
		}
		if len(truncated) > 0 {
			summary += " " + truncatedNote(truncated)
		}
		if len(page) > 0 {
			next = append(next, eventHints(page[0])...)
		} else {
			next = append(next, "Add one with event_create, or widen from/to.")
		}
		return respond.List(summary, page, p, next...), nil
	}
	return tool, handler
}

// truncatedNote tells the caller which series were cut at
// maxSeriesOccurrences occurrences.
func truncatedNote(titles []string) string {
	quoted := make([]string, len(titles))
	for i, t := range titles {
		quoted[i] = strconv.Quote(t)
	}
	return fmt.Sprintf("%s %s more than %d times in this range; only the first %d are listed, so ask for a shorter range to see the rest.",
		strings.Join(quoted, ", "), plural(len(titles), "repeats", "repeat"), maxSeriesOccurrences, maxSeriesOccurrences)
}

// matchesQuery reports whether every word of q appears (case-insensitive)
// in the event's title, description, location or calendar name.
func matchesQuery(ev Event, q string) bool {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return true
	}
	hay := strings.ToLower(strings.Join([]string{ev.Title, ev.Description, ev.Location, ev.CalendarName}, " "))
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// describeRange words a [from, to) range for summaries: "on Wed Nov 18",
// "from Wed Nov 18 to Tue Nov 24", or exact times when not whole days.
func describeRange(from, to time.Time, loc *time.Location) string {
	f, t := from.In(loc), to.In(loc)
	isMidnight := func(x time.Time) bool { return x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 }
	if isMidnight(f) && isMidnight(t) {
		last := t.AddDate(0, 0, -1)
		if !last.After(f) {
			return "on " + f.Format("Mon Jan 2")
		}
		return "from " + f.Format("Mon Jan 2") + " to " + last.Format("Mon Jan 2")
	}
	return "between " + f.Format("Mon Jan 2 3:04 PM") + " and " + t.Format("Mon Jan 2 3:04 PM")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// eventHints are the next steps for one event.
func eventHints(ev Event) []string {
	if ev.Recurring {
		day := ev.LocalDate
		if ev.RecurrenceID != "" {
			day = ev.RecurrenceID
		}
		return []string{
			fmt.Sprintf("Change one occurrence with event_update event_id=%s scope=this occurrence_date=%s; scope=all changes every one.", ev.ID, day),
			fmt.Sprintf("Skip one with event_delete event_id=%s scope=this occurrence_date=%s; scope=all deletes the series.", ev.ID, day),
		}
	}
	return []string{
		fmt.Sprintf("Change it with event_update event_id=%s.", ev.ID),
		fmt.Sprintf("Remove it with event_delete event_id=%s.", ev.ID),
	}
}

// conflictHints are the next steps after a create or a move: one per
// conflict (at most 2), or the plain event hints when there are none.
func conflictHints(ev Event, conflicts []ConflictEvent, loc *time.Location) []string {
	if len(conflicts) == 0 {
		return eventHints(ev)
	}
	var out []string
	for i, c := range conflicts {
		if i == 2 {
			break
		}
		when := describeWhen(Event{Start: c.Start, End: c.End, AllDay: c.AllDay}, loc)
		out = append(out, fmt.Sprintf("Overlaps %q %s; move it with event_update event_id=%s.", c.Title, when, ev.ID))
	}
	return append(out, "Tell the user about the overlap; the event was saved.")
}

// eventRange is the [start, end) span an event occupies, for conflict checks.
func eventRange(ev Event) (time.Time, time.Time, bool) {
	s, e, err := parseEventTime(ev)
	return s, e, err == nil && e.After(s)
}

// event_create, event_update and event_delete are in events_write.go.

func exampleTimePlus(loc *time.Location, d time.Duration) string {
	t, _ := time.Parse(time.RFC3339, exampleTime(loc))
	return t.Add(d).In(loc).Format(time.RFC3339)
}

// conflictsFor runs findConflicts over every household calendar and turns a
// failure into an empty list plus a conflicts_error message.
func conflictsFor(ctx context.Context, q querier, householdID string, loc *time.Location, start, end time.Time, excludeID string) ([]ConflictEvent, string) {
	conflicts, err := findConflicts(ctx, q, householdID, loc, nil, start, end, excludeID)
	if err != nil {
		slog.Warn("calendar: conflict check failed", "event_id", excludeID, "error", err)
		return []ConflictEvent{}, "The overlap check failed; check with event_find from/to."
	}
	return conflicts, ""
}

// recentDuplicate finds an event on calendarID with the same title, times
// and all-day flag created within duplicateWindow. Errors only disable the
// guard.
func recentDuplicate(ctx context.Context, q querier, calendarID, title, start, end string, allDay bool) (eventRow, bool) {
	row, err := scanEventRow(q.QueryRow(ctx,
		`SELECT `+eventColumns+` FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id
		  WHERE co.calendar_id = $1
		    AND co.created_at > NOW() - $6::interval
		    AND co.ical_data::jsonb->>'title' = $2
		    AND co.starts_at = $3::timestamptz
		    AND co.ends_at = $4::timestamptz
		    AND COALESCE((co.ical_data::jsonb->>'all_day')::boolean, false) = $5
		  ORDER BY co.created_at DESC LIMIT 1`,
		calendarID, title, start, end, allDay, duplicateWindow))
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("event_create: duplicate check failed", "error", err)
		}
		return eventRow{}, false
	}
	return row, true
}

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

// ─────────────────────────────────────────────
// calendar_find_free_time
// ─────────────────────────────────────────────

const (
	defaultSlotStartHour = 8
	defaultSlotEndHour   = 20
	maxSlotRangeDays     = 31
)

// NewCalendarFindFreeTimeTool creates calendar_find_free_time (replaces
// find_available_slots).
//
// Every day's window and query bounds are built with time.Date in the
// household's timezone, so a DST day is 23 or 25 hours long, and every
// returned time carries the household's offset.
func NewCalendarFindFreeTimeTool(pool *pgxpool.Pool) (mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("calendar_find_free_time", toolOptions(classR, "Find free time",
		mcp.WithDescription("Find free time slots across a date range in the family timezone, e.g. a 2-hour gap any evening next week. One call covers the whole range. All-day events don't block."),
		mcp.WithString("from", mcp.Description("First day, YYYY-MM-DD, e.g. 2026-11-18"), mcp.Required()),
		mcp.WithNumber("duration_minutes", mcp.Description("Minimum free minutes, e.g. 120"), mcp.Required(), mcp.Min(1)),
		mcp.WithString("to", mcp.Description(fmt.Sprintf("Last day, inclusive (default from; at most %d days)", maxSlotRangeDays))),
		mcp.WithNumber("start_hour", mcp.Description(fmt.Sprintf("Local hour the day opens, 0-23 (default %d)", defaultSlotStartHour)), mcp.Min(0), mcp.Max(23)),
		mcp.WithNumber("end_hour", mcp.Description(fmt.Sprintf("Local hour the day closes, 1-24 (default %d)", defaultSlotEndHour)), mcp.Min(1), mcp.Max(24)),
		mcp.WithArray("calendar_ids", mcp.Description("Only these calendars (names or ids); default all"), mcp.WithStringItems()),
		respond.ListSchema[AvailableSlot](),
	)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return authRequired(), nil
		}
		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)
		args := req.GetArguments()

		fromArg := getStringArg(args, "from")
		if strings.TrimSpace(fromArg) == "" {
			return respond.Invalid("from", "from is required.", "Pass the first day as YYYY-MM-DD.",
				map[string]any{"from": exampleDate(loc), "duration_minutes": 60}), nil
		}
		firstDay, bad := parseDay("from", fromArg, loc)
		if bad != nil {
			return bad, nil
		}
		lastDay := firstDay
		if toArg := getStringArg(args, "to"); strings.TrimSpace(toArg) != "" {
			if lastDay, bad = parseDay("to", toArg, loc); bad != nil {
				return bad, nil
			}
		}
		days := daysInRange(firstDay, lastDay, loc)
		if len(days) == 0 {
			return respond.Invalid("to", "to must not be before from.", "Pass to on or after from (both YYYY-MM-DD).",
				map[string]any{"from": fromArg, "to": fromArg, "duration_minutes": 60}), nil
		}
		if len(days) > maxSlotRangeDays {
			return respond.Invalid("to", fmt.Sprintf("The range is %d days; at most %d are allowed.", len(days), maxSlotRangeDays),
				"Search a shorter range.", map[string]any{"from": fromArg, "to": firstDay.AddDate(0, 0, 6).Format(dateLayout), "duration_minutes": 60}), nil
		}

		durationMin := getFloat64Arg(args, "duration_minutes", 0)
		if durationMin <= 0 {
			return respond.Invalid("duration_minutes", "duration_minutes must be a positive number of minutes.",
				"Pass how long the free slot must be, e.g. 60.", map[string]any{"from": fromArg, "duration_minutes": 60}), nil
		}
		duration := time.Duration(durationMin * float64(time.Minute))

		startHour, endHour, err := slotHours(args)
		if err != nil {
			return respond.Invalid("start_hour", err.Error(), "Pass whole local hours with start_hour < end_hour, e.g. 17 and 21.",
				map[string]any{"from": fromArg, "duration_minutes": 60, "start_hour": 17, "end_hour": 21}), nil
		}

		calendarIDs, bad := resolveCalendars(ctx, pool, claims.HouseholdID, "calendar_ids", getStringSliceArg(args, "calendar_ids"))
		if bad != nil {
			return bad, nil
		}

		// Query from local midnight of the first day to local midnight after
		// the last day. time.Date normalises day+1, so DST days are covered
		// exactly (no midnight+24h drift).
		queryStart := days[0]
		last := days[len(days)-1]
		queryEnd := time.Date(last.Year(), last.Month(), last.Day()+1, 0, 0, 0, 0, loc)

		role, _, bad := callerRoleOf(ctx, pool, claims)
		if bad != nil {
			return bad, nil
		}
		events, _, err := queryOccurrencesAs(ctx, pool, eventView{role: role}, claims.HouseholdID, loc, calendarIDs, queryStart, queryEnd)
		if err != nil {
			return respond.Failed("read the calendar", err), nil
		}
		busy := make([]timeInterval, 0, len(events))
		for _, ev := range events {
			// All-day events (bills, birthdays, trips) are free time, as in
			// calendar apps. Only free-time search skips them: event_find
			// and event_create's conflicts (findConflicts) still report them.
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
			slots = append(slots, freeSlots(busy, windowStart, windowEnd, duration, day, loc)...)
		}

		where := describeRange(days[0], queryEnd, loc)
		window := fmt.Sprintf("between %02d:00 and %02d:00", startHour, endHour)
		if len(slots) == 0 {
			return respond.List(fmt.Sprintf("No free %v-minute slot %s %s.", durationMin, where, window), slots, respond.Page{},
				"Try a wider from/to, a shorter duration_minutes or other start_hour/end_hour."), nil
		}
		first := slots[0]
		bookStart, _ := time.Parse(time.RFC3339, first.Start)
		return respond.List(fmt.Sprintf("Found %d free %s of at least %v minutes %s %s.", len(slots), plural(len(slots), "slot", "slots"), durationMin, where, window),
			slots, respond.Page{},
			fmt.Sprintf("Book one with event_create start=%s end=%s.", first.Start, bookStart.Add(duration).In(loc).Format(time.RFC3339))), nil
	}
	return tool, handler
}

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
// windowEnd) that no busy interval covers, dated by day. Every time is
// formatted in loc, so slot offsets are consistent whatever offset the
// events were stored in.
func freeSlots(busy []timeInterval, windowStart, windowEnd time.Time, duration time.Duration, day time.Time, loc *time.Location) []AvailableSlot {
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
				LocalDate: day.Format(dateLayout),
				Weekday:   day.Weekday().String(),
				Start:     from.In(loc).Format(time.RFC3339),
				End:       to.In(loc).Format(time.RFC3339),
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
