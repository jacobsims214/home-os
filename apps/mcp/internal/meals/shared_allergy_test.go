package meals

// TestEngineIsTheSharedOne is #4891's "no second implementation" test on the
// MCP side.
//
// The vocabulary, the normaliser and the check moved to packages/allergen
// because the API needs them too, and this package is now aliases and one-line
// wrappers. Two things are asserted here:
//
//  1. This package's names ARE the shared package's names (type identity, and
//     a same-answer check), so nothing can grow a private copy behind them.
//  2. The answers match packages/db/testdata/allergen_engine.json, the same
//     fixture apps/api/internal/meals/TestRecipes_SameEngineAsMCP asserts. A
//     second vocabulary in either module fails one of the two tests.
//
// Go's internal-package rule stops the API importing this package (and the
// reverse), so the fixture is how the two are compared without linking.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"home-os/allergen"
)

func TestEngineIsTheSharedOne(t *testing.T) {
	t.Parallel()

	// 1. Identity: these must be the shared types, not lookalikes. A local
	//    re-declaration would fail to compile here.
	var sharedMatcher allergen.Matcher
	var localMatcher allergyMatcher = sharedMatcher
	_ = localMatcher
	if reflect.TypeOf(allergyMatcher{}) != reflect.TypeOf(allergen.Matcher{}) {
		t.Fatalf("meals.allergyMatcher is %v, not allergen.Matcher", reflect.TypeOf(allergyMatcher{}))
	}
	var sharedWarning allergen.Warning
	var localWarning AllergyWarning = sharedWarning
	_ = localWarning
	if reflect.TypeOf(AllergyWarning{}) != reflect.TypeOf(allergen.Warning{}) {
		t.Fatalf("meals.AllergyWarning is %v, not allergen.Warning", reflect.TypeOf(AllergyWarning{}))
	}
	// The vocabulary variables must BE the shared maps (same header pointer),
	// so a local copy cannot shadow one.
	if reflect.ValueOf(allergenGroups).Pointer() != reflect.ValueOf(allergen.Groups).Pointer() {
		t.Error("allergenGroups is a copy, not allergen.Groups")
	}
	if reflect.ValueOf(allergenExceptions).Pointer() != reflect.ValueOf(allergen.Exceptions).Pointer() {
		t.Error("allergenExceptions is a copy, not allergen.Exceptions")
	}
	if reflect.ValueOf(allergenAliases).Pointer() != reflect.ValueOf(allergen.Aliases).Pointer() {
		t.Error("allergenAliases is a copy, not allergen.Aliases")
	}

	// 2. Same answers, through this package's wrapper and through the shared
	//    package directly, and both equal to the fixture the API asserts.
	path := filepath.Join("..", "..", "..", "..", "packages", "db", "testdata", "allergen_engine.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
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
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(f.Matches) < 15 {
		t.Fatalf("%s looks truncated: %d matches", path, len(f.Matches))
	}
	if noIngredientsLine != allergen.NoIngredientsLine || noIngredientsLine != f.ChecksWording.NoIngredients {
		t.Errorf("noIngredientsLine = %q, shared = %q, fixture = %q",
			noIngredientsLine, allergen.NoIngredientsLine, f.ChecksWording.NoIngredients)
	}

	for _, tc := range f.Matches {
		viaShim, _ := allergyMatchers("p", "Nora", tc.Allergy)
		direct, _ := allergen.Matchers("p", "Nora", tc.Allergy)
		gotShim := slices.ContainsFunc(viaShim, func(m allergyMatcher) bool { return m.Matches(tc.Ingredient) })
		gotDirect := slices.ContainsFunc(direct, func(m allergen.Matcher) bool { return m.Matches(tc.Ingredient) })
		if gotShim != tc.Matches || gotDirect != tc.Matches {
			t.Errorf("allergy %q vs %q: shim %v, shared %v, fixture wants %v",
				tc.Allergy, tc.Ingredient, gotShim, gotDirect, tc.Matches)
		}
	}

	// The check itself agrees: same warnings and checks from both paths.
	shimMs, shimSeed := allergyMatchers("p", "Nora", "Deathly allergic to peanuts")
	wantW, wantC := allergen.RecipeCheck(shimMs, "Satay Bowl", []string{"peanuts", "rice noodles"})
	gotW, gotC := recipeAllergyCheck(shimMs, "Satay Bowl", []string{"peanuts", "rice noodles"})
	if !slices.Equal(wantW, gotW) || !slices.Equal(wantC, gotC) || len(shimSeed) != 0 {
		t.Errorf("recipeAllergyCheck = %+v/%q, allergen.RecipeCheck = %+v/%q", gotW, gotC, wantW, wantC)
	}
}
