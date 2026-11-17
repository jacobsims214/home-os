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

func (e env) suggest(t *testing.T) mealSuggestResult {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d mealSuggestResult
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return d
}

func suggestTool(pool *pgxpool.Pool) tool {
	_, d, h := NewMealSuggestTool(pool)
	return tool{d, h}
}

func suggested(r mealSuggestResult) []string {
	out := make([]string, len(r.Suggestions))
	for i, s := range r.Suggestions {
		out[i] = s.Name
	}
	return out
}

func excludedNames(r mealSuggestResult) []string {
	out := make([]string, len(r.Excluded))
	for i, s := range r.Excluded {
		out[i] = s.Name
	}
	return out
}

func rankOf(t *testing.T, r mealSuggestResult, name string) int {
	t.Helper()
	for _, s := range r.Suggestions {
		if s.Name == name {
			return s.Rank
		}
	}
	t.Fatalf("%q not suggested: %v", name, suggested(r))
	return 0
}

// kitchen seeds a family and its recipes for the suggestion tests.
type kitchen struct {
	t     *testing.T
	pool  *pgxpool.Pool
	hh    testutil.Household
	ctx   context.Context
	today time.Time
}

func newKitchen(t *testing.T) *kitchen {
	t.Helper()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	var tz string
	if err := pool.QueryRow(context.Background(), `SELECT timezone FROM households WHERE id = $1`, hh.HouseholdID).Scan(&tz); err != nil {
		t.Fatal(err)
	}
	return &kitchen{t: t, pool: pool, hh: hh, ctx: ctx, today: familyToday(tz)}
}

func (k *kitchen) exec(sql string, args ...any) {
	k.t.Helper()
	if _, err := k.pool.Exec(context.Background(), sql, args...); err != nil {
		k.t.Fatalf("%v\n%s", err, sql)
	}
}

func (k *kitchen) person(name string, allergies ...string) string {
	k.t.Helper()
	var id string
	if allergies == nil {
		allergies = []string{}
	}
	if err := k.pool.QueryRow(context.Background(), `INSERT INTO people (household_id, display_name, allergies) VALUES ($1, $2, $3) RETURNING id::text`,
		k.hh.HouseholdID, name, allergies).Scan(&id); err != nil {
		k.t.Fatal(err)
	}
	return id
}

func (k *kitchen) pref(personID, kind, value, notes string) {
	k.t.Helper()
	k.exec(`INSERT INTO person_preferences (household_id, person_id, kind, value, notes) VALUES ($1, $2, $3::person_preference_kind, $4, NULLIF($5, ''))`,
		k.hh.HouseholdID, personID, kind, value, notes)
}

// recipe saves a recipe; minutes 0 means no time saved.
func (k *kitchen) recipe(name string, minutes int, tags []string, ingredients ...string) string {
	k.t.Helper()
	ings := []map[string]any{}
	for _, i := range ingredients {
		ings = append(ings, map[string]any{"name": i, "quantity": nil, "unit": nil, "section": nil})
	}
	raw, _ := json.Marshal(ings)
	var cook *int
	if minutes > 0 {
		cook = &minutes
	}
	if tags == nil {
		tags = []string{}
	}
	var id string
	if err := k.pool.QueryRow(context.Background(), `INSERT INTO recipes (household_id, title, cook_minutes, tags, ingredients) VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
		k.hh.HouseholdID, name, cook, tags, raw).Scan(&id); err != nil {
		k.t.Fatal(err)
	}
	return id
}

// ate plans recipe (or free text when recipeID is "") offset days from
// today and rates it per person.
func (k *kitchen) ate(offset int, slot, recipeID, title string, ratings map[string]int) {
	k.t.Helper()
	var rid *string
	if recipeID != "" {
		rid = &recipeID
	}
	var mealID string
	if err := k.pool.QueryRow(context.Background(), `INSERT INTO meal_plans (household_id, date, slot, recipe_id, title) VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
		k.hh.HouseholdID, k.today.AddDate(0, 0, offset).Format(time.DateOnly), slot, rid, title).Scan(&mealID); err != nil {
		k.t.Fatal(err)
	}
	for p, r := range ratings {
		k.exec(`INSERT INTO meal_feedback (household_id, meal_plan_id, person_id, rating) VALUES ($1, $2, $3, $4)`, k.hh.HouseholdID, mealID, p, r)
	}
}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

func TestMealSuggestDefinition(t *testing.T) {
	d := suggestTool(nil).def
	if d.Name != "meal_suggest" {
		t.Fatalf("name %q", d.Name)
	}
	if n := utf8.RuneCountInString(d.Description); n == 0 || n > 200 {
		t.Errorf("description has %d chars", n)
	}
	if len(d.InputSchema.Required) != 0 {
		t.Errorf("required = %v, want none", d.InputSchema.Required)
	}
	for _, p := range []string{"date", "slot", "person_ids", "max_minutes", "tag", "count"} {
		prop, ok := d.InputSchema.Properties[p].(map[string]any)
		if !ok {
			t.Errorf("lacks param %s", p)
			continue
		}
		if desc, _ := prop["description"].(string); desc == "" || utf8.RuneCountInString(desc) > 80 {
			t.Errorf("%s description %q", p, desc)
		}
	}
	a := d.Annotations
	if a.Title == "" || a.ReadOnlyHint == nil || !*a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint ||
		a.IdempotentHint == nil || !*a.IdempotentHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("hints = %+v", a)
	}
	resolvedSchema(t, d)
}

