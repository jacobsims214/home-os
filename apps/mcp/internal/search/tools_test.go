package search

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/testutil"
)

type hit struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Score        int    `json:"score"`
	Start        string `json:"start"`
	LocalDate    string `json:"local_date"`
	Weekday      string `json:"weekday"`
	CalendarName string `json:"calendar_name"`
}

type group struct {
	EntityType string `json:"entity_type"`
	Count      int    `json:"count"`
	Results    []hit  `json:"results"`
}

type searchResult struct {
	Query   string  `json:"query"`
	Total   int     `json:"total"`
	Results []group `json:"results"`
	Error   string  `json:"error"`
}

func seedCalendar(t *testing.T, pool *pgxpool.Pool, householdID uuid.UUID, name string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, $2, 'family', $3) RETURNING id`,
		householdID, name, uuid.NewString(),
	).Scan(&id); err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	return id
}

func seedEvent(t *testing.T, pool *pgxpool.Pool, calendarID, icalData string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type) VALUES ($1, $2, $3, 'etag-0', 'event') RETURNING id`,
		calendarID, uuid.NewString(), icalData,
	).Scan(&id); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	return id
}

func runSearch(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string) searchResult {
	t.Helper()
	_, _, handler := NewSearchTool(pool)
	var req mcp.CallToolRequest
	req.Params.Name = "search"
	req.Params.Arguments = map[string]any{"query": query}
	res, err := handler(ctx, req)
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	var out searchResult
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("decode %s: %v", text.Text, err)
	}
	if out.Error != "" {
		t.Fatalf("search %q: error %s", query, out.Error)
	}
	return out
}

func eventHits(r searchResult) []hit {
	for _, g := range r.Results {
		if g.EntityType == "event" {
			return g.Results
		}
	}
	return nil
}

func TestSearchEvents(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, h := testutil.SeedHousehold(t, pool)
	_, other := testutil.SeedHousehold(t, pool)

	cal := seedCalendar(t, pool, h.HouseholdID, "Kids")
	soccer := seedEvent(t, pool, cal, `{"title":"Emma Soccer Practice","description":"bring shin guards","start":"2026-11-18T17:00:00Z","end":"2026-11-18T18:30:00Z","location":"Riverside Park"}`)
	electric := seedEvent(t, pool, cal, `{"title":"Pay bill","description":"Electric company autopay check","start":"2026-11-20T09:00:00Z","end":"2026-11-20T09:30:00Z","location":""}`)
	cafe := seedEvent(t, pool, cal, `{"title":"Coffee","description":"","start":"2026-11-21T08:00:00Z","end":"2026-11-21T09:00:00Z","location":"Café Luna"}`)
	seedEvent(t, pool, cal, `not json at all: soccer`)

	otherCal := seedCalendar(t, pool, other.HouseholdID, "Other Family")
	otherSoccer := seedEvent(t, pool, otherCal, `{"title":"Soccer tournament","start":"2026-11-18T10:00:00Z","end":"2026-11-18T16:00:00Z"}`)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"title match, case-insensitive", "soccer", []string{soccer}},
		{"description match", "electric", []string{electric}},
		{"location match", "riverside", []string{soccer}},
		{"non-ASCII query matches escaped JSON", "café", []string{cafe}},
		{"JSON keys are not searched", "description", nil},
		{"other fields are not searched", "2026-11-18", nil},
		{"LIKE wildcards are literal", "%", nil},
		{"no match", "dentist", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := []string{}
			for _, e := range eventHits(runSearch(t, ctx, pool, tc.query)) {
				if e.ID == otherSoccer {
					t.Fatalf("result includes another household's event %s", otherSoccer)
				}
				got = append(got, e.ID)
			}
			want := append([]string{}, tc.want...)
			sort.Strings(got)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("query %q: got %v, want %v", tc.query, got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("query %q: got %v, want %v", tc.query, got, want)
				}
			}
		})
	}

	t.Run("event hit carries title, start and calendar name", func(t *testing.T) {
		r := runSearch(t, ctx, pool, "soccer")
		hits := eventHits(r)
		if len(hits) != 1 {
			t.Fatalf("want 1 event hit, got %+v", r)
		}
		want := hit{ID: soccer, Title: "Emma Soccer Practice", Score: 2, Start: "2026-11-18T17:00:00Z", LocalDate: "2026-11-18", Weekday: "Wednesday", CalendarName: "Kids"}
		if hits[0] != want {
			t.Fatalf("got %+v, want %+v", hits[0], want)
		}
		if r.Total != 1 {
			t.Fatalf("total = %d, want 1", r.Total)
		}
	})

	t.Run("other household only sees its own event", func(t *testing.T) {
		otherCtx, _ := testutil.SeedHousehold(t, pool)
		if hits := eventHits(runSearch(t, otherCtx, pool, "soccer")); len(hits) != 0 {
			t.Fatalf("household with no events got %+v", hits)
		}
	})
}

