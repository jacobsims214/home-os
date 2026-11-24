package vendors

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

// --- shape helpers (pattern from internal/property tests) ---

func findToolDef() mcp.Tool {
	_, tool, _ := NewContactFindTool(nil)
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
		t.Fatalf("contact_find declares no outputSchema (%v)", err)
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

// wireEnvelope is the JSON contact_find returns.
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
	req.Params.Name = "contact_find"
	req.Params.Arguments = args
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("contact_find returned a Go error (protocol fault): %v", err)
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

func (e wireEnvelope) contacts(t *testing.T) []contactData {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d []contactData
	if err := json.Unmarshal(e.Items, &d); err != nil {
		t.Fatalf("decode items: %v", err)
	}
	return d
}

func ids(cs []contactData) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func wantError(t *testing.T, e wireEnvelope, field string) {
	t.Helper()
	if e.OK || e.Error == nil {
		t.Fatalf("want isError on %q, got success: %s", field, e.Summary)
	}
	if e.Error.Field != field {
		t.Fatalf("error field = %q, want %q (%s)", e.Error.Field, field, e.Error.Message)
	}
	if e.Error.Fix == "" || e.Error.Example == nil {
		t.Errorf("error lacks a fix or example: %+v", e.Error)
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

// TestToolDefinition pins contact_find to the catalog: name, params, all
// four hints (class R), description lengths, no legacy params advertised,
// string ids and no property_id/household_id in the outputSchema.
func TestToolDefinition(t *testing.T) {
	tool := findToolDef()
	if tool.Name != "contact_find" {
		t.Fatalf("name = %q", tool.Name)
	}
	if len(tool.InputSchema.Required) != 0 {
		t.Errorf("required = %v, want none", tool.InputSchema.Required)
	}
	want := []string{"id", "trade", "query", "kind", "limit", "cursor"}
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
	if a.Title != "Find contacts" {
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
	for _, gone := range []string{"property_id", "household_id"} {
		if _, ok := props[gone]; ok {
			t.Errorf("outputSchema still has %s", gone)
		}
	}
	outputSchema(t)
}

// TestContactFind covers listing, id lookup, trade (exact, synonym, partial,
// specialty fallback), query, counts, household scoping, pagination,
// aliases and validation.
func TestContactFind(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, _, find := NewContactFindTool(pool)

	// Empty household.
	env := call(t, ctx, find, map[string]any{})
	if got := env.contacts(t); len(got) != 0 || env.Summary != "No contacts saved yet." || env.Total == nil || *env.Total != 0 {
		t.Fatalf("empty: %+v", env)
	}
	if len(env.Next) == 0 || !strings.HasPrefix(env.Next[0], "Add a contact with contact_create") {
		t.Errorf("empty next = %v", env.Next)
	}

	joe := insertID(t, pool, `INSERT INTO vendors (household_id, name, specialty, phone, email, notes, trades)
		VALUES ($1, 'Joe''s Plumbing', 'Plumbing', '555-0101', 'joe@example.com', 'Fixed the water heater 2025', ARRAY['plumber'])
		RETURNING id`, hh.HouseholdID)
	vet := insertID(t, pool, `INSERT INTO vendors (household_id, name, phone, trades)
		VALUES ($1, 'Paws Animal Clinic', '555-0202', ARRAY['vet']) RETURNING id`, hh.HouseholdID)
	sparky := insertID(t, pool, `INSERT INTO vendors (household_id, name, website, trades)
		VALUES ($1, 'Sparky Co', 'sparky.example', ARRAY['electrician', 'handyman']) RETURNING id`, hh.HouseholdID)
	// specialty 043 could not map: trades stays empty.
	sitter := insertID(t, pool, `INSERT INTO vendors (household_id, name, specialty)
		VALUES ($1, 'Anna 100%', 'Babysitter (weekends)') RETURNING id`, hh.HouseholdID)

	insertID(t, pool, `INSERT INTO bills (household_id, name, amount, vendor_id) VALUES ($1, 'Water heater plan', 20, $2) RETURNING id`, hh.HouseholdID, joe)
	insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, vendor_id) VALUES ($1, 'Flush heater', '2026-12-01', $2) RETURNING id`, hh.HouseholdID, joe)
	insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, vendor_id, status) VALUES ($1, 'Old job', '2026-01-01', $2, 'done') RETURNING id`, hh.HouseholdID, joe)
	insertID(t, pool, `INSERT INTO pets (household_id, name, species, vet_vendor_id) VALUES ($1, 'Rex', 'dog', $2) RETURNING id`, hh.HouseholdID, vet)

	// Another family's plumber never shows.
	_, other := testutil.SeedHousehold(t, pool)
	foreign := insertID(t, pool, `INSERT INTO vendors (household_id, name, trades) VALUES ($1, 'Away Plumbing', ARRAY['plumber']) RETURNING id`, other.HouseholdID)

	// List: sorted by name, trades, counts; never property_id/household_id.
	env = call(t, ctx, find, map[string]any{})
	got := env.contacts(t)
	if fmt.Sprint(ids(got)) != fmt.Sprint([]string{sitter, joe, vet, sparky}) || *env.Total != 4 || env.Truncated {
		t.Fatalf("list order/ids: %+v", env)
	}
	if !strings.HasPrefix(env.Summary, "Found 4 contacts: Anna 100% (Babysitter (weekends)), Joe's Plumbing (plumber), ") ||
		!strings.Contains(env.Summary, "Sparky Co (electrician, handyman)") {
		t.Errorf("summary = %q", env.Summary)
	}
	if strings.Contains(string(env.Items), "property_id") || strings.Contains(string(env.Items), "household_id") {
		t.Errorf("items leak property_id/household_id: %s", env.Items)
	}
	j := got[1]
	if *j.Phone != "555-0101" || *j.Email != "joe@example.com" || *j.Specialty != "Plumbing" || fmt.Sprint(j.Trades) != "[plumber]" {
		t.Errorf("joe fields: %+v", j)
	}
	if j.Counts != (contactCounts{Bills: 1, OpenMaintenance: 1}) {
		t.Errorf("joe counts = %+v", j.Counts)
	}
	if got[2].Counts != (contactCounts{Pets: 1}) {
		t.Errorf("vet counts = %+v", got[2].Counts)
	}
	if got[0].Trades == nil || len(got[0].Trades) != 0 {
		t.Errorf("sitter trades = %#v, want []", got[0].Trades)
	}

	// trade: "who's the plumber?" and friends.
	tradeCases := []struct {
		trade string
		want  []string
	}{
		{"plumber", []string{joe}},
		{"Plumber", []string{joe}},
		{"plumbing", []string{joe}},     // synonym
		{"plumb", []string{joe}},        // partial trade
		{"veterinarian", []string{vet}}, // synonym
		{"handyman", []string{sparky}},  // second trade
		{"babysitter", []string{sitter}},
		{"dentist", nil},
		{"%", nil}, // wildcards are literal
	}
	for _, tc := range tradeCases {
		t.Run("trade="+tc.trade, func(t *testing.T) {
			env := call(t, ctx, find, map[string]any{"trade": tc.trade})
			if got := ids(env.contacts(t)); fmt.Sprint(got) != fmt.Sprint(append([]string{}, tc.want...)) {
				t.Errorf("got %v, want %v (%s)", got, tc.want, env.Summary)
			}
		})
	}
	env = call(t, ctx, find, map[string]any{"trade": "plumber"})
	if env.Summary != "Found Joe's Plumbing (plumber): phone 555-0101, email joe@example.com." {
		t.Errorf("plumber summary = %q", env.Summary)
	}
	if len(env.Next) == 0 || !strings.Contains(env.Next[0], "maintenance_create contact_id="+joe) {
		t.Errorf("plumber next = %v", env.Next)
	}
	env = call(t, ctx, find, map[string]any{"trade": "Pest Control"})
	if env.Summary != "No Pest Control saved." || len(env.Next) == 0 ||
		env.Next[0] != "Add one with contact_create name=… trades=[pest_control] phone=…." {
		t.Errorf("empty trade: %q %v", env.Summary, env.Next)
	}

	// query matches name, notes, email, phone, trade; wildcards literal.
	for q, want := range map[string][]string{
		"joe": {joe}, "WATER HEATER": {joe}, "example.com": {joe}, "0202": {vet},
		"electrician": {sparky}, "100%": {sitter}, "zzz": nil,
	} {
		if got := ids(call(t, ctx, find, map[string]any{"query": q}).contacts(t)); fmt.Sprint(got) != fmt.Sprint(append([]string{}, want...)) {
			t.Errorf("query %q: got %v, want %v", q, got, want)
		}
	}
	// trade and query combine.
	if got := ids(call(t, ctx, find, map[string]any{"trade": "plumber", "query": "sparky"}).contacts(t)); len(got) != 0 {
		t.Errorf("trade+query: %v", got)
	}

	// id: exactly one contact; legacy aliases.
	for _, args := range []map[string]any{{"id": vet}, {"vendor_id": vet}, {"contact_id": vet}} {
		if got := ids(call(t, ctx, find, args).contacts(t)); len(got) != 1 || got[0] != vet {
			t.Errorf("%v: got %v", args, got)
		}
	}
	if got := ids(call(t, ctx, find, map[string]any{"specialty": "vet"}).contacts(t)); len(got) != 1 || got[0] != vet {
		t.Errorf("specialty alias: %v", got)
	}

	// Another family's contact: not found, never leaked.
	env = call(t, ctx, find, map[string]any{"id": foreign})
	if env.OK || env.Error == nil || !strings.Contains(env.Error.Fix, "contact_find") {
		t.Errorf("foreign id: %+v", env)
	}
	if got := call(t, ctx, find, map[string]any{"query": "away"}).contacts(t); len(got) != 0 {
		t.Errorf("foreign query leaked: %+v", got)
	}

	// Pagination: limit + next_cursor passed back as cursor.
	env = call(t, ctx, find, map[string]any{"limit": 3})
	if got := ids(env.contacts(t)); len(got) != 3 || !env.Truncated || *env.Total != 4 || env.NextCursor != respond.EncodeCursor(3) {
		t.Fatalf("page 1: %+v", env)
	}
	env = call(t, ctx, find, map[string]any{"limit": "3", "cursor": env.NextCursor})
	if got := ids(env.contacts(t)); len(got) != 1 || got[0] != sparky || env.Truncated || env.NextCursor != "" {
		t.Fatalf("page 2: %+v", env)
	}
	env = call(t, ctx, find, map[string]any{"cursor": "9"})
	if got := env.contacts(t); len(got) != 0 || !strings.Contains(env.Summary, "past the end") {
		t.Fatalf("past end: %+v", env)
	}

	// Validation errors are isError results on the right field.
	wantError(t, call(t, ctx, find, map[string]any{"id": "Joe"}), "id")
	wantError(t, call(t, ctx, find, map[string]any{"limit": 0}), "limit")
	wantError(t, call(t, ctx, find, map[string]any{"limit": 101}), "limit")
	wantError(t, call(t, ctx, find, map[string]any{"limit": 2.5}), "limit")
	wantError(t, call(t, ctx, find, map[string]any{"cursor": "abc"}), "cursor")
	wantError(t, call(t, ctx, find, map[string]any{"trade": 5}), "trade")
	wantError(t, call(t, ctx, find, map[string]any{"query": true}), "query")

	// No claims.
	if env := call(t, context.Background(), find, map[string]any{}); env.OK {
		t.Errorf("no claims: %+v", env)
	}
}
