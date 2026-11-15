package lists

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/testutil"
)

// envelope is a respond envelope as a client decodes it.
type envelope[T any] struct {
	OK      bool     `json:"ok"`
	Summary string   `json:"summary"`
	Data    *T       `json:"data"`
	Items   []T      `json:"items"`
	Total   *int     `json:"total"`
	Next    []string `json:"next"`
	Cursor  string   `json:"next_cursor"`
	Error   *struct {
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Field   string         `json:"field"`
		Example map[string]any `json:"example"`
	} `json:"error"`
}

func outputSchema(t *testing.T, tool mcp.Tool) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.OutputSchema) == 0 {
		t.Fatalf("tool %s declares no outputSchema", tool.Name)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw.OutputSchema, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// caller runs one tool and checks every result as a strict client would:
// one text block equal to structuredContent, valid against the outputSchema.
type caller[T any] struct {
	tool    mcp.Tool
	handler handlerFunc
	rs      *jsonschema.Resolved
}

func newCaller[T any](t *testing.T, name string, tool mcp.Tool, h handlerFunc) caller[T] {
	t.Helper()
	if name != tool.Name {
		t.Fatalf("returned name %q != tool name %q", name, tool.Name)
	}
	return caller[T]{tool: tool, handler: h, rs: outputSchema(t, tool)}
}

func (c caller[T]) call(t testing.TB, ctx context.Context, args map[string]any) (bool, envelope[T]) {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = c.tool.Name
	req.Params.Arguments = args
	res, err := c.handler(ctx, req)
	if err != nil {
		t.Fatalf("%s returned a protocol error: %v", c.tool.Name, err)
	}
	b, _ := json.Marshal(res)
	var w struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent map[string]any `json:"structuredContent"`
		IsError           bool           `json:"isError"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	if len(w.Content) != 1 || w.Content[0].Type != "text" {
		t.Fatalf("want exactly one text block, got %+v", w.Content)
	}
	var fromText map[string]any
	if err := json.Unmarshal([]byte(w.Content[0].Text), &fromText); err != nil {
		t.Fatalf("text block is not JSON: %v", err)
	}
	x, _ := json.Marshal(fromText)
	y, _ := json.Marshal(w.StructuredContent)
	if string(x) != string(y) {
		t.Fatalf("text and structuredContent differ:\n%s\n%s", x, y)
	}
	if err := c.rs.Validate(w.StructuredContent); err != nil {
		t.Fatalf("%s: structuredContent does not match outputSchema: %v\n%s", c.tool.Name, err, y)
	}
	var env envelope[T]
	if err := json.Unmarshal([]byte(w.Content[0].Text), &env); err != nil {
		t.Fatal(err)
	}
	if w.IsError == env.OK {
		t.Fatalf("isError=%v but ok=%v", w.IsError, env.OK)
	}
	return w.IsError, env
}

func (c caller[T]) ok(t testing.TB, ctx context.Context, args map[string]any) envelope[T] {
	t.Helper()
	isErr, env := c.call(t, ctx, args)
	if isErr {
		t.Fatalf("%s %v: want success, got %q (fix %q)", c.tool.Name, args, env.Summary, env.Error.Fix)
	}
	return env
}

// fail calls and checks the result is an isError naming field, with a fix.
func (c caller[T]) fail(t testing.TB, ctx context.Context, args map[string]any, field, msgPart string) envelope[T] {
	t.Helper()
	isErr, env := c.call(t, ctx, args)
	if !isErr || env.Error == nil {
		t.Fatalf("%s %v: want isError, got %q", c.tool.Name, args, env.Summary)
	}
	if env.Error.Field != field {
		t.Errorf("%s: field = %q, want %q (%s)", c.tool.Name, env.Error.Field, field, env.Error.Message)
	}
	if !strings.Contains(env.Error.Message, msgPart) {
		t.Errorf("%s: message %q does not contain %q", c.tool.Name, env.Error.Message, msgPart)
	}
	if env.Error.Fix == "" {
		t.Errorf("%s: error has no fix", c.tool.Name)
	}
	return env
}

type tools struct {
	find    caller[listInfo]
	get     caller[listDetail]
	create  caller[createResult]
	add     caller[addResult]
	check   caller[checkResult]
	upItem  caller[itemResult]
	remove  caller[removeResult]
	update  caller[listInfo]
	pool    *pgxpool.Pool
	ctx     context.Context
	hh      testutil.Household
	otherCt context.Context
}

func setup(t *testing.T) tools {
	t.Helper()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)
	n, tl, h := NewListFindTool(pool)
	tt := tools{pool: pool, ctx: ctx, hh: hh, otherCt: otherCtx}
	tt.find = newCaller[listInfo](t, n, tl, h)
	n, tl, h = NewListGetTool(pool)
	tt.get = newCaller[listDetail](t, n, tl, h)
	n, tl, h = NewListCreateTool(pool)
	tt.create = newCaller[createResult](t, n, tl, h)
	n, tl, h = NewListAddItemsTool(pool)
	tt.add = newCaller[addResult](t, n, tl, h)
	n, tl, h = NewListCheckItemsTool(pool)
	tt.check = newCaller[checkResult](t, n, tl, h)
	n, tl, h = NewListUpdateItemTool(pool)
	tt.upItem = newCaller[itemResult](t, n, tl, h)
	n, tl, h = NewListRemoveItemsTool(pool)
	tt.remove = newCaller[removeResult](t, n, tl, h)
	n, tl, h = NewListUpdateTool(pool)
	tt.update = newCaller[listInfo](t, n, tl, h)
	return tt
}

// state returns text -> checked for every item of the list.
func state(t testing.TB, pool *pgxpool.Pool, listID string) map[string]bool {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT text, checked FROM list_items WHERE list_id = $1`, listID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var s string
		var c bool
		if err := rows.Scan(&s, &c); err != nil {
			t.Fatal(err)
		}
		out[s] = c
	}
	return out
}

