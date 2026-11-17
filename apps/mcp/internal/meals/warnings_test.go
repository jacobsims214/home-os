package meals

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"home-os/mcp/internal/testutil"
)

// TestWarningsAgreeWithMealSuggest is #4803's acceptance test. An allergy can
// be saved on the person or filed as a preference that mentions one, and
// meal_suggest reads both. The recipe and meal warnings must read the same
// two places and look at the same text (ingredients and name), or a recipe the
// suggester refuses can be planned with no warning at all.
//
// Di's allergy is only a dietary preference ("Severe shellfish allergy"), which
// is the case from wave check #4502: meal_suggest excluded "Garlic Shrimp" and
// recipe_find returned allergy_warnings=[] for the same recipe.
func TestWarningsAgreeWithMealSuggest(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	bg := context.Background()
	ts := newTools(pool)
	pt := newPlanTools(pool)

	// Di: no people.allergies at all, only a preference that mentions one.
	var di string
	if err := pool.QueryRow(bg, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Di') RETURNING id::text`, hh.HouseholdID).Scan(&di); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO person_preferences (household_id, person_id, kind, value) VALUES ($1, $2, 'dietary', 'Severe shellfish allergy')`, hh.HouseholdID, di); err != nil {
		t.Fatal(err)
	}
	// Sam: a dislike that is only an allergy because of its note.
	var sam string
	if err := pool.QueryRow(bg, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Sam') RETURNING id::text`, hh.HouseholdID).Scan(&sam); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO person_preferences (household_id, person_id, kind, value, notes) VALUES ($1, $2, 'dislike', 'sesame', 'allergy')`, hh.HouseholdID, sam); err != nil {
		t.Fatal(err)
	}

	shrimp := ts.create.run(t, ctx, map[string]any{"name": "Garlic Shrimp", "ingredients": []any{"400 g shrimp", "3 cloves garlic", "1 lemon"},
		"steps": []any{"Fry."}}).recipe(t)
	sesame := ts.create.run(t, ctx, map[string]any{"name": "Sesame Noodles", "ingredients": []any{"200 g noodles", "1 tbsp tahini"},
		"steps": []any{"Toss."}}).recipe(t)
	safe := ts.create.run(t, ctx, map[string]any{"name": "Veggie Tray", "ingredients": []any{"carrots", "cucumber"}, "steps": []any{"Chop."}}).recipe(t)

	// meal_suggest: both are excluded, with the reason a warning must repeat.
	st := suggestTool(pool)
	sg := st.run(t, ctx, map[string]any{"count": 10}).suggest(t)
	for name, reason := range map[string]string{
		"Garlic Shrimp":  "Di (shellfish): shrimp",
		"Sesame Noodles": "Sam (sesame): tahini",
	} {
		if slices.Contains(suggested(sg), name) {
			t.Errorf("meal_suggest suggested %s, which has an allergen: %v", name, suggested(sg))
		}
		var got string
		for _, e := range sg.Excluded {
			if e.Name == name {
				got = e.Reason
			}
		}
		if !strings.Contains(got, reason) {
			t.Errorf("meal_suggest reason for %s = %q, want it to contain %q", name, got, reason)
		}
	}
	if !slices.Contains(suggested(sg), "Veggie Tray") {
		t.Errorf("meal_suggest left out the safe recipe: %v", suggested(sg))
	}

	// recipe_get warns, with the same person, allergen and match as the reason.
	for _, c := range []struct {
		recipe     recipeData
		wantPerson string
		wantReason string
	}{{shrimp, "Di", "Di (shellfish): shrimp"}, {sesame, "Sam", "Sam (sesame): tahini"}} {
		g := ts.get.run(t, ctx, map[string]any{"recipe_id": c.recipe.ID}).recipe(t)
		if !warningsMatch(g.AllergyWarnings, c.wantReason) {
			t.Errorf("recipe_get %s warnings = %+v, want %q", c.recipe.Name, g.AllergyWarnings, c.wantReason)
		}
	}
	if g := ts.get.run(t, ctx, map[string]any{"recipe_id": safe.ID}).recipe(t); len(g.AllergyWarnings) != 0 {
		t.Errorf("recipe_get safe warnings = %+v", g.AllergyWarnings)
	}

	// recipe_find: the same rows, so a list never hides what get warns about.
	items := ts.find.run(t, ctx, map[string]any{}).items(t)
	found := 0
	for _, it := range items {
		switch it.Name {
		case "Garlic Shrimp":
			found++
			if !slices.Contains(it.AllergyWarnings, "Di (shellfish): shrimp") {
				t.Errorf("recipe_find shrimp warnings = %v", it.AllergyWarnings)
			}
		case "Sesame Noodles":
			found++
			if !slices.Contains(it.AllergyWarnings, "Sam (sesame): tahini") {
				t.Errorf("recipe_find sesame warnings = %v", it.AllergyWarnings)
			}
		case "Veggie Tray":
			found++
			if it.AllergyWarnings != nil {
				t.Errorf("recipe_find safe warnings = %v", it.AllergyWarnings)
			}
		}
	}
	if found != 3 {
		t.Fatalf("recipe_find returned %d of the 3 recipes: %+v", found, names(items))
	}

	// meal_set warns on the same recipes, and agenda_get agrees with it.
	today := familyToday("UTC")
	date := today.Format(time.DateOnly)
	week := pt.set.run(t, ctx, map[string]any{"meals": []any{
		map[string]any{"date": date, "slot": "dinner", "recipe_id": shrimp.ID},
		map[string]any{"date": date, "slot": "lunch", "recipe_id": sesame.ID},
	}}).set(t)
	// meal_set answers by date then slot, so find each meal by slot.
	bySlot := map[string]*PlannedMeal{}
	for _, it := range week.Meals {
		bySlot[it.Slot] = it.Meal
	}
	if !slices.Contains(bySlot["dinner"].AllergyWarnings, "Di (shellfish): shrimp") ||
		!slices.Contains(bySlot["lunch"].AllergyWarnings, "Sam (sesame): tahini") {
		t.Fatalf("meal_set = %+v", week.Meals)
	}
	plan, _, allowed, err := AgendaMeals(ctx, pool, today, today)
	if err != nil || !allowed || len(plan) != 2 {
		t.Fatalf("agenda = %+v %v %v", plan, allowed, err)
	}
	for _, m := range plan {
		switch m.Slot {
		case "dinner":
			if !slices.Contains(m.AllergyWarnings, "Di (shellfish): shrimp") {
				t.Errorf("agenda dinner = %+v", m.AllergyWarnings)
			}
		case "lunch":
			if !slices.Contains(m.AllergyWarnings, "Sam (sesame): tahini") {
				t.Errorf("agenda lunch = %+v", m.AllergyWarnings)
			}
		}
	}

	// Housesitters see allergy data from both places, as meal_suggest does.
	sitter, _ := memberCtx(t, pool, hh, "housesitter")
	if g := ts.get.run(t, sitter, map[string]any{"recipe_id": shrimp.ID}).recipe(t); !warningsMatch(g.AllergyWarnings, "Di (shellfish): shrimp") {
		t.Errorf("housesitter recipe_get = %+v", g.AllergyWarnings)
	}
}

