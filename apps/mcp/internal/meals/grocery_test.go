package meals

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/testutil"
)

func (e env) grocery(t *testing.T) groceryResult {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d groceryResult
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return d
}

func groceryTool(pool *pgxpool.Pool) tool {
	_, d, h := NewMealBuildGroceryListTool(pool)
	return tool{d, h}
}

func itemByText(t *testing.T, r groceryResult, text string) groceryItem {
	t.Helper()
	for _, it := range r.Items {
		if it.Text == text {
			return it
		}
	}
	texts := make([]string, len(r.Items))
	for i, it := range r.Items {
		texts[i] = it.Text
	}
	t.Fatalf("no item %q in %v", text, texts)
	return groceryItem{}
}

func qtyIs(q *float64, want float64) bool { return q != nil && *q == want }

func unitIs(u *string, want string) bool {
	if want == "" {
		return u == nil
	}
	return u != nil && *u == want
}

// ---------------------------------------------------------------------------
// Definition and pure helpers
// ---------------------------------------------------------------------------

func TestGroceryToolDefinition(t *testing.T) {
	d := groceryTool(nil).def
	if d.Name != "meal_build_grocery_list" {
		t.Fatalf("name %q", d.Name)
	}
	if n := utf8.RuneCountInString(d.Description); n == 0 || n > 200 {
		t.Errorf("description has %d chars", n)
	}
	req := slices.Clone(d.InputSchema.Required)
	slices.Sort(req)
	if !slices.Equal(req, []string{"from", "to"}) {
		t.Errorf("required = %v (catalog: from, to)", req)
	}
	for _, p := range []string{"from", "to", "list_id", "slots", "servings", "scale", "skip"} {
		prop, ok := d.InputSchema.Properties[p].(map[string]any)
		if !ok {
			t.Errorf("lacks param %s", p)
			continue
		}
		if desc, _ := prop["description"].(string); desc == "" || utf8.RuneCountInString(desc) > 80 {
			t.Errorf("%s description %q (want 1-80 chars)", p, desc)
		}
	}
	a := d.Annotations
	if a.Title == "" || a.ReadOnlyHint == nil || *a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint ||
		a.IdempotentHint == nil || !*a.IdempotentHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("hints = %+v (class U)", a)
	}
	resolvedSchema(t, d)
}

