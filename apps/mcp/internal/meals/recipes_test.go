package meals

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type env struct {
	OK      bool            `json:"ok"`
	Summary string          `json:"summary"`
	Data    json.RawMessage `json:"data"`
	Items   json.RawMessage `json:"items"`
	Total   *int            `json:"total"`
	Next    []string        `json:"next"`
	Cursor  string          `json:"next_cursor"`
	Error   *struct {
		Message string `json:"message"`
		Fix     string `json:"fix"`
		Field   string `json:"field"`
	} `json:"error"`
}

func (e env) recipe(t *testing.T) recipeData {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d recipeData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return d
}

func (e env) items(t *testing.T) []recipeItem {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var out []recipeItem
	if err := json.Unmarshal(e.Items, &out); err != nil {
		t.Fatalf("decode items: %v", err)
	}
	return out
}

func (e env) wantError(t *testing.T, field, contains string) {
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
	if contains != "" && !strings.Contains(e.Error.Message, contains) {
		t.Errorf("error %q does not mention %q", e.Error.Message, contains)
	}
}

type tool struct {
	def mcp.Tool
	h   toolHandler
}

type tools struct{ find, get, create, update, del tool }

func newTools(pool *pgxpool.Pool) tools {
	mk := func(_ string, d mcp.Tool, h toolHandler) tool { return tool{d, h} }
	return tools{
		find: mk(NewRecipeFindTool(pool)), get: mk(NewRecipeGetTool(pool)), create: mk(NewRecipeCreateTool(pool)),
		update: mk(NewRecipeUpdateTool(pool)), del: mk(NewRecipeDeleteTool(pool)),
	}
}

func resolvedSchema(t *testing.T, tl mcp.Tool) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(tl)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || len(raw.OutputSchema) == 0 {
		t.Fatalf("%s declares no outputSchema (%v)", tl.Name, err)
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

// run calls the tool and checks the respond invariants: one text block equal
// to structuredContent, valid against the outputSchema, ok == !isError.
func (w tool) run(t *testing.T, ctx context.Context, args map[string]any) env {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = w.def.Name
	// Round-trip through JSON, as the transport does (ints become float64).
	b, _ := json.Marshal(args)
	var wire map[string]any
	_ = json.Unmarshal(b, &wire)
	req.Params.Arguments = wire
	res, err := w.h(ctx, req)
	if err != nil {
		t.Fatalf("%s returned a Go error: %v", w.def.Name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("want 1 content item, got %d", len(res.Content))
	}
	text := res.Content[0].(mcp.TextContent).Text
	sc, err := json.Marshal(res.StructuredContent)
	if err != nil || string(sc) != text {
		t.Fatalf("text differs from structuredContent (%v)", err)
	}
	var instance map[string]any
	if err := json.Unmarshal(sc, &instance); err != nil {
		t.Fatal(err)
	}
	if err := resolvedSchema(t, w.def).Validate(instance); err != nil {
		t.Fatalf("%s result does not match its outputSchema: %v\n%s", w.def.Name, err, text)
	}
	var e env
	if err := json.Unmarshal(sc, &e); err != nil {
		t.Fatal(err)
	}
	if e.OK == res.IsError {
		t.Fatalf("ok=%v but isError=%v", e.OK, res.IsError)
	}
	return e
}

func memberCtx(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) (context.Context, uuid.UUID) {
	t.Helper()
	bg := context.Background()
	email := role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(bg, `INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`, email, role).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hh.HouseholdID, uid, role); err != nil {
		t.Fatal(err)
	}
	return server.ContextWithClaims(bg, &auth.Claims{
		Identity: auth.Identity{Subject: uid.String(), Email: email}, UserID: uid.String(),
		HouseholdID: hh.HouseholdID.String(), Role: role,
	}), uid
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v\n%s", err, sql)
	}
	return n
}

func names(items []recipeItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Name
	}
	return out
}

var tacos = map[string]any{
	"name": "Chicken Tacos", "servings": 4, "total_minutes": 30, "tags": []any{"Dinner", "quick", "dinner"},
	"ingredients": []any{
		map[string]any{"item": "chicken thighs", "quantity": 1.5, "unit": "pounds"},
		map[string]any{"item": "corn tortillas", "quantity": 8},
		map[string]any{"item": "tomatoes", "quantity": "2", "section": "Salsa"},
		map[string]any{"item": "lime", "quantity": "1/2", "section": "Salsa"},
		"2 tablespoons olive oil",
		map[string]any{"item": "salt", "quantity": "a pinch"},
	},
	"steps": []any{"1. Season the chicken.", "Step 2: Grill it.\nSlice thinly.", "Warm the tortillas."},
}

// ---------------------------------------------------------------------------
// Definitions and pure helpers (no database)
// ---------------------------------------------------------------------------

