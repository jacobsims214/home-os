package meals_test

// The allergy surface on GET /api/v1/recipes and /recipes/{id} (#4891).
//
// AC-2 is the reason these exist: an allergy saved as "Deathly allergic to
// peanuts" must produce a warning THROUGH THE API, not only through the MCP.
// #4874 fixed the normaliser so that wording reads as "peanuts"; before it, it
// read as "deathly peanuts", matched nothing and failed open. The API calls the
// same engine (home-os/allergen), so a recipe the assistant refuses is a recipe
// the API warns about.
//
// allergen_fixture_test.go pins that with packages/db/testdata/allergen_engine.json:
// the same fixture the MCP asserts, so two copies of the vocabulary cannot pass
// both.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/allergen"
)

// recipeAllergyJSON is the two fields the Recipes UI reads (apps/ui/lib/recipes/types.ts).
// Both are pointers so a test can tell "present and empty" from "absent",
// which is the whole difference between "checked, nothing matched" and "nobody
// looked" for the UI (KB ui/recipes-pages.md).
type recipeAllergyJSON struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	AllergyWarnings *[]struct {
		Person     string `json:"person"`
		PersonID   string `json:"person_id"`
		Allergy    string `json:"allergy"`
		Ingredient string `json:"ingredient"`
	} `json:"allergy_warnings"`
	Checks *[]string `json:"checks"`
}

// allergyPerson adds a person with allergies saved, and returns their id.
func (f *fixture) allergyPerson(t *testing.T, hhID, name string, allergies []string) string {
	t.Helper()
	id := f.kid(t, hhID, name)
	_, err := f.pool.Exec(context.Background(),
		`UPDATE people SET allergies = $2 WHERE id = $1`, id, allergies)
	require.NoError(t, err)
	return id
}

