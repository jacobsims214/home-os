package calendar

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/testutil"
)

// TestToolDefinitions checks every calendar tool against the v2 catalog
// (KB architecture/mcp-tool-catalog.md): name, required params, all four
// annotation hints for its class, an outputSchema, and description lengths.
func TestToolDefinitions(t *testing.T) {
	type hints struct{ readOnly, destructive, idempotent bool }
	r, c, u, x := hints{true, false, true}, hints{false, false, false}, hints{false, false, true}, hints{false, true, true}
	tests := []struct {
		ctor     func(*pgxpool.Pool) (mcp.Tool, handlerFunc)
		name     string
		hints    hints
		required []string
		params   []string
	}{
		{NewCalendarFindTool, "calendar_find", r, nil, nil},
		{NewCalendarCreateTool, "calendar_create", c, []string{"name"}, []string{"color"}},
		{NewEventFindTool, "event_find", r, nil, []string{"id", "from", "to", "query", "calendar_id", "limit", "cursor"}},
		{NewEventCreateTool, "event_create", c, []string{"title", "start", "end"}, []string{"calendar_id", "all_day", "location", "description", "event_type"}},
		{NewEventUpdateTool, "event_update", u, []string{"event_id"}, []string{"title", "start", "end", "all_day", "location", "description", "event_type", "calendar_id"}},
		{NewEventDeleteTool, "event_delete", x, []string{"event_id"}, nil},
		{NewCalendarFindFreeTimeTool, "calendar_find_free_time", r, []string{"from", "duration_minutes"}, []string{"to", "start_hour", "end_hour", "calendar_ids"}},
		{NewAgendaGetTool, "agenda_get", r, nil, []string{"from", "to", "areas"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, _ := tt.ctor(nil)
			if tool.Name != tt.name {
				t.Fatalf("name = %q, want %q", tool.Name, tt.name)
			}
			a := tool.Annotations
			if a.Title == "" || a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
				t.Fatalf("annotations incomplete: %+v", a)
			}
			got := hints{*a.ReadOnlyHint, *a.DestructiveHint, *a.IdempotentHint}
			if got != tt.hints || *a.OpenWorldHint {
				t.Errorf("hints = %+v openWorld=%v, want %+v and false", got, *a.OpenWorldHint, tt.hints)
			}
			if n := utf8.RuneCountInString(tool.Description); n == 0 || n > 200 {
				t.Errorf("description is %d characters (want 1-200): %q", n, tool.Description)
			}
			_ = outputSchema(t, tool)

			req := append([]string(nil), tool.InputSchema.Required...)
			if strings.Join(req, ",") != strings.Join(tt.required, ",") {
				t.Errorf("required = %v, want %v", req, tt.required)
			}
			want := append(append([]string(nil), tt.required...), tt.params...)
			if len(tool.InputSchema.Properties) != len(want) {
				t.Errorf("params = %v, want %v", keys(tool.InputSchema.Properties), want)
			}
			for _, p := range want {
				prop, ok := tool.InputSchema.Properties[p].(map[string]any)
				if !ok {
					t.Errorf("param %q missing", p)
					continue
				}
				d, _ := prop["description"].(string)
				if n := utf8.RuneCountInString(d); n == 0 || n > 80 {
					t.Errorf("param %q description is %d characters (want 1-80): %q", p, n, d)
				}
			}
		})
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// seedNamedCalendar inserts a calendar with the given name and type.
func seedNamedCalendar(t *testing.T, pool *pgxpool.Pool, householdID uuid.UUID, name, typ string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, $2, $3::calendar_type, $4) RETURNING id`,
		householdID, name, typ, uuid.NewString(),
	).Scan(&id); err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	return id
}

// listEnvelope is the list result shape, for truncation checks.
type listEnvelope struct {
	OK        bool              `json:"ok"`
	Summary   string            `json:"summary"`
	Items     []json.RawMessage `json:"items"`
	Total     int               `json:"total"`
	Truncated bool              `json:"truncated"`
	Narrow    string            `json:"narrow"`
	Next      []string          `json:"next"`
}

func decodeList(t *testing.T, text string) listEnvelope {
	t.Helper()
	var env listEnvelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return env
}

func TestEventFind(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	family := seedNamedCalendar(t, pool, hh.HouseholdID, "Family", "family")
	kids := seedNamedCalendar(t, pool, hh.HouseholdID, "Kids", "custom")
	find := bind(t, NewEventFindTool, pool)

	// Mon-Fri 11/16-11/20 at 09:00 New York: one Kids "Soccer" event a day,
	// seeded out of order so sorting by start is visible.
	ids := map[string]string{}
	for _, d := range []string{"20", "17", "16", "19", "18"} {
		ids[d] = seedEvent(t, pool, kids, uuid.NewString(),
			`{"title":"Soccer","location":"Field 2","start":"2026-11-`+d+`T14:00:00Z","end":"2026-11-`+d+`T15:00:00Z"}`)
	}
	dentist := seedEvent(t, pool, family, uuid.NewString(),
		`{"title":"Dentist","description":"Dr. Lee","start":"2026-11-18T19:00:00Z","end":"2026-11-18T20:00:00Z"}`)

	t.Run("id returns exactly that event", func(t *testing.T) {
		text, isErr := callTool(t, find, ctx, map[string]any{"id": dentist, "query": "ignored"})
		evs := eventItems(t, text)
		if isErr || len(evs) != 1 || evs[0].ID != dentist || evs[0].Start != "2026-11-18T14:00:00-05:00" {
			t.Fatalf("got %s", text)
		}
		if !strings.Contains(text, "event_update event_id="+dentist) {
			t.Errorf("want an event_update hint: %s", text)
		}
	})

	t.Run("unknown and malformed ids are corrective errors", func(t *testing.T) {
		if text, isErr := callTool(t, find, ctx, map[string]any{"id": uuid.NewString()}); !isErr || !strings.Contains(text, "No event with id") {
			t.Errorf("unknown id: %s", text)
		}
		if text, isErr := callTool(t, find, ctx, map[string]any{"id": "dentist"}); !isErr || !strings.Contains(text, `"field":"id"`) {
			t.Errorf("malformed id: %s", text)
		}
	})

	t.Run("query and calendar name filter, sorted by start", func(t *testing.T) {
		text, _ := callTool(t, find, ctx, map[string]any{"from": "2026-11-16", "to": "2026-11-20", "query": "dr lee"})
		if evs := eventItems(t, text); len(evs) != 1 || evs[0].ID != dentist {
			t.Errorf("query: %s", text)
		}
		text, _ = callTool(t, find, ctx, map[string]any{"from": "2026-11-16", "to": "2026-11-20", "calendar_id": "kids"})
		evs := eventItems(t, text)
		if len(evs) != 5 {
			t.Fatalf("calendar by name: %s", text)
		}
		for i, d := range []string{"16", "17", "18", "19", "20"} {
			if evs[i].ID != ids[d] || evs[i].CalendarName != "Kids" {
				t.Errorf("item %d = %s on %s, want the 11/%s Soccer", i, evs[i].ID, evs[i].LocalDate, d)
			}
		}
	})

	t.Run("pages with limit and cursor", func(t *testing.T) {
		args := map[string]any{"from": "2026-11-16", "to": "2026-11-20", "calendar_id": kids, "limit": 2.0}
		var seen []string
		for page := 0; page < 3; page++ {
			text, isErr := callTool(t, find, ctx, args)
			if isErr {
				t.Fatalf("page %d: %s", page, text)
			}
			env := decodeList(t, text)
			for _, ev := range eventItems(t, text) {
				seen = append(seen, ev.ID)
			}
			if page < 2 {
				if !env.Truncated || !strings.Contains(env.Narrow, "cursor=") {
					t.Fatalf("page %d should be truncated with a cursor: %s", page, text)
				}
				if !strings.Contains(env.Summary, "of 5") {
					t.Errorf("page %d summary = %q", page, env.Summary)
				}
				var c string
				for _, n := range env.Next {
					if _, after, ok := strings.Cut(n, `cursor="`); ok {
						c, _, _ = strings.Cut(after, `"`)
					}
				}
				args["cursor"] = c
			} else if env.Truncated || len(env.Items) != 1 {
				t.Errorf("last page should hold 1 item and not be truncated: %s", text)
			}
		}
		if strings.Join(seen, ",") != strings.Join([]string{ids["16"], ids["17"], ids["18"], ids["19"], ids["20"]}, ",") {
			t.Errorf("pages returned %v", seen)
		}
	})

	t.Run("default range is today plus 7 days", func(t *testing.T) {
		text, isErr := callTool(t, find, ctx, map[string]any{})
		if isErr || !decodeList(t, text).OK {
			t.Errorf("no-arg call: %s", text)
		}
	})

	errs := []struct {
		name string
		args map[string]any
		want []string
	}{
		{"naive from gets the family offset", map[string]any{"from": "2026-11-18T09:00:00"}, []string{"has no UTC offset", "2026-11-18T09:00:00-05:00"}},
		{"to before from", map[string]any{"from": "2026-11-18", "to": "2026-11-17"}, []string{"to must be after from"}},
		{"unknown calendar lists the real ones", map[string]any{"calendar_id": "Work"}, []string{`No calendar \"Work\"`, `\"Kids\"`}},
		{"bad cursor", map[string]any{"cursor": "nope"}, []string{`"field":"cursor"`}},
		{"limit too big", map[string]any{"limit": 500.0}, []string{`"field":"limit"`}},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			text, isErr := callTool(t, find, ctx, tt.args)
			if !isErr {
				t.Fatalf("want error, got %s", text)
			}
			for _, w := range tt.want {
				if !strings.Contains(text, w) {
					t.Errorf("error %s does not contain %s", text, w)
				}
			}
		})
	}

	t.Run("ambiguous calendar name lists the candidates", func(t *testing.T) {
		other := seedNamedCalendar(t, pool, hh.HouseholdID, "kids", "custom")
		text, isErr := callTool(t, find, ctx, map[string]any{"calendar_id": "KIDS"})
		if !isErr || !strings.Contains(text, kids) || !strings.Contains(text, other) {
			t.Errorf("want both ids in the error: %s", text)
		}
	})
}