// TestToolDefinitions pins the tools to the catalog: names, required params,
// descriptions (<= 200 chars, params <= 80), all four hints, outputSchema.
func TestToolDefinitions(t *testing.T) {
	ts := newTools(nil)
	for _, tc := range []struct {
		w                          tool
		name                       string
		required, params           []string
		readOnly, destr, idempoten bool
	}{
		{ts.find, "recipe_find", nil, []string{"query", "tag", "ingredient", "max_minutes", "limit", "cursor", "id"}, true, false, true},
		{ts.get, "recipe_get", []string{"recipe_id"}, nil, true, false, true},
		{ts.create, "recipe_create", []string{"name", "ingredients", "steps"}, []string{"servings", "total_minutes", "tags", "source_url", "notes"}, false, false, false},
		{ts.update, "recipe_update", []string{"recipe_id"}, []string{"name", "ingredients", "steps", "servings", "total_minutes", "tags", "source_url", "notes"}, false, false, true},
		{ts.del, "recipe_delete", []string{"recipe_id"}, nil, false, true, true},
	} {
		d := tc.w.def
		if d.Name != tc.name {
			t.Errorf("name %q, want %q", d.Name, tc.name)
		}
		if n := utf8.RuneCountInString(d.Description); n == 0 || n > 200 {
			t.Errorf("%s description has %d chars", d.Name, n)
		}
		req := slices.Clone(d.InputSchema.Required)
		slices.Sort(req)
		want := slices.Clone(tc.required)
		slices.Sort(want)
		if !slices.Equal(req, want) {
			t.Errorf("%s required = %v, want %v", d.Name, req, want)
		}
		for _, p := range append(slices.Clone(tc.required), tc.params...) {
			prop, ok := d.InputSchema.Properties[p].(map[string]any)
			if !ok {
				t.Errorf("%s lacks param %s", d.Name, p)
				continue
			}
			if desc, _ := prop["description"].(string); desc == "" || utf8.RuneCountInString(desc) > 80 {
				t.Errorf("%s.%s description %q (want 1-80 chars)", d.Name, p, desc)
			}
		}
		a := d.Annotations
		if a.Title == "" || a.ReadOnlyHint == nil || *a.ReadOnlyHint != tc.readOnly || a.DestructiveHint == nil || *a.DestructiveHint != tc.destr ||
			a.IdempotentHint == nil || *a.IdempotentHint != tc.idempoten || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s hints = %+v", d.Name, a)
		}
		resolvedSchema(t, d)
	}
}

