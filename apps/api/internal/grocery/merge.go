// Package grocery merges a week of planned meals into grocery items (#4568).
//
// WHY THIS EXISTS. "Put this week's dinners on the grocery list" has been an
// MCP tool since #4500 (apps/mcp/internal/meals/grocery.go, meal_build_grocery_list),
// but the UI cannot call the MCP — it refuses `home-os-ui` tokens by audience
// (#4692). The Meals week view (#4568) needs the same answer with a PREVIEW
// before anything is written, because the write goes into a list another family
// member may be using in store mode right now (manager decision on #4519,
// comment #13046): a full preview page, then one confirm.
//
// So this package holds the merge as PURE functions over already-loaded rows.
// The REST endpoints in apps/api/internal/meals/grocery.go load the rows and
// call Merge; the lists package decides what is already on the list. No SQL
// here, so the merge is unit-testable without a database.
//
// PORTED FROM apps/mcp/internal/meals/grocery.go. The rules are the MCP tool's
// rules, deliberately including its quirks, because the assistant and the page
// must give the same answer for the same week:
//
//	ingredientKey   lower case, each word singular, leading the/a/an/some dropped
//	unitFamilies    tsp/tbsp/fl oz/cup/pint/quart/gallon, ml/l, g/kg, oz/lb convert
//	                within a family; anything else is its own family; no unit is a count
//	sectionOf       the last word decides first ("chicken broth" is Pantry,
//	                "garlic powder" is Spices), then any other word
//	mealIsRecipe    a free-text meal whose title equals a recipe's name counts as
//	                that recipe (the SQL lives in this package too, see MealIsRecipe)
//
// DUPLICATION, recorded rather than hidden: singular(), words() and the unit
// tables now exist in more than one place (this package, apps/mcp/internal/meals,
// and apps/ui/lib/recipes for pasted-recipe previews). The follow-up that
// removes it is a shared `packages/grocery`; until then each copy has a test
// pinning the documented examples so they cannot drift silently.
package grocery

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// MealIsRecipe is the SQL that decides a planned meal's recipe: the linked
// recipe, or — for a free-text meal — the recipe whose name the title is
// exactly. Copied verbatim from apps/mcp/internal/meals/suggest.go so
// meal_suggest, meal_build_grocery_list and this page agree on what "this meal
// is that recipe" means. Keep `m` (meal_plans) and `r` (recipes) as the aliases.
const MealIsRecipe = `r.household_id = m.household_id AND (r.id = m.recipe_id OR (m.recipe_id IS NULL AND lower(btrim(m.title)) = lower(btrim(r.title))))`

// SectionOrder is the order sections are listed in: a walk through a store.
var SectionOrder = []string{"Produce", "Meat & seafood", "Dairy & eggs", "Bakery", "Pantry", "Spices", "Frozen", "Other"}

// Ingredient is one row of a recipe's structured ingredients
// (recipes.ingredients, migration 047). Quantity is a number, a text amount
// ("a pinch") or nil, which is why it is `any`.
type Ingredient struct {
	Name     string
	Quantity any
	Unit     *string
}

// Meal is one planned meal with the recipe it uses, if any. RecipeID and
// RecipeTitle are nil for a free-text meal that matches no recipe.
type Meal struct {
	Date        string // YYYY-MM-DD
	Slot        string // breakfast | lunch | dinner | snack
	Title       string
	RecipeID    *string
	RecipeTitle *string
	Servings    *int
	Ingredients []Ingredient
}

// Item is one merged grocery item.
type Item struct {
	// Text is the ingredient as first written, capitalised ("Bell peppers").
	Text string `json:"text"`
	// Key is the merge key, so the caller can match an existing list item to it.
	Key string `json:"key"`
	// Quantity is nil when the amounts could not be added up (no numbers, or
	// numbers in units that do not convert).
	Quantity *float64 `json:"quantity"`
	Unit     *string  `json:"unit"`
	Section  string   `json:"section"`
	// Meals names the planned meals this item came from ("Mon dinner
	// Sheet-pan chicken fajitas"), for the "why is this here" line.
	Meals []string `json:"meals"`
	// Note carries "plus a pinch" or "amounts in different units: 2 cups, 3 oz".
	Note string `json:"note,omitempty"`
}

