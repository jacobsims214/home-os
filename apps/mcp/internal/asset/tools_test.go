package asset

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
	"home-os/mcp/internal/search"
	"home-os/mcp/internal/testutil"
)

// --- shape helpers (pattern from internal/respond and internal/loan tests) ---

func toolDefs() []mcp.Tool {
	_, find, _ := NewAssetFindTool(nil)
	_, create, _ := NewAssetCreateTool(nil, nil)
	_, upd, _ := NewAssetUpdateTool(nil, nil)
	_, del, _ := NewAssetDeleteTool(nil)
	return []mcp.Tool{find, create, upd, del}
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
		t.Fatalf("no asset tool named %q", name)
	}
	return rs
}

// wireEnvelope is the JSON every asset tool returns.
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

func (e wireEnvelope) asset(t *testing.T) assetData {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d assetData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return d
}

func (e wireEnvelope) assets(t *testing.T) []assetData {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d []assetData
	if err := json.Unmarshal(e.Items, &d); err != nil {
		t.Fatalf("decode items: %v", err)
	}
	return d
}

// wantError checks an actionable error: isError on field, with a fix and
// (for argument errors) a valid example.
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
	if field != "" && len(e.Error.Example) == 0 {
		t.Errorf("error lacks an example: %+v", e.Error)
	}
}

// wantNotFound checks a respond.NotFound result that points at asset_find.
func wantNotFound(t *testing.T, e wireEnvelope) {
	t.Helper()
	if e.OK || e.Error == nil || !strings.Contains(e.Error.Message, "No asset with id") || !strings.Contains(e.Error.Fix, "asset_find") {
		t.Fatalf("want asset not found, got %s / %+v", e.Summary, e.Error)
	}
}

func deref2[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// --- fixtures ---

type fakeIndexer struct {
	mu   sync.Mutex
	docs []search.Document
}

func (f *fakeIndexer) IndexDocument(_ context.Context, doc search.Document) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.docs = append(f.docs, doc)
	return nil
}

type fixture struct {
	pool                   *pgxpool.Pool
	ctx                    context.Context
	hh                     testutil.Household
	idx                    *fakeIndexer
	find, create, upd, del toolHandler
}

func setup(t *testing.T) fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	if _, err := pool.Exec(context.Background(), `UPDATE households SET timezone = 'America/New_York' WHERE id = $1`, hh.HouseholdID); err != nil {
		t.Fatalf("set timezone: %v", err)
	}
	f := fixture{pool: pool, ctx: ctx, hh: hh, idx: &fakeIndexer{}}
	_, _, f.find = NewAssetFindTool(pool)
	_, _, f.create = NewAssetCreateTool(pool, f.idx)
	_, _, f.upd = NewAssetUpdateTool(pool, f.idx)
	_, _, f.del = NewAssetDeleteTool(pool)
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

func (f fixture) room(t *testing.T, propertyID, name string) string {
	return insertID(t, f.pool, `INSERT INTO rooms (property_id, name) VALUES ($1, $2) RETURNING id`, propertyID, name)
}

// otherHousehold seeds a second family and returns its id.
func (f fixture) otherHousehold(t *testing.T) uuid.UUID {
	_, other := testutil.SeedHousehold(t, f.pool)
	return other.HouseholdID
}

// --- tests ---

// TestToolDefinitions pins every asset tool to the catalog: name, required
// and advertised params, all four hints (class R/C/U/X), description
// lengths, no legacy params advertised, and string ids in the outputSchema.
func TestToolDefinitions(t *testing.T) {
	type hints struct{ readOnly, destructive, idempotent bool }
	createParams := []string{"name", "category", "manufacturer", "model", "serial_number", "room_id", "property_id",
		"purchase_date", "purchase_price", "current_value", "warranty_expiry"}
	want := map[string]struct {
		title    string
		required []string
		params   []string
		class    hints
	}{
		"asset_find":   {"Find assets", nil, []string{"id", "query", "category", "property_id", "room_id", "limit", "cursor"}, hints{true, false, true}},
		"asset_create": {"Create asset", []string{"name", "category"}, createParams, hints{false, false, false}},
		"asset_update": {"Update asset", []string{"asset_id"}, append([]string{"asset_id"}, createParams...), hints{false, false, true}},
		"asset_delete": {"Delete asset", []string{"asset_id"}, []string{"asset_id"}, hints{false, true, true}},
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
			if tool.Name != "asset_find" {
				if _, ok := tool.InputSchema.Properties["id"]; ok {
					t.Errorf("advertises legacy param id")
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
			for _, id := range []string{"id", "property_id", "room_id"} {
				if s := fmt.Sprint(props[id].(map[string]any)["type"]); !strings.Contains(s, "string") {
					t.Errorf("%s schema type = %s, want string", id, s)
				}
			}
			outputSchema(t, tool.Name)
		})
	}
}

