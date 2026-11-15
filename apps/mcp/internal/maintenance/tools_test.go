package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/testutil"
)

// ─────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────

type toolCtor func(*pgxpool.Pool) (string, mcp.Tool, handlerFunc)

var ctors = map[string]toolCtor{
	"maintenance_find":     NewMaintenanceFindTool,
	"maintenance_create":   NewMaintenanceCreateTool,
	"maintenance_update":   NewMaintenanceUpdateTool,
	"maintenance_complete": NewMaintenanceCompleteTool,
	"maintenance_delete":   NewMaintenanceDeleteTool,
}

var (
	schemasOnce sync.Once
	schemas     map[string]*jsonschema.Resolved
	schemasErr  error
)

// outputSchema resolves the outputSchema the named tool advertises.
func outputSchema(t *testing.T, name string) *jsonschema.Resolved {
	t.Helper()
	schemasOnce.Do(func() {
		schemas = map[string]*jsonschema.Resolved{}
		for n, ctor := range ctors {
			_, tool, _ := ctor(nil)
			b, err := json.Marshal(tool)
			if err != nil {
				schemasErr = err
				return
			}
			var raw struct {
				OutputSchema json.RawMessage `json:"outputSchema"`
			}
			if err := json.Unmarshal(b, &raw); err != nil {
				schemasErr = err
				return
			}
			if len(raw.OutputSchema) == 0 {
				schemasErr = fmt.Errorf("tool %s declares no outputSchema", n)
				return
			}
			var s jsonschema.Schema
			if err := json.Unmarshal(raw.OutputSchema, &s); err != nil {
				schemasErr = err
				return
			}
			rs, err := s.Resolve(nil)
			if err != nil {
				schemasErr = fmt.Errorf("resolve %s: %w", n, err)
				return
			}
			schemas[n] = rs
		}
	})
	if schemasErr != nil {
		t.Fatalf("output schemas: %v", schemasErr)
	}
	return schemas[name]
}

// envelope is the JSON every maintenance tool returns.
type envelope struct {
	OK         bool            `json:"ok"`
	Summary    string          `json:"summary"`
	Data       *taskData       `json:"data"`
	Items      []taskData      `json:"items"`
	Total      *int            `json:"total"`
	Truncated  bool            `json:"truncated"`
	Narrow     string          `json:"narrow"`
	NextCursor string          `json:"next_cursor"`
	Next       []string        `json:"next"`
	Error      *map[string]any `json:"error"`
}

// call runs a tool and checks the respond invariants: one text block equal
// to structuredContent, valid against the advertised outputSchema, ok
// matching isError.
func call(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string, args map[string]any) envelope {
	t.Helper()
	_, _, h := ctors[name](pool)
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("%s: want 1 content block, got %d", name, len(res.Content))
	}
	text := res.Content[0].(mcp.TextContent).Text
	sc, _ := json.Marshal(res.StructuredContent)
	if string(sc) != text {
		t.Fatalf("%s: text differs from structuredContent:\n%s\n%s", name, text, sc)
	}
	var instance map[string]any
	if err := json.Unmarshal(sc, &instance); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if err := outputSchema(t, name).Validate(instance); err != nil {
		t.Fatalf("%s: result does not match outputSchema: %v\n%s", name, err, text)
	}
	var env envelope
	if err := json.Unmarshal(sc, &env); err != nil {
		t.Fatalf("%s: decode: %v", name, err)
	}
	if env.OK == res.IsError {
		t.Fatalf("%s: ok=%v isError=%v", name, env.OK, res.IsError)
	}
	return env
}

func mustOK(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string, args map[string]any) envelope {
	t.Helper()
	env := call(t, ctx, pool, name, args)
	if !env.OK {
		t.Fatalf("%s(%v) failed: %s %v", name, args, env.Summary, env.Error)
	}
	return env
}

