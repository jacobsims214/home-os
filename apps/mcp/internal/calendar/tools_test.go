package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
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

// bind builds a tool and returns its handler wrapped so every result is
// checked against the tool's advertised outputSchema, with the text block
// equal to structuredContent (the respond contract).
func bind(t *testing.T, ctor func(*pgxpool.Pool) (mcp.Tool, handlerFunc), pool *pgxpool.Pool) handlerFunc {
	t.Helper()
	tool, h := ctor(pool)
	rs := outputSchema(t, tool)
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := h(ctx, req)
		if err == nil && res != nil {
			checkResult(t, tool.Name, rs, res)
		}
		return res, err
	}
}

// outputSchema resolves the outputSchema a tool advertises in tools/list.
func outputSchema(t *testing.T, tool mcp.Tool) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal tool: %v", err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal tool: %v", err)
	}
	if len(raw.OutputSchema) == 0 {
		t.Fatalf("tool %s declares no outputSchema", tool.Name)
	}
	var sch jsonschema.Schema
	if err := json.Unmarshal(raw.OutputSchema, &sch); err != nil {
		t.Fatalf("unmarshal outputSchema: %v", err)
	}
	rs, err := sch.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve outputSchema of %s: %v", tool.Name, err)
	}
	return rs
}

// checkResult validates one result's wire shape. It uses Errorf (not
// Fatalf) because it may run inside a subtest of t.
func checkResult(t *testing.T, name string, rs *jsonschema.Resolved, res *mcp.CallToolResult) {
	if len(res.Content) != 1 {
		t.Errorf("%s: want 1 content block, got %d", name, len(res.Content))
		return
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Errorf("%s: want TextContent, got %T", name, res.Content[0])
		return
	}
	structured, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Errorf("%s: marshal structuredContent: %v", name, err)
		return
	}
	if string(structured) != text.Text {
		t.Errorf("%s: text and structuredContent differ:\n%s\n%s", name, text.Text, structured)
	}
	var wire any
	if err := json.Unmarshal(structured, &wire); err != nil {
		t.Errorf("%s: structuredContent is not JSON: %v", name, err)
		return
	}
	if err := rs.Validate(wire); err != nil {
		t.Errorf("%s: result does not match outputSchema: %v\n%s", name, err, structured)
	}
}

// field returns one top-level key of a result envelope ("data", "items").
func field(t *testing.T, text, key string) []byte {
	t.Helper()
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", text, err)
	}
	v, ok := env[key]
	if !ok {
		t.Fatalf("envelope has no %q: %s", key, text)
	}
	return v
}

func data(t *testing.T, text string) []byte  { t.Helper(); return field(t, text, "data") }
func items(t *testing.T, text string) []byte { t.Helper(); return field(t, text, "items") }

// writtenEvent decodes event_create / event_update data.
func writtenEvent(t *testing.T, text string) eventWriteResult {
	t.Helper()
	var w eventWriteResult
	if err := json.Unmarshal(data(t, text), &w); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return w
}

// eventItems decodes an event_find result.
func eventItems(t *testing.T, text string) []Event {
	t.Helper()
	var evs []Event
	if err := json.Unmarshal(items(t, text), &evs); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return evs
}

