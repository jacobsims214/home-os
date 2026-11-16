package meals

// The meal plan tools (catalog v2 "meal", ticket #4487): meal_set (U) plans
// or clears one or many date+slot entries in one call, with a recipe or free
// text; meal_rate (U) records how one person liked one meal. The plan is read
// with agenda_get areas=["meals"], which calls AgendaMeals below (the catalog
// has no separate meal-read tool).
//
// Dates are civil dates in the family's timezone (households.timezone).
// Small models say "tuesday" or "tomorrow", so meal_set and meal_rate accept
// those words too and answer with the resolved YYYY-MM-DD and weekday.
//
// Every planned meal carries allergy warnings for the family (everyone eats
// the family meal): a recipe's ingredients, or a free-text meal's name, are
// matched against people.allergies with the recipe tools' matcher.
//
// Ratings follow the API's policy (apps/api/internal/meals DefaultPolicy):
// owners and family managers rate for anyone (kids have no login), a family
// member rates only as the person linked to their own login, viewers and
// housesitters read the plan, vendors get nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

const (
	maxMealsPerCall = 62  // a month of lunches and dinners
	maxPlanDays     = 366 // how far from today a meal may be planned or rated
	maxCommentLen   = 2000
	maxMealNotesLen = 2000
)

// slots in the order a day is read.
var slots = []string{"breakfast", "lunch", "dinner", "snack"}

var slotAliases = map[string]string{
	"breakfast": "breakfast", "lunch": "lunch", "dinner": "dinner", "snack": "snack",
	"supper": "dinner", "tea": "dinner", "snacks": "snack", "dinnertime": "dinner",
}

// parseSlot reads a slot word ("Dinner", "supper").
func parseSlot(raw string) (string, bool) {
	s, ok := slotAliases[strings.ToLower(strings.TrimSpace(raw))]
	return s, ok
}

// ---------------------------------------------------------------------------
// Dates
// ---------------------------------------------------------------------------

// familyLocation is the family's timezone (UTC when unknown).
func familyLocation(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

var weekdayNames = map[string]time.Weekday{
	"sunday": time.Sunday, "sun": time.Sunday,
	"monday": time.Monday, "mon": time.Monday,
	"tuesday": time.Tuesday, "tue": time.Tuesday, "tues": time.Tuesday,
	"wednesday": time.Wednesday, "wed": time.Wednesday, "weds": time.Wednesday,
	"thursday": time.Thursday, "thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday,
	"friday": time.Friday, "fri": time.Friday,
	"saturday": time.Saturday, "sat": time.Saturday,
}

// resolveDay turns a date argument into a civil date (midnight UTC) in the
// family's timezone. It accepts YYYY-MM-DD, a timestamp with an offset,
// today/tonight/tomorrow/yesterday/last night, and weekday names with an
// optional this/next/last/coming. A bare weekday looks forward from today
// when planning (forward) and back from today when rating: "tuesday" is the
// coming Tuesday for meal_set and the last one for meal_rate (today counts
// for both). "next tuesday" is the first Tuesday after today and "last
// tuesday" the last one before today.
func resolveDay(raw string, today time.Time, loc *time.Location, forward bool) (time.Time, bool) {
	s := strings.ToLower(strings.Join(strings.Fields(raw), " "))
	if s == "" {
		return time.Time{}, false
	}
	if d, err := time.Parse(time.DateOnly, s); err == nil {
		return d, true
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
		y, m, d := t.In(loc).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC), true
	}
	switch s {
	case "today", "tonight", "this evening", "this morning", "this afternoon", "now":
		return today, true
	case "tomorrow", "tomorrow night", "tomorrow evening", "tomorrow morning":
		return today.AddDate(0, 0, 1), true
	case "yesterday", "last night", "yesterday evening":
		return today.AddDate(0, 0, -1), true
	}
	fields := strings.Fields(s)
	mod := ""
	if len(fields) == 2 && slices.Contains([]string{"this", "next", "last", "coming", "on", "past"}, fields[0]) {
		mod, fields = fields[0], fields[1:]
	}
	if len(fields) != 1 {
		return time.Time{}, false
	}
	wd, ok := weekdayNames[strings.TrimSuffix(strings.TrimSuffix(fields[0], "'s"), ".")]
	if !ok {
		return time.Time{}, false
	}
	ahead := (int(wd) - int(today.Weekday()) + 7) % 7
	back := (int(today.Weekday()) - int(wd) + 7) % 7
	switch {
	case mod == "next":
		if ahead == 0 {
			ahead = 7
		}
		return today.AddDate(0, 0, ahead), true
	case mod == "last" || mod == "past":
		if back == 0 {
			back = 7
		}
		return today.AddDate(0, 0, -back), true
	case mod == "coming" || forward:
		return today.AddDate(0, 0, ahead), true
	}
	return today.AddDate(0, 0, -back), true
}

// describeDay is "Tuesday 2026-11-17".
func describeDay(d time.Time) string { return d.Weekday().String() + " " + d.Format(time.DateOnly) }

// ---------------------------------------------------------------------------
// Output types
// ---------------------------------------------------------------------------

// MealRating is one person's rating of a planned meal.
type MealRating struct {
	Person   *string `json:"person" jsonschema_description:"Who rated it; null when the person was removed."`
	PersonID *string `json:"person_id"`
	Rating   *int    `json:"rating" jsonschema_description:"1-5; null for a comment only."`
	Comment  *string `json:"comment"`
}