func mustFail(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string, args map[string]any, field string) envelope {
	t.Helper()
	env := call(t, ctx, pool, name, args)
	if env.OK {
		t.Fatalf("%s(%v) succeeded, want error on %q: %s", name, args, field, env.Summary)
	}
	if got, _ := (*env.Error)["field"].(string); got != field {
		t.Fatalf("%s(%v) error field = %q, want %q (%s)", name, args, got, field, env.Summary)
	}
	return env
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func insertID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id.String()
}

// family seeds a household in tz with a Family and a Maintenance calendar.
type family struct {
	ctx           context.Context
	id            string
	maintCalendar string
	today         time.Time
}

func seedFamily(t *testing.T, pool *pgxpool.Pool, tz string) family {
	t.Helper()
	ctx, hh := testutil.SeedHousehold(t, pool)
	exec(t, pool, `UPDATE households SET timezone = $2 WHERE id = $1`, hh.HouseholdID, tz)
	insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, hh.HouseholdID)
	maint := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Maintenance', 'maintenance', gen_random_uuid()::text) RETURNING id`, hh.HouseholdID)
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatal(err)
	}
	return family{ctx: ctx, id: hh.HouseholdID.String(), maintCalendar: maint, today: todayIn(loc)}
}

func (f family) day(offset int) string { return f.today.AddDate(0, 0, offset).Format(time.DateOnly) }

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func names(items []taskData) string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Name
	}
	return strings.Join(out, ",")
}

// ─────────────────────────────────────────────
// Definitions
// ─────────────────────────────────────────────

// TestToolDefinitions pins names, required params, the four hints per
// catalog class, description lengths and the outputSchema.
func TestToolDefinitions(t *testing.T) {
	want := map[string]struct {
		class    hintClass
		required []string
		params   []string
	}{
		"maintenance_find":     {classR, nil, []string{"id", "status", "due_within_days", "property_id", "asset_id", "vehicle_id", "query", "limit", "cursor"}},
		"maintenance_create":   {classC, []string{"name", "due_date"}, []string{"name", "due_date", "description", "property_id", "asset_id", "vehicle_id", "contact_id"}},
		"maintenance_update":   {classU, []string{"task_id"}, []string{"task_id", "name", "due_date", "description", "property_id", "asset_id", "vehicle_id", "contact_id"}},
		"maintenance_complete": {classU, []string{"task_id"}, []string{"task_id", "done_date", "cost", "odometer", "notes"}},
		"maintenance_delete":   {classX, []string{"task_id"}, []string{"task_id"}},
	}
	for name, w := range want {
		t.Run(name, func(t *testing.T) {
			gotName, tool, _ := ctors[name](nil)
			if gotName != name || tool.Name != name {
				t.Fatalf("name = %q/%q", gotName, tool.Name)
			}
			if n := utf8.RuneCountInString(tool.Description); n > 200 {
				t.Errorf("description is %d chars (max 200)", n)
			}
			a := tool.Annotations
			if a.Title == "" || a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
				t.Fatalf("missing annotations: %+v", a)
			}
			ro, de, id := *a.ReadOnlyHint, *a.DestructiveHint, *a.IdempotentHint
			wantRO, wantDe, wantID := w.class == classR, w.class == classX, w.class != classC
			if ro != wantRO || de != wantDe || id != wantID || *a.OpenWorldHint {
				t.Errorf("hints ro=%v de=%v id=%v ow=%v, want %v %v %v false", ro, de, id, *a.OpenWorldHint, wantRO, wantDe, wantID)
			}
			if strings.Join(tool.InputSchema.Required, ",") != strings.Join(w.required, ",") {
				t.Errorf("required = %v, want %v", tool.InputSchema.Required, w.required)
			}
			if len(tool.InputSchema.Properties) != len(w.params) {
				t.Errorf("params = %d, want %d (%v)", len(tool.InputSchema.Properties), len(w.params), w.params)
			}
			for _, p := range w.params {
				prop, ok := tool.InputSchema.Properties[p].(map[string]any)
				if !ok {
					t.Errorf("param %s missing", p)
					continue
				}
				desc, _ := prop["description"].(string)
				if desc == "" || utf8.RuneCountInString(desc) > 80 {
					t.Errorf("param %s description %q: want 1-80 chars", p, desc)
				}
			}
			if outputSchema(t, name) == nil {
				t.Errorf("no outputSchema")
			}
		})
	}
	status := func() []any {
		_, tool, _ := NewMaintenanceFindTool(nil)
		e, _ := tool.InputSchema.Properties["status"].(map[string]any)["enum"].([]string)
		out := make([]any, len(e))
		for i, v := range e {
			out[i] = v
		}
		return out
	}()
	if fmt.Sprint(status) != "[pending done all]" {
		t.Errorf("status enum = %v", status)
	}
}