// warningsMatch: ws contains one warning whose rendered line is want.
func warningsMatch(ws []AllergyWarning, want string) bool {
	return slices.Contains(warningLines(ws), want)
}

// TestNoIngredientsFailsClosed is #4803's other acceptance test, on the
// manager's decision: when anyone eating has an allergy, a recipe with no
// ingredients saved is not suggested (an unknown recipe is not a safe recipe),
// and the reason, the recipe warnings and the meal warnings all say the same
// thing. With no allergy in the family it keeps the old behaviour: suggested,
// ranked lower, with a caution.
func TestNoIngredientsFailsClosed(t *testing.T) {
	t.Parallel()
	k := newKitchen(t)
	k.person("Nora", "peanuts")
	brittle := k.recipe("Peanut Brittle", 40, nil)
	mystery := k.recipe("Mystery Casserole", 60, nil)
	k.recipe("Chicken Tacos", 30, nil, "chicken thighs")

	st := suggestTool(k.pool)
	r := st.run(t, k.ctx, map[string]any{"count": 10}).suggest(t)
	for _, name := range []string{"Peanut Brittle", "Mystery Casserole"} {
		if slices.Contains(suggested(r), name) {
			t.Errorf("suggested %s with an allergy in the family: %v", name, suggested(r))
		}
		if !slices.Contains(excludedNames(r), name) {
			t.Errorf("%s not in excluded: %+v", name, r.Excluded)
		}
	}
	// Peanut Brittle is caught by its name; Mystery Casserole by having nothing
	// to check, with the shared line.
	reasons := map[string]string{}
	for _, e := range r.Excluded {
		reasons[e.Name] = e.Reason
	}
	if reasons["Peanut Brittle"] != "Nora (peanuts): Peanut Brittle" {
		t.Errorf("peanut brittle reason %q", reasons["Peanut Brittle"])
	}
	if reasons["Mystery Casserole"] != noIngredientsLine {
		t.Errorf("mystery reason %q, want %q", reasons["Mystery Casserole"], noIngredientsLine)
	}

	// recipe_get: the name is checked, and the no-ingredients line is a check.
	ts := newTools(k.pool)
	for _, c := range []struct {
		id        string
		wantWarn  string
		wantCheck bool
	}{{brittle, "Nora (peanuts): Peanut Brittle", true}, {mystery, "", true}} {
		g := ts.get.run(t, k.ctx, map[string]any{"recipe_id": c.id}).recipe(t)
		if c.wantWarn == "" {
			if len(g.AllergyWarnings) != 0 {
				t.Errorf("mystery warnings = %+v", g.AllergyWarnings)
			}
		} else if !warningsMatch(g.AllergyWarnings, c.wantWarn) {
			t.Errorf("brittle warnings = %+v, want %q", g.AllergyWarnings, c.wantWarn)
		}
		if c.wantCheck && !slices.Contains(g.Checks, noIngredientsLine) {
			t.Errorf("%s checks = %v, want %q", g.Name, g.Checks, noIngredientsLine)
		}
	}
	// A recipe with ingredients does not carry the no-ingredients check.
	tacosR := ts.find.run(t, k.ctx, map[string]any{"query": "tacos"}).items(t)
	if len(tacosR) != 1 || slices.Contains(tacosR[0].Checks, noIngredientsLine) {
		t.Errorf("tacos checks = %+v", tacosR)
	}

	// meal_set and agenda_get: the same line, named with the meal.
	pt := newPlanTools(k.pool)
	date := k.today.Format(time.DateOnly)
	res := pt.set.run(t, k.ctx, map[string]any{"meals": []any{
		map[string]any{"date": date, "slot": "dinner", "recipe_id": mystery},
		map[string]any{"date": date, "slot": "lunch", "recipe_id": brittle},
	}}).set(t)
	if !slices.Contains(res.Checks, "Mystery Casserole: "+noIngredientsLine) {
		t.Errorf("meal_set checks = %v", res.Checks)
	}
	bySlot := map[string]*PlannedMeal{}
	for _, it := range res.Meals {
		bySlot[it.Slot] = it.Meal
	}
	if !slices.Contains(bySlot["lunch"].AllergyWarnings, "Nora (peanuts): Peanut Brittle") {
		t.Errorf("meal_set brittle = %+v", bySlot["lunch"].AllergyWarnings)
	}
	plan, checks, _, err := AgendaMeals(k.ctx, k.pool, k.today, k.today)
	if err != nil || len(plan) != 2 {
		t.Fatalf("agenda = %+v %v", plan, err)
	}
	if !slices.Contains(checks, "Mystery Casserole: "+noIngredientsLine) {
		t.Errorf("agenda checks = %v", checks)
	}
	for _, m := range plan {
		if m.Slot == "lunch" && !slices.Contains(m.AllergyWarnings, "Nora (peanuts): Peanut Brittle") {
			t.Errorf("agenda brittle = %+v", m.AllergyWarnings)
		}
	}

	// Nobody eating has an allergy: suggested again, ranked lower, with a caution.
	k2 := newKitchen(t)
	sam := k2.person("Sam")
	k2.pref(sam, "dietary", "vegetarian", "")
	k2.recipe("Mystery Casserole", 60, nil)
	k2.recipe("Veggie Curry", 45, nil, "chickpeas", "spinach")
	r2 := suggestTool(k2.pool).run(t, k2.ctx, map[string]any{"count": 10}).suggest(t)
	if !slices.Equal(suggested(r2), []string{"Veggie Curry", "Mystery Casserole"}) {
		t.Fatalf("no allergies: %v", suggested(r2))
	}
	last := r2.Suggestions[1]
	if !slices.ContainsFunc(last.Cautions, func(c string) bool { return strings.Contains(c, "No ingredients are saved") }) {
		t.Errorf("mystery cautions = %v", last.Cautions)
	}
}

