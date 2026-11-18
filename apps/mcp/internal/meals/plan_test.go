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
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/testutil"
)

func (e env) set(t *testing.T) mealSetResult {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d mealSetResult
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return d
}

func (e env) rate(t *testing.T) mealRateResult {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d mealRateResult
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return d
}

type planTools struct{ set, rate tool }

func newPlanTools(pool *pgxpool.Pool) planTools {
	mk := func(_ string, d mcp.Tool, h toolHandler) tool { return tool{d, h} }
	return planTools{set: mk(NewMealSetTool(pool)), rate: mk(NewMealRateTool(pool))}
}

// TestMealToolDefinitions pins meal_set and meal_rate to the catalog.
func TestMealToolDefinitions(t *testing.T) {
	ts := newPlanTools(nil)
	for _, tc := range []struct {
		w        tool
		name     string
		required []string
		params   []string
	}{
		{ts.set, "meal_set", []string{"meals"}, nil},
		{ts.rate, "meal_rate", []string{"date", "slot", "person_id", "rating"}, []string{"comment"}},
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
		if a.Title == "" || a.ReadOnlyHint == nil || *a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint ||
			a.IdempotentHint == nil || !*a.IdempotentHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s hints = %+v", d.Name, a)
		}
		resolvedSchema(t, d)
	}
	items, _ := ts.set.def.InputSchema.Properties["meals"].(map[string]any)["items"].(map[string]any)
	props, _ := items["properties"].(map[string]any)
	for _, p := range []string{"date", "slot", "recipe_id", "text", "notes", "clear"} {
		if prop, ok := props[p].(map[string]any); !ok || utf8.RuneCountInString(prop["description"].(string)) > 80 {
			t.Errorf("meals[].%s = %v", p, props[p])
		}
	}
}

func TestResolveDay(t *testing.T) {
	today := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC) // a Tuesday
	ny, _ := time.LoadLocation("America/New_York")
	for _, tc := range []struct {
		in      string
		forward bool
		want    string
	}{
		{"2026-12-01", true, "2026-12-01"},
		{"2026-11-18T01:30:00Z", true, "2026-11-17"}, // evening of the 17th in New York
		{"today", true, "2026-11-17"},
		{"Tonight", false, "2026-11-17"},
		{"tomorrow", true, "2026-11-18"},
		{"yesterday", false, "2026-11-16"},
		{"last night", false, "2026-11-16"},
		{"tuesday", true, "2026-11-17"},
		{"tuesday", false, "2026-11-17"},
		{"Friday", true, "2026-11-20"},
		{"friday", false, "2026-11-13"},
		{"next tuesday", true, "2026-11-24"},
		{"next wed", true, "2026-11-18"},
		{"last tuesday", false, "2026-11-10"},
		{"last monday", true, "2026-11-16"},
		{"this sat", true, "2026-11-21"},
		{"coming monday", false, "2026-11-23"},
		{"thurs", true, "2026-11-19"},
		{"sunday's", true, "2026-11-22"},
	} {
		got, ok := resolveDay(tc.in, today, ny, tc.forward)
		if !ok || got.Format(time.DateOnly) != tc.want {
			t.Errorf("resolveDay(%q, forward=%v) = %s %v, want %s", tc.in, tc.forward, got.Format(time.DateOnly), ok, tc.want)
		}
	}
	for _, bad := range []string{"", "someday", "next week", "11/17", "tuesday dinner"} {
		if _, ok := resolveDay(bad, today, ny, true); ok {
			t.Errorf("resolveDay(%q) accepted", bad)
		}
	}
}

func TestParseRating(t *testing.T) {
	for in, want := range map[any]int{4.0: 4, "5": 5, "3/5": 3, "4 stars": 4, "2 out of 5": 2, "1 star": 1} {
		if got, err := parseRating(in); err != nil || got == nil || *got != want {
			t.Errorf("parseRating(%v) = %v %v", in, got, err)
		}
	}
	for _, in := range []any{0.0, 6.0, 4.5, "great", true} {
		if _, err := parseRating(in); err == nil {
			t.Errorf("parseRating(%v) accepted", in)
		}
	}
}