func TestNextDue(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	tests := []struct {
		done, rrule, want string // want "" = no next
	}{
		{"2026-11-15", "FREQ=DAILY;INTERVAL=90", "2027-02-13"},
		{"2026-11-15", "FREQ=WEEKLY", "2026-11-22"},
		{"2026-11-15", "RRULE:FREQ=WEEKLY;INTERVAL=2", "2026-11-29"},
		{"2027-01-31", "FREQ=MONTHLY", "2027-02-28"},
		{"2027-11-30", "FREQ=MONTHLY;INTERVAL=3", "2028-02-29"},
		{"2028-02-29", "FREQ=YEARLY", "2029-02-28"},
		{"2026-11-15", "freq=monthly;interval=6", "2027-05-15"},
		{"2026-11-15", "FREQ=MONTHLY;BYDAY=1SA", ""},
		{"2026-11-15", "FREQ=HOURLY", ""},
		{"2026-11-15", "FREQ=DAILY;INTERVAL=0", ""},
		{"2026-11-15", "", ""},
	}
	for _, tc := range tests {
		got, ok := nextDue(d(tc.done), tc.rrule)
		gotS := ""
		if ok {
			gotS = got.Format(time.DateOnly)
		}
		if gotS != tc.want {
			t.Errorf("nextDue(%s, %q) = %q, want %q", tc.done, tc.rrule, gotS, tc.want)
		}
	}
}

// ─────────────────────────────────────────────
// Behaviour (real Postgres with every migration)
// ─────────────────────────────────────────────

