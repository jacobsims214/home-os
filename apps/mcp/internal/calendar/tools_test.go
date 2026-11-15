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
