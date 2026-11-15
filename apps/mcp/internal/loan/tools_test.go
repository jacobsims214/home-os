package loan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/testutil"
)

// --- shape helpers (pattern from internal/respond and internal/bill tests) ---

func toolDefs() []mcp.Tool {
	out := []mcp.Tool{}
	for _, mk := range []func(*pgxpool.Pool) (string, mcp.Tool, toolHandler){
		NewLoanFindTool, NewLoanCreateTool, NewLoanUpdateTool, NewLoanDeleteTool,
	} {
		_, tool, _ := mk(nil)
		out = append(out, tool)
	}
	return out
}

var (
	schemasOnce sync.Once
	schemas     map[string]*jsonschema.Resolved
	schemasErr  error
)

func outputSchema(t *testing.T, name string) *jsonschema.Resolved {
	t.Helper()
	schemasOnce.Do(func() {
		schemas = map[string]*jsonschema.Resolved{}
		for _, tool := range toolDefs() {
			b, err := json.Marshal(tool)
			if err != nil {
				schemasErr = err
				return
			}
			var raw struct {
				OutputSchema json.RawMessage `json:"outputSchema"`
			}
			if err := json.Unmarshal(b, &raw); err != nil || len(raw.OutputSchema) == 0 {
				schemasErr = fmt.Errorf("tool %s declares no outputSchema (%v)", tool.Name, err)
				return
			}
			var s jsonschema.Schema
			if err := json.Unmarshal(raw.OutputSchema, &s); err != nil {
				schemasErr = err
				return
			}
			rs, err := s.Resolve(nil)
			if err != nil {
				schemasErr = fmt.Errorf("resolve %s outputSchema: %w", tool.Name, err)
				return
			}
			schemas[tool.Name] = rs
		}
	})
	if schemasErr != nil {
		t.Fatalf("output schemas: %v", schemasErr)
	}
	rs := schemas[name]
	if rs == nil {
		t.Fatalf("no loan tool named %q", name)
	}
	return rs
}

// wireEnvelope is the JSON every loan tool returns.
type wireEnvelope struct {
	OK         bool            `json:"ok"`
	Summary    string          `json:"summary"`
	Data       json.RawMessage `json:"data"`
	Items      json.RawMessage `json:"items"`
	Total      *int            `json:"total"`
	Truncated  bool            `json:"truncated"`
	Narrow     string          `json:"narrow"`
	NextCursor string          `json:"next_cursor"`
	Next       []string        `json:"next"`
	Error      *struct {
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Field   string         `json:"field"`
		Example map[string]any `json:"example"`
	} `json:"error"`
}

// call runs a handler and checks the respond invariants: one text block
// equal to structuredContent, valid against the advertised outputSchema
// (errors included), ok == !isError.
func call(t *testing.T, ctx context.Context, h toolHandler, name string, args map[string]any) wireEnvelope {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("%s returned a Go error (protocol fault): %v", name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("%s: want 1 content item, got %d", name, len(res.Content))
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("%s: want TextContent, got %T", name, res.Content[0])
	}
	sc, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("%s: marshal structuredContent: %v", name, err)
	}
	if string(sc) != text.Text {
		t.Fatalf("%s: text differs from structuredContent:\n%s\n%s", name, text.Text, sc)
	}
	var instance map[string]any
	if err := json.Unmarshal(sc, &instance); err != nil {
		t.Fatalf("%s: structuredContent is not an object: %v", name, err)
	}
	if err := outputSchema(t, name).Validate(instance); err != nil {
		t.Fatalf("%s: result does not match its outputSchema: %v\n%s", name, err, text.Text)
	}
	var env wireEnvelope
	if err := json.Unmarshal(sc, &env); err != nil {
		t.Fatalf("%s: decode: %v", name, err)
	}
	if env.OK == res.IsError {
		t.Fatalf("%s: ok=%v but isError=%v", name, env.OK, res.IsError)
	}
	return env
}

func (e wireEnvelope) loan(t *testing.T) loanData {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d loanData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return d
}

func (e wireEnvelope) loans(t *testing.T) []loanData {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d []loanData
	if err := json.Unmarshal(e.Items, &d); err != nil {
		t.Fatalf("decode items: %v", err)
	}
	return d
}