// TestCreate covers subjects (vehicle exclusive, property derived from the
// asset, other families' ids refused), the Maintenance calendar entry and
// the loop guard.
func TestCreate(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool, "America/New_York")
	other := seedFamily(t, pool, "UTC")

	house := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Main House') RETURNING id`, f.id)
	lake := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Lake House') RETURNING id`, f.id)
	furnace := insertID(t, pool, `INSERT INTO assets (household_id, property_id, name) VALUES ($1, $2, 'Furnace') RETURNING id`, f.id, house)
	car := insertID(t, pool, `INSERT INTO vehicles (household_id, year, make, model) VALUES ($1, 2021, 'Ford', 'F-150') RETURNING id`, f.id)
	plumber := insertID(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Joe Plumbing') RETURNING id`, f.id)
	otherAsset := insertID(t, pool, `INSERT INTO assets (household_id, name) VALUES ($1, 'Their TV') RETURNING id`, other.id)

	env := mustOK(t, f.ctx, pool, "maintenance_create", map[string]any{
		"name": "  Replace   furnace filter ", "due_date": f.day(7), "description": "16x25x1",
		"asset_id": furnace, "contact_id": plumber, "vehicle_id": "",
	})
	d := env.Data
	if d.Name != "Replace furnace filter" || *d.DueDate != f.day(7) || d.Status != "pending" || d.Duplicate {
		t.Fatalf("created %+v", d)
	}
	if d.AssetID == nil || *d.AssetID != furnace || d.PropertyID == nil || *d.PropertyID != house || *d.PropertyName != "Main House" {
		t.Errorf("asset task should derive its property: %+v", d)
	}
	if *d.ContactID != plumber || *d.ContactName != "Joe Plumbing" || *d.DaysUntilDue != 7 || d.Overdue {
		t.Errorf("contact/days: %+v", d)
	}
	if !strings.Contains(env.Summary, "for Furnace") || !strings.Contains(env.Next[0], "maintenance_complete task_id="+d.ID) {
		t.Errorf("summary/next = %q %v", env.Summary, env.Next)
	}

	// Calendar: one all-day event on the Maintenance calendar, kind
	// maintenance_task, with a change recorded.
	var calID, ical, entityType string
	if err := pool.QueryRow(context.Background(),
		`SELECT calendar_id::text, ical_data, entity_type FROM calendar_objects WHERE entity_id = $1`, d.ID,
	).Scan(&calID, &ical, &entityType); err != nil {
		t.Fatalf("calendar event: %v", err)
	}
	if calID != f.maintCalendar || entityType != "maintenance_task" {
		t.Errorf("event on %s kind %s, want Maintenance calendar, maintenance_task", calID, entityType)
	}
	wantStart := f.day(7) + "T00:00:00Z"
	if !strings.Contains(ical, `"all_day":true`) || !strings.Contains(ical, wantStart) || !strings.Contains(ical, `"title":"Replace furnace filter"`) {
		t.Errorf("ical_data = %s", ical)
	}
	if n := count(t, pool, `SELECT COUNT(*) FROM calendar_changes WHERE calendar_id = $1 AND action = 'create'`, f.maintCalendar); n != 1 {
		t.Errorf("calendar_changes = %d, want 1", n)
	}

	// Loop guard: the same job again returns the first row.
	dup := mustOK(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "replace furnace filter", "due_date": f.day(7), "asset_id": furnace})
	if !dup.Data.Duplicate || dup.Data.ID != d.ID || !strings.Contains(dup.Next[0], "maintenance_update") {
		t.Errorf("duplicate = %+v %v", dup.Data, dup.Next)
	}
	// ...but a different subject or day is a new task.
	mustOK(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "Replace furnace filter", "due_date": f.day(8), "asset_id": furnace})
	if n := count(t, pool, `SELECT COUNT(*) FROM maintenance_tasks WHERE household_id = $1`, f.id); n != 2 {
		t.Errorf("tasks = %d, want 2", n)
	}

	// Vehicle task: vehicle name, no property.
	v := mustOK(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "Oil change", "due_date": f.day(-2), "vehicle_id": car}).Data
	if *v.VehicleName != "2021 Ford F-150" || v.PropertyID != nil || !v.Overdue || *v.DaysUntilDue != -2 {
		t.Errorf("vehicle task %+v", v)
	}

	// Asset with the matching property is fine; a different property is not.
	mustOK(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "Clean burners", "due_date": f.day(3), "asset_id": furnace, "property_id": house})
	mustFail(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "X", "due_date": f.day(3), "asset_id": furnace, "property_id": lake}, "property_id")

	// Errors.
	mustFail(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "X", "due_date": f.day(1), "vehicle_id": car, "property_id": house}, "vehicle_id")
	mustFail(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "X", "due_date": f.day(1), "asset_id": otherAsset}, "asset_id")
	mustFail(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "X", "due_date": f.day(1), "property_id": "lake"}, "property_id")
	mustFail(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "X", "due_date": "next week"}, "due_date")
	mustFail(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "X"}, "due_date")
	mustFail(t, f.ctx, pool, "maintenance_create", map[string]any{"name": " ", "due_date": f.day(1)}, "name")
	mustFail(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "X", "due_date": f.day(1), "contact_id": uuid.NewString()}, "contact_id")

	// No claims.
	env = call(t, context.Background(), pool, "maintenance_create", map[string]any{"name": "X", "due_date": f.day(1)})
	if env.OK {
		t.Error("unauthenticated create succeeded")
	}
}