func TestToolDefinitions(t *testing.T) {
	t.Parallel()
	want := map[string]struct {
		required []string
		params   []string
	}{
		"list_find":         {nil, []string{"kind", "query", "templates_only", "include_archived", "limit", "cursor"}},
		"list_get":          {[]string{"list_id"}, []string{"list_id", "checked"}},
		"list_create":       {[]string{"name", "kind"}, []string{"name", "kind", "from_template_id", "items", "is_template", "about_type", "about_id"}},
		"list_add_items":    {[]string{"list_id", "items"}, []string{"list_id", "items"}},
		"list_check_items":  {[]string{"list_id"}, []string{"list_id", "item_ids", "texts", "checked"}},
		"list_update_item":  {[]string{"item_id"}, []string{"item_id", "text", "quantity", "unit", "section", "assignee_person_id", "due_date"}},
		"list_remove_items": {[]string{"list_id"}, []string{"list_id", "item_ids", "texts", "clear_checked"}},
		"list_update":       {[]string{"list_id"}, []string{"list_id", "name", "kind", "archived"}},
	}
	ctors := []func(*pgxpool.Pool) (string, mcp.Tool, handlerFunc){
		NewListFindTool, NewListGetTool, NewListCreateTool, NewListAddItemsTool,
		NewListCheckItemsTool, NewListUpdateItemTool, NewListRemoveItemsTool, NewListUpdateTool,
	}
	for _, ctor := range ctors {
		name, tool, _ := ctor(nil)
		w, ok := want[name]
		if !ok {
			t.Errorf("unexpected tool %s", name)
			continue
		}
		var got []string
		for p := range tool.InputSchema.Properties {
			got = append(got, p)
		}
		slices.Sort(got)
		wp := slices.Clone(w.params)
		slices.Sort(wp)
		if !slices.Equal(got, wp) {
			t.Errorf("%s params = %v, want %v", name, got, wp)
		}
		req := slices.Clone(tool.InputSchema.Required)
		slices.Sort(req)
		wr := slices.Clone(w.required)
		slices.Sort(wr)
		if !slices.Equal(req, wr) {
			t.Errorf("%s required = %v, want %v", name, req, wr)
		}
		if n := len([]rune(tool.Description)); n == 0 || n > 200 {
			t.Errorf("%s description is %d characters", name, n)
		}
	}
}

