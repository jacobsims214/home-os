package allergen_test

// The engine's own unit suite, moved with the rule (#4891): the vocabulary, the
// normaliser's #4874 fail-closed behaviour, the compound matching of #4805 and
// the split/label rules of #4802. It lives beside the implementation now, so a
// change to the vocabulary or a matcher is tested by the package both the API
// and the MCP call, and neither app can carry its own divergent expectation.

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"home-os/allergen"
)

func TestWordForms(t *testing.T) {
	for _, tc := range []struct {
		phrase, text string
		want         bool
	}{
		{"tomatoes", "cherry tomato", true},
		{"tomato", "Tomatoes, diced", true},
		{"egg", "eggs", true},
		{"egg", "eggplant", false},
		{"berries", "blueberry", false},
		{"berries", "mixed berry", true},
		{"green onions", "green onion", true},
		{"rice", "licorice", false},
		{"cheese", "cream cheese", true},
	} {
		_, goRe, _ := allergen.PhrasePatterns(tc.phrase)
		m := mustMatch(goRe, tc.text)
		if m != tc.want {
			t.Errorf("%q vs %q = %v, want %v", tc.phrase, tc.text, m, tc.want)
		}
	}
}
func mustMatch(re, s string) bool { return regexp.MustCompile(re).MatchString(s) }
func TestAllergyMatcher(t *testing.T) {
	for _, tc := range []struct {
		allergy, item string
		want          bool
	}{
		{"peanuts", "peanut butter", true},
		{"Peanut allergy", "Peanuts, chopped", true},
		{"tree nuts", "walnuts", true},
		{"tree nuts", "coconut milk", false},
		{"dairy", "unsalted butter", true},
		{"Dairy", "parmesan", true},
		{"gluten", "all-purpose flour", true},
		{"shellfish", "shrimp", true},
		{"eggs", "mayonnaise", true},
		{"eggs", "eggplant", false},
		{"strawberries", "strawberry jam", true},
		{"sesame", "tahini", true},
		{"fish", "chicken", false},
		{"dairy", "peanut butter", false},
		{"dairy", "almond milk", false},
		{"tree nuts", "almond milk", true},
		{"milk", "coconut milk", true}, // the allergy's own word always matches
		{"nuts", "nutmeg", false},
		{"nuts", "butternut squash", false},
		// Stored notes (#4742) are not part of the allergen.
		{"peanuts (carries an EpiPen)", "peanut butter", true},
		{"peanuts (carries an EpiPen)", "satay sauce", true},
		{"tree nuts (cashews, in backpack)", "walnuts", true},
		{"Dairy (hives)", "cheddar", true},
		// #4802: the same normaliser preference text gets, so ordinary
		// wording of an allergy still matches. Every one of these matched
		// nothing before.
		{"Shrimp allergy", "shrimp", true},
		{"Shrimp allergy", "Garlic Shrimp", true},
		{"strawberry allergy", "fresh strawberries", true},
		{"Severe peanut allergy", "peanut butter", true},
		{"allergic to peanuts", "peanuts", true},
		{"allergic to peanuts", "peanut butter", true},
		{"Peanuts - anaphylaxis", "peanuts", true},
		{"peanuts \u2013 anaphylaxis", "roasted peanuts", true},
		{"peanuts: EpiPen", "roasted peanuts", true},
		{"peanuts and tree nuts", "cashews", true},
		{"peanuts and tree nuts", "peanut butter", true},
		{"tree nuts; peanuts", "walnuts", true},
		{"Allergic to shrimp & crab", "crab cakes", true},
		// Worded allergies still leave other foods alone.
		{"allergic to peanuts", "chicken thighs", false},
		{"Shrimp allergy", "shrimp stock paste", true},
	} {
		if got := anyMatch(tc.allergy, tc.item); got != tc.want {
			t.Errorf("allergy %q vs %q = %v, want %v", tc.allergy, tc.item, got, tc.want)
		}
	}
}