// PlannedMeal is one meal on the plan (or in the history, once its date has
// passed).
type PlannedMeal struct {
	MealID          string       `json:"meal_id"`
	LocalDate       string       `json:"local_date" jsonschema_description:"YYYY-MM-DD in the family timezone."`
	Weekday         string       `json:"weekday" jsonschema_description:"e.g. Tuesday."`
	Slot            string       `json:"slot" jsonschema_description:"breakfast, lunch, dinner or snack."`
	Title           string       `json:"title" jsonschema_description:"What is eaten: the recipe name or free text."`
	RecipeID        *string      `json:"recipe_id" jsonschema_description:"The recipe, for recipe_get; null for free text."`
	Notes           *string      `json:"notes,omitempty"`
	Ratings         []MealRating `json:"ratings,omitempty" jsonschema_description:"How each person liked it."`
	AllergyWarnings []string     `json:"allergy_warnings,omitempty" jsonschema_description:"e.g. Nora (peanuts): peanut butter. Always tell the user."`
}

// mealSetItem is what meal_set did to one date+slot.
type mealSetItem struct {
	Action         string       `json:"action" jsonschema_description:"planned, replaced, updated, unchanged, cleared or already_empty."`
	LocalDate      string       `json:"local_date"`
	Weekday        string       `json:"weekday"`
	Slot           string       `json:"slot"`
	Meal           *PlannedMeal `json:"meal" jsonschema_description:"The meal now planned; null when cleared."`
	Previous       *string      `json:"previous,omitempty" jsonschema_description:"The meal it replaced or cleared."`
	RatingsRemoved int          `json:"ratings_removed,omitempty" jsonschema_description:"Ratings deleted with a cleared meal."`
	SimilarRecipes []string     `json:"similar_recipes,omitempty" jsonschema_description:"Free text that resembles saved recipes; pass recipe_id to link one."`
}

type mealSetResult struct {
	Meals    []mealSetItem `json:"meals" jsonschema_description:"One entry per date+slot, by date then slot."`
	Timezone string        `json:"timezone" jsonschema_description:"The family timezone dates are in."`
	Notes    []string      `json:"notes,omitempty" jsonschema_description:"Things the user should know, e.g. a param that is not saved."`
}

type mealRateResult struct {
	Action         string      `json:"action" jsonschema_description:"saved, updated or unchanged."`
	Person         string      `json:"person"`
	PersonID       string      `json:"person_id"`
	Rating         *int        `json:"rating" jsonschema_description:"1-5; null when only a comment is saved."`
	Comment        *string     `json:"comment"`
	PreviousRating *int        `json:"previous_rating,omitempty" jsonschema_description:"The rating this one replaced."`
	Meal           PlannedMeal `json:"meal" jsonschema_description:"The meal with everyone's ratings."`
}

// ---------------------------------------------------------------------------
// Reading the plan
// ---------------------------------------------------------------------------

// loadPlan returns the meals planned from from to to (YYYY-MM-DD, inclusive),
// by date then slot, with ratings and allergy warnings.
func loadPlan(ctx context.Context, q querier, hid uuid.UUID, from, to string) ([]PlannedMeal, error) {
	rows, err := q.Query(ctx, `
SELECT m.id::text, m.date::text, m.slot, m.title, m.recipe_id::text, m.notes, r.ingredients
  FROM meal_plans m
  LEFT JOIN recipes r ON r.id = m.recipe_id AND r.household_id = m.household_id
 WHERE m.household_id = $1 AND m.date BETWEEN $2::date AND $3::date
 ORDER BY m.date, array_position(ARRAY['breakfast','lunch','dinner','snack']::text[], m.slot::text), m.id`, hid, from, to)
	if err != nil {
		return nil, err
	}
	type row struct {
		meal PlannedMeal
		ings []byte
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.meal.MealID, &x.meal.LocalDate, &x.meal.Slot, &x.meal.Title, &x.meal.RecipeID, &x.meal.Notes, &x.ings)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	out := make([]PlannedMeal, 0, len(all))
	if len(all) == 0 {
		return out, nil
	}
	matchers, err := familyAllergies(ctx, q, hid)
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	for _, x := range all {
		m := x.meal
		if d, err := time.Parse(time.DateOnly, m.LocalDate); err == nil {
			m.Weekday = d.Weekday().String()
		}
		m.AllergyWarnings = mealWarnings(matchers, m.Title, m.RecipeID != nil, x.ings)
		index[m.MealID] = len(out)
		out = append(out, m)
	}

	fb, err := q.Query(ctx, `
SELECT f.meal_plan_id::text, p.display_name, f.person_id::text, f.rating, f.comment
  FROM meal_feedback f
  JOIN meal_plans m ON m.id = f.meal_plan_id
  LEFT JOIN people p ON p.id = f.person_id AND p.household_id = f.household_id
 WHERE m.household_id = $1 AND m.date BETWEEN $2::date AND $3::date
 ORDER BY lower(p.display_name) NULLS LAST, f.created_at, f.id`, hid, from, to)
	if err != nil {
		return nil, err
	}
	defer fb.Close()
	for fb.Next() {
		var mealID string
		var r MealRating
		var rating *int16
		if err := fb.Scan(&mealID, &r.Person, &r.PersonID, &rating, &r.Comment); err != nil {
			return nil, err
		}
		if rating != nil {
			v := int(*rating)
			r.Rating = &v
		}
		if i, ok := index[mealID]; ok {
			out[i].Ratings = append(out[i].Ratings, r)
		}
	}
	return out, fb.Err()
}

