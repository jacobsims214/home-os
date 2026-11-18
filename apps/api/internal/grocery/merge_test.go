package grocery_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/grocery"
)

// The rules here are meal_build_grocery_list's rules (#4500,
// apps/mcp/internal/meals/grocery.go). These tests pin the examples that
// tool's own tests use, so the REST preview and the assistant cannot give a
// family two different answers for the same week.

func ptrStr(s string) *string   { return &s }
func ptrNum(f float64) *float64 { return &f }
func ptrInt(i int) *int         { return &i }

func ing(name string, qty any, unit string) grocery.Ingredient {
	i := grocery.Ingredient{Name: name, Quantity: qty}
	if unit != "" {
		i.Unit = ptrStr(unit)
	}
	return i
}

func meal(date, slot, title string, recipeID *string, servings *int, ings ...grocery.Ingredient) grocery.Meal {
	return grocery.Meal{Date: date, Slot: slot, Title: title, RecipeID: recipeID, Servings: servings, Ingredients: ings}
}

func TestIngredientKey(t *testing.T) {
	cases := map[string]string{
		"Tomatoes":        "tomato",
		"tomato":          "tomato",
		"the onions":      "onion",
		"a pinch of salt": "pinch of salt",
		"some eggs":       "egg",
		"Chicken thighs":  "chicken thigh",
		"berries":         "berry",
		"glasses":         "glass",
		"  Olive  Oil  ":  "olive oil",
		"":                "",
	}
	for in, want := range cases {
		assert.Equal(t, want, grocery.IngredientKey(in), in)
	}
}

func TestSectionOf(t *testing.T) {
	cases := map[string]string{
		"bell peppers":       "Produce",
		"red pepper":         "Produce",
		"chicken thighs":     "Meat & seafood",
		"chicken broth":      "Pantry", // the last word decides first
		"garlic powder":      "Spices",
		"garlic":             "Produce",
		"peanut butter":      "Pantry",
		"coconut milk":       "Pantry",
		"sour cream":         "Dairy & eggs",
		"flour tortillas":    "Bakery",
		"frozen peas":        "Frozen",
		"olive oil":          "Pantry",
		"limes":              "Produce",
		"":                   "Other",
		"mystery ingredient": "Other",
	}
	for in, want := range cases {
		assert.Equal(t, want, grocery.SectionOf(in), in)
	}
}

func TestNormUnitAndParseAmount(t *testing.T) {
	assert.Equal(t, "tbsp", grocery.NormUnit("Tablespoons"))
	assert.Equal(t, "tbsp", grocery.NormUnit(" tbs "))
	assert.Equal(t, "craziness", grocery.NormUnit("Craziness"), "an unknown unit is kept, lower-cased")

	n, ok := grocery.ParseAmount("1 1/2")
	require.True(t, ok)
	assert.InDelta(t, 1.5, n, 1e-9)

	n, ok = grocery.ParseAmount("1½")
	require.True(t, ok)
	assert.InDelta(t, 1.5, n, 1e-9)

	n, ok = grocery.ParseAmount("½")
	require.True(t, ok)
	assert.InDelta(t, 0.5, n, 1e-9)

	_, ok = grocery.ParseAmount("a pinch")
	assert.False(t, ok, "a text amount is not a number")
}