// TestAddMilkAndEggs is the catalog's example: one call, the list found by
// what people call it, created if the family has none yet.
func TestAddMilkAndEggs(t *testing.T) {
	tt := setup(t)
	ctx := tt.ctx

	// No grocery list yet: the same single call creates it.
	env := tt.add.ok(t, ctx, map[string]any{"list_id": "the grocery list", "items": []any{"milk", "eggs"}})
	d := env.Data
	if !d.CreatedList || d.List.Kind != "grocery" || d.List.Name != "Grocery" {
		t.Fatalf("want a new Grocery list, got %+v", d.List)
	}
	if got := itemTexts(d.Added); !slices.Equal(got, []string{"milk", "eggs"}) {
		t.Fatalf("added = %v", got)
	}
	if !strings.Contains(env.Summary, "Added milk and eggs") || !strings.Contains(env.Next[0], "list_check_items") {
		t.Errorf("summary %q next %v", env.Summary, env.Next)
	}
	groceryID := d.List.ID

	// The second time the list is found (by kind, by name, by id) and nothing
	// is duplicated.
	for _, ref := range []string{"grocery", "Groceries", "GROCERY", groceryID} {
		env = tt.add.ok(t, ctx, map[string]any{"list_id": ref, "items": "Milk, eggs"})
		if env.Data.List.ID != groceryID || env.Data.CreatedList || len(env.Data.Added) != 0 || len(env.Data.AlreadyOnList) != 2 {
			t.Fatalf("list_id %q: %+v", ref, env.Data)
		}
	}
	if !strings.Contains(env.Summary, "already on") {
		t.Errorf("summary %q", env.Summary)
	}

	// Objects with details; a checked item comes back instead of a copy.
	tt.check.ok(t, ctx, map[string]any{"list_id": "grocery", "texts": []string{"milk"}})
	env = tt.add.ok(t, ctx, map[string]any{"list_id": "Grocery", "items": []any{
		map[string]any{"text": "milk", "quantity": 2, "unit": "gal"},
		map[string]any{"text": "bananas", "section": "Produce", "quantity": "6"},
	}})
	if len(env.Data.Reopened) != 1 || env.Data.Reopened[0].Checked || *env.Data.Reopened[0].Quantity != 2 {
		t.Errorf("reopened = %+v", env.Data.Reopened)
	}
	if len(env.Data.Added) != 1 || *env.Data.Added[0].Section != "Produce" || *env.Data.Added[0].Quantity != 6 {
		t.Errorf("added = %+v", env.Data.Added)
	}
	if s := state(t, tt.pool, groceryID); len(s) != 3 || s["milk"] {
		t.Errorf("items = %v", s)
	}
	if env.Data.List.OpenCount != 3 {
		t.Errorf("open_count = %d", env.Data.List.OpenCount)
	}

	tt.add.fail(t, ctx, map[string]any{"list_id": "grocery", "items": []any{}}, "items", "empty")
	tt.add.fail(t, ctx, map[string]any{"list_id": "grocery", "items": []any{map[string]any{"text": "x", "quantity": -1}}}, "items", "quantity")
	tt.add.fail(t, ctx, map[string]any{"list_id": "grocery", "items": []any{map[string]any{"text": "x", "due_date": "friday"}}}, "items", "due_date")
	tt.add.fail(t, ctx, map[string]any{"list_id": "Hardware store", "items": []any{"nails"}}, "list_id", "No list named")
	tt.add.fail(t, tt.otherCt, map[string]any{"list_id": groceryID, "items": []any{"nails"}}, "list_id", "No list with id")
}

// TestCheckOffByText: "check off the turkey" matches by text.
func TestCheckOffByText(t *testing.T) {
	tt := setup(t)
	ctx := tt.ctx
	created := tt.create.ok(t, ctx, map[string]any{"name": "Thanksgiving", "kind": "grocery", "items": []any{
		"Turkey, 14 lb", "Egg", "cranberry sauce", "Sweet potatoes", "potatoes", "Brown sugar", "white sugar",
	}})
	id := created.Data.List.ID

	env := tt.check.ok(t, ctx, map[string]any{"list_id": "Thanksgiving", "texts": []string{"turkey"}})
	if got := itemTexts(env.Data.Changed); !slices.Equal(got, []string{"Turkey, 14 lb"}) {
		t.Fatalf("changed = %v", got)
	}
	if !strings.Contains(env.Summary, "Checked off Turkey, 14 lb") || !strings.HasPrefix(env.Next[0], "6 left: ") {
		t.Errorf("summary %q next %v", env.Summary, env.Next)
	}

	// Plurals match, and an exact text beats a partial one ("potatoes" is
	// not "Sweet potatoes"). Several texts in one call.
	env = tt.check.ok(t, ctx, map[string]any{"list_id": id, "texts": []string{"eggs", "potatoes", "Cranberry Sauce"}})
	if got := itemTexts(env.Data.Changed); !slices.Equal(got, []string{"Egg", "cranberry sauce", "potatoes"}) {
		t.Fatalf("changed = %v", got)
	}
	// Repeating changes nothing.
	env = tt.check.ok(t, ctx, map[string]any{"list_id": id, "texts": []string{"egg"}})
	if len(env.Data.Changed) != 0 || len(env.Data.Unchanged) != 1 || !strings.Contains(env.Summary, "already checked off") {
		t.Errorf("repeat: %+v %q", env.Data, env.Summary)
	}

	// An ambiguous partial text fails the whole call and changes nothing.
	before := state(t, tt.pool, id)
	e := tt.check.fail(t, ctx, map[string]any{"list_id": id, "texts": []string{"sweet potatoes", "sugar"}}, "texts", `"sugar" matches 2 items`)
	if !strings.Contains(e.Error.Message, "Brown sugar") || !strings.Contains(e.Error.Message, "white sugar") {
		t.Errorf("candidates missing: %s", e.Error.Message)
	}
	if after := state(t, tt.pool, id); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("failed call changed items: %v -> %v", before, after)
	}
	tt.check.fail(t, ctx, map[string]any{"list_id": id, "texts": []string{"gravy"}}, "texts", `No item on "Thanksgiving" matches "gravy"`)
	tt.check.fail(t, ctx, map[string]any{"list_id": id, "item_ids": []string{uuid.NewString()}}, "item_ids", "is not an item")
	tt.check.fail(t, ctx, map[string]any{"list_id": id}, "texts", "Pass item_ids or texts")

	// Uncheck by id; by text, "turkey" now prefers the checked one.
	env = tt.check.ok(t, ctx, map[string]any{"list_id": id, "texts": []string{"turkey"}, "checked": false})
	if len(env.Data.Changed) != 1 || env.Data.Changed[0].Checked || env.Data.Checked {
		t.Errorf("uncheck: %+v", env.Data)
	}
	env = tt.check.ok(t, ctx, map[string]any{"list_id": id, "item_ids": []string{env.Data.Changed[0].ID}})
	if !env.Data.Changed[0].Checked {
		t.Errorf("check by id: %+v", env.Data)
	}

	// Checking everything off says so.
	env = tt.check.ok(t, ctx, map[string]any{"list_id": id, "texts": "sweet potatoes, brown sugar, white sugar"})
	if env.Data.List.OpenCount != 0 || !strings.Contains(env.Summary, "Nothing left") || !strings.Contains(env.Next[1], "clear_checked") {
		t.Errorf("all done: %q %v", env.Summary, env.Next)
	}
}