// mealWarnings matches a meal against the family's allergies: a recipe's
// ingredients, or a free-text meal's own name ("peanut noodles").
func mealWarnings(matchers []allergyMatcher, title string, hasRecipe bool, rawIngredients []byte) []string {
	if len(matchers) == 0 {
		return nil
	}
	var items []string
	if hasRecipe && len(rawIngredients) > 0 {
		if ings, err := decodeIngredients(rawIngredients); err == nil {
			items = ingredientNames(ings)
		}
	}
	if !hasRecipe {
		items = []string{title}
	}
	var out []string
	for _, l := range warningLines(AllergyWarnings(matchers, items)) {
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

// AgendaMeals is agenda_get's meals area: the meals planned from first to
// last (dates in the family timezone, inclusive). allowed is false when the
// caller's role can't see meal plans (vendor, or not signed in).
func AgendaMeals(ctx context.Context, pool *pgxpool.Pool, first, last time.Time) (meals []PlannedMeal, allowed bool, err error) {
	c, err := callerInfo(ctx, pool)
	if errors.Is(err, errNotSignedIn) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !canRead(c.role) {
		return nil, false, nil
	}
	meals, err = loadPlan(ctx, pool, c.householdID, first.Format(time.DateOnly), last.Format(time.DateOnly))
	return meals, true, err
}

// AgendaMealLine is one meal for a summary: "Tue dinner Chicken Tacos".
func AgendaMealLine(m PlannedMeal) string {
	wd := m.Weekday
	if len(wd) > 3 {
		wd = wd[:3]
	}
	return fmt.Sprintf("%s %s %s", wd, m.Slot, m.Title)
}

// ---------------------------------------------------------------------------
// Access for the plan
// ---------------------------------------------------------------------------

func cannotPlan(role, tool string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't change the meal plan (tool %s): only an owner, family manager or family member can.", roleLabel(role), tool),
		Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
	})
}

// canRateOthers: API meals CanRateOthers (auth.CanManageMembers).
func canRateOthers(role string) bool { return role == "owner" || role == "family_manager" }

// ---------------------------------------------------------------------------
// meal_set
// ---------------------------------------------------------------------------

var mealsAliases = map[string]string{"meal_plan": "meals", "plan": "meals", "entries": "meals", "days": "meals", "items": "meals"}

var entryAliases = map[string]string{
	"day": "date", "on": "date", "when": "date",
	"meal_type": "slot", "meal_slot": "slot", "time": "slot",
	"recipe": "recipe_id", "recipe_name": "recipe_id",
	"title": "text", "name": "text", "dish": "text", "description": "text", "food": "text",
	"note": "notes", "remove": "clear", "delete": "clear",
}

var setExample = map[string]any{"meals": []any{
	map[string]any{"date": "tuesday", "slot": "dinner", "text": "tacos"},
	map[string]any{"date": "2026-11-18", "slot": "dinner", "recipe_id": "Chicken Tacos"},
}}

// mealEntry is one validated meals[] entry.
type mealEntry struct {
	field     string // "meals[2]"
	date      time.Time
	slot      string
	recipeRef *string
	text      *string
	notes     textField
	clear     bool
	recipeID  *uuid.UUID
	similar   []string
}

func (e mealEntry) key() string { return e.date.Format(time.DateOnly) + " " + e.slot }

func readBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "yes", "1":
			return true, true
		case "false", "no", "0", "":
			return false, true
		}
	case float64:
		return x != 0, true
	}
	return false, false
}

// mealEntries reads meals[] (a list, one object, a JSON string of either, or
// the entry's fields at the top level).
func mealEntries(args map[string]any) ([]map[string]any, error) {
	args = withAliases(args, mealsAliases)
	raw, ok := args["meals"]
	if !ok || raw == nil {
		for _, k := range []string{"date", "day", "slot", "recipe_id", "recipe", "text", "title"} {
			if given(args, k) {
				return []map[string]any{args}, nil
			}
		}
		return nil, badArg("meals", "meals is required: a list of {date, slot, recipe_id or text}.",
			`Pass meals=[{"date":"tuesday","slot":"dinner","text":"tacos"}].`)
	}
	if s, isStr := raw.(string); isStr {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil, badArg("meals", "meals must be a list of objects.", `Pass meals=[{"date":"tuesday","slot":"dinner","text":"tacos"}].`)
		}
		raw = v
	}
	var list []any
	switch x := raw.(type) {
	case []any:
		list = x
	case map[string]any:
		list = []any{x}
	default:
		return nil, badArg("meals", "meals must be a list of objects.", `Pass meals=[{"date":"tuesday","slot":"dinner","text":"tacos"}].`)
	}
	if len(list) == 0 {
		return nil, badArg("meals", "meals is empty.", `Pass at least one entry, e.g. {"date":"tuesday","slot":"dinner","text":"tacos"}.`)
	}
	if len(list) > maxMealsPerCall {
		return nil, badArg("meals", fmt.Sprintf("At most %d meals per call.", maxMealsPerCall), "Plan a week or two at a time.")
	}
	out := make([]map[string]any, 0, len(list))
	for i, v := range list {
		m, isMap := v.(map[string]any)
		if !isMap {
			return nil, badArg(fmt.Sprintf("meals[%d]", i), fmt.Sprintf("meals[%d] must be an object.", i),
				`Pass each meal as {"date":"tuesday","slot":"dinner","text":"tacos"}.`)
		}
		out = append(out, m)
	}
	return out, nil
}