func wantError(t *testing.T, e wireEnvelope, field string) {
	t.Helper()
	if e.OK || e.Error == nil {
		t.Fatalf("want isError on %q, got success: %s", field, e.Summary)
	}
	if e.Error.Field != field {
		t.Fatalf("error field = %q, want %q (%s)", e.Error.Field, field, e.Error.Message)
	}
	if e.Error.Fix == "" || len(e.Error.Example) == 0 && field != "loan_id" && field != "id" {
		t.Errorf("error lacks fix/example: %+v", e.Error)
	}
}

// --- fixtures ---

type fixture struct {
	pool                   *pgxpool.Pool
	ctx                    context.Context
	hh                     testutil.Household
	find, create, upd, del toolHandler
}

func setup(t *testing.T, tz string) fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	if _, err := pool.Exec(context.Background(), `UPDATE households SET timezone = $2 WHERE id = $1`, hh.HouseholdID, tz); err != nil {
		t.Fatalf("set timezone: %v", err)
	}
	f := fixture{pool: pool, ctx: ctx, hh: hh}
	_, _, f.find = NewLoanFindTool(pool)
	_, _, f.create = NewLoanCreateTool(pool)
	_, _, f.upd = NewLoanUpdateTool(pool)
	_, _, f.del = NewLoanDeleteTool(pool)
	return f
}

func insertID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("insert: %v\n%s", err, sql)
	}
	return id.String()
}

func (f fixture) property(t *testing.T, hh uuid.UUID, name string) string {
	return insertID(t, f.pool, `INSERT INTO properties (household_id, name) VALUES ($1, $2) RETURNING id`, hh, name)
}

func (f fixture) vehicle(t *testing.T, hh uuid.UUID) string {
	return insertID(t, f.pool, `INSERT INTO vehicles (household_id, year, make, model) VALUES ($1, 2022, 'Honda', 'Odyssey') RETURNING id`, hh)
}

// subjectColumns reads a loan's typed and legacy subject columns.
func (f fixture) subjectColumns(t *testing.T, id string) (prop, veh, asset, entityType, entityID *string) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(),
		`SELECT property_id::text, vehicle_id::text, asset_id::text, entity_type, entity_id::text FROM loans WHERE id = $1`, id,
	).Scan(&prop, &veh, &asset, &entityType, &entityID); err != nil {
		t.Fatalf("read subject columns: %v", err)
	}
	return
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// --- tests ---