func TestMatchText(t *testing.T) {
	t.Parallel()
	keys := func(texts ...string) []itemKey {
		out := make([]itemKey, len(texts))
		for i, s := range texts {
			out[i] = itemKey{ID: uuid.New(), Text: s}
		}
		return out
	}
	tests := []struct {
		items     []itemKey
		text      string
		want      []string
		ambiguous int
	}{
		{keys("Turkey, 14 lb", "Milk"), "turkey", []string{"Turkey, 14 lb"}, 0},
		{keys("Egg"), "eggs", []string{"Egg"}, 0},
		{keys("tomatoes"), "tomato", []string{"tomatoes"}, 0},
		{keys("berries"), "berry", []string{"berries"}, 0},
		{keys("glass"), "glas", []string{"glass"}, 0},
		{keys("potatoes", "Sweet potatoes"), "potatoes", []string{"potatoes"}, 0},
		{keys("milk", "Milk "), "MILK", []string{"milk", "Milk "}, 0},
		{keys("Brown sugar", "white sugar"), "sugar", nil, 2},
		{keys("ham"), "graham crackers", []string{"ham"}, 0},
		{keys("ox"), "box", nil, 0},
		{keys("Milk"), "eggs", nil, 0},
	}
	for _, tc := range tests {
		m, amb := matchText(tc.items, tc.text, nil)
		var got []string
		for _, k := range m {
			got = append(got, k.Text)
		}
		if !slices.Equal(got, tc.want) || len(amb) != tc.ambiguous {
			t.Errorf("matchText(%q) = %v, ambiguous %d; want %v, %d", tc.text, got, len(amb), tc.want, tc.ambiguous)
		}
	}
	// The wanted state breaks a tie: checking off "milk" means the open one.
	items := []itemKey{{ID: uuid.New(), Text: "Milk", Checked: true}, {ID: uuid.New(), Text: "Milk"}}
	checked := true
	if m, _ := matchText(items, "milk", &checked); len(m) != 1 || m[0].Checked {
		t.Errorf("prefer open: %+v", m)
	}
}