// parseEntry validates one meals[] entry.
func parseEntry(i int, m map[string]any, today time.Time, loc *time.Location) (mealEntry, bool, error) {
	f := fmt.Sprintf("meals[%d]", i)
	// {"meal":"dinner"} names the slot; {"meal":"tacos"} the food.
	if v, ok := m["meal"].(string); ok {
		m = withAliases(m, nil)
		delete(m, "meal")
		if _, isSlot := parseSlot(v); isSlot && !given(m, "slot") {
			m["slot"] = v
		} else if !given(m, "text") {
			m["text"] = v
		}
	}
	m = withAliases(m, entryAliases)
	e := mealEntry{field: f}
	servings := given(m, "servings")

	dateRaw, err := optionalString(m, "date")
	if err != nil || dateRaw == nil {
		return e, servings, badArg(f+".date", fmt.Sprintf("%s.date is required.", f),
			fmt.Sprintf("Pass date as YYYY-MM-DD or a word like tuesday or tomorrow (family timezone %s).", loc))
	}
	d, ok := resolveDay(*dateRaw, today, loc, true)
	if !ok {
		return e, servings, badArg(f+".date", fmt.Sprintf("%s.date %q is not a date.", f, *dateRaw),
			fmt.Sprintf("Pass date as YYYY-MM-DD or a word like tuesday or tomorrow (family timezone %s).", loc))
	}
	if days := int(math.Round(d.Sub(today).Hours() / 24)); days > maxPlanDays || days < -maxPlanDays {
		return e, servings, badArg(f+".date", fmt.Sprintf("%s.date %s is more than a year from today.", f, d.Format(time.DateOnly)),
			"Plan meals within a year of today.")
	}
	e.date = d

	e.slot = "dinner"
	if s, err := optionalString(m, "slot"); err != nil {
		return e, servings, badArg(f+".slot", f+".slot must be text.", "Pass slot as breakfast, lunch, dinner or snack.")
	} else if s != nil {
		if e.slot, ok = parseSlot(*s); !ok {
			return e, servings, badArg(f+".slot", fmt.Sprintf("%s.slot %q is not a meal slot.", f, *s), "Pass slot as breakfast, lunch, dinner or snack.")
		}
	}

	if v, ok := m["clear"]; ok && v != nil {
		b, ok := readBool(v)
		if !ok {
			return e, servings, badArg(f+".clear", f+".clear must be true or false.", "Pass clear:true to empty the slot.")
		}
		e.clear = b
	}
	if e.recipeRef, err = optionalString(m, "recipe_id"); err != nil {
		return e, servings, badArg(f+".recipe_id", f+".recipe_id must be text.", "Pass the recipe's id from recipe_find, or its name.")
	}
	if e.text, err = optionalString(m, "text"); err != nil {
		return e, servings, badArg(f+".text", f+".text must be text.", `Pass what is eaten as text, e.g. "pizza night".`)
	}
	if e.text != nil && utf8.RuneCountInString(*e.text) > maxNameLen {
		return e, servings, badArg(f+".text", fmt.Sprintf("%s.text is longer than %d characters.", f, maxNameLen), "Shorten it; put details in notes.")
	}
	if e.notes, err = readText(m, "notes", maxMealNotesLen); err != nil {
		return e, servings, badArg(f+".notes", fmt.Sprintf("%s.notes must be text up to %d characters.", f, maxMealNotesLen), "Shorten the notes.")
	}
	switch {
	case e.clear && (e.recipeRef != nil || e.text != nil):
		return e, servings, badArg(f+".clear", f+" has clear:true and a meal.", "Pass clear:true alone to empty the slot, or a recipe_id/text to plan it.")
	case !e.clear && e.recipeRef == nil && e.text == nil:
		return e, servings, badArg(f+".text", f+" needs a recipe_id or text (or clear:true).",
			`Pass text for free text ("pizza night") or recipe_id for a saved recipe.`)
	}
	return e, servings, nil
}