func TestSectionOf(t *testing.T) {
	for in, want := range map[string]string{
		"onions": "Produce", "Roma tomatoes": "Produce", "red bell pepper": "Produce", "fresh cilantro": "Produce",
		"chicken thighs": "Meat & seafood", "ground beef": "Meat & seafood", "shrimp": "Meat & seafood",
		"chicken broth": "Pantry", "peanut butter": "Pantry", "olive oil": "Pantry", "rice noodles": "Pantry", "coconut milk": "Pantry",
		"butter": "Dairy & eggs", "eggs": "Dairy & eggs", "shredded cheddar": "Dairy & eggs",
		"corn tortillas": "Bakery", "black pepper": "Spices", "pepper": "Spices", "garlic powder": "Spices", "kosher salt": "Spices",
		"frozen peas": "Frozen", "xanthan gum": "Other",
	} {
		if got := sectionOf(in); got != want {
			t.Errorf("sectionOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIngredientMerge(t *testing.T) {
	str := func(s string) *string { return &s }
	tot := &ingredientTotal{}
	tot.add(storedIngredient{Name: "olive oil", Quantity: 2.0, Unit: str("tbsp")}, 1, "a")
	tot.add(storedIngredient{Name: "olive oil", Quantity: "1/2", Unit: str("cups")}, 2, "b")
	tot.add(storedIngredient{Name: "Olive Oil", Quantity: "a drizzle"}, 1, "b")
	if len(tot.amounts) != 2 || !slices.Equal(tot.meals, []string{"a", "b"}) {
		t.Fatalf("amounts = %+v meals = %v", tot.amounts, tot.meals)
	}
	// 2 tbsp + 2 x 1/2 cup = 1.125 cup, shown in the largest unit used.
	if a := tot.amounts[0]; !qtyIs(a.quantity(), 1.125) || a.unit != "cup" || a.String() != "1.125 cup" {
		t.Errorf("volume = %+v %v", a, a.quantity())
	}
	if a := tot.amounts[1]; a.quantity() != nil || a.String() != "a drizzle" {
		t.Errorf("count = %+v", a)
	}
	lb := &ingredientTotal{}
	lb.add(storedIngredient{Name: "beef", Quantity: 8.0, Unit: str("oz")}, 1, "a")
	lb.add(storedIngredient{Name: "beef", Quantity: 1.0, Unit: str("lb")}, 1, "a")
	if a := lb.amounts[0]; !qtyIs(a.quantity(), 1.5) || a.unit != "lb" {
		t.Errorf("weight = %+v", a)
	}
	for in, want := range map[string]string{"Tomatoes": "tomato", "the onions": "onion", "Chicken  Thighs": "chicken thigh", "berries": "berry"} {
		if got := ingredientKey(in); got != want {
			t.Errorf("ingredientKey(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]bool{"water": true, "warm water": true, "coconut water": false, "watercress": false} {
		if got := isTapWater(ingredientKey(in)); got != want {
			t.Errorf("isTapWater(%q) = %v", in, got)
		}
	}
	for in, want := range map[any]float64{2.0: 2, "1.5": 1.5, "2x": 2, "double": 2, "half": 0.5} {
		if got, ok, err := readScale(map[string]any{"scale": in}); err != nil || !ok || got != want {
			t.Errorf("readScale(%v) = %v %v %v", in, got, ok, err)
		}
	}
	for _, in := range []any{0.0, -1.0, 21.0, "lots", true} {
		if _, _, err := readScale(map[string]any{"scale": in}); err == nil {
			t.Errorf("readScale(%v) accepted", in)
		}
	}
}

func TestGroceryRange(t *testing.T) {
	today := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC) // a Wednesday
	for _, tc := range []struct {
		from, to         any
		wantFrom, wantTo string
	}{
		{nil, nil, "2026-11-18", "2026-11-24"},
		{"this week", nil, "2026-11-18", "2026-11-24"},
		{"next week", nil, "2026-11-23", "2026-11-29"},
		{"today", "sunday", "2026-11-18", "2026-11-22"},
		{"next monday", "sunday", "2026-11-23", "2026-11-29"}, // sunday after from
		{"2026-11-20", "2026-11-20", "2026-11-20", "2026-11-20"},
	} {
		args := map[string]any{}
		if tc.from != nil {
			args["from"] = tc.from
		}
		if tc.to != nil {
			args["to"] = tc.to
		}
		f, to, bad := readRange(args, today, time.UTC)
		if bad != nil || f.Format(time.DateOnly) != tc.wantFrom || to.Format(time.DateOnly) != tc.wantTo {
			t.Errorf("readRange(%v, %v) = %s %s %v", tc.from, tc.to, f.Format(time.DateOnly), to.Format(time.DateOnly), bad)
		}
	}
	for _, args := range []map[string]any{
		{"from": "2026-11-20", "to": "2026-11-19"},
		{"from": "today", "to": "2027-01-30"},
		{"from": "someday"},
	} {
		if _, _, bad := readRange(args, today, time.UTC); bad == nil {
			t.Errorf("readRange(%v) accepted", args)
		}
	}
}

// ---------------------------------------------------------------------------
// Database
// ---------------------------------------------------------------------------

// groceryWeek plans a week: tacos twice, chili, a free-text pizza night, a
// lunch, and satay noodles. It returns the date helper.
func groceryWeek(t *testing.T, pool *pgxpool.Pool, ctx context.Context, today time.Time, tacosID, satayID string) func(int) string {
	t.Helper()
	chili := newTools(pool).create.run(t, ctx, map[string]any{
		"name": "Beef Chili", "steps": []any{"Simmer."}, "ingredients": []any{
			map[string]any{"item": "ground beef", "quantity": 1, "unit": "lb"},
			map[string]any{"item": "Tomato", "quantity": 3},
			map[string]any{"item": "olive oil", "quantity": 1, "unit": "tbsp"},
			map[string]any{"item": "onion", "quantity": 1},
			map[string]any{"item": "water", "quantity": 2, "unit": "cups"},
			map[string]any{"item": "salt", "quantity": 1, "unit": "tsp"},
		},
	}).recipe(t)
	d := func(n int) string { return today.AddDate(0, 0, n).Format(time.DateOnly) }
	newPlanTools(pool).set.run(t, ctx, map[string]any{"meals": []any{
		map[string]any{"date": d(0), "slot": "dinner", "recipe_id": tacosID},
		map[string]any{"date": d(1), "slot": "dinner", "recipe_id": chili.ID},
		map[string]any{"date": d(2), "slot": "dinner", "text": "Pizza night"},
		map[string]any{"date": d(3), "slot": "dinner", "recipe_id": tacosID},
		map[string]any{"date": d(4), "slot": "lunch", "recipe_id": chili.ID},
		map[string]any{"date": d(5), "slot": "dinner", "recipe_id": satayID},
	}}).set(t)
	return d
}

// TestMealBuildGroceryListWeek is the acceptance test: a week of planned
// dinners becomes one merged grocery list in one call, and re-running
// duplicates nothing.
func TestMealBuildGroceryListWeek(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh, _, _, tacosR, satay, today := setupFamily(t, pool)
	d := groceryWeek(t, pool, ctx, today, tacosR.ID, satay.ID)
	bg := context.Background()
	// Last week's list was archived (#4713): a new one is started.
	if _, err := pool.Exec(bg, `INSERT INTO lists (household_id, name, kind, archived) VALUES ($1, 'Grocery', 'grocery', true)`, hh.HouseholdID); err != nil {
		t.Fatal(err)
	}
	g := groceryTool(pool)
	week := map[string]any{"from": "today", "to": d(6), "slots": []any{"dinner"}}

	e := g.run(t, ctx, week)
	r := e.grocery(t)
	if r.List == nil || !r.List.Created || r.List.Name != "Grocery" || r.Scaled || r.From != d(0) || r.To != d(6) {
		t.Fatalf("result = %+v", r)
	}
	// Tacos twice + chili + satay, merged.
	for _, c := range []struct {
		text    string
		qty     float64
		unit    string
		section string
		meals   int
	}{
		{"Chicken thighs", 3, "lb", "Meat & seafood", 2},
		{"Corn tortillas", 16, "", "Bakery", 2},
		{"Tomatoes", 7, "", "Produce", 3},
		{"Lime", 1, "", "Produce", 2},
		{"Olive oil", 5, "tbsp", "Pantry", 3},
		{"Ground beef", 1, "lb", "Meat & seafood", 1},
		{"Onion", 1, "", "Produce", 1},
		{"Salt", 1, "tsp", "Spices", 3},
		{"Rice noodles", 200, "g", "Pantry", 1},
		{"Peanut butter", 3, "tbsp", "Pantry", 1},
	} {
		it := itemByText(t, r, c.text)
		if !qtyIs(it.Quantity, c.qty) || !unitIs(it.Unit, c.unit) || it.Section != c.section || len(it.For) != c.meals || it.Status != "added" || it.ItemID == "" {
			t.Errorf("%s = %+v (qty %v)", c.text, it, it.Quantity)
		}
	}
	if salt := itemByText(t, r, "Salt"); salt.Note == nil || *salt.Note != "plus a pinch" {
		t.Errorf("salt note = %v", salt.Note)
	}
	if len(r.Items) != 10 {
		t.Errorf("%d items, want 10 (water is never listed)", len(r.Items))
	}
	if r.Sections[0].Section != "Produce" || !slices.Contains(r.Sections[0].Items, "Tomatoes") {
		t.Errorf("sections = %+v", r.Sections)
	}
	// The lunch is filtered out; pizza night is reported as skipped.
	if len(r.Meals) != 5 || r.Meals[2].Title != "Pizza night" || r.Meals[2].Used || r.Meals[2].Skipped == nil {
		t.Errorf("meals = %+v", r.Meals)
	}
	for _, want := range []string{`Created the grocery list "Grocery".`, "archived list", "10 items added", "from 4 meals", "Produce: Lime (1), Onion (1), Tomatoes (7).",
		"Chicken thighs (3 lb)", "Skipped 1 meal: " + today.AddDate(0, 0, 2).Weekday().String()[:3] + " dinner Pizza night (no recipe)", "per recipe as written"} {
		if !strings.Contains(e.Summary, want) {
			t.Errorf("summary lacks %q: %s", want, e.Summary)
		}
	}
	if next := strings.Join(e.Next, " "); !strings.Contains(next, "list_get list_id="+r.List.ID) || !strings.Contains(next, `"date":"`+d(2)+`"`) {
		t.Errorf("next = %v", e.Next)
	}
	listItems := func() int {
		return count(t, pool, `SELECT count(*) FROM list_items WHERE list_id = $1`, r.List.ID)
	}
	if n := listItems(); n != 10 {
		t.Fatalf("%d list items", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM list_items i JOIN lists l ON l.id = i.list_id WHERE l.household_id = $1 AND l.archived`, hh.HouseholdID); n != 0 {
		t.Errorf("the archived list was written to")
	}

	// Re-running adds nothing.
	again := g.run(t, ctx, week).grocery(t)
	if again.List.ID != r.List.ID || again.List.Created {
		t.Errorf("rerun list = %+v", again.List)
	}
	for _, it := range again.Items {
		if it.Status != "already_on_list" || it.Note != nil && strings.HasPrefix(*it.Note, "the list says") {
			t.Errorf("rerun item = %+v", it)
		}
	}
	if n := listItems(); n != 10 {
		t.Errorf("%d list items after a rerun", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM lists WHERE household_id = $1 AND kind = 'grocery'`, hh.HouseholdID); n != 2 {
		t.Errorf("%d grocery lists", n)
	}

	// Bought tomatoes go back on; a plural typed by hand is the same item.
	if _, err := pool.Exec(bg, `UPDATE list_items SET checked = true, checked_at = now() WHERE list_id = $1 AND text = 'Tomatoes'`, r.List.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `UPDATE list_items SET text = 'onions' WHERE list_id = $1 AND text = 'Onion'`, r.List.ID); err != nil {
		t.Fatal(err)
	}
	third := g.run(t, ctx, week).grocery(t)
	if it := itemByText(t, third, "Tomatoes"); it.Status != "reopened" {
		t.Errorf("tomatoes = %+v", it)
	}
	if it := itemByText(t, third, "Onion"); it.Status != "already_on_list" {
		t.Errorf("onion = %+v", it)
	}
	if n := listItems(); n != 10 {
		t.Errorf("%d list items after reopen", n)
	}

	// Scaled: tacos serve 4, so 8 servings doubles them; chili and satay
	// list no servings and stay as written. Items already open keep their
	// amount and say what the plan needs now.
	sc := g.run(t, ctx, map[string]any{"from": d(0), "to": d(6), "slots": "dinner", "servings": 8, "skip": []any{"salt", "olive oil"}})
	scaled := sc.grocery(t)
	if !scaled.Scaled || scaled.Meals[0].Scale == nil || *scaled.Meals[0].Scale != 2 || scaled.Meals[1].Scale != nil {
		t.Errorf("scaled meals = %+v", scaled.Meals)
	}
	ch := itemByText(t, scaled, "Chicken thighs")
	if !qtyIs(ch.Quantity, 6) || ch.Note == nil || !strings.Contains(*ch.Note, "3 lb; the plan needs 6 lb") {
		t.Errorf("chicken = %+v %v", ch, ch.Note)
	}
	if !slices.Equal(scaled.SkippedStaples, []string{"Olive oil", "Salt"}) && !slices.Equal(scaled.SkippedStaples, []string{"Salt", "Olive oil"}) {
		t.Errorf("staples = %v", scaled.SkippedStaples)
	}
	for _, want := range []string{"2 recipes list no servings", "different amount", "Left off as staples"} {
		if !strings.Contains(sc.Summary, want) {
			t.Errorf("summary lacks %q: %s", want, sc.Summary)
		}
	}
	if n := listItems(); n != 10 {
		t.Errorf("%d list items after a scaled run", n)
	}

	// An explicit list by name, and a range with nothing planned.
	if _, err := pool.Exec(bg, `INSERT INTO lists (household_id, name, kind) VALUES ($1, 'Costco run', 'shopping')`, hh.HouseholdID); err != nil {
		t.Fatal(err)
	}
	costco := g.run(t, ctx, map[string]any{"from": d(5), "to": d(5), "list_id": "costco"}).grocery(t)
	if costco.List == nil || costco.List.Name != "Costco run" || len(costco.Items) != 2 {
		t.Errorf("costco = %+v", costco)
	}
	empty := g.run(t, ctx, map[string]any{"from": d(20), "to": d(26)})
	if er := empty.grocery(t); er.List != nil || len(er.Items) != 0 || !strings.Contains(empty.Summary, "Nothing is planned") {
		t.Errorf("empty = %+v / %s", er, empty.Summary)
	}
	g.run(t, ctx, map[string]any{"from": d(0), "to": d(40)}).wantError(t, "to", "more than 31")
	g.run(t, ctx, map[string]any{"from": d(0), "to": d(6), "slots": []any{"brunch"}}).wantError(t, "slots", "brunch")
	g.run(t, ctx, map[string]any{"from": d(0), "to": d(6), "scale": 2, "servings": 4}).wantError(t, "scale", "not both")
}

func TestMealBuildGroceryListRoles(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ownerCtx, hh, _, _, tacosR, satay, today := setupFamily(t, pool)
	d := groceryWeek(t, pool, ownerCtx, today, tacosR.ID, satay.ID)
	g := groceryTool(pool)
	week := map[string]any{"from": d(0), "to": d(6)}

	// A housesitter may add items, not start a list.
	sitterCtx, _ := memberCtx(t, pool, hh, "housesitter")
	g.run(t, sitterCtx, week).wantError(t, "list_id", "can't start a new list")
	if n := count(t, pool, `SELECT count(*) FROM lists WHERE household_id = $1`, hh.HouseholdID); n != 0 {
		t.Fatalf("a refused call made %d lists", n)
	}
	for _, role := range []string{"viewer", "vendor"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		if e := g.run(t, ctx, week); e.OK || !strings.Contains(e.Summary, "The "+role+" role can't") {
			t.Errorf("%s = %+v", role, e)
		}
	}
	memberCtx2, _ := memberCtx(t, pool, hh, "family_member")
	if r := g.run(t, memberCtx2, week).grocery(t); r.List == nil || !r.List.Created {
		t.Errorf("member = %+v", r)
	}
	if r := g.run(t, sitterCtx, week).grocery(t); r.List == nil || r.List.Created {
		t.Errorf("sitter = %+v", r)
	}
	if n := count(t, pool, `SELECT count(*) FROM lists WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("%d lists", n)
	}
}