// TestProbedWordingsWarnEverywhere is #4874's warning-path acceptance test. The
// suggester is one place an allergy is honoured; the recipe tools and the plan
// are the others, and #4803's rule is that they cannot disagree. The probed
// wordings must therefore exclude in meal_suggest AND warn in recipe_get,
// recipe_find, meal_set and agenda_get — before #4874 they did none of it,
// because the junk phrase compiled to a regex that matched nothing and reported
// ok, so the checks list that is supposed to cover that case stayed empty too.
func TestProbedWordingsWarnEverywhere(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	bg := context.Background()
	ts := newTools(pool)
	pt := newPlanTools(pool)

	if _, err := pool.Exec(bg, `INSERT INTO people (household_id, display_name, allergies)
		VALUES ($1, 'Eve', ARRAY['Deathly allergic to peanuts']), ($1, 'Ari', ARRAY['red dye 40'])`,
		hh.HouseholdID); err != nil {
		t.Fatal(err)
	}
	satay := ts.create.run(t, ctx, map[string]any{"name": "Satay Bowl",
		"ingredients": []any{"200 g rice noodles", "3 tbsp peanuts", "1 lime"}, "steps": []any{"Toss."}}).recipe(t)
	tacos := ts.create.run(t, ctx, map[string]any{"name": "Chicken Tacos",
		"ingredients": []any{"chicken thighs", "corn tortillas"}, "steps": []any{"Cook."}}).recipe(t)

	// recipe_get: the peanut hit is there, and Ari's unreadable allergy is
	// visible rather than silently checked.
	g := ts.get.run(t, ctx, map[string]any{"recipe_id": satay.ID}).recipe(t)
	if !warningsMatch(g.AllergyWarnings, "Eve (peanuts): peanuts") {
		t.Errorf("recipe_get %s warnings = %+v, want Eve (peanuts): peanuts", satay.Name, g.AllergyWarnings)
	}
	if !slices.ContainsFunc(g.Checks, func(c string) bool {
		return strings.Contains(c, `Ari has an allergy saved as "red dye 40" that could not be checked`)
	}) {
		t.Errorf("recipe_get checks = %v", g.Checks)
	}

	// recipe_find: the same rows, so a list never hides what get warns about.
	var found int
	for _, it := range ts.find.run(t, ctx, map[string]any{}).items(t) {
		switch it.Name {
		case satay.Name:
			found++
			if !slices.Contains(it.AllergyWarnings, "Eve (peanuts): peanuts") {
				t.Errorf("recipe_find %s warnings = %v", satay.Name, it.AllergyWarnings)
			}
		case tacos.Name:
			found++
			if len(it.AllergyWarnings) != 0 {
				t.Errorf("recipe_find %s warnings = %v, want none", tacos.Name, it.AllergyWarnings)
			}
		}
	}
	if found != 2 {
		t.Fatalf("recipe_find returned %d of the 2 recipes", found)
	}

	// meal_set plans it with the warning, and agenda_get reads it back.
	today := familyToday("UTC")
	date := today.Format(time.DateOnly)
	res := pt.set.run(t, ctx, map[string]any{"date": date, "slot": "dinner", "recipe_id": satay.ID}).set(t)
	if len(res.Meals) != 1 || res.Meals[0].Meal == nil ||
		!slices.Contains(res.Meals[0].Meal.AllergyWarnings, "Eve (peanuts): peanuts") {
		t.Fatalf("meal_set = %+v", res.Meals)
	}
	if !slices.ContainsFunc(res.Checks, func(c string) bool { return strings.Contains(c, "red dye 40") }) {
		t.Errorf("meal_set checks = %v", res.Checks)
	}
	plan, checks, allowed, err := AgendaMeals(ctx, pool, today, today)
	if err != nil || !allowed || len(plan) != 1 {
		t.Fatalf("agenda = %+v %v %v %v", plan, checks, allowed, err)
	}
	if !slices.Contains(plan[0].AllergyWarnings, "Eve (peanuts): peanuts") ||
		!slices.ContainsFunc(checks, func(c string) bool { return strings.Contains(c, "red dye 40") }) {
		t.Errorf("agenda meal = %+v / checks %v", plan[0], checks)
	}
}