// setupFamily seeds a family in a far-east timezone (so "today" differs
// from the server's UTC date for half the day), with Nora (peanut allergy)
// and Sam, and two recipes.
func setupFamily(t *testing.T, pool *pgxpool.Pool) (context.Context, testutil.Household, string, string, recipeData, recipeData, time.Time) {
	t.Helper()
	ctx, hh := testutil.SeedHousehold(t, pool)
	bg := context.Background()
	if _, err := pool.Exec(bg, `UPDATE households SET timezone = 'Pacific/Kiritimati' WHERE id = $1`, hh.HouseholdID); err != nil {
		t.Fatal(err)
	}
	var nora, sam string
	if err := pool.QueryRow(bg, `INSERT INTO people (household_id, display_name, allergies) VALUES ($1, 'Nora', ARRAY['peanuts']) RETURNING id::text`, hh.HouseholdID).Scan(&nora); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(bg, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Sam') RETURNING id::text`, hh.HouseholdID).Scan(&sam); err != nil {
		t.Fatal(err)
	}
	rt := newTools(pool)
	tacosR := rt.create.run(t, ctx, tacos).recipe(t)
	satay := rt.create.run(t, ctx, map[string]any{"name": "Satay Noodles", "ingredients": []any{"200 g rice noodles", "3 tbsp peanut butter"}, "steps": []any{"Cook."}}).recipe(t)
	return ctx, hh, nora, sam, tacosR, satay, familyToday("Pacific/Kiritimati")
}