// resolveEntries links each entry to a recipe: recipe_id strictly (an id, or
// the exact name), text by exact name when exactly one recipe has it. Free
// text that only resembles recipes keeps the text and lists the look-alikes.
func resolveEntries(ctx context.Context, q querier, hid uuid.UUID, entries []mealEntry) (*mcp.CallToolResult, error) {
	for i := range entries {
		e := &entries[i]
		switch {
		case e.recipeRef != nil:
			id, bad, err := resolveRecipe(ctx, q, hid, map[string]any{"recipe_id": *e.recipeRef}, setExample)
			if err != nil || bad != nil {
				return bad, err
			}
			e.recipeID = &id
		case e.text != nil:
			rows, err := q.Query(ctx, `SELECT id, title, lower(btrim(title)) = lower(btrim($2)) FROM recipes
			                            WHERE household_id = $1 AND (lower(btrim(title)) = lower(btrim($2)) OR title ILIKE '%' || $3 || '%' ESCAPE '\')
			                            ORDER BY 3 DESC, lower(title), id LIMIT 4`, hid, *e.text, escapeLike(*e.text))
			if err != nil {
				return nil, err
			}
			var exact []uuid.UUID
			for rows.Next() {
				var id uuid.UUID
				var title string
				var isExact bool
				if err := rows.Scan(&id, &title, &isExact); err != nil {
					rows.Close()
					return nil, err
				}
				if isExact {
					exact = append(exact, id)
				}
				e.similar = append(e.similar, fmt.Sprintf("%s (%s)", title, id))
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
			if len(exact) == 1 {
				e.recipeID, e.similar = &exact[0], nil
			}
		}
	}
	return nil, nil
}

// NewMealSetTool creates meal_set (catalog v2, class U).
func NewMealSetTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	entrySchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"date":      map[string]any{"type": "string", "description": "YYYY-MM-DD, or tuesday, tomorrow, next friday (family timezone)"},
			"slot":      map[string]any{"type": "string", "enum": slots, "description": "Default dinner"},
			"recipe_id": map[string]any{"type": "string", "description": "A saved recipe: id from recipe_find, or its exact name"},
			"text":      map[string]any{"type": "string", "description": "Free text instead of a recipe, e.g. pizza night"},
			"notes":     map[string]any{"type": "string", "description": "e.g. Grandma visiting, double it"},
			"clear":     map[string]any{"type": "boolean", "description": "true empties the slot"},
		},
		"required": []string{"date"},
	}
	tool := mcp.NewTool("meal_set", append([]mcp.ToolOption{
		mcp.WithDescription(`Plan meals: set or clear one or many date+slot entries in one call, with a recipe or free text ("pizza night"). Warns about allergies.`),
		mcp.WithArray("meals", mcp.Required(), mcp.Description("Entries {date, slot, recipe_id or text, notes, clear}"), mcp.Items(entrySchema)),
		respond.Schema[mealSetResult](),
	}, annotations("Plan meals", false, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotPlan(c.role, "meal_set"), nil
		}
		loc := familyLocation(c.tz)
		today := familyToday(c.tz)
		raw, err := mealEntries(req.GetArguments())
		if err != nil {
			return invalid(err, setExample), nil
		}
		entries := make([]mealEntry, 0, len(raw))
		seen := map[string]string{}
		var servingsGiven bool
		for i, m := range raw {
			e, servings, err := parseEntry(i, m, today, loc)
			if err != nil {
				return invalid(err, setExample), nil
			}
			servingsGiven = servingsGiven || servings
			if prev, dup := seen[e.key()]; dup {
				return respond.Invalid(e.field+".date",
					fmt.Sprintf("%s and %s are both %s %s.", prev, e.field, describeDay(e.date), e.slot),
					"Pass one entry per date and slot.", setExample), nil
			}
			seen[e.key()] = e.field
			entries = append(entries, e)
		}
		sort.SliceStable(entries, func(i, j int) bool {
			if !entries[i].date.Equal(entries[j].date) {
				return entries[i].date.Before(entries[j].date)
			}
			return slices.Index(slots, entries[i].slot) < slices.Index(slots, entries[j].slot)
		})
		if bad, err := resolveEntries(ctx, pool, c.householdID, entries); err != nil {
			return respond.Failed("plan the meals", err), nil
		} else if bad != nil {
			return bad, nil
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("plan the meals", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		res := mealSetResult{Meals: make([]mealSetItem, 0, len(entries)), Timezone: loc.String()}
		for _, e := range entries {
			item, err := applyEntry(ctx, tx, c.householdID, e)
			if errors.Is(err, pgx.ErrNoRows) {
				return respond.NotFound("recipe", e.recipeID.String(), "Call recipe_find to search the family's recipes."), nil
			}
			if err != nil {
				return respond.Failed("plan the meals", err), nil
			}
			res.Meals = append(res.Meals, item)
		}
		first, last := entries[0].date.Format(time.DateOnly), entries[len(entries)-1].date.Format(time.DateOnly)
		plan, err := loadPlan(ctx, tx, c.householdID, first, last)
		if err != nil {
			return respond.Failed("plan the meals", err), nil
		}
		for i := range res.Meals {
			it := &res.Meals[i]
			if it.Action == "cleared" || it.Action == "already_empty" {
				continue
			}
			for _, m := range plan {
				if m.LocalDate == it.LocalDate && m.Slot == it.Slot {
					m := m
					it.Meal = &m
				}
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("plan the meals", err), nil
		}
		if servingsGiven {
			res.Notes = append(res.Notes, "servings is not saved on a planned meal; the recipe's own servings are used.")
		}
		return respond.OK(setSummary(res), res, setHints(res)...), nil
	}
	return "meal_set", tool, handler
}

// applyEntry writes one entry inside tx.
func applyEntry(ctx context.Context, tx pgx.Tx, hid uuid.UUID, e mealEntry) (mealSetItem, error) {
	item := mealSetItem{LocalDate: e.date.Format(time.DateOnly), Weekday: e.date.Weekday().String(), Slot: e.slot, SimilarRecipes: e.similar}
	var (
		curID     uuid.UUID
		curRecipe *uuid.UUID
		curTitle  string
		curNotes  *string
	)
	err := tx.QueryRow(ctx, `SELECT id, recipe_id, title, notes FROM meal_plans
	                          WHERE household_id = $1 AND date = $2 AND slot = $3 FOR UPDATE`, hid, e.date, e.slot).
		Scan(&curID, &curRecipe, &curTitle, &curNotes)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return item, err
	}

	if e.clear {
		if !exists {
			item.Action = "already_empty"
			return item, nil
		}
		if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM meal_feedback WHERE meal_plan_id = $1`, curID).Scan(&item.RatingsRemoved); err != nil {
			return item, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM meal_plans WHERE id = $1`, curID); err != nil {
			return item, err
		}
		item.Action, item.Previous = "cleared", &curTitle
		return item, nil
	}

	var title string
	if e.recipeID != nil {
		// Locks the recipe against a concurrent delete; another family's id is not found.
		if err := tx.QueryRow(ctx, `SELECT title FROM recipes WHERE id = $1 AND household_id = $2 FOR SHARE`, *e.recipeID, hid).Scan(&title); err != nil {
			return item, err
		}
	}
	if e.text != nil {
		title = *e.text
	}
	notes := e.notes.ptr()
	if !e.notes.given() && exists && sameRecipe(curRecipe, e.recipeID) && curTitle == title {
		notes = curNotes
	}

	switch {
	case !exists:
		if _, err := tx.Exec(ctx, `
INSERT INTO meal_plans (household_id, date, slot, recipe_id, title, notes) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (household_id, date, slot) DO UPDATE SET recipe_id = EXCLUDED.recipe_id, title = EXCLUDED.title, notes = EXCLUDED.notes`,
			hid, e.date, e.slot, e.recipeID, title, notes); err != nil {
			return item, err
		}
		item.Action = "planned"
	case sameRecipe(curRecipe, e.recipeID) && curTitle == title && samePtr(curNotes, notes):
		item.Action = "unchanged"
	default:
		if _, err := tx.Exec(ctx, `UPDATE meal_plans SET recipe_id = $2, title = $3, notes = $4 WHERE id = $1`,
			curID, e.recipeID, title, notes); err != nil {
			return item, err
		}
		if sameRecipe(curRecipe, e.recipeID) && curTitle == title {
			item.Action = "updated"
		} else {
			item.Action, item.Previous = "replaced", &curTitle
		}
	}
	return item, nil
}

func sameRecipe(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func setSummary(res mealSetResult) string {
	counts := map[string]int{}
	var lines, warnings []string
	for _, it := range res.Meals {
		counts[it.Action]++
		wd := it.Weekday[:3]
		switch it.Action {
		case "cleared":
			lines = append(lines, fmt.Sprintf("%s %s %s: cleared (was %s)", wd, it.LocalDate, it.Slot, *it.Previous))
		case "already_empty":
			lines = append(lines, fmt.Sprintf("%s %s %s: already empty", wd, it.LocalDate, it.Slot))
		default:
			if it.Meal == nil {
				continue
			}
			l := fmt.Sprintf("%s %s %s: %s", wd, it.LocalDate, it.Slot, it.Meal.Title)
			if it.Previous != nil {
				l += fmt.Sprintf(" (was %s)", *it.Previous)
			}
			lines = append(lines, l)
			for _, w := range it.Meal.AllergyWarnings {
				warnings = append(warnings, fmt.Sprintf("%s %s %s: %s", wd, it.Slot, it.Meal.Title, w))
			}
		}
	}
	var parts []string
	for _, a := range []string{"planned", "replaced", "updated", "unchanged", "cleared", "already_empty"} {
		if n := counts[a]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, strings.ReplaceAll(a, "_", " ")))
		}
	}
	s := fmt.Sprintf("Meal plan: %s. %s.", strings.Join(parts, ", "), strings.Join(lines, "; "))
	if len(warnings) > 0 {
		s += " Allergy warning: " + strings.Join(warnings, "; ") + "."
	}
	for _, it := range res.Meals {
		if len(it.SimilarRecipes) > 0 {
			s += fmt.Sprintf(" %s on %s is free text; saved recipes with a similar name: %s.", it.Meal.Title, it.Weekday, strings.Join(it.SimilarRecipes, ", "))
		}
	}
	for _, n := range res.Notes {
		s += " Note: " + n
	}
	return s
}