// TestNotSignedIn: every tool refuses a call without household claims with
// an isError result, not a protocol error.
func TestNotSignedIn(t *testing.T) {
	f := setup(t)
	for name, h := range map[string]toolHandler{"asset_find": f.find, "asset_create": f.create, "asset_update": f.upd, "asset_delete": f.del} {
		e := call(t, context.Background(), h, name, map[string]any{"asset_id": exampleAssetID, "name": "x", "category": "y"})
		if e.OK || e.Error == nil || !strings.Contains(e.Error.Message, "not signed in") {
			t.Errorf("%s: want not-signed-in error, got %s", name, e.Summary)
		}
	}
}

// TestAssetCreate covers optional location (domain model v2), the property
// following the room, the loop guard, indexing and actionable errors.
func TestAssetCreate(t *testing.T) {
	f := setup(t)
	house := f.property(t, f.hh.HouseholdID, "Maple St")
	kitchen := f.room(t, house, "Kitchen")
	cabin := f.property(t, f.hh.HouseholdID, "Lake Cabin")

	t.Run("no room or property", func(t *testing.T) {
		e := call(t, f.ctx, f.create, "asset_create", map[string]any{
			"name": "Wedding ring", "category": "Jewelry", "purchase_price": "$1,250.50",
		})
		d := e.asset(t)
		if d.PropertyID != nil || d.RoomID != nil || d.PropertyName != nil {
			t.Errorf("want no place, got property=%v room=%v", deref2(d.PropertyID), deref2(d.RoomID))
		}
		if deref2(d.Category) != "Jewelry" || deref2(d.PurchasePrice) != 1250.5 {
			t.Errorf("category=%v price=%v", deref2(d.Category), deref2(d.PurchasePrice))
		}
		if !strings.HasPrefix(e.Summary, "Added Wedding ring") || len(e.Next) != 2 || !strings.Contains(e.Next[0], "document_upload about_type=asset about_id="+d.ID) {
			t.Errorf("summary/next = %q %v", e.Summary, e.Next)
		}
	})

	t.Run("room sets property", func(t *testing.T) {
		e := call(t, f.ctx, f.create, "asset_create", map[string]any{
			"name": "Dishwasher", "category": "appliance", "manufacturer": "Bosch", "model": "SHX78",
			"room_id": kitchen, "purchase_date": "2024-03-15", "warranty_expiry": "2099-03-15",
		})
		d := e.asset(t)
		if deref2(d.PropertyID) != house || deref2(d.RoomID) != kitchen || deref2(d.RoomName) != "Kitchen" || deref2(d.PropertyName) != "Maple St" {
			t.Errorf("place = %v/%v %v/%v", deref2(d.PropertyID), deref2(d.PropertyName), deref2(d.RoomID), deref2(d.RoomName))
		}
		if d.WarrantyActive == nil || !*d.WarrantyActive || d.WarrantyDaysLeft == nil || *d.WarrantyDaysLeft <= 0 || deref2(d.WarrantyWeekday) == nil {
			t.Errorf("warranty = %v %v %v", deref2(d.WarrantyActive), deref2(d.WarrantyDaysLeft), deref2(d.WarrantyWeekday))
		}
		if !strings.Contains(e.Summary, "in the Kitchen at Maple St") || !strings.Contains(e.Summary, "warranty until") {
			t.Errorf("summary = %q", e.Summary)
		}
		f.idx.mu.Lock()
		var indexed bool
		for _, doc := range f.idx.docs {
			indexed = indexed || (doc.EntityID == d.ID && doc.PropertyID != nil && *doc.PropertyID == house)
		}
		f.idx.mu.Unlock()
		if !indexed {
			t.Errorf("asset was not indexed with its property")
		}

		// Loop guard: same name and category again returns the same row.
		again := call(t, f.ctx, f.create, "asset_create", map[string]any{"name": "dishwasher", "category": "Appliance"})
		dd := again.asset(t)
		if !dd.Duplicate || dd.ID != d.ID || !strings.Contains(again.Next[0], "asset_update asset_id="+d.ID) {
			t.Errorf("want duplicate of %s, got %+v / %v", d.ID, dd, again.Next)
		}
		var n int
		_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM assets WHERE lower(name) = 'dishwasher'`).Scan(&n)
		if n != 1 {
			t.Errorf("%d dishwasher rows, want 1", n)
		}
	})

	t.Run("property only", func(t *testing.T) {
		d := call(t, f.ctx, f.create, "asset_create", map[string]any{"name": "Kayak", "category": "outdoor", "property_id": cabin}).asset(t)
		if deref2(d.PropertyID) != cabin || d.RoomID != nil {
			t.Errorf("place = %v %v", deref2(d.PropertyID), deref2(d.RoomID))
		}
	})

	other := f.otherHousehold(t)
	otherHouse := f.property(t, other, "Elsewhere")
	otherRoom := f.room(t, otherHouse, "Den")

	for _, tc := range []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"missing category", map[string]any{"name": "TV"}, "category"},
		{"missing name", map[string]any{"category": "electronics"}, "name"},
		{"another family's room", map[string]any{"name": "TV", "category": "electronics", "room_id": otherRoom}, "room_id"},
		{"another family's property", map[string]any{"name": "TV", "category": "electronics", "property_id": otherHouse}, "property_id"},
		{"room and property disagree", map[string]any{"name": "TV", "category": "electronics", "room_id": kitchen, "property_id": cabin}, "property_id"},
		{"room name instead of id", map[string]any{"name": "TV", "category": "electronics", "room_id": "Kitchen"}, "room_id"},
		{"bad date", map[string]any{"name": "TV", "category": "electronics", "warranty_expiry": "next year"}, "warranty_expiry"},
		{"negative price", map[string]any{"name": "TV", "category": "electronics", "purchase_price": -5}, "purchase_price"},
		{"price not a number", map[string]any{"name": "TV", "category": "electronics", "purchase_price": "a lot"}, "purchase_price"},
		{"name too long", map[string]any{"name": strings.Repeat("x", 256), "category": "electronics"}, "name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, call(t, f.ctx, f.create, "asset_create", tc.args), tc.field)
		})
	}
	var n int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM assets WHERE name = 'TV'`).Scan(&n)
	if n != 0 {
		t.Errorf("failed creates wrote %d rows", n)
	}
}

// TestAssetFind covers filters, id lookup, paging, LIKE escaping, family
// scoping and actionable errors.
func TestAssetFind(t *testing.T) {
	f := setup(t)
	house := f.property(t, f.hh.HouseholdID, "Maple St")
	kitchen := f.room(t, house, "Kitchen")
	garage := f.room(t, house, "Garage")
	mk := func(args map[string]any) assetData {
		return call(t, f.ctx, f.create, "asset_create", args).asset(t)
	}
	dw := mk(map[string]any{"name": "Dishwasher", "category": "appliance", "manufacturer": "Bosch", "room_id": kitchen,
		"warranty_expiry": "2020-01-31"})
	fridge := mk(map[string]any{"name": "Fridge", "category": "Appliance", "manufacturer": "LG", "room_id": kitchen})
	mower := mk(map[string]any{"name": "Lawn mower", "category": "outdoor", "room_id": garage, "serial_number": "SN_100%"})
	laptop := mk(map[string]any{"name": "Laptop", "category": "electronics"})

	// Another family's asset never shows up.
	other := f.otherHousehold(t)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO assets (household_id, name, category) VALUES ($1, 'Dishwasher', 'appliance')`, other); err != nil {
		t.Fatal(err)
	}

	ids := func(items []assetData) string {
		var out []string
		for _, a := range items {
			out = append(out, a.Name)
		}
		return strings.Join(out, ",")
	}

	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"all, sorted by name", map[string]any{}, "Dishwasher,Fridge,Laptop,Lawn mower"},
		{"category any case", map[string]any{"category": "APPLIANCE"}, "Dishwasher,Fridge"},
		{"query by maker", map[string]any{"query": "bosch"}, "Dishwasher"},
		{"query by room name", map[string]any{"query": "garage"}, "Lawn mower"},
		{"query by property name", map[string]any{"query": "maple"}, "Dishwasher,Fridge,Lawn mower"},
		{"query wildcard is literal", map[string]any{"query": "%"}, "Lawn mower"},
		{"query underscore is literal", map[string]any{"query": "N_1"}, "Lawn mower"},
		{"property filter", map[string]any{"property_id": house}, "Dishwasher,Fridge,Lawn mower"},
		{"room filter", map[string]any{"room_id": kitchen}, "Dishwasher,Fridge"},
		{"id", map[string]any{"id": laptop.ID}, "Laptop"},
		{"old asset_id alias", map[string]any{"asset_id": mower.ID}, "Lawn mower"},
		{"no match", map[string]any{"query": "piano"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := call(t, f.ctx, f.find, "asset_find", tc.args)
			if got := ids(e.assets(t)); got != tc.want {
				t.Errorf("got %q, want %q (%s)", got, tc.want, e.Summary)
			}
		})
	}

	t.Run("one result describes it", func(t *testing.T) {
		e := call(t, f.ctx, f.find, "asset_find", map[string]any{"id": dw.ID})
		got := e.assets(t)[0]
		if got.WarrantyActive == nil || *got.WarrantyActive || *got.WarrantyDaysLeft >= 0 || deref2(got.WarrantyWeekday) != "Fri" {
			t.Errorf("expired warranty = %v %v %v", deref2(got.WarrantyActive), deref2(got.WarrantyDaysLeft), deref2(got.WarrantyWeekday))
		}
		if !strings.Contains(e.Summary, "warranty expired Fri Jan 31, 2020") || !strings.Contains(strings.Join(e.Next, " "), "maintenance_create asset_id="+dw.ID) {
			t.Errorf("summary/next = %q %v", e.Summary, e.Next)
		}
	})

	t.Run("paging", func(t *testing.T) {
		e := call(t, f.ctx, f.find, "asset_find", map[string]any{"limit": 3})
		if ids(e.assets(t)) != "Dishwasher,Fridge,Laptop" || !e.Truncated || *e.Total != 4 || e.NextCursor != respond.EncodeCursor(3) {
			t.Fatalf("page 1 = %s truncated=%v total=%v narrow=%q", ids(e.assets(t)), e.Truncated, deref2(e.Total), e.Narrow)
		}
		e2 := call(t, f.ctx, f.find, "asset_find", map[string]any{"limit": 3, "cursor": e.NextCursor})
		if ids(e2.assets(t)) != "Lawn mower" || e2.Truncated || e2.NextCursor != "" {
			t.Errorf("page 2 = %s truncated=%v", ids(e2.assets(t)), e2.Truncated)
		}
	})

	t.Run("empty family", func(t *testing.T) {
		pool := testutil.NewPool(t)
		ctx, _ := testutil.SeedHousehold(t, pool)
		_, _, find := NewAssetFindTool(pool)
		e := call(t, ctx, find, "asset_find", map[string]any{})
		if len(e.assets(t)) != 0 || !strings.Contains(e.Next[0], "asset_create") {
			t.Errorf("empty = %s %v", e.Summary, e.Next)
		}
	})

	t.Run("unknown id", func(t *testing.T) {
		wantNotFound(t, call(t, f.ctx, f.find, "asset_find", map[string]any{"id": uuid.NewString()}))
	})
	for _, tc := range []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"bad cursor", map[string]any{"cursor": "page2"}, "cursor"},
		{"bad id", map[string]any{"id": "Fridge"}, "id"},
		{"limit too big", map[string]any{"limit": 500}, "limit"},
		{"room name instead of id", map[string]any{"room_id": "Kitchen"}, "room_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, call(t, f.ctx, f.find, "asset_find", tc.args), tc.field)
		})
	}
	_ = fridge
}

// TestAssetUpdate covers partial updates, moves between rooms and
// properties (property follows the room), clearing, and errors.
func TestAssetUpdate(t *testing.T) {
	f := setup(t)
	house := f.property(t, f.hh.HouseholdID, "Maple St")
	kitchen := f.room(t, house, "Kitchen")
	cabin := f.property(t, f.hh.HouseholdID, "Lake Cabin")
	porch := f.room(t, cabin, "Porch")

	a := call(t, f.ctx, f.create, "asset_create", map[string]any{
		"name": "Grill", "category": "outdoor", "manufacturer": "Weber", "purchase_price": 500,
	}).asset(t)
	upd := func(args map[string]any) wireEnvelope {
		args["asset_id"] = a.ID
		return call(t, f.ctx, f.upd, "asset_update", args)
	}

	t.Run("only given fields change", func(t *testing.T) {
		e := upd(map[string]any{"current_value": 300})
		d := e.asset(t)
		if deref2(d.CurrentValue) != 300.0 || deref2(d.PurchasePrice) != 500.0 || deref2(d.Manufacturer) != "Weber" || d.Name != "Grill" {
			t.Errorf("got %+v", d)
		}
		if !strings.Contains(e.Summary, "(current_value)") {
			t.Errorf("summary = %q", e.Summary)
		}
	})

	t.Run("room sets property", func(t *testing.T) {
		d := upd(map[string]any{"room_id": kitchen}).asset(t)
		if deref2(d.RoomID) != kitchen || deref2(d.PropertyID) != house {
			t.Errorf("place = %v %v", deref2(d.RoomID), deref2(d.PropertyID))
		}
	})

	t.Run("new property leaves the room", func(t *testing.T) {
		e := upd(map[string]any{"property_id": cabin})
		d := e.asset(t)
		if d.RoomID != nil || deref2(d.PropertyID) != cabin || !strings.Contains(e.Summary, "no longer in a room") {
			t.Errorf("place = %v %v (%s)", deref2(d.RoomID), deref2(d.PropertyID), e.Summary)
		}
	})

	t.Run("room in another property moves both", func(t *testing.T) {
		upd(map[string]any{"room_id": kitchen})
		d := upd(map[string]any{"room_id": porch}).asset(t)
		if deref2(d.RoomID) != porch || deref2(d.PropertyID) != cabin {
			t.Errorf("place = %v %v", deref2(d.RoomID), deref2(d.PropertyID))
		}
	})

	t.Run("blank room keeps the property", func(t *testing.T) {
		d := upd(map[string]any{"room_id": ""}).asset(t)
		if d.RoomID != nil || deref2(d.PropertyID) != cabin {
			t.Errorf("place = %v %v", deref2(d.RoomID), deref2(d.PropertyID))
		}
	})

	t.Run("blank property unties it", func(t *testing.T) {
		upd(map[string]any{"room_id": porch})
		d := upd(map[string]any{"property_id": ""}).asset(t)
		if d.RoomID != nil || d.PropertyID != nil {
			t.Errorf("place = %v %v", deref2(d.RoomID), deref2(d.PropertyID))
		}
	})

	t.Run("blank text clears, old id alias", func(t *testing.T) {
		d := call(t, f.ctx, f.upd, "asset_update", map[string]any{"id": a.ID, "manufacturer": "", "warranty_expiry": "2030-06-01"}).asset(t)
		if d.Manufacturer != nil || deref2(d.WarrantyExpiry) != "2030-06-01" {
			t.Errorf("manufacturer=%v warranty=%v", deref2(d.Manufacturer), deref2(d.WarrantyExpiry))
		}
	})

	t.Run("unknown asset", func(t *testing.T) {
		wantNotFound(t, call(t, f.ctx, f.upd, "asset_update", map[string]any{"asset_id": uuid.NewString(), "name": "x"}))
	})
	t.Run("another family's asset", func(t *testing.T) {
		other := f.otherHousehold(t)
		id := insertID(t, f.pool, `INSERT INTO assets (household_id, name) VALUES ($1, 'Piano') RETURNING id`, other)
		wantNotFound(t, call(t, f.ctx, f.upd, "asset_update", map[string]any{"asset_id": id, "name": "Mine now"}))
	})
	for _, tc := range []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"name cannot be cleared", map[string]any{"name": " "}, "name"},
		{"category cannot be cleared", map[string]any{"category": ""}, "category"},
		{"room and property disagree", map[string]any{"room_id": kitchen, "property_id": cabin}, "property_id"},
		{"unknown room", map[string]any{"room_id": uuid.NewString()}, "room_id"},
		{"bad purchase_date", map[string]any{"purchase_date": "03/15/2024"}, "purchase_date"},
		{"nothing to change", map[string]any{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, upd(tc.args), tc.field)
		})
	}
	t.Run("missing asset_id", func(t *testing.T) {
		wantError(t, call(t, f.ctx, f.upd, "asset_update", map[string]any{"name": "x"}), "asset_id")
	})
}

// TestAssetDelete covers what goes with an asset (upkeep deleted, documents
// moved to the family, notes deleted), repeat calls and errors.
func TestAssetDelete(t *testing.T) {
	f := setup(t)
	house := f.property(t, f.hh.HouseholdID, "Maple St")
	a := call(t, f.ctx, f.create, "asset_create", map[string]any{"name": "Furnace", "category": "hvac", "property_id": house}).asset(t)
	ctx := context.Background()
	hh := f.hh.HouseholdID
	for _, q := range []string{
		`INSERT INTO maintenance_tasks (household_id, asset_id, name, due_date) VALUES ($1, $2, 'Replace filter', '2026-12-01')`,
		`INSERT INTO maintenance_tasks (household_id, asset_id, name, due_date) VALUES ($1, $2, 'Annual service', '2027-01-15')`,
		`INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'asset', $2, 'Filter size', '16x25x1')`,
	} {
		if _, err := f.pool.Exec(ctx, q, hh, a.ID); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	blob := insertID(t, f.pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	file := insertID(t, f.pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id)
		VALUES ($1, $2, 'manual.pdf', 1, 'asset', $3) RETURNING id`, hh, blob, a.ID)

	e := call(t, f.ctx, f.del, "asset_delete", map[string]any{"asset_id": a.ID})
	d := e.asset(t)
	if d.ID != a.ID || deref2(d.UpkeepTasksDeleted) != 2 || deref2(d.DocumentsMoved) != 1 || deref2(d.NotesDeleted) != 1 {
		t.Errorf("deleted = %+v", d)
	}
	if !strings.Contains(e.Summary, "Deleted asset Furnace at Maple St") || !strings.Contains(e.Summary, "2 upkeep tasks deleted") ||
		!strings.Contains(e.Summary, "1 document moved to the family") {
		t.Errorf("summary = %q", e.Summary)
	}
	var assets, tasks int
	var fileEntity string
	_ = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM assets WHERE id = $1), (SELECT count(*) FROM maintenance_tasks WHERE asset_id = $1)`, a.ID).Scan(&assets, &tasks)
	_ = f.pool.QueryRow(ctx, `SELECT entity_type FROM files WHERE id = $1`, file).Scan(&fileEntity)
	if assets != 0 || tasks != 0 || fileEntity != "household" {
		t.Errorf("after delete: assets=%d tasks=%d file entity=%q", assets, tasks, fileEntity)
	}

	t.Run("repeat is not found and safe", func(t *testing.T) {
		wantNotFound(t, call(t, f.ctx, f.del, "asset_delete", map[string]any{"asset_id": a.ID}))
	})
	t.Run("old id alias", func(t *testing.T) {
		b := call(t, f.ctx, f.create, "asset_create", map[string]any{"name": "Toaster", "category": "appliance"}).asset(t)
		d := call(t, f.ctx, f.del, "asset_delete", map[string]any{"id": b.ID}).asset(t)
		if d.ID != b.ID || deref2(d.UpkeepTasksDeleted) != 0 {
			t.Errorf("got %+v", d)
		}
	})
	t.Run("another family's asset", func(t *testing.T) {
		other := f.otherHousehold(t)
		id := insertID(t, f.pool, `INSERT INTO assets (household_id, name) VALUES ($1, 'Piano') RETURNING id`, other)
		wantNotFound(t, call(t, f.ctx, f.del, "asset_delete", map[string]any{"asset_id": id}))
		var n int
		_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM assets WHERE id = $1`, id).Scan(&n)
		if n != 1 {
			t.Errorf("another family's asset was deleted")
		}
	})
	t.Run("bad asset_id", func(t *testing.T) {
		wantError(t, call(t, f.ctx, f.del, "asset_delete", map[string]any{"asset_id": "Furnace"}), "asset_id")
	})
	t.Run("missing asset_id", func(t *testing.T) {
		wantError(t, call(t, f.ctx, f.del, "asset_delete", map[string]any{}), "asset_id")
	})
}

// TestDataWarranty pins the warranty arithmetic to the family's today.
func TestDataWarranty(t *testing.T) {
	today := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		expiry time.Time
		days   int
		active bool
	}{
		{today, 0, true},
		{today.AddDate(0, 0, 1), 1, true},
		{today.AddDate(0, 0, -1), -1, false},
	} {
		d := assetRow{WarrantyExpiry: &tc.expiry}.data(today)
		if *d.WarrantyDaysLeft != tc.days || *d.WarrantyActive != tc.active {
			t.Errorf("%s: days=%d active=%v, want %d %v", tc.expiry.Format(time.DateOnly), *d.WarrantyDaysLeft, *d.WarrantyActive, tc.days, tc.active)
		}
	}
}