func TestParseAmountAndLines(t *testing.T) {
	for in, want := range map[string]float64{"2": 2, "1.5": 1.5, "1/2": 0.5, "1 1/2": 1.5, "½": 0.5, "1½": 1.5, "1/3": 0.333} {
		if got, ok := parseAmount(in); !ok || got != want {
			t.Errorf("parseAmount(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"a pinch", "1-2", "", "1/0", "x"} {
		if got, ok := parseAmount(in); ok {
			t.Errorf("parseAmount(%q) = %v, want not a number", in, got)
		}
	}
	for _, tc := range []struct {
		in, name, unit string
		qty            any
		note           string
	}{
		{"2 cups flour, sifted", "flour", "cup", 2.0, "sifted"},
		{"1 1/2 tbsp. olive oil", "olive oil", "tbsp", 1.5, ""},
		{"3 cloves of garlic", "garlic", "clove", 3.0, ""},
		{"½ cup milk", "milk", "cup", 0.5, ""},
		{"4 eggs", "eggs", "", 4.0, ""},
		{"1 T butter", "butter", "tbsp", 1.0, ""},
		{"Salt to taste", "Salt to taste", "", nil, ""},
		{"- 8 oz cream cheese", "cream cheese", "oz", 8.0, ""},
	} {
		got := parseLine(tc.in)
		unit := ""
		if got.Unit != nil {
			unit = *got.Unit
		}
		note := ""
		if got.Note != nil {
			note = *got.Note
		}
		if got.Name != tc.name || unit != tc.unit || got.Quantity != tc.qty || note != tc.note {
			t.Errorf("parseLine(%q) = %+v (unit %q note %q)", tc.in, got, unit, note)
		}
	}
}

func TestParseSteps(t *testing.T) {
	got, err := parseSteps("1. Boil water.\n\n2) Add pasta.\nStep 3: Drain.")
	if err != nil || !slices.Equal(got, []string{"Boil water.", "Add pasta.", "Drain."}) {
		t.Errorf("parseSteps = %q, %v", got, err)
	}
	if _, err := parseSteps([]any{"ok", 3}); err == nil {
		t.Error("a number step was accepted")
	}
}

// The vocabulary and matcher tests moved to home-os/allergen with the engine
// (#4891): TestWordForms, TestAllergyMatcher, TestAllergyIntensifiersAndFailClosed,
// TestPreferenceMatchersStayBestEffort, TestCompoundIngredients,
// TestCompoundWordsAreGroupWordsOnly, TestRecipeFindIngredientFilterUnchanged,
// TestAllergenGroupVocabulary, TestAllergenAliasGroups and
// TestAllergyMatchersSplitAndLabel now run against the shared package both apps
// call. TestDietUsesWidenedGroups stays here: dietRules is this package's own.

// TestDietUsesWidenedGroups is the other half of #4804: dietRules reuses
// allergenGroups through the same newAllergyMatcher, so "gluten-free",
// "dairy-free" and "no seafood" inherit the wider vocabulary. Without this a
// celiac's allergy would flag penne while their own diet let it through.
func TestDietUsesWidenedGroups(t *testing.T) {
	for _, tc := range []struct {
		diet, item string
		want       bool
	}{
		{"gluten-free", "penne", true},
		{"gluten-free", "naan", true},
		{"gluten-free", "root beer", false},
		{"gluten free", "puff pastry", true},
		{"dairy free", "mascarpone", true},
		{"dairy-free", "peanut butter", false},
		{"dairy free", "paneer", true},
		{"no seafood", "squid", true},
		{"no seafood", "salmon", true},
		{"no seafood", "chicken", false},
		{"no shellfish", "calamari", true},
		{"no soya", "soybeans", true},
		{"no sesame seeds", "tahini", true},
		{"no gluten", "beer", true},
		{"no gluten", "ginger beer", false},
		// #4805: diets go through compileGroupPhrases too, so the compound
		// forms reach them. A vegan offered catfish is the same bug.
		{"vegan", "catfish fillets", true},
		{"vegetarian", "swordfish steaks", true},
		{"gluten-free", "cornbread", true},
		{"dairy-free", "cheesecake", true},
		{"dairy free", "peanut butter", false},
		{"egg-free", "eggnog", true},
		{"egg free", "eggplant", false},
		{"corn-free", "cornbread", true},
		{"no fish", "catfish fillets", true},
	} {
		rules, ok := dietRules(eater{id: "p", name: "Nora"}, tc.diet)
		if !ok {
			t.Fatalf("diet %q was not applied", tc.diet)
		}
		got := false
		for _, r := range rules {
			if r.m.Matches(tc.item) {
				got = true
			}
		}
		if got != tc.want {
			t.Errorf("diet %q vs %q = %v, want %v", tc.diet, tc.item, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Database tests
// ---------------------------------------------------------------------------

// TestRecipeCreateAndFindByIngredient is the ticket's acceptance test: a
// recipe saved with structured ingredients is stored in 047's shape and is
// found by ingredient (singular or plural), name, tag and time.
func TestRecipeCreateAndFindByIngredient(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)

	e := ts.create.run(t, ctx, tacos)
	d := e.recipe(t)
	if d.Name != "Chicken Tacos" || *d.Servings != 4 || *d.TotalMinutes != 30 || *d.CookMinutes != 30 || d.PrepMinutes != nil {
		t.Errorf("created = %+v", d)
	}
	if !slices.Equal(d.Tags, []string{"dinner", "quick"}) {
		t.Errorf("tags = %v", d.Tags)
	}
	if !slices.Equal(d.Steps, []string{"Season the chicken.", "Grill it.", "Slice thinly.", "Warm the tortillas."}) {
		t.Errorf("steps = %q", d.Steps)
	}
	if len(d.Ingredients) != 6 {
		t.Fatalf("ingredients = %+v", d.Ingredients)
	}
	oil := d.Ingredients[4]
	if oil.Item != "olive oil" || oil.Quantity != 2.0 || *oil.Unit != "tbsp" {
		t.Errorf("parsed line = %+v", oil)
	}
	if d.Ingredients[0].Quantity != 1.5 || *d.Ingredients[0].Unit != "lb" || d.Ingredients[3].Quantity != 0.5 || d.Ingredients[5].Quantity != "a pinch" {
		t.Errorf("quantities = %+v", d.Ingredients)
	}
	if !strings.Contains(e.Summary, "Saved recipe Chicken Tacos") {
		t.Errorf("summary %q", e.Summary)
	}

	// 047 shape: name/quantity/unit/section; the API reads it unchanged.
	var stored string
	if err := pool.QueryRow(context.Background(), `SELECT (ingredients->0)::text FROM recipes WHERE id = $1`, d.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var first map[string]any
	_ = json.Unmarshal([]byte(stored), &first)
	if first["name"] != "chicken thighs" || first["quantity"] != 1.5 || first["unit"] != "lb" {
		t.Errorf("stored ingredient = %s", stored)
	}
	if n := count(t, pool, `SELECT count(*) FROM recipes WHERE id = $1 AND steps = E'Season the chicken.\nGrill it.\nSlice thinly.\nWarm the tortillas.'`, d.ID); n != 1 {
		t.Error("steps not stored one per line")
	}

	ts.create.run(t, ctx, map[string]any{"name": "Veggie Pasta", "ingredients": []any{"1 lb pasta", "2 cups tomato sauce", "1 zucchini"},
		"steps": "Boil pasta.\nAdd sauce.", "tags": "vegetarian, dinner", "prep_minutes": 10, "cook_minutes": 50})
	ts.create.run(t, ctx, map[string]any{"name": "Berry Smoothie", "ingredients": []any{map[string]any{"item": "mixed berries", "quantity": 1, "unit": "cup"}, "1 cup milk"},
		"steps": []any{"Blend."}, "total_minutes": 5})

	for _, tc := range []struct {
		args map[string]any
		want []string
	}{
		{map[string]any{"ingredient": "chicken"}, []string{"Chicken Tacos"}},
		{map[string]any{"ingredient": "tomato"}, []string{"Chicken Tacos", "Veggie Pasta"}},   // tomatoes / tomato sauce
		{map[string]any{"ingredient": "tomatoes"}, []string{"Chicken Tacos", "Veggie Pasta"}}, // plural
		{map[string]any{"ingredient": "tomato, pasta"}, []string{"Veggie Pasta"}},             // all of them
		{map[string]any{"ingredient": "berry"}, []string{"Berry Smoothie"}},
		{map[string]any{"ingredient": "beef"}, nil},
		{map[string]any{"query": "tacos"}, []string{"Chicken Tacos"}},
		{map[string]any{"query": "chicken recipe"}, []string{"Chicken Tacos"}},
		{map[string]any{"query": "pasta"}, []string{"Veggie Pasta"}},
		{map[string]any{"tag": "dinner"}, []string{"Chicken Tacos", "Veggie Pasta"}},
		{map[string]any{"tag": "Vegetarian"}, []string{"Veggie Pasta"}},
		{map[string]any{"max_minutes": 30}, []string{"Berry Smoothie", "Chicken Tacos"}},
		{map[string]any{"max_minutes": "45 minutes", "tag": "dinner"}, []string{"Chicken Tacos"}},
		{map[string]any{"name": "smoothie"}, []string{"Berry Smoothie"}}, // alias
		{map[string]any{"query": "100%"}, nil},                           // no wildcard
		{map[string]any{}, []string{"Berry Smoothie", "Chicken Tacos", "Veggie Pasta"}},
	} {
		items := ts.find.run(t, ctx, tc.args).items(t)
		if got := names(items); !slices.Equal(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
			t.Errorf("recipe_find %v = %v, want %v", tc.args, got, tc.want)
		}
	}
	items := ts.find.run(t, ctx, map[string]any{"ingredient": "tomato"}).items(t)
	if !slices.Equal(items[0].Matched, []string{"tomatoes"}) || items[0].IngredientCount != 6 || *items[0].TotalMinutes != 30 {
		t.Errorf("find item = %+v", items[0])
	}

	// Paging.
	e = ts.find.run(t, ctx, map[string]any{"limit": 2})
	if len(e.items(t)) != 2 || *e.Total != 3 || e.Cursor == "" {
		t.Fatalf("page 1 = %+v", e)
	}
	e = ts.find.run(t, ctx, map[string]any{"limit": 2, "cursor": e.Cursor})
	if got := names(e.items(t)); !slices.Equal(got, []string{"Veggie Pasta"}) || e.Cursor != "" {
		t.Errorf("page 2 = %v / %q", got, e.Cursor)
	}
	ts.find.run(t, ctx, map[string]any{"cursor": "nope"}).wantError(t, "cursor", "")
	ts.find.run(t, ctx, map[string]any{"id": "tacos"}).wantError(t, "id", "")
	if got := names(ts.find.run(t, ctx, map[string]any{"id": d.ID}).items(t)); !slices.Equal(got, []string{"Chicken Tacos"}) {
		t.Errorf("find by id = %v", got)
	}

	// recipe_get by exact name, any case.
	g := ts.get.run(t, ctx, map[string]any{"recipe_id": "chicken tacos"}).recipe(t)
	if g.ID != d.ID || len(g.Ingredients) != 6 {
		t.Errorf("get = %+v", g)
	}
	ts.get.run(t, ctx, map[string]any{"recipe_id": "Taco"}).wantError(t, "recipe_id", "Close matches: Chicken Tacos")
	ts.get.run(t, ctx, map[string]any{"recipe_id": "Lasagna"}).wantError(t, "recipe_id", "No recipe named")
	ts.get.run(t, ctx, map[string]any{}).wantError(t, "recipe_id", "required")

	// Another family can't see it.
	otherCtx, _ := testutil.SeedHousehold(t, pool)
	if items := ts.find.run(t, otherCtx, map[string]any{"ingredient": "chicken"}).items(t); len(items) != 0 {
		t.Errorf("other family found %v", names(items))
	}
	if e := ts.get.run(t, otherCtx, map[string]any{"recipe_id": d.ID}); e.OK {
		t.Error("other family read the recipe")
	}
	_ = hh
}

func TestRecipeCreateValidation(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)
	ok := func() map[string]any {
		return map[string]any{"name": "Toast", "ingredients": []any{"1 slice bread"}, "steps": []any{"Toast it."}}
	}
	with := func(k string, v any) map[string]any { m := ok(); m[k] = v; return m }
	without := func(k string) map[string]any { m := ok(); delete(m, k); return m }

	ts.create.run(t, ctx, without("name")).wantError(t, "name", "required")
	ts.create.run(t, ctx, without("ingredients")).wantError(t, "ingredients", "required")
	ts.create.run(t, ctx, without("steps")).wantError(t, "steps", "required")
	ts.create.run(t, ctx, with("ingredients", []any{})).wantError(t, "ingredients", "empty")
	ts.create.run(t, ctx, with("ingredients", []any{map[string]any{"quantity": 2}})).wantError(t, "ingredients", "no item")
	ts.create.run(t, ctx, with("ingredients", []any{map[string]any{"item": "x", "quantity": -1}})).wantError(t, "ingredients", "negative")
	ts.create.run(t, ctx, with("ingredients", []any{map[string]any{"item": "x", "quantity": true}})).wantError(t, "ingredients", "number or text")
	ts.create.run(t, ctx, with("servings", 0)).wantError(t, "servings", "")
	ts.create.run(t, ctx, with("total_minutes", 2.5)).wantError(t, "total_minutes", "")
	ts.create.run(t, ctx, with("source_url", "javascript:alert(1)")).wantError(t, "source_url", "")
	ts.create.run(t, ctx, with("name", strings.Repeat("x", 256))).wantError(t, "name", "")
	m := ok()
	m["prep_minutes"], m["total_minutes"] = 20, 10
	ts.create.run(t, ctx, m).wantError(t, "total_minutes", "less than prep")
	if n := count(t, pool, `SELECT count(*) FROM recipes`); n != 0 {
		t.Errorf("%d recipes saved by invalid calls", n)
	}

	// Aliases: title, instructions, serves, url.
	d := ts.create.run(t, ctx, map[string]any{"title": "Pancakes", "ingredients": "2 cups flour\n2 eggs\n1 1/2 cups milk",
		"instructions": "Mix.\nFry.", "serves": "4", "url": "https://example.com/pancakes", "description": "Sunday"}).recipe(t)
	if d.Name != "Pancakes" || len(d.Ingredients) != 3 || len(d.Steps) != 2 || *d.Servings != 4 || *d.SourceURL != "https://example.com/pancakes" || *d.Notes != "Sunday" {
		t.Errorf("aliased create = %+v", d)
	}
	if d.Ingredients[2].Item != "milk" || d.Ingredients[2].Quantity != 1.5 || *d.Ingredients[2].Unit != "cup" {
		t.Errorf("pasted line = %+v", d.Ingredients[2])
	}
}

func TestRecipeCreateLoopGuard(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)

	first := ts.create.run(t, ctx, tacos).recipe(t)
	again := ts.create.run(t, ctx, map[string]any{"name": "  chicken tacos ", "ingredients": []any{"chicken"}, "steps": []any{"Cook."}})
	d := again.recipe(t)
	if !d.Duplicate || d.ID != first.ID || !strings.Contains(again.Summary, "already saved") {
		t.Errorf("repeat = %+v / %s", d, again.Summary)
	}
	// Parallel identical calls make one row.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var req mcp.CallToolRequest
			req.Params.Arguments = map[string]any{"name": "Soup", "ingredients": []any{"water"}, "steps": []any{"Boil."}}
			_, _ = ts.create.h(ctx, req)
		}()
	}
	wg.Wait()
	if n := count(t, pool, `SELECT count(*) FROM recipes WHERE title = 'Soup'`); n != 1 {
		t.Errorf("parallel creates made %d rows", n)
	}
	// Older than 10 minutes: a new row, with a hint to use ids.
	if _, err := pool.Exec(context.Background(), `UPDATE recipes SET created_at = now() - interval '11 minutes'`); err != nil {
		t.Fatal(err)
	}
	e := ts.create.run(t, ctx, tacos)
	if d := e.recipe(t); d.Duplicate || d.ID == first.ID || !strings.Contains(strings.Join(e.Next, " "), "pass recipe ids") {
		t.Errorf("after window = %+v / %v", d, e.Next)
	}
	ts.get.run(t, ctx, map[string]any{"recipe_id": "Chicken Tacos"}).wantError(t, "recipe_id", "2 recipes are named")
}

func TestRecipeUpdate(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)
	d := ts.create.run(t, ctx, tacos).recipe(t)
	bg := context.Background()
	var before string
	_ = pool.QueryRow(bg, `SELECT updated_at::text FROM recipes WHERE id = $1`, d.ID).Scan(&before)

	// Only the given fields change.
	e := ts.update.run(t, ctx, map[string]any{"recipe_id": "Chicken Tacos", "servings": 6, "notes": "Kids like it mild", "tags": "", "steps": ""})
	u := e.recipe(t)
	if !slices.Equal(u.Changed, []string{"notes", "servings"}) || *u.Servings != 6 || len(u.Ingredients) != 6 || len(u.Steps) != 4 ||
		!slices.Equal(u.Tags, []string{"dinner", "quick"}) {
		t.Errorf("update = %+v", u)
	}
	// ingredients replace the whole list; total_minutes keeps prep.
	u = ts.update.run(t, ctx, map[string]any{"recipe_id": d.ID, "ingredients": []any{"1 lb ground beef", "8 taco shells"}, "prep_minutes": 10, "total_minutes": 25}).recipe(t)
	if len(u.Ingredients) != 2 || u.Ingredients[0].Item != "ground beef" || *u.PrepMinutes != 10 || *u.CookMinutes != 15 || *u.TotalMinutes != 25 {
		t.Errorf("replace = %+v", u)
	}
	if got := names(ts.find.run(t, ctx, map[string]any{"ingredient": "chicken"}).items(t)); len(got) != 0 {
		t.Errorf("old ingredient still found: %v", got)
	}
	// No-op writes nothing.
	var mid string
	_ = pool.QueryRow(bg, `SELECT updated_at::text FROM recipes WHERE id = $1`, d.ID).Scan(&mid)
	e = ts.update.run(t, ctx, map[string]any{"recipe_id": d.ID, "servings": 6, "ingredients": []any{"1 lb ground beef", "8 taco shells"}})
	if u := e.recipe(t); len(u.Changed) != 0 || !strings.HasPrefix(e.Summary, "Nothing changed") {
		t.Errorf("no-op = %+v / %s", u, e.Summary)
	}
	var after string
	_ = pool.QueryRow(bg, `SELECT updated_at::text FROM recipes WHERE id = $1`, d.ID).Scan(&after)
	if after != mid || mid == before {
		t.Errorf("updated_at before %s mid %s after %s", before, mid, after)
	}
	// null clears; name and ingredients can't be cleared.
	u = ts.update.run(t, ctx, map[string]any{"recipe_id": d.ID, "notes": nil, "tags": nil, "total_minutes": nil, "name": "Beef Tacos"}).recipe(t)
	if u.Notes != nil || len(u.Tags) != 0 || u.TotalMinutes != nil || u.Name != "Beef Tacos" {
		t.Errorf("clear = %+v", u)
	}
	ts.update.run(t, ctx, map[string]any{"recipe_id": d.ID, "name": nil}).wantError(t, "name", "cannot be cleared")
	ts.update.run(t, ctx, map[string]any{"recipe_id": d.ID, "ingredients": nil}).wantError(t, "ingredients", "")
	ts.update.run(t, ctx, map[string]any{"recipe_id": d.ID}).wantError(t, "", "No fields")
	ts.update.run(t, ctx, map[string]any{"recipe_id": uuid.NewString(), "servings": 2}).wantError(t, "", "No recipe with id")
	ts.update.run(t, ctx, map[string]any{"recipe_id": "Nope", "servings": 2}).wantError(t, "recipe_id", "")
}

func TestRecipeRatingsAndAllergies(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)
	bg := context.Background()

	var nora, sam string
	if err := pool.QueryRow(bg, `INSERT INTO people (household_id, display_name, allergies) VALUES ($1, 'Nora', ARRAY['peanuts','Dairy']) RETURNING id::text`, hh.HouseholdID).Scan(&nora); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(bg, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Sam') RETURNING id::text`, hh.HouseholdID).Scan(&sam); err != nil {
		t.Fatal(err)
	}
	e := ts.create.run(t, ctx, map[string]any{"name": "Satay Noodles", "ingredients": []any{"200 g rice noodles", "3 tbsp peanut butter", "1 tbsp butter", "1 lime"},
		"steps": []any{"Cook.", "Toss."}})
	d := e.recipe(t)
	// Three hits now: peanut butter and the name ("Satay" is a peanut synonym),
	// plus butter for the dairy allergy (#4803 checks the title too).
	if len(d.AllergyWarnings) != 3 || !strings.Contains(e.Summary, "Allergy warning: Nora (peanuts): peanut butter") {
		t.Fatalf("warnings = %+v / %s", d.AllergyWarnings, e.Summary)
	}
	for _, w := range d.AllergyWarnings {
		if w.PersonID != nora {
			t.Errorf("warning person = %+v", w)
		}
	}
	ts.create.run(t, ctx, map[string]any{"name": "Plain Rice", "ingredients": []any{"1 cup rice"}, "steps": []any{"Cook."}})

	// Meal history and ratings.
	var past, future string
	if err := pool.QueryRow(bg, `INSERT INTO meal_plans (household_id, date, slot, recipe_id) VALUES ($1, current_date - 7, 'dinner', $2) RETURNING id::text`, hh.HouseholdID, d.ID).Scan(&past); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO meal_plans (household_id, date, slot, recipe_id) VALUES ($1, current_date - 1, 'lunch', $2)`, hh.HouseholdID, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(bg, `INSERT INTO meal_plans (household_id, date, slot, recipe_id) VALUES ($1, current_date + 3, 'dinner', $2) RETURNING id::text`, hh.HouseholdID, d.ID).Scan(&future); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO meal_feedback (meal_plan_id, person_id, rating, comment) VALUES ($1, $2, 5, 'Yum'), ($1, $3, 4, NULL)`, past, sam, nora); err != nil {
		t.Fatal(err)
	}

	g := ts.get.run(t, ctx, map[string]any{"recipe_id": d.ID}).recipe(t)
	if g.Rating == nil || g.Rating.Average != 4.5 || g.Rating.Count != 2 || g.TimesMade != 2 || g.LastMade == nil || g.NextPlanned == nil || len(g.Feedback) != 2 {
		t.Fatalf("get stats = %+v", g)
	}
	if g.Feedback[0].Weekday == "" || g.Feedback[0].Slot != "dinner" {
		t.Errorf("feedback = %+v", g.Feedback)
	}
	items := ts.find.run(t, ctx, map[string]any{"query": "noodles"})
	it := items.items(t)
	if len(it) != 1 || it[0].Rating == nil || it[0].Rating.Average != 4.5 || it[0].TimesMade != 2 ||
		!slices.Contains(it[0].AllergyWarnings, "Nora (peanuts): peanut butter") || !strings.Contains(items.Summary, "allergen") {
		t.Errorf("find = %+v / %s", it, items.Summary)
	}
	if it := ts.find.run(t, ctx, map[string]any{"query": "rice"}).items(t); len(it) != 2 || it[0].Name != "Plain Rice" || it[0].AllergyWarnings != nil {
		t.Errorf("rice find = %+v", it)
	}

	// Delete keeps the meal history's title.
	del := ts.del.run(t, ctx, map[string]any{"recipe_id": "satay noodles"})
	dd := del.recipe(t)
	if *dd.PlannedMealsKept != 3 || !strings.Contains(del.Summary, "3 planned meals keep the name") {
		t.Errorf("delete = %+v / %s", dd, del.Summary)
	}
	if n := count(t, pool, `SELECT count(*) FROM meal_plans WHERE household_id = $1 AND recipe_id IS NULL AND title = 'Satay Noodles'`, hh.HouseholdID); n != 3 {
		t.Errorf("%d meals kept the title", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM meal_feedback WHERE meal_plan_id = $1`, past); n != 2 {
		t.Errorf("feedback lost: %d", n)
	}
	_ = future
}

// TestRecipeWarningsForWordedAllergies is #4802's other acceptance test: an
// allergy saved as a sentence ("allergic to peanuts") must produce the same
// recipe and meal warnings a bare allergen does, and one that normalises to
// nothing must be visible in checks instead of silently matching nothing.
func TestRecipeWarningsForWordedAllergies(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)
	bg := context.Background()

	if _, err := pool.Exec(bg, `INSERT INTO people (household_id, display_name, allergies)
		VALUES ($1, 'Nora', ARRAY['allergic to peanuts']), ($1, 'Sam', ARRAY['Shrimp allergy']), ($1, 'Bea', ARRAY['allergy'])`,
		hh.HouseholdID); err != nil {
		t.Fatal(err)
	}
	d := ts.create.run(t, ctx, map[string]any{"name": "Satay Noodles", "ingredients": []any{"200 g rice noodles", "3 tbsp peanut butter"}, "steps": []any{"Cook."}}).recipe(t)

	// recipe_get: the ingredient matches, and the label stays the bare allergen.
	g := ts.get.run(t, ctx, map[string]any{"recipe_id": d.ID}).recipe(t)
	// The ingredient and the name: "Satay" is a peanut synonym, so the title
	// is a second hit for Nora (#4803).
	if len(g.AllergyWarnings) != 2 || g.AllergyWarnings[0].Allergy != "peanuts" || g.AllergyWarnings[0].Ingredient != "peanut butter" ||
		g.AllergyWarnings[1].Ingredient != "Satay Noodles" {
		t.Errorf("get warnings = %+v", g.AllergyWarnings)
	}
	if !slices.Contains(g.Checks, `Bea has an allergy saved as "allergy" that could not be checked against recipes; confirm the meal is safe.`) {
		t.Errorf("get checks = %v", g.Checks)
	}

	// recipe_find: the same hit, and the summary names the unreadable allergy.
	e := ts.find.run(t, ctx, map[string]any{"query": "satay"})
	it := e.items(t)
	if len(it) != 1 || !slices.Contains(it[0].AllergyWarnings, "Nora (peanuts): peanut butter") ||
		!slices.Contains(it[0].AllergyWarnings, "Nora (peanuts): Satay Noodles") {
		t.Errorf("find = %+v", it)
	}
	if !strings.Contains(e.Summary, "contain an allergen") || !strings.Contains(e.Summary, `Bea has an allergy saved as "allergy"`) {
		t.Errorf("find summary = %s", e.Summary)
	}

	// meal_set: planning it warns, and the plan read back (agenda_get) agrees.
	today := familyToday("UTC")
	date := today.Format(time.DateOnly)
	res := newPlanTools(pool).set.run(t, ctx, map[string]any{"date": date, "slot": "dinner", "recipe_id": d.ID}).set(t)
	if len(res.Meals) != 1 || res.Meals[0].Meal == nil ||
		!slices.Contains(res.Meals[0].Meal.AllergyWarnings, "Nora (peanuts): peanut butter") {
		t.Fatalf("meal_set = %+v", res.Meals)
	}
	if !slices.Contains(res.Checks, `Bea has an allergy saved as "allergy" that could not be checked against recipes; confirm the meal is safe.`) {
		t.Errorf("meal_set checks = %v", res.Checks)
	}
	plan, checks, allowed, err := AgendaMeals(ctx, pool, today, today)
	if err != nil || !allowed || len(plan) != 1 {
		t.Fatalf("agenda = %+v %v %v %v", plan, checks, allowed, err)
	}
	if !slices.Contains(plan[0].AllergyWarnings, "Nora (peanuts): peanut butter") ||
		!slices.Contains(plan[0].AllergyWarnings, "Nora (peanuts): Satay Noodles") || len(checks) != 1 {
		t.Errorf("agenda meal = %+v / checks %v", plan[0], checks)
	}

	// Free text too: "peanut satay" is matched by the normalised allergen.
	res = newPlanTools(pool).set.run(t, ctx, map[string]any{"date": date, "slot": "lunch", "text": "Peanut satay skewers"}).set(t)
	if len(res.Meals) != 1 || !slices.Contains(res.Meals[0].Meal.AllergyWarnings, "Nora (peanuts): Peanut satay skewers") {
		t.Errorf("free text = %+v", res.Meals)
	}

	// A worded allergy that names another food does not warn on this one.
	ts.create.run(t, ctx, map[string]any{"name": "Plain Rice", "ingredients": []any{"1 cup rice"}, "steps": []any{"Cook."}})
	rice := ts.get.run(t, ctx, map[string]any{"recipe_id": "Plain Rice"}).recipe(t)
	if len(rice.AllergyWarnings) != 0 {
		t.Errorf("rice warnings = %+v", rice.AllergyWarnings)
	}
}

func TestRecipeDelete(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)
	d := ts.create.run(t, ctx, tacos).recipe(t)
	bg := context.Background()
	if _, err := pool.Exec(bg, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'recipe', $2, 'Tip', 'More lime')`, hh.HouseholdID, d.ID); err != nil {
		t.Fatal(err)
	}
	ts.del.run(t, ctx, map[string]any{"recipe_id": d.ID}).wantError(t, "confirm", "1 note that would be deleted")
	if n := count(t, pool, `SELECT count(*) FROM recipes WHERE id = $1`, d.ID); n != 1 {
		t.Fatal("refused delete removed the recipe")
	}
	dd := ts.del.run(t, ctx, map[string]any{"recipe_id": d.ID, "confirm": true}).recipe(t)
	if *dd.NotesDeleted != 1 || dd.Name != "Chicken Tacos" {
		t.Errorf("delete = %+v", dd)
	}
	ts.del.run(t, ctx, map[string]any{"recipe_id": d.ID}).wantError(t, "", "No recipe with id")
}