// TestAllergyIntensifiersAndFailClosed is #4874's unit acceptance test. The
// #4802-#4805 chain fixed the vocabulary; it did not fix "the matcher
// silently succeeds on garbage". Two holes, both proved end to end on real
// Postgres: allergyFiller missed intensifiers and connectors, so "Deathly
// allergic to peanuts" normalised to "deathly peanuts"; and allergen.NewMatcher
// said ok whenever a phrase had any word, so that junk phrase compiled,
// matched nothing and never reached checks. A peanut-allergic person was
// suggested a peanut recipe with an empty warnings list.
//
// Every wording on the ticket has to end in one of two states, never a third:
// a real matcher that excludes the food, or no matcher and a visible
// unchecked-allergy line.
func TestAllergyIntensifiersAndFailClosed(t *testing.T) {
	// The probed wordings, with the allergen each must normalise to and a food
	// it must then match. Getting the allergen right is the filler half.
	for _, tc := range []struct {
		in      string
		want    []string
		matches string
	}{
		{"Deathly allergic to peanuts", []string{"peanuts"}, "peanut butter"},
		{"extremely allergic to dairy", []string{"dairy"}, "cheddar"},
		{"highly allergic to eggs", []string{"eggs"}, "mayonnaise"},
		{"epi-pen for peanuts", []string{"peanuts"}, "peanuts"},
		{"allergic to peanuts, wears medicalert", []string{"peanuts"}, "peanut butter"},
		// The rest of the filler family, so a new word is a test row and not a
		// silent regression.
		{"deadly peanut allergy", []string{"peanut"}, "satay"},
		{"really badly allergic to shellfish", []string{"shellfish"}, "prawns"},
		{"strongly allergic to sesame", []string{"sesame"}, "tahini"},
		{"possibly allergic to soy", []string{"soy"}, "tofu"},
		{"allergic to all tree nuts", []string{"tree nuts"}, "cashews"},
		{"carries medication for peanuts", []string{"peanuts"}, "peanuts"},
		{"allergic to peanuts and wears a medical alert bracelet", []string{"peanuts"}, "peanut butter"},
		{"allergic to peanuts, wears a medicalert bracelet", []string{"peanuts"}, "peanut butter"},
	} {
		if got := allergen.AllergensIn(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("allergen.AllergensIn(%q) = %q, want %q", tc.in, got, tc.want)
		}
		ms, unchecked := allergen.Matchers("p", "Nora", tc.in)
		if len(unchecked) != 0 {
			t.Errorf("%q: unchecked = %q, want a usable matcher", tc.in, unchecked)
		}
		if !slices.ContainsFunc(ms, func(m allergen.Matcher) bool { return m.Matches(tc.matches) }) {
			t.Errorf("%q did not match %q", tc.in, tc.matches)
		}
	}

	// The fail-closed half: a multi-word phrase the vocabulary does not know
	// gets no matcher and is reported, instead of compiling into a regex that
	// matches nothing. This is what makes "the matcher silently succeeds on
	// garbage" impossible.
	for _, junk := range []string{
		"peanuts nuts seeds",   // words, but not a phrase the vocabulary knows
		"red dye 40",           // a real allergy the vocabulary has no words for
		"something in the air", // plainly unreadable
		"purple elephants",     // nothing food-shaped in it at all
	} {
		ms, unchecked := allergen.Matchers("p", "Nora", junk)
		if len(ms) != 0 {
			t.Errorf("allergen.Matchers(%q) gave %d matchers, want none (fail closed)", junk, len(ms))
		}
		if len(unchecked) == 0 {
			t.Errorf("allergen.Matchers(%q) reported nothing unchecked, want the phrase", junk)
		}
	}

	// The guard itself, straight at allergen.NewMatcher with the pre-normalised
	// phrase: these are the exact strings the old code compiled and called
	// clean. The filler now means stored text cannot produce them, and this
	// keeps them from ever counting as a checked allergy again.
	for _, junk := range []string{
		"deathly peanuts",        // what "Deathly allergic to peanuts" used to become
		"extremely dairy",        // ... and "extremely allergic to dairy"
		"highly eggs",            // ... and "highly allergic to eggs"
		"for peanuts",            // ... and "epi-pen for peanuts"
		"wears medicalert",       // ... and "allergic to peanuts, wears medicalert"
		"purple elephants",       // anything else with no food in it
		"all of the tree things", // plural nonsense
	} {
		if m, ok := allergen.NewMatcher("p", "Nora", junk); ok {
			t.Errorf("allergen.NewMatcher(%q) = ok, want fail closed (%d own, %d group)", junk, len(m.Own), len(m.Group))
		}
	}

	// A real allergen the vocabulary does not know is still one word, so it
	// keeps working: fail-closed is about opaque phrases, not unknown foods.
	for _, oneWord := range []string{"strawberry", "kiwi", "mango", "cinnamon"} {
		ms, unchecked := allergen.Matchers("p", "Nora", oneWord)
		if len(ms) != 1 || len(unchecked) != 0 {
			t.Errorf("allergen.Matchers(%q) = %d matchers / %q unchecked, want 1 and none", oneWord, len(ms), unchecked)
		}
	}

	// True multi-word allergens must keep resolving through group keys and
	// aliases (#4874 says explicitly: do not break these).
	for _, tc := range []struct {
		in      string
		matches string
	}{
		{"tree nuts", "cashews"},
		{"Tree nut allergies", "walnuts"},
		{"sesame seeds", "tahini"},
		{"cow's milk", "cheddar"},
		{"cows milk", "butter"},
		{"cow\u2019s milk", "yogurt"},
		{"dairy products", "mascarpone"},
		{"lactose intolerance", "milk"},
		{"celiac disease", "penne"},
		{"seafood", "salmon"},
		{"fish sauce", "fish sauce"}, // a group food, not a group key
		{"sea bass", "sea bass"},
		{"pine nut", "pine nuts"},
		{"brazil nut", "brazil nuts"},
		{"peanuts and tree nuts", "peanut butter"},
		// Exception words are foods too, and people do save them as an
		// allergy. Their matcher is the literal phrase only, so they can
		// over-warn on that dish but never clear a real allergen.
		{"coconut milk", "coconut milk"},
		{"peanut butter", "peanut butter"},
		{"root beer", "root beer"},
	} {
		ms, unchecked := allergen.Matchers("p", "Nora", tc.in)
		if len(ms) == 0 || len(unchecked) != 0 {
			t.Errorf("%q: %d matchers / unchecked %q, want a matcher", tc.in, len(ms), unchecked)
		}
		if !slices.ContainsFunc(ms, func(m allergen.Matcher) bool { return m.Matches(tc.matches) }) {
			t.Errorf("%q did not match %q", tc.in, tc.matches)
		}
	}

	// A partly readable entry keeps the half it can read and reports the rest.
	ms, unchecked := allergen.Matchers("p", "Nora", "peanuts, red dye 40")
	if len(ms) != 1 || !slices.Equal(unchecked, []string{"red dye 40"}) {
		t.Errorf("mixed entry = %d matchers / unchecked %q, want 1 and [red dye 40]", len(ms), unchecked)
	}
	if !ms[0].Matches("peanut butter") {
		t.Error("the readable half of a mixed entry stopped matching")
	}
}

