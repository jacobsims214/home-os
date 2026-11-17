package allergen

// The allergen vocabulary and the word matching over it. This file and the four
// beside it are the ONE implementation of "what counts as a match" for an
// allergy: apps/mcp/internal/meals (recipe_find/_get/_create/_update/_delete,
// meal_set, meal_rate, agenda_get, meal_suggest, the grocery list) and
// apps/api/internal/meals (GET /api/v1/recipes, /recipes/{id}) both call it
// through a thin alias shim, so a warning the API shows and a recipe the
// assistant refuses cannot disagree (#4803, #4874, #4891).
//
// It lives in packages/ rather than in either app for the reason #4819 moved
// bill.BillDueFor here: apps/api cannot import another module's internal
// package, and a second copy is the bug class this project keeps hitting
// (mcp/recipe-tools.md forbids a second "what counts as a match" list).
//
// Moved verbatim from apps/mcp/internal/meals/recipes.go (#4891). Only the
// names changed (exported) and the comments' identifiers followed; the data
// and the rules are untouched.

import (
	"regexp"
	"strings"
)

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func Words(s string) []string {
	var out []string
	for _, w := range nonWord.Split(strings.ToLower(s), -1) {
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// singular strips a plural ending: berries -> berry, tomatoes -> tomato,
// glasses -> glass, eggs -> egg.
func Singular(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 3 && (strings.HasSuffix(w, "oes") || strings.HasSuffix(w, "xes") || strings.HasSuffix(w, "ches") ||
		strings.HasSuffix(w, "shes") || strings.HasSuffix(w, "sses")):
		return w[:len(w)-2]
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us"):
		return w[:len(w)-1]
	}
	return w
}

// WordForm is the regex body matching w in singular or plural form.
func WordForm(w string) string {
	w = Singular(w)
	if strings.HasSuffix(w, "y") && len(w) > 2 {
		return regexp.QuoteMeta(w[:len(w)-1]) + `(?:y|ies)`
	}
	return regexp.QuoteMeta(w) + `(?:s|es)?`
}

// PhrasePatterns returns the Postgres (\m...\M) and Go (\b...\b) regexes for
// a phrase: its words in order, each singular or plural. ok is false when
// the phrase has no words.
//
// Both ends are anchored to a word boundary, so a phrase only matches whole
// words. That is what the recipe_find filters want, and what an allergy's own
// word and its exceptions want. Group words that also sit inside a one-word
// compound get GroupPatterns instead (#4805).
func PhrasePatterns(phrase string) (pg, goRe string, ok bool) {
	ws := Words(phrase)
	if len(ws) == 0 {
		return "", "", false
	}
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = WordForm(w)
	}
	body := strings.Join(parts, `[^\w]+`)
	return `\m` + body + `\M`, `(?i)\b` + body + `\b`, true
}

// CompoundMode says where in a longer word a group word may sit (#4805).
type CompoundMode uint8

const (
	CompoundPrefix CompoundMode = 1 << iota // cheesecake, butterscotch
	CompoundSuffix                          // catfish, cornbread
)

// CompoundWords are the group words that are also part of a one-word
// ingredient, which a word boundary cannot see: "catfish fillets" carries
// fish, "cornbread" carries bread, "cheesecake" carries cheese. Only curated
// words get this, and only as group words or diet words: an allergy's own
// word ("nut") and the exceptions keep the whole-word rule.
//
// Deliberately absent:
//   - nut: coconut, doughnut and nutmeg stay unmatched (the ticket's negative).
//   - cream, cake, sauce, oil, flour and the rest: they compound too rarely as
//     a head or a tail to be worth the false positives.
//
// bread is both ways round: cornbread/shortbread/flatbread on the tail, and
// breadstick/breadcrumbs/breaded on the head (breaded chicken is gluten).
var CompoundWords = map[string]CompoundMode{
	"corn":   CompoundPrefix,                  // cornbread, cornmeal, cornstarch
	"fish":   CompoundSuffix,                  // catfish, swordfish, monkfish, whitefish
	"bread":  CompoundSuffix | CompoundPrefix, // cornbread; breadstick, breaded
	"cheese": CompoundPrefix,                  // cheesecake, cheeseburger, cheesesteak
	"egg":    CompoundPrefix,                  // eggnog, eggroll (eggplant excepted)
	"milk":   CompoundPrefix,                  // milkshake (milkfish excepted)
	"butter": CompoundPrefix,                  // butterscotch, buttercream (butterfly excepted)
}

