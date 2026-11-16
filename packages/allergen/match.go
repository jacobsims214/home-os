package allergen

// Turning stored allergy text into matchers, and matching ingredient names
// against them. #4874's fail-closed rule lives here: a phrase the vocabulary
// cannot vouch for yields NO matcher and is reported back to the caller, so it
// lands in checks instead of silently matching nothing.

import (
	"fmt"
	"slices"
	"strings"
)

// KnownPhrase reports whether the vocabulary can vouch for an allergen.
// A single word always can: it is its own phrase, and "strawberry" is a real
// allergy whether or not a group lists it. A multi-word phrase has to be a
// food the vocabulary already names, in one of the four places it names them:
// a group key ("tree nuts"), an alias ("cow's milk", "sesame seeds"), a food
// listed in a group ("fish sauce", "sea bass") or a food listed in an
// exception ("peanut butter", "coconut milk" — words that do not carry the
// group allergen, but that people do save as an allergy). Anything else is
// opaque: "deathly peanuts" compiles to a regex that matches no ingredient,
// and reading that as "this person has no allergy" is how a peanut recipe gets
// suggested to a peanut-allergic person (#4874).
//
// Accepting an exception word is safe in the only direction that matters: its
// matcher is the literal phrase and nothing else (an exception word has no
// group key), so it can over-warn on that one dish and can never clear a real
// allergen.
func KnownPhrase(allergen string) bool {
	key := AllergyKey(allergen)
	if !strings.Contains(key, " ") {
		return true
	}
	if _, ok := Groups[key]; ok {
		return true
	}
	if _, ok := Aliases[key]; ok {
		return true
	}
	for _, foods := range []map[string][]string{Groups, Exceptions} {
		for _, list := range foods {
			for _, food := range list {
				if AllergyKey(food) == key {
					return true
				}
			}
		}
	}
	return false
}

// GroupKeys is the group keys an allergen means: its own key and, when
// it is a synonym, the groups it stands for ("crustaceans" -> crustacean,
// "seafood" -> fish and shellfish). One lookup over
// Groups/Aliases, so a synonym is data and not a second
// matcher (#4804).
func GroupKeys(allergen string) []string {
	key := AllergyKey(allergen)
	keys := []string{key}
	for _, alias := range Aliases[key] {
		if !slices.Contains(keys, alias) {
			keys = append(keys, alias)
		}
	}
	return keys
}

// Matchers turns one stored allergy into the matchers it means. The
// entry is read with AllergensIn first, so ordinary wording works: "Shrimp
// allergy" matches shrimp and "allergic to peanuts" matches peanuts. One
// entry can mean several allergens ("peanuts and tree nuts"), and an entry
// that normalises to nothing gives no matcher at all, so the caller can say
// it could not be checked instead of silently matching nothing (#4802).
//
// unchecked holds the fragments the matcher would not vouch for, so a caller
// can report them. It is per fragment, not per entry: "peanuts, red dye 40"
// keeps the peanut matcher and still surfaces the half it could not read
// (#4874).
func Matchers(personID, person, allergy string) (matchers []Matcher, unchecked []string) {
	for _, a := range AllergensIn(allergy) {
		if m, ok := NewMatcher(personID, person, a); ok {
			matchers = append(matchers, m)
		} else {
			unchecked = append(unchecked, a)
		}
	}
	return matchers, unchecked
}

// PreferenceMatchers reads text that is a like or a dislike rather than an
// allergy. It is best-effort, so a phrase the vocabulary does not know ("ice
// cream") keeps its matcher: a dislike that matched nothing is a ranking
// detail, and there is no checks list to report it in. Only allergy text goes
// through the fail-closed Matchers (#4874).
func PreferenceMatchers(personID, person, text string) []Matcher {
	var out []Matcher
	for _, a := range AllergensIn(text) {
		if m, _ := NewMatcher(personID, person, a); len(m.Own) > 0 {
			out = append(out, m)
		}
	}
	return out
}

// UncheckedAllergyLine says in one line that a saved allergy could not be
// matched against anything. meal_suggest's checks and the recipe and meal
// warnings use it, so the same problem reads the same everywhere.
func UncheckedAllergyLine(person, allergy string) string {
	return fmt.Sprintf("%s has an allergy saved as %q that could not be checked against recipes; confirm the meal is safe.", person, allergy)
}

func (m Matcher) Matches(item string) bool {
	for _, re := range m.Own {
		if re.MatchString(item) {
			return true
		}
	}
	for _, re := range m.Except {
		item = re.ReplaceAllString(item, " ")
	}
	for _, re := range m.Group {
		if re.MatchString(item) {
			return true
		}
	}
	return false
}