// TestToolDefinitions pins every loan tool to the catalog: name, required
// and advertised params, all four hints (class R/C/U/X), description
// lengths, no legacy params advertised, and string ids in the outputSchema.
func TestToolDefinitions(t *testing.T) {
	type hints struct{ readOnly, destructive, idempotent bool }
	createParams := []string{"name", "original_amount", "remaining_balance", "lender", "interest_rate", "term_months",
		"monthly_payment", "due_day", "start_date", "notes", "property_id", "vehicle_id", "asset_id"}
	want := map[string]struct {
		title    string
		required []string
		params   []string
		class    hints
	}{
		"loan_find":   {"Find loans", nil, []string{"id", "query", "property_id", "vehicle_id", "limit", "cursor"}, hints{true, false, true}},
		"loan_create": {"Create loan", []string{"name", "original_amount", "remaining_balance"}, createParams, hints{false, false, false}},
		"loan_update": {"Update loan", []string{"loan_id"}, append([]string{"loan_id"}, createParams...), hints{false, false, true}},
		"loan_delete": {"Delete loan", []string{"loan_id"}, []string{"loan_id"}, hints{false, true, true}},
	}
	tools := toolDefs()
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(tools), len(want))
	}
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			w, ok := want[tool.Name]
			if !ok {
				t.Fatalf("unexpected tool %q", tool.Name)
			}
			if fmt.Sprint(tool.InputSchema.Required) != fmt.Sprint(w.required) && !(len(w.required) == 0 && len(tool.InputSchema.Required) == 0) {
				t.Errorf("required = %v, want %v", tool.InputSchema.Required, w.required)
			}
			if len(tool.InputSchema.Properties) != len(w.params) {
				t.Errorf("got %d params, want %v", len(tool.InputSchema.Properties), w.params)
			}
			for _, p := range w.params {
				if _, ok := tool.InputSchema.Properties[p]; !ok {
					t.Errorf("missing param %q", p)
				}
			}
			for _, old := range []string{"entity_type", "entity_id", "kind"} {
				if _, ok := tool.InputSchema.Properties[old]; ok {
					t.Errorf("advertises legacy/unbuilt param %q", old)
				}
			}
			if n := len(tool.Description); n == 0 || n > 200 {
				t.Errorf("description is %d chars, want 1-200", n)
			}
			for param, schema := range tool.InputSchema.Properties {
				d, _ := schema.(map[string]any)["description"].(string)
				if d == "" || len(d) > 80 {
					t.Errorf("%s description is %d chars, want 1-80", param, len(d))
				}
			}
			a := tool.Annotations
			if a.Title != w.title {
				t.Errorf("title = %q, want %q", a.Title, w.title)
			}
			if a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
				t.Fatalf("annotations not all explicit: %+v", a)
			}
			if got := (hints{*a.ReadOnlyHint, *a.DestructiveHint, *a.IdempotentHint}); got != w.class || *a.OpenWorldHint {
				t.Errorf("hints = %+v openWorld=%v, want %+v openWorld=false", got, *a.OpenWorldHint, w.class)
			}
			b, _ := json.Marshal(tool)
			var raw struct {
				OutputSchema map[string]any `json:"outputSchema"`
			}
			if err := json.Unmarshal(b, &raw); err != nil || raw.OutputSchema == nil {
				t.Fatalf("no outputSchema: %v", err)
			}
			top := raw.OutputSchema["properties"].(map[string]any)
			var item map[string]any
			if d, ok := top["data"]; ok {
				item = d.(map[string]any)
			} else {
				item = top["items"].(map[string]any)["items"].(map[string]any)
			}
			props := item["properties"].(map[string]any)
			for _, id := range []string{"id", "property_id", "vehicle_id", "asset_id"} {
				if s := fmt.Sprint(props[id].(map[string]any)["type"]); !strings.Contains(s, "string") {
					t.Errorf("%s schema type = %s, want string", id, s)
				}
			}
			outputSchema(t, tool.Name)
		})
	}
}