// TestCreate_NoMaintenanceCalendar falls back to the family's first calendar,
// and a family with no calendar still gets its task.
func TestCreate_NoMaintenanceCalendar(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	d := mustOK(t, ctx, pool, "maintenance_create", map[string]any{"name": "Gutters", "due_date": "2027-04-01"}).Data
	if n := count(t, pool, `SELECT COUNT(*) FROM calendar_objects WHERE entity_id = $1`, d.ID); n != 0 {
		t.Errorf("events = %d without any calendar", n)
	}
	fam := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, hh.HouseholdID)
	d = mustOK(t, ctx, pool, "maintenance_create", map[string]any{"name": "Windows", "due_date": "2027-04-02"}).Data
	if n := count(t, pool, `SELECT COUNT(*) FROM calendar_objects WHERE entity_id = $1 AND calendar_id = $2`, d.ID, fam); n != 1 {
		t.Errorf("event on Family calendar = %d, want 1", n)
	}
}

// TestFind covers the default (pending), status, due_within_days (overdue
// included), subject filters (property includes its assets), query, id,
// paging and family isolation.
func TestFind(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool, "Pacific/Kiritimati") // UTC+14: "today" differs from the server's often
	other := seedFamily(t, pool, "UTC")

	house := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Main House') RETURNING id`, f.id)
	furnace := insertID(t, pool, `INSERT INTO assets (household_id, property_id, name) VALUES ($1, $2, 'Furnace') RETURNING id`, f.id, house)
	car := insertID(t, pool, `INSERT INTO vehicles (household_id, make) VALUES ($1, 'Honda') RETURNING id`, f.id)

	filter := mustOK(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "Furnace filter", "due_date": f.day(5), "asset_id": furnace}).Data
	mustOK(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "Gutters", "due_date": f.day(40), "property_id": house, "description": "front and back"})
	mustOK(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "Oil change", "due_date": f.day(-3), "vehicle_id": car})
	smoke := mustOK(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "Smoke alarms", "due_date": f.day(1)}).Data
	mustOK(t, f.ctx, pool, "maintenance_complete", map[string]any{"task_id": smoke.ID})
	mustOK(t, other.ctx, pool, "maintenance_create", map[string]any{"name": "Their gutters", "due_date": other.day(1)})

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"default pending by due date", nil, "Oil change,Furnace filter,Gutters"},
		{"done", map[string]any{"status": "done"}, "Smoke alarms"},
		{"all", map[string]any{"status": "ALL"}, "Oil change,Smoke alarms,Furnace filter,Gutters"},
		{"due within 30 includes overdue", map[string]any{"due_within_days": 30}, "Oil change,Furnace filter"},
		{"due within 0 is today or overdue", map[string]any{"due_within_days": "0"}, "Oil change"},
		{"property includes its assets", map[string]any{"property_id": house}, "Furnace filter,Gutters"},
		{"asset", map[string]any{"asset_id": furnace}, "Furnace filter"},
		{"vehicle", map[string]any{"vehicle_id": car}, "Oil change"},
		{"query in description", map[string]any{"query": "BACK"}, "Gutters"},
		{"query wildcard is literal", map[string]any{"query": "%"}, ""},
		{"empty strings are ignored", map[string]any{"status": "", "query": "", "property_id": ""}, "Oil change,Furnace filter,Gutters"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := mustOK(t, f.ctx, pool, "maintenance_find", tc.args)
			if got := names(env.Items); got != tc.want {
				t.Errorf("got %q, want %q (%s)", got, tc.want, env.Summary)
			}
		})
	}

	env := mustOK(t, f.ctx, pool, "maintenance_find", map[string]any{"due_within_days": 30})
	if env.Summary != "Found 2 pending upkeep tasks due in the next 30 days or overdue (1 overdue on this page)." {
		t.Errorf("summary = %q", env.Summary)
	}
	if !env.Items[0].Overdue || env.Items[1].Overdue || *env.Items[1].DueWeekday != shortWeekday(f.today.AddDate(0, 0, 5)) {
		t.Errorf("overdue/weekday: %+v", env.Items)
	}
	empty := mustOK(t, f.ctx, pool, "maintenance_find", map[string]any{"query": "nothing like this"})
	if len(empty.Items) != 0 || !strings.Contains(empty.Next[0], "maintenance_create") {
		t.Errorf("empty = %+v", empty)
	}

	// id returns exactly one; another family's id is not found.
	one := mustOK(t, f.ctx, pool, "maintenance_find", map[string]any{"id": filter.ID, "status": "done"})
	if len(one.Items) != 1 || one.Items[0].ID != filter.ID || one.Items[0].AssetName == nil {
		t.Errorf("by id = %+v", one.Items)
	}
	if env := call(t, other.ctx, pool, "maintenance_find", map[string]any{"id": filter.ID}); env.OK {
		t.Error("other family can see the task")
	}

	// Paging.
	p1 := mustOK(t, f.ctx, pool, "maintenance_find", map[string]any{"limit": 2})
	if len(p1.Items) != 2 || !p1.Truncated || *p1.Total != 3 || p1.NextCursor != respond.EncodeCursor(2) {
		t.Fatalf("page 1 = %+v", p1)
	}
	p2 := mustOK(t, f.ctx, pool, "maintenance_find", map[string]any{"limit": 2, "cursor": p1.NextCursor})
	if names(p2.Items) != "Gutters" || p2.Truncated || p2.NextCursor != "" {
		t.Errorf("page 2 = %+v", p2)
	}

	mustFail(t, f.ctx, pool, "maintenance_find", map[string]any{"status": "open"}, "status")
	mustFail(t, f.ctx, pool, "maintenance_find", map[string]any{"due_within_days": 1000}, "due_within_days")
	mustFail(t, f.ctx, pool, "maintenance_find", map[string]any{"limit": 0}, "limit")
	mustFail(t, f.ctx, pool, "maintenance_find", map[string]any{"cursor": "bogus"}, "cursor")
	mustFail(t, f.ctx, pool, "maintenance_find", map[string]any{"vehicle_id": "the honda"}, "vehicle_id")
}

