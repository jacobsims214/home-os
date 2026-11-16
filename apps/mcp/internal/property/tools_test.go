package property

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

func findToolDef() mcp.Tool {
	_, tool, _ := NewPropertyFindTool(nil)
	return tool
}

func outputSchema(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(findToolDef())
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || len(raw.OutputSchema) == 0 {
		t.Fatalf("property_find declares no outputSchema (%v)", err)
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

// wireEnvelope is the JSON property_find returns.
type wireEnvelope struct {
	OK         bool            `json:"ok"`
	Summary    string          `json:"summary"`
	Items      json.RawMessage `json:"items"`
	Returned   int             `json:"returned"`
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
	req.Params.Name = "property_find"
	req.Params.Arguments = args
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("property_find returned a Go error (protocol fault): %v", err)
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
		t.Fatalf("marshal structuredContent: %v", err)
	}
	if string(sc) != text.Text {
		t.Fatalf("text differs from structuredContent:\n%s\n%s", text.Text, sc)
	}
	var instance map[string]any
	if err := json.Unmarshal(sc, &instance); err != nil {
		t.Fatalf("structuredContent is not an object: %v", err)
	}
	if err := outputSchema(t).Validate(instance); err != nil {
		t.Fatalf("result does not match its outputSchema: %v\n%s", err, text.Text)
	}
	var env wireEnvelope
	if err := json.Unmarshal(sc, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.OK == res.IsError {
		t.Fatalf("ok=%v but isError=%v", env.OK, res.IsError)
	}
	return env
}

func (e wireEnvelope) properties(t *testing.T) []propertyData {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d []propertyData
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
	if e.Error.Fix == "" {
		t.Errorf("error lacks a fix: %+v", e.Error)
	}
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

// TestToolDefinition pins property_find to the catalog: name, params, all
// four hints (class R), description lengths, no legacy params advertised,
// and string ids in the outputSchema.
func TestToolDefinition(t *testing.T) {
	tool := findToolDef()
	if tool.Name != "property_find" {
		t.Fatalf("name = %q", tool.Name)
	}
	if len(tool.InputSchema.Required) != 0 {
		t.Errorf("required = %v, want none", tool.InputSchema.Required)
	}
	want := []string{"id", "query", "limit", "cursor"}
	if len(tool.InputSchema.Properties) != len(want) {
		t.Errorf("got %d params, want %v", len(tool.InputSchema.Properties), want)
	}
	for _, p := range want {
		if _, ok := tool.InputSchema.Properties[p]; !ok {
			t.Errorf("missing param %q", p)
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
	if a.Title != "Find properties" {
		t.Errorf("title = %q", a.Title)
	}
	if a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
		t.Fatalf("annotations not all explicit: %+v", a)
	}
	if !*a.ReadOnlyHint || *a.DestructiveHint || !*a.IdempotentHint || *a.OpenWorldHint {
		t.Errorf("hints = %+v, want class R (true,false,true,false)", a)
	}
	b, _ := json.Marshal(tool)
	var raw struct {
		OutputSchema map[string]any `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || raw.OutputSchema == nil {
		t.Fatalf("no outputSchema: %v", err)
	}
	item := raw.OutputSchema["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)
	props := item["properties"].(map[string]any)
	if s := fmt.Sprint(props["id"].(map[string]any)["type"]); !strings.Contains(s, "string") {
		t.Errorf("id schema type = %s, want string", s)
	}
	room := props["rooms"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if s := fmt.Sprint(room["id"].(map[string]any)["type"]); !strings.Contains(s, "string") {
		t.Errorf("room id schema type = %s, want string", s)
	}
	outputSchema(t)
}

// TestPropertyFind covers listing with rooms and counts, id lookup (the
// search_all room hit path), query over room names, household scoping,
// pagination, aliases and validation.
func TestPropertyFind(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, _, find := NewPropertyFindTool(pool)

	// Empty household.
	env := call(t, ctx, find, map[string]any{})
	if got := env.properties(t); len(got) != 0 || env.Summary != "No properties yet." || env.Total == nil || *env.Total != 0 {
		t.Fatalf("empty: %+v", env)
	}

	home := insertID(t, pool, `INSERT INTO properties (household_id, name, address, property_type, purchase_date, purchase_price, current_value)
		VALUES ($1, 'Home', '12 Elm St', 'house', '2019-06-01', 380000, 450000) RETURNING id`, hh.HouseholdID)
	lake := insertID(t, pool, `INSERT INTO properties (household_id, name, notes) VALUES ($1, 'Lake House', 'dock needs stain') RETURNING id`, hh.HouseholdID)
	kitchen := insertID(t, pool, `INSERT INTO rooms (property_id, name, floor) VALUES ($1, 'Kitchen', 1) RETURNING id`, home)
	insertID(t, pool, `INSERT INTO rooms (property_id, name, floor) VALUES ($1, 'Zephyr Loft', 2) RETURNING id`, home)
	attic := insertID(t, pool, `INSERT INTO rooms (property_id, name) VALUES ($1, 'Attic') RETURNING id`, home)
	insertID(t, pool, `INSERT INTO assets (household_id, name, category, room_id) VALUES ($1, 'Fridge', 'appliance', $2) RETURNING id`, hh.HouseholdID, kitchen)
	insertID(t, pool, `INSERT INTO assets (household_id, name, category, property_id) VALUES ($1, 'Mower', 'tool', $2) RETURNING id`, hh.HouseholdID, home)
	insertID(t, pool, `INSERT INTO loans (household_id, name, original_amount, remaining_balance, property_id) VALUES ($1, 'Mortgage', 300000, 250000, $2) RETURNING id`, hh.HouseholdID, home)
	insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, property_id) VALUES ($1, 'Clean gutters', '2026-12-01', $2) RETURNING id`, hh.HouseholdID, home)
	insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, property_id, status) VALUES ($1, 'Old job', '2026-01-01', $2, 'done') RETURNING id`, hh.HouseholdID, home)

	// Another family's property never shows.
	otherCtx, other := testutil.SeedHousehold(t, pool)
	foreign := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Home Away') RETURNING id`, other.HouseholdID)
	_ = otherCtx

	// List: sorted by name, rooms and counts on every property.
	env = call(t, ctx, find, map[string]any{})
	got := env.properties(t)
	if len(got) != 2 || got[0].Name != "Home" || got[1].Name != "Lake House" || *env.Total != 2 || env.Truncated {
		t.Fatalf("list: %+v", env)
	}
	if env.Summary != "Found 2 properties: Home (3 rooms), Lake House (0 rooms)." {
		t.Errorf("summary = %q", env.Summary)
	}
	h := got[0]
	if h.ID != home || *h.Address != "12 Elm St" || *h.PurchaseDate != "2019-06-01" || *h.CurrentValue != 450000 || *h.PurchasePrice != 380000 {
		t.Errorf("home fields: %+v", h)
	}
	if h.Counts.Rooms != 3 || h.Counts.Assets != 2 || h.Counts.OpenMaintenance != 1 ||
		h.Counts.Loans == nil || *h.Counts.Loans != 1 || h.Counts.Bills == nil || *h.Counts.Bills != 0 {
		t.Errorf("home counts = %+v", h.Counts)
	}
	if len(h.Rooms) != 3 || h.Rooms[0].Name != "Kitchen" || h.Rooms[0].AssetCount != 1 || h.Rooms[0].ID != kitchen ||
		h.Rooms[1].Name != "Zephyr Loft" || h.Rooms[2].ID != attic || h.Rooms[2].Floor != nil {
		t.Errorf("home rooms (floor, then name, no floor last) = %+v", h.Rooms)
	}
	if got[1].Rooms == nil || len(got[1].Rooms) != 0 {
		t.Errorf("lake rooms = %#v, want []", got[1].Rooms)
	}
	if len(env.Next) == 0 || !strings.Contains(env.Next[0], "asset_find property_id="+home) ||
		!strings.Contains(strings.Join(env.Next, " "), "loan_find property_id="+home) {
		t.Errorf("next = %v", env.Next)
	}

	// id: exactly one property with rooms (search_all opens room hits this way).
	env = call(t, ctx, find, map[string]any{"id": home})
	if got := env.properties(t); len(got) != 1 || got[0].ID != home || len(got[0].Rooms) != 3 {
		t.Fatalf("id: %+v", env)
	}
	if !strings.HasPrefix(env.Summary, "Found property Home: at 12 Elm St, 3 rooms, 2 assets, 1 loan, value $450,000.00") {
		t.Errorf("id summary = %q", env.Summary)
	}
	// Legacy get_property alias property_id.
	if got := call(t, ctx, find, map[string]any{"property_id": lake}).properties(t); len(got) != 1 || got[0].ID != lake {
		t.Errorf("property_id alias: %+v", got)
	}

	// query matches room names, notes and address, case-insensitively.
	for q, want := range map[string]string{"zephyr": home, "DOCK": lake, "elm": home, "100%": ""} {
		got := call(t, ctx, find, map[string]any{"query": q}).properties(t)
		if want == "" {
			if len(got) != 0 {
				t.Errorf("query %q: want none (wildcards are literal), got %d", q, len(got))
			}
			continue
		}
		if len(got) != 1 || got[0].ID != want {
			t.Errorf("query %q: got %+v", q, got)
		}
	}
	if env := call(t, ctx, find, map[string]any{"query": "nowhere"}); env.Summary != "No properties match." {
		t.Errorf("no match summary = %q", env.Summary)
	}

	// Another family's property: not found, never leaked.
	env = call(t, ctx, find, map[string]any{"id": foreign})
	if env.OK || env.Error == nil || !strings.Contains(env.Error.Fix, "property_find") {
		t.Errorf("foreign id: %+v", env)
	}
	if got := call(t, ctx, find, map[string]any{"query": "away"}).properties(t); len(got) != 0 {
		t.Errorf("foreign query leaked: %+v", got)
	}

	// Pagination: limit + next_cursor passed back as cursor.
	env = call(t, ctx, find, map[string]any{"limit": 1})
	if got := env.properties(t); len(got) != 1 || got[0].ID != home || !env.Truncated || *env.Total != 2 ||
		env.NextCursor != respond.EncodeCursor(1) {
		t.Fatalf("page 1: %+v", env)
	}
	env = call(t, ctx, find, map[string]any{"limit": "1", "cursor": env.NextCursor})
	if got := env.properties(t); len(got) != 1 || got[0].ID != lake || env.Truncated || env.NextCursor != "" {
		t.Fatalf("page 2: %+v", env)
	}
	env = call(t, ctx, find, map[string]any{"cursor": "9"})
	if got := env.properties(t); len(got) != 0 || !strings.Contains(env.Summary, "past the end") {
		t.Fatalf("past end: %+v", env)
	}

	// Validation errors are isError results on the right field.
	wantError(t, call(t, ctx, find, map[string]any{"id": "Home"}), "id")
	wantError(t, call(t, ctx, find, map[string]any{"limit": 0}), "limit")
	wantError(t, call(t, ctx, find, map[string]any{"limit": 2.5}), "limit")
	wantError(t, call(t, ctx, find, map[string]any{"cursor": "abc"}), "cursor")
	wantError(t, call(t, ctx, find, map[string]any{"query": 5}), "query")

	// No claims.
	if env := call(t, context.Background(), find, map[string]any{}); env.OK {
		t.Errorf("no claims: %+v", env)
	}
}

func TestMoney(t *testing.T) {
	for in, want := range map[float64]string{0: "$0.00", 450000: "$450,000.00", 1234567.5: "$1,234,567.50", -12: "-$12.00"} {
		if got := money(in); got != want {
			t.Errorf("money(%v) = %q, want %q", in, got, want)
		}
	}
}