// TestLoanCreate covers typed subjects (no legacy columns written), term_end,
// next due date, the loop guard, the entity_type alias and validation.
func TestLoanCreate(t *testing.T) {
	f := setup(t, "America/New_York")
	house := f.property(t, f.hh.HouseholdID, "Maple St")

	env := call(t, f.ctx, f.create, "loan_create", map[string]any{
		"name": "Mortgage", "original_amount": 320000.0, "remaining_balance": 251340.123,
		"lender": "Chase", "interest_rate": 6.25, "term_months": 360.0, "monthly_payment": 1850.0,
		"due_day": 31.0, "start_date": "2021-01-31", "property_id": house,
	})
	d := env.loan(t)
	if d.RemainingBalance != 251340.12 || d.InterestRate == nil || *d.InterestRate != 6.25 || *d.DueDay != 31 {
		t.Errorf("stored values = %+v", d)
	}
	if deref(d.PropertyID) != house || deref(d.SubjectType) != "property" || deref(d.SubjectName) != "Maple St" {
		t.Errorf("subject = %v %v %v", deref(d.PropertyID), deref(d.SubjectType), deref(d.SubjectName))
	}
	// Postgres month arithmetic: Jan 31 2021 + 360 months = Jan 31 2051.
	if deref(d.TermEnd) != "2051-01-31" {
		t.Errorf("term_end = %v, want 2051-01-31", deref(d.TermEnd))
	}
	if d.NextDueDate == nil || d.NextDueWeekday == nil || d.DaysUntilDue == nil || *d.DaysUntilDue < 0 || *d.DaysUntilDue > 31 {
		t.Fatalf("next due = %v %v %v", deref(d.NextDueDate), deref(d.NextDueWeekday), deref(d.DaysUntilDue))
	}
	due, _ := time.Parse(time.DateOnly, *d.NextDueDate)
	if due.Weekday().String()[:3] != *d.NextDueWeekday {
		t.Errorf("weekday %s does not match %s", *d.NextDueWeekday, *d.NextDueDate)
	}
	if !strings.HasPrefix(env.Summary, "Created loan Mortgage: $1,850.00/month due ") ||
		!strings.Contains(env.Summary, "at 6.25%, with Chase, $251,340.12 left") {
		t.Errorf("summary = %q", env.Summary)
	}
	if len(env.Next) == 0 || !strings.Contains(env.Next[0], "budget_summary") {
		t.Errorf("next = %v, want budget_summary first", env.Next)
	}
	prop, veh, asset, et, eid := f.subjectColumns(t, d.ID)
	if deref(prop) != house || veh != nil || asset != nil || et != nil || eid != nil {
		t.Errorf("columns = %v %v %v legacy %v %v; want typed property only", deref(prop), veh, asset, deref(et), deref(eid))
	}

	// Loop guard: same name (any case) and amounts within 10 minutes.
	dup := call(t, f.ctx, f.create, "loan_create", map[string]any{
		"name": "mortgage", "original_amount": 320000.0, "remaining_balance": 251340.12,
	})
	if dd := dup.loan(t); !dd.Duplicate || dd.ID != d.ID || !strings.Contains(dup.Next[0], "loan_update loan_id="+d.ID) {
		t.Errorf("duplicate = %+v next %v", dd, dup.Next)
	}

	// Legacy entity_type/entity_id alias writes the typed vehicle_id; no
	// monthly_payment gives a loan_update hint.
	van := f.vehicle(t, f.hh.HouseholdID)
	alias := call(t, f.ctx, f.create, "loan_create", map[string]any{
		"name": "Van loan", "original_amount": "30000", "remaining_balance": 12000.0,
		"entity_type": "vehicle", "entity_id": van,
	}).loan(t)
	if deref(alias.VehicleID) != van || deref(alias.SubjectName) != "2022 Honda Odyssey" || alias.NextDueDate != nil {
		t.Errorf("alias loan = %+v", alias)
	}
	if _, veh, _, et, _ := f.subjectColumns(t, alias.ID); deref(veh) != van || et != nil {
		t.Errorf("alias columns veh=%v legacy=%v", deref(veh), deref(et))
	}

	// Validation: nothing is inserted.
	var otherHH uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO households (name) VALUES ('Other') RETURNING id`).Scan(&otherHH); err != nil {
		t.Fatal(err)
	}
	foreignHouse := f.property(t, otherHH, "Not ours")
	cases := []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"missing name", map[string]any{"original_amount": 1.0, "remaining_balance": 1.0}, "name"},
		{"missing balance", map[string]any{"name": "X", "original_amount": 1.0}, "remaining_balance"},
		{"rate as decimal too high", map[string]any{"name": "X", "original_amount": 1.0, "remaining_balance": 1.0, "interest_rate": 625.0}, "interest_rate"},
		{"bad due_day", map[string]any{"name": "X", "original_amount": 1.0, "remaining_balance": 1.0, "due_day": 32.0}, "due_day"},
		{"bad date", map[string]any{"name": "X", "original_amount": 1.0, "remaining_balance": 1.0, "start_date": "June 2021"}, "start_date"},
		{"negative", map[string]any{"name": "X", "original_amount": -1.0, "remaining_balance": 1.0}, "original_amount"},
		{"vehicle and property", map[string]any{"name": "X", "original_amount": 1.0, "remaining_balance": 1.0, "vehicle_id": van, "property_id": house}, "vehicle_id"},
		{"other family's property", map[string]any{"name": "X", "original_amount": 1.0, "remaining_balance": 1.0, "property_id": foreignHouse}, "property_id"},
		{"name not uuid", map[string]any{"name": "X", "original_amount": 1.0, "remaining_balance": 1.0, "property_id": "Maple St"}, "property_id"},
		{"unknown entity_type", map[string]any{"name": "X", "original_amount": 1.0, "remaining_balance": 1.0, "entity_type": "pet", "entity_id": van}, "property_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantError(t, call(t, f.ctx, f.create, "loan_create", c.args), c.field)
		})
	}
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM loans`).Scan(&n); err != nil || n != 2 {
		t.Errorf("loans = %d (%v), want 2", n, err)
	}
}

