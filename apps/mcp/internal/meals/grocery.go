package meals

// meal_build_grocery_list (#4500): "put this week's dinners on the grocery
// list" in one call. It reads the planned meals of a date range, merges
// their recipes' structured ingredients (same ingredient and unit family are
// summed: 2 tbsp + 1 cup of oil is 1.125 cup), sorts them by store section
// and adds them through lists.AddItems, the same transaction list_add_items
// runs. So nothing open is added twice (words match: "Eggs" is "egg"), a
// checked-off item is put back, and with no active grocery list one is
// created, even when an archived "Grocery" exists (#4713).
//
// meal_plans has no servings column (#4487), so quantities are per recipe as
// written unless the caller passes scale (a factor) or servings (each recipe
// is scaled from its own servings). Free-text meals ("Pizza night") have no
// ingredients and are reported as skipped.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/lists"
	"home-os/mcp/internal/respond"
)

const (
	maxGroceryDays = 31 // the longest range one call reads
	maxScale       = 20
	summaryItems   = 25 // items named in the summary
)

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

type groceryMeal struct {
	LocalDate string   `json:"local_date" jsonschema_description:"YYYY-MM-DD in the family timezone."`
	Weekday   string   `json:"weekday"`
	Slot      string   `json:"slot"`
	Title     string   `json:"title"`
	RecipeID  *string  `json:"recipe_id" jsonschema_description:"The recipe whose ingredients were used; null for free text."`
	Used      bool     `json:"used" jsonschema_description:"true when its ingredients went on the list."`
	Scale     *float64 `json:"scale,omitempty" jsonschema_description:"Factor its quantities were multiplied by."`
	Skipped   *string  `json:"skipped,omitempty" jsonschema_description:"Why it was not used, e.g. no recipe."`
}

type groceryItem struct {
	Text     string   `json:"text"`
	Quantity *float64 `json:"quantity" jsonschema_description:"Total needed; null when the recipes give no amount."`
	Unit     *string  `json:"unit"`
	Section  string   `json:"section" jsonschema_description:"Store section, e.g. Produce."`
	Status   string   `json:"status" jsonschema_description:"added, reopened (was checked off) or already_on_list."`
	ItemID   string   `json:"item_id" jsonschema_description:"The list item; pass it in item_ids."`
	For      []string `json:"for" jsonschema_description:"Meals that need it, e.g. Tue dinner Chicken Tacos."`
	Note     *string  `json:"note,omitempty" jsonschema_description:"e.g. plus a pinch, or what the list already says."`
}

type grocerySection struct {
	Section string   `json:"section"`
	Items   []string `json:"items"`
}

type groceryList struct {
	ID        string `json:"id" jsonschema_description:"Pass it as list_id."`
	Name      string `json:"name"`
	OpenCount int    `json:"open_count"`
	Created   bool   `json:"created,omitempty" jsonschema_description:"true when this call started the list."`
}

type groceryResult struct {
	From           string           `json:"from" jsonschema_description:"First day read, YYYY-MM-DD."`
	To             string           `json:"to" jsonschema_description:"Last day read, YYYY-MM-DD."`
	Timezone       string           `json:"timezone"`
	List           *groceryList     `json:"list" jsonschema_description:"The list written to; null when there was nothing to add."`
	Items          []groceryItem    `json:"items" jsonschema_description:"One per ingredient, merged across meals."`
	Sections       []grocerySection `json:"sections" jsonschema_description:"Item texts grouped by store section."`
	Meals          []groceryMeal    `json:"meals" jsonschema_description:"Every planned meal in the range, used or skipped."`
	SkippedStaples []string         `json:"skipped_staples,omitempty" jsonschema_description:"Ingredients left off because of skip."`
	Scaled         bool             `json:"scaled" jsonschema_description:"false: quantities are per recipe as written."`
	Notes          []string         `json:"notes,omitempty"`
}

// ---------------------------------------------------------------------------
// Store sections
// ---------------------------------------------------------------------------