// TestUpdate covers partial updates, subject replacement (vehicle clears
// property and asset) and the calendar entry following the task.
func TestUpdate(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool, "UTC")
	house := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Main House') RETURNING id`, f.id)
	furnace := insertID(t, pool, `INSERT INTO assets (household_id, property_id, name) VALUES ($1, $2, 'Furnace') RETURNING id`, f.id, house)
	car := insertID(t, pool, `INSERT INTO vehicles (household_id, make) VALUES ($1, 'Honda') RETURNING id`, f.id)

	task := mustOK(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "Filter", "due_date": f.day(3), "asset_id": furnace, "description": "16x25"}).Data
	// A phone stored raw iCalendar for the event (CalDAV PUT): the sync
	// must clear it with the JSON change, or CalDAV/the API would keep
	// reading a raw for the old JSON (#4465).
	if _, err := pool.Exec(context.Background(),
		`UPDATE calendar_objects SET ical_raw = 'BEGIN:VCALENDAR'||chr(13)||chr(10)||'END:VCALENDAR', rrule = 'FREQ=DAILY' WHERE entity_id = $1`, task.ID); err != nil {
		t.Fatal(err)
	}

	env := mustOK(t, f.ctx, pool, "maintenance_update", map[string]any{"task_id": task.ID, "due_date": f.day(10), "name": "Furnace filter", "description": ""})
	d := env.Data
	if d.Name != "Furnace filter" || *d.DueDate != f.day(10) || *d.Description != "16x25" || *d.AssetID != furnace {
		t.Errorf("updated %+v", d)
	}
	if !strings.Contains(env.Summary, `was "Filter"`) {
		t.Errorf("summary = %q", env.Summary)
	}
	var ical string
	if err := pool.QueryRow(context.Background(), `SELECT ical_data FROM calendar_objects WHERE entity_id = $1`, task.ID).Scan(&ical); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ical, f.day(10)+"T00:00:00Z") || !strings.Contains(ical, `"title":"Furnace filter"`) {
		t.Errorf("calendar not updated: %s", ical)
	}
	if n := count(t, pool, `SELECT COUNT(*) FROM calendar_objects WHERE entity_id = $1`, task.ID); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
	if n := count(t, pool, `SELECT COUNT(*) FROM calendar_objects WHERE entity_id = $1 AND (ical_raw IS NOT NULL OR rrule IS NOT NULL)`, task.ID); n != 0 {
		t.Error("ical_raw/rrule not cleared by the calendar sync")
	}
	if n := count(t, pool, `SELECT COUNT(*) FROM calendar_changes WHERE action = 'update' AND calendar_id = $1`, f.maintCalendar); n != 1 {
		t.Errorf("update changes = %d, want 1", n)
	}

	// Subject replaced: a vehicle clears asset and property.
	d = mustOK(t, f.ctx, pool, "maintenance_update", map[string]any{"task_id": task.ID, "vehicle_id": car}).Data
	if d.VehicleID == nil || d.AssetID != nil || d.PropertyID != nil {
		t.Errorf("vehicle subject %+v", d)
	}
	// ...and a property clears the vehicle.
	d = mustOK(t, f.ctx, pool, "maintenance_update", map[string]any{"task_id": task.ID, "property_id": house}).Data
	if d.VehicleID != nil || *d.PropertyID != house || d.AssetID != nil {
		t.Errorf("property subject %+v", d)
	}
	// The DB never holds vehicle + property together.
	if n := count(t, pool, `SELECT COUNT(*) FROM maintenance_tasks WHERE vehicle_id IS NOT NULL AND (property_id IS NOT NULL OR asset_id IS NOT NULL)`); n != 0 {
		t.Errorf("%d rows with vehicle and property/asset", n)
	}

	mustFail(t, f.ctx, pool, "maintenance_update", map[string]any{"task_id": task.ID}, "")
	mustFail(t, f.ctx, pool, "maintenance_update", map[string]any{"task_id": task.ID, "vehicle_id": car, "asset_id": furnace}, "vehicle_id")
	mustFail(t, f.ctx, pool, "maintenance_update", map[string]any{"task_id": "abc", "name": "x"}, "task_id")
	mustFail(t, f.ctx, pool, "maintenance_update", map[string]any{"name": "x"}, "task_id")
	if env := call(t, f.ctx, pool, "maintenance_update", map[string]any{"task_id": uuid.NewString(), "name": "x"}); env.OK {
		t.Error("unknown task updated")
	}
}