// TestLoanFind covers list, id, query (incl. subject name), subject filters,
// the legacy entity_type filter, pagination and household scoping.
func TestLoanFind(t *testing.T) {
	f := setup(t, "UTC")

	empty := call(t, f.ctx, f.find, "loan_find", nil)
	if empty.Summary != "No loans yet." || len(empty.loans(t)) != 0 || !strings.Contains(empty.Next[0], "loan_create") {
		t.Errorf("empty = %q %v", empty.Summary, empty.Next)
	}

	house := f.property(t, f.hh.HouseholdID, "Lake House")
	van := f.vehicle(t, f.hh.HouseholdID)
	mortgage := call(t, f.ctx, f.create, "loan_create", map[string]any{
		"name": "Mortgage", "original_amount": 300000.0, "remaining_balance": 200000.0, "monthly_payment": 1500.0,
		"interest_rate": 5.5, "due_day": 1.0, "lender": "Wells", "property_id": house,
	}).loan(t)
	car := call(t, f.ctx, f.create, "loan_create", map[string]any{
		"name": "Car loan", "original_amount": 30000.0, "remaining_balance": 9000.0, "vehicle_id": van,
	}).loan(t)
	student := call(t, f.ctx, f.create, "loan_create", map[string]any{
		"name": "Student loan", "original_amount": 40000.0, "remaining_balance": 0.0, "monthly_payment": 300.0, "due_day": 15.0,
	}).loan(t)

	all := call(t, f.ctx, f.find, "loan_find", nil)
	got := all.loans(t)
	if len(got) != 3 || got[0].ID != car.ID || got[1].ID != mortgage.ID || got[2].ID != student.ID {
		t.Fatalf("order = %v", got)
	}
	if all.Summary != "Found 3 loans." || !strings.Contains(strings.Join(all.Next, " "), "Car loan has no monthly_payment; set it with loan_update loan_id="+car.ID) {
		t.Errorf("summary/next = %q %v", all.Summary, all.Next)
	}
	if !got[2].PaidOff || got[2].NextDueDate != nil {
		t.Errorf("paid-off loan = %+v", got[2])
	}

	one := call(t, f.ctx, f.find, "loan_find", map[string]any{"id": mortgage.ID})
	if l := one.loans(t); len(l) != 1 || !strings.HasPrefix(one.Summary, "Found loan Mortgage: $1,500.00/month due ") {
		t.Errorf("by id = %q", one.Summary)
	}
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"query lender":        {map[string]any{"query": "wells"}, mortgage.ID},
		"query subject":       {map[string]any{"query": "lake"}, mortgage.ID},
		"query vehicle":       {map[string]any{"query": "odyssey"}, car.ID},
		"property_id":         {map[string]any{"property_id": house}, mortgage.ID},
		"vehicle_id":          {map[string]any{"vehicle_id": van}, car.ID},
		"legacy entity_type":  {map[string]any{"entity_type": "vehicle"}, car.ID},
		"legacy entity pair":  {map[string]any{"entity_type": "property", "entity_id": house}, mortgage.ID},
		"legacy loan_id name": {map[string]any{"loan_id": student.ID}, student.ID},
	} {
		t.Run(name, func(t *testing.T) {
			l := call(t, f.ctx, f.find, "loan_find", tc.args).loans(t)
			if len(l) != 1 || l[0].ID != tc.want {
				t.Errorf("got %v, want only %s", l, tc.want)
			}
		})
	}

	// Pagination.
	p1 := call(t, f.ctx, f.find, "loan_find", map[string]any{"limit": 2.0})
	if len(p1.loans(t)) != 2 || !p1.Truncated || p1.Total == nil || *p1.Total != 3 || p1.NextCursor != respond.EncodeCursor(2) {
		t.Fatalf("page 1 = %+v", p1)
	}
	p2 := call(t, f.ctx, f.find, "loan_find", map[string]any{"limit": 2.0, "cursor": p1.NextCursor})
	if l := p2.loans(t); len(l) != 1 || l[0].ID != student.ID || p2.Truncated || p2.NextCursor != "" || p2.Summary != "Found 3 loans; showing 3-3." {
		t.Errorf("page 2 = %+v", p2)
	}

	wantError(t, call(t, f.ctx, f.find, "loan_find", map[string]any{"cursor": "abc"}), "cursor")
	wantError(t, call(t, f.ctx, f.find, "loan_find", map[string]any{"id": "Mortgage"}), "id")
	missing := call(t, f.ctx, f.find, "loan_find", map[string]any{"id": uuid.NewString()})
	if missing.OK {
		t.Errorf("unknown id should be isError")
	}

	// Another household sees none of these loans.
	otherCtx, _ := testutil.SeedHousehold(t, f.pool)
	if l := call(t, otherCtx, f.find, "loan_find", nil).loans(t); len(l) != 0 {
		t.Errorf("other household sees %d loans", len(l))
	}
	if call(t, otherCtx, f.find, "loan_find", map[string]any{"id": mortgage.ID}).OK {
		t.Errorf("other household can read the mortgage by id")
	}
	if call(t, context.Background(), f.find, "loan_find", nil).OK {
		t.Errorf("no claims should be isError")
	}
}