// GroupPatterns is PhrasePatterns for one of an allergen group's words, which
// may also sit inside a one-word compound (#4805). A multi-word phrase and a
// word that is not in CompoundWords get PhrasePatterns exactly, so the two
// cannot drift: it is the same body, anchored one word-boundary looser on the
// side the compound hangs off.
//
// There is deliberately no Postgres form. Group words are matched in Go, on
// ingredient names (Warnings, RecipeCheck); the regexes that go
// to Postgres are recipe_find's query and ingredient filters, which stay on
// PhrasePatterns and keep matching whole words only, so searching for "cod"
// still does not return every recipe with "codfish" (deliberate, #4805).
func GroupPatterns(phrase string) (goRe string, ok bool) {
	ws := Words(phrase)
	if len(ws) != 1 {
		_, goRe, ok = PhrasePatterns(phrase)
		return goRe, ok
	}
	mode, isCompound := CompoundWords[Singular(strings.ToLower(ws[0]))]
	if !isCompound {
		_, goRe, ok = PhrasePatterns(phrase)
		return goRe, ok
	}
	// The compound hangs off the side the word does not sit on: a suffix word
	// (fish) matches whatever precedes it, a prefix word (cheese) whatever
	// follows.
	head, tail := `\b`, `\b`
	if mode&CompoundSuffix != 0 {
		head = `\b\w*`
	}
	if mode&CompoundPrefix != 0 {
		tail = `\w*\b`
	}
	return `(?i)` + head + WordForm(phrase) + tail, true
}

// Groups expands an allergy to the ingredients that usually carry
// it. The allergy's own words always match too.
//
// Breadth matters more than precision here: a missed allergen can be an
// anaphylaxis, a needless warning is a nuisance. #4804 widened the groups
// with the words a parent actually types (#4804), using supermarket names
// for the same food (penne, naan, mascarpone, squid, soybeans ...).
var Groups = map[string][]string{
	"peanut":   {"peanut", "groundnut", "satay"},
	"nut":      {"almond", "walnut", "pecan", "cashew", "pistachio", "hazelnut", "macadamia", "brazil nut", "pine nut", "nut", "praline", "marzipan", "nutella", "peanut"},
	"tree nut": {"almond", "walnut", "pecan", "cashew", "pistachio", "hazelnut", "macadamia", "brazil nut", "pine nut", "praline", "marzipan", "nutella"},
	// dairy and milk are the same list: a person who says "dairy" and one who
	// says "milk" are avoiding the same foods (#4804: mascarpone, paneer,
	// kefir, creme fraiche, the named cheeses).
	"dairy": dairyFoods,
	"milk":  dairyFoods,
	// lactose is dairy too: a lactose-intolerant person may tolerate hard
	// cheese, but flagging it is the cheaper mistake (#4804).
	"lactose": dairyFoods,
	"egg":     {"egg", "mayonnaise", "mayo", "meringue", "aioli", "custard"},
	// gluten, wheat and celiac differ only where the grain does: malt is
	// barley, so it is gluten and celiac but not wheat.
	"gluten":     append(append([]string{}, grainFoods...), "malt"),
	"wheat":      grainFoods,
	"celiac":     append(append([]string{}, grainFoods...), "malt"),
	"shellfish":  shellfishFoods,
	"crustacean": {"shrimp", "prawn", "crab", "lobster", "crayfish", "crawfish", "langoustine", "scampi", "krill", "shrimp paste", "crab paste"},
	"mollusc":    molluscFoods,
	// corn had no group at all, so "corn allergy" matched only the literal
	// word and cornbread, popcorn and cornstarch all went through (#4805).
	"corn": {"corn", "popcorn", "sweetcorn", "cornmeal", "corn starch", "cornstarch", "cornflour",
		"corn flour", "corn syrup", "polenta", "grits", "masa", "hominy", "corn chip", "tortilla chip",
		"corn on the cob"},
	"fish":   {"fish", "salmon", "tuna", "cod", "tilapia", "halibut", "anchovy", "sardine", "trout", "haddock", "mackerel", "fish sauce", "worcestershire", "sea bass", "bass", "snapper", "pollock", "sole", "flounder", "mahi mahi", "swordfish", "catfish", "monkfish", "grouper", "hake", "bream"},
	"soy":    {"soy", "soya", "soybean", "tofu", "edamame", "tempeh", "miso", "soy sauce", "tamari"},
	"sesame": {"sesame", "tahini"},
}