func TestEventCreate(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	bills := seedNamedCalendar(t, pool, hh.HouseholdID, "Bills", "bills")
	family := seedNamedCalendar(t, pool, hh.HouseholdID, "Family", "family")
	kids := seedNamedCalendar(t, pool, hh.HouseholdID, "Kids", "custom")
	_ = bills
	create := bind(t, NewEventCreateTool, pool)

	count := func(t *testing.T, title string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(context.Background(),
			`SELECT count(*) FROM calendar_objects WHERE ical_data::jsonb->>'title' = $1`, title).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	t.Run("calendar_id defaults to the Family calendar", func(t *testing.T) {
		text, isErr := callTool(t, create, ctx, map[string]any{"title": "Dinner", "start": "2026-11-19T18:00:00-05:00", "end": "2026-11-19T19:00:00-05:00"})
		w := writtenEvent(t, text)
		if isErr || w.Event.CalendarID != family || w.Event.Start != "2026-11-19T18:00:00-05:00" || w.Event.Weekday != "Thursday" {
			t.Fatalf("got %s", text)
		}
		if !strings.Contains(text, "Created \\\"Dinner\\\" on Thu Nov 19, 6:00 PM") {
			t.Errorf("summary: %s", text)
		}
	})

	t.Run("calendar_id accepts a name", func(t *testing.T) {
		text, _ := callTool(t, create, ctx, map[string]any{"calendar_id": "kids", "title": "Piano", "start": "2026-11-20T16:00:00-05:00", "end": "2026-11-20T17:00:00-05:00"})
		if w := writtenEvent(t, text); w.Event.CalendarID != kids || w.Event.CalendarName != "Kids" {
			t.Fatalf("got %s", text)
		}
	})

	t.Run("an identical create within 10 minutes returns the first event", func(t *testing.T) {
		args := map[string]any{"title": "Vet", "start": "2026-11-21T10:00:00-05:00", "end": "2026-11-21T11:00:00-05:00"}
		first := writtenEvent(t, func() string { s, _ := callTool(t, create, ctx, args); return s }())
		text, isErr := callTool(t, create, ctx, args)
		second := writtenEvent(t, text)
		if isErr || !second.Duplicate || second.Event.ID != first.Event.ID || first.Duplicate {
			t.Fatalf("want duplicate of %s, got %s", first.Event.ID, text)
		}
		if n := count(t, "Vet"); n != 1 {
			t.Errorf("%d Vet rows, want 1", n)
		}
		if !strings.Contains(text, "event_update event_id="+first.Event.ID) {
			t.Errorf("want an event_update hint: %s", text)
		}
		// A different time is a new event.
		args["start"], args["end"] = "2026-11-22T10:00:00-05:00", "2026-11-22T11:00:00-05:00"
		if w := writtenEvent(t, func() string { s, _ := callTool(t, create, ctx, args); return s }()); w.Duplicate {
			t.Errorf("different time flagged duplicate")
		}
	})

	errs := []struct {
		name string
		args map[string]any
		want []string
	}{
		{"naive start", map[string]any{"title": "X", "start": "2026-11-19T17:30:00", "end": "2026-11-19T18:30:00-05:00"},
			[]string{"has no UTC offset", "2026-11-19T17:30:00-05:00", "America/New_York"}},
		{"date without all_day", map[string]any{"title": "X", "start": "2026-11-19", "end": "2026-11-19"},
			[]string{"a date without a time", "all_day:true"}},
		{"missing title", map[string]any{"start": "2026-11-19T17:30:00-05:00", "end": "2026-11-19T18:30:00-05:00"},
			[]string{"title is required", `"example"`}},
		{"end before start", map[string]any{"title": "X", "start": "2026-11-19T17:30:00-05:00", "end": "2026-11-19T16:30:00-05:00"},
			[]string{"end must be after start"}},
		{"unknown calendar", map[string]any{"calendar_id": "Work", "title": "X", "start": "2026-11-19T17:30:00-05:00", "end": "2026-11-19T18:30:00-05:00"},
			[]string{`No calendar \"Work\"`}},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			text, isErr := callTool(t, create, ctx, tt.args)
			if !isErr {
				t.Fatalf("want error, got %s", text)
			}
			for _, w := range tt.want {
				if !strings.Contains(text, w) {
					t.Errorf("error %s does not contain %s", text, w)
				}
			}
		})
	}
	if n := count(t, "X"); n != 0 {
		t.Errorf("rejected creates stored %d rows", n)
	}
}