func callTool(t *testing.T, handler handlerFunc, ctx context.Context, args map[string]any) (string, bool) {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	res, err := handler(ctx, req)
	if err != nil {
		t.Fatalf("handler returned a protocol error: %v", err)
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
	handler := bind(t, NewEventDeleteTool, pool)
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

			args := map[string]any{"event_id": eventID}
			if tt.wantSeries {
				args["scope"] = "all"
			}
			text, isErr := callTool(t, handler, ctx, args)
			if isErr {
				t.Fatalf("unexpected error: %s", text)
			}
			var res eventDeleteResult
			if err := json.Unmarshal(data(t, text), &res); err != nil {
				t.Fatalf("decode %q: %v", text, err)
			}
			if res.Event.ID != eventID || res.Event.Title != "Game" || res.SeriesDeleted != tt.wantSeries {
				t.Errorf("result = %+v, want event %s and series_deleted=%v", res, eventID, tt.wantSeries)
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
			// A repeat is a corrective not-found, and deletes nothing more.
			if text, isErr := callTool(t, handler, ctx, map[string]any{"event_id": eventID}); !isErr || !strings.Contains(text, "No event with id") {
				t.Errorf("second delete: isErr=%v %s", isErr, text)
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

	handler := bind(t, NewEventUpdateTool, pool)

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
				"start": "2026-11-15T16:20:00-05:00",
				"end":   "2026-11-15T17:50:00-05:00",
			},
			wantStart: "2026-11-15T16:20:00-05:00",
			wantEnd:   "2026-11-15T17:50:00-05:00",
			wantTitle: "Game",
		},
		{
			name:      "start-only move later keeps the duration",
			args:      map[string]any{"start": "2026-11-15T18:30:00Z"},
			wantStart: "2026-11-15T18:30:00Z",
			wantEnd:   "2026-11-15T19:30:00Z",
			wantTitle: "Game",
		},
		{
			name:      "start-only move earlier keeps the duration",
			args:      map[string]any{"start": "2026-11-15T09:15:00-05:00"},
			wantStart: "2026-11-15T14:15:00Z",
			wantEnd:   "2026-11-15T15:15:00Z",
			wantTitle: "Game",
		},
		{
			name:      "end-only change keeps the start",
			args:      map[string]any{"end": "2026-11-15T17:45:00Z"},
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
			args:    map[string]any{"start": "2026-11-15T18:00:00Z", "end": "2026-11-15T17:00:00Z"},
			wantErr: "end must be after start",
		},
		{
			name:    "end-only before stored start is rejected",
			args:    map[string]any{"end": "2026-11-15T14:00:00Z"},
			wantErr: "end must be after start",
		},
		{
			name:    "naive start gets the family offset as its fix",
			args:    map[string]any{"start": "2026-11-15T16:20:00"},
			wantErr: "has no UTC offset",
		},
		{
			name:    "malformed start is rejected",
			args:    map[string]any{"start": "tomorrow 4pm"},
			wantErr: "is not an ISO 8601 time",
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

			// The response is the full updated event, in local time.
			w := writtenEvent(t, text)
			got := w.Event
			if w.Conflicts == nil {
				t.Errorf("conflicts must be a list: %s", text)
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

	// A raw the JSON no longer matches (here: the stored JSON has a
	// location the raw lacks) is stale: CalDAV serves the JSON as a single
	// event, so the update treats it as one and rewrites ical_raw and
	// rrule from the result (#4465).
	stale := []struct {
		name    string
		rrule   *string
		icalRaw *string
	}{
		{name: "rrule column set", rrule: ptr("FREQ=WEEKLY;BYDAY=TU"), icalRaw: ptr(weeklyRaw)},
		{name: "only ical_raw carries RRULE", icalRaw: ptr(weeklyRaw)},
	}
	for _, tt := range stale {
		t.Run("stale recurring raw is rewritten: "+tt.name, func(t *testing.T) {
			uid := "caldav-" + uuid.NewString() + "@example.com"
			eventID := seedEvent(t, pool, calendarID, uid, stored)
			setRecurrence(t, pool, eventID, tt.rrule, tt.icalRaw)
			text, isErr := callTool(t, handler, ctx, map[string]any{"event_id": eventID, "title": "Soccer (renamed)"})
			if isErr {
				t.Fatalf("unexpected error: %s", text)
			}
			row := readRow(t, pool, eventID)
			if row.Rrule != "" || strings.Contains(row.IcalRaw, "RRULE") || !strings.Contains(row.IcalRaw, "SUMMARY:Soccer (renamed)") ||
				!strings.Contains(row.IcalRaw, "UID:"+uid) || !strings.Contains(row.IcalRaw, "LOCATION:Field 2") {
				t.Errorf("raw not regenerated from the JSON: rrule=%q\n%s", row.Rrule, row.IcalRaw)
			}
		})
	}

	// A raw holding only overrides (an invitation to one occurrence) can't
	// be rewritten without losing it: refused, nothing written.
	t.Run("override-only raw is refused", func(t *testing.T) {
		uid := "caldav-" + uuid.NewString() + "@example.com"
		eventID := seedEvent(t, pool, calendarID, uid, stored)
		setRecurrence(t, pool, eventID, nil, ptr(overrideRaw))
		before := readRow(t, pool, eventID)
		text, isErr := callTool(t, handler, ctx, map[string]any{"event_id": eventID, "title": "Soccer (renamed)"})
		if !isErr || !strings.Contains(text, "Nothing was changed") || !strings.Contains(text, "phone") {
			t.Fatalf("want a refusal, got isErr=%v %s", isErr, text)
		}
		if after := readRow(t, pool, eventID); after != before {
			t.Errorf("refused update changed the row:\nbefore %+v\nafter  %+v", before, after)
		}
	})

	t.Run("non-recurring event with ical_raw still updates", func(t *testing.T) {
		uid := "caldav-" + uuid.NewString() + "@example.com"
		eventID := seedEvent(t, pool, calendarID, uid, stored)
		setRecurrence(t, pool, eventID, nil, ptr(singleRaw))
		text, isErr := callTool(t, handler, ctx, map[string]any{"event_id": eventID, "title": "Final"})
		if isErr {
			t.Fatalf("unexpected error: %s", text)
		}
		got := writtenEvent(t, text).Event
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
		if !isErr || !strings.Contains(text, "No event with id") {
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
			want:     []AvailableSlot{{LocalDate: "2026-11-16", Weekday: "Monday", Start: "2026-11-16T08:00:00-05:00", End: "2026-11-16T20:00:00-05:00"}},
		},
		{
			name:     "UTC-stored evening event is excluded and slots use the local offset",
			busy:     []timeInterval{evening},
			duration: 30 * time.Minute,
			want: []AvailableSlot{
				{LocalDate: "2026-11-16", Weekday: "Monday", Start: "2026-11-16T08:00:00-05:00", End: "2026-11-16T17:30:00-05:00"},
				{LocalDate: "2026-11-16", Weekday: "Monday", Start: "2026-11-16T18:30:00-05:00", End: "2026-11-16T20:00:00-05:00"},
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
			want:     []AvailableSlot{{LocalDate: "2026-11-16", Weekday: "Monday", Start: "2026-11-16T12:00:00-05:00", End: "2026-11-16T20:00:00-05:00"}},
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
			got := freeSlots(tt.busy, ws, we, tt.duration, day, ny)
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

	handler := bind(t, NewCalendarFindFreeTimeTool, pool)

	decode := func(t *testing.T, args map[string]any) []AvailableSlot {
		t.Helper()
		text, isErr := callTool(t, handler, ctx, args)
		if isErr {
			t.Fatalf("tool error: %s", text)
		}
		var slots []AvailableSlot
		if err := json.Unmarshal(items(t, text), &slots); err != nil {
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
			args: map[string]any{"from": "2026-11-17", "duration_minutes": 30.0},
			want: []AvailableSlot{
				{LocalDate: "2026-11-17", Weekday: "Tuesday", Start: "2026-11-17T08:00:00-05:00", End: "2026-11-17T17:30:00-05:00"},
				{LocalDate: "2026-11-17", Weekday: "Tuesday", Start: "2026-11-17T18:30:00-05:00", End: "2026-11-17T20:00:00-05:00"},
			},
		},
		{
			name: "range returns slots per day with custom hours in one call",
			args: map[string]any{"from": "2026-11-16", "to": "2026-11-18", "duration_minutes": 60.0, "start_hour": 16.0, "end_hour": 19.0},
			want: []AvailableSlot{
				{LocalDate: "2026-11-16", Weekday: "Monday", Start: "2026-11-16T16:00:00-05:00", End: "2026-11-16T19:00:00-05:00"},
				{LocalDate: "2026-11-17", Weekday: "Tuesday", Start: "2026-11-17T16:00:00-05:00", End: "2026-11-17T17:30:00-05:00"},
				{LocalDate: "2026-11-18", Weekday: "Wednesday", Start: "2026-11-18T16:00:00-05:00", End: "2026-11-18T17:30:00-05:00"},
			},
		},
		{
			name: "all-day event does not block the day, timed event still does",
			args: map[string]any{"from": "2026-11-18", "duration_minutes": 30.0},
			want: []AvailableSlot{
				{LocalDate: "2026-11-18", Weekday: "Wednesday", Start: "2026-11-18T08:00:00-05:00", End: "2026-11-18T17:30:00-05:00"},
				{LocalDate: "2026-11-18", Weekday: "Wednesday", Start: "2026-11-18T18:30:00-05:00", End: "2026-11-18T20:00:00-05:00"},
			},
		},
		{
			name: "fall-back day covers all 25 hours and sees the late event",
			args: map[string]any{"from": "2026-11-01", "duration_minutes": 30.0, "start_hour": 0.0, "end_hour": 24.0},
			want: []AvailableSlot{
				{LocalDate: "2026-11-01", Weekday: "Sunday", Start: "2026-11-01T00:00:00-04:00", End: "2026-11-01T23:15:00-05:00"},
			},
		},
		{
			name: "range across DST keeps each day's own offset",
			args: map[string]any{"from": "2026-10-31", "to": "2026-11-01", "duration_minutes": 30.0, "start_hour": 9.0, "end_hour": 10.0},
			want: []AvailableSlot{
				{LocalDate: "2026-10-31", Weekday: "Saturday", Start: "2026-10-31T09:00:00-04:00", End: "2026-10-31T10:00:00-04:00"},
				{LocalDate: "2026-11-01", Weekday: "Sunday", Start: "2026-11-01T09:00:00-05:00", End: "2026-11-01T10:00:00-05:00"},
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

	// event_find (the conflict check) still reports the all-day event:
	// only free-time search skips all-day events.
	t.Run("event_find still reports all-day overlap", func(t *testing.T) {
		find := bind(t, NewEventFindTool, pool)
		text, isErr := callTool(t, find, ctx, map[string]any{
			"from": "2026-11-18T10:00:00-05:00", "to": "2026-11-18T11:00:00-05:00",
		})
		if isErr {
			t.Fatalf("event_find error: %s", text)
		}
		if !strings.Contains(text, billID) || !strings.Contains(text, "Electric bill") {
			t.Errorf("event_find = %s, want the all-day Electric bill (%s)", text, billID)
		}
	})

	t.Run("calendar_ids accepts a calendar name", func(t *testing.T) {
		got := decode(t, map[string]any{"from": "2026-11-17", "duration_minutes": 30.0, "calendar_ids": []any{"family"}})
		if len(got) != 2 {
			t.Errorf("got %+v, want the two slots around soccer practice", got)
		}
	})

	errTests := []struct {
		name    string
		args    map[string]any
		wantMsg string
	}{
		{"end_date before date", map[string]any{"from": "2026-11-17", "to": "2026-11-16", "duration_minutes": 30.0}, "must not be before"},
		{"range too long", map[string]any{"from": "2026-01-01", "to": "2026-03-01", "duration_minutes": 30.0}, "at most 31"},
		{"bad hours", map[string]any{"from": "2026-11-17", "duration_minutes": 30.0, "start_hour": 20.0, "end_hour": 8.0}, "end_hour must be after"},
		{"bad end_date", map[string]any{"from": "2026-11-17", "to": "11/18", "duration_minutes": 30.0}, "is not a date"},
		{"missing duration", map[string]any{"from": "2026-11-17"}, "duration_minutes must be"},
		{"unknown calendar", map[string]any{"from": "2026-11-17", "duration_minutes": 30.0, "calendar_ids": []any{"Nope"}}, "No calendar"},
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

	create := bind(t, NewEventCreateTool, pool)
	list := bind(t, NewEventFindTool, pool)
	update := bind(t, NewEventUpdateTool, pool)
	agenda := bind(t, NewAgendaGetTool, pool)

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
		return writtenEvent(t, text).Event.ID
	}
	listTitles := func(t *testing.T, date string) []string {
		t.Helper()
		text, isErr := callTool(t, list, ctx, map[string]any{"from": date, "to": date})
		if isErr {
			t.Fatalf("event_find: %s", text)
		}
		evs := eventItems(t, text)
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
		"start": "2026-11-18T09:00:00-05:00", "end": "2026-11-18T23:59:00-05:00",
	})
	if s, e, ad := storedTimes(t, billID); s != "2026-11-18T00:00:00Z" || e != "2026-11-19T00:00:00Z" || !ad {
		t.Errorf("stored bill = %s..%s all_day=%v, want 2026-11-18T00:00:00Z..2026-11-19T00:00:00Z all_day=true", s, e, ad)
	}

	// A field trip 12/3-12/4 given with the exclusive midnight end.
	createID(t, map[string]any{
		"calendar_id": calendarID, "title": "Field trip", "all_day": true,
		"start": "2026-12-03", "end": "2026-12-05T00:00:00-05:00",
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
		t.Run("event_find "+tt.date, func(t *testing.T) {
			got := listTitles(t, tt.date)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("event_find returns local midnights", func(t *testing.T) {
		text, _ := callTool(t, list, ctx, map[string]any{"from": "2026-11-18", "to": "2026-11-18"})
		evs := eventItems(t, text)
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
		t.Run("event_find as conflict check "+tt.name, func(t *testing.T) {
			text, isErr := callTool(t, list, ctx, map[string]any{"from": tt.start, "to": tt.end})
			if isErr {
				t.Fatalf("event_find: %s", text)
			}
			got := eventItems(t, text)
			if len(got) != tt.want {
				t.Errorf("got %d conflicts %+v, want %d", len(got), got, tt.want)
			}
		})
	}

	t.Run("agenda shows the all-day bill on its day only", func(t *testing.T) {
		// detail=full: one entry per occurrence with its instant, which is what
		// this test is about (the default summarizes and uses a local day).
		text, _ := callTool(t, agenda, ctx, map[string]any{"from": "2026-11-18", "to": "2026-11-18", "detail": "full"})
		var a agendaResult
		if err := json.Unmarshal(data(t, text), &a); err != nil {
			t.Fatalf("decode %q: %v", text, err)
		}
		if len(a.Events) != 1 || a.Events[0].Title != "Electric bill" || !a.Events[0].AllDay ||
			a.Events[0].LocalDate != "2026-11-18" || a.Events[0].Weekday != "Wednesday" {
			t.Errorf("11/18 agenda events = %+v", a.Events)
		}
		text, _ = callTool(t, agenda, ctx, map[string]any{"from": "2026-11-17", "to": "2026-11-17", "detail": "full"})
		if err := json.Unmarshal(data(t, text), &a); err != nil {
			t.Fatalf("decode %q: %v", text, err)
		}
		// The events section, not the whole result: since #4943 a bill-like
		// entry is *reported* (bill_like_entries, with the reason it is not
		// bill state) on every day of the range, which is the guard against a
		// briefing reading it as a bill. What must not happen is the event
		// itself appearing on the wrong day.
		for _, e := range a.Events {
			if e.Title == "Electric bill" {
				t.Errorf("11/17 agenda lists the 11/18 bill as an event: %+v", a.Events)
			}
		}
	})

	t.Run("update_event keeps all-day events normalised", func(t *testing.T) {
		text, isErr := callTool(t, update, ctx, map[string]any{"event_id": billID, "start": "2026-11-20T10:00:00-05:00"})
		if isErr {
			t.Fatalf("update_event: %s", text)
		}
		if s, e, _ := storedTimes(t, billID); s != "2026-11-20T00:00:00Z" || e != "2026-11-21T00:00:00Z" {
			t.Errorf("stored after move = %s..%s", s, e)
		}
		ev := writtenEvent(t, text).Event
		if ev.Start != "2026-11-20T00:00:00-05:00" || ev.End != "2026-11-21T00:00:00-05:00" {
			t.Errorf("returned %s..%s", ev.Start, ev.End)
		}
		if text, isErr := callTool(t, update, ctx, map[string]any{"event_id": billID, "all_day": false}); !isErr || !strings.Contains(text, "Pass start and end when turning off all_day") {
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

	list := bind(t, NewEventFindTool, pool)
	for date, want := range map[string]int{"2026-11-22": 0, "2026-11-23": 1, "2026-11-24": 0} {
		text, isErr := callTool(t, list, ctx, map[string]any{"from": date, "to": date})
		if isErr {
			t.Fatalf("event_find: %s", text)
		}
		evs := eventItems(t, text)
		if len(evs) != want {
			t.Errorf("%s: got %d events %+v, want %d", date, len(evs), evs, want)
		}
	}

	// 23:00-23:30 Tokyo on 11/23 is 14:00Z, inside both readings; 08:00 on
	// 11/24 Tokyo (23:00Z on 11/23) is inside the UTC day but not the local one.
	for _, tt := range []struct {
		start, end string
		want       int
	}{
		{"2026-11-23T23:00:00+09:00", "2026-11-23T23:30:00+09:00", 1},
		{"2026-11-24T08:00:00+09:00", "2026-11-24T08:30:00+09:00", 0},
	} {
		text, _ := callTool(t, list, ctx, map[string]any{"from": tt.start, "to": tt.end})
		got := eventItems(t, text)
		if len(got) != tt.want {
			t.Errorf("%s: got %d conflicts, want %d", tt.start, len(got), tt.want)
		}
	}
}

// TestCreateEventConflicts checks that event_create reports overlapping
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

	create := bind(t, NewEventCreateTool, pool)

	tests := []struct {
		name string
		args map[string]any
		want []string // conflict ids, in order
	}{
		{
			name: "timed overlap on another calendar",
			args: map[string]any{"start": "2026-11-18T10:30:00-05:00", "end": "2026-11-18T11:30:00-05:00"},
			want: []string{dentist},
		},
		{
			name: "touching end is not a conflict",
			args: map[string]any{"start": "2026-11-18T11:00:00-05:00", "end": "2026-11-18T12:00:00-05:00"},
			want: []string{},
		},
		{
			name: "no overlap",
			args: map[string]any{"start": "2026-11-19T10:00:00-05:00", "end": "2026-11-19T11:00:00-05:00"},
			want: []string{},
		},
		{
			// 21:00 New York on 11/20 is 02:00Z on 11/21: outside the UTC
			// day but inside the household-local all-day event.
			name: "timed inside local all-day event",
			args: map[string]any{"start": "2026-11-20T21:00:00-05:00", "end": "2026-11-20T22:00:00-05:00"},
			want: []string{trip},
		},
		{
			// 20:00 New York on 11/19 is 01:00Z on 11/20: inside the UTC
			// day but before the household-local all-day event.
			name: "timed before local all-day event",
			args: map[string]any{"start": "2026-11-19T20:00:00-05:00", "end": "2026-11-19T21:00:00-05:00"},
			want: []string{},
		},
		{
			name: "all-day create overlaps timed event",
			args: map[string]any{"start": "2026-11-18", "end": "2026-11-18", "all_day": true},
			want: []string{dentist},
		},
		{
			name: "all-day create next to all-day event",
			args: map[string]any{"start": "2026-11-21", "end": "2026-11-21", "all_day": true},
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
			if err := json.Unmarshal(data(t, text), &raw); err != nil {
				t.Fatalf("decode %q: %v", text, err)
			}
			if string(raw["conflicts"]) == "null" || raw["conflicts"] == nil {
				t.Fatalf("conflicts must be a list, got %q", text)
			}
			w := writtenEvent(t, text)
			out := struct {
				ID        string
				Conflicts []ConflictEvent
			}{w.Event.ID, w.Conflicts}
			if out.ID == "" || w.ConflictsError != "" || w.Duplicate {
				t.Fatalf("unexpected response %q", text)
			}
			if len(tt.want) > 0 && !strings.Contains(text, "event_update event_id="+out.ID) {
				t.Errorf("a conflict should hint how to move the new event: %s", text)
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
		if err := json.Unmarshal(items(t, text), &raw); err != nil {
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

	t.Run("event_find", func(t *testing.T) {
		list := bind(t, NewEventFindTool, pool)
		text, isErr := callTool(t, list, ctx, map[string]any{"from": "2026-11-18", "to": "2026-11-20"})
		if isErr {
			t.Fatalf("event_find: %s", text)
		}
		checkKeys(t, text)
		evs := eventItems(t, text)
		// Timed starts come back in the household's offset, sorted by start.
		if len(evs) == 4 && (evs[0].Title != "Electric bill" || evs[1].Start != "2026-11-18T10:00:00-05:00" || evs[2].Start != "2026-11-18T23:00:00-05:00") {
			t.Errorf("want local, start-ordered events, got %s", text)
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

	t.Run("event_find as conflict check", func(t *testing.T) {
		check := bind(t, NewEventFindTool, pool)
		text, isErr := callTool(t, check, ctx, map[string]any{
			"from": "2026-11-18T22:00:00-05:00", "to": "2026-11-18T23:45:00-05:00",
		})
		if isErr {
			t.Fatalf("event_find: %s", text)
		}
		checkKeys(t, text)
		cs := eventItems(t, text)
		if len(cs) != 2 {
			t.Fatalf("want bill + late call, got %s", text)
		}
		for _, c := range cs {
			if c.LocalDate != "2026-11-18" || c.Weekday != "Wednesday" {
				t.Errorf("%s: got %s %s", c.Title, c.LocalDate, c.Weekday)
			}
		}
	})

	t.Run("event_update", func(t *testing.T) {
		update := bind(t, NewEventUpdateTool, pool)
		text, isErr := callTool(t, update, ctx, map[string]any{"event_id": soccer, "title": "Soccer practice"})
		if isErr {
			t.Fatalf("update_event: %s", text)
		}
		ev := writtenEvent(t, text).Event
		if ev.LocalDate != "2026-11-20" || ev.Weekday != "Friday" {
			t.Errorf("got %s %s (%s)", ev.LocalDate, ev.Weekday, text)
		}
	})

	t.Run("agenda_get", func(t *testing.T) {
		agenda := bind(t, NewAgendaGetTool, pool)
		text, isErr := callTool(t, agenda, ctx, map[string]any{"from": "2026-11-18", "to": "2026-11-18", "detail": "full"})
		if isErr {
			t.Fatalf("agenda_get: %s", text)
		}
		if !strings.Contains(text, "Wednesday, November 18, 2026") {
			t.Errorf("summary missing weekday: %s", text)
		}
		var a agendaResult
		if err := json.Unmarshal(data(t, text), &a); err != nil {
			t.Fatalf("decode: %v", err)
		}
		var got []string
		for _, ev := range a.Events {
			// start is empty for an all-day entry at summary detail; this test
			// runs detail=full, so every occurrence carries its instant.
			got = append(got, ev.Title+" "+ev.Start+" "+ev.Weekday)
		}
		want := []string{
			"Electric bill 2026-11-18T00:00:00-05:00 Wednesday",
			"Dentist 2026-11-18T10:00:00-05:00 Wednesday",
			"Late call 2026-11-18T23:00:00-05:00 Wednesday",
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("agenda events = %v, want %v", got, want)
		}
		if strings.Contains(text, "Soccer") || strings.Contains(text, "Thursday") {
			t.Errorf("agenda has events from other days:\n%s", text)
		}
	})
}

// TestAgendaBills checks agenda_get's bills follow the bill tools' rule (bill.BillDue): due_day clamps to the month's last day,
// and paid_date hides a bill only when it pays that due date.
func TestAgendaBills(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	agenda := bind(t, NewAgendaGetTool, pool)

	for _, b := range []struct {
		name   string
		dueDay *int
		paid   *string
	}{
		{"Rent", intPtr(31), nil},                // clamps to Nov 30
		{"Water", intPtr(30), ptr("2026-10-30")}, // paid last cycle
		{"Power", intPtr(30), ptr("2026-11-30")}, // paid for this due date
		{"Phone", intPtr(28), ptr("2026-11-01")}, // paid late (Oct 28 + 4 days), Nov 28 due
		{"Gym", intPtr(15), nil},                 // due another day
		{"Misc", nil, nil},                       // no due_day
	} {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO bills (household_id, name, amount, due_day, category, paid_date) VALUES ($1, $2, 10, $3, 'utilities', $4::date)`,
			hh.HouseholdID, b.name, b.dueDay, b.paid,
		); err != nil {
			t.Fatalf("insert bill %s: %v", b.name, err)
		}
	}

	billsSection := func(t *testing.T, date string) string {
		t.Helper()
		// One day: the default is a week, so to=date is what "what is due then"
		// means.
		text, isErr := callTool(t, agenda, ctx, map[string]any{"from": date, "to": date, "areas": []any{"bills"}})
		if isErr {
			t.Fatalf("agenda_get %s: %s", date, text)
		}
		var a agendaResult
		if err := json.Unmarshal(data(t, text), &a); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if strings.Join(a.Coverage.Areas, ",") != "bills" || len(a.Events) != 0 {
			t.Errorf("areas=[bills] returned %v and %d events", a.Coverage.Areas, len(a.Events))
		}
		var lines []string
		for _, b := range a.Bills {
			if b.DueDate != date || b.BillID == "" {
				t.Errorf("bill %+v is not due %s", b, date)
			}
			lines = append(lines, fmt.Sprintf("**%s** — $%.2f [%s]", b.Name, b.Amount, b.Category))
		}
		if len(lines) == 0 {
			return "No bills due today."
		}
		return strings.Join(lines, "\n")
	}

	for _, tt := range []struct {
		date      string
		want, not []string
	}{
		{"2026-11-30", []string{"**Rent** — $10.00 [utilities]", "**Water**"}, []string{"Power", "Phone", "Gym", "Misc"}},
		{"2026-11-29", nil, []string{"Rent", "Water", "Power"}},
		{"2026-11-28", []string{"**Phone**"}, []string{"Rent"}},
		{"2026-11-15", []string{"**Gym**"}, []string{"Rent", "Phone"}},
		{"2026-12-31", []string{"**Rent**"}, []string{"Water"}},
		{"2026-12-30", []string{"**Water**", "**Power**"}, nil},
	} {
		t.Run(tt.date, func(t *testing.T) {
			section := billsSection(t, tt.date)
			for _, w := range tt.want {
				if !strings.Contains(section, w) {
					t.Errorf("missing %q:\n%s", w, section)
				}
			}
			for _, n := range tt.not {
				if strings.Contains(section, n) {
					t.Errorf("unexpected %q:\n%s", n, section)
				}
			}
			if len(tt.want) == 0 && !strings.Contains(section, "No bills due today.") {
				t.Errorf("want no bills:\n%s", section)
			}
		})
	}
}

func intPtr(i int) *int { return &i }

// TestAgendaLists covers the agenda's lists area: open items due in the
// range from active non-template lists, open lists with counts when the
// range includes today, the cap, and that another family's items never
// appear.
func TestAgendaLists(t *testing.T) {
	pool := testutil.NewPool(t)
	ctxA, a := testutil.SeedHousehold(t, pool)
	ctxB, b := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, a.HouseholdID, "UTC")
	setHouseholdTimezone(t, pool, b.HouseholdID, "UTC")
	agenda := bind(t, NewAgendaGetTool, pool)
	bg := context.Background()

	today := time.Now().UTC()
	day := func(n int) string { return today.AddDate(0, 0, n).Format("2006-01-02") }
	insertID := func(t *testing.T, sql string, args ...any) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(bg, sql+" RETURNING id::text", args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	list := func(t *testing.T, hh uuid.UUID, name, kind string, template, archived bool) string {
		return insertID(t, `INSERT INTO lists (household_id, name, kind, is_template, archived) VALUES ($1, $2, $3, $4, $5)`,
			hh, name, kind, template, archived)
	}
	item := func(t *testing.T, hh uuid.UUID, listID, text string, due *string, checked bool, assignee *string) string {
		return insertID(t, `INSERT INTO list_items (list_id, household_id, text, due_date, checked, assignee_person_id) VALUES ($1, $2, $3, $4::date, $5, $6::uuid)`,
			listID, hh, text, due, checked, assignee)
	}

	nora := insertID(t, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Nora')`, a.HouseholdID)
	chores := list(t, a.HouseholdID, "Weekend chores", "chores", false, false)
	mow := item(t, a.HouseholdID, chores, "Mow lawn", ptr(day(1)), false, &nora)
	item(t, a.HouseholdID, chores, "Rake leaves", ptr(day(1)), true, nil)
	item(t, a.HouseholdID, chores, "Clean gutters", ptr(day(10)), false, nil)
	item(t, a.HouseholdID, chores, "Sweep porch", nil, false, nil)
	grocery := list(t, a.HouseholdID, "Grocery", "grocery", false, false)
	milk := item(t, a.HouseholdID, grocery, "Milk", ptr(day(2)), false, nil)
	old := list(t, a.HouseholdID, "Old chores", "chores", false, true)
	item(t, a.HouseholdID, old, "Archived thing", ptr(day(1)), false, nil)
	tpl := list(t, a.HouseholdID, "Packing", "packing", true, false)
	item(t, a.HouseholdID, tpl, "Template thing", ptr(day(1)), false, nil)

	groceryB := list(t, b.HouseholdID, "Grocery", "grocery", false, false)
	secretB := item(t, b.HouseholdID, groceryB, "Other family item", ptr(day(1)), false, nil)

	call := func(t *testing.T, ctx context.Context, args map[string]any) (agendaResult, string) {
		t.Helper()
		text, isErr := callTool(t, agenda, ctx, args)
		var res agendaResult
		if err := json.Unmarshal(data(t, text), &res); err != nil || isErr {
			t.Fatalf("agenda_get %v: %s (%v)", args, text, err)
		}
		return res, text
	}

	t.Run("due open items in range and open lists, one family only", func(t *testing.T) {
		res, text := call(t, ctxA, map[string]any{"from": day(0), "to": day(6), "areas": []any{"lists"}})
		if strings.Join(res.Coverage.Areas, ",") != "lists" || res.Lists == nil {
			t.Fatalf("areas = %v lists = %v", res.Coverage.Areas, res.Lists)
		}
		var got []string
		for _, it := range res.Lists.Due {
			who := ""
			if it.Assignee != nil {
				who = "@" + *it.Assignee
			}
			got = append(got, fmt.Sprintf("%s %s/%s%s %s", it.DueDate, it.ListName, it.Text, who, it.Weekday[:3]))
		}
		want := []string{
			day(1) + " Weekend chores/Mow lawn@Nora " + today.AddDate(0, 0, 1).Weekday().String()[:3],
			day(2) + " Grocery/Milk " + today.AddDate(0, 0, 2).Weekday().String()[:3],
		}
		if strings.Join(got, "|") != strings.Join(want, "|") || res.Lists.DueTotal != 2 {
			t.Errorf("due = %v (total %d)\nwant %v", got, res.Lists.DueTotal, want)
		}
		if res.Lists.Due[0].ItemID != mow || res.Lists.Due[0].ListID != chores || res.Lists.Due[1].ItemID != milk {
			t.Errorf("ids = %+v", res.Lists.Due)
		}
		var open []string
		for _, l := range res.Lists.Open {
			open = append(open, fmt.Sprintf("%s:%s:%d:%s", l.Name, l.Kind, l.OpenItems, strings.Join(l.Preview, ",")))
		}
		wantOpen := "Grocery:grocery:1:Milk|Weekend chores:chores:3:Mow lawn,Clean gutters,Sweep porch"
		if strings.Join(open, "|") != wantOpen || res.Lists.OpenTotal != 2 {
			t.Errorf("open = %v (total %d)\nwant %s", open, res.Lists.OpenTotal, wantOpen)
		}
		for _, leak := range []string{"Other family item", secretB, groceryB, "Archived thing", "Template thing", "Rake leaves", "Not available yet"} {
			if strings.Contains(text, leak) {
				t.Errorf("result contains %q:\n%s", leak, text)
			}
		}
		if !strings.Contains(text, "2 list items due (2 open lists)") {
			t.Errorf("summary: %s", text)
		}
		if !strings.Contains(text, fmt.Sprintf("list_check_items list_id=%s item_ids=[\\\"%s\\\"]", chores, mow)) ||
			!strings.Contains(text, "list_get list_id="+grocery) {
			t.Errorf("hints: %s", text)
		}
	})

	t.Run("a range without today has no open lists", func(t *testing.T) {
		res, _ := call(t, ctxA, map[string]any{"from": day(2), "to": day(3), "areas": []any{"lists"}})
		if len(res.Lists.Due) != 1 || res.Lists.Due[0].Text != "Milk" || res.Lists.Open != nil || res.Lists.OpenTotal != 0 {
			t.Errorf("lists = %+v", res.Lists)
		}
	})

	t.Run("lists is a default area", func(t *testing.T) {
		res, text := call(t, ctxA, map[string]any{"from": day(1), "to": day(1)})
		if strings.Join(res.Coverage.Areas, ",") != "events,bills,meals,lists" {
			t.Errorf("areas = %v: %s", res.Coverage.Areas, text)
		}
		if res.Lists == nil || len(res.Lists.Due) != 1 || res.Lists.Due[0].Text != "Mow lawn" {
			t.Errorf("lists = %+v", res.Lists)
		}
	})

	t.Run("the other family sees only its own items, capped with a total", func(t *testing.T) {
		big := list(t, b.HouseholdID, "Big chores", "chores", false, false)
		for i := 0; i < 25; i++ {
			item(t, b.HouseholdID, big, fmt.Sprintf("Chore %02d", i), ptr(day(3)), false, nil)
		}
		res, text := call(t, ctxB, map[string]any{"from": day(0), "to": day(6), "areas": []any{"lists"}})
		if res.Lists.DueTotal != 26 || len(res.Lists.Due) != agendaSummaryListItems || res.Lists.Due[0].ItemID != secretB {
			t.Errorf("due total %d, %d returned, first %+v", res.Lists.DueTotal, len(res.Lists.Due), res.Lists.Due[0])
		}
		for _, leak := range []string{"Mow lawn", "Milk", "Nora", chores, grocery} {
			if strings.Contains(text, leak) {
				t.Errorf("household B sees %q", leak)
			}
		}
		if len(res.Lists.Open) != 2 || res.Lists.Open[0].Name != "Big chores" || len(res.Lists.Open[0].Preview) != agendaListPreview {
			t.Errorf("open = %+v", res.Lists.Open)
		}
		if !strings.Contains(text, "26 list items due") {
			t.Errorf("summary: %s", text)
		}
	})
}