// Skipped is a planned meal the merge could not take ingredients from.
type Skipped struct {
	Date   string `json:"date"`
	Slot   string `json:"slot"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// Result is the merge output.
type Result struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Items    []Item    `json:"items"`
	Sections []string  `json:"sections"`
	Skipped  []Skipped `json:"skipped"`
	// Planned is how many meals were in the range (including skipped ones).
	Planned int `json:"planned"`
	// RecipeMeals is how many meals contributed ingredients.
	RecipeMeals int `json:"recipe_meals"`
	// Notes are the summary lines the page shows above the list.
	Notes []string `json:"notes"`
}

// ---------------------------------------------------------------------------
// Words
// ---------------------------------------------------------------------------

var wordSep = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// words splits on anything that is not a letter or a digit ("Turkey, 14 lb" is
// turkey/14/lb).
func words(s string) []string {
	var out []string
	for _, w := range wordSep.Split(strings.ToLower(s), -1) {
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// singular strips a plural ending: berries -> berry, tomatoes -> tomato,
// glasses -> glass, eggs -> egg.
func singular(w string) string {
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

var leadingFiller = map[string]bool{"the": true, "a": true, "an": true, "some": true, "my": true, "our": true}

// IngredientKey is the merge key of an ingredient name: lower-case singular
// words without leading filler, so "Tomatoes" and "tomato" merge.
func IngredientKey(name string) string {
	var out []string
	for _, w := range words(name) {
		if len(out) == 0 && leadingFiller[w] {
			continue
		}
		out = append(out, singular(w))
	}
	return strings.Join(out, " ")
}

// ---------------------------------------------------------------------------
// Store sections
// ---------------------------------------------------------------------------

// sectionWords maps a singular word to a store section.
var sectionWords = map[string]string{}

func init() {
	for section, ws := range map[string]string{
		"Produce": "onion garlic shallot scallion leek tomato potato carrot celery lettuce spinach kale cabbage broccoli cauliflower " +
			"cucumber zucchini squash pumpkin eggplant mushroom avocado lemon lime orange apple banana berry strawberry blueberry " +
			"raspberry grape pear peach mango pineapple cilantro parsley basil mint dill rosemary thyme ginger jalapeno chile chili " +
			"corn pea arugula radish beet asparagus herb sprout produce fruit vegetable salad greens",
		"Meat & seafood": "chicken beef pork turkey bacon sausage ham lamb steak mince fish salmon shrimp prawn cod tilapia " +
			"crab lobster scallop thigh breast drumstick wing chop rib brisket chorizo prosciutto pepperoni meatball",
		"Dairy & eggs": "milk butter cheese cheddar parmesan mozzarella feta ricotta cream yogurt yoghurt egg ghee buttermilk",
		"Bakery":       "bread tortilla bun roll pita bagel baguette naan croissant brioche wrap",
		"Pantry": "rice pasta spaghetti noodle flour sugar oil vinegar sauce broth stock bean lentil chickpea oat cereal honey syrup " +
			"ketchup mustard mayonnaise mayo salsa paste can tuna quinoa couscous breadcrumb cornstarch yeast cocoa chocolate " +
			"nut almond walnut peanut cashew pecan raisin cracker chip soy tahini jam coconut water wine",
		"Spices": "salt pepper peppercorn cumin paprika oregano cinnamon nutmeg turmeric coriander cardamom clove allspice " +
			"seasoning spice powder flake bay vanilla",
		"Frozen": "frozen",
	} {
		for _, w := range strings.Fields(ws) {
			sectionWords[w] = section
		}
	}
}

// SectionOf guesses where an ingredient is in the store. A recipe's own
// `section` is a part of the recipe ("Sauce"), not an aisle, so it is not used.
func SectionOf(name string) string {
	ws := words(name)
	for i := range ws {
		ws[i] = singular(ws[i])
	}
	if len(ws) == 0 {
		return "Other"
	}
	for _, w := range ws {
		if w == "frozen" {
			return "Frozen"
		}
	}
	last := ws[len(ws)-1]
	if len(ws) > 1 {
		prev := ws[len(ws)-2]
		switch {
		case last == "pepper" && contains([]string{"bell", "red", "green", "yellow", "orange", "sweet", "jalapeno", "chili", "chile", "poblano", "serrano", "habanero"}, prev):
			return "Produce"
		case last == "butter" && contains([]string{"peanut", "almond", "cashew", "nut", "sunflower", "seed", "apple"}, prev):
			return "Pantry"
		case last == "milk" && contains([]string{"coconut", "condensed", "evaporated"}, prev):
			return "Pantry"
		}
	}
	if s, ok := sectionWords[last]; ok {
		return s
	}
	for i := len(ws) - 2; i >= 0; i-- {
		if s, ok := sectionWords[ws[i]]; ok {
			return s
		}
	}
	return "Other"
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Units
// ---------------------------------------------------------------------------

// unitFamily is a unit's family and its size in that family's base unit.
type unitFamily struct {
	family string
	size   float64
}

var unitFamilies = map[string]unitFamily{
	"tsp": {"volume", 1}, "tbsp": {"volume", 3}, "fl oz": {"volume", 6}, "cup": {"volume", 48},
	"pint": {"volume", 96}, "quart": {"volume", 192}, "gallon": {"volume", 768},
	"ml": {"metric volume", 1}, "l": {"metric volume", 1000},
	"g": {"metric weight", 1}, "kg": {"metric weight", 1000},
	"oz": {"weight", 1}, "lb": {"weight", 16},
}

var unitAliases = map[string]string{
	"tablespoon": "tbsp", "tablespoons": "tbsp", "tbsp": "tbsp", "tbsps": "tbsp", "tbs": "tbsp", "tbl": "tbsp",
	"teaspoon": "tsp", "teaspoons": "tsp", "tsp": "tsp", "tsps": "tsp",
	"cup": "cup", "cups": "cup",
	"ounce": "oz", "ounces": "oz", "oz": "oz",
	"fluid ounce": "fl oz", "fluid ounces": "fl oz", "fl oz": "fl oz",
	"pound": "lb", "pounds": "lb", "lb": "lb", "lbs": "lb",
	"gram": "g", "grams": "g", "g": "g",
	"kilogram": "kg", "kilograms": "kg", "kg": "kg",
	"milliliter": "ml", "milliliters": "ml", "millilitre": "ml", "millilitres": "ml", "ml": "ml",
	"liter": "l", "liters": "l", "litre": "l", "litres": "l", "l": "l",
	"pint": "pint", "pints": "pint", "quart": "quart", "quarts": "quart", "gallon": "gallon", "gallons": "gallon",
	"clove": "clove", "cloves": "clove", "can": "can", "cans": "can", "jar": "jar", "jars": "jar",
	"package": "package", "packages": "package", "pkg": "package", "packet": "packet", "packets": "packet",
	"slice": "slice", "slices": "slice", "stick": "stick", "sticks": "stick", "bunch": "bunch", "bunches": "bunch",
	"pinch": "pinch", "pinches": "pinch", "dash": "dash", "dashes": "dash", "head": "head", "heads": "head",
	"sprig": "sprig", "sprigs": "sprig", "piece": "piece", "pieces": "piece", "handful": "handful", "handfuls": "handful",
}

// NormUnit is the short form of a unit ("tablespoons" is tbsp), or the input
// lower-cased when it is not a known unit.
func NormUnit(u string) string {
	k := strings.ToLower(strings.TrimSpace(u))
	if v, ok := unitAliases[k]; ok {
		return v
	}
	return k
}

var unicodeFractions = map[rune]float64{
	'½': 0.5, '⅓': 1.0 / 3, '⅔': 2.0 / 3, '¼': 0.25, '¾': 0.75, '⅕': 0.2, '⅖': 0.4, '⅗': 0.6, '⅘': 0.8,
	'⅙': 1.0 / 6, '⅚': 5.0 / 6, '⅛': 0.125, '⅜': 0.375, '⅝': 0.625, '⅞': 0.875,
}

// ParseAmount reads "2", "1.5", "1/2", "1 1/2", "½" or "1½" as a number. ok is
// false for anything else ("a pinch", "1-2"), which the caller keeps as text.
// The same rules as parseAmount in apps/mcp/internal/meals/recipes.go.
func ParseAmount(in string) (float64, bool) {
	s := strings.TrimSpace(in)
	if s == "" {
		return 0, false
	}
	var total float64
	// A trailing unicode fraction: "1½" or "½".
	if r, size := utf8.DecodeLastRuneInString(s); size > 0 {
		if f, ok := unicodeFractions[r]; ok {
			total = f
			s = strings.TrimSpace(s[:len(s)-size])
			if s == "" {
				return round3(total), true
			}
		}
	}
	parts := strings.Fields(s)
	if len(parts) > 2 || (total != 0 && len(parts) != 1) {
		return 0, false
	}
	for i, p := range parts {
		if num, den, found := strings.Cut(p, "/"); found {
			if i == 0 && len(parts) == 2 {
				return 0, false // "1/2 3"
			}
			n, err1 := strconv.ParseFloat(num, 64)
			d, err2 := strconv.ParseFloat(den, 64)
			if err1 != nil || err2 != nil || d == 0 || n < 0 {
				return 0, false
			}
			total += n / d
			continue
		}
		if i == 1 {
			return 0, false // "1 2"
		}
		n, err := strconv.ParseFloat(p, 64)
		if err != nil || n < 0 || math.IsInf(n, 0) || math.IsNaN(n) {
			return 0, false
		}
		total += n
	}
	return round3(total), true
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

// ---------------------------------------------------------------------------
// Merging
// ---------------------------------------------------------------------------

// unitAmount is the total of one ingredient in one unit family.
type unitAmount struct {
	// unit is the largest unit seen in the family, used for display; "" for a count.
	unit string
	// family is the unitFamilies family, "unit:<u>" for other units, "" for a count.
	family string
	// base is the total in the family's base unit (or in `unit` when it has no family).
	base     float64
	measured bool
	vague    []string
}

func (a *unitAmount) quantity() *float64 {
	if !a.measured || a.base <= 0 {
		return nil
	}
	size := 1.0
	if f, ok := unitFamilies[a.unit]; ok {
		size = f.size
	}
	q := round3(a.base / size)
	if q <= 0 {
		return nil
	}
	return &q
}

func (a *unitAmount) text() string {
	q := a.quantity()
	if q == nil {
		return strings.Join(a.vague, " + ")
	}
	s := fmtQty(*q)
	if a.unit != "" {
		s += " " + a.unit
	}
	return s
}

func fmtQty(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// total is one merged ingredient across every meal in the range.
type total struct {
	name    string
	key     string
	section string
	amounts []*unitAmount
	meals   []string
}

func (t *total) add(ing Ingredient, meal string) {
	if !contains(t.meals, meal) {
		t.meals = append(t.meals, meal)
	}
	unit := ""
	if ing.Unit != nil {
		unit = NormUnit(*ing.Unit)
	}
	family, size := "", 1.0
	if f, ok := unitFamilies[unit]; ok {
		family, size = f.family, f.size
	} else if unit != "" {
		family = "unit:" + unit
	}
	var a *unitAmount
	for _, x := range t.amounts {
		if x.family == family {
			a = x
		}
	}
	if a == nil {
		a = &unitAmount{unit: unit, family: family}
		t.amounts = append(t.amounts, a)
	}
	// Show the total in the largest unit used within the family (2 tbsp + ½ cup
	// is ⅔ cup, not 32 tbsp).
	if f, ok := unitFamilies[unit]; ok {
		if cur, ok := unitFamilies[a.unit]; !ok || f.size > cur.size {
			a.unit = unit
		}
	}
	switch q := ing.Quantity.(type) {
	case float64:
		if q > 0 {
			a.base += q * size
			a.measured = true
		}
	case int:
		if q > 0 {
			a.base += float64(q) * size
			a.measured = true
		}
	case string:
		if f, ok := ParseAmount(q); ok && f > 0 {
			a.base += f * size
			a.measured = true
		} else if txt := strings.TrimSpace(q); txt != "" && !contains(a.vague, txt) {
			a.vague = append(a.vague, txt)
		}
	}
}

// Merge turns planned meals into merged grocery items, grouped by section in
// a store's walking order. Notes explain what it could not do, so the page
// never shows a short list without saying why it is short.
func Merge(meals []Meal, from, to string) Result {
	res := Result{From: from, To: to, Items: []Item{}, Sections: []string{}, Skipped: []Skipped{}, Planned: len(meals)}

	var totals []*total
	byKey := map[string]*total{}
	missingServings := 0
	for _, m := range meals {
		label := mealLabel(m)
		if m.RecipeID == nil {
			res.Skipped = append(res.Skipped, Skipped{
				Date: m.Date, Slot: m.Slot, Title: m.Title,
				Reason: "not a recipe, so there are no ingredients to add",
			})
			continue
		}
		if len(m.Ingredients) == 0 {
			res.Skipped = append(res.Skipped, Skipped{
				Date: m.Date, Slot: m.Slot, Title: m.Title,
				Reason: "the recipe has no ingredients saved",
			})
			continue
		}
		if m.Servings == nil {
			missingServings++
		}
		res.RecipeMeals++
		for _, ing := range m.Ingredients {
			key := IngredientKey(ing.Name)
			if key == "" {
				continue
			}
			t, ok := byKey[key]
			if !ok {
				t = &total{name: capitalise(strings.TrimSpace(ing.Name)), key: key, section: SectionOf(ing.Name)}
				byKey[key] = t
				totals = append(totals, t)
			}
			t.add(ing, label)
		}
	}

	sort.SliceStable(totals, func(i, j int) bool {
		si, sj := sectionRank(totals[i].section), sectionRank(totals[j].section)
		if si != sj {
			return si < sj
		}
		return totals[i].name < totals[j].name
	})

	seenSection := map[string]bool{}
	for _, t := range totals {
		item := Item{Text: t.name, Key: t.key, Section: t.section, Meals: t.meals}
		measured := 0
		var q *float64
		var unit *string
		for _, a := range t.amounts {
			if a.quantity() != nil {
				measured++
				q = a.quantity()
				if a.unit != "" {
					u := a.unit
					unit = &u
				} else {
					unit = nil // a count: no unit, not an empty string
				}
			}
		}
		switch {
		case len(t.amounts) == 1 && measured == 1:
			item.Quantity, item.Unit = q, unit
			if len(t.amounts[0].vague) > 0 {
				item.Note = "plus " + strings.Join(t.amounts[0].vague, " + ")
			}
		case measured == 1:
			// One measured amount and some vague ones: keep the number, note the rest.
			item.Quantity, item.Unit = q, unit
			var vague []string
			for _, a := range t.amounts {
				vague = append(vague, a.vague...)
			}
			if len(vague) > 0 {
				item.Note = "plus " + strings.Join(vague, " + ")
			}
		case measured > 1:
			// Numbers in units that do not convert: one row, no quantity, and
			// the amounts spelled out. The list never gets the same ingredient twice.
			var parts []string
			for _, a := range t.amounts {
				if a.quantity() != nil {
					parts = append(parts, a.text())
				}
			}
			item.Note = "amounts in different units: " + strings.Join(parts, ", ")
		default:
			var parts []string
			for _, a := range t.amounts {
				if txt := a.text(); txt != "" {
					parts = append(parts, txt)
				}
			}
			if len(parts) > 0 {
				item.Quantity = nil
				item.Note = strings.Join(parts, ", ")
			}
		}
		res.Items = append(res.Items, item)
		if !seenSection[t.section] {
			seenSection[t.section] = true
			res.Sections = append(res.Sections, t.section)
		}
	}

	if res.RecipeMeals == 0 && res.Planned > 0 {
		res.Notes = append(res.Notes, "None of the planned meals has a recipe with ingredients, so there is nothing to add.")
	}
	if res.RecipeMeals > 0 {
		note := "Quantities are as each recipe is written; meal plans have no servings yet."
		if missingServings > 0 {
			note += " " + plural(missingServings, "recipe") + " list no servings."
		}
		res.Notes = append(res.Notes, note)
	}
	return res
}

func mealLabel(m Meal) string {
	return strings.TrimSpace(dayShort(m.Date) + " " + m.Slot + " " + m.Title)
}

// dayShort is the three-letter weekday of a YYYY-MM-DD date, or "" if unparsable.
func dayShort(date string) string {
	t, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return ""
	}
	return t.Format("Mon")
}

func sectionRank(s string) int {
	for i, v := range SectionOrder {
		if v == s {
			return i
		}
	}
	return len(SectionOrder)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] = r[0] - 'a' + 'A'
	}
	return string(r)
}
