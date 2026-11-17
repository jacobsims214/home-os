package meals

import (
	"context"
	"regexp"

	"github.com/google/uuid"

	"home-os/allergen"
)

// The allergy engine lives in home-os/allergen now (#4891), because the API
// needs the same one for GET /api/v1/recipes and /recipes/{id} and apps/api
// cannot import an internal package of the MCP module (#4819 did the same for
// the bill due rule). This file is the MCP-side name for it: type aliases and
// one-line wrappers with NO logic, so every tool below — recipe_find,
// recipe_get, recipe_create/_update/_delete, meal_set, meal_rate, agenda_get,
// meal_suggest and the grocery list — reaches the one implementation. A change
// to the vocabulary or the check changes the API's warnings and the
// assistant's exclusions together, which is the property #4803 and #4874 were
// about.
//
// apps/mcp/cmd/mcp/shared_allergy_test.go and the API's
// TestRecipes_SameEngineAsMCP pin this from both modules without either
// importing the other's internal packages.

// allergyMatcher is one person's allergy, compiled.
type allergyMatcher = allergen.Matcher

// The vocabulary: the one list of what a group word means.
var (
	allergenGroups     = allergen.Groups
	allergenAliases    = allergen.Aliases
	allergenExceptions = allergen.Exceptions
	allergyMention     = allergen.Mention
	listSep            = allergen.ListSeparator
)

// noIngredientsLine is the wording the UI renders verbatim, so it is one
// constant and not a sentence written twice.
const noIngredientsLine = allergen.NoIngredientsLine

// The word matching recipe_find's query and ingredient filters are built from.
// It is shared with the allergen matchers so a filter and a warning read a
// phrase the same way (#4805); the filters stay whole-word, which
// TestRecipeFindIngredientFilterUnchanged in packages/allergen pins.
func words(s string) []string  { return allergen.Words(s) }
func singular(w string) string { return allergen.Singular(w) }
func wordForm(w string) string { return allergen.WordForm(w) }

func phrasePatterns(phrase string) (pg, goRe string, ok bool) {
	return allergen.PhrasePatterns(phrase)
}

func groupPatterns(phrase string) (goRe string, ok bool) { return allergen.GroupPatterns(phrase) }

// compilePhrases and compileGroupPhrases compile a phrase list whole-word and
// compound-aware respectively; dietRules is the only caller left (an allergy's
// phrases are compiled inside NewMatcher).
func compilePhrases(phrases []string) []*regexp.Regexp { return allergen.CompilePhrases(phrases) }

func compileGroupPhrases(phrases []string) []*regexp.Regexp {
	return allergen.CompileGroupPhrases(phrases)
}

// newAllergyMatcher builds the regexes for one normalised allergen. ok is false
// when the vocabulary cannot vouch for the phrase, which is #4874's fail-closed
// rule: the caller then reports it in checks instead of matching nothing.
func newAllergyMatcher(personID, person, phrase string) (allergyMatcher, bool) {
	return allergen.NewMatcher(personID, person, phrase)
}

// allergensIn normalises allergy text to the allergens it names, and
// allergyGroupKeys is the group keys one allergen means. Both are the reading
// half of the engine; the shim exposes them because the MCP's own tests assert
// on them.
func allergensIn(text string) []string { return allergen.AllergensIn(text) }

func allergyGroupKeys(phrase string) []string { return allergen.GroupKeys(phrase) }

// allergyMatchers reads one stored allergy into the matchers it means;
// preferenceMatchers is the best-effort variant for a like or a dislike.
func allergyMatchers(personID, person, allergy string) (matchers []allergyMatcher, unchecked []string) {
	return allergen.Matchers(personID, person, allergy)
}

func preferenceMatchers(personID, person, text string) []allergyMatcher {
	return allergen.PreferenceMatchers(personID, person, text)
}

// allergySources and familyAllergies load the family's allergies from both
// places one can be saved (#4803).
func allergySources(ctx context.Context, q querier, hid uuid.UUID, personIDs []string) (matchers []allergyMatcher, checks []string, err error) {
	return allergen.Sources(ctx, q, hid, personIDs)
}

func familyAllergies(ctx context.Context, q querier, hid uuid.UUID) (matchers []allergyMatcher, checks []string, err error) {
	return allergen.FamilyAllergies(ctx, q, hid)
}

// AllergyWarnings lists the ingredients that match someone's allergy;
// recipeAllergyCheck is the one place a recipe is checked (ingredients then
// name, with the no-ingredients caution).
func AllergyWarnings(matchers []allergyMatcher, items []string) []AllergyWarning {
	return allergen.Warnings(matchers, items)
}

func recipeAllergyCheck(matchers []allergyMatcher, title string, ingredients []string) (warnings []AllergyWarning, checks []string) {
	return allergen.RecipeCheck(matchers, title, ingredients)
}

// checkItems is what a recipe is checked against, and the three wording
// helpers are the user-facing phrasing the MCP summaries and the UI share.
func checkItems(title string, ingredients []string) []string {
	return allergen.CheckItems(title, ingredients)
}

func warningLines(ws []AllergyWarning) []string { return allergen.WarningLines(ws) }

func dedupeLines(lines []string) []string { return allergen.DedupeLines(lines) }

func warningSentence(ws []AllergyWarning) string { return allergen.WarningSentence(ws) }

func checkSentence(checks []string) string { return allergen.CheckSentence(checks) }
