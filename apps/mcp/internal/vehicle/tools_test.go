package vehicle

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/testutil"
)

// --- shape helpers (pattern from internal/respond and internal/loan tests) ---

func findTool() mcp.Tool {
	_, tool, _ := NewVehicleFindTool(nil)
	return tool
}

func outputSchema(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(findTool())
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || len(raw.OutputSchema) == 0 {
		t.Fatalf("vehicle_find declares no outputSchema (%v)", err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw.OutputSchema, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve outputSchema: %v", err)
	}
	return rs
}

type wireEnvelope struct {
	OK         bool            `json:"ok"`
	Summary    string          `json:"summary"`
	Items      json.RawMessage `json:"items"`
	Returned   *int            `json:"returned"`
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

// call runs the handler and checks the respond invariants: one text block
// equal to structuredContent, valid against the advertised outputSchema
// (errors included), ok == !isError.
func call(t *testing.T, ctx context.Context, h toolHandler, args map[string]any) wireEnvelope {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = "vehicle_find"
	req.Params.Arguments = args
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("vehicle_find returned a Go error (protocol fault): %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("want 1 content item, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	sc, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if string(sc) != text.Text {
		t.Fatalf("text differs from structuredContent:\n%s\n%s", text.Text, sc)
	}
	var instance map[string]any
	if err := json.Unmarshal(sc, &instance); err != nil {
		t.Fatal(err)
	}
	if err := outputSchema(t).Validate(instance); err != nil {
		t.Fatalf("result does not match its outputSchema: %v\n%s", err, text.Text)
	}
	var env wireEnvelope
	if err := json.Unmarshal(sc, &env); err != nil {
		t.Fatal(err)
	}
	if env.OK == res.IsError {
		t.Fatalf("ok=%v but isError=%v", env.OK, res.IsError)
	}
	return env
}

func (e wireEnvelope) vehicles(t *testing.T) []vehicleData {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d []vehicleData
	if err := json.Unmarshal(e.Items, &d); err != nil {
		t.Fatalf("decode items: %v", err)
	}
	return d
}

func insertID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("insert: %v\n%s", err, sql)
	}
	return id.String()
}

// --- tests ---

// TestToolDefinition pins vehicle_find to the catalog: name, params, all four
// class R hints, description lengths, and string ids in the outputSchema.
func TestToolDefinition(t *testing.T) {
	tool := findTool()
	if tool.Name != "vehicle_find" {
		t.Fatalf("name = %q", tool.Name)
	}
	if n := len(tool.Description); n > 200 {
		t.Errorf("description is %d chars, max 200", n)
	}
	if len(tool.InputSchema.Required) != 0 {
		t.Errorf("required = %v, want none", tool.InputSchema.Required)
	}
	want := []string{"id", "query", "limit", "cursor"}
	if len(tool.InputSchema.Properties) != len(want) {
		t.Errorf("params = %v, want %v", tool.InputSchema.Properties, want)
	}
	for _, p := range want {
		prop, ok := tool.InputSchema.Properties[p].(map[string]any)
		if !ok {
			t.Errorf("param %q missing", p)
			continue
		}
		if d, _ := prop["description"].(string); d == "" || len(d) > 80 {
			t.Errorf("param %q description %q: want 1-80 chars", p, d)
		}
	}
	a := tool.Annotations
	if a.Title != "Find vehicles" || a.ReadOnlyHint == nil || !*a.ReadOnlyHint ||
		a.DestructiveHint == nil || *a.DestructiveHint || a.IdempotentHint == nil || !*a.IdempotentHint ||
		a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations = %+v, want class R with all four hints set", a)
	}
	b, _ := json.Marshal(tool)
	if strings.Contains(string(b), `"array","items":{"type":"integer"`) {
		t.Errorf("outputSchema advertises a byte-array id (uuid.UUID leaked into the payload type)")
	}
	outputSchema(t) // resolves
}