// sectionOrder is the order sections are listed in (a walk through a store).
var sectionOrder = []string{"Produce", "Meat & seafood", "Dairy & eggs", "Bakery", "Pantry", "Spices", "Frozen", "Other"}

// sectionWords map a singular word to a store section. The last word of an
// ingredient decides first ("chicken broth" is Pantry, "garlic powder"
// Spices), then any other word ("chicken thighs" is Meat).
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

// sectionOf guesses where an ingredient is in the store. Recipe "section"
// is a part of the recipe (Sauce), not an aisle, so it is not used.
func sectionOf(name string) string {
	ws := words(name)
	for i := range ws {
		ws[i] = singular(ws[i])
	}
	if len(ws) == 0 {
		return "Other"
	}
	if slices.Contains(ws, "frozen") {
		return "Frozen"
	}
	last := ws[len(ws)-1]
	switch {
	case last == "pepper" && len(ws) > 1 && slices.Contains([]string{"bell", "red", "green", "yellow", "orange", "sweet", "jalapeno", "chili", "chile", "poblano", "serrano", "habanero"}, ws[len(ws)-2]):
		return "Produce"
	case last == "butter" && len(ws) > 1 && slices.Contains([]string{"peanut", "almond", "cashew", "nut", "sunflower", "seed", "apple"}, ws[len(ws)-2]):
		return "Pantry"
	case last == "milk" && len(ws) > 1 && slices.Contains([]string{"coconut", "condensed", "evaporated"}, ws[len(ws)-2]):
		return "Pantry"
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

// ---------------------------------------------------------------------------
// Merging ingredients
// ---------------------------------------------------------------------------

// unitFamilies are the units that convert into each other: a family and the
// unit's size in the family's base unit.
var unitFamilies = map[string]struct {
	family string
	size   float64
}{
	"tsp": {"volume", 1}, "tbsp": {"volume", 3}, "fl oz": {"volume", 6}, "cup": {"volume", 48},
	"pint": {"volume", 96}, "quart": {"volume", 192}, "gallon": {"volume", 768},
	"ml": {"metric volume", 1}, "l": {"metric volume", 1000},
	"g": {"metric weight", 1}, "kg": {"metric weight", 1000},
	"oz": {"weight", 1}, "lb": {"weight", 16},
}

// leadingFillerWords are dropped from the front of a name when merging, as
// the lists package does when matching ("the onions" is "onion").
var leadingFillerWords = map[string]bool{"the": true, "a": true, "an": true, "some": true, "my": true, "our": true}

// ingredientKey is the merge key of an ingredient name: lower-case singular
// words without leading filler, so "Tomatoes" and "tomato" merge.
func ingredientKey(name string) string {
	var out []string
	for _, w := range words(name) {
		if len(out) == 0 && leadingFillerWords[w] {
			continue
		}
		out = append(out, singular(w))
	}
	return strings.Join(out, " ")
}

// unitAmount is the total of one ingredient in one unit family.
type unitAmount struct {
	unit     string  // the largest unit seen, used for display; "" for a count
	family   string  // unitFamilies family, "unit:<u>" for others, "" for a count
	base     float64 // total in the family's base unit (or in unit)
	measured bool    // some recipe gave a number
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

func (a *unitAmount) String() string {
	q := a.quantity()
	if q == nil {
		if len(a.vague) > 0 {
			return strings.Join(a.vague, " + ")
		}
		return "some"
	}
	s := fmtQty(*q)
	if a.unit != "" {
		s += " " + a.unit
	}
	return s
}

func fmtQty(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// ingredientTotal is one merged ingredient.
type ingredientTotal struct {
	name    string // as first written, capitalised
	key     string
	section string
	amounts []*unitAmount
	meals   []string
}

func (t *ingredientTotal) add(ing storedIngredient, factor float64, meal string) {
	if !slices.Contains(t.meals, meal) {
		t.meals = append(t.meals, meal)
	}
	unit := ""
	if ing.Unit != nil {
		unit = normUnit(*ing.Unit)
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
	if f, ok := unitFamilies[unit]; ok {
		if cur, ok := unitFamilies[a.unit]; !ok || f.size > cur.size {
			a.unit = unit // show the total in the largest unit used
		}
	}
	var qty float64
	var ok bool
	switch q := ing.Quantity.(type) {
	case float64:
		qty, ok = q, q > 0
	case json.Number:
		f, err := q.Float64()
		qty, ok = f, err == nil && f > 0
	case string:
		qty, ok = parseAmount(q)
		if !ok && strings.TrimSpace(q) != "" && !slices.Contains(a.vague, strings.TrimSpace(q)) {
			a.vague = append(a.vague, strings.TrimSpace(q))
		}
	}
	if ok && qty > 0 {
		a.base += qty * factor * size
		a.measured = true
	}
}

// plannedRecipe is one planned meal with the recipe it uses, if any.
type plannedRecipe struct {
	date        time.Time
	slot, title string
	recipeID    *string
	recipeName  *string
	servings    *int
	ingredients []byte
}

func (p plannedRecipe) line() string {
	return fmt.Sprintf("%s %s %s", p.date.Weekday().String()[:3], p.slot, p.title)
}

// loadGroceryMeals reads the meals planned in [from, to] (optionally only
// some slots) with the recipe each uses. A free-text meal whose title is a
// recipe's name counts as that recipe (mealIsRecipe, as meal_suggest).
func loadGroceryMeals(ctx context.Context, q querier, c caller, from, to time.Time, slots []string) ([]plannedRecipe, error) {
	rows, err := q.Query(ctx, `
SELECT m.date, m.slot, m.title, r.id::text, r.title, r.servings, r.ingredients
  FROM meal_plans m
  LEFT JOIN LATERAL (
       SELECT r.id, r.title, r.servings, r.ingredients FROM recipes r
        WHERE `+mealIsRecipe+`
        ORDER BY (r.id = m.recipe_id) IS TRUE DESC, r.id
        LIMIT 1) r ON true
 WHERE m.household_id = $1 AND m.date BETWEEN $2::date AND $3::date
   AND ($4::text[] IS NULL OR m.slot = ANY($4::text[]))
 ORDER BY m.date, array_position(ARRAY['breakfast','lunch','dinner','snack']::text[], m.slot::text), m.id`,
		c.householdID, from.Format(time.DateOnly), to.Format(time.DateOnly), slots)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (plannedRecipe, error) {
		var p plannedRecipe
		err := r.Scan(&p.date, &p.slot, &p.title, &p.recipeID, &p.recipeName, &p.servings, &p.ingredients)
		return p, err
	})
}

// ---------------------------------------------------------------------------
// Arguments
// ---------------------------------------------------------------------------

var groceryAliases = map[string]string{
	"start": "from", "start_date": "from", "from_date": "from", "date": "from", "week": "from",
	"end": "to", "end_date": "to", "to_date": "to", "until": "to", "through": "to",
	"list": "list_id", "list_name": "list_id", "grocery_list": "list_id",
	"slot": "slots", "meal": "slots", "meals": "slots", "meal_type": "slots",
	"factor": "scale", "multiplier": "scale",
	"people": "servings", "serves": "servings",
	"pantry": "skip", "staples": "skip", "exclude": "skip", "skip_items": "skip", "have": "skip",
}

var groceryExample = map[string]any{"from": "today", "to": "sunday", "slots": []any{"dinner"}}

// readScale reads a positive factor: 2, "1.5", "2x", "double", "half".
func readScale(args map[string]any) (float64, bool, error) {
	v, ok := args["scale"]
	if !ok || v == nil {
		return 0, false, nil
	}
	bad := badArg("scale", fmt.Sprintf("scale must be a number above 0 and at most %d.", maxScale), "Pass scale as a factor, e.g. 2 to double every recipe, or leave it out.")
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		switch s {
		case "":
			return 0, false, nil
		case "double", "twice", "x2":
			return 2, true, nil
		case "triple":
			return 3, true, nil
		case "half":
			return 0.5, true, nil
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "x")), 64)
		if err != nil {
			return 0, false, bad
		}
		f = n
	default:
		return 0, false, bad
	}
	if math.IsNaN(f) || f <= 0 || f > maxScale {
		return 0, false, bad
	}
	return f, true, nil
}

// readRange resolves from and to. from also takes "this week" (today to six
// days on) and "next week" (next Monday to Sunday). A weekday word for to
// is the first such day on or after from ("from next monday to sunday").
func readRange(args map[string]any, today time.Time, loc *time.Location) (from, to time.Time, bad *mcp.CallToolResult) {
	from, to = today, time.Time{}
	rawFrom, err := optionalString(args, "from")
	if err != nil {
		return from, to, invalid(err, groceryExample)
	}
	if rawFrom != nil {
		switch strings.ToLower(strings.Join(strings.Fields(*rawFrom), " ")) {
		case "this week", "week", "the week", "this coming week", "coming week":
			from = today
		case "next week":
			ahead := (int(time.Monday) - int(today.Weekday()) + 7) % 7
			if ahead == 0 {
				ahead = 7
			}
			from = today.AddDate(0, 0, ahead)
			to = from.AddDate(0, 0, 6)
		default:
			d, ok := resolveDay(*rawFrom, today, loc, true)
			if !ok {
				return from, to, respond.Invalid("from", fmt.Sprintf("from %q is not a date.", *rawFrom),
					fmt.Sprintf("Pass from as YYYY-MM-DD or a word like today or monday (family timezone %s).", loc), groceryExample)
			}
			from = d
		}
	}
	rawTo, err := optionalString(args, "to")
	if err != nil {
		return from, to, invalid(err, groceryExample)
	}
	if rawTo != nil {
		d, ok := resolveDay(*rawTo, today, loc, true)
		if !ok {
			return from, to, respond.Invalid("to", fmt.Sprintf("to %q is not a date.", *rawTo),
				fmt.Sprintf("Pass to as YYYY-MM-DD or a word like sunday (family timezone %s), or leave it out for a week.", loc), groceryExample)
		}
		if _, err := time.Parse(time.DateOnly, strings.TrimSpace(*rawTo)); err != nil {
			for d.Before(from) && !strings.Contains(strings.ToLower(*rawTo), "last") {
				d = d.AddDate(0, 0, 7) // "sunday" after a later from
			}
		}
		to = d
	}
	if to.IsZero() {
		to = from.AddDate(0, 0, 6)
	}
	if to.Before(from) {
		return from, to, respond.Invalid("to", fmt.Sprintf("to (%s) is before from (%s).", to.Format(time.DateOnly), from.Format(time.DateOnly)),
			"Pass a to on or after from.", groceryExample)
	}
	if days := int(math.Round(to.Sub(from).Hours()/24)) + 1; days > maxGroceryDays {
		return from, to, respond.Invalid("to", fmt.Sprintf("%d days is more than %d in one call.", days, maxGroceryDays),
			"Pass a range of at most a month, e.g. one week.", groceryExample)
	}
	for _, d := range []time.Time{from, to} {
		if days := int(math.Round(d.Sub(today).Hours() / 24)); days > maxPlanDays || days < -maxPlanDays {
			return from, to, respond.Invalid("from", fmt.Sprintf("%s is more than a year from today.", d.Format(time.DateOnly)),
				"Pass dates within a year of today.", groceryExample)
		}
	}
	return from, to, nil
}

// ---------------------------------------------------------------------------
// meal_build_grocery_list
// ---------------------------------------------------------------------------

func cannotAddGroceries(role string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't add to the family's lists.", roleLabel(role)),
		Fix:     "Tell the user to ask a family member; retrying will not help.",
	})
}