// TestComplete covers done_date, cost, notes + odometer, idempotency and a
// repeating schedule adding its next task exactly once.
func TestComplete(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool, "America/Los_Angeles")
	car := insertID(t, pool, `INSERT INTO vehicles (household_id, make) VALUES ($1, 'Honda') RETURNING id`, f.id)

	oil := mustOK(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "Oil change", "due_date": f.day(0), "vehicle_id": car}).Data
	env := mustOK(t, f.ctx, pool, "maintenance_complete", map[string]any{
		"task_id": oil.ID, "done_date": f.day(-1), "cost": "$64.99", "odometer": 45210, "notes": "Synthetic 0W-20",
	})
	d := env.Data
	if d.Status != "done" || *d.CompletedDate != f.day(-1) || *d.Cost != 64.99 || *d.AlreadyDone {
		t.Errorf("completed %+v", d)
	}
	if d.Notes == nil || *d.Notes != "Synthetic 0W-20\nOdometer: 45210" {
		t.Errorf("notes = %v", d.Notes)
	}
	if d.NextTaskID != nil || !strings.Contains(env.Summary, "done on") {
		t.Errorf("non-recurring: %+v %q", d, env.Summary)
	}

	again := mustOK(t, f.ctx, pool, "maintenance_complete", map[string]any{"task_id": oil.ID, "notes": "again", "cost": 1})
	if !*again.Data.AlreadyDone || *again.Data.Notes != "Synthetic 0W-20\nOdometer: 45210" || *again.Data.Cost != 64.99 {
		t.Errorf("second complete changed something: %+v", again.Data)
	}
	if !strings.Contains(again.Summary, "already marked done on") {
		t.Errorf("summary = %q", again.Summary)
	}

	// Repeating: a schedule every 3 months.
	sched := insertID(t, pool, `INSERT INTO maintenance_schedules (household_id, vehicle_id, name, rrule) VALUES ($1, $2, 'Rotate tires', 'FREQ=MONTHLY;INTERVAL=3') RETURNING id`, f.id, car)
	rot := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, schedule_id, vehicle_id, name, due_date) VALUES ($1, $2, $3, 'Rotate tires', $4) RETURNING id`, f.id, sched, car, f.day(0))
	env = mustOK(t, f.ctx, pool, "maintenance_complete", map[string]any{"task_id": rot})
	wantNext := addMonths(f.today, 3).Format(time.DateOnly)
	if env.Data.NextTaskID == nil || *env.Data.NextDueDate != wantNext || !env.Data.Recurring {
		t.Fatalf("recurring complete = %+v", env.Data)
	}
	if !strings.Contains(env.Summary, "Next due") || !strings.Contains(env.Next[0], "Next due") {
		t.Errorf("summary/next = %q %v", env.Summary, env.Next)
	}
	next := mustOK(t, f.ctx, pool, "maintenance_find", map[string]any{"id": *env.Data.NextTaskID}).Items[0]
	if next.Status != "pending" || *next.VehicleID != car || next.Name != "Rotate tires" {
		t.Errorf("next task %+v", next)
	}
	if n := count(t, pool, `SELECT COUNT(*) FROM calendar_objects WHERE entity_id = $1 AND entity_type = 'maintenance_task'`, next.ID); n != 1 {
		t.Errorf("next task events = %d", n)
	}
	mustOK(t, f.ctx, pool, "maintenance_complete", map[string]any{"task_id": rot})
	if n := count(t, pool, `SELECT COUNT(*) FROM maintenance_tasks WHERE schedule_id = $1`, sched); n != 2 {
		t.Errorf("schedule tasks = %d, want 2 (no second next task)", n)
	}

	mustFail(t, f.ctx, pool, "maintenance_complete", map[string]any{"task_id": next.ID, "done_date": f.day(2)}, "done_date")
	mustFail(t, f.ctx, pool, "maintenance_complete", map[string]any{"task_id": next.ID, "cost": -5}, "cost")
	mustFail(t, f.ctx, pool, "maintenance_complete", map[string]any{"task_id": next.ID, "odometer": 1.5}, "odometer")
	if env := call(t, f.ctx, pool, "maintenance_complete", map[string]any{"task_id": uuid.NewString()}); env.OK {
		t.Error("unknown task completed")
	}
}

// TestDelete removes the task and its calendar entry, records the change,
// and a repeat call deletes nothing.
func TestDelete(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool, "UTC")
	other := seedFamily(t, pool, "UTC")
	task := mustOK(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "Gutters", "due_date": f.day(4)}).Data

	if env := call(t, other.ctx, pool, "maintenance_delete", map[string]any{"task_id": task.ID}); env.OK {
		t.Fatal("other family deleted the task")
	}
	env := mustOK(t, f.ctx, pool, "maintenance_delete", map[string]any{"task_id": task.ID})
	if env.Data.ID != task.ID || !strings.Contains(env.Summary, "calendar entry") {
		t.Errorf("delete = %+v %q", env.Data, env.Summary)
	}
	if n := count(t, pool, `SELECT COUNT(*) FROM maintenance_tasks WHERE id = $1`, task.ID); n != 0 {
		t.Error("task still there")
	}
	if n := count(t, pool, `SELECT COUNT(*) FROM calendar_objects WHERE entity_id = $1`, task.ID); n != 0 {
		t.Error("calendar entry still there")
	}
	if n := count(t, pool, `SELECT COUNT(*) FROM calendar_changes WHERE calendar_id = $1 AND action = 'delete'`, f.maintCalendar); n != 1 {
		t.Errorf("delete changes = %d, want 1", n)
	}
	if env := call(t, f.ctx, pool, "maintenance_delete", map[string]any{"task_id": task.ID}); env.OK {
		t.Error("second delete succeeded")
	}
	mustFail(t, f.ctx, pool, "maintenance_delete", map[string]any{"task_id": "gutters"}, "task_id")
}