func TestVehicleFind(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, otherHH := testutil.SeedHousehold(t, pool)
	_, _, find := NewVehicleFindTool(pool)

	// Empty family.
	env := call(t, ctx, find, map[string]any{})
	if got := env.vehicles(t); len(got) != 0 || env.Summary != "No vehicles yet." || len(env.Next) == 0 {
		t.Fatalf("empty: %+v", env)
	}

	odyssey := insertID(t, pool, `INSERT INTO vehicles (household_id, year, make, model, vin, license_plate, color,
		purchase_price, purchase_date, current_value, insurance_cost, loan_amount)
		VALUES ($1, 2022, 'Honda', 'Odyssey', '5FNRL6H7XNB000001', 'ABC123', 'Blue', 38000, '2022-03-15', 31000, 1200, 30000)
		RETURNING id`, hh.HouseholdID)
	f150 := insertID(t, pool, `INSERT INTO vehicles (household_id, year, make, model, notes)
		VALUES ($1, 2019, 'Ford', 'F-150', 'Tow hitch 100%_ rated') RETURNING id`, hh.HouseholdID)
	theirs := insertID(t, pool, `INSERT INTO vehicles (household_id, make, model) VALUES ($1, 'Tesla', 'Model 3') RETURNING id`, otherHH.HouseholdID)

	// Linked records: a typed loan, two typed bills, pending and done upkeep,
	// and another family's bill on their own vehicle (must not count).
	insertID(t, pool, `INSERT INTO loans (household_id, name, lender, original_amount, remaining_balance, monthly_payment, vehicle_id)
		VALUES ($1, 'Odyssey loan', 'Honda Finance', 30000, 21000, 550, $2) RETURNING id`, hh.HouseholdID, odyssey)
	insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day, vehicle_id)
		VALUES ($1, 'Car insurance', 100, 5, $2) RETURNING id`, hh.HouseholdID, odyssey)
	insertID(t, pool, `INSERT INTO bills (household_id, name, amount, frequency, due_date, vehicle_id)
		VALUES ($1, 'Registration', 85, 'annual', '2027-03-01', $2) RETURNING id`, hh.HouseholdID, odyssey)
	insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, vehicle_id)
		VALUES ($1, 'Oil change', '2026-12-04', $2) RETURNING id`, hh.HouseholdID, odyssey)
	insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, status, vehicle_id)
		VALUES ($1, 'Old tires', 'done', $2) RETURNING id`, hh.HouseholdID, odyssey)
	insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day, vehicle_id)
		VALUES ($1, 'Not ours', 1, 1, $2) RETURNING id`, otherHH.HouseholdID, theirs)

	t.Run("list", func(t *testing.T) {
		env := call(t, ctx, find, map[string]any{})
		got := env.vehicles(t)
		if len(got) != 2 || *env.Total != 2 || env.Truncated {
			t.Fatalf("list: %s %+v", env.Summary, got)
		}
		// Sorted by make: Ford before Honda. No related on lists.
		if got[0].ID != f150 || got[1].ID != odyssey || got[1].Related != nil {
			t.Fatalf("order/related: %+v", got)
		}
		o := got[1]
		if o.Name != "2022 Honda Odyssey" || o.LoanCount != 1 || o.BillCount != 2 || o.OpenUpkeepCount != 1 ||
			o.PurchaseDate == nil || *o.PurchaseDate != "2022-03-15" || o.CurrentValue == nil || *o.CurrentValue != 31000 {
			t.Fatalf("odyssey: %+v", o)
		}
		if strings.Contains(string(env.Items), "insurance_cost") || strings.Contains(string(env.Items), "loan_amount") {
			t.Errorf("legacy finance columns leaked: %s", env.Items)
		}
		if env.Summary != "Found 2 vehicles." || !strings.Contains(strings.Join(env.Next, " "), "vehicle_find id=") {
			t.Errorf("summary/next: %q %v", env.Summary, env.Next)
		}
	})

	t.Run("id gives related", func(t *testing.T) {
		env := call(t, ctx, find, map[string]any{"id": odyssey})
		got := env.vehicles(t)
		if len(got) != 1 || got[0].Related == nil {
			t.Fatalf("id: %+v", got)
		}
		r := got[0].Related
		if len(r.Loans) != 1 || r.Loans[0].Name != "Odyssey loan" || r.Loans[0].RemainingBalance != 21000 {
			t.Errorf("loans: %+v", r.Loans)
		}
		if len(r.Bills) != 2 || r.Bills[0].Name != "Car insurance" || r.Bills[1].Name != "Registration" ||
			r.Bills[1].DueDate == nil || *r.Bills[1].DueDate != "2027-03-01" || r.Bills[1].Frequency != "annual" {
			t.Errorf("bills: %+v", r.Bills)
		}
		if len(r.Upkeep) != 1 || r.Upkeep[0].Name != "Oil change" || *r.Upkeep[0].DueWeekday != "Fri" {
			t.Errorf("upkeep: %+v", r.Upkeep)
		}
		if !strings.Contains(env.Summary, "2022 Honda Odyssey") || !strings.Contains(env.Summary, "plate ABC123") ||
			!strings.Contains(env.Summary, "$31,000.00") {
			t.Errorf("summary: %q", env.Summary)
		}
		next := strings.Join(env.Next, " ")
		if !strings.Contains(next, "maintenance_create vehicle_id="+odyssey) || !strings.Contains(next, "loan_find vehicle_id="+odyssey) {
			t.Errorf("next: %v", env.Next)
		}
		// vehicle_id alias works and gives the same record.
		if alias := call(t, ctx, find, map[string]any{"vehicle_id": odyssey}).vehicles(t); len(alias) != 1 || alias[0].ID != odyssey {
			t.Errorf("vehicle_id alias: %+v", alias)
		}
		// A vehicle without links gets empty arrays, not null.
		if f := call(t, ctx, find, map[string]any{"id": f150}).vehicles(t); f[0].Related == nil ||
			f[0].Related.Loans == nil || f[0].Related.Bills == nil || f[0].Related.Upkeep == nil {
			t.Errorf("f150 related: %+v", f[0].Related)
		}
	})

	t.Run("query", func(t *testing.T) {
		tests := []struct {
			query string
			want  []string
		}{
			{"honda", []string{odyssey}},
			{"2019 ford", []string{f150}},
			{"abc1", []string{odyssey}},
			{"5FNRL6", []string{odyssey}},
			{"blue", []string{odyssey}},
			{"100%_", []string{f150}},
			{"%", []string{f150}}, // wildcards are literal: only the note with a % matches
			{"%%", nil},
			{"tesla", nil},
		}
		for _, tc := range tests {
			got := call(t, ctx, find, map[string]any{"query": tc.query}).vehicles(t)
			var ids []string
			for _, v := range got {
				ids = append(ids, v.ID)
			}
			if fmt.Sprint(ids) != fmt.Sprint(tc.want) {
				t.Errorf("query %q: got %v, want %v", tc.query, ids, tc.want)
			}
		}
		if env := call(t, ctx, find, map[string]any{"query": "tesla"}); env.Summary != "No vehicles match." {
			t.Errorf("no match summary: %q", env.Summary)
		}
	})

	t.Run("paging", func(t *testing.T) {
		env := call(t, ctx, find, map[string]any{"limit": 1})
		if got := env.vehicles(t); len(got) != 1 || got[0].ID != f150 || !env.Truncated || env.NextCursor != respond.EncodeCursor(1) {
			t.Fatalf("page 1: %+v", env)
		}
		env = call(t, ctx, find, map[string]any{"limit": "1", "cursor": env.NextCursor})
		if got := env.vehicles(t); len(got) != 1 || got[0].ID != odyssey || env.Truncated || env.NextCursor != "" {
			t.Fatalf("page 2: %+v", env)
		}
		if env.Summary != "Found 2 vehicles; showing 2-2." {
			t.Errorf("page 2 summary: %q", env.Summary)
		}
	})

	t.Run("errors", func(t *testing.T) {
		tests := []struct {
			name  string
			args  map[string]any
			field string
		}{
			{"bad id", map[string]any{"id": "Honda"}, "id"},
			{"bad alias", map[string]any{"vehicle_id": "x"}, "vehicle_id"},
			{"id not string", map[string]any{"id": 5.0}, "id"},
			{"limit too big", map[string]any{"limit": 101.0}, "limit"},
			{"limit fraction", map[string]any{"limit": 2.5}, "limit"},
			{"bad cursor", map[string]any{"cursor": "abc"}, "cursor"},
			{"query not string", map[string]any{"query": true}, "query"},
		}
		for _, tc := range tests {
			env := call(t, ctx, find, tc.args)
			if env.OK || env.Error == nil || env.Error.Field != tc.field || env.Error.Fix == "" || len(env.Error.Example) == 0 {
				t.Errorf("%s: want isError on %q with fix+example, got %+v", tc.name, tc.field, env)
			}
		}
		// Another family's vehicle and an unknown id are not found.
		for _, id := range []string{theirs, uuid.NewString()} {
			env := call(t, ctx, find, map[string]any{"id": id})
			if env.OK || env.Error == nil || !strings.Contains(env.Error.Fix, "vehicle_find") {
				t.Errorf("id %s: want not found, got %+v", id, env)
			}
		}
		// No claims.
		env := call(t, context.Background(), find, map[string]any{})
		if env.OK {
			t.Errorf("no claims: want isError")
		}
	})
}