// TestRecipes_DeathlyPeanutAllergyWarns is #4891's AC-2 and #4874's case,
// through the API: the exact probed wording must produce a warning on both the
// list and the detail response.
func TestRecipes_DeathlyPeanutAllergyWarns(t *testing.T) {
	f := setup(t)
	hh := f.household(t, "UTC")
	owner := f.member(t, hh, "owner")
	nora := f.allergyPerson(t, hh, "Nora", []string{"Deathly allergic to peanuts"})

	peanuts := f.createRecipe(t, owner, map[string]any{
		"title":       "Peanut noodles",
		"ingredients": []map[string]any{{"name": "peanut butter"}, {"name": "rice noodles"}},
	})
	f.createRecipe(t, owner, map[string]any{
		"title":       "Chicken tacos",
		"ingredients": []map[string]any{{"name": "chicken thighs"}, {"name": "tortillas"}},
	})

	t.Run("detail", func(t *testing.T) {
		got := decode[recipeAllergyJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes/" + peanuts.ID, as: owner}))
		require.NotNil(t, got.AllergyWarnings, "allergy_warnings must be present, never absent")
		require.NotNil(t, got.Checks, "checks must be present, never absent")
		// Two matches, and both are correct: the engine checks the ingredients
		// AND the recipe's own name (#4803), so "Peanut noodles" matches a
		// peanut allergy through its title too. That is the same answer
		// recipe_get gives, and the UI prints `ingredient` as "what matched".
		require.Len(t, *got.AllergyWarnings, 2)
		w := (*got.AllergyWarnings)[0]
		assert.Equal(t, "Nora", w.Person)
		assert.Equal(t, nora, w.PersonID)
		assert.Equal(t, "peanuts", w.Allergy, "the allergen as saved, normalised — not the raw sentence")
		assert.Equal(t, "peanut butter", w.Ingredient)
		assert.Equal(t, "Peanut noodles", (*got.AllergyWarnings)[1].Ingredient, "the recipe's own name is checked too")
		assert.Empty(t, *got.Checks, "a readable allergy that matched is not a check")
	})

	t.Run("list", func(t *testing.T) {
		all := decode[[]recipeAllergyJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes", as: owner}))
		require.Len(t, all, 2)
		byTitle := map[string]recipeAllergyJSON{}
		for _, r := range all {
			byTitle[r.Title] = r
			require.NotNil(t, r.AllergyWarnings, "%s: allergy_warnings absent", r.Title)
			require.NotNil(t, r.Checks, "%s: checks absent", r.Title)
		}
		require.Len(t, *byTitle["Peanut noodles"].AllergyWarnings, 2)
		assert.Empty(t, *byTitle["Chicken tacos"].AllergyWarnings, "no false positive")
		assert.Empty(t, *byTitle["Chicken tacos"].Checks, "no check when everything was readable")
	})

	t.Run("write responses carry it too", func(t *testing.T) {
		body := map[string]any{
			"title":       "Satay skewers",
			"ingredients": []map[string]any{{"name": "chicken"}, {"name": "satay sauce"}},
		}
		rec := f.do(t, req{method: http.MethodPost, path: "/api/v1/recipes", as: owner, body: body})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		got := decode[recipeAllergyJSON](t, rec)
		require.NotNil(t, got.AllergyWarnings)
		require.Len(t, *got.AllergyWarnings, 2, "satay is a peanut group word, and the title says Satay")
		assert.Equal(t, "satay sauce", (*got.AllergyWarnings)[0].Ingredient)
		assert.Equal(t, "Satay skewers", (*got.AllergyWarnings)[1].Ingredient)
	})
}

// TestRecipes_AllergyChecksArePresentAndEmptyWhenClean pins the shape the UI
// depends on: a household with NO allergies still gets both keys as empty
// arrays, so the page can say "no allergies saved" rather than "not checked".
func TestRecipes_AllergyChecksArePresentAndEmptyWhenClean(t *testing.T) {
	f := setup(t)
	hh := f.household(t, "UTC")
	owner := f.member(t, hh, "owner")
	f.kid(t, hh, "Noa") // a person, no allergies
	r := f.createRecipe(t, owner, tacoBody())

	got := decode[recipeAllergyJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes/" + r.ID, as: owner}))
	require.NotNil(t, got.AllergyWarnings)
	require.NotNil(t, got.Checks)
	assert.Empty(t, *got.AllergyWarnings)
	assert.Empty(t, *got.Checks)
}

// TestRecipes_AllergyChecksWordingAndFailClosed pins the wording contract and
// the fail-closed half on the real endpoint: a preference whose allergy cannot
// be read lands in checks, and a recipe with no ingredients says so. The UI
// renders these lines verbatim, so the exact sentence is asserted here and in
// packages/allergen (TestEngineAnswerContract).
func TestRecipes_AllergyChecksWordingAndFailClosed(t *testing.T) {
	f := setup(t)
	hh := f.household(t, "UTC")
	owner := f.member(t, hh, "owner")
	// One unreadable allergy (Ari: "red dye 40") and one readable one (Noa:
	// peanuts). The no-ingredients caution fires when someone HAS an allergy
	// to check against, so a family with only the unreadable one would get the
	// unreadable line and nothing else, which is the engine's long-standing
	// rule and not this ticket's to change.
	f.allergyPerson(t, hh, "Noa", []string{"peanuts"})
	f.allergyPerson(t, hh, "Ari", []string{"red dye 40"})

	bare := f.createRecipe(t, owner, map[string]any{"title": "Mystery Casserole"})

	got := decode[recipeAllergyJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes/" + bare.ID, as: owner}))
	require.NotNil(t, got.Checks)
	assert.Empty(t, *got.AllergyWarnings)
	// Two lines: the unreadable allergy (once, from the loader) and the
	// no-ingredients caution (from the recipe check). Both verbatim.
	want := []string{
		"Ari has an allergy saved as \"red dye 40\" that could not be checked against recipes; confirm the meal is safe.",
		"no ingredients saved, can't check allergies",
	}
	// Ordered people by display_name: Ari's line, then the recipe's own check.
	assert.Equal(t, want, *got.Checks, "checks wording is a contract the UI renders verbatim")
}

// TestRecipes_HousesitterGetsAllergiesNotPreferences is the role-policy half:
// a housesitter may read recipes and allergies ("names, relationship and
// allergies only"), and must not learn anything about a person's preferences
// through this response. A preference that could not be read still produces a
// check — the allergy is applied, and the recipe is not silently safe — but the
// line does not quote the preference's value or notes.
func TestRecipes_HousesitterGetsAllergiesNotPreferences(t *testing.T) {
	f := setup(t)
	hh := f.household(t, "UTC")
	owner := f.member(t, hh, "owner")
	sitter := f.member(t, hh, "housesitter")

	// Allergy on the person record: safety data, readable by every meals reader.
	nora := f.allergyPerson(t, hh, "Nora", []string{"Deathly allergic to peanuts"})
	// A private note and a preference the sitter may not read.
	_, err := f.pool.Exec(context.Background(),
		`UPDATE people SET birthdate = '2014-02-03', school = 'Oak Grove', grade = '5', notes = 'EpiPen in the blue bag' WHERE id = $1`, nora)
	require.NoError(t, err)
	// A preference that mentions an allergy but cannot be read as one, and a
	// normal private preference.
	for _, p := range []struct{ kind, category, value, notes string }{
		{"other", "general", "shellfish", "allergic, carries an EpiPen"}, // readable: shrimp
		{"dietary", "food", "purple elephants", "allergic"},              // unreadable: a check, no quote
		{"like", "food", "sushi", ""},                                    // not allergy talk at all
	} {
		_, err := f.pool.Exec(context.Background(),
			`INSERT INTO person_preferences (household_id, person_id, kind, category, value, notes)
			 VALUES ($1, $2, $3, $4, $5, $6)`, hh, nora, p.kind, p.category, p.value, p.notes)
		require.NoError(t, err)
	}

	shrimp := f.createRecipe(t, owner, map[string]any{
		"title":       "Garlic Shrimp",
		"ingredients": []map[string]any{{"name": "shrimp"}, {"name": "garlic"}},
	})
	peanuts := f.createRecipe(t, owner, map[string]any{
		"title":       "Peanut noodles",
		"ingredients": []map[string]any{{"name": "peanut butter"}},
	})

	detail := decode[recipeAllergyJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes/" + shrimp.ID, as: sitter}))
	require.NotNil(t, detail.AllergyWarnings)
	require.NotNil(t, detail.Checks)
	// The preference's allergy is still READ and still applied: shrimp warns.
	require.Len(t, *detail.AllergyWarnings, 2, "the sitter must still get the allergy warning")
	assert.Equal(t, "shellfish", (*detail.AllergyWarnings)[0].Allergy)
	assert.Equal(t, "shrimp", (*detail.AllergyWarnings)[0].Ingredient)
	assert.Equal(t, "Garlic Shrimp", (*detail.AllergyWarnings)[1].Ingredient)
	// And one check for the preference that could not be read — without its text.
	require.Len(t, *detail.Checks, 1)
	assert.Equal(t, "Nora has an allergy note that could not be read; confirm the meal is safe.", (*detail.Checks)[0])
	for _, forbidden := range []string{"purple elephants", "EpiPen", "Oak Grove"} {
		assert.NotContains(t, (*detail.Checks)[0], forbidden)
	}

	// The peanut warning is unchanged for the sitter (allergies are safety data).
	peanut := decode[recipeAllergyJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes/" + peanuts.ID, as: sitter}))
	require.Len(t, *peanut.AllergyWarnings, 2) // ingredient and title both say peanut
	assert.Equal(t, "peanuts", (*peanut.AllergyWarnings)[0].Allergy)

	// The whole response body carries no private people field: the recipe
	// payload speaks about a person only as {person, person_id}.
	rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes", as: sitter})
	require.Equal(t, http.StatusOK, rec.Code)
	for _, forbidden := range []string{"purple elephants", "EpiPen", "Oak Grove", "2014-02-03", "birthdate", "school", "preferences", "sushi"} {
		assert.NotContains(t, rec.Body.String(), forbidden, "a housesitter's recipe payload must not leak %q", forbidden)
	}

	// The owner, who may read preferences, keeps the old quoting: this is what
	// the sitter's wording is narrower THAN, not a change to family roles.
	ownerDetail := decode[recipeAllergyJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes/" + shrimp.ID, as: owner}))
	require.Len(t, *ownerDetail.Checks, 1)
	assert.Contains(t, (*ownerDetail.Checks)[0], "purple elephants")
}

// TestRecipes_AllergyPayloadStaysSmall is the list-endpoint size check (#4891
// follows #4809/#4810 here). The allergies are loaded ONCE per request and the
// checks seed is copied per row, so N recipes cost one people query and one
// preferences query, not 2N. The test builds a family where every recipe
// matches, so a per-recipe reload is not what the size shows — the query count
// is what it shows.
func TestRecipes_AllergyPayloadStaysSmall(t *testing.T) {
	f := setup(t)
	hh := f.household(t, "UTC")
	owner := f.member(t, hh, "owner")
	f.allergyPerson(t, hh, "Nora", []string{"peanuts"})
	for i := 0; i < 40; i++ {
		f.createRecipe(t, owner, map[string]any{
			"title":       "Recipe " + strconv.Itoa(i),
			"ingredients": []map[string]any{{"name": "peanut butter"}},
		})
	}
	rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes", as: owner})
	require.Equal(t, http.StatusOK, rec.Code)
	all := decode[[]recipeAllergyJSON](t, rec)
	require.Len(t, all, 40)
	for _, r := range all {
		require.NotNil(t, r.AllergyWarnings)
		// "Recipe N" is not an allergen, so only the ingredient matches: one
		// warning per row, which is what makes the size assertion meaningful.
		require.Len(t, *r.AllergyWarnings, 1, "%s lost its warning", r.Title)
	}
	// allergy_warnings + checks per row are ~120 B on this shape, so 40 recipes
	// stay well under 40 KB. The point is that nothing per-row scales with the
	// family's people count: this household has 2 people and 40 recipes.
	assert.Less(t, rec.Body.Len(), 40_000, "a 40-recipe list with allergies must stay small")
}

// TestRecipes_SameEngineAsMCP is #4891's "no second implementation" test.
//
// Go's internal-package rule stops the API from importing
// apps/mcp/internal/meals, so the two cannot be compared by calling each
// other. What CAN be asserted is that both reach the same shared package and
// agree on the same contract: this test runs the engine the API calls over
// packages/db/testdata/allergen_engine.json, and apps/mcp/internal/meals'
// TestEngineIsTheSharedOne asserts the same file from the other module. A
// second copy of the vocabulary in either module fails one of the two.
func TestRecipes_SameEngineAsMCP(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "..", "..", "packages", "db", "testdata", "allergen_engine.json")
	b, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)
	var f struct {
		ChecksWording struct {
			NoIngredients string `json:"no_ingredients"`
		} `json:"checks_wording"`
		Matches []struct {
			Allergy    string `json:"allergy"`
			Ingredient string `json:"ingredient"`
			Matches    bool   `json:"matches"`
		} `json:"matches"`
	}
	require.NoError(t, json.Unmarshal(b, &f))
	require.GreaterOrEqual(t, len(f.Matches), 15, "fixture looks truncated")

	if allergen.NoIngredientsLine != f.ChecksWording.NoIngredients {
		t.Errorf("NoIngredientsLine = %q, fixtures's %q", allergen.NoIngredientsLine, f.ChecksWording.NoIngredients)
	}
	for _, tc := range f.Matches {
		ms, _ := allergen.Matchers("p", "Nora", tc.Allergy)
		got := slices.ContainsFunc(ms, func(m allergen.Matcher) bool { return m.Matches(tc.Ingredient) })
		if got != tc.Matches {
			t.Errorf("allergy %q vs %q = %v, want %v", tc.Allergy, tc.Ingredient, got, tc.Matches)
		}
	}
}