func setHints(res mealSetResult) []string {
	first, last := res.Meals[0].LocalDate, res.Meals[len(res.Meals)-1].LocalDate
	out := []string{fmt.Sprintf(`Read the plan back with agenda_get from=%s to=%s areas=["meals"].`, first, last)}
	for _, it := range res.Meals {
		if len(it.SimilarRecipes) > 0 {
			out = append(out, fmt.Sprintf(`Link a saved recipe with meal_set meals=[{"date":"%s","slot":"%s","recipe_id":"…"}].`, it.LocalDate, it.Slot))
			break
		}
	}
	for _, it := range res.Meals {
		if it.Meal != nil && it.Meal.RecipeID != nil {
			out = append(out, fmt.Sprintf("See the recipe with recipe_get recipe_id=%s.", *it.Meal.RecipeID))
			break
		}
	}
	out = append(out, "After the meal, record how each person liked it with meal_rate date=… slot=… person_id=… rating=1-5.")
	return out
}

// ---------------------------------------------------------------------------
// meal_rate
// ---------------------------------------------------------------------------

var rateAliases = map[string]string{
	"day": "date", "meal_type": "slot", "meal": "slot",
	"person": "person_id", "who": "person_id", "name": "person_id", "rated_by": "person_id",
	"stars": "rating", "score": "rating", "note": "comment", "notes": "comment", "feedback": "comment",
}

var rateExample = map[string]any{"date": "yesterday", "slot": "dinner", "person_id": "Nora", "rating": 4, "comment": "Loved the salsa"}

// parseRating reads 1-5: 4, "4", "4/5", "4 stars", "4 out of 5".
func parseRating(v any) (*int, error) {
	bad := badArg("rating", "rating must be a whole number from 1 to 5.", "Pass rating as 1 (didn't like it) to 5 (loved it).")
	var f float64
	switch x := v.(type) {
	case nil:
		return nil, nil
	case float64:
		f = x
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		if s == "" {
			return nil, nil
		}
		for _, suf := range []string{"out of 5", "/5", "stars", "star"} {
			s = strings.TrimSpace(strings.TrimSuffix(s, suf))
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, bad
		}
		f = n
	default:
		return nil, bad
	}
	if f != math.Trunc(f) || f < 1 || f > 5 {
		return nil, bad
	}
	n := int(f)
	return &n, nil
}