var (
	// dairyFoods is every word that means a dairy product (#4804).
	dairyFoods = []string{"milk", "butter", "ghee", "whey", "casein", "cheese", "cream", "yogurt", "yoghurt",
		"buttermilk", "half and half", "custard", "parmesan", "parmigiano reggiano", "mozzarella", "cheddar",
		"ricotta", "feta", "mascarpone", "paneer", "queso", "kefir", "brie", "gruyere", "gruyère", "halloumi",
		"pecorino", "parmigiano", "creme fraiche", "crème fraîche"}

	// grainFoods is what carries gluten: wheat and the other gluten grains,
	// and the breads, pasta shapes and pastries made from them (#4804).
	grainFoods = []string{"wheat", "flour", "bread", "breadcrumb", "panko", "pasta", "spaghetti", "noodle",
		"couscous", "barley", "rye", "semolina", "farro", "bulgur", "cracker", "tortilla", "soy sauce", "seitan",
		"penne", "macaroni", "lasagna", "lasagne", "orzo", "ramen", "udon", "naan", "pita", "pitta", "baguette",
		"sourdough", "puff pastry", "pastry", "phyllo", "filo", "pie crust", "pizza dough", "crouton", "beer",
		"bagel", "croissant", "donut", "doughnut", "pretzel", "muffin", "scone", "waffle", "pancake", "cookie",
		"biscuit", "cake", "stuffing"}

	// shellfishFoods is the everyday meaning of "shellfish": crustaceans and
	// molluscs together (#4804). Crustacean and mollusc allergy are separate
	// diagnoses, so each has its own group as well.
	shellfishFoods = []string{"shrimp", "prawn", "crab", "lobster", "crayfish", "crawfish", "langoustine",
		"scampi", "krill", "shellfish", "squid", "calamari", "octopus", "cuttlefish", "clam", "mussel",
		"oyster", "scallop", "cockle", "whelk", "abalone", "snail", "escargot"}

	// molluscFoods is a mollusc allergy, which shellfish covers too.
	molluscFoods = []string{"squid", "calamari", "octopus", "cuttlefish", "clam", "mussel", "oyster", "scallop",
		"cockle", "whelk", "abalone", "snail", "escargot"}
)

// Aliases maps the key NewMatcher builds for an allergy (its
// singularised words, "dairy products" -> "dairy product") to the groups it
// means. Without this, only an exact group key expanded, so "crustaceans",
// "seafood" or "cow's milk" matched nothing but their own literal words
// (#4804). The alias is data: the one matcher, the one loader and the one
// check still do the work.
var Aliases = map[string][]string{
	// A crustacean allergy is not a mollusc allergy, but the ticket asks for
	// it to cover shellfish: flagging squid is a nuisance, missing crab is
	// not, so it errs wide (#4804).
	"crustacean":          {"shellfish"},
	"seafood":             {"fish", "shellfish"},
	"sea food":            {"fish", "shellfish"},
	"shell fish":          {"shellfish"},
	"dairy product":       {"dairy"},
	"cow milk":            {"dairy"}, // "cow's milk", "cows milk": the key drops the possessive
	"celiac disease":      {"celiac"},
	"mollusk":             {"mollusc"},
	"soya":                {"soy"},
	"soya bean":           {"soy"},
	"sesame seed":         {"sesame"},
	"lactose intolerance": {"lactose"},
	"lactose intolerant":  {"lactose"},
}