// TestPreferenceMatchersStayBestEffort pins the other side of #4874: a like or
// a dislike is ranking data with no checks list to report in, so an unknown
// phrase keeps its matcher. Only allergy text fails closed.
func TestPreferenceMatchersStayBestEffort(t *testing.T) {
	for _, tc := range []struct {
		text, item string
	}{
		{"ice cream", "vanilla ice cream"},
		{"root beer", "root beer"},
		{"red dye 40", "red dye 40"},
	} {
		if !slices.ContainsFunc(allergen.PreferenceMatchers("p", "Sam", tc.text), func(m allergen.Matcher) bool { return m.Matches(tc.item) }) {
			t.Errorf("allergen.PreferenceMatchers(%q) did not match %q", tc.text, tc.item)
		}
	}
}

// anyMatch is how a stored allergy is used everywhere now: read the text
// with allergen.AllergensIn, then match each allergen it gives.
func anyMatch(allergy, item string) bool {
	ms, _ := allergen.Matchers("p", "Nora", allergy)
	for _, m := range ms {
		if m.Matches(item) {
			return true
		}
	}
	return false
}

// TestCompoundIngredients is #4805's acceptance test: an allergen group word
// that sits inside a one-word ingredient still matches. Before it, every word
// was wrapped in \b...\b, so "catfish fillets" carried no fish, "cornbread" no
// bread and "cheesecake" no cheese: a fish-allergic eater could be offered
// Blackened Catfish with no warning at all.
//
// It goes through anyMatch, the path a stored allergy takes, so it only passes
// when the whole chain agrees: allergen.AllergensIn, the group key, the group word and
// the compiled compound regex.
func TestCompoundIngredients(t *testing.T) {
	for _, tc := range []struct {
		allergy, item string
		want          bool
		why           string
	}{
		// -- the ticket's four, end to end --
		{"fish", "catfish fillets", true, "catfish"},
		{"fish", "swordfish steaks", true, "swordfish"},
		{"gluten", "cornbread", true, "cornbread"},
		{"dairy", "cheesecake", true, "cheesecake"},

		// -- suffix words: fish, bread, and what they catch --
		{"fish", "catfish", true, "catfish"},
		{"fish", "monkfish", true, "monkfish"},
		{"fish", "whitefish", true, "whitefish"},
		{"fish", "blackened catfish", true, "an adjective, then the compound"},
		{"Seafood allergy", "catfish fillets", true, "seafood alias -> fish group"},
		{"shellfish", "shellfish", true, "the word itself still matches"},
		{"gluten", "cornbread stuffing", true, "cornbread"},
		{"gluten", "shortbread", true, "shortbread"},
		{"gluten", "flatbread", true, "flatbread"},
		{"celiac", "cornbread", true, "celiac is a gluten allergy"},
		{"wheat", "cornbread", true, "wheat gets the grain group too"},
		{"gluten", "breaded chicken", true, "breaded is bread on the head"},
		{"gluten", "breadsticks", true, "breadstick"},
		{"gluten", "breadcrumbs", true, "breadcrumb"},

		// -- prefix words: cheese, egg, milk, butter, corn --
		{"dairy", "cheesecake", true, "cheesecake"},
		{"dairy", "cheeseburger", true, "cheeseburger"},
		{"dairy", "cheesesteak", true, "cheesesteak"},
		{"milk allergy", "cheesecake", true, "milk -> dairy -> cheese"},
		{"lactose intolerance", "cheesecake", true, "lactose -> dairy -> cheese"},
		{"dairy products", "cheesecake", true, "dairy products alias"},
		{"egg", "eggnog", true, "eggnog"},
		{"egg", "egg rolls", true, "egg roll (the space form already worked)"},
		{"milk", "milkshake", true, "milkshake"},
		{"dairy", "butterscotch", true, "butterscotch"},
		{"dairy", "buttercream", true, "buttercream"},
		{"dairy", "buttermilk", true, "buttermilk"},
		{"corn", "cornbread", true, "cornbread carries corn"},
		{"corn", "popcorn", true, "popcorn"},
		{"corn", "cornstarch", true, "cornstarch"},
		{"corn allergy", "cornbread", true, "the worded form"},
		{"corn-free", "cornbread", true, "a corn-free diet"},

		// -- the negatives: a compound must not turn a word into a match --
		{"egg", "eggplant", false, "eggplant is not egg"},
		{"fish", "goldfish crackers", false, "goldfish crackers are not fish"},
		{"fish", "jellyfish", false, "a jellyfish is not a fish"},
		{"fish", "starfish", false, "a starfish is not a fish"},
		{"nut", "coconut", false, "coconut is not a nut, compound or not"},
		{"nut", "nutmeg", false, "nutmeg is not a nut"},
		{"nut", "doughnut", false, "doughnut is not a nut"},
		{"nut", "butternut squash", false, "butternut is not a nut"},
		{"tree nuts", "coconut milk", false, "coconut is still not a tree nut"},
		{"dairy", "peanut butter", false, "peanut butter is not dairy"},
		{"dairy", "butterfly pasta", false, "butterfly is not butter"},
		{"dairy", "butternut squash", false, "butternut is not butter"},
		{"dairy", "cheesecloth", false, "cheesecloth is cloth"},
		{"milk", "milkfish", false, "milkfish is a fish, not milk"},
		{"gluten", "breadfruit", false, "breadfruit carries no gluten"},
		{"corn", "unicorn cookies", false, "unicorn is not corn"},
		{"gluten", "root beer", false, "an exception still wins"},
		{"dairy", "almond milk", false, "an exception still wins"},
		{"fish", "chicken thighs", false, "chicken is not fish"},
		{"dairy", "chicken thighs", false, "chicken is not dairy"},

		// -- and an allergy's own word is never compound-matched --
		{"catfish", "catfish fillets", true, "the allergy's own word"},
		{"catfish", "swordfish steaks", false, "catfish is not a swordfish"},
		{"cheese", "cheesecake", false, "the own word is whole-word only"},
	} {
		if got := anyMatch(tc.allergy, tc.item); got != tc.want {
			t.Errorf("%s: allergy %q vs %q = %v, want %v", tc.why, tc.allergy, tc.item, got, tc.want)
		}
	}
}