// resolvePerson finds a person of the family: a people.id, the person's
// login (users.id), or the exact display name, any case.
func resolvePerson(ctx context.Context, q querier, hid uuid.UUID, ref string) (uuid.UUID, string, *mcp.CallToolResult, error) {
	var rows pgx.Rows
	var err error
	if id, perr := uuid.Parse(ref); perr == nil {
		rows, err = q.Query(ctx, `SELECT id, display_name FROM people WHERE household_id = $1 AND (id = $2 OR user_id = $2)
		                           ORDER BY (id = $2) DESC LIMIT 1`, hid, id)
	} else {
		rows, err = q.Query(ctx, `SELECT id, display_name FROM people WHERE household_id = $1 AND lower(btrim(display_name)) = lower(btrim($2))
		                           ORDER BY display_name, id`, hid, ref)
	}
	if err != nil {
		return uuid.Nil, "", nil, err
	}
	type person struct {
		id   uuid.UUID
		name string
	}
	found, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (person, error) {
		var p person
		return p, r.Scan(&p.id, &p.name)
	})
	if err != nil {
		return uuid.Nil, "", nil, err
	}
	switch len(found) {
	case 1:
		return found[0].id, found[0].name, nil, nil
	case 0:
		var names []string
		if err := q.QueryRow(ctx, `SELECT coalesce(array_agg(display_name ORDER BY lower(display_name)), '{}') FROM people WHERE household_id = $1`, hid).Scan(&names); err != nil {
			return uuid.Nil, "", nil, err
		}
		return uuid.Nil, "", respond.Invalid("person_id", fmt.Sprintf("No person %q in this family (people: %s).", ref, strings.Join(names, ", ")),
			"Pass the person's exact name or person_id from person_find.", rateExample), nil
	}
	var labels []string
	for _, p := range found {
		labels = append(labels, fmt.Sprintf("%s (%s)", p.name, p.id))
	}
	return uuid.Nil, "", respond.Invalid("person_id", fmt.Sprintf("%d people are named %q: %s.", len(found), ref, strings.Join(labels, ", ")),
		"Pass the person_id of the one you mean.", rateExample), nil
}