// The acceptance example from meal_build_grocery_list's own test: tomatoes
// 2+2+3, olive oil 5 tbsp, salt 1 tsp plus a pinch.
func TestMergeAddsWithinAUnitFamily(t *testing.T) {
	r1, r2 := ptrStr("r1"), ptrStr("r2")
	meals := []grocery.Meal{
		meal("2026-11-16", "dinner", "Sunday sauce", r1, ptrInt(4),
			ing("tomatoes", 2.0, "cup"),
			ing("olive oil", 3.0, "tbsp"),
			ing("salt", 1.0, "tsp"),
		),
		meal("2026-11-17", "dinner", "Pasta", r2, ptrInt(4),
			ing("Tomatoes", 2.0, "cup"),
			ing("olive oil", 2.0, "tbsp"),
			ing("salt", "a pinch", ""),
		),
		meal("2026-11-18", "dinner", "Sauce again", r1, ptrInt(4),
			ing("tomato", 3.0, "cup"),
		),
	}
	res := grocery.Merge(meals, "2026-11-16", "2026-11-22")

	require.Len(t, res.Items, 3, "tomatoes, olive oil and salt merge into three items")
	byText := map[string]grocery.Item{}
	for _, it := range res.Items {
		byText[it.Key] = it
	}

	tomatoes := byText["tomato"]
	require.NotNil(t, tomatoes.Quantity)
	assert.InDelta(t, 7.0, *tomatoes.Quantity, 1e-9)

	oil := byText["olive oil"]
	require.NotNil(t, oil.Quantity)
	assert.InDelta(t, 5.0, *oil.Quantity, 1e-9)
	require.NotNil(t, oil.Unit)
	assert.Equal(t, "tbsp", *oil.Unit)

	salt := byText["salt"]
	require.NotNil(t, salt.Quantity)
	assert.InDelta(t, 1.0, *salt.Quantity, 1e-9)
	assert.Contains(t, salt.Note, "plus a pinch")
}

// "2 tbsp + 1/2 cup x2" is shown as 1.125 cup: the total is in the largest
// unit used inside one family.
func TestMergeShowsTheLargestUnitInTheFamily(t *testing.T) {
	r := ptrStr("r1")
	meals := []grocery.Meal{
		meal("2026-11-16", "dinner", "A", r, ptrInt(2), ing("olive oil", 2.0, "tbsp")),
		meal("2026-11-17", "dinner", "B", r, ptrInt(2), ing("olive oil", 0.5, "cup")),
		meal("2026-11-18", "dinner", "C", r, ptrInt(2), ing("olive oil", 0.5, "cup")),
	}
	res := grocery.Merge(meals, "2026-11-16", "2026-11-18")
	require.Len(t, res.Items, 1)
	it := res.Items[0]
	require.NotNil(t, it.Quantity)
	require.NotNil(t, it.Unit)
	assert.Equal(t, "cup", *it.Unit)
	assert.InDelta(t, 1.125, *it.Quantity, 1e-9)
}

// Amounts that do not convert become ONE row with no quantity and a note,
// so the list never gets the same ingredient twice.
func TestMergeDoesNotConvertAcrossFamilies(t *testing.T) {
	r := ptrStr("r1")
	meals := []grocery.Meal{
		meal("2026-11-16", "dinner", "A", r, ptrInt(2), ing("chicken thighs", 1.5, "lb")),
		meal("2026-11-17", "dinner", "B", r, ptrInt(2), ing("chicken thighs", 400.0, "g")),
	}
	res := grocery.Merge(meals, "2026-11-16", "2026-11-17")
	require.Len(t, res.Items, 1)
	it := res.Items[0]
	assert.Nil(t, it.Quantity, "lb and g are different families")
	assert.Contains(t, it.Note, "amounts in different units")
	assert.Contains(t, it.Note, "lb")
	assert.Contains(t, it.Note, "g")
}

// A free-text meal has no ingredients to contribute, and the result says so
// instead of quietly producing a shorter list.
func TestMergeSkipsMealsWithoutARecipe(t *testing.T) {
	r := ptrStr("r1")
	meals := []grocery.Meal{
		meal("2026-11-16", "dinner", "Sheet-pan chicken fajitas", r, ptrInt(5), ing("bell peppers", 3.0, "")),
		meal("2026-11-17", "dinner", "Pizza night", nil, nil),
		meal("2026-11-18", "dinner", "Pasta night", nil, nil),
	}
	res := grocery.Merge(meals, "2026-11-16", "2026-11-18")
	assert.Equal(t, 3, res.Planned)
	assert.Equal(t, 1, res.RecipeMeals)
	require.Len(t, res.Skipped, 2)
	assert.Equal(t, "Pizza night", res.Skipped[0].Title)
	assert.Contains(t, res.Skipped[0].Reason, "not a recipe")
	assert.Len(t, res.Items, 1)
}