// NewMealBuildGroceryListTool creates meal_build_grocery_list (class U):
// the meal plan for a date range becomes merged grocery items in one call.
// Re-running adds nothing twice.
func NewMealBuildGroceryListTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("meal_build_grocery_list", append([]mcp.ToolOption{
		mcp.WithDescription(`Put the meal plan's ingredients for a date range on the grocery list in one call: merged, by store section. Re-running adds nothing twice.`),
		mcp.WithString("from", mcp.Required(), mcp.Description("First day: YYYY-MM-DD, today, monday, or this week / next week")),
		mcp.WithString("to", mcp.Required(), mcp.Description("Last day: YYYY-MM-DD or a word like sunday (default a week from from)")),
		mcp.WithString("list_id", mcp.Description(`List id or name (default the open grocery list; created if none)`)),
		mcp.WithArray("slots", mcp.Description("Only these meals, e.g. [\"dinner\"] (default all)"), mcp.WithStringItems(mcp.Enum(slots...))),
		mcp.WithNumber("servings", mcp.Min(1), mcp.Description("Scale each recipe to this many servings, e.g. 6")),
		mcp.WithNumber("scale", mcp.Description("Or multiply every quantity, e.g. 2 (default 1: as written)")),
		mcp.WithArray("skip", mcp.Description(`Staples the family has, left off, e.g. ["salt","olive oil"]`), mcp.WithStringItems()),
		respond.Schema[groceryResult](),
	}, annotations("Meal plan to grocery list", false, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canRead(c.role) {
			return cannotRead(c.role), nil
		}
		// Adding list items: the family and a housesitter (API ResLists
		// ActAddItems), not a viewer. Starting a new list is family only.
		if !canWrite(c.role) && c.role != "housesitter" {
			return cannotAddGroceries(c.role), nil
		}
		loc := familyLocation(c.tz)
		today := familyToday(c.tz)
		args := withAliases(req.GetArguments(), groceryAliases)

		from, to, bad := readRange(args, today, loc)
		if bad != nil {
			return bad, nil
		}
		slotRefs, _, _, err := readStrings(args, "slots")
		if err != nil {
			return invalid(err, groceryExample), nil
		}
		var slotFilter []string
		for _, s := range slotRefs {
			if l := strings.ToLower(s); l == "all" || l == "any" || l == "every meal" {
				slotFilter = nil
				break
			}
			v, ok := parseSlot(s)
			if !ok {
				return respond.Invalid("slots", fmt.Sprintf("%q is not a meal slot.", s), `Pass slots as a list of breakfast, lunch, dinner or snack, e.g. ["dinner"].`, groceryExample), nil
			}
			if !slices.Contains(slotFilter, v) {
				slotFilter = append(slotFilter, v)
			}
		}
		scale, scaled, err := readScale(args)
		if err != nil {
			return invalid(err, groceryExample), nil
		}
		servings, err := readNum(args, "servings", 1, maxServings)
		if err != nil {
			return invalid(err, groceryExample), nil
		}
		if scaled && servings.set {
			return respond.Invalid("scale", "Pass servings or scale, not both.", "Use servings for a number of people, or scale for a factor.", groceryExample), nil
		}
		skipRefs, _, _, err := readStrings(args, "skip")
		if err != nil {
			return invalid(err, groceryExample), nil
		}
		var skip [][]string
		for _, s := range skipRefs {
			if k := ingredientKey(s); k != "" {
				skip = append(skip, strings.Fields(k))
			}
		}
		listRef := ""
		if s, err := optionalString(args, "list_id"); err != nil {
			return invalid(err, groceryExample), nil
		} else if s != nil {
			listRef = strings.TrimSpace(*s)
		}
		if listRef == "" {
			listRef = "grocery"
		}

		planned, err := loadGroceryMeals(ctx, pool, c, from, to, slotFilter)
		if err != nil {
			return respond.Failed("read the meal plan", err), nil
		}
		res := groceryResult{
			From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), Timezone: loc.String(),
			Items: []groceryItem{}, Sections: []grocerySection{}, Meals: []groceryMeal{},
			Scaled: scaled || servings.set,
		}

		// Merge every recipe's ingredients.
		var totals []*ingredientTotal
		byKey := map[string]*ingredientTotal{}
		noServings := 0
		for _, p := range planned {
			m := groceryMeal{LocalDate: p.date.Format(time.DateOnly), Weekday: p.date.Weekday().String(), Slot: p.slot, Title: p.title, RecipeID: p.recipeID}
			ings, err := decodeIngredients(p.ingredients)
			switch {
			case p.recipeID == nil:
				m.Skipped = ptr("no recipe: free text has no ingredients")
			case err != nil || len(ings) == 0:
				m.Skipped = ptr(fmt.Sprintf("the recipe %s has no ingredients saved", *p.recipeName))
			}
			if m.Skipped != nil {
				res.Meals = append(res.Meals, m)
				continue
			}
			factor := 1.0
			switch {
			case scaled:
				factor = scale
			case servings.set && p.servings != nil && *p.servings > 0:
				factor = float64(servings.val) / float64(*p.servings)
			case servings.set:
				noServings++
			}
			if factor != 1 {
				f := round3(factor)
				m.Scale = &f
			}
			m.Used = true
			res.Meals = append(res.Meals, m)
			line := p.line()
			for _, ing := range ings {
				key := ingredientKey(ing.Name)
				if key == "" {
					continue
				}
				if isTapWater(key) {
					continue
				}
				if slices.ContainsFunc(skip, func(s []string) bool { return containsWords(strings.Fields(key), s) }) {
					if n := capitalize(strings.TrimSpace(ing.Name)); !slices.ContainsFunc(res.SkippedStaples, func(x string) bool { return ingredientKey(x) == key }) {
						res.SkippedStaples = append(res.SkippedStaples, n)
					}
					continue
				}
				t := byKey[key]
				if t == nil {
					t = &ingredientTotal{name: capitalize(strings.Join(strings.Fields(ing.Name), " ")), key: key, section: sectionOf(ing.Name)}
					byKey[key] = t
					totals = append(totals, t)
				}
				t.add(ing, factor, line)
			}
		}
		if noServings > 0 {
			res.Notes = append(res.Notes, fmt.Sprintf("%s list no servings, so they were not scaled.", plural(noServings, "recipe")))
		}
		if !res.Scaled {
			res.Notes = append(res.Notes, "Quantities are per recipe as written; pass servings or scale to change them.")
		}
		slices.SortStableFunc(totals, func(a, b *ingredientTotal) int {
			if d := slices.Index(sectionOrder, a.section) - slices.Index(sectionOrder, b.section); d != 0 {
				return d
			}
			return strings.Compare(a.key, b.key)
		})

		if len(totals) == 0 {
			return respond.OK(emptyGrocerySummary(res, planned), res, groceryHints(res, planned, c.role)...), nil
		}

		// One item per ingredient. Several unit families (2 tbsp + 1 stick of
		// butter) stay one item with no quantity, so the list never holds the
		// same ingredient twice; the amounts are in note.
		add := make([]lists.AddItem, 0, len(totals))
		for _, t := range totals {
			it := groceryItem{Text: t.name, Section: t.section, For: t.meals}
			// One measured amount: that is the quantity, and amounts without
			// a number ("a pinch") are a note. Several measured amounts in
			// units that don't convert (2 tbsp + 1 stick of butter) stay one
			// item without a quantity, so the list never holds the same
			// ingredient twice; the amounts are in note.
			var measured []*unitAmount
			var vague []string
			for _, a := range t.amounts {
				if a.quantity() != nil {
					measured = append(measured, a)
				}
				for _, v := range a.vague {
					if !slices.Contains(vague, v) {
						vague = append(vague, v)
					}
				}
			}
			switch len(measured) {
			case 0:
				if len(vague) > 0 {
					it.Note = ptr(strings.Join(vague, " + "))
				}
			case 1:
				it.Quantity = measured[0].quantity()
				if u := measured[0].unit; u != "" {
					it.Unit = &u
				}
				if len(vague) > 0 {
					it.Note = ptr("plus " + strings.Join(vague, " + "))
				}
			default:
				parts := make([]string, 0, len(measured)+len(vague))
				for _, a := range measured {
					parts = append(parts, fmtQty(*a.quantity())+strings.TrimRight(" "+a.unit, " "))
				}
				it.Note = ptr("amounts in different units: " + strings.Join(append(parts, vague...), " + "))
			}
			res.Items = append(res.Items, it)
			section := t.section
			add = append(add, lists.AddItem{Text: it.Text, Quantity: it.Quantity, Unit: it.Unit, Section: &section})
		}

		example := map[string]any{"from": res.From, "to": res.To, "list_id": "Grocery"}
		out, created, bad, err := lists.AddItems(ctx, pool, c.householdID, listRef, add, c.role == "housesitter", example)
		if bad != nil {
			return bad, nil
		}
		if err != nil {
			return respond.Failed("add the groceries", err), nil
		}
		res.List = &groceryList{ID: out.List.ID, Name: out.List.Name, OpenCount: out.List.OpenCount, Created: out.CreatedList}

		index := map[string]int{}
		for i, it := range res.Items {
			index[ingredientKey(it.Text)] = i
		}
		mark := func(status string, text, id string, qty *float64, unit *string) {
			i, ok := index[ingredientKey(text)]
			if !ok {
				return
			}
			it := &res.Items[i]
			it.Status, it.ItemID = status, id
			if status == "already_on_list" && it.Quantity != nil && (qty == nil || *qty != *it.Quantity || !sameUnit(unit, it.Unit)) {
				it.Note = ptr(fmt.Sprintf("the list says %q %s; the plan needs %s", text, amountText(qty, unit), amountText(it.Quantity, it.Unit)))
			}
		}
		for _, x := range out.Added {
			mark("added", x.Text, x.ID, x.Quantity, x.Unit)
		}
		for _, x := range out.Reopened {
			mark("reopened", x.Text, x.ID, x.Quantity, x.Unit)
		}
		for _, x := range out.AlreadyOnList {
			mark("already_on_list", x.Text, x.ID, x.Quantity, x.Unit)
		}
		for _, s := range sectionOrder {
			var texts []string
			for _, it := range res.Items {
				if it.Section == s {
					texts = append(texts, it.Text)
				}
			}
			if len(texts) > 0 {
				res.Sections = append(res.Sections, grocerySection{Section: s, Items: texts})
			}
		}
		return respond.OK(grocerySummary(res, created), res, groceryHints(res, planned, c.role)...), nil
	}
	return "meal_build_grocery_list", tool, handler
}