// NewMealRateTool creates meal_rate (catalog v2, class U).
func NewMealRateTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("meal_rate", append([]mcp.ToolOption{
		mcp.WithDescription("Record how a person liked a meal (1-5) so suggestions improve. One rating per person per meal; rating again replaces it."),
		mcp.WithString("date", mcp.Required(), mcp.Description("Day eaten: YYYY-MM-DD, or yesterday, tuesday (family timezone)")),
		mcp.WithString("slot", mcp.Required(), mcp.Enum(slots...), mcp.Description("Which meal; default the day's only meal, else dinner")),
		mcp.WithString("person_id", mcp.Required(), mcp.Description("Who: person_id or exact name; default you")),
		mcp.WithNumber("rating", mcp.Required(), mcp.Min(1), mcp.Max(5), mcp.Description("1 (didn't like it) to 5 (loved it)")),
		mcp.WithString("comment", mcp.Description("What they said, e.g. too spicy")),
		respond.Schema[mealRateResult](),
	}, annotations("Rate a meal", false, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotPlan(c.role, "meal_rate"), nil
		}
		loc := familyLocation(c.tz)
		today := familyToday(c.tz)
		args := withAliases(req.GetArguments(), rateAliases)

		dateRaw, err := optionalString(args, "date")
		if err != nil || dateRaw == nil {
			return respond.Invalid("date", "date is required: the day the meal was eaten.",
				fmt.Sprintf("Pass date as YYYY-MM-DD or a word like yesterday (family timezone %s).", loc), rateExample), nil
		}
		date, ok := resolveDay(*dateRaw, today, loc, false)
		if !ok {
			return respond.Invalid("date", fmt.Sprintf("date %q is not a date.", *dateRaw),
				fmt.Sprintf("Pass date as YYYY-MM-DD or a word like yesterday (family timezone %s).", loc), rateExample), nil
		}
		if days := int(math.Round(date.Sub(today).Hours() / 24)); days > maxPlanDays || days < -maxPlanDays {
			return respond.Invalid("date", fmt.Sprintf("date %s is more than a year from today.", date.Format(time.DateOnly)),
				"Pass the day the meal was eaten.", rateExample), nil
		}
		var slot string
		if s, err := optionalString(args, "slot"); err != nil {
			return invalid(err, rateExample), nil
		} else if s != nil {
			if slot, ok = parseSlot(*s); !ok {
				return respond.Invalid("slot", fmt.Sprintf("slot %q is not a meal slot.", *s), "Pass slot as breakfast, lunch, dinner or snack.", rateExample), nil
			}
		}
		rating, err := parseRating(args["rating"])
		if err != nil {
			return invalid(err, rateExample), nil
		}
		comment, err := readText(args, "comment", maxCommentLen)
		if err != nil {
			return invalid(err, rateExample), nil
		}
		if rating == nil && !comment.set {
			return respond.Invalid("rating", "rating is required (or a comment).", "Pass rating 1-5, e.g. 4.", rateExample), nil
		}

		// Who.
		var personID uuid.UUID
		var person string
		if ref, err := optionalString(args, "person_id"); err != nil {
			return invalid(err, rateExample), nil
		} else if ref != nil {
			id, name, bad, err := resolvePerson(ctx, pool, c.householdID, *ref)
			if err != nil {
				return respond.Failed("save the rating", err), nil
			} else if bad != nil {
				return bad, nil
			}
			personID, person = id, name
		} else {
			err := pool.QueryRow(ctx, `SELECT id, display_name FROM people WHERE household_id = $1 AND user_id = $2`, c.householdID, c.userID).
				Scan(&personID, &person)
			if errors.Is(err, pgx.ErrNoRows) {
				return respond.Invalid("person_id", "person_id is required: your login is not linked to a person in this family.",
					"Pass the person's exact name or person_id from person_find.", rateExample), nil
			}
			if err != nil {
				return respond.Failed("save the rating", err), nil
			}
		}
		if !canRateOthers(c.role) {
			var self bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM people WHERE id = $1 AND user_id = $2)`, personID, c.userID).Scan(&self); err != nil {
				return respond.Failed("save the rating", err), nil
			}
			if !self {
				return respond.Fail(respond.Error{
					Message: fmt.Sprintf("A %s can only rate meals for themselves, not for %s: an owner or family manager rates for others.", c.role, person),
					Fix:     "Leave person_id out to rate as yourself, or ask an owner or family manager; retrying will not help.", Field: "person_id",
				}), nil
			}
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("save the rating", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		// Which meal.
		type planned struct {
			id   uuid.UUID
			slot string
		}
		rows, err := tx.Query(ctx, `SELECT id, slot FROM meal_plans WHERE household_id = $1 AND date = $2
		                             ORDER BY array_position(ARRAY['breakfast','lunch','dinner','snack']::text[], slot::text) FOR SHARE`, c.householdID, date)
		if err != nil {
			return respond.Failed("save the rating", err), nil
		}
		day, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (planned, error) {
			var p planned
			return p, r.Scan(&p.id, &p.slot)
		})
		if err != nil {
			return respond.Failed("save the rating", err), nil
		}
		if slot == "" {
			slot = "dinner"
			if len(day) == 1 {
				slot = day[0].slot
			}
		}
		var mealID uuid.UUID
		var daySlots []string
		for _, p := range day {
			daySlots = append(daySlots, p.slot)
			if p.slot == slot {
				mealID = p.id
			}
		}
		if mealID == uuid.Nil {
			msg := fmt.Sprintf("Nothing is planned for %s on %s.", slot, describeDay(date))
			if len(daySlots) > 0 {
				msg += " Planned that day: " + strings.Join(daySlots, ", ") + "."
			}
			return respond.Fail(respond.Error{
				Message: msg,
				Fix: fmt.Sprintf(`Record what was eaten with meal_set meals=[{"date":"%s","slot":"%s","text":"…"}], then call meal_rate again.`,
					date.Format(time.DateOnly), slot),
				Field: "date",
			}), nil
		}

		res := mealRateResult{Person: person, PersonID: personID.String()}
		var prevRating *int16
		var prevComment *string
		err = tx.QueryRow(ctx, `SELECT rating, comment FROM meal_feedback WHERE meal_plan_id = $1 AND person_id = $2 FOR UPDATE`, mealID, personID).
			Scan(&prevRating, &prevComment)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return respond.Failed("save the rating", err), nil
		}
		newRating, newComment := rating, comment.ptr()
		if exists {
			if prevRating != nil {
				v := int(*prevRating)
				res.PreviousRating = &v
			}
			if rating == nil {
				newRating = res.PreviousRating
			}
			if !comment.given() {
				newComment = prevComment
			}
		}
		if newRating == nil && (newComment == nil || strings.TrimSpace(*newComment) == "") {
			return respond.Invalid("rating", "rating is required when the comment is cleared.", "Pass rating 1-5, e.g. 4.", rateExample), nil
		}
		switch {
		case exists && sameInt(res.PreviousRating, newRating) && samePtr(prevComment, newComment):
			res.Action = "unchanged"
		default:
			if _, err := tx.Exec(ctx, `
INSERT INTO meal_feedback (meal_plan_id, household_id, person_id, rating, comment) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (meal_plan_id, person_id) DO UPDATE SET rating = EXCLUDED.rating, comment = EXCLUDED.comment`,
				mealID, c.householdID, personID, newRating, newComment); err != nil {
				return respond.Failed("save the rating", err), nil
			}
			res.Action = "saved"
			if exists {
				res.Action = "updated"
			}
		}
		res.Rating, res.Comment = newRating, newComment
		if res.Action == "saved" {
			res.PreviousRating = nil
		}

		plan, err := loadPlan(ctx, tx, c.householdID, date.Format(time.DateOnly), date.Format(time.DateOnly))
		if err != nil {
			return respond.Failed("save the rating", err), nil
		}
		for _, m := range plan {
			if m.MealID == mealID.String() {
				res.Meal = m
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("save the rating", err), nil
		}
		return respond.OK(rateSummary(res), res, rateHints(res)...), nil
	}
	return "meal_rate", tool, handler
}

func rateSummary(r mealRateResult) string {
	what := fmt.Sprintf("%s (%s %s %s)", r.Meal.Title, r.Meal.Weekday, r.Meal.LocalDate, r.Meal.Slot)
	var got string
	if r.Rating != nil {
		got = fmt.Sprintf("%d/5", *r.Rating)
	}
	if r.Comment != nil {
		got = strings.TrimSpace(got + fmt.Sprintf(" %q", *r.Comment))
	}
	var s string
	switch r.Action {
	case "unchanged":
		s = fmt.Sprintf("%s's rating of %s was already %s.", r.Person, what, got)
	case "updated":
		s = fmt.Sprintf("Updated %s's rating of %s: %s", r.Person, what, got)
		if r.PreviousRating != nil && !sameInt(r.PreviousRating, r.Rating) {
			s += fmt.Sprintf(" (was %d/5)", *r.PreviousRating)
		}
		s += "."
	default:
		s = fmt.Sprintf("Saved %s's rating of %s: %s.", r.Person, what, got)
	}
	var sum, n int
	for _, x := range r.Meal.Ratings {
		if x.Rating != nil {
			sum += *x.Rating
			n++
		}
	}
	if n > 1 {
		s += fmt.Sprintf(" This meal now averages %.1f from %d ratings.", float64(sum)/float64(n), n)
	}
	return s
}

func rateHints(r mealRateResult) []string {
	out := []string{fmt.Sprintf("Rate for someone else with meal_rate date=%s slot=%s person_id=… rating=1-5.", r.Meal.LocalDate, r.Meal.Slot)}
	if r.Meal.RecipeID != nil {
		out = append(out, fmt.Sprintf("See the recipe's ratings with recipe_get recipe_id=%s.", *r.Meal.RecipeID))
	}
	return out
}