func TestMergeSkipsRecipesWithoutIngredients(t *testing.T) {
	empty := ptrStr("r2")
	meals := []grocery.Meal{
		meal("2026-11-16", "dinner", "Peanut brittle", empty, ptrInt(8)),
	}
	res := grocery.Merge(meals, "2026-11-16", "2026-11-16")
	assert.Empty(t, res.Items)
	require.Len(t, res.Skipped, 1)
	assert.Contains(t, res.Skipped[0].Reason, "no ingredients")
	require.NotEmpty(t, res.Notes)
	assert.Contains(t, res.Notes[0], "nothing to add")
}

func TestMergeOrdersSectionsLikeAStoreWalk(t *testing.T) {
	r := ptrStr("r1")
	meals := []grocery.Meal{
		meal("2026-11-16", "dinner", "A", r, ptrInt(2),
			ing("olive oil", 1.0, "tbsp"),    // Pantry
			ing("bell peppers", 2.0, ""),     // Produce
			ing("chicken thighs", 1.0, "lb"), // Meat & seafood
		),
	}
	res := grocery.Merge(meals, "2026-11-16", "2026-11-16")
	assert.Equal(t, []string{"Produce", "Meat & seafood", "Pantry"}, res.Sections)
	require.Len(t, res.Items, 3)
	assert.Equal(t, "Bell peppers", res.Items[0].Text)
}

func TestMergeKeepsEveryMealOnAnItem(t *testing.T) {
	r1, r2 := ptrStr("r1"), ptrStr("r2")
	meals := []grocery.Meal{
		meal("2026-11-16", "dinner", "Fajitas", r1, ptrInt(5), ing("limes", 2.0, "")),
		meal("2026-11-19", "dinner", "Curry", r2, ptrInt(5), ing("lime", 2.0, "")),
	}
	res := grocery.Merge(meals, "2026-11-16", "2026-11-19")
	require.Len(t, res.Items, 1)
	assert.Len(t, res.Items[0].Meals, 2, "both meals are named, so the preview can say why")
	assert.Contains(t, res.Items[0].Meals[0], "Fajitas")
	require.NotNil(t, res.Items[0].Quantity)
	assert.InDelta(t, 4.0, *res.Items[0].Quantity, 1e-9)
}

func TestMergeEmptyRange(t *testing.T) {
	res := grocery.Merge(nil, "2026-11-16", "2026-11-22")
	assert.Empty(t, res.Items)
	assert.Empty(t, res.Skipped)
	assert.Empty(t, res.Notes, "nothing planned is not an error; the page offers to plan a dinner")
	assert.NotNil(t, res.Items, "empty slices marshal as [], not null")
}

func TestMergeSaysWhenServingsAreMissing(t *testing.T) {
	r := ptrStr("r1")
	meals := []grocery.Meal{
		meal("2026-11-16", "dinner", "A", r, nil, ing("rice", 1.0, "cup")),
	}
	res := grocery.Merge(meals, "2026-11-16", "2026-11-16")
	require.NotEmpty(t, res.Notes)
	assert.Contains(t, res.Notes[0], "no servings")
}

func TestMergeCountQuantity(t *testing.T) {
	r := ptrStr("r1")
	meals := []grocery.Meal{
		meal("2026-11-16", "dinner", "A", r, ptrInt(2), ing("eggs", 2.0, "")),
		meal("2026-11-17", "dinner", "B", r, ptrInt(2), ing("egg", 3.0, "")),
	}
	res := grocery.Merge(meals, "2026-11-16", "2026-11-17")
	require.Len(t, res.Items, 1)
	require.NotNil(t, res.Items[0].Quantity)
	assert.InDelta(t, 5.0, *res.Items[0].Quantity, 1e-9)
	assert.Nil(t, res.Items[0].Unit, "a count has no unit")
}