func ptr[T any](v T) *T { return &v }

// isTapWater: "water", "warm water" and the like are never shopped for.
func isTapWater(key string) bool {
	ws := strings.Fields(key)
	switch len(ws) {
	case 1:
		return ws[0] == "water"
	case 2:
		return ws[1] == "water" && slices.Contains([]string{"hot", "cold", "warm", "boiling", "ice", "tap", "lukewarm"}, ws[0])
	}
	return false
}

func containsWords(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return len(want) > 0
}

func sameUnit(a, b *string) bool {
	switch {
	case a == nil || *a == "":
		return b == nil || *b == ""
	case b == nil:
		return false
	}
	return normUnit(*a) == normUnit(*b)
}

func amountText(q *float64, u *string) string {
	if q == nil {
		return "no amount"
	}
	s := fmtQty(*q)
	if u != nil && *u != "" {
		s += " " + *u
	}
	return s
}

func itemLine(it groceryItem) string {
	if it.Quantity == nil {
		if it.Note != nil && strings.HasPrefix(*it.Note, "amounts in") {
			return fmt.Sprintf("%s (%s)", it.Text, strings.TrimPrefix(*it.Note, "amounts in different units: "))
		}
		return it.Text
	}
	return fmt.Sprintf("%s (%s)", it.Text, amountText(it.Quantity, it.Unit))
}