func TestEventUpdateConflictsAndMove(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	family := seedNamedCalendar(t, pool, hh.HouseholdID, "Family", "family")
	kids := seedNamedCalendar(t, pool, hh.HouseholdID, "Kids", "custom")
	update := bind(t, NewEventUpdateTool, pool)

	// Dentist 10:00-11:00 and Lunch 12:00-13:00 New York on 11/18.
	dentist := seedEvent(t, pool, family, uuid.NewString(),
		`{"title":"Dentist","start":"2026-11-18T15:00:00Z","end":"2026-11-18T16:00:00Z"}`)
	lunchUID := "lunch-" + uuid.NewString()
	lunch := seedEvent(t, pool, family, lunchUID,
		`{"title":"Lunch","start":"2026-11-18T17:00:00Z","end":"2026-11-18T18:00:00Z"}`)

	t.Run("moving into another event reports the conflict", func(t *testing.T) {
		text, isErr := callTool(t, update, ctx, map[string]any{"event_id": lunch, "start": "2026-11-18T10:30:00-05:00"})
		w := writtenEvent(t, text)
		if isErr || len(w.Conflicts) != 1 || w.Conflicts[0].ID != dentist {
			t.Fatalf("want the dentist conflict, got %s", text)
		}
		if w.Event.End != "2026-11-18T11:30:00-05:00" {
			t.Errorf("duration not kept: %s", w.Event.End)
		}
		if !strings.Contains(text, "overlaps 1 other event") || !strings.Contains(text, "Overlaps \\\"Dentist\\\"") {
			t.Errorf("summary/next should name the overlap: %s", text)
		}
	})

	t.Run("a title-only change reports no conflicts", func(t *testing.T) {
		text, _ := callTool(t, update, ctx, map[string]any{"event_id": lunch, "title": "Team lunch"})
		if w := writtenEvent(t, text); len(w.Conflicts) != 0 || w.Event.Title != "Team lunch" {
			t.Errorf("got %s", text)
		}
	})

	t.Run("calendar_id moves the event and syncs both calendars", func(t *testing.T) {
		text, isErr := callTool(t, update, ctx, map[string]any{"event_id": lunch, "calendar_id": "Kids"})
		w := writtenEvent(t, text)
		if isErr || w.Event.CalendarID != kids || w.Event.CalendarName != "Kids" || w.Event.Title != "Team lunch" {
			t.Fatalf("got %s", text)
		}
		var actions []string
		rows, err := pool.Query(context.Background(),
			`SELECT calendar_id::text || ':' || action FROM calendar_changes WHERE event_uid = $1 ORDER BY id`, lunchUID)
		if err != nil {
			t.Fatalf("changes: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var a string
			_ = rows.Scan(&a)
			actions = append(actions, a)
		}
		got := strings.Join(actions, ",")
		if !strings.HasSuffix(got, family+":delete,"+kids+":create") {
			t.Errorf("calendar_changes = %s, want a delete on Family then a create on Kids", got)
		}
	})

	t.Run("bad ids are corrective errors", func(t *testing.T) {
		if text, isErr := callTool(t, update, ctx, map[string]any{"event_id": "lunch", "title": "x"}); !isErr || !strings.Contains(text, `"field":"event_id"`) {
			t.Errorf("malformed: %s", text)
		}
		if text, isErr := callTool(t, update, ctx, map[string]any{"event_id": dentist, "calendar_id": "Work"}); !isErr || !strings.Contains(text, "No calendar") {
			t.Errorf("unknown calendar: %s", text)
		}
	})
}

func TestCalendarFind(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	seedNamedCalendar(t, pool, hh.HouseholdID, "Family", "family")
	seedNamedCalendar(t, pool, hh.HouseholdID, "Bills", "bills")
	_, other := testutil.SeedHousehold(t, pool)
	seedNamedCalendar(t, pool, other.HouseholdID, "Stranger", "family")

	text, isErr := callTool(t, bind(t, NewCalendarFindTool, pool), ctx, map[string]any{})
	var cals []Calendar
	if err := json.Unmarshal(items(t, text), &cals); err != nil || isErr {
		t.Fatalf("got %s (%v)", text, err)
	}
	if len(cals) != 2 || cals[0].Name != "Bills" || cals[1].Name != "Family" || cals[1].Type != "family" {
		t.Errorf("calendars = %+v", cals)
	}
	if !strings.Contains(text, "Found 2 calendars: Bills, Family.") {
		t.Errorf("summary: %s", text)
	}
}

func TestAgendaAreas(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	agenda := bind(t, NewAgendaGetTool, pool)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO maintenance_tasks (household_id, name, due_date) VALUES ($1, 'Change furnace filter', '2026-11-19')`, hh.HouseholdID); err != nil {
		t.Fatalf("insert task: %v", err)
	}

	text, isErr := callTool(t, agenda, ctx, map[string]any{"from": "2026-11-18", "to": "2026-11-20", "areas": []any{"maintenance", "meals"}})
	var a agendaResult
	if err := json.Unmarshal(data(t, text), &a); err != nil || isErr {
		t.Fatalf("got %s (%v)", text, err)
	}
	if len(a.Maintenance) != 1 || a.Maintenance[0].Weekday != "Thursday" || a.Maintenance[0].Status != "pending" || a.Maintenance[0].TaskID == "" {
		t.Errorf("maintenance = %+v", a.Maintenance)
	}
	if strings.Join(a.Areas, ",") != "maintenance" || strings.Join(a.Unavailable, ",") != "meals" {
		t.Errorf("areas = %v unavailable = %v", a.Areas, a.Unavailable)
	}
	if !strings.Contains(a.From+a.To, "2026-11-182026-11-20") || !strings.Contains(text, "Not available yet: meals") {
		t.Errorf("got %s", text)
	}

	for name, args := range map[string]map[string]any{
		"unknown area":   {"areas": []any{"chores"}},
		"to before from": {"from": "2026-11-18", "to": "2026-11-17"},
		"bad date":       {"from": "next tuesday"},
		"too long":       {"from": "2026-01-01", "to": "2026-03-01"},
	} {
		if text, isErr := callTool(t, agenda, ctx, args); !isErr || !strings.Contains(text, `"fix"`) {
			t.Errorf("%s: want corrective error, got %s", name, text)
		}
	}
}

// TestOutputSchemaRejectsBadShapes proves the schema check in bind is real:
// a numeric id or a missing weekday fails validation.
func TestOutputSchemaRejectsBadShapes(t *testing.T) {
	findTool, _ := NewEventFindTool(nil)
	rs := outputSchema(t, findTool)
	for name, doc := range map[string]string{
		"numeric id":      `{"ok":true,"summary":"x","items":[{"id":5}],"returned":1,"total":1}`,
		"missing weekday": `{"ok":true,"summary":"x","items":[{"id":"a","calendar_id":"b","title":"t","description":"","start":"s","end":"e","all_day":false,"location":"","color":"","event_type":"event","local_date":"2026-11-18","recurring":false}],"returned":1,"total":1}`,
	} {
		var v any
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			t.Fatal(err)
		}
		if err := rs.Validate(v); err == nil {
			t.Errorf("%s: schema accepted %s", name, doc)
		}
	}
}