func TestAllergensIn(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"peanut allergy", []string{"peanut"}},
		{"Severe peanut allergy (carries an EpiPen)", []string{"peanut"}},
		{"allergic to shrimp and crab", []string{"shrimp", "crab"}},
		{"nut-free (allergy)", []string{"nut"}},
		{"Tree nut allergies", []string{"tree nut"}},
		{"lactose intolerant", []string{"lactose"}},
		{"coeliac", []string{"celiac"}},
		{"allergy", nil},
		{"anaphylactic", nil},
		// #4802: people.allergies entries are read with this same
		// normaliser, so every wording below has to give an allergen.
		{"Shrimp allergy", []string{"shrimp"}},
		{"strawberry allergy", []string{"strawberry"}},
		{"Severe peanut allergy", []string{"peanut"}},
		{"allergic to peanuts", []string{"peanuts"}},
		{"Peanuts - anaphylaxis", []string{"peanuts"}},
		{"peanuts \u2013 anaphylaxis", []string{"peanuts"}},
		{"peanuts: EpiPen", []string{"peanuts"}},
		{"peanuts and tree nuts", []string{"peanuts", "tree nuts"}},
		{"peanuts, tree nuts; sesame", []string{"peanuts", "tree nuts", "sesame"}},
		{"shellfish (hives) - carries an EpiPen", []string{"shellfish"}},
		{"  Peanuts  ", []string{"peanuts"}},
		{"(carries an EpiPen)", nil},
		{"severe", nil},
	} {
		if got := allergensIn(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("allergensIn(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDietRules(t *testing.T) {
	e := eater{id: "p", name: "Sam"}
	for _, tc := range []struct {
		diet, item string
		want       bool
	}{
		{"vegetarian", "chicken thighs", true},
		{"Vegetarian", "salmon fillet", true},
		{"vegetarian", "chickpeas", false},
		{"vegan", "cheddar", true},
		{"vegan", "eggs", true},
		{"vegan", "eggplant", false},
		{"vegan", "honey", true},
		{"pescatarian", "salmon", false},
		{"pescatarian", "bacon", true},
		{"no pork", "bacon", true},
		{"no pork", "chicken", false},
		{"gluten-free", "spaghetti", true},
		{"dairy free", "butter", true},
		{"dairy free", "peanut butter", false},
		{"halal", "ham", true},
		{"no red meat", "steak", true},
		{"no red meat", "chicken", false},
	} {
		rs, ok := dietRules(e, tc.diet)
		if !ok {
			t.Fatalf("diet %q not applied", tc.diet)
		}
		got := slices.ContainsFunc(rs, func(r rule) bool { return r.m.Matches(tc.item) })
		if got != tc.want {
			t.Errorf("diet %q vs %q = %v, want %v", tc.diet, tc.item, got, tc.want)
		}
	}
	if _, ok := dietRules(e, "low carb"); ok {
		t.Error("low carb should not be applied (it is reported in checks)")
	}
}

// ---------------------------------------------------------------------------
// Database tests
// ---------------------------------------------------------------------------

// TestMealSuggestNeverSuggestsAllergens is the acceptance test: a recipe
// with an allergen of anyone eating is never suggested, whether the allergy
// is on people.allergies (with a stored note) or filed as a diet, a dislike
// or a note; uncertain cases are flagged, not hidden.
func TestMealSuggestNeverSuggestsAllergens(t *testing.T) {
	t.Parallel()
	k := newKitchen(t)
	nora := k.person("Nora", "peanuts (carries an EpiPen)")
	sam := k.person("Sam")
	leo := k.person("Leo Smith")
	k.pref(sam, "dietary", "Severe shellfish allergy", "")
	k.pref(leo, "other", "tree nuts", "allergic, hives")
	k.pref(leo, "dislike", "sesame", "allergy")
	k.pref(sam, "dietary", "low carb", "")
	_ = nora

	k.recipe("Satay Noodles", 25, nil, "rice noodles", "peanut butter", "lime")
	k.recipe("Shrimp Scampi", 20, nil, "shrimp", "linguine", "garlic")
	k.recipe("Pesto Pasta", 15, nil, "pasta", "basil", "pine nuts", "parmesan")
	k.recipe("Sesame Chicken", 30, nil, "chicken", "tahini", "honey")
	k.recipe("Peanut Brittle", 40, nil) // no ingredients: the name still counts
	k.recipe("Chicken Tacos", 30, []string{"quick"}, "chicken thighs", "corn tortillas", "tomatoes")
	k.recipe("Veggie Curry", 45, []string{"vegetarian"}, "chickpeas", "coconut milk", "spinach")
	k.recipe("Mystery Casserole", 60, nil)

	st := suggestTool(k.pool)
	r := st.run(t, k.ctx, map[string]any{"count": 10}).suggest(t)
	got := suggested(r)
	// Mystery Casserole has no ingredients saved. Someone eating has an
	// allergy, so it is left out too: an unknown recipe is not a safe recipe
	// (manager decision on #4803), and the reason is the shared line.
	for _, bad := range []string{"Satay Noodles", "Shrimp Scampi", "Pesto Pasta", "Sesame Chicken", "Peanut Brittle", "Mystery Casserole"} {
		if slices.Contains(got, bad) {
			t.Errorf("suggested %s, which has an allergen: %v", bad, got)
		}
		if !slices.Contains(excludedNames(r), bad) {
			t.Errorf("%s not listed in excluded: %+v", bad, r.Excluded)
		}
	}
	if !slices.Equal(got, []string{"Chicken Tacos", "Veggie Curry"}) {
		t.Errorf("suggestions = %v", got)
	}
	if r.ExcludedTotal != 6 || len(r.Eating) != 4 {
		t.Errorf("excluded_total %d, eating %v", r.ExcludedTotal, r.Eating)
	}
	for _, e := range r.Excluded {
		if e.Name == "Satay Noodles" && e.Reason != "Nora (peanuts): peanut butter" {
			t.Errorf("satay reason %q", e.Reason)
		}
		if e.Name == "Shrimp Scampi" && !strings.Contains(e.Reason, "Sam (shellfish): shrimp") {
			t.Errorf("scampi reason %q", e.Reason)
		}
	}
	// No ingredients saved with an allergy in the family: excluded, not hidden.
	for _, e := range r.Excluded {
		if e.Name == "Mystery Casserole" && e.Reason != noIngredientsLine {
			t.Errorf("mystery casserole reason %q, want %q", e.Reason, noIngredientsLine)
		}
	}
	if !slices.ContainsFunc(r.Checks, func(c string) bool { return strings.Contains(c, `"low carb" was not applied`) }) {
		t.Errorf("checks = %v", r.Checks)
	}
	e := st.run(t, k.ctx, map[string]any{})
	if !strings.Contains(e.Summary, "Left out for allergies or diets") || !strings.Contains(e.Summary, noIngredientsLine) {
		t.Errorf("summary = %s", e.Summary)
	}

	// Only Sam eats: Nora's peanuts are not a hard rule, but they are flagged.
	r = st.run(t, k.ctx, map[string]any{"person_ids": []any{"sam"}, "count": 10}).suggest(t)
	if !slices.Contains(suggested(r), "Satay Noodles") || slices.Contains(suggested(r), "Shrimp Scampi") {
		t.Fatalf("Sam only: %v", suggested(r))
	}
	for _, s := range r.Suggestions {
		if s.Name == "Satay Noodles" && !slices.ContainsFunc(s.Cautions, func(c string) bool { return strings.Contains(c, "for Nora (peanuts), who is not eating") }) {
			t.Errorf("satay cautions = %v", s.Cautions)
		}
	}
	if !slices.ContainsFunc(r.Checks, func(c string) bool { return strings.Contains(c, "Only the allergies of the people eating") }) {
		t.Errorf("checks = %v", r.Checks)
	}
	// First name and "and" in one string.
	r = st.run(t, k.ctx, map[string]any{"person_ids": "Leo and Nora"}).suggest(t)
	if !slices.Equal(r.Eating, []string{"Leo Smith", "Nora"}) {
		t.Errorf("eating = %v", r.Eating)
	}
	for _, bad := range []string{"Satay Noodles", "Pesto Pasta", "Sesame Chicken"} {
		if slices.Contains(suggested(r), bad) {
			t.Errorf("Leo and Nora: suggested %s", bad)
		}
	}
	st.run(t, k.ctx, map[string]any{"person_ids": []any{"Zed"}}).wantError(t, "person_ids", "No person")
}

// TestMealSuggestWordedAllergies is #4802's acceptance test: people.allergies
// entries are free text ("allergic to peanuts", "Shrimp allergy", "Peanuts -
// anaphylaxis", "peanuts and tree nuts"), and they must exclude and warn like
// a bare allergen does. Before this, every one of them matched no ingredient
// and returned ok, so the exclusion silently did nothing.
func TestMealSuggestWordedAllergies(t *testing.T) {
	t.Parallel()
	k := newKitchen(t)
	k.person("Nora", "allergic to peanuts")
	k.person("Sam", "Shrimp allergy")
	k.person("Leo", "Peanuts - anaphylaxis")
	k.person("Ada", "peanuts and tree nuts")
	k.person("Kit", "peanuts: EpiPen")
	k.person("Bea", "allergy") // normalises to nothing: must surface in checks

	k.recipe("Satay Bowl", 25, nil, "rice noodles", "peanuts", "lime")
	k.recipe("Garlic Shrimp", 20, nil, "shrimp", "linguine", "garlic")
	k.recipe("Cashew Curry", 35, nil, "cashews", "coconut milk", "spinach")
	k.recipe("Peanut Brittle", 40, nil) // no ingredients: the name still counts
	k.recipe("Chicken Tacos", 30, nil, "chicken thighs", "corn tortillas")

	st := suggestTool(k.pool)
	r := st.run(t, k.ctx, map[string]any{"count": 10}).suggest(t)
	got := suggested(r)
	for _, bad := range []string{"Satay Bowl", "Garlic Shrimp", "Cashew Curry", "Peanut Brittle"} {
		if slices.Contains(got, bad) {
			t.Errorf("suggested %s, which has an allergen: %v", bad, got)
		}
		if !slices.Contains(excludedNames(r), bad) {
			t.Errorf("%s not listed in excluded: %+v", bad, r.Excluded)
		}
	}
	if !slices.Equal(got, []string{"Chicken Tacos"}) {
		t.Errorf("suggestions = %v", got)
	}
	// The label in the reason stays the bare allergen, not the raw text.
	reasons := map[string]string{}
	for _, e := range r.Excluded {
		reasons[e.Name] = e.Reason
	}
	for name, want := range map[string]string{
		"Satay Bowl":     "(peanuts): peanuts",
		"Garlic Shrimp":  "(shrimp): shrimp",
		"Cashew Curry":   "(tree nuts): cashews",
		"Peanut Brittle": "(peanuts): Peanut Brittle",
	} {
		if !strings.Contains(reasons[name], want) {
			t.Errorf("%s reason %q lacks %q", name, reasons[name], want)
		}
	}
	// An allergy that normalises to nothing is visible, not silent.
	if !slices.ContainsFunc(r.Checks, func(c string) bool {
		return strings.Contains(c, `Bea has an allergy saved as "allergy" that could not be checked`)
	}) {
		t.Errorf("checks = %v", r.Checks)
	}
	if e := st.run(t, k.ctx, map[string]any{}); !strings.Contains(e.Summary, `Bea has an allergy saved as "allergy"`) {
		t.Errorf("summary lacks the unchecked allergy: %s", e.Summary)
	}

	// One entry can mean several allergens: Ada's "peanuts and tree nuts".
	r = st.run(t, k.ctx, map[string]any{"person_ids": []any{"Ada"}, "count": 10}).suggest(t)
	for _, bad := range []string{"Satay Bowl", "Cashew Curry"} {
		if slices.Contains(suggested(r), bad) {
			t.Errorf("Ada: suggested %s", bad)
		}
	}
	if !slices.Contains(suggested(r), "Garlic Shrimp") {
		t.Errorf("Ada has no shrimp allergy: %v", suggested(r))
	}
}

// TestMealSuggestRanking: recipes eaten in the last 7 days rank lower,
// highly rated ones higher, and the eaters' own ratings count.
func TestMealSuggestRanking(t *testing.T) {
	t.Parallel()
	k := newKitchen(t)
	nora := k.person("Nora")
	sam := k.person("Sam")
	owner := ""
	if err := k.pool.QueryRow(context.Background(), `SELECT id::text FROM people WHERE household_id = $1 AND user_id = $2`, k.hh.HouseholdID, k.hh.UserID).Scan(&owner); err != nil {
		t.Fatal(err)
	}

	lasagne := k.recipe("Lasagne", 60, nil, "lasagne sheets", "beef mince", "tomatoes")
	tacos := k.recipe("Chicken Tacos", 30, []string{"quick"}, "chicken thighs", "corn tortillas")
	soup := k.recipe("Cabbage Soup", 40, nil, "cabbage", "onion")
	k.recipe("Veggie Curry", 45, []string{"vegetarian"}, "chickpeas", "spinach")
	fish := k.recipe("Fish Pie", 50, nil, "cod", "potatoes")
	k.recipe("Mushroom Risotto", 35, nil, "arborio rice", "mushrooms")

	all5 := map[string]int{nora: 5, sam: 5, owner: 5}
	k.ate(-2, "dinner", lasagne, "Lasagne", all5)                                 // best rated, but eaten 2 days ago
	k.ate(-30, "dinner", tacos, "Chicken Tacos", map[string]int{nora: 4, sam: 5}) // well rated, a month ago
	k.ate(-40, "dinner", soup, "Cabbage Soup", map[string]int{nora: 2, sam: 1})   // poorly rated
	k.ate(-20, "dinner", "", "veggie curry", map[string]int{nora: 5, sam: 1})     // free text counts as the recipe
	k.ate(3, "dinner", fish, "Fish Pie", nil)                                     // already planned this week

	st := suggestTool(k.pool)
	r := st.run(t, k.ctx, map[string]any{"count": 10}).suggest(t)
	t.Logf("ranking: %v", suggested(r))
	if r.Suggestions[0].Name != "Chicken Tacos" {
		t.Errorf("first = %s, want Chicken Tacos (%v)", r.Suggestions[0].Name, suggested(r))
	}
	if rankOf(t, r, "Lasagne") <= rankOf(t, r, "Mushroom Risotto") {
		t.Errorf("Lasagne (eaten 2 days ago) should rank below an untried recipe: %v", suggested(r))
	}
	if rankOf(t, r, "Chicken Tacos") >= rankOf(t, r, "Cabbage Soup") {
		t.Errorf("well rated should beat poorly rated: %v", suggested(r))
	}
	if rankOf(t, r, "Fish Pie") <= rankOf(t, r, "Mushroom Risotto") {
		t.Errorf("Fish Pie (planned in 3 days) should rank lower: %v", suggested(r))
	}
	for _, s := range r.Suggestions {
		switch s.Name {
		case "Lasagne":
			if !strings.Contains(s.Reason, "had 2 days before") || s.LastMade == nil || s.TimesMade != 1 {
				t.Errorf("lasagne = %+v", s)
			}
		case "Chicken Tacos":
			if !strings.Contains(s.Reason, "Rated 4.5/5 by Nora and Sam") || s.Rating == nil || s.Rating.Average != 4.5 || s.Rating.Count != 2 {
				t.Errorf("tacos = %+v", s)
			}
		case "Fish Pie":
			if !strings.Contains(s.Reason, "already planned for") {
				t.Errorf("fish pie = %+v", s)
			}
		case "Mushroom Risotto":
			if !strings.Contains(s.Reason, "not made yet") {
				t.Errorf("risotto = %+v", s)
			}
		}
	}

	// Per person: the curry is Nora's 5 and Sam's 1.
	rn := st.run(t, k.ctx, map[string]any{"person_ids": []any{"Nora"}, "count": 10}).suggest(t)
	rs := st.run(t, k.ctx, map[string]any{"person_ids": []any{"Sam"}, "count": 10}).suggest(t)
	if rankOf(t, rn, "Veggie Curry") >= rankOf(t, rs, "Veggie Curry") {
		t.Errorf("curry: Nora rank %d, Sam rank %d", rankOf(t, rn, "Veggie Curry"), rankOf(t, rs, "Veggie Curry"))
	}

	// Asking about a day weeks out: the lasagne is no longer recent but a favourite.
	later := st.run(t, k.ctx, map[string]any{"date": k.today.AddDate(0, 0, 25).Format(time.DateOnly), "count": 10}).suggest(t)
	if rankOf(t, later, "Lasagne") != 1 {
		t.Errorf("25 days on, Lasagne should lead: %v", suggested(later))
	}

	// Likes lift, dislikes sink.
	k.pref(sam, "dislike", "chicken", "")
	k.pref(nora, "like", "mushrooms", "")
	r = st.run(t, k.ctx, map[string]any{"count": 10}).suggest(t)
	if r.Suggestions[0].Name != "Mushroom Risotto" || !strings.Contains(r.Suggestions[0].Reason, "Nora likes mushrooms") {
		t.Errorf("first = %+v", r.Suggestions[0])
	}
	for _, s := range r.Suggestions {
		if s.Name == "Chicken Tacos" && !strings.Contains(s.Reason, "Sam dislikes chicken") {
			t.Errorf("tacos reason %q", s.Reason)
		}
	}
	// One-call answer: summary names the ideas and the hint is a meal_set template.
	e := st.run(t, k.ctx, map[string]any{"slot": "supper"})
	if !strings.Contains(e.Summary, "1. Mushroom Risotto (35 min)") || len(e.Next) == 0 ||
		!strings.Contains(e.Next[0], `meal_set meals=[{"date":"`+k.today.Format(time.DateOnly)+`","slot":"dinner","recipe_id":"…"}]`) {
		t.Errorf("summary %q next %v", e.Summary, e.Next)
	}
}

// TestMealSuggestConstraints: max_minutes, tag, diets, already planned,
// date words, count and bad input.
func TestMealSuggestConstraints(t *testing.T) {
	t.Parallel()
	k := newKitchen(t)
	sam := k.person("Sam")
	k.pref(sam, "dietary", "vegetarian", "")
	k.recipe("Chicken Tacos", 30, []string{"quick"}, "chicken thighs", "corn tortillas")
	k.recipe("Veggie Curry", 45, []string{"vegetarian"}, "chickpeas", "spinach")
	k.recipe("Bean Chili", 90, []string{"vegetarian"}, "kidney beans", "tomatoes")
	k.recipe("Toast", 0, nil, "bread")
	k.ate(1, "dinner", "", "Pizza night", nil)

	st := suggestTool(k.pool)
	r := st.run(t, k.ctx, map[string]any{"max_minutes": "60 minutes", "date": "tomorrow"}).suggest(t)
	if !slices.Equal(suggested(r), []string{"Veggie Curry"}) || r.FilteredOut != 2 {
		t.Errorf("max 60 = %v filtered %d", suggested(r), r.FilteredOut)
	}
	if !slices.Contains(excludedNames(r), "Chicken Tacos") || !strings.Contains(r.Excluded[0].Reason, "Sam (diet: vegetarian): chicken thighs") {
		t.Errorf("excluded = %+v", r.Excluded)
	}
	if r.AlreadyPlanned == nil || *r.AlreadyPlanned != "Pizza night" || r.LocalDate != k.today.AddDate(0, 0, 1).Format(time.DateOnly) {
		t.Errorf("already planned %v on %s", r.AlreadyPlanned, r.LocalDate)
	}
	r = st.run(t, k.ctx, map[string]any{"tags": "Vegetarian", "count": 1}).suggest(t)
	if len(r.Suggestions) != 1 || r.FilteredOut != 2 {
		t.Errorf("tag = %v filtered %d", suggested(r), r.FilteredOut)
	}
	e := st.run(t, k.ctx, map[string]any{"max_minutes": 5})
	if s := e.suggest(t); len(s.Suggestions) != 0 || !strings.Contains(e.Summary, "No recipe fits") ||
		!slices.ContainsFunc(e.Next, func(n string) bool { return strings.Contains(n, "without max_minutes") }) {
		t.Errorf("nothing fits = %s %v", e.Summary, e.Next)
	}
	st.run(t, k.ctx, map[string]any{"slot": "elevenses"}).wantError(t, "slot", "not a meal slot")
	st.run(t, k.ctx, map[string]any{"date": "someday"}).wantError(t, "date", "not a date")
	st.run(t, k.ctx, map[string]any{"count": 50}).wantError(t, "count", "")

	// An empty kitchen points at recipe_create.
	k2 := newKitchen(t)
	e = suggestTool(k2.pool).run(t, k2.ctx, map[string]any{})
	if !strings.Contains(e.Summary, "No saved recipes") || !slices.ContainsFunc(e.Next, func(n string) bool { return strings.Contains(n, "recipe_create") }) {
		t.Errorf("empty = %s %v", e.Summary, e.Next)
	}
}

// TestMealSuggestRoles: housesitters get allergy-safe suggestions without
// likes, dislikes or diets and without a meal_set hint; vendors get nothing.
func TestMealSuggestRoles(t *testing.T) {
	t.Parallel()
	k := newKitchen(t)
	nora := k.person("Nora", "peanuts")
	sam := k.person("Sam")
	k.pref(sam, "dislike", "chicken", "")
	k.pref(sam, "dietary", "shellfish allergy", "")
	k.pref(nora, "dietary", "vegetarian", "")
	k.recipe("Satay Noodles", 25, nil, "peanut butter")
	k.recipe("Prawn Curry", 25, nil, "prawns")
	k.recipe("Chicken Tacos", 30, nil, "chicken thighs")

	st := suggestTool(k.pool)
	for _, role := range []string{"family_member", "viewer"} {
		ctx, _ := memberCtx(t, k.pool, k.hh, role)
		r := st.run(t, ctx, map[string]any{}).suggest(t)
		if len(r.Suggestions) != 0 || r.ExcludedTotal != 3 || r.Limited {
			t.Errorf("%s = %v excluded %+v", role, suggested(r), r.Excluded)
		}
	}
	sitter, _ := memberCtx(t, k.pool, k.hh, "housesitter")
	e := st.run(t, sitter, map[string]any{})
	r := e.suggest(t)
	if !r.Limited || !slices.Equal(suggested(r), []string{"Chicken Tacos"}) || !slices.Equal(excludedNames(r), []string{"Prawn Curry", "Satay Noodles"}) {
		t.Errorf("housesitter = %+v", r)
	}
	if strings.Contains(e.Summary, "Sam dislikes") || strings.Contains(e.Summary, "vegetarian") {
		t.Errorf("housesitter sees preferences: %s", e.Summary)
	}
	if slices.ContainsFunc(e.Next, func(n string) bool { return strings.Contains(n, "meal_set") }) {
		t.Errorf("housesitter hinted meal_set: %v", e.Next)
	}
	vendor, _ := memberCtx(t, k.pool, k.hh, "vendor")
	if e := st.run(t, vendor, map[string]any{}); e.OK || !strings.Contains(e.Summary, "vendor role can't see") {
		t.Errorf("vendor = %+v", e)
	}
	// "me" is the caller's own person.
	member, uid := memberCtx(t, k.pool, k.hh, "family_member")
	r = st.run(t, member, map[string]any{"person_ids": "me"}).suggest(t)
	var name string
	if err := k.pool.QueryRow(context.Background(), `SELECT display_name FROM people WHERE user_id = $1`, uid).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Eating, []string{name}) {
		t.Errorf("me = %v, want %s", r.Eating, name)
	}
}

// wholeWordMatcher builds the matcher exactly as it was BEFORE #4805: group
// words compiled with compilePhrases, so every word is anchored \b...\b and a
// group word inside a one-word ingredient cannot be seen. newAllergyMatcher
// differs from this by one line (compileGroupPhrases), which is what makes it a
// faithful oracle for "what happens if compound matching is reverted".
//
// It exists for the fixture guard in TestMealSuggestCompoundIngredients: a case
// whose ingredient (or title) also matches this matcher would be excluded by a
// whole-word match alone, so it would pass with the compound support reverted
// and would not be guarding #4805 at all. That is exactly how this test was
// neutered before #4875 — its ingredients were "catfish fillets", "cornmeal"
// and "cream cheese", every one of which carries a literal group member.
func wholeWordMatcher(personID, person, allergen string) allergyMatcher {
	m := allergyMatcher{PersonID: personID, Person: person, Allergy: allergen}
	m.Own = compilePhrases([]string{allergen})
	for _, key := range allergyGroupKeys(allergen) {
		m.Group = append(m.Group, compilePhrases(allergenGroups[key])...)
		m.Except = append(m.Except, compilePhrases(allergenExceptions[key])...)
	}
	return m
}

// TestMealSuggestCompoundIngredients is #4805's end-to-end case on real
// Postgres: a recipe whose ONLY route to an eater's allergen is a compound word
// (whitefish, shortbread, cheesecake, cornflakes, eggnog) must be excluded, not
// suggested. Before #4805 every group word was wrapped in \b...\b, so none of
// those were seen and the recipe was suggested with no warning — a live
// food-safety control failing open.
//
// The cases are chosen so that reverting newAllergyMatcher's one
// compileGroupPhrases line fails this test, and the guard asserts that: for
// every case, no ingredient and not the title may match wholeWordMatcher, the
// pre-#4805 matcher. Without that guard the fixtures can quietly decay into
// whole-word matches, which is what happened here (#4875): the original
// ingredients were "catfish fillets", "cornmeal" and "cream cheese", all
// literal group members, so the test stayed green with compound matching
// reverted. If a future vocabulary widening makes a case fail the guard, pick a
// different compound; do not delete the guard.
func TestMealSuggestCompoundIngredients(t *testing.T) {
	t.Parallel()

	type compoundCase struct {
		recipe      string
		ingredients []string
		allergy     string // the allergen text wholeWordMatcher is built for
		reason      string // the exclusion reason substring: person, allergen, matched text
	}
	cases := []compoundCase{
		// suffix: fish -> whitefish. "fish" is a group word; "whitefish" is not.
		{"Friday Tray Bake", []string{"whitefish fillets", "lemon", "potatoes"}, "fish", "Nora (fish): whitefish fillets"},
		// suffix + prefix: bread -> shortbread (the tail) and breaded (the head).
		{"Tea Time Treat", []string{"shortbread", "jam"}, "gluten", "Sam (gluten): shortbread"},
		{"Crispy Chicken", []string{"breaded chicken cutlets", "rice"}, "gluten", "Sam (gluten): breaded chicken cutlets"},
		// prefix: cheese -> cheesecake, and the recipe title as the only compound.
		{"Birthday Dessert", []string{"cheesecake", "strawberries", "sugar"}, "dairy", "Ada (dairy): cheesecake"},
		{"Cheeseburger Night", []string{"vegetarian patty", "pickles", "onions"}, "dairy", "Ada (dairy): Cheeseburger Night"},
		// prefix: corn -> cornflakes (the corn group's own word is "corn").
		{"Breakfast Cereal", []string{"cornflakes", "banana", "oat drink"}, "corn", "Leo (corn): cornflakes"},
		// prefix: egg -> eggnog. eggplant stays a negative below.
		{"Holiday Punch", []string{"eggnog", "cinnamon", "cloves"}, "egg", "Ivy (egg): eggnog"},
	}

	k := newKitchen(t)
	k.person("Nora", "fish allergy")
	k.person("Sam", "gluten")
	k.person("Ada", "dairy")
	k.person("Ivy", "egg")
	k.pref(k.person("Leo"), "dietary", "corn allergy", "")
	for _, c := range cases {
		k.recipe(c.recipe, 30, nil, c.ingredients...)
	}
	// The negatives, in the same catalogue: each carries a word that looks like
	// an allergen but is not one (allergenExceptions), so a test that excluded
	// everything would not pass either. They are also the control that proves the
	// catalogue was actually searched — seven positives plus these three makes ten
	// recipes, and count is capped at ten, so nothing is hidden by the limit and a
	// suggestion list that came back empty cannot pass this test.
	k.recipe("Aubergine Bake", 40, nil, "eggplant", "tomatoes", "olive oil")          // egg prefix, excepted
	k.recipe("Breadfruit Curry", 30, nil, "breadfruit", "coconut milk", "rice")       // bread prefix, excepted
	k.recipe("Carrot Ribbons", 15, nil, "carrots", "butterfly cut zucchini", "pesto") // butter prefix, excepted

	// Fixture guard, before any DB work: every positive case must be reachable
	// ONLY through compound matching. If one is not, this fails loudly instead of
	// letting the test pass with #4805 reverted.
	for _, c := range cases {
		wm := wholeWordMatcher("p", "x", c.allergy)
		for _, text := range append(append([]string{}, c.ingredients...), c.recipe) {
			if wm.Matches(text) {
				t.Fatalf("fixture %q: %q matches the pre-#4805 whole-word matcher for %q, so this case does not guard compound matching; pick a different compound",
					c.recipe, text, c.allergy)
			}
		}
	}

	r := suggestTool(k.pool).run(t, k.ctx, map[string]any{"count": 10}).suggest(t)

	for _, c := range cases {
		if slices.Contains(suggested(r), c.recipe) {
			t.Errorf("suggested %s, whose only allergen is a compound: suggested=%v excluded=%+v",
				c.recipe, suggested(r), r.Excluded)
		}
		if !slices.Contains(excludedNames(r), c.recipe) {
			t.Errorf("%s not listed in excluded: %+v", c.recipe, r.Excluded)
		}
	}
	// The negatives stay suggested. If one of these is missing it was excluded on
	// an exception word (eggplant, breadfruit, butterfly), which would be the
	// over-match direction #4805's exception list exists to prevent.
	for _, ok := range []string{"Aubergine Bake", "Breadfruit Curry", "Carrot Ribbons"} {
		if slices.Contains(excludedNames(r), ok) {
			t.Errorf("%s was excluded but carries no allergen (an exception word at most): %+v", ok, r.Excluded)
			continue
		}
		if !slices.Contains(suggested(r), ok) {
			t.Errorf("%s is neither suggested nor excluded: suggested=%v excluded=%+v", ok, suggested(r), r.Excluded)
		}
	}
	if r.ExcludedTotal != len(cases) {
		t.Errorf("excluded_total = %d, want %d: %+v", r.ExcludedTotal, len(cases), r.Excluded)
	}
	if len(r.Checks) != 0 {
		t.Errorf("checks = %v, want none: every allergy here is readable", r.Checks)
	}
	reasons := map[string]string{}
	for _, e := range r.Excluded {
		reasons[e.Name] = e.Reason
	}
	// The warning names the person, the allergen as saved, and the text that
	// matched: the compound word, not the whole ingredient line.
	for _, c := range cases {
		if !strings.Contains(reasons[c.recipe], c.reason) {
			t.Errorf("%s reason %q lacks %q", c.recipe, reasons[c.recipe], c.reason)
		}
	}
}

// TestMealSuggestProbedWordings is #4874's end-to-end acceptance test, on real
// Postgres, for the exact wordings the reviewer probed. Each of them used to
// normalise to a phrase with a junk word glued on ("deathly peanuts"), or to a
// stray fragment ("wears medicalert"), and newAllergyMatcher still said ok: the
// junk phrase compiled, matched nothing, and because it "compiled" the person
// never appeared in checks either. A person with allergies ["Deathly allergic
// to peanuts"] was suggested a peanut recipe with SUGGESTED=[Satay Bowl] and
// CHECKS=[].
//
// Every wording below has to end in one of two states, never a third:
//   - the peanut/dairy/egg recipe is in excluded with a reason naming the
//     person and the allergen, or
//   - the allergy is visible in checks and the recipe is not suggested.
func TestMealSuggestProbedWordings(t *testing.T) {
	for _, tc := range []struct {
		wording   string
		wantLabel string // the allergen label the reason must name
		wantIn    string // the ingredient the reason must name
		recipe    string // the recipe that must not be suggested
	}{
		{"Deathly allergic to peanuts", "peanuts", "peanuts", "Satay Bowl"},
		{"extremely allergic to dairy", "dairy", "cheddar", "Mac and Cheese"},
		{"highly allergic to eggs", "eggs", "eggs", "Veggie Omelette"},
		{"epi-pen for peanuts", "peanuts", "peanuts", "Satay Bowl"},
		{"allergic to peanuts, wears medicalert", "peanuts", "peanuts", "Satay Bowl"},
	} {
		t.Run(tc.wording, func(t *testing.T) {
			t.Parallel()
			k := newKitchen(t)
			k.person("Eve", tc.wording)
			k.recipe("Satay Bowl", 25, nil, "rice noodles", "peanuts", "lime")
			k.recipe("Mac and Cheese", 20, nil, "macaroni", "cheddar", "butter")
			k.recipe("Veggie Omelette", 10, nil, "eggs", "spinach")
			k.recipe("Chicken Tacos", 30, nil, "chicken thighs", "corn tortillas")

			r := suggestTool(k.pool).run(t, k.ctx, map[string]any{"count": 10}).suggest(t)

			if slices.Contains(suggested(r), tc.recipe) {
				t.Fatalf("%q: SUGGESTED %s, which has %s: suggested=%v excluded=%v checks=%v",
					tc.wording, tc.recipe, tc.wantIn, suggested(r), r.Excluded, r.Checks)
			}
			// Either the matcher read it (excluded, with a reason), or it could
			// not and the allergy is visible. Never neither.
			excludedWithReason := slices.ContainsFunc(r.Excluded, func(e excludedRecipe) bool {
				return e.Name == tc.recipe && strings.Contains(e.Reason, "("+tc.wantLabel+"): ") &&
					strings.Contains(e.Reason, tc.wantIn)
			})
			visible := slices.ContainsFunc(r.Checks, func(c string) bool {
				return strings.Contains(c, "could not be checked against recipes")
			})
			if !excludedWithReason && !visible {
				t.Errorf("%q: %s is neither excluded with a reason nor covered by an unchecked-allergy line: excluded=%+v checks=%v",
					tc.wording, tc.recipe, r.Excluded, r.Checks)
			}
			// For these five the filler fix means it must actually have been
			// read: the exclusion reason is the stronger outcome, and a checks
			// line here would mean the allergy was still not honoured.
			if !excludedWithReason {
				t.Errorf("%q: not excluded with %q/%q; the normaliser still cannot read it: excluded=%+v checks=%v",
					tc.wording, tc.wantLabel, tc.wantIn, r.Excluded, r.Checks)
			}
			// The safe recipe still comes through: failing closed must not
			// empty the list.
			if !slices.Contains(suggested(r), "Chicken Tacos") {
				t.Errorf("%q: Chicken Tacos was not suggested: %v", tc.wording, suggested(r))
			}
		})
	}
}

// TestMealSuggestUnreadableAllergyIsVisible is the other half of #4874: an
// allergy whose food the vocabulary genuinely cannot read still cannot pass as
// checked. Before, any multi-word phrase compiled and the person never
// appeared in checks, so the safety line was empty and the allergy was
// silently cleared.
func TestMealSuggestUnreadableAllergyIsVisible(t *testing.T) {
	t.Parallel()
	k := newKitchen(t)
	k.person("Eve", "Deathly allergic to peanuts") // readable: must still work
	k.person("Ari", "red dye 40")                  // not: must be visible
	k.person("Bo", "peanuts, blue 2 lake")         // half readable: both halves count
	k.recipe("Satay Bowl", 25, nil, "rice noodles", "peanuts", "lime")
	k.recipe("Chicken Tacos", 30, nil, "chicken thighs", "corn tortillas")

	st := suggestTool(k.pool)
	r := st.run(t, k.ctx, map[string]any{"count": 10}).suggest(t)

	if slices.Contains(suggested(r), "Satay Bowl") {
		t.Errorf("suggested Satay Bowl to a peanut-allergic person: %v", suggested(r))
	}
	for _, want := range []string{
		`Ari has an allergy saved as "red dye 40" that could not be checked`,
		`Bo has an allergy saved as "blue 2 lake" that could not be checked`,
	} {
		if !slices.ContainsFunc(r.Checks, func(c string) bool { return strings.Contains(c, want) }) {
			t.Errorf("checks lack %q: %v", want, r.Checks)
		}
	}
	// Bo's readable half still excludes the peanut recipe.
	if !slices.ContainsFunc(r.Excluded, func(e excludedRecipe) bool {
		return e.Name == "Satay Bowl" && strings.Contains(e.Reason, "Bo (peanuts): peanuts")
	}) {
		t.Errorf("Bo's peanut matcher was lost: %+v", r.Excluded)
	}
	// And the visible lines reach the summary a small model reads out.
	e := st.run(t, k.ctx, map[string]any{})
	for _, want := range []string{"red dye 40", "blue 2 lake"} {
		if !strings.Contains(e.Summary, want) {
			t.Errorf("summary lacks %q: %s", want, e.Summary)
		}
	}
}