// Exceptions are ingredients a group word appears in that don't
// carry the allergen ("peanut butter" is not dairy). They are ignored only
// for the group words, never for the allergy's own words.
var Exceptions = map[string][]string{
	"dairy":   dairyFree,
	"milk":    dairyFree,
	"lactose": dairyFree,
	"egg":     {"eggplant"},
	// #4805 turned on suffix matching for fish, so every -fish word that is
	// not a fish has to be listed here. Only the ones that are not food: a
	// -fish shellfish (shellfish, crayfish, crawfish) is deliberately left
	// matching, because flagging a crustacean for a fish allergy is the cheap
	// mistake and clearing one is not.
	"fish": {"fish shaped", "goldfish", "bass ale", "jellyfish", "starfish", "silverfish"},
	"nut":  {"nutmeg", "butternut", "coconut", "water chestnut"},
	// Root beer and ginger beer carry no gluten, so a gluten allergy must not
	// flag them (#4804 added "beer" to the grains). Note what is NOT listed:
	// "gluten-free bread" stays flagged. A packet claim is not a food
	// identity, "bread" still matches, and a needless flag is the cheaper
	// mistake than trusting a label the recipe text cannot verify.
	"gluten": glutenFree,
	"wheat":  glutenFree,
	"celiac": glutenFree,
	// #4804 widened shellfish and dairy: oyster mushrooms are mushrooms and
	// bean curd is tofu, so neither may raise the alarm.
	"shellfish": {"oyster mushroom"},
	"mollusc":   {"oyster mushroom"},
}

// glutenFree is what a gluten group word appears in without any gluten: root
// beer and ginger beer are not beer, and the alternative flours are not wheat
// (#4804). Exceptions only apply to the group's words, so an almond flour
// still matches a nut allergy.
var glutenFree = []string{"root beer", "ginger beer", "corn tortilla",
	// Breadfruit is a fruit, and #4805 made bread match as a prefix so that
	// breadstick and breaded are caught.
	"breadfruit",
	// Flours and noodles made from something other than wheat.
	"almond flour", "rice flour", "cornflour", "corn flour", "coconut flour", "chickpea flour",
	"buckwheat flour", "potato flour", "tapioca flour", "potato starch",
	"rice noodle", "rice pasta", "glass noodle", "rice cake", "rice cracker",
	"buckwheat noodle", "buckwheat pasta", "soba noodle", "chickpea pasta", "lentil pasta", "corn pasta",
	"zucchini noodle", "courgette noodle"}

var dairyFree = []string{"peanut butter", "almond butter", "cashew butter", "nut butter", "sunflower butter", "seed butter",
	"apple butter", "cocoa butter", "shea butter", "butternut", "coconut milk", "almond milk", "oat milk", "soy milk",
	"rice milk", "cashew milk", "coconut cream", "cream of tartar", "butter bean", "butter lettuce", "bean curd",
	"cream of coconut", "coconut yogurt", "coconut yoghurt", "oat cream", "oat yogurt", "soy cream", "soy yogurt",
	"cashew cream", "almond cream", "cream soda", "buttercup squash", "butterhead lettuce", "buttercup",
	"butterhead",
	// #4805 turned on prefix matching for butter, milk and cheese: a butterfly
	// cut is not butter, milkfish (bangus) is a fish, and cheesecloth is
	// cloth. Anything else that starts with one of those words and carries no
	// dairy belongs here too.
	"butterfly", "butterflied", "milkfish", "cheesecloth"}

// Matcher matches ingredient names against one person's allergy.
type Matcher struct {
	PersonID, Person, Allergy string
	Own, Group, Except        []*regexp.Regexp
}
