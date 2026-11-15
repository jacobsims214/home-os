package calendar

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/testutil"
)

// seedCalendar inserts a family calendar for the household and returns its id.
func seedCalendar(t *testing.T, pool *pgxpool.Pool, householdID uuid.UUID) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', $2) RETURNING id`,
		householdID, uuid.NewString(),
	).Scan(&id); err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	return id
}

// seedEvent inserts a calendar object with the given uid and raw ical_data
// JSON and returns its id. A uid different from the id mimics an event
// created over CalDAV.
func seedEvent(t *testing.T, pool *pgxpool.Pool, calendarID, uid, icalData string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type) VALUES ($1, $2, $3, 'etag-0', 'event') RETURNING id`,
		calendarID, uid, icalData,
	).Scan(&id); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	return id
}

func callTool(t *testing.T, handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error), ctx context.Context, args map[string]any) (string, bool) {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = "update_event"
	req.Params.Arguments = args
	res, err := handler(ctx, req)
	if err != nil {
		t.Fatalf("update_event: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("want 1 content item, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	return text.Text, res.IsError
}

func ptr(s string) *string { return &s }

// Raw VCALENDARs as a phone would PUT them over CalDAV (CRLF line endings).
const (
	weeklyRaw = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:soccer@example.com\r\n" +
		"DTSTART:20261115T150000Z\r\nDTEND:20261115T160000Z\r\nRRULE:FREQ=WEEKLY;BYDAY=TU\r\n" +
		"SUMMARY:Game\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	overrideRaw = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:soccer@example.com\r\n" +
		"RECURRENCE-ID;TZID=America/New_York:20261117T100000\r\n" +
		"DTSTART:20261115T150000Z\r\nDTEND:20261115T160000Z\r\nSUMMARY:Game\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	singleRaw = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:game@example.com\r\n" +
		"DTSTART:20261115T150000Z\r\nDTEND:20261115T160000Z\r\nSUMMARY:Game\r\n" +
		"DESCRIPTION:bring the RRULE:FREQ=WEEKLY printout\r\n  RRULE:FREQ=DAILY folded text\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
)

// setRecurrence sets the migration-036 columns on a seeded event.
func setRecurrence(t *testing.T, pool *pgxpool.Pool, eventID string, rrule, icalRaw *string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE calendar_objects SET rrule = $1, ical_raw = $2 WHERE id = $3`, rrule, icalRaw, eventID,
	); err != nil {
		t.Fatalf("set recurrence: %v", err)
	}
}

type storedRow struct {
	IcalData, Etag, EventType string
	IcalRaw, Rrule            string
	UpdatedAt                 time.Time
}

func readRow(t *testing.T, pool *pgxpool.Pool, eventID string) storedRow {
	t.Helper()
	var r storedRow
	if err := pool.QueryRow(context.Background(),
		`SELECT ical_data, etag, event_type, COALESCE(ical_raw, ''), COALESCE(rrule, ''), updated_at FROM calendar_objects WHERE id = $1`,
		eventID,
	).Scan(&r.IcalData, &r.Etag, &r.EventType, &r.IcalRaw, &r.Rrule, &r.UpdatedAt); err != nil {
		t.Fatalf("read event row: %v", err)
	}
	return r
}