func TestSearchEventsAlongsideOtherEntities(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, h := testutil.SeedHousehold(t, pool)

	if _, err := pool.Exec(context.Background(),
		`INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'household', $1, 'Soccer carpool', 'Tues/Thurs')`, h.HouseholdID,
	); err != nil {
		t.Fatalf("insert note: %v", err)
	}
	cal := seedCalendar(t, pool, h.HouseholdID, "Family")
	seedEvent(t, pool, cal, `{"title":"Soccer game","start":"2026-11-22T10:00:00Z","end":"2026-11-22T11:00:00Z"}`)

	r := runSearch(t, ctx, pool, "soccer")
	types := []string{}
	for _, g := range r.Results {
		types = append(types, g.EntityType)
	}
	if len(types) != 2 || types[0] != "note" || types[1] != "event" {
		t.Fatalf("groups = %v, want [note event]", types)
	}
	if r.Total != 2 {
		t.Fatalf("total = %d, want 2", r.Total)
	}
}

func TestSearchEventsHouseholdLocalDay(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, h := testutil.SeedHousehold(t, pool)
	if _, err := pool.Exec(context.Background(),
		`UPDATE households SET timezone = 'America/New_York' WHERE id = $1`, h.HouseholdID,
	); err != nil {
		t.Fatalf("set timezone: %v", err)
	}
	cal := seedCalendar(t, pool, h.HouseholdID, "Family")
	// 23:30 New York on Wednesday 11/18 is 04:30Z on Thursday 11/19.
	late := seedEvent(t, pool, cal, `{"title":"Late call","start":"2026-11-19T04:30:00Z","end":"2026-11-19T05:00:00Z","all_day":false}`)
	// All-day events are dated by the date as written, not shifted to 11/17.
	allDay := seedEvent(t, pool, cal, `{"title":"Teacher workday","start":"2026-11-18T00:00:00Z","end":"2026-11-19T00:00:00Z","all_day":true}`)
	// A note with no event fields: non-event hits keep no start/local_date/weekday.
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'household', $1, 'Late call notes', 'agenda')`, h.HouseholdID,
	); err != nil {
		t.Fatalf("insert note: %v", err)
	}

	tests := []struct {
		name  string
		query string
		want  hit
	}{
		{"timed event in household time", "late call", hit{ID: late, Title: "Late call", Score: 2, Start: "2026-11-18T23:30:00-05:00", LocalDate: "2026-11-18", Weekday: "Wednesday", CalendarName: "Family"}},
		{"all-day event dated as written", "teacher", hit{ID: allDay, Title: "Teacher workday", Score: 2, Start: "2026-11-18T00:00:00-05:00", LocalDate: "2026-11-18", Weekday: "Wednesday", CalendarName: "Family"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits := eventHits(runSearch(t, ctx, pool, tc.query))
			if len(hits) != 1 {
				t.Fatalf("want 1 event hit, got %+v", hits)
			}
			if hits[0] != tc.want {
				t.Fatalf("got %+v, want %+v", hits[0], tc.want)
			}
		})
	}

	t.Run("non-event hits carry no event day fields", func(t *testing.T) {
		_, _, handler := NewSearchTool(pool)
		var req mcp.CallToolRequest
		req.Params.Arguments = map[string]any{"query": "late call"}
		res, err := handler(ctx, req)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		var raw struct {
			Results []struct {
				EntityType string           `json:"entity_type"`
				Results    []map[string]any `json:"results"`
			} `json:"results"`
		}
		if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &raw); err != nil {
			t.Fatalf("decode: %v", err)
		}
		found := false
		for _, g := range raw.Results {
			if g.EntityType != "note" {
				continue
			}
			found = true
			for _, r := range g.Results {
				for _, k := range []string{"start", "local_date", "weekday", "calendar_name"} {
					if _, ok := r[k]; ok {
						t.Fatalf("note hit has %q: %v", k, r)
					}
				}
			}
		}
		if !found {
			t.Fatalf("no note group in %+v", raw)
		}
	})
}