func TestRecipeRoles(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ownerCtx, hh := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)
	bg := context.Background()
	r := ts.create.run(t, ownerCtx, tacos).recipe(t)

	for _, role := range []string{"family_manager", "family_member"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		d := ts.create.run(t, ctx, map[string]any{"name": "Dish of " + role, "ingredients": []any{"rice"}, "steps": []any{"Cook."}}).recipe(t)
		ts.update.run(t, ctx, map[string]any{"recipe_id": d.ID, "notes": role}).recipe(t)
		ts.del.run(t, ctx, map[string]any{"recipe_id": d.ID}).recipe(t)
	}
	for _, role := range []string{"viewer", "housesitter"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		if got := names(ts.find.run(t, ctx, map[string]any{}).items(t)); !slices.Equal(got, []string{"Chicken Tacos"}) {
			t.Errorf("%s find = %v", role, got)
		}
		ts.get.run(t, ctx, map[string]any{"recipe_id": r.ID}).recipe(t)
	}
	for _, role := range []string{"viewer", "housesitter", "vendor"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		for _, c := range []struct {
			w    tool
			args map[string]any
		}{
			{ts.create, map[string]any{"name": "Nope", "ingredients": []any{"x"}, "steps": []any{"x"}}},
			{ts.update, map[string]any{"recipe_id": r.ID, "notes": "nope"}},
			{ts.del, map[string]any{"recipe_id": r.ID}},
		} {
			e := c.w.run(t, ctx, c.args)
			if e.OK || !strings.Contains(e.Summary, "The "+role+" role can't change recipes") {
				t.Errorf("%s %s = %+v", role, c.w.def.Name, e)
			}
		}
	}
	vendorCtx, _ := memberCtx(t, pool, hh, "vendor")
	for _, w := range []tool{ts.find, ts.get} {
		if e := w.run(t, vendorCtx, map[string]any{"recipe_id": r.ID}); e.OK || !strings.Contains(e.Summary, "can't see the family's recipes") {
			t.Errorf("vendor %s = %+v", w.def.Name, e)
		}
	}

	// Demoted: claims say family_member, memberships say viewer.
	ctx, uid := memberCtx(t, pool, hh, "family_member")
	if _, err := pool.Exec(bg, `UPDATE memberships SET role = 'viewer' WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if e := ts.update.run(t, ctx, map[string]any{"recipe_id": r.ID, "notes": "nope"}); e.OK {
		t.Error("demoted member updated a recipe")
	}
	if _, err := pool.Exec(bg, `DELETE FROM memberships WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if e := ts.find.run(t, ctx, map[string]any{}); e.OK || !strings.Contains(e.Summary, "not signed in") {
		t.Errorf("removed member = %+v", e)
	}
	if n := count(t, pool, `SELECT count(*) FROM recipes WHERE id = $1 AND description IS NULL`, r.ID); n != 1 {
		t.Error("refused calls changed the recipe")
	}
	// Writes are attributed to the member (050 authorship via the pool).
	if n := count(t, pool, `SELECT count(*) FROM recipes WHERE id = $1 AND created_by = $2`, r.ID, hh.UserID); n != 1 {
		t.Error("recipe not attributed to its creator")
	}
}