func TestParseHelpers(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"the grocery list": "grocery", "Our To-Do List": "to-do", "Costco run": "costco run", "  Family   chores ": "chores",
	} {
		if got := normListRef(in); got != want {
			t.Errorf("normListRef(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"Groceries": "grocery", "to do": "todo", "Packing list": "packing", "chore": "chores"} {
		if got, ok := parseKind(in); !ok || got != want {
			t.Errorf("parseKind(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := parseKind("hardware"); ok {
		t.Error("parseKind(hardware) should fail")
	}
	items, bad := parseItems("milk,, eggs\nbread;", nil)
	if bad != nil || len(items) != 3 || items[2].Text != "bread" {
		t.Errorf("parseItems string: %+v %v", items, bad)
	}
	if _, bad := parseItems([]any{map[string]any{"quantity": 2}}, nil); bad == nil {
		t.Error("an object without text should fail")
	}
	if items, _ := parseItems(map[string]any{"name": "soap"}, nil); len(items) != 1 || items[0].Text != "soap" {
		t.Errorf("single object with name alias: %+v", items)
	}
}

func TestCreateFromTemplate(t *testing.T) {
	tt := setup(t)
	ctx := tt.ctx
	tmpl := tt.create.ok(t, ctx, map[string]any{"name": "Beach trip packing", "kind": "packing", "is_template": true,
		"items": []any{map[string]any{"text": "Sunscreen", "section": "Bathroom"}, "Towels", "Swimsuits"}})
	if !tmpl.Data.List.IsTemplate || tmpl.Data.List.ItemCount != 3 {
		t.Fatalf("template: %+v", tmpl.Data.List)
	}
	// A checked template item is copied unchecked.
	if _, err := tt.pool.Exec(context.Background(), `UPDATE list_items SET checked = true WHERE list_id = $1 AND text = 'Towels'`, tmpl.Data.List.ID); err != nil {
		t.Fatal(err)
	}

	env := tt.create.ok(t, ctx, map[string]any{"name": "Beach trip", "kind": "packing", "from_template_id": "beach trip packing",
		"items": []any{"Kite", "towels"}})
	d := env.Data
	if d.FromTemplate == nil || *d.FromTemplate != "Beach trip packing" || d.List.IsTemplate {
		t.Fatalf("from template: %+v", d)
	}
	if got := itemTexts(d.Items); !slices.Equal(got, []string{"Kite", "towels", "Sunscreen", "Swimsuits"}) {
		t.Errorf("items = %v (given items first, template order, no duplicate towels)", got)
	}
	for _, it := range d.Items {
		if it.Checked {
			t.Errorf("%s copied checked", it.Text)
		}
		if it.Text == "Sunscreen" && (it.Section == nil || *it.Section != "Bathroom") {
			t.Errorf("section not copied: %+v", it)
		}
	}
	if !strings.Contains(env.Summary, `from "Beach trip packing" with 4 items`) {
		t.Errorf("summary %q", env.Summary)
	}

	// Template by kind word and by id; name and kind default to the template's.
	env = tt.create.ok(t, ctx, map[string]any{"name": "Lake weekend", "kind": "", "from_template_id": "packing"})
	if env.Data.List.Kind != "packing" || env.Data.List.ItemCount != 3 {
		t.Errorf("by kind: %+v", env.Data.List)
	}

	// Loop guard: the same create again returns the same list.
	again := tt.create.ok(t, ctx, map[string]any{"name": "beach trip", "kind": "packing", "from_template_id": "beach trip packing"})
	if again.Data.List.ID != d.List.ID || !again.Data.List.Duplicate {
		t.Errorf("loop guard: %+v", again.Data.List)
	}

	tt.create.fail(t, ctx, map[string]any{"name": "Ski", "kind": "packing", "from_template_id": "Ski trip"}, "from_template_id", "No template named")
	tt.create.fail(t, ctx, map[string]any{"name": "Ski", "kind": "hardware"}, "kind", "not a list kind")
	tt.create.fail(t, ctx, map[string]any{"name": "", "kind": "todo"}, "name", "required")
	tt.create.fail(t, ctx, map[string]any{"name": "Car trip", "kind": "packing", "about_type": "vehicle", "about_id": uuid.NewString()}, "about_id", "No vehicle with id")
	tt.create.fail(t, tt.otherCt, map[string]any{"name": "x", "kind": "packing", "from_template_id": tmpl.Data.List.ID}, "from_template_id", "No list or template")

	// About a person of the family.
	var nora string
	if err := tt.pool.QueryRow(context.Background(), `INSERT INTO people (household_id, display_name) VALUES ($1, 'Nora') RETURNING id::text`, tt.hh.HouseholdID).Scan(&nora); err != nil {
		t.Fatal(err)
	}
	env = tt.create.ok(t, ctx, map[string]any{"name": "Nora's camp", "kind": "packing", "about_type": "person", "about_id": nora,
		"items": []any{map[string]any{"text": "Sleeping bag", "assignee_person_id": "nora", "due_date": "2026-12-01"}}})
	if env.Data.List.AboutType != "person" || *env.Data.List.AboutID != nora {
		t.Errorf("about: %+v", env.Data.List)
	}
	if it := env.Data.Items[0]; it.AssigneeName == nil || *it.AssigneeName != "Nora" || *it.DueDate != "2026-12-01" || it.DueWeekday == nil || *it.DueWeekday != "Tue" {
		t.Errorf("item: %+v", it)
	}
}

func TestFindAndGet(t *testing.T) {
	tt := setup(t)
	ctx := tt.ctx
	g := tt.create.ok(t, ctx, map[string]any{"name": "Grocery", "kind": "grocery", "items": []any{"milk", "turkey", "bread"}}).Data.List
	tt.create.ok(t, ctx, map[string]any{"name": "Grocery staples", "kind": "grocery", "is_template": true, "items": []any{"rice"}})
	old := tt.create.ok(t, ctx, map[string]any{"name": "Old chores", "kind": "chores"}).Data.List
	tt.update.ok(t, ctx, map[string]any{"list_id": old.ID, "archived": true})
	for i := range 4 {
		tt.create.ok(t, ctx, map[string]any{"name": fmt.Sprintf("Todo %d", i), "kind": "todo"})
	}
	tt.create.ok(t, tt.otherCt, map[string]any{"name": "Grocery", "kind": "grocery", "items": []any{"secret"}})

	env := tt.find.ok(t, ctx, map[string]any{"kind": "groceries"})
	if len(env.Items) != 2 || env.Items[0].Name != "Grocery" || !env.Items[1].IsTemplate || env.Items[0].OpenCount != 3 {
		t.Fatalf("by kind: %+v", env.Items)
	}
	if env = tt.find.ok(t, ctx, map[string]any{"templates_only": true}); len(env.Items) != 1 || env.Items[0].Name != "Grocery staples" {
		t.Errorf("templates_only: %+v", env.Items)
	}
	if env = tt.find.ok(t, ctx, map[string]any{"query": "turkey"}); len(env.Items) != 1 || env.Items[0].ID != g.ID {
		t.Errorf("query by item text: %+v", env.Items)
	}
	if env = tt.find.ok(t, ctx, map[string]any{"query": "chores"}); len(env.Items) != 0 {
		t.Errorf("archived listed by default: %+v", env.Items)
	}
	if env = tt.find.ok(t, ctx, map[string]any{"query": "chores", "include_archived": true}); len(env.Items) != 1 || !env.Items[0].Archived {
		t.Errorf("include_archived: %+v", env.Items)
	}
	// Paging with next_cursor.
	env = tt.find.ok(t, ctx, map[string]any{"limit": 3})
	if len(env.Items) != 3 || env.Cursor == "" || *env.Total != 6 {
		t.Fatalf("page 1: %d items, cursor %q, total %v", len(env.Items), env.Cursor, *env.Total)
	}
	env2 := tt.find.ok(t, ctx, map[string]any{"limit": 3, "cursor": env.Cursor})
	if len(env2.Items) != 3 || env2.Cursor != "" || env2.Items[0].ID == env.Items[0].ID {
		t.Errorf("page 2: %+v cursor %q", env2.Items, env2.Cursor)
	}
	tt.find.fail(t, ctx, map[string]any{"cursor": "nope"}, "cursor", "cursor")
	tt.find.fail(t, ctx, map[string]any{"kind": "hardware"}, "kind", "not a list kind")

	// list_get: exact name wins over the template; filters; who added.
	if _, err := tt.pool.Exec(context.Background(), `UPDATE list_items SET created_by = $1 WHERE list_id = $2 AND text = 'milk'`, tt.hh.UserID, g.ID); err != nil {
		t.Fatal(err)
	}
	tt.check.ok(t, ctx, map[string]any{"list_id": g.ID, "texts": []string{"bread"}})
	got := tt.get.ok(t, ctx, map[string]any{"list_id": "grocery"})
	if got.Data.List.ID != g.ID || len(got.Data.Items) != 3 || got.Data.OpenCount != 2 || got.Data.DoneCount != 1 {
		t.Fatalf("get: %+v", got.Data)
	}
	if a := got.Data.Items[0].AddedBy; a == nil || *a != "Test Owner" {
		t.Errorf("added_by = %v", a)
	}
	if !strings.Contains(got.Next[0], `texts=["milk","turkey"]`) {
		t.Errorf("next = %v", got.Next)
	}
	if open := tt.get.ok(t, ctx, map[string]any{"list_id": g.ID, "checked": "open"}); len(open.Data.Items) != 2 || open.Data.Showing != "open" {
		t.Errorf("open: %+v", open.Data.Items)
	}
	if done := tt.get.ok(t, ctx, map[string]any{"list_id": g.ID, "checked": "done"}); len(done.Data.Items) != 1 || done.Data.Items[0].Text != "bread" {
		t.Errorf("done: %+v", done.Data.Items)
	}
	tt.get.fail(t, ctx, map[string]any{"list_id": g.ID, "checked": "maybe"}, "checked", "not all, open or done")
	tt.get.fail(t, tt.otherCt, map[string]any{"list_id": g.ID}, "list_id", "No list with id")
	tt.get.fail(t, ctx, map[string]any{"list_id": "todo"}, "list_id", "4 lists match")
	if s := tt.get.ok(t, ctx, map[string]any{"list_id": "Todo 2"}); s.Data.List.Name != "Todo 2" {
		t.Errorf("exact name: %+v", s.Data.List)
	}
}

func TestArchivedListIsFrozen(t *testing.T) {
	tt := setup(t)
	ctx := tt.ctx
	l := tt.create.ok(t, ctx, map[string]any{"name": "Beach trip", "kind": "packing", "items": []any{"Towels"}})
	itemID := l.Data.Items[0].ID
	env := tt.update.ok(t, ctx, map[string]any{"list_id": "Beach trip", "archived": true})
	if !env.Data.Archived || !strings.Contains(env.Summary, "archived it") {
		t.Fatalf("archive: %+v %q", env.Data, env.Summary)
	}
	tt.add.fail(t, ctx, map[string]any{"list_id": "Beach trip", "items": []any{"Kite"}}, "list_id", "is archived")
	tt.check.fail(t, ctx, map[string]any{"list_id": "Beach trip", "texts": []string{"towels"}}, "list_id", "is archived")
	tt.remove.fail(t, ctx, map[string]any{"list_id": "Beach trip", "texts": []string{"towels"}}, "list_id", "is archived")
	tt.upItem.fail(t, ctx, map[string]any{"item_id": itemID, "text": "Beach towels"}, "list_id", "is archived")
	// Still readable.
	if g := tt.get.ok(t, ctx, map[string]any{"list_id": "Beach trip"}); !g.Data.List.Archived || len(g.Data.Items) != 1 {
		t.Errorf("get archived: %+v", g.Data)
	}
	if s := state(t, tt.pool, l.Data.List.ID); len(s) != 1 || s["Towels"] {
		t.Errorf("archived list changed: %v", s)
	}
	tt.update.ok(t, ctx, map[string]any{"list_id": "Beach trip", "archived": false, "name": "Beach trip 2026"})
	tt.add.ok(t, ctx, map[string]any{"list_id": "Beach trip 2026", "items": []any{"Kite"}})
	tt.update.fail(t, ctx, map[string]any{"list_id": "Beach trip 2026"}, "name", "Nothing to change")
}

func TestUpdateAndRemoveItems(t *testing.T) {
	tt := setup(t)
	ctx := tt.ctx
	var nora string
	if err := tt.pool.QueryRow(context.Background(), `INSERT INTO people (household_id, display_name) VALUES ($1, 'Nora') RETURNING id::text`, tt.hh.HouseholdID).Scan(&nora); err != nil {
		t.Fatal(err)
	}
	l := tt.create.ok(t, ctx, map[string]any{"name": "Chores", "kind": "chores", "items": []any{
		map[string]any{"text": "Dishes", "section": "Kitchen", "quantity": 1}, "Laundry", "Trash", "Vacuum",
	}}).Data
	dishes := l.Items[0].ID

	env := tt.upItem.ok(t, ctx, map[string]any{"item_id": dishes, "assignee_person_id": "Nora", "due_date": "2026-11-20", "section": nil, "unit": "", "text": "Dishes and counters"})
	it := env.Data.Item
	if it.Text != "Dishes and counters" || it.AssigneePersonID == nil || *it.AssigneePersonID != nora || it.Section != nil || *it.Quantity != 1 || *it.DueWeekday != "Fri" {
		t.Errorf("update item: %+v", it)
	}
	env = tt.upItem.ok(t, ctx, map[string]any{"item_id": dishes, "quantity": nil, "assignee_person_id": nil})
	if env.Data.Item.Quantity != nil || env.Data.Item.AssigneePersonID != nil || env.Data.Item.DueDate == nil {
		t.Errorf("clear: %+v", env.Data.Item)
	}
	tt.upItem.fail(t, ctx, map[string]any{"item_id": dishes}, "text", "Nothing to change")
	tt.upItem.fail(t, ctx, map[string]any{"item_id": "Dishes", "text": "x"}, "item_id", "not an item id")
	tt.upItem.fail(t, ctx, map[string]any{"item_id": dishes, "assignee_person_id": "Zed"}, "assignee_person_id", "No person")
	tt.upItem.fail(t, ctx, map[string]any{"item_id": dishes, "quantity": 0}, "quantity", "above 0")
	tt.upItem.fail(t, tt.otherCt, map[string]any{"item_id": dishes, "text": "x"}, "item_id", "No list item")

	tt.check.ok(t, ctx, map[string]any{"list_id": "chores", "texts": []string{"laundry", "trash"}})
	rm := tt.remove.ok(t, ctx, map[string]any{"list_id": "Chores", "texts": []string{"vacuum"}, "clear_checked": true})
	if got := itemTexts(rm.Data.Removed); !slices.Equal(got, []string{"Laundry", "Trash", "Vacuum"}) {
		t.Errorf("removed = %v", got)
	}
	if s := state(t, tt.pool, l.List.ID); len(s) != 1 {
		t.Errorf("left = %v", s)
	}
	if rm = tt.remove.ok(t, ctx, map[string]any{"list_id": "Chores", "clear_checked": true}); len(rm.Data.Removed) != 0 || !strings.Contains(rm.Summary, "Nothing to remove") {
		t.Errorf("empty clear: %+v %q", rm.Data, rm.Summary)
	}
	tt.remove.fail(t, ctx, map[string]any{"list_id": "Chores"}, "texts", "Pass item_ids")
	tt.remove.fail(t, ctx, map[string]any{"list_id": "Chores", "texts": []string{"mow"}}, "texts", "No item")
}

// TestConcurrentCheckOffs: several people check off different items of one
// list at the same moment, while others edit item texts and add items. No
// write may overwrite or lose another.
func TestConcurrentCheckOffs(t *testing.T) {
	tt := setup(t)
	ctx := tt.ctx
	var names []any
	for i := range 40 {
		names = append(names, fmt.Sprintf("item %02d", i))
	}
	l := tt.create.ok(t, ctx, map[string]any{"name": "Grocery", "kind": "grocery", "items": names}).Data
	ids := map[string]string{}
	for _, it := range l.Items {
		ids[it.Text] = it.ID
	}

	var wg sync.WaitGroup
	errs := make(chan string, 200)
	run := func(f func() (bool, string)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if isErr, msg := f(); isErr {
				errs <- msg
			}
		}()
	}
	// items 00-19: checked off one per call, half by text, half by id.
	for i := range 20 {
		run(func() (bool, string) {
			args := map[string]any{"list_id": "grocery", "texts": []string{fmt.Sprintf("item %02d", i)}}
			if i%2 == 1 {
				args = map[string]any{"list_id": l.List.ID, "item_ids": []string{ids[fmt.Sprintf("item %02d", i)]}}
			}
			isErr, env := tt.check.call(t, ctx, args)
			return isErr, env.Summary
		})
	}
	// items 20-29: texts edited.
	for i := 20; i < 30; i++ {
		run(func() (bool, string) {
			isErr, env := tt.upItem.call(t, ctx, map[string]any{"item_id": ids[fmt.Sprintf("item %02d", i)], "text": fmt.Sprintf("edited %02d", i)})
			return isErr, env.Summary
		})
	}
	// Adds, including the same text from several people at once.
	for i := range 10 {
		run(func() (bool, string) {
			isErr, env := tt.add.call(t, ctx, map[string]any{"list_id": "Grocery", "items": []any{fmt.Sprintf("new %d", i), "shared"}})
			return isErr, env.Summary
		})
	}
	// One batch check-off overlapping nothing else: items 30-34.
	run(func() (bool, string) {
		isErr, env := tt.check.call(t, ctx, map[string]any{"list_id": "grocery", "texts": []string{"item 30", "item 31", "item 32", "item 33", "item 34"}})
		return isErr, env.Summary
	})
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent call failed: %s", e)
	}

	s := state(t, tt.pool, l.List.ID)
	for i := range 40 {
		name := fmt.Sprintf("item %02d", i)
		switch {
		case i < 20 || (i >= 30 && i < 35):
			if !s[name] {
				t.Errorf("%s lost its check-off", name)
			}
		case i < 30:
			if _, ok := s[fmt.Sprintf("edited %02d", i)]; !ok {
				t.Errorf("%s lost its edit", name)
			}
		default:
			if checked, ok := s[name]; !ok || checked {
				t.Errorf("%s changed: present %v checked %v", name, ok, checked)
			}
		}
	}
	for i := range 10 {
		if _, ok := s[fmt.Sprintf("new %d", i)]; !ok {
			t.Errorf("new %d lost", i)
		}
	}
	var shared, positions, distinct int
	if err := tt.pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE text = 'shared'), count(*), count(DISTINCT position) FROM list_items WHERE list_id = $1`, l.List.ID).Scan(&shared, &positions, &distinct); err != nil {
		t.Fatal(err)
	}
	if shared != 1 {
		t.Errorf("'shared' added %d times by parallel adds, want 1", shared)
	}
	if positions != distinct {
		t.Errorf("%d items share positions (%d distinct)", positions, distinct)
	}
}

// TestConcurrentAddsCreateOneList: parallel "add to the grocery list" calls
// in a family with no grocery list create exactly one.
func TestConcurrentAddsCreateOneList(t *testing.T) {
	tt := setup(t)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if isErr, env := tt.add.call(t, tt.ctx, map[string]any{"list_id": "grocery", "items": []any{fmt.Sprintf("thing %d", i)}}); isErr {
				t.Errorf("add: %s", env.Summary)
			}
		}()
	}
	wg.Wait()
	var lists, items int
	if err := tt.pool.QueryRow(context.Background(),
		`SELECT count(DISTINCT l.id), count(i.id) FROM lists l LEFT JOIN list_items i ON i.list_id = l.id WHERE l.household_id = $1`,
		tt.hh.HouseholdID).Scan(&lists, &items); err != nil {
		t.Fatal(err)
	}
	if lists != 1 || items != 8 {
		t.Errorf("lists = %d, items = %d; want 1 and 8", lists, items)
	}
}