// TestLoanUpdate covers partial updates, subject replacement and clearing,
// term_end following start_date/term_months, aliases and errors.
func TestLoanUpdate(t *testing.T) {
	f := setup(t, "UTC")
	house := f.property(t, f.hh.HouseholdID, "Maple St")
	van := f.vehicle(t, f.hh.HouseholdID)
	l := call(t, f.ctx, f.create, "loan_create", map[string]any{
		"name": "Mortgage", "original_amount": 300000.0, "remaining_balance": 200000.0,
		"lender": "Chase", "notes": "refi in 2027", "property_id": house,
	}).loan(t)

	env := call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": l.ID, "remaining_balance": 199000.0, "monthly_payment": 1500.0})
	u := env.loan(t)
	if u.RemainingBalance != 199000 || deref(u.MonthlyPayment) != 1500.0 || deref(u.Lender) != "Chase" || deref(u.PropertyID) != house {
		t.Errorf("partial update = %+v", u)
	}
	if !strings.HasPrefix(env.Summary, "Updated loan Mortgage (remaining_balance, monthly_payment):") {
		t.Errorf("summary = %q", env.Summary)
	}

	// Repeat: idempotent.
	if again := call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": l.ID, "remaining_balance": 199000.0}).loan(t); again.RemainingBalance != 199000 {
		t.Errorf("repeat = %+v", again)
	}

	// term_end follows start_date + term_months.
	u = call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": l.ID, "start_date": "2020-03-31", "term_months": 12.0}).loan(t)
	if deref(u.TermEnd) != "2021-03-31" || !u.PaidOff {
		t.Errorf("term_end = %v paid_off=%v, want 2021-03-31 and paid off (term ended)", deref(u.TermEnd), u.PaidOff)
	}
	u = call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": l.ID, "term_months": 360.0}).loan(t)
	if deref(u.TermEnd) != "2050-03-31" || u.PaidOff {
		t.Errorf("term_end after term change = %v", deref(u.TermEnd))
	}

	// Subject: a vehicle replaces the property (the CHECK would otherwise fail).
	u = call(t, f.ctx, f.upd, "loan_update", map[string]any{"id": l.ID, "vehicle_id": van}).loan(t)
	if deref(u.VehicleID) != van || u.PropertyID != nil || deref(u.SubjectType) != "vehicle" {
		t.Errorf("vehicle subject = %+v", u)
	}
	// Blank clears text fields and the subject.
	u = call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": l.ID, "vehicle_id": "", "lender": "", "notes": " "}).loan(t)
	if u.VehicleID != nil || u.SubjectType != nil || u.Lender != nil || u.Notes != nil {
		t.Errorf("cleared = %+v", u)
	}
	// Legacy alias sets the typed property again.
	call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": l.ID, "entity_type": "property", "entity_id": house})
	if prop, veh, _, et, _ := f.subjectColumns(t, l.ID); deref(prop) != house || veh != nil || et != nil {
		t.Errorf("alias update columns = %v %v %v", deref(prop), deref(veh), deref(et))
	}

	cases := []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"no loan_id", map[string]any{"name": "X"}, "loan_id"},
		{"bad loan_id", map[string]any{"loan_id": "Mortgage", "name": "X"}, "loan_id"},
		{"no fields", map[string]any{"loan_id": l.ID}, ""},
		{"clear name", map[string]any{"loan_id": l.ID, "name": ""}, "name"},
		{"clear balance", map[string]any{"loan_id": l.ID, "remaining_balance": ""}, "remaining_balance"},
		{"bad rate", map[string]any{"loan_id": l.ID, "interest_rate": "six"}, "interest_rate"},
		{"unknown vehicle", map[string]any{"loan_id": l.ID, "vehicle_id": uuid.NewString()}, "vehicle_id"},
		{"start after term_end", map[string]any{"loan_id": l.ID, "start_date": "2060-01-01"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := call(t, f.ctx, f.upd, "loan_update", c.args)
			if c.name == "start after term_end" {
				// term_end is recomputed from the new start (term_months set), so this is valid.
				if d := e.loan(t); deref(d.TermEnd) != "2090-01-01" {
					t.Errorf("term_end = %v", deref(d.TermEnd))
				}
				return
			}
			wantError(t, e, c.field)
		})
	}

	otherCtx, _ := testutil.SeedHousehold(t, f.pool)
	if call(t, otherCtx, f.upd, "loan_update", map[string]any{"loan_id": l.ID, "name": "Mine"}).OK {
		t.Errorf("other household updated the loan")
	}
	if call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": uuid.NewString(), "name": "X"}).OK {
		t.Errorf("unknown loan should be isError")
	}
}

