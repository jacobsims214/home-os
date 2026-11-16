package allergen

// Checking a recipe against the family's allergies, and the words for saying
// so. RecipeCheck is the one place a recipe's allergies are checked (#4803), so
// the API's recipe list and recipe_get, meal_set, agenda_get and meal_suggest
// cannot disagree. The wording helpers are a user-facing contract: the Recipes
// UI renders Checks verbatim (ui/recipes-pages.md), so NoIngredientsLine and
// UncheckedAllergyLine must keep their exact sentences.

import (
	"fmt"
	"slices"
	"strings"
)

// Warnings lists the ingredients that match someone's allergy.
func Warnings(matchers []Matcher, items []string) []Warning {
	out := []Warning{}
	for _, m := range matchers {
		for _, it := range items {
			if m.Matches(it) {
				out = append(out, Warning{Person: m.Person, PersonID: m.PersonID, Allergy: m.Allergy, Ingredient: it})
			}
		}
	}
	return out
}

// UnreadableAllergyLine is the check a caller gets for a preference whose
// allergen could not be read when the caller may NOT see preference text
// (allergen.Options.QuotePreferenceText false, i.e. a housesitter). It names
// the person and says the allergy could not be checked, which is the fact the
// reader needs, and it deliberately says nothing about which preference said
// it — the role policy lets a housesitter see names, relationship and
// allergies, not preferences (decisions/2026-11-15-role-policy.md).
func UnreadableAllergyLine(person string) string {
	return fmt.Sprintf("%s has an allergy note that could not be read; confirm the meal is safe.", person)
}

// NoIngredientsLine is the one wording for "this recipe has no ingredients
// saved, so its allergies could only be checked against its name".
// meal_suggest excludes such a recipe when anyone eating has an allergy (fail
// closed: an unknown recipe is not a safe recipe), and the recipe and meal
// tools report the same sentence in checks. One constant, so they cannot drift
// apart (#4803).
const NoIngredientsLine = "no ingredients saved, can't check allergies"

// CheckItems is what a recipe's allergies and diets are checked against: its
// ingredients, then its name. One helper, so meal_suggest and the recipe and
// meal warnings look at exactly the same text (#4803).
func CheckItems(title string, ingredients []string) []string {
	items := slices.Clone(ingredients)
	if strings.TrimSpace(title) != "" {
		items = append(items, title)
	}
	return items
}

// RecipeCheck is the one place a recipe's allergies are checked, so
// recipe_find, recipe_get, meal_set, agenda_get and meal_suggest cannot
// disagree (#4803). It checks the ingredients and then the recipe's name, and
// reports the no-ingredients caution when the name is all there is to check.
func RecipeCheck(matchers []Matcher, title string, ingredients []string) (warnings []Warning, checks []string) {
	// One person's allergy can match the same item through two matchers
	// ("peanuts and nuts"), so drop the repeats here, once, for every caller.
	seen := map[Warning]bool{}
	for _, w := range Warnings(matchers, CheckItems(title, ingredients)) {
		if !seen[w] {
			seen[w] = true
			warnings = append(warnings, w)
		}
	}
	if len(ingredients) == 0 && len(matchers) > 0 {
		checks = []string{NoIngredientsLine}
	}
	return warnings, checks
}

func WarningLines(ws []Warning) []string {
	var out []string
	for _, w := range ws {
		out = append(out, fmt.Sprintf("%s (%s): %s", w.Person, w.Allergy, w.Ingredient))
	}
	return out
}

// DedupeLines drops repeats, keeping the first: one person's allergy can match
// the same ingredient through two matchers ("peanuts and tree nuts").
func DedupeLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

func WarningSentence(ws []Warning) string {
	if len(ws) == 0 {
		return ""
	}
	return " Allergy warning: " + strings.Join(WarningLines(ws), "; ") + "."
}

// CheckSentence says which allergies could not be checked at all, for the
// summaries that have no structured checks field to put them in.
func CheckSentence(checks []string) string {
	if len(checks) == 0 {
		return ""
	}
	s := " " + strings.Join(checks, " ")
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}
