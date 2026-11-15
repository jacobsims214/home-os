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
				{Date: "2026-11-18", Start: "2026-11-18T16:00:00-05:00", End: "2026-11-18T19:00:00-05:00"},
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