// TestCompoundWordsAreGroupWordsOnly pins the boundary of #4805: compound
// matching is for allergen.Groups and diet words. An allergy's own word keeps
// the whole-word rule, and so do the exceptions, so "nut" cannot start
// matching "doughnut" while its group stays unchanged.
func TestCompoundWordsAreGroupWordsOnly(t *testing.T) {
	// The own word is compiled by allergen.PhrasePatterns, not allergen.GroupPatterns.
	if _, ok := allergen.GroupPatterns("catfish"); !ok {
		t.Fatal("allergen.GroupPatterns(catfish) gave no regex")
	}
	for _, w := range []string{"fish", "bread", "cheese", "egg", "milk", "butter", "corn"} {
		goRe, ok := allergen.GroupPatterns(w)
		if !ok {
			t.Fatalf("allergen.GroupPatterns(%q) gave no regex", w)
		}
		if _, whole, _ := allergen.PhrasePatterns(w); goRe == whole {
			t.Errorf("%q: allergen.GroupPatterns did not widen allergen.PhrasePatterns", w)
		}
	}
	// Not curated: nut (the ticket's explicit exclusion) and every other word.
	for _, w := range []string{"nut", "almond", "walnut", "shrimp", "cream", "cake", "flour", "tahini", "sea bass"} {
		goRe, ok := allergen.GroupPatterns(w)
		if !ok {
			t.Fatalf("allergen.GroupPatterns(%q) gave no regex", w)
		}
		if _, whole, _ := allergen.PhrasePatterns(w); goRe != whole {
			t.Errorf("%q got a compound form (%q); only allergen.CompoundWords may", w, goRe)
		}
	}
	// Every curated word must still be a group word somewhere, or the pattern
	// is dead weight.
	for w := range allergen.CompoundWords {
		found := false
		for _, members := range allergen.Groups {
			for _, m := range members {
				if allergen.Singular(strings.ToLower(m)) == w {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("allergen.CompoundWords has %q, which no allergen group lists", w)
		}
	}
}

// TestRecipeFindIngredientFilterUnchanged documents and pins the #4805
// decision about recipe_find: its query and ingredient filters are built with
// allergen.PhrasePatterns, not allergen.GroupPatterns, so searching for an ingredient still
// matches whole words only. Compound matching is for allergen warnings and
// diets; it is not a recipe search widening.
func TestRecipeFindIngredientFilterUnchanged(t *testing.T) {
	for _, tc := range []struct {
		phrase, text string
		want         bool
		why          string
	}{
		{"cod", "cod fillets", true, "an ingredient term matches the whole word"},
		{"cod", "codfish", false, "recipe_find does not compound-match"},
		{"bread", "cornbread", false, "recipe_find does not compound-match"},
		{"cheese", "cheesecake", false, "recipe_find does not compound-match"},
		{"fish", "catfish", false, "recipe_find does not compound-match"},
		{"tomato", "tomatoes, diced", true, "singular/plural still works"},
	} {
		pg, goRe, ok := allergen.PhrasePatterns(tc.phrase)
		if !ok {
			t.Fatalf("%q gave no pattern", tc.phrase)
		}
		if got := mustMatch(goRe, tc.text); got != tc.want {
			t.Errorf("%s: allergen.PhrasePatterns(%q) vs %q = %v, want %v", tc.why, tc.phrase, tc.text, got, tc.want)
		}
		// The Postgres form is what recipe_find actually sends; it has the
		// same shape, so the two agree.
		if !strings.HasPrefix(pg, `\m`) || !strings.HasSuffix(pg, `\M`) || strings.Contains(pg, `\w*`) {
			t.Errorf("allergen.PhrasePatterns(%q) Postgres form = %q, want a plain \\m...\\M", tc.phrase, pg)
		}
	}
	// A group word compiled for matching is NOT what the filter uses: this is
	// the assertion that keeps a future edit from pointing both at one helper.
	if goRe, _ := allergen.GroupPatterns("fish"); goRe == func() string { _, r, _ := allergen.PhrasePatterns("fish"); return r }() {
		t.Error("allergen.GroupPatterns(fish) and allergen.PhrasePatterns(fish) agree; the filter and the warnings would be the same pattern")
	}
}

// TestAllergenGroupVocabulary is #4804's acceptance test: every synonym and
// every group member the ticket named matches the ingredient it named. The
// data is what changed, so there is a row per pair: a shortened group shows
// up here as one failing row naming the word that went missing, not as a
// vague count.
//
// It goes through anyMatch, the path a stored allergy takes (allergen.AllergensIn then
// one matcher per allergen), so a synonym only passes if the alias map and
// the group agree.
func TestAllergenGroupVocabulary(t *testing.T) {
	for _, tc := range []struct {
		allergy, item string
		want          bool
		why           string
	}{
		// -- synonyms -> group (the ticket's "should map to a group" list) --
		{"crustaceans", "shrimp", true, "crustacean(s) -> shellfish"},
		{"Crustacean allergy", "crab", true, "crustacean(s) -> shellfish"},
		{"seafood", "shrimp", true, "seafood -> shellfish"},
		{"seafood", "salmon", true, "seafood -> fish"},
		{"seafood", "mussels", true, "seafood -> shellfish (molluscs)"},
		{"seafood allergy", "sea bass", true, "seafood -> fish"},
		{"dairy products", "cheddar", true, "dairy product(s) -> dairy"},
		{"Dairy products", "mascarpone", true, "dairy product(s) -> dairy"},
		{"cow's milk", "cheddar", true, "cow's milk -> dairy"},
		{"cows milk", "butter", true, "cow's milk, no apostrophe -> dairy"},
		{"cow\u2019s milk", "ghee", true, "cow's milk with a curly apostrophe -> dairy"},
		{"lactose intolerance", "milk", true, "lactose intolerance -> lactose"},
		{"lactose intolerant", "milk", true, "lactose intolerant -> lactose"},
		{"Lactose intolerant", "cream", true, "lactose intolerant -> lactose"},
		{"celiac disease", "flour", true, "celiac disease -> celiac"},
		{"coeliac", "flour", true, "coeliac, people.allergies path"},
		{"Coeliac disease", "naan", true, "coeliac disease -> celiac"},
		{"soya", "tofu", true, "soya -> soy"},
		{"soya", "soybeans", true, "soya -> soy"},
		{"sesame seeds", "toasted sesame oil", true, "sesame seed(s) -> sesame"},
		{"sesame seed", "tahini", true, "sesame seed -> sesame"},
		{"mollusc", "squid", true, "mollusc -> squid"},
		{"mollusk", "octopus", true, "mollusk -> octopus"},
		{"mollusc", "clam", true, "mollusc -> clam"},
		{"mollusc", "mussels", true, "mollusc -> mussel"},
		{"mollusc", "oyster", true, "mollusc -> oyster"},
		{"mollusc", "scallops", true, "mollusc -> scallop"},

		// -- group members the ticket named as missing --
		{"shellfish", "squid", true, "shellfish -> squid"},
		{"shellfish", "calamari", true, "shellfish -> calamari"},
		{"shellfish", "octopus", true, "shellfish -> octopus"},
		{"shellfish", "langoustine", true, "shellfish -> langoustine"},
		{"fish", "sea bass", true, "fish -> sea bass"},
		{"fish", "snapper", true, "fish -> snapper"},
		{"fish", "pollock", true, "fish -> pollock"},
		{"fish", "sole", true, "fish -> sole"},
		{"fish", "flounder", true, "fish -> flounder"},
		{"fish", "mahi mahi", true, "fish -> mahi mahi"},
		{"fish", "swordfish", true, "fish -> swordfish"},
		{"fish", "catfish", true, "fish -> catfish"},
		{"dairy", "mascarpone", true, "dairy -> mascarpone"},
		{"dairy", "parmigiano reggiano", true, "dairy -> parmigiano reggiano"},
		{"dairy", "gruyere", true, "dairy -> gruyere"},
		{"dairy", "brie", true, "dairy -> brie"},
		{"dairy", "paneer", true, "dairy -> paneer"},
		{"dairy", "queso", true, "dairy -> queso"},
		{"dairy", "kefir", true, "dairy -> kefir"},
		{"gluten", "penne", true, "gluten -> penne"},
		{"gluten", "sourdough", true, "gluten -> sourdough"},
		{"gluten", "naan", true, "gluten -> naan"},
		{"gluten", "puff pastry", true, "gluten -> puff pastry"},
		{"gluten", "beer", true, "gluten -> beer"},
		{"gluten", "macaroni", true, "gluten -> macaroni"},
		{"gluten", "lasagna", true, "gluten -> lasagna"},
		{"gluten", "orzo", true, "gluten -> orzo"},
		{"gluten", "ramen", true, "gluten -> ramen"},
		{"gluten", "udon", true, "gluten -> udon"},
		{"gluten", "pita", true, "gluten -> pita"},
		{"gluten", "baguette", true, "gluten -> baguette"},
		{"gluten", "phyllo", true, "gluten -> phyllo"},
		{"gluten", "pie crust", true, "gluten -> pie crust"},
		{"gluten", "pizza dough", true, "gluten -> pizza dough"},
		{"gluten", "crouton", true, "gluten -> crouton"},
		{"soy", "soybeans", true, "soy -> soybeans"},
		// celiac is a gluten allergy, so it gets the same shapes.
		{"celiac", "naan", true, "celiac -> naan"},
		{"celiac", "penne", true, "celiac -> penne"},
		{"celiac", "soy sauce", true, "celiac -> soy sauce (wheat)"},

		// -- the manager's list, in the words a parent types --
		{"dairy", "butter", true, "dairy -> butter"},
		{"dairy", "ghee", true, "dairy -> ghee"},
		{"dairy", "whey", true, "dairy -> whey"},
		{"dairy", "creme fraiche", true, "dairy -> creme fraiche"},
		{"dairy", "halloumi", true, "dairy -> halloumi"},
		{"milk allergy", "kefir", true, "milk -> kefir"},
		{"gluten", "flour", true, "gluten -> flour"},
		{"celiac", "flour", true, "celiac -> flour"},
		{"celiac", "bread", true, "celiac -> bread"},

		// -- exceptions that must keep holding (#4802/#4804) --
		{"dairy", "peanut butter", false, "peanut butter is not dairy"},
		{"milk", "peanut butter", false, "peanut butter is not milk"},
		{"lactose intolerance", "almond milk", false, "almond milk is not lactose"},
		{"lactose intolerance", "mascarpone", true, "lactose gets the wider dairy list"},
		{"lactose intolerant", "paneer", true, "lactose gets the wider dairy list"},
		{"dairy", "almond milk", false, "almond milk is not dairy"},
		{"dairy", "bean curd", false, "bean curd is tofu, not dairy"},
		{"egg", "eggplant", false, "eggplant is not egg"},
		{"nut", "nutmeg", false, "nutmeg is not a nut"},
		{"nut", "coconut", false, "coconut is not a nut"},
		{"nut", "butternut squash", false, "butternut is not a nut"},
		{"shellfish", "oyster mushroom", false, "oyster mushrooms are not shellfish"},
		{"fish", "chicken", false, "chicken is not fish"},
		{"gluten", "root beer", false, "root beer is not gluten"},
		{"gluten", "ginger beer", false, "ginger beer is not gluten"},
		{"celiac", "root beer", false, "root beer is not gluten"},
		{"celiac", "almond flour", false, "almond flour is not gluten"},
		{"celiac", "rice flour", false, "rice flour is not gluten"},
		{"celiac", "corn tortillas", false, "corn tortillas are not gluten"},
		// "gluten-free bread" is deliberately still flagged: the words are
		// stripped and "bread" remains, and a label is not proof (#4804).
		{"celiac", "gluten-free bread", true, "a packet claim is not proof"},
		{"celiac", "chickpea pasta", false, "chickpea pasta is not gluten"},
		{"celiac", "soba noodles", false, "soba is buckwheat"},
		{"dairy", "coconut yogurt", false, "coconut yogurt is not dairy"},
		{"dairy", "oat cream", false, "oat cream is not dairy"},
		{"dairy", "cream soda", false, "cream soda is not dairy"},
		{"fish", "bass ale", false, "bass ale is a beer, not a fish"},
		{"gluten", "rice noodles", false, "rice noodles are not gluten"},
		// allergen.Exceptions are for the group words only: an allergy's own word still
		// always matches, and a nut allergy still flags almond flour.
		{"almond", "almond flour", true, "the allergy's own word always matches"},
		{"nut", "almond flour", true, "almond flour is still nuts"},
		{"milk", "coconut milk", true, "the allergy's own word always matches"},

		// -- #4805: compounds in the groups this table already covers --
		{"fish", "catfish fillets", true, "#4805 fish -> catfish"},
		{"fish", "monkfish", true, "#4805 fish -> monkfish"},
		{"fish", "goldfish crackers", false, "#4805 goldfish is not fish"},
		{"gluten", "cornbread", true, "#4805 gluten -> cornbread"},
		{"celiac", "cornbread", true, "#4805 celiac -> cornbread"},
		{"dairy", "cheesecake", true, "#4805 dairy -> cheesecake"},
		{"egg", "eggnog", true, "#4805 egg -> eggnog"},
		{"egg", "eggplant", false, "#4805 eggplant is still not egg"},
		{"nut", "coconut", false, "#4805 coconut is still not a nut"},
		{"nut", "doughnut", false, "#4805 doughnut is still not a nut"},
	} {
		if got := anyMatch(tc.allergy, tc.item); got != tc.want {
			t.Errorf("%s: allergy %q vs %q = %v, want %v", tc.why, tc.allergy, tc.item, got, tc.want)
		}
	}
}

// TestAllergenAliasGroups pins the mapping itself, so a future edit that
// drops a group from an alias is visible even if the ingredient lists happen
// to overlap. It also keeps the groups honest: an alias must point at a real
// group, and every group must be reachable by its own key.
func TestAllergenAliasGroups(t *testing.T) {
	for key := range allergen.Groups {
		if len(allergen.CompilePhrases(allergen.Groups[key])) == 0 {
			t.Errorf("group %q has no usable ingredient", key)
		}
	}
	for alias, groups := range allergen.Aliases {
		for _, g := range groups {
			if _, ok := allergen.Groups[g]; !ok {
				t.Errorf("alias %q points at %q, which is not a group", alias, g)
			}
		}
	}
	// The synonyms the ticket named, by the keys allergen.AllergensIn produces.
	for _, tc := range []struct {
		allergy string
		want    []string
	}{
		{"crustaceans", []string{"crustacean"}},
		{"seafood", []string{"fish", "shellfish"}},
		{"dairy products", []string{"dairy"}},
		{"cow's milk", []string{"dairy"}},
		{"lactose intolerance", []string{"lactose"}},
		{"lactose intolerant", []string{"lactose"}},
		{"celiac disease", []string{"celiac"}},
		{"coeliac", []string{"celiac"}},
		{"coeliac disease", []string{"celiac"}},
		{"soya", []string{"soy"}},
		{"sesame seeds", []string{"sesame"}},
		{"mollusc", []string{"mollusc"}},
		{"mollusk", []string{"mollusc"}},
	} {
		var got []string
		for _, a := range allergen.AllergensIn(tc.allergy) {
			got = append(got, allergen.GroupKeys(a)...)
		}
		for _, w := range tc.want {
			if !slices.Contains(got, w) {
				t.Errorf("allergy %q gives groups %v, want %q among them", tc.allergy, got, w)
			}
		}
	}
}

// TestAllergyMatchersSplitAndLabel pins the two things a stored entry must
// now do: one entry can give several allergens, and each warning keeps a
// readable label (the bare allergen, not the raw text).
func TestAllergyMatchersSplitAndLabel(t *testing.T) {
	ms, unchecked := allergen.Matchers("p", "Nora", "peanuts and tree nuts")
	if len(unchecked) != 0 {
		t.Errorf("unchecked = %q, want none", unchecked)
	}
	if len(ms) != 2 {
		t.Fatalf("matchers = %+v, want peanut and tree nut", ms)
	}
	var labels []string
	for _, m := range ms {
		labels = append(labels, m.Allergy)
	}
	if !slices.Equal(labels, []string{"peanuts", "tree nuts"}) {
		t.Errorf("labels = %q", labels)
	}
	// The label stays the bare allergen, so warnings read as they always did.
	if got := allergen.WarningLines(allergen.Warnings(ms, []string{"peanut butter", "cashews"})); len(got) != 2 ||
		got[0] != "Nora (peanuts): peanut butter" || got[1] != "Nora (tree nuts): cashews" {
		t.Errorf("warnings = %q", got)
	}
	// Nothing left to match: no matchers, so the caller can say so.
	for _, nothing := range []string{"allergy", "anaphylactic", "  ", "severe", "(carries an EpiPen)"} {
		if ms, _ := allergen.Matchers("p", "Nora", nothing); len(ms) != 0 {
			t.Errorf("allergen.Matchers(%q) = %+v, want none", nothing, ms)
		}
	}
}