// TestIsRecurringEvent covers recurrence detection without a database.
func TestIsRecurringEvent(t *testing.T) {
	tests := []struct {
		name    string
		rrule   *string
		icalRaw *string
		want    bool
	}{
		{name: "both NULL (pre-036 row)", want: false},
		{name: "empty rrule, no raw", rrule: ptr("  "), want: false},
		{name: "rrule set", rrule: ptr("FREQ=DAILY"), want: true},
		{name: "raw with RRULE", icalRaw: ptr(weeklyRaw), want: true},
		{name: "raw with RRULE, LF endings", icalRaw: ptr(strings.ReplaceAll(weeklyRaw, "\r\n", "\n")), want: true},
		{name: "raw with RECURRENCE-ID override", icalRaw: ptr(overrideRaw), want: true},
		{name: "raw with RDATE", icalRaw: ptr("BEGIN:VEVENT\r\nRDATE:20261120T150000Z\r\nEND:VEVENT\r\n"), want: true},
		{name: "lower-case property name", icalRaw: ptr("BEGIN:VEVENT\nrrule:FREQ=WEEKLY\nEND:VEVENT\n"), want: true},
		{name: "single event mentioning RRULE in text", icalRaw: ptr(singleRaw), want: false},
		{name: "empty raw", icalRaw: ptr(""), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRecurringEvent(tt.rrule, tt.icalRaw); got != tt.want {
				t.Errorf("isRecurringEvent = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDeleteEvent checks delete_event removes a whole series and says so.
func TestDeleteEvent(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	calendarID := seedCalendar(t, pool, hh.HouseholdID)
	_, handler := NewDeleteEventTool(pool)
	const stored = `{"title":"Game","start":"2026-11-15T15:00:00Z","end":"2026-11-15T16:00:00Z","all_day":false}`

	tests := []struct {
		name       string
		rrule      *string
		icalRaw    *string
		wantSeries bool
	}{
		{name: "one-off event", wantSeries: false},
		{name: "recurring series", rrule: ptr("FREQ=WEEKLY"), icalRaw: ptr(weeklyRaw), wantSeries: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uid := "caldav-" + uuid.NewString() + "@example.com"
			eventID := seedEvent(t, pool, calendarID, uid, stored)
			setRecurrence(t, pool, eventID, tt.rrule, tt.icalRaw)

			text, isErr := callTool(t, handler, ctx, map[string]any{"event_id": eventID})
			if isErr {
				t.Fatalf("unexpected error: %s", text)
			}
			var res struct {
				Success       bool   `json:"success"`
				SeriesDeleted bool   `json:"series_deleted"`
				Note          string `json:"note"`
			}
			if err := json.Unmarshal([]byte(text), &res); err != nil {
				t.Fatalf("decode %q: %v", text, err)
			}
			if !res.Success || res.SeriesDeleted != tt.wantSeries {
				t.Errorf("result = %+v, want success and series_deleted=%v", res, tt.wantSeries)
			}
			if tt.wantSeries && !strings.Contains(res.Note, "whole series") {
				t.Errorf("note = %q, want it to say the whole series was deleted", res.Note)
			}
			var n int
			if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM calendar_objects WHERE id = $1`, eventID).Scan(&n); err != nil {
				t.Fatalf("count: %v", err)
			}
			if n != 0 {
				t.Error("event row still exists")
			}
			var action string
			if err := pool.QueryRow(context.Background(),
				`SELECT action FROM calendar_changes WHERE calendar_id = $1 AND event_uid = $2`, calendarID, uid,
			).Scan(&action); err != nil || action != "delete" {
				t.Errorf("delete change = %q, %v", action, err)
			}
		})
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

// TestUpdateEvent runs update_event against a real, migrated Postgres.
func TestUpdateEvent(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)
	calendarID := seedCalendar(t, pool, hh.HouseholdID)

	_, handler := NewUpdateEventTool(pool)

	// The stored event: 15:00–16:00 UTC (one hour).
	const stored = `{"sequence":3,"title":"Game","description":"","start":"2026-11-15T15:00:00Z","end":"2026-11-15T16:00:00Z","all_day":false,"location":"Field 2","color":"","event_type":""}`

	tests := []struct {
		name      string
		args      map[string]any
		wantStart string // RFC 3339 instant
		wantEnd   string
		wantTitle string
		wantErr   string
	}{
		{
			name: "explicit end_time is stored exactly (war game 16:20-17:50)",
			args: map[string]any{
				"start_time": "2026-11-15T16:20:00-05:00",
				"end_time":   "2026-11-15T17:50:00-05:00",
			},
			wantStart: "2026-11-15T16:20:00-05:00",
			wantEnd:   "2026-11-15T17:50:00-05:00",
			wantTitle: "Game",
		},
		{
			name:      "start-only move later keeps the duration",
			args:      map[string]any{"start_time": "2026-11-15T18:30:00Z"},
			wantStart: "2026-11-15T18:30:00Z",
			wantEnd:   "2026-11-15T19:30:00Z",
			wantTitle: "Game",
		},
		{
			name:      "start-only move earlier keeps the duration",
			args:      map[string]any{"start_time": "2026-11-15T09:15:00-05:00"},
			wantStart: "2026-11-15T14:15:00Z",
			wantEnd:   "2026-11-15T15:15:00Z",
			wantTitle: "Game",
		},
		{
			name:      "end-only change keeps the start",
			args:      map[string]any{"end_time": "2026-11-15T17:45:00Z"},
			wantStart: "2026-11-15T15:00:00Z",
			wantEnd:   "2026-11-15T17:45:00Z",
			wantTitle: "Game",
		},
		{
			name:      "title-only change leaves times alone",
			args:      map[string]any{"title": "Final"},
			wantStart: "2026-11-15T15:00:00Z",
			wantEnd:   "2026-11-15T16:00:00Z",
			wantTitle: "Final",
		},
		{
			name:    "end before start is rejected",
			args:    map[string]any{"start_time": "2026-11-15T18:00:00Z", "end_time": "2026-11-15T17:00:00Z"},
			wantErr: "end_time must be after start_time",
		},
		{
			name:    "end-only before stored start is rejected",
			args:    map[string]any{"end_time": "2026-11-15T14:00:00Z"},
			wantErr: "end_time must be after start_time",
		},
		{
			name:    "malformed start_time is rejected",
			args:    map[string]any{"start_time": "tomorrow 4pm"},
			wantErr: "invalid start_time format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uid := "caldav-" + uuid.NewString() + "@example.com"
			eventID := seedEvent(t, pool, calendarID, uid, stored)

			var ctagBefore string
			if err := pool.QueryRow(context.Background(), `SELECT ctag FROM calendars WHERE id = $1`, calendarID).Scan(&ctagBefore); err != nil {
				t.Fatalf("read ctag: %v", err)
			}

			args := map[string]any{"event_id": eventID}
			for k, v := range tt.args {
				args[k] = v
			}
			text, isErr := callTool(t, handler, ctx, args)

			if tt.wantErr != "" {
				if !isErr || !strings.Contains(text, tt.wantErr) {
					t.Fatalf("want error containing %q, got isError=%v %q", tt.wantErr, isErr, text)
				}
				var stillStored string
				if err := pool.QueryRow(context.Background(), `SELECT ical_data FROM calendar_objects WHERE id = $1`, eventID).Scan(&stillStored); err != nil {
					t.Fatalf("read event: %v", err)
				}
				if stillStored != stored {
					t.Errorf("rejected update changed the event: %s", stillStored)
				}
				return
			}
			if isErr {
				t.Fatalf("unexpected error: %s", text)
			}

			// The response is the full updated event.
			var got Event
			if err := json.Unmarshal([]byte(text), &got); err != nil {
				t.Fatalf("decode response %q: %v", text, err)
			}
			if got.ID != eventID || got.CalendarID != calendarID || got.CalendarName != "Family" {
				t.Errorf("response identity = %+v", got)
			}
			if got.Title != tt.wantTitle || got.Location != "Field 2" || got.EventType != "event" {
				t.Errorf("response fields = %+v", got)
			}
			if !mustTime(t, got.Start).Equal(mustTime(t, tt.wantStart)) {
				t.Errorf("response start = %s, want %s", got.Start, tt.wantStart)
			}
			if !mustTime(t, got.End).Equal(mustTime(t, tt.wantEnd)) {
				t.Errorf("response end = %s, want %s", got.End, tt.wantEnd)
			}

			// The stored row matches, keeps keys MCP doesn't model, and bumps sequence.
			var icalData string
			if err := pool.QueryRow(context.Background(), `SELECT ical_data FROM calendar_objects WHERE id = $1`, eventID).Scan(&icalData); err != nil {
				t.Fatalf("read event: %v", err)
			}
			var doc struct {
				Sequence *int   `json:"sequence"`
				Start    string `json:"start"`
				End      string `json:"end"`
			}
			if err := json.Unmarshal([]byte(icalData), &doc); err != nil {
				t.Fatalf("decode stored %q: %v", icalData, err)
			}
			if !mustTime(t, doc.Start).Equal(mustTime(t, tt.wantStart)) || !mustTime(t, doc.End).Equal(mustTime(t, tt.wantEnd)) {
				t.Errorf("stored start/end = %s/%s, want %s/%s", doc.Start, doc.End, tt.wantStart, tt.wantEnd)
			}
			if doc.Sequence == nil || *doc.Sequence != 4 {
				t.Errorf("stored sequence = %v, want 4", doc.Sequence)
			}

			// The change is recorded against the event's uid, and the ctag moves.
			var action string
			if err := pool.QueryRow(context.Background(),
				`SELECT action FROM calendar_changes WHERE calendar_id = $1 AND event_uid = $2`,
				calendarID, uid,
			).Scan(&action); err != nil {
				t.Fatalf("calendar_changes row for uid %s: %v", uid, err)
			}
			if action != "update" {
				t.Errorf("change action = %q, want update", action)
			}
			var byID int
			if err := pool.QueryRow(context.Background(),
				`SELECT count(*) FROM calendar_changes WHERE event_uid = $1`, eventID,
			).Scan(&byID); err != nil {
				t.Fatalf("count changes by id: %v", err)
			}
			if byID != 0 {
				t.Errorf("%d calendar_changes rows recorded with the event id instead of the uid", byID)
			}
			var ctagAfter string
			if err := pool.QueryRow(context.Background(), `SELECT ctag FROM calendars WHERE id = $1`, calendarID).Scan(&ctagAfter); err != nil {
				t.Fatalf("read ctag: %v", err)
			}
			if ctagAfter == ctagBefore {
				t.Error("ctag was not bumped")
			}
		})
	}

	// Recurring events are refused without writing anything (#4629): an
	// edit here would make ical_raw stale and CalDAV would regenerate the
	// event without its RRULE, flattening the series on every phone.
	recurring := []struct {
		name    string
		rrule   *string
		icalRaw *string
	}{
		{name: "rrule column set", rrule: ptr("FREQ=WEEKLY;BYDAY=TU"), icalRaw: ptr(weeklyRaw)},
		{name: "only ical_raw carries RRULE", icalRaw: ptr(weeklyRaw)},
		{name: "ical_raw is an occurrence override (RECURRENCE-ID)", icalRaw: ptr(overrideRaw)},
	}
	for _, tt := range recurring {
		t.Run("recurring event is refused: "+tt.name, func(t *testing.T) {
			uid := "caldav-" + uuid.NewString() + "@example.com"
			eventID := seedEvent(t, pool, calendarID, uid, stored)
			setRecurrence(t, pool, eventID, tt.rrule, tt.icalRaw)
			before := readRow(t, pool, eventID)
			var ctagBefore string
			if err := pool.QueryRow(context.Background(), `SELECT ctag FROM calendars WHERE id = $1`, calendarID).Scan(&ctagBefore); err != nil {
				t.Fatalf("read ctag: %v", err)
			}

			text, isErr := callTool(t, handler, ctx, map[string]any{"event_id": eventID, "title": "Soccer (renamed)"})
			if !isErr {
				t.Fatalf("want an error result, got %q", text)
			}
			for _, want := range []string{"repeats", "phone", "Nothing was changed"} {
				if !strings.Contains(text, want) {
					t.Errorf("error %q does not mention %q", text, want)
				}
			}

			if after := readRow(t, pool, eventID); after != before {
				t.Errorf("refused update changed the row:\nbefore %+v\nafter  %+v", before, after)
			}
			var changes int
			if err := pool.QueryRow(context.Background(),
				`SELECT count(*) FROM calendar_changes WHERE calendar_id = $1 AND event_uid = $2`, calendarID, uid,
			).Scan(&changes); err != nil {
				t.Fatalf("count changes: %v", err)
			}
			if changes != 0 {
				t.Errorf("%d calendar_changes rows added for a refused update", changes)
			}
			var ctagAfter string
			if err := pool.QueryRow(context.Background(), `SELECT ctag FROM calendars WHERE id = $1`, calendarID).Scan(&ctagAfter); err != nil {
				t.Fatalf("read ctag: %v", err)
			}
			if ctagAfter != ctagBefore {
				t.Error("ctag was bumped for a refused update")
			}
		})
	}

	t.Run("non-recurring event with ical_raw still updates", func(t *testing.T) {
		uid := "caldav-" + uuid.NewString() + "@example.com"
		eventID := seedEvent(t, pool, calendarID, uid, stored)
		setRecurrence(t, pool, eventID, nil, ptr(singleRaw))
		text, isErr := callTool(t, handler, ctx, map[string]any{"event_id": eventID, "title": "Final"})
		if isErr {
			t.Fatalf("unexpected error: %s", text)
		}
		var got Event
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatalf("decode response %q: %v", text, err)
		}
		if got.Title != "Final" {
			t.Errorf("title = %q, want Final", got.Title)
		}
		if row := readRow(t, pool, eventID); row.Etag == "etag-0" {
			t.Error("etag was not changed by the update")
		}
	})

	t.Run("another household cannot update the event", func(t *testing.T) {
		eventID := seedEvent(t, pool, calendarID, uuid.NewString(), stored)
		text, isErr := callTool(t, handler, otherCtx, map[string]any{"event_id": eventID, "title": "Hijack"})
		if !isErr || !strings.Contains(text, "event not found") {
			t.Fatalf("want event not found, got isError=%v %q", isErr, text)
		}
	})
}

// TestResolveUpdatedTimes covers the start/end rules without a database.
func TestResolveUpdatedTimes(t *testing.T) {
	str := func(s string) *string { return &s }
	const oldStart, oldEnd = "2026-11-15T15:00:00Z", "2026-11-15T16:30:00Z"

	tests := []struct {
		name               string
		start, end         *string
		wantStart, wantEnd string
		wantErr            bool
	}{
		{name: "neither", wantStart: oldStart, wantEnd: oldEnd},
		{name: "both", start: str("2026-11-16T10:00:00+01:00"), end: str("2026-11-16T12:00:00+01:00"), wantStart: "2026-11-16T09:00:00Z", wantEnd: "2026-11-16T11:00:00Z"},
		{name: "start only keeps 90m", start: str("2026-11-15T20:00:00Z"), wantStart: "2026-11-15T20:00:00Z", wantEnd: "2026-11-15T21:30:00Z"},
		{name: "end only", end: str("2026-11-15T17:00:00Z"), wantStart: oldStart, wantEnd: "2026-11-15T17:00:00Z"},
		{name: "equal times rejected", start: str(oldStart), end: str(oldStart), wantErr: true},
		{name: "naive end rejected", end: str("2026-11-15T17:00:00"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, e, err := resolveUpdatedTimes(oldStart, oldEnd, tt.start, tt.end)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %s/%s", s, e)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := s.Format(time.RFC3339); got != tt.wantStart {
				t.Errorf("start = %s, want %s", got, tt.wantStart)
			}
			if got := e.Format(time.RFC3339); got != tt.wantEnd {
				t.Errorf("end = %s, want %s", got, tt.wantEnd)
			}
		})
	}
}

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return loc
}

func setHouseholdTimezone(t *testing.T, pool *pgxpool.Pool, householdID uuid.UUID, tz string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE households SET timezone = $1 WHERE id = $2`, tz, householdID); err != nil {
		t.Fatalf("set timezone: %v", err)
	}
}

func TestFreeSlots(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	day := time.Date(2026, 11, 16, 0, 0, 0, 0, ny)
	ws := time.Date(2026, 11, 16, 8, 0, 0, 0, ny)
	we := time.Date(2026, 11, 16, 20, 0, 0, 0, ny)
	// Events are stored in UTC (since #4329): 17:30-18:30 EST is 22:30-23:30Z.
	evening := timeInterval{start: mustTime(t, "2026-11-16T22:30:00Z"), end: mustTime(t, "2026-11-16T23:30:00Z")}

	tests := []struct {
		name     string
		busy     []timeInterval
		duration time.Duration
		want     []AvailableSlot
	}{
		{
			name:     "no events gives the whole window",
			duration: 30 * time.Minute,
			want:     []AvailableSlot{{Date: "2026-11-16", Start: "2026-11-16T08:00:00-05:00", End: "2026-11-16T20:00:00-05:00"}},
		},
		{
			name:     "UTC-stored evening event is excluded and slots use the local offset",
			busy:     []timeInterval{evening},
			duration: 30 * time.Minute,
			want: []AvailableSlot{
				{Date: "2026-11-16", Start: "2026-11-16T08:00:00-05:00", End: "2026-11-16T17:30:00-05:00"},
				{Date: "2026-11-16", Start: "2026-11-16T18:30:00-05:00", End: "2026-11-16T20:00:00-05:00"},
			},
		},
		{
			name: "overlapping and out-of-window events merge; short gaps dropped",
			busy: []timeInterval{
				{start: mustTime(t, "2026-11-16T12:00:00Z"), end: mustTime(t, "2026-11-16T14:00:00Z")}, // 07:00-09:00 local
				{start: mustTime(t, "2026-11-16T14:20:00Z"), end: mustTime(t, "2026-11-16T16:00:00Z")}, // 09:20-11:00
				{start: mustTime(t, "2026-11-16T15:00:00Z"), end: mustTime(t, "2026-11-16T17:00:00Z")}, // 10:00-12:00
				{start: mustTime(t, "2026-11-17T02:00:00Z"), end: mustTime(t, "2026-11-17T03:00:00Z")}, // 21:00-22:00, outside
			},
			duration: 30 * time.Minute,
			want:     []AvailableSlot{{Date: "2026-11-16", Start: "2026-11-16T12:00:00-05:00", End: "2026-11-16T20:00:00-05:00"}},
		},
		{
			name:     "fully booked window has no slots",
			busy:     []timeInterval{{start: ws.Add(-time.Hour), end: we.Add(time.Hour)}},
			duration: 30 * time.Minute,
			want:     []AvailableSlot{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := freeSlots(tt.busy, ws, we, tt.duration, day.Format("2006-01-02"), ny)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d slots %+v, want %+v", len(got), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("slot %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestDaysInRangeDST(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	tests := []struct {
		name      string
		first     time.Time
		last      time.Time
		wantDays  []string
		wantHours []float64 // length of each local day
	}{
		{
			name:      "spring forward day is 23 hours",
			first:     time.Date(2026, 3, 7, 0, 0, 0, 0, ny),
			last:      time.Date(2026, 3, 9, 0, 0, 0, 0, ny),
			wantDays:  []string{"2026-03-07", "2026-03-08", "2026-03-09"},
			wantHours: []float64{24, 23, 24},
		},
		{
			name:      "fall back day is 25 hours",
			first:     time.Date(2026, 10, 31, 0, 0, 0, 0, ny),
			last:      time.Date(2026, 11, 2, 0, 0, 0, 0, ny),
			wantDays:  []string{"2026-10-31", "2026-11-01", "2026-11-02"},
			wantHours: []float64{24, 25, 24},
		},
		{
			name:  "end before start is empty",
			first: time.Date(2026, 11, 2, 0, 0, 0, 0, ny),
			last:  time.Date(2026, 11, 1, 0, 0, 0, 0, ny),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			days := daysInRange(tt.first, tt.last, ny)
			if len(days) != len(tt.wantDays) {
				t.Fatalf("got %d days, want %d", len(days), len(tt.wantDays))
			}
			for i, d := range days {
				if got := d.Format("2006-01-02"); got != tt.wantDays[i] {
					t.Errorf("day %d = %s, want %s", i, got, tt.wantDays[i])
				}
				next := time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, ny)
				if h := next.Sub(d).Hours(); h != tt.wantHours[i] {
					t.Errorf("day %s is %v hours, want %v", tt.wantDays[i], h, tt.wantHours[i])
				}
			}
		})
	}
}

func TestSlotHours(t *testing.T) {
	tests := []struct {
		name      string
		args      map[string]any
		wantStart int
		wantEnd   int
		wantErr   bool
	}{
		{name: "defaults", args: map[string]any{}, wantStart: 8, wantEnd: 20},
		{name: "custom", args: map[string]any{"start_hour": 6.0, "end_hour": 22.0}, wantStart: 6, wantEnd: 22},
		{name: "full day", args: map[string]any{"start_hour": 0.0, "end_hour": 24.0}, wantStart: 0, wantEnd: 24},
		{name: "end not after start", args: map[string]any{"start_hour": 10.0, "end_hour": 10.0}, wantErr: true},
		{name: "start out of range", args: map[string]any{"start_hour": 24.0}, wantErr: true},
		{name: "end out of range", args: map[string]any{"end_hour": 25.0}, wantErr: true},
		{name: "fractional hour", args: map[string]any{"start_hour": 8.5}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, e, err := slotHours(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && (s != tt.wantStart || e != tt.wantEnd) {
				t.Errorf("got %d-%d, want %d-%d", s, e, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

// TestFindAvailableSlots runs find_available_slots against a real, migrated Postgres.
func TestFindAvailableSlots(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	calendarID := seedCalendar(t, pool, hh.HouseholdID)

	// 17:30-18:30 EST on 2026-11-17, stored in UTC like every event since #4329.
	seedEvent(t, pool, calendarID, uuid.NewString(),
		`{"title":"Soccer practice","start":"2026-11-17T22:30:00Z","end":"2026-11-17T23:30:00Z"}`)
	// 23:15-23:45 EST on the 25-hour fall-back day 2026-11-01. midnight+24h
	// would end at 23:00 local and miss it.
	seedEvent(t, pool, calendarID, uuid.NewString(),
		`{"title":"Late call","start":"2026-11-02T04:15:00Z","end":"2026-11-02T04:45:00Z"}`)

	// 2026-11-18: an all-day bill (does not block) and a timed event (does).
	billID := seedEvent(t, pool, calendarID, uuid.NewString(),
		`{"title":"Electric bill","start":"2026-11-18T00:00:00Z","end":"2026-11-19T00:00:00Z","all_day":true}`)
	seedEvent(t, pool, calendarID, uuid.NewString(),
		`{"title":"Piano lesson","start":"2026-11-18T22:30:00Z","end":"2026-11-18T23:30:00Z"}`)

	_, handler := NewFindAvailableSlotsTool(pool)

	decode := func(t *testing.T, args map[string]any) []AvailableSlot {
		t.Helper()
		text, isErr := callTool(t, handler, ctx, args)
		if isErr {
			t.Fatalf("tool error: %s", text)
		}
		var slots []AvailableSlot
		if err := json.Unmarshal([]byte(text), &slots); err != nil {
			t.Fatalf("decode %q: %v", text, err)
		}
		return slots
	}

	tests := []struct {
		name string
		args map[string]any
		want []AvailableSlot
	}{
		{
			name: "local evening event is excluded from default 8-20 window",
			args: map[string]any{"date": "2026-11-17", "duration_minutes": 30.0},
			want: []AvailableSlot{
				{Date: "2026-11-17", Start: "2026-11-17T08:00:00-05:00", End: "2026-11-17T17:30:00-05:00"},
				{Date: "2026-11-17", Start: "2026-11-17T18:30:00-05:00", End: "2026-11-17T20:00:00-05:00"},
			},
		},
		{
			name: "range returns slots per day with custom hours in one call",
			args: map[string]any{"date": "2026-11-16", "end_date": "2026-11-18", "duration_minutes": 60.0, "start_hour": 16.0, "end_hour": 19.0},
			want: []AvailableSlot{
				{Date: "2026-11-16", Start: "2026-11-16T16:00:00-05:00", End: "2026-11-16T19:00:00-05:00"},
				{Date: "2026-11-17", Start: "2026-11-17T16:00:00-05:00", End: "2026-11-17T17:30:00-05:00"},
				{Date: "2026-11-18", Start: "2026-11-18T16:00:00-05:00", End: "2026-11-18T17:30:00-05:00"},
			},
		},
		{
			name: "all-day event does not block the day, timed event still does",
			args: map[string]any{"date": "2026-11-18", "duration_minutes": 30.0},
			want: []AvailableSlot{
				{Date: "2026-11-18", Start: "2026-11-18T08:00:00-05:00", End: "2026-11-18T17:30:00-05:00"},
				{Date: "2026-11-18", Start: "2026-11-18T18:30:00-05:00", End: "2026-11-18T20:00:00-05:00"},
			},
		},
		{
			name: "fall-back day covers all 25 hours and sees the late event",
			args: map[string]any{"date": "2026-11-01", "duration_minutes": 30.0, "start_hour": 0.0, "end_hour": 24.0},
			want: []AvailableSlot{
				{Date: "2026-11-01", Start: "2026-11-01T00:00:00-04:00", End: "2026-11-01T23:15:00-05:00"},
			},
		},
		{
			name: "range across DST keeps each day's own offset",
			args: map[string]any{"date": "2026-10-31", "end_date": "2026-11-01", "duration_minutes": 30.0, "start_hour": 9.0, "end_hour": 10.0},
			want: []AvailableSlot{
				{Date: "2026-10-31", Start: "2026-10-31T09:00:00-04:00", End: "2026-10-31T10:00:00-04:00"},
				{Date: "2026-11-01", Start: "2026-11-01T09:00:00-05:00", End: "2026-11-01T10:00:00-05:00"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decode(t, tt.args)
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("slot %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}

	// check_conflicts still reports the all-day event: only free-time search
	// skips all-day events.
	t.Run("check_conflicts still reports all-day overlap", func(t *testing.T) {
		_, check := NewCheckConflictsTool(pool)
		text, isErr := callTool(t, check, ctx, map[string]any{
			"start_time": "2026-11-18T10:00:00-05:00", "end_time": "2026-11-18T11:00:00-05:00",
		})
		if isErr {
			t.Fatalf("check_conflicts error: %s", text)
		}
		if !strings.Contains(text, billID) || !strings.Contains(text, "Electric bill") {
			t.Errorf("check_conflicts = %s, want the all-day Electric bill (%s)", text, billID)
		}
	})

	errTests := []struct {
		name    string
		args    map[string]any
		wantMsg string
	}{
		{"end_date before date", map[string]any{"date": "2026-11-17", "end_date": "2026-11-16", "duration_minutes": 30.0}, "must not be before"},
		{"range too long", map[string]any{"date": "2026-01-01", "end_date": "2026-03-01", "duration_minutes": 30.0}, "at most 31"},
		{"bad hours", map[string]any{"date": "2026-11-17", "duration_minutes": 30.0, "start_hour": 20.0, "end_hour": 8.0}, "end_hour must be after"},
		{"bad end_date", map[string]any{"date": "2026-11-17", "end_date": "11/18", "duration_minutes": 30.0}, "invalid end_date"},
	}
	for _, tt := range errTests {
		t.Run(tt.name, func(t *testing.T) {
			text, isErr := callTool(t, handler, ctx, tt.args)
			if !isErr || !strings.Contains(text, tt.wantMsg) {
				t.Errorf("got isErr=%v %q, want error containing %q", isErr, text, tt.wantMsg)
			}
		})
	}
}

func TestAllDayRange(t *testing.T) {
	tests := []struct {
		name, start, end string
		wantFirst        string // YYYY-MM-DD
		wantEndExcl      string
		wantErr          bool
	}{
		{"bare dates, same day", "2026-11-18", "2026-11-18", "2026-11-18", "2026-11-19", false},
		{"bare end date names the last day", "2026-12-03", "2026-12-05", "2026-12-03", "2026-12-06", false},
		{"clock times on one day normalise to the whole day", "2026-11-18T09:00:00-05:00", "2026-11-18T23:59:00-05:00", "2026-11-18", "2026-11-19", false},
		{"UTC midnight is that date, not the previous evening", "2026-11-18T00:00:00Z", "2026-11-19T00:00:00Z", "2026-11-18", "2026-11-19", false},
		{"exclusive next-midnight end (field trip)", "2026-12-03T00:00:00", "2026-12-05T00:00:00", "2026-12-03", "2026-12-05", false},
		{"non-midnight end names the last day", "2026-12-03", "2026-12-05T08:00:00-05:00", "2026-12-03", "2026-12-06", false},
		{"empty end is one day", "2026-11-18", "", "2026-11-18", "2026-11-19", false},
		{"end before start", "2026-11-18", "2026-11-17", "", "", true},
		{"garbage", "11/18", "2026-11-18", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, endExcl, err := allDayRange(tt.start, tt.end)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %v..%v", first, endExcl)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := first.Format(dateLayout); got != tt.wantFirst {
				t.Errorf("first = %s, want %s", got, tt.wantFirst)
			}
			if got := endExcl.Format(dateLayout); got != tt.wantEndExcl {
				t.Errorf("endExcl = %s, want %s", got, tt.wantEndExcl)
			}
		})
	}
}

func TestLocalizeAllDay(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	tokyo := mustLoc(t, "Asia/Tokyo")
	tests := []struct {
		name               string
		ev                 Event
		loc                *time.Location
		wantStart, wantEnd string
	}{
		{"CalDAV-style UTC midnight in New York",
			Event{AllDay: true, Start: "2026-11-18T00:00:00Z", End: "2026-11-19T00:00:00Z"}, ny,
			"2026-11-18T00:00:00-05:00", "2026-11-19T00:00:00-05:00"},
		{"CalDAV-style UTC midnight in Tokyo",
			Event{AllDay: true, Start: "2026-11-18T00:00:00Z", End: "2026-11-19T00:00:00Z"}, tokyo,
			"2026-11-18T00:00:00+09:00", "2026-11-19T00:00:00+09:00"},
		{"legacy local-midnight offset keeps its written date",
			Event{AllDay: true, Start: "2026-11-18T00:00:00+09:00", End: "2026-11-19T00:00:00+09:00"}, ny,
			"2026-11-18T00:00:00-05:00", "2026-11-19T00:00:00-05:00"},
		{"legacy inclusive same-day end",
			Event{AllDay: true, Start: "2026-11-18T00:00:00Z", End: "2026-11-18T23:59:59Z"}, ny,
			"2026-11-18T00:00:00-05:00", "2026-11-19T00:00:00-05:00"},
		{"timed events are untouched",
			Event{Start: "2026-11-18T14:00:00Z", End: "2026-11-18T15:00:00Z"}, ny,
			"2026-11-18T14:00:00Z", "2026-11-18T15:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := tt.ev
			localizeAllDay(&ev, tt.loc)
			if ev.Start != tt.wantStart || ev.End != tt.wantEnd {
				t.Errorf("got %s..%s, want %s..%s", ev.Start, ev.End, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

func TestResolveUpdatedAllDay(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	str := func(s string) *string { return &s }
	allDay3 := eventJSON{AllDay: true, Start: "2026-12-03T00:00:00Z", End: "2026-12-06T00:00:00Z"}
	tests := []struct {
		name               string
		existing           eventJSON
		newStart, newEnd   *string
		wantStart, wantEnd string
	}{
		{"start-only move keeps the number of days", allDay3, str("2026-12-10"), nil,
			"2026-12-10T00:00:00Z", "2026-12-13T00:00:00Z"},
		{"end-only change keeps the start", allDay3, nil, str("2026-12-04"),
			"2026-12-03T00:00:00Z", "2026-12-05T00:00:00Z"},
		{"timed 9pm New York event switched to all-day lands on its local date",
			eventJSON{Start: "2026-11-19T02:00:00Z", End: "2026-11-19T03:00:00Z"}, nil, nil,
			"2026-11-18T00:00:00Z", "2026-11-19T00:00:00Z"},
		{"clock times on one day normalise", allDay3, str("2026-11-18T09:00:00-05:00"), str("2026-11-18T23:59:00-05:00"),
			"2026-11-18T00:00:00Z", "2026-11-19T00:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, e, err := resolveUpdatedAllDay(tt.existing, ny, tt.newStart, tt.newEnd)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if s != tt.wantStart || e != tt.wantEnd {
				t.Errorf("got %s..%s, want %s..%s", s, e, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

// TestAllDayEvents covers all-day events end to end in a New York household:
// create stores the CalDAV form, and list_events, check_conflicts and the
// daily briefing see the event on its own day only.
func TestAllDayEvents(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	calendarID := seedCalendar(t, pool, hh.HouseholdID)

	_, create := NewCreateEventTool(pool)
	_, list := NewListEventsTool(pool)
	_, conflicts := NewCheckConflictsTool(pool)
	_, update := NewUpdateEventTool(pool)
	_, briefing := NewGetDailyBriefingTool(pool)

	storedTimes := func(t *testing.T, id string) (string, string, bool) {
		t.Helper()
		var raw string
		if err := pool.QueryRow(context.Background(), `SELECT ical_data FROM calendar_objects WHERE id = $1`, id).Scan(&raw); err != nil {
			t.Fatalf("load event: %v", err)
		}
		var ej eventJSON
		if err := json.Unmarshal([]byte(raw), &ej); err != nil {
			t.Fatalf("decode ical_data: %v", err)
		}
		return ej.Start, ej.End, ej.AllDay
	}
	createID := func(t *testing.T, args map[string]any) string {
		t.Helper()
		text, isErr := callTool(t, create, ctx, args)
		if isErr {
			t.Fatalf("create_event: %s", text)
		}
		var out struct{ ID string }
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("decode %q: %v", text, err)
		}
		return out.ID
	}
	listTitles := func(t *testing.T, date string) []string {
		t.Helper()
		text, isErr := callTool(t, list, ctx, map[string]any{"start_date": date, "end_date": date})
		if isErr {
			t.Fatalf("list_events: %s", text)
		}
		var evs []Event
		if err := json.Unmarshal([]byte(text), &evs); err != nil {
			t.Fatalf("decode %q: %v", text, err)
		}
		titles := make([]string, 0, len(evs))
		for _, ev := range evs {
			titles = append(titles, ev.Title)
		}
		return titles
	}

	// A bill created with clock times 09:00-23:59 is normalised to the day,
	// stored the way CalDAV stores DTSTART;VALUE=DATE.
	billID := createID(t, map[string]any{
		"calendar_id": calendarID, "title": "Electric bill", "all_day": true,
		"start_time": "2026-11-18T09:00:00-05:00", "end_time": "2026-11-18T23:59:00-05:00",
	})
	if s, e, ad := storedTimes(t, billID); s != "2026-11-18T00:00:00Z" || e != "2026-11-19T00:00:00Z" || !ad {
		t.Errorf("stored bill = %s..%s all_day=%v, want 2026-11-18T00:00:00Z..2026-11-19T00:00:00Z all_day=true", s, e, ad)
	}

	// A field trip 12/3-12/4 given with the exclusive midnight end.
	createID(t, map[string]any{
		"calendar_id": calendarID, "title": "Field trip", "all_day": true,
		"start_time": "2026-12-03", "end_time": "2026-12-05T00:00:00-05:00",
	})

	for _, tt := range []struct {
		date string
		want []string
	}{
		{"2026-11-17", []string{}},
		{"2026-11-18", []string{"Electric bill"}},
		{"2026-11-19", []string{}},
		{"2026-12-04", []string{"Field trip"}},
		{"2026-12-05", []string{}},
	} {
		t.Run("list_events "+tt.date, func(t *testing.T) {
			got := listTitles(t, tt.date)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("list_events returns local midnights", func(t *testing.T) {
		text, _ := callTool(t, list, ctx, map[string]any{"start_date": "2026-11-18", "end_date": "2026-11-18"})
		var evs []Event
		_ = json.Unmarshal([]byte(text), &evs)
		if len(evs) != 1 || evs[0].Start != "2026-11-18T00:00:00-05:00" || evs[0].End != "2026-11-19T00:00:00-05:00" {
			t.Errorf("got %+v", evs)
		}
	})

	for _, tt := range []struct {
		name, start, end string
		want             int
	}{
		{"late evening on the day conflicts", "2026-11-18T22:00:00-05:00", "2026-11-18T23:00:00-05:00", 1},
		{"just after the day does not", "2026-11-19T00:30:00-05:00", "2026-11-19T01:00:00-05:00", 0},
		{"previous evening does not", "2026-11-17T20:00:00-05:00", "2026-11-17T21:00:00-05:00", 0},
	} {
		t.Run("check_conflicts "+tt.name, func(t *testing.T) {
			text, isErr := callTool(t, conflicts, ctx, map[string]any{"start_time": tt.start, "end_time": tt.end})
			if isErr {
				t.Fatalf("check_conflicts: %s", text)
			}
			var got []ConflictEvent
			if err := json.Unmarshal([]byte(text), &got); err != nil {
				t.Fatalf("decode %q: %v", text, err)
			}
			if len(got) != tt.want {
				t.Errorf("got %d conflicts %+v, want %d", len(got), got, tt.want)
			}
		})
	}

	t.Run("daily briefing shows the bill on its day only", func(t *testing.T) {
		text, _ := callTool(t, briefing, ctx, map[string]any{"date": "2026-11-18"})
		if !strings.Contains(text, "All day** (Wednesday 2026-11-18) — Electric bill") {
			t.Errorf("11/18 briefing missing bill:\n%s", text)
		}
		text, _ = callTool(t, briefing, ctx, map[string]any{"date": "2026-11-17"})
		if strings.Contains(text, "Electric bill") {
			t.Errorf("11/17 briefing shows bill:\n%s", text)
		}
	})

	t.Run("update_event keeps all-day events normalised", func(t *testing.T) {
		text, isErr := callTool(t, update, ctx, map[string]any{"event_id": billID, "start_time": "2026-11-20T10:00:00-05:00"})
		if isErr {
			t.Fatalf("update_event: %s", text)
		}
		if s, e, _ := storedTimes(t, billID); s != "2026-11-20T00:00:00Z" || e != "2026-11-21T00:00:00Z" {
			t.Errorf("stored after move = %s..%s", s, e)
		}
		var ev Event
		_ = json.Unmarshal([]byte(text), &ev)
		if ev.Start != "2026-11-20T00:00:00-05:00" || ev.End != "2026-11-21T00:00:00-05:00" {
			t.Errorf("returned %s..%s", ev.Start, ev.End)
		}
		if text, isErr := callTool(t, update, ctx, map[string]any{"event_id": billID, "all_day": false}); !isErr || !strings.Contains(text, "start_time and end_time") {
			t.Errorf("turning off all_day without times: isErr=%v %q", isErr, text)
		}
	})

	t.Run("switching a timed event to all-day uses its local date", func(t *testing.T) {
		// 21:00-22:00 New York on 11/22, stored as UTC (02:00Z on 11/23).
		id := seedEvent(t, pool, calendarID, uuid.NewString(),
			`{"title":"Movie","start":"2026-11-23T02:00:00Z","end":"2026-11-23T03:00:00Z","all_day":false}`)
		if text, isErr := callTool(t, update, ctx, map[string]any{"event_id": id, "all_day": true}); isErr {
			t.Fatalf("update_event: %s", text)
		}
		if s, e, ad := storedTimes(t, id); s != "2026-11-22T00:00:00Z" || e != "2026-11-23T00:00:00Z" || !ad {
			t.Errorf("stored = %s..%s all_day=%v", s, e, ad)
		}
	})
}

// TestAllDayEventsEastOfUTC checks a CalDAV-created all-day event (stored as
// UTC midnight) in a Tokyo household: it must not bleed into the next day,
// which the plain timestamptz comparison would allow.
func TestAllDayEventsEastOfUTC(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "Asia/Tokyo")
	calendarID := seedCalendar(t, pool, hh.HouseholdID)
	seedEvent(t, pool, calendarID, uuid.NewString(),
		`{"title":"Holiday","start":"2026-11-23T00:00:00Z","end":"2026-11-24T00:00:00Z","all_day":true,"sequence":0}`)

	_, list := NewListEventsTool(pool)
	for date, want := range map[string]int{"2026-11-22": 0, "2026-11-23": 1, "2026-11-24": 0} {
		text, isErr := callTool(t, list, ctx, map[string]any{"start_date": date, "end_date": date})
		if isErr {
			t.Fatalf("list_events: %s", text)
		}
		var evs []Event
		if err := json.Unmarshal([]byte(text), &evs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(evs) != want {
			t.Errorf("%s: got %d events %+v, want %d", date, len(evs), evs, want)
		}
	}

	_, conflicts := NewCheckConflictsTool(pool)
	// 23:00-23:30 Tokyo on 11/23 is 14:00Z, inside both readings; 08:00 on
	// 11/24 Tokyo (23:00Z on 11/23) is inside the UTC day but not the local one.
	for _, tt := range []struct {
		start, end string
		want       int
	}{
		{"2026-11-23T23:00:00+09:00", "2026-11-23T23:30:00+09:00", 1},
		{"2026-11-24T08:00:00+09:00", "2026-11-24T08:30:00+09:00", 0},
	} {
		text, _ := callTool(t, conflicts, ctx, map[string]any{"start_time": tt.start, "end_time": tt.end})
		var got []ConflictEvent
		_ = json.Unmarshal([]byte(text), &got)
		if len(got) != tt.want {
			t.Errorf("%s: got %d conflicts, want %d", tt.start, len(got), tt.want)
		}
	}
}

// TestCreateEventConflicts checks that create_event reports overlapping
// events on any household calendar, using check_conflicts' rule (half-open
// ranges, all-day events as household-local days), and still creates the
// event.
func TestCreateEventConflicts(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	family := seedCalendar(t, pool, hh.HouseholdID)
	work := seedCalendar(t, pool, hh.HouseholdID)

	// 10:00-11:00 New York on 11/18, stored as UTC, on the other calendar.
	dentist := seedEvent(t, pool, work, uuid.NewString(),
		`{"title":"Dentist","start":"2026-11-18T15:00:00Z","end":"2026-11-18T16:00:00Z","all_day":false}`)
	// All-day on 11/20, stored the CalDAV way.
	trip := seedEvent(t, pool, family, uuid.NewString(),
		`{"title":"Trip","start":"2026-11-20T00:00:00Z","end":"2026-11-21T00:00:00Z","all_day":true}`)

	// Another household's overlapping event must never show up.
	_, other := testutil.SeedHousehold(t, pool)
	otherCal := seedCalendar(t, pool, other.HouseholdID)
	seedEvent(t, pool, otherCal, uuid.NewString(),
		`{"title":"Stranger","start":"2026-11-18T15:00:00Z","end":"2026-11-18T16:00:00Z","all_day":false}`)

	_, create := NewCreateEventTool(pool)

	tests := []struct {
		name string
		args map[string]any
		want []string // conflict ids, in order
	}{
		{
			name: "timed overlap on another calendar",
			args: map[string]any{"start_time": "2026-11-18T10:30:00-05:00", "end_time": "2026-11-18T11:30:00-05:00"},
			want: []string{dentist},
		},
		{
			name: "touching end is not a conflict",
			args: map[string]any{"start_time": "2026-11-18T11:00:00-05:00", "end_time": "2026-11-18T12:00:00-05:00"},
			want: []string{},
		},
		{
			name: "no overlap",
			args: map[string]any{"start_time": "2026-11-19T10:00:00-05:00", "end_time": "2026-11-19T11:00:00-05:00"},
			want: []string{},
		},
		{
			// 21:00 New York on 11/20 is 02:00Z on 11/21: outside the UTC
			// day but inside the household-local all-day event.
			name: "timed inside local all-day event",
			args: map[string]any{"start_time": "2026-11-20T21:00:00-05:00", "end_time": "2026-11-20T22:00:00-05:00"},
			want: []string{trip},
		},
		{
			// 20:00 New York on 11/19 is 01:00Z on 11/20: inside the UTC
			// day but before the household-local all-day event.
			name: "timed before local all-day event",
			args: map[string]any{"start_time": "2026-11-19T20:00:00-05:00", "end_time": "2026-11-19T21:00:00-05:00"},
			want: []string{},
		},
		{
			name: "all-day create overlaps timed event",
			args: map[string]any{"start_time": "2026-11-18", "end_time": "2026-11-18", "all_day": true},
			want: []string{dentist},
		},
		{
			name: "all-day create next to all-day event",
			args: map[string]any{"start_time": "2026-11-21", "end_time": "2026-11-21", "all_day": true},
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{"calendar_id": family, "title": "New " + tt.name}
			for k, v := range tt.args {
				args[k] = v
			}
			text, isErr := callTool(t, create, ctx, args)
			if isErr {
				t.Fatalf("create_event: %s", text)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(text), &raw); err != nil {
				t.Fatalf("decode %q: %v", text, err)
			}
			if string(raw["conflicts"]) == "null" || raw["conflicts"] == nil {
				t.Fatalf("conflicts must be a list, got %q", text)
			}
			var out createEventResponse
			if err := json.Unmarshal([]byte(text), &out); err != nil {
				t.Fatalf("decode %q: %v", text, err)
			}
			if out.ID == "" || out.ConflictsError != "" {
				t.Fatalf("unexpected response %q", text)
			}
			// The event was created even when it conflicts.
			var n int
			if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM calendar_objects WHERE id = $1`, out.ID).Scan(&n); err != nil || n != 1 {
				t.Fatalf("event %s not stored (n=%d, err=%v)", out.ID, n, err)
			}
			got := make([]string, 0, len(out.Conflicts))
			for _, c := range out.Conflicts {
				if c.ID == out.ID {
					t.Errorf("new event listed as its own conflict")
				}
				got = append(got, c.ID)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("conflicts = %v, want %v (%s)", got, tt.want, text)
			}
			// Remove it so later cases only see the seeded events.
			if _, err := pool.Exec(context.Background(), `DELETE FROM calendar_objects WHERE id = $1`, out.ID); err != nil {
				t.Fatalf("cleanup: %v", err)
			}
		})
	}
}

func TestSetLocalDay(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	tokyo := mustLoc(t, "Asia/Tokyo")
	tests := []struct {
		name                  string
		ev                    Event
		loc                   *time.Location
		wantDate, wantWeekday string
	}{
		// 2026-11-18 is a Wednesday.
		{"timed event stored as UTC", Event{Start: "2026-11-18T15:00:00Z"}, ny, "2026-11-18", "Wednesday"},
		{"late evening New York is next day in UTC", Event{Start: "2026-11-19T04:30:00Z"}, ny, "2026-11-18", "Wednesday"},
		{"same instant in Tokyo", Event{Start: "2026-11-19T04:30:00Z"}, tokyo, "2026-11-19", "Thursday"},
		{"offset input is read as an instant", Event{Start: "2026-11-18T23:30:00-05:00"}, ny, "2026-11-18", "Wednesday"},
		{"DST spring-forward day", Event{Start: "2026-03-08T07:30:00Z"}, ny, "2026-03-08", "Sunday"},
		{"localized all-day event", Event{AllDay: true, Start: "2026-11-18T00:00:00Z", End: "2026-11-19T00:00:00Z"}, tokyo, "2026-11-18", "Wednesday"},
		{"unreadable start leaves fields empty", Event{Start: "garbage"}, ny, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := tt.ev
			localizeAllDay(&ev, tt.loc)
			setLocalDay(&ev, tt.loc)
			if ev.LocalDate != tt.wantDate || ev.Weekday != tt.wantWeekday {
				t.Errorf("got %q %q, want %q %q", ev.LocalDate, ev.Weekday, tt.wantDate, tt.wantWeekday)
			}
		})
	}
}

// TestEventLocalDay checks local_date/weekday on every event payload for a
// New York household, including events whose UTC date differs from the
// household date.
func TestEventLocalDay(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	calendarID := seedCalendar(t, pool, hh.HouseholdID)

	// Wed 11/18 10:00 New York.
	dentist := seedEvent(t, pool, calendarID, uuid.NewString(),
		`{"title":"Dentist","start":"2026-11-18T15:00:00Z","end":"2026-11-18T16:00:00Z","all_day":false}`)
	// Wed 11/18 23:00 New York = Thu 04:00Z.
	late := seedEvent(t, pool, calendarID, uuid.NewString(),
		`{"title":"Late call","start":"2026-11-19T04:00:00Z","end":"2026-11-19T04:30:00Z","all_day":false}`)
	// All-day Wed 11/18, stored the CalDAV way.
	bill := seedEvent(t, pool, calendarID, uuid.NewString(),
		`{"title":"Electric bill","start":"2026-11-18T00:00:00Z","end":"2026-11-19T00:00:00Z","all_day":true}`)
	// Fri 11/20 09:00 New York.
	soccer := seedEvent(t, pool, calendarID, uuid.NewString(),
		`{"title":"Soccer","start":"2026-11-20T14:00:00Z","end":"2026-11-20T15:00:00Z","all_day":false}`)

	type day struct{ date, weekday string }
	want := map[string]day{
		dentist: {"2026-11-18", "Wednesday"},
		late:    {"2026-11-18", "Wednesday"},
		bill:    {"2026-11-18", "Wednesday"},
		soccer:  {"2026-11-20", "Friday"},
	}
	checkKeys := func(t *testing.T, text string) {
		t.Helper()
		var raw []map[string]any
		if err := json.Unmarshal([]byte(text), &raw); err != nil {
			t.Fatalf("decode %q: %v", text, err)
		}
		for _, m := range raw {
			for _, k := range []string{"local_date", "weekday"} {
				if _, ok := m[k].(string); !ok {
					t.Errorf("event %v missing string %q", m["id"], k)
				}
			}
		}
	}

	t.Run("list_events", func(t *testing.T) {
		_, list := NewListEventsTool(pool)
		text, isErr := callTool(t, list, ctx, map[string]any{"start_date": "2026-11-18", "end_date": "2026-11-20"})
		if isErr {
			t.Fatalf("list_events: %s", text)
		}
		checkKeys(t, text)
		var evs []Event
		if err := json.Unmarshal([]byte(text), &evs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(evs) != len(want) {
			t.Fatalf("got %d events, want %d: %s", len(evs), len(want), text)
		}
		for _, ev := range evs {
			if w := want[ev.ID]; ev.LocalDate != w.date || ev.Weekday != w.weekday {
				t.Errorf("%s: got %s %s, want %s %s", ev.Title, ev.LocalDate, ev.Weekday, w.date, w.weekday)
			}
		}
	})

	t.Run("check_conflicts", func(t *testing.T) {
		_, check := NewCheckConflictsTool(pool)
		text, isErr := callTool(t, check, ctx, map[string]any{
			"start_time": "2026-11-18T22:00:00-05:00", "end_time": "2026-11-18T23:45:00-05:00",
		})
		if isErr {
			t.Fatalf("check_conflicts: %s", text)
		}
		checkKeys(t, text)
		var cs []ConflictEvent
		_ = json.Unmarshal([]byte(text), &cs)
		if len(cs) != 2 {
			t.Fatalf("want bill + late call, got %s", text)
		}
		for _, c := range cs {
			if c.LocalDate != "2026-11-18" || c.Weekday != "Wednesday" {
				t.Errorf("%s: got %s %s", c.Title, c.LocalDate, c.Weekday)
			}
		}
	})

	t.Run("update_event", func(t *testing.T) {
		_, update := NewUpdateEventTool(pool)
		text, isErr := callTool(t, update, ctx, map[string]any{"event_id": soccer, "title": "Soccer practice"})
		if isErr {
			t.Fatalf("update_event: %s", text)
		}
		var ev Event
		_ = json.Unmarshal([]byte(text), &ev)
		if ev.LocalDate != "2026-11-20" || ev.Weekday != "Friday" {
			t.Errorf("got %s %s (%s)", ev.LocalDate, ev.Weekday, text)
		}
	})

	t.Run("get_daily_briefing", func(t *testing.T) {
		_, briefing := NewGetDailyBriefingTool(pool)
		text, isErr := callTool(t, briefing, ctx, map[string]any{"date": "2026-11-18"})
		if isErr {
			t.Fatalf("get_daily_briefing: %s", text)
		}
		if !strings.Contains(text, "Wednesday, November 18, 2026") {
			t.Errorf("header missing weekday: %s", text)
		}
		for _, line := range []string{
			"- **10:00 AM** (Wednesday 2026-11-18) — Dentist",
			"- **11:00 PM** (Wednesday 2026-11-18) — Late call",
			"- **All day** (Wednesday 2026-11-18) — Electric bill",
		} {
			if !strings.Contains(text, line) {
				t.Errorf("briefing missing %q:\n%s", line, text)
			}
		}
		if strings.Contains(text, "Soccer") || strings.Contains(text, "Thursday") {
			t.Errorf("briefing has events from other days:\n%s", text)
		}
	})
}