func rangeText(r groceryResult) string {
	f, _ := time.Parse(time.DateOnly, r.From)
	t, _ := time.Parse(time.DateOnly, r.To)
	if r.From == r.To {
		return describeDay(f)
	}
	return fmt.Sprintf("%s %s to %s %s", f.Weekday().String()[:3], r.From, t.Weekday().String()[:3], r.To)
}

func skippedText(r groceryResult) string {
	var s []string
	for _, m := range r.Meals {
		if m.Skipped != nil {
			reason := "no recipe"
			if m.RecipeID != nil {
				reason = "no ingredients saved"
			}
			s = append(s, fmt.Sprintf("%s %s %s (%s)", m.Weekday[:3], m.Slot, m.Title, reason))
		}
	}
	if len(s) == 0 {
		return ""
	}
	return fmt.Sprintf(" Skipped %s: %s.", plural(len(s), "meal"), strings.Join(s, ", "))
}

func emptyGrocerySummary(r groceryResult, planned []plannedRecipe) string {
	if len(planned) == 0 {
		return fmt.Sprintf("Nothing is planned for %s, so nothing was added to the grocery list.", rangeText(r))
	}
	s := fmt.Sprintf("Nothing to add to the grocery list for %s.", rangeText(r)) + skippedText(r)
	if len(r.SkippedStaples) > 0 {
		s += fmt.Sprintf(" Left off as staples: %s.", strings.Join(r.SkippedStaples, ", "))
	}
	return s
}