// TestLoanDelete covers delete, the repeat (not found, nothing deleted) and
// household scoping.
func TestLoanDelete(t *testing.T) {
	f := setup(t, "UTC")
	keep := call(t, f.ctx, f.create, "loan_create", map[string]any{"name": "Keep", "original_amount": 1.0, "remaining_balance": 1.0}).loan(t)
	gone := call(t, f.ctx, f.create, "loan_create", map[string]any{"name": "HELOC", "original_amount": 50000.0, "remaining_balance": 10.0}).loan(t)

	otherCtx, _ := testutil.SeedHousehold(t, f.pool)
	if call(t, otherCtx, f.del, "loan_delete", map[string]any{"loan_id": gone.ID}).OK {
		t.Fatalf("other household deleted the loan")
	}

	env := call(t, f.ctx, f.del, "loan_delete", map[string]any{"loan_id": gone.ID})
	if d := env.loan(t); d.ID != gone.ID || env.Summary != "Deleted loan HELOC." {
		t.Errorf("delete = %q %+v", env.Summary, d)
	}
	again := call(t, f.ctx, f.del, "loan_delete", map[string]any{"id": gone.ID})
	if again.OK {
		t.Errorf("repeat delete should be isError not found")
	}
	wantError(t, call(t, f.ctx, f.del, "loan_delete", nil), "loan_id")
	if l := call(t, f.ctx, f.find, "loan_find", nil).loans(t); len(l) != 1 || l[0].ID != keep.ID {
		t.Errorf("remaining = %v", l)
	}
}

// TestMoney pins the summary money format.
func TestMoney(t *testing.T) {
	for in, want := range map[float64]string{0: "$0.00", 12.5: "$12.50", 1850: "$1,850.00", 251340.12: "$251,340.12", 1234567.8: "$1,234,567.80", -42: "-$42.00"} {
		if got := money(in); got != want {
			t.Errorf("money(%v) = %q, want %q", in, got, want)
		}
	}
}

// TestNextDueDate pins the due-day clamp and the start_date rule.
func TestNextDueDate(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	for _, c := range []struct {
		today string
		day   int
		want  string
	}{
		{"2026-11-15", 31, "2026-11-30"},
		{"2026-11-15", 15, "2026-11-15"},
		{"2026-11-15", 1, "2026-12-01"},
		{"2027-02-01", 30, "2027-02-28"},
		{"2026-12-31", 5, "2027-01-05"},
	} {
		if got := nextDueDate(d(c.today), c.day).Format(time.DateOnly); got != c.want {
			t.Errorf("nextDueDate(%s, %d) = %s, want %s", c.today, c.day, got, c.want)
		}
	}
	start := d("2027-03-10")
	r := loanRow{RemainingBalance: 1, DueDay: ptr(1), StartDate: &start}
	if got := r.data(d("2026-11-15")); deref(got.NextDueDate) != "2027-04-01" {
		t.Errorf("future start: next due = %v, want 2027-04-01", deref(got.NextDueDate))
	}
}

func ptr[T any](v T) *T { return &v }