// TestMealSetWeek is the acceptance test: a week of dinners in one call
// (recipe by name, by id, free text, date words in the family timezone),
// read back through the plan reader agenda_get uses.
func TestMealSetWeek(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh, _, _, tacosR, satay, today := setupFamily(t, pool)
	ts := newPlanTools(pool)
	d := func(n int) string { return today.AddDate(0, 0, n).Format(time.DateOnly) }

	week := map[string]any{"meals": []any{
		map[string]any{"date": "today", "slot": "dinner", "recipe_id": "chicken tacos"},
		map[string]any{"date": d(1), "slot": "Supper", "recipe_id": satay.ID},
		map[string]any{"date": d(2), "slot": "dinner", "text": "Pizza night", "notes": "Grandma visiting"},
		map[string]any{"date": d(3), "text": "satay noodles"}, // exact name: linked; slot defaults to dinner
		map[string]any{"date": d(4), "slot": "dinner", "text": "tacos"},
		map[string]any{"date": d(5), "meal": "dinner", "title": "Leftovers"},
		map[string]any{"date": d(6), "slot": "dinner", "text": "Peanut brittle sundaes", "servings": 6},
	}}
	e := ts.set.run(t, ctx, week)
	res := e.set(t)
	if len(res.Meals) != 7 || res.Timezone != "Pacific/Kiritimati" {
		t.Fatalf("meals = %+v", res)
	}
	for i, it := range res.Meals {
		if it.Action != "planned" || it.Meal == nil || it.LocalDate != d(i) || it.Slot != "dinner" || it.Weekday != today.AddDate(0, 0, i).Weekday().String() {
			t.Errorf("meal %d = %+v", i, it)
		}
	}
	m := res.Meals
	if *m[0].Meal.RecipeID != tacosR.ID || m[0].Meal.Title != "Chicken Tacos" || *m[1].Meal.RecipeID != satay.ID ||
		m[2].Meal.RecipeID != nil || m[2].Meal.Title != "Pizza night" || *m[2].Meal.Notes != "Grandma visiting" ||
		m[3].Meal.RecipeID == nil || *m[3].Meal.RecipeID != satay.ID || m[3].Meal.Title != "satay noodles" ||
		m[4].Meal.RecipeID != nil || len(m[4].SimilarRecipes) != 1 || !strings.HasPrefix(m[4].SimilarRecipes[0], "Chicken Tacos (") ||
		m[5].Meal.Title != "Leftovers" {
		t.Errorf("meals = %+v", m)
	}
	// Allergy warnings for the family: a recipe's ingredients, or free text.
	// "Satay Noodles" is a second hit: the recipe's name is checked too (#4803).
	if !slices.Equal(m[1].Meal.AllergyWarnings, []string{"Nora (peanuts): peanut butter", "Nora (peanuts): Satay Noodles"}) ||
		!slices.Equal(m[6].Meal.AllergyWarnings, []string{"Nora (peanuts): Peanut brittle sundaes"}) || m[0].Meal.AllergyWarnings != nil {
		t.Errorf("warnings = %v / %v / %v", m[1].Meal.AllergyWarnings, m[6].Meal.AllergyWarnings, m[0].Meal.AllergyWarnings)
	}
	for _, want := range []string{"7 planned", "Chicken Tacos", "Allergy warning:", "Nora (peanuts): peanut butter", "tacos on", "similar name: Chicken Tacos", "servings is not saved"} {
		if !strings.Contains(e.Summary, want) {
			t.Errorf("summary lacks %q: %s", want, e.Summary)
		}
	}
	if !strings.Contains(strings.Join(e.Next, " "), `agenda_get from=`+d(0)+` to=`+d(6)) || !strings.Contains(strings.Join(e.Next, " "), "meal_rate") {
		t.Errorf("next = %v", e.Next)
	}

	// Read back: what agenda_get areas=["meals"] serves.
	plan, _, allowed, err := AgendaMeals(ctx, pool, today, today.AddDate(0, 0, 6))
	if err != nil || !allowed || len(plan) != 7 {
		t.Fatalf("plan = %+v %v %v", plan, allowed, err)
	}
	titles := make([]string, len(plan))
	for i, p := range plan {
		titles[i] = p.Title
	}
	if !slices.Equal(titles, []string{"Chicken Tacos", "Satay Noodles", "Pizza night", "satay noodles", "tacos", "Leftovers", "Peanut brittle sundaes"}) {
		t.Errorf("titles = %v", titles)
	}

	// The same call again changes nothing.
	again := ts.set.run(t, ctx, week).set(t)
	for _, it := range again.Meals {
		if it.Action != "unchanged" {
			t.Errorf("rerun = %+v", it)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM meal_plans WHERE household_id = $1`, hh.HouseholdID); n != 7 {
		t.Errorf("%d meal plans after a rerun", n)
	}

	// Replace, update notes, clear (ratings go with it), clear an empty slot.
	if _, err := pool.Exec(context.Background(), `INSERT INTO meal_feedback (meal_plan_id, rating) SELECT id, 5 FROM meal_plans WHERE household_id = $1 AND date = $2`, hh.HouseholdID, d(5)); err != nil {
		t.Fatal(err)
	}
	mix := ts.set.run(t, ctx, map[string]any{"meals": []any{
		map[string]any{"date": d(2), "slot": "dinner", "text": "Pizza night", "notes": "Grandma and Grandpa"},
		map[string]any{"date": d(4), "slot": "dinner", "recipe_id": tacosR.ID},
		map[string]any{"date": d(5), "slot": "dinner", "clear": true},
		map[string]any{"date": d(5), "slot": "lunch", "clear": "true"},
	}})
	mr := mix.set(t)
	if mr.Meals[0].Action != "updated" || *mr.Meals[0].Meal.Notes != "Grandma and Grandpa" ||
		mr.Meals[1].Action != "replaced" || *mr.Meals[1].Previous != "tacos" || mr.Meals[1].Meal.Title != "Chicken Tacos" ||
		mr.Meals[2].Action != "already_empty" || mr.Meals[3].Action != "cleared" || mr.Meals[3].Meal != nil || mr.Meals[3].RatingsRemoved != 1 {
		t.Errorf("mix = %+v / %s", mr.Meals, mix.Summary)
	}

	// "Tuesday dinner is tacos" is one call with top-level fields.
	one := ts.set.run(t, ctx, map[string]any{"date": "tuesday", "text": "Fish tacos"})
	got := one.set(t)
	tue, _ := resolveDay("tuesday", today, time.UTC, true)
	if len(got.Meals) != 1 || got.Meals[0].LocalDate != tue.Format(time.DateOnly) || got.Meals[0].Slot != "dinner" ||
		got.Meals[0].Meal.Title != "Fish tacos" || (got.Meals[0].Action != "replaced" && got.Meals[0].Action != "planned") {
		t.Errorf("tuesday = %+v / %s", got.Meals, one.Summary)
	}
}

func TestMealSetValidation(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh, _, _, _, _, today := setupFamily(t, pool)
	ts := newPlanTools(pool)
	d0 := today.Format(time.DateOnly)
	for _, tc := range []struct {
		args     map[string]any
		field    string
		contains string
	}{
		{map[string]any{}, "meals", "meals is required"},
		{map[string]any{"meals": []any{}}, "meals", "empty"},
		{map[string]any{"meals": "not json"}, "meals", "list of objects"},
		{map[string]any{"meals": []any{"tacos"}}, "meals[0]", "must be an object"},
		{map[string]any{"meals": []any{map[string]any{"slot": "dinner", "text": "x"}}}, "meals[0].date", "required"},
		{map[string]any{"meals": []any{map[string]any{"date": "someday", "text": "x"}}}, "meals[0].date", "not a date"},
		{map[string]any{"meals": []any{map[string]any{"date": "2031-01-01", "text": "x"}}}, "meals[0].date", "more than a year"},
		{map[string]any{"meals": []any{map[string]any{"date": d0, "slot": "elevenses", "text": "x"}}}, "meals[0].slot", "not a meal slot"},
		{map[string]any{"meals": []any{map[string]any{"date": d0}}}, "meals[0].text", "needs a recipe_id or text"},
		{map[string]any{"meals": []any{map[string]any{"date": d0, "text": "x", "clear": true}}}, "meals[0].clear", "clear:true and a meal"},
		{map[string]any{"meals": []any{map[string]any{"date": d0, "text": "a"}, map[string]any{"date": "today", "slot": "dinner", "text": "b"}}}, "meals[1].date", "are both"},
		{map[string]any{"meals": []any{map[string]any{"date": d0, "recipe_id": "Lasagne"}}}, "recipe_id", "No recipe named"},
	} {
		ts.set.run(t, ctx, tc.args).wantError(t, tc.field, tc.contains)
	}
	// A JSON string of the list is accepted.
	if r := ts.set.run(t, ctx, map[string]any{"meals": `[{"date":"` + d0 + `","slot":"lunch","text":"Soup"}]`}).set(t); r.Meals[0].Meal.Title != "Soup" {
		t.Errorf("json string = %+v", r)
	}
	// Nothing from the refused calls was written.
	if n := count(t, pool, `SELECT count(*) FROM meal_plans WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("%d meal plans", n)
	}
	// Another family's recipe id is not found.
	other, _ := testutil.SeedHousehold(t, pool)
	foreign := newTools(pool).create.run(t, other, map[string]any{"name": "Secret Stew", "ingredients": []any{"beef"}, "steps": []any{"Stew."}}).recipe(t)
	if e := ts.set.run(t, ctx, map[string]any{"date": d0, "recipe_id": foreign.ID}); e.OK {
		t.Errorf("planned another family's recipe: %s", e.Summary)
	}
}

// TestMealRate: ratings are stored per person (one row each, rating again
// updates it), with the API's who-may-rate-whom policy.
func TestMealRate(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh, nora, sam, tacosR, _, today := setupFamily(t, pool)
	ts := newPlanTools(pool)
	yesterday := today.AddDate(0, 0, -1).Format(time.DateOnly)
	ts.set.run(t, ctx, map[string]any{"meals": []any{
		map[string]any{"date": yesterday, "slot": "dinner", "recipe_id": tacosR.ID},
		map[string]any{"date": yesterday, "slot": "lunch", "text": "Sandwiches"},
	}}).set(t)

	r := ts.rate.run(t, ctx, map[string]any{"date": "yesterday", "slot": "dinner", "person_id": "nora", "rating": 5, "comment": "Loved the salsa"})
	nr := r.rate(t)
	if nr.Action != "saved" || nr.PersonID != nora || *nr.Rating != 5 || *nr.Comment != "Loved the salsa" || nr.Meal.Title != "Chicken Tacos" {
		t.Errorf("nora = %+v", nr)
	}
	sr := ts.rate.run(t, ctx, map[string]any{"date": yesterday, "slot": "dinner", "person_id": sam, "rating": "3/5"}).rate(t)
	if sr.Action != "saved" || len(sr.Meal.Ratings) != 2 {
		t.Errorf("sam = %+v", sr)
	}
	// Rating again updates the person's row and keeps the comment.
	up := ts.rate.run(t, ctx, map[string]any{"date": yesterday, "slot": "dinner", "person_id": "Nora", "rating": 4})
	ur := up.rate(t)
	if ur.Action != "updated" || *ur.Rating != 4 || *ur.PreviousRating != 5 || ur.Comment == nil || *ur.Comment != "Loved the salsa" ||
		!strings.Contains(up.Summary, "(was 5/5)") || !strings.Contains(up.Summary, "averages 3.5 from 2 ratings") {
		t.Errorf("update = %+v / %s", ur, up.Summary)
	}
	if e := ts.rate.run(t, ctx, map[string]any{"date": yesterday, "slot": "dinner", "person_id": "Nora", "rating": 4}); e.rate(t).Action != "unchanged" {
		t.Errorf("same rating = %s", e.Summary)
	}
	if n := count(t, pool, `SELECT count(*) FROM meal_feedback WHERE household_id = $1`, hh.HouseholdID); n != 2 {
		t.Errorf("%d feedback rows, want one per person", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM meal_feedback WHERE person_id = $1 AND rating = 4 AND comment = 'Loved the salsa'`, nora); n != 1 {
		t.Error("Nora's row is wrong")
	}
	// The recipe's rating now reflects the meal.
	if g := newTools(pool).get.run(t, ctx, map[string]any{"recipe_id": tacosR.ID}).recipe(t); g.Rating == nil || g.Rating.Average != 3.5 || g.Rating.Count != 2 {
		t.Errorf("recipe rating = %+v", g.Rating)
	}
	// Comment only; the day's slot is needed when there are several meals.
	cr := ts.rate.run(t, ctx, map[string]any{"date": yesterday, "slot": "lunch", "person_id": "Sam", "comment": "Too much mustard"}).rate(t)
	if cr.Rating != nil || cr.Meal.Title != "Sandwiches" {
		t.Errorf("comment = %+v", cr)
	}

	for _, tc := range []struct {
		args     map[string]any
		field    string
		contains string
	}{
		{map[string]any{"date": yesterday, "person_id": "Nora", "rating": 9}, "rating", "1 to 5"},
		{map[string]any{"date": yesterday, "person_id": "Nora"}, "rating", "rating is required"},
		{map[string]any{"date": yesterday, "person_id": "Zed", "rating": 3}, "person_id", "people: Nora, Sam"},
		{map[string]any{"date": "someday", "person_id": "Nora", "rating": 3}, "date", "not a date"},
		{map[string]any{"date": today.AddDate(0, 0, -3).Format(time.DateOnly), "person_id": "Nora", "rating": 3}, "date", "Nothing is planned for dinner"},
		{map[string]any{"date": yesterday, "slot": "snack", "person_id": "Nora", "rating": 3}, "date", "Planned that day: lunch, dinner"},
	} {
		ts.rate.run(t, ctx, tc.args).wantError(t, tc.field, tc.contains)
	}

	// A family member rates only as themselves (default person_id).
	mCtx, uid := memberCtx(t, pool, hh, "family_member")
	if e := ts.rate.run(t, mCtx, map[string]any{"date": yesterday, "slot": "dinner", "person_id": "Sam", "rating": 1}); e.OK ||
		!strings.Contains(e.Summary, "can only rate meals for themselves") {
		t.Errorf("member for Sam = %+v", e)
	}
	// Each login has its own person (the membership trigger makes one).
	var self string
	if err := pool.QueryRow(context.Background(), `SELECT id::text FROM people WHERE user_id = $1`, uid).Scan(&self); err != nil {
		t.Fatal(err)
	}
	mr := ts.rate.run(t, mCtx, map[string]any{"date": yesterday, "slot": "dinner", "rating": 2}).rate(t)
	if mr.PersonID != self || mr.Action != "saved" || *mr.Rating != 2 {
		t.Errorf("member self = %+v", mr)
	}
	if e := ts.rate.run(t, mCtx, map[string]any{"date": yesterday, "slot": "dinner", "person_id": uid.String(), "rating": 3}); e.rate(t).PersonID != self {
		t.Errorf("person_id as the login id = %s", e.Summary)
	}
	// Without a linked person, person_id is needed.
	if _, err := pool.Exec(context.Background(), `UPDATE people SET user_id = NULL WHERE id = $1`, self); err != nil {
		t.Fatal(err)
	}
	ts.rate.run(t, mCtx, map[string]any{"date": yesterday, "slot": "dinner", "rating": 3}).wantError(t, "person_id", "not linked to a person")
	// Managers rate for anyone (kids have no login).
	mgrCtx, _ := memberCtx(t, pool, hh, "family_manager")
	ts.rate.run(t, mgrCtx, map[string]any{"date": yesterday, "slot": "dinner", "person_id": nora, "rating": 5}).rate(t)
}

func TestMealPlanRoles(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ownerCtx, hh, _, _, _, _, today := setupFamily(t, pool)
	ts := newPlanTools(pool)
	d0 := today.Format(time.DateOnly)
	ts.set.run(t, ownerCtx, map[string]any{"date": d0, "slot": "dinner", "text": "Pizza"}).set(t)

	for _, role := range []string{"family_manager", "family_member"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		ts.set.run(t, ctx, map[string]any{"date": d0, "slot": "lunch", "text": "Soup by " + role}).set(t)
	}
	for _, role := range []string{"viewer", "housesitter"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		plan, _, allowed, err := AgendaMeals(ctx, pool, today, today)
		if err != nil || !allowed || len(plan) != 2 {
			t.Errorf("%s reads plan = %+v %v %v", role, plan, allowed, err)
		}
	}
	for _, role := range []string{"viewer", "housesitter", "vendor"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		for _, c := range []struct {
			w    tool
			args map[string]any
		}{
			{ts.set, map[string]any{"date": d0, "slot": "dinner", "clear": true}},
			{ts.rate, map[string]any{"date": d0, "slot": "dinner", "person_id": "Nora", "rating": 1}},
		} {
			if e := c.w.run(t, ctx, c.args); e.OK || !strings.Contains(e.Summary, "The "+role+" role can't change the meal plan") {
				t.Errorf("%s %s = %+v", role, c.w.def.Name, e)
			}
		}
	}
	vendorCtx, _ := memberCtx(t, pool, hh, "vendor")
	if plan, _, allowed, err := AgendaMeals(vendorCtx, pool, today, today); err != nil || allowed || plan != nil {
		t.Errorf("vendor reads plan = %+v %v %v", plan, allowed, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM meal_plans WHERE household_id = $1 AND date = $2 AND slot = 'dinner'`, hh.HouseholdID, d0); n != 1 {
		t.Error("a refused call cleared dinner")
	}
	if n := count(t, pool, `SELECT count(*) FROM meal_feedback WHERE household_id = $1`, hh.HouseholdID); n != 0 {
		t.Error("a refused call saved a rating")
	}
	// Writes are attributed (050 authorship via the pool).
	if n := count(t, pool, `SELECT count(*) FROM meal_plans WHERE household_id = $1 AND title = 'Pizza' AND created_by = $2`, hh.HouseholdID, hh.UserID); n != 1 {
		t.Error("meal not attributed to its planner")
	}
}