func grocerySummary(r groceryResult, created string) string {
	var added, reopened, already int
	for _, it := range r.Items {
		switch it.Status {
		case "added":
			added++
		case "reopened":
			reopened++
		case "already_on_list":
			already++
		}
	}
	used := 0
	for _, m := range r.Meals {
		if m.Used {
			used++
		}
	}
	var b strings.Builder
	if created != "" {
		b.WriteString(created + " ")
	}
	fmt.Fprintf(&b, "Grocery list %q for %s from %s: %s added", r.List.Name, rangeText(r), plural(used, "meal"), plural(added, "item"))
	if reopened > 0 {
		fmt.Fprintf(&b, ", %d put back", reopened)
	}
	if already > 0 {
		fmt.Fprintf(&b, ", %d already on the list", already)
	}
	b.WriteString(".")
	n := 0
	for _, sec := range r.Sections {
		var lines []string
		for _, it := range r.Items {
			if it.Section != sec.Section || n >= summaryItems {
				continue
			}
			lines = append(lines, itemLine(it))
			n++
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, " %s: %s.", sec.Section, strings.Join(lines, ", "))
		}
	}
	if len(r.Items) > n {
		fmt.Fprintf(&b, " And %d more.", len(r.Items)-n)
	}
	diff := 0
	for _, it := range r.Items {
		if it.Note != nil && strings.HasPrefix(*it.Note, "the list says") {
			diff++
		}
	}
	if diff > 0 {
		fmt.Fprintf(&b, " %s already on the list with a different amount (see note).", plural(diff, "item"))
	}
	b.WriteString(skippedText(r))
	if len(r.SkippedStaples) > 0 {
		fmt.Fprintf(&b, " Left off as staples: %s.", strings.Join(r.SkippedStaples, ", "))
	}
	for _, note := range r.Notes {
		b.WriteString(" " + note)
	}
	fmt.Fprintf(&b, " (%d open on the list.)", r.List.OpenCount)
	return b.String()
}

func groceryHints(r groceryResult, planned []plannedRecipe, role string) []string {
	var out []string
	if r.List != nil {
		out = append(out, fmt.Sprintf("See the whole list with list_get list_id=%s.", r.List.ID),
			fmt.Sprintf(`Check items off while shopping with list_check_items list_id=%s texts=["…"].`, r.List.ID))
	}
	write := canWrite(role)
	if len(planned) == 0 && write {
		out = append(out, `Plan meals first with meal_set meals=[{"date":"…","slot":"dinner","recipe_id":"…"}], or get ideas with meal_suggest.`)
	}
	for _, m := range r.Meals {
		if m.Skipped != nil && m.RecipeID == nil && write {
			out = append(out, fmt.Sprintf(`Link a saved recipe with meal_set meals=[{"date":"%s","slot":"%s","recipe_id":"…"}], then run this again.`, m.LocalDate, m.Slot))
			break
		}
	}
	return out
}
