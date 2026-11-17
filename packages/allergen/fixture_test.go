package allergen_test

// The engine's contract with the two apps that call it (#4891).
//
// The vocabulary lives once, in this package, and both apps/mcp/internal/meals
// and apps/api/internal/meals reach it through a thin alias shim. This test
// reads packages/db/testdata/allergen_engine.json and asserts the engine's
// answer for every probed case. The same fixture is asserted from each app
// (apps/api/internal/meals/allergen_fixture_test.go and
// apps/mcp/internal/meals/shared_allergy_test.go), so:
//
//   - a second copy of the vocabulary in either app fails that app's test;
//   - any change to the rule that one app sees and the other does not fails
//     one of the two, and the wording contract in the fixture fails here.
//
// It also pins the exact user-facing sentences. `checks` is rendered VERBATIM
// by the Recipes UI (KB ui/recipes-pages.md), so rewording NoIngredientsLine or
// UncheckedAllergyLine is a contract break and not a tidy-up.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"home-os/allergen"
)

type engineFixture struct {
	ChecksWording struct {
		NoIngredients    string `json:"no_ingredients"`
		UncheckedAllergy string `json:"unchecked_allergy"`
	} `json:"checks_wording"`
	Matches []struct {
		Allergy    string `json:"allergy"`
		Ingredient string `json:"ingredient"`
		Matches    bool   `json:"matches"`
	} `json:"matches"`
	RecipeCheck struct {
		Title        string   `json:"title"`
		Ingredients  []string `json:"ingredients"`
		Allergy      string   `json:"allergy"`
		WantWarnings []struct {
			Person     string `json:"person"`
			Allergy    string `json:"allergy"`
			Ingredient string `json:"ingredient"`
		} `json:"want_warnings"`
		WantChecks []string `json:"want_checks"`
	} `json:"recipe_check"`
	NoIngredientsCheck struct {
		Title        string   `json:"title"`
		Ingredients  []string `json:"ingredients"`
		Allergy      string   `json:"allergy"`
		WantWarnings []struct {
			Person     string `json:"person"`
			Allergy    string `json:"allergy"`
			Ingredient string `json:"ingredient"`
		} `json:"want_warnings"`
		WantChecks []string `json:"want_checks"`
	} `json:"no_ingredients_check"`
}

// readEngineFixture loads packages/db/testdata/allergen_engine.json. It is
// exported for the two apps' tests through their own copy of this loader (each
// module reads the file itself, since neither may import the other's internal
// package).
func readEngineFixture(t *testing.T) engineFixture {
	t.Helper()
	path := filepath.Join("..", "..", "packages", "db", "testdata", "allergen_engine.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var f engineFixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(f.Matches) < 15 || len(f.RecipeCheck.WantWarnings) == 0 {
		t.Fatalf("%s looks truncated: %d matches", path, len(f.Matches))
	}
	return f
}

// TestEngineAnswerContract pins the engine's answers from the package that
// holds the rule. The apps assert the same file; see this file's header.
func TestEngineAnswerContract(t *testing.T) {
	t.Parallel()
	f := readEngineFixture(t)

	if allergen.NoIngredientsLine != f.ChecksWording.NoIngredients {
		t.Errorf("NoIngredientsLine = %q, want the UI's contract %q", allergen.NoIngredientsLine, f.ChecksWording.NoIngredients)
	}
	if got := allergen.UncheckedAllergyLine("Nora", "red dye 40"); got != fmt.Sprintf(f.ChecksWording.UncheckedAllergy, "Nora", "red dye 40") {
		t.Errorf("UncheckedAllergyLine = %q, want the UI's contract", got)
	}

	for _, tc := range f.Matches {
		ms, _ := allergen.Matchers("p", "Nora", tc.Allergy)
		got := slices.ContainsFunc(ms, func(m allergen.Matcher) bool { return m.Matches(tc.Ingredient) })
		if got != tc.Matches {
			t.Errorf("allergy %q vs %q = %v, want %v", tc.Allergy, tc.Ingredient, got, tc.Matches)
		}
	}

	for _, rc := range []struct {
		name        string
		title       string
		ingredients []string
		allergy     string
		wantWarn    int
		wantChecks  []string
	}{
		{"recipe_check", f.RecipeCheck.Title, f.RecipeCheck.Ingredients, f.RecipeCheck.Allergy,
			len(f.RecipeCheck.WantWarnings), f.RecipeCheck.WantChecks},
		{"no_ingredients_check", f.NoIngredientsCheck.Title, f.NoIngredientsCheck.Ingredients, f.NoIngredientsCheck.Allergy,
			len(f.NoIngredientsCheck.WantWarnings), f.NoIngredientsCheck.WantChecks},
	} {
		ms, seed := allergen.Matchers("p", "Nora", rc.allergy)
		warnings, checks := allergen.RecipeCheck(ms, rc.title, rc.ingredients)
		if len(warnings) != rc.wantWarn {
			t.Errorf("%s: %d warnings, want %d (%+v)", rc.name, len(warnings), rc.wantWarn, warnings)
		}
		all := append(append([]string{}, seed...), checks...)
		if !slices.Equal(all, rc.wantChecks) {
			t.Errorf("%s: checks = %q, want %q", rc.name, all, rc.wantChecks)
		}
	}
	// The warning carries who/what/which ingredient, which is what both the
	// API's allergy_warnings and the MCP's warning line mean.
	ms, _ := allergen.Matchers("p", "Nora", f.RecipeCheck.Allergy)
	warnings, _ := allergen.RecipeCheck(ms, f.RecipeCheck.Title, f.RecipeCheck.Ingredients)
	want := f.RecipeCheck.WantWarnings[0]
	if len(warnings) == 0 || warnings[0].Person != want.Person || warnings[0].Allergy != want.Allergy || warnings[0].Ingredient != want.Ingredient {
		t.Errorf("warning = %+v, want %+v", warnings, want)
	}
}
