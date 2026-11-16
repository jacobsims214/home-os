// Package meals provides the MCP recipe tools (catalog v2, KB
// architecture/mcp-tool-catalog.md "recipe", ticket #4476): recipe_find (R),
// recipe_get (R), recipe_create (C), recipe_update (U) and recipe_delete (X),
// over migration 047's recipes table. The meal plan tools (plan.go: meal_set,
// meal_rate, #4487) share this package; meal_suggest (#4496) and the grocery
// list (#4500) join later and reuse its ingredient and allergy helpers.
//
// Catalog words map to 047 columns: name -> title, notes -> description,
// ingredients[].item -> ingredients[].name, steps[] -> steps (one step per
// line). total_minutes is prep_minutes + cook_minutes; prep_minutes and
// cook_minutes are accepted too.
//
// Ingredients are stored structured ({name, quantity, unit, section}) so the
// grocery list (#4500) can merge and scale them: quantities like "1 1/2" or
// "½" become numbers, units are normalised (tablespoons -> tbsp), and a plain
// text line such as "2 cups flour" is split when a model sends one. Every
// recipe result carries allergy_warnings: an ingredient that matches a family
// member's allergy (people.allergies, 041), with common allergen groups
// expanded (dairy -> milk, butter, cheese ...). Allergies are safety data, so
// matching errs towards warning.
//
// Roles follow the API's policy (apps/api/internal/auth/roles.go ResMeals,
// #4689): owner, family_manager and family_member read and write; viewer and
// housesitter read; vendor and unknown roles get nothing. The role is re-read
// from memberships on every call.
package meals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// Limits. Column sizes from 047; list sizes keep one result readable.
const (
	defaultLimit    = 25
	maxLimit        = 100
	maxNameLen      = 255
	maxNotesLen     = 10000
	maxItemLen      = 200
	maxUnitLen      = 30
	maxSectionLen   = 100
	maxIngredients  = 200
	maxSteps        = 100
	maxStepLen      = 2000
	maxTags         = 30
	maxTagLen       = 50
	maxURLLen       = 2000
	maxServings     = 1000
	maxMinutes      = 10080 // a week
	duplicateWindow = "10 minutes"
	feedbackShown   = 10
)

// querier is what both a pool and a transaction offer.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// ---------------------------------------------------------------------------
// Output types (ids are strings: a uuid.UUID is advertised as a byte array)
// ---------------------------------------------------------------------------

// Ingredient is one recipe ingredient in catalog words.
type Ingredient struct {
	Item     string  `json:"item" jsonschema_description:"What it is, e.g. flour."`
	Quantity any     `json:"quantity" jsonschema_description:"How much: a number (1.5), text (a pinch) or null."`
	Unit     *string `json:"unit" jsonschema_description:"e.g. cup, tbsp, g; null for a count."`
	Section  *string `json:"section" jsonschema_description:"Part of the recipe, e.g. Sauce; null when none."`
	Note     *string `json:"note,omitempty" jsonschema_description:"Preparation, e.g. chopped."`
}

// storedIngredient is one element of recipes.ingredients (047 shape).
type storedIngredient struct {
	Name     string  `json:"name"`
	Quantity any     `json:"quantity"`
	Unit     *string `json:"unit"`
	Section  *string `json:"section"`
	Note     *string `json:"note,omitempty"`
}

type ratingData struct {
	Average float64 `json:"average" jsonschema_description:"Average of the family's 1-5 ratings."`
	Count   int     `json:"count" jsonschema_description:"How many ratings."`
}

type dayData struct {
	LocalDate string `json:"local_date" jsonschema_description:"YYYY-MM-DD in the family timezone."`
	Weekday   string `json:"weekday" jsonschema_description:"e.g. Thu."`
}

type feedbackData struct {
	LocalDate string  `json:"local_date"`
	Weekday   string  `json:"weekday"`
	Slot      string  `json:"slot" jsonschema_description:"breakfast, lunch, dinner or snack."`
	Person    *string `json:"person" jsonschema_description:"Who rated it; null when not known."`
	PersonID  *string `json:"person_id"`
	Rating    *int    `json:"rating" jsonschema_description:"1-5; null for a comment only."`
	Comment   *string `json:"comment"`
}

// AllergyWarning is an ingredient that matches a family member's allergy.
type AllergyWarning struct {
	Person     string `json:"person"`
	PersonID   string `json:"person_id"`
	Allergy    string `json:"allergy" jsonschema_description:"The allergy as saved on the person, e.g. tree nuts."`
	Ingredient string `json:"ingredient" jsonschema_description:"The ingredient that matched, e.g. walnuts."`
}

// recipeData is one full recipe (recipe_get and the write tools).
type recipeData struct {
	ID              string           `json:"id" jsonschema_description:"The recipe_id; also about_id with about_type=recipe."`
	Name            string           `json:"name"`
	Notes           *string          `json:"notes"`
	Servings        *int             `json:"servings"`
	PrepMinutes     *int             `json:"prep_minutes"`
	CookMinutes     *int             `json:"cook_minutes"`
	TotalMinutes    *int             `json:"total_minutes" jsonschema_description:"prep + cook minutes; null when neither is known."`
	Ingredients     []Ingredient     `json:"ingredients"`
	Steps           []string         `json:"steps"`
	Tags            []string         `json:"tags"`
	SourceURL       *string          `json:"source_url"`
	Rating          *ratingData      `json:"rating" jsonschema_description:"Family ratings of meals made from it; null when never rated."`
	TimesMade       int              `json:"times_made" jsonschema_description:"Planned meals with this recipe up to today."`
	LastMade        *dayData         `json:"last_made"`
	NextPlanned     *dayData         `json:"next_planned"`
	Feedback        []feedbackData   `json:"feedback,omitempty" jsonschema_description:"recipe_get: the newest ratings and comments."`
	AllergyWarnings []AllergyWarning `json:"allergy_warnings" jsonschema_description:"Ingredients matching someone's allergy. Always tell the user."`
	Checks          []string         `json:"checks,omitempty" jsonschema_description:"Allergies that could not be checked; tell the user."`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`

	Duplicate        bool     `json:"duplicate,omitempty" jsonschema_description:"recipe_create: the same recipe was saved minutes ago and is returned instead."`
	Changed          []string `json:"changed,omitempty" jsonschema_description:"recipe_update: the fields that changed."`
	PlannedMealsKept *int     `json:"planned_meals_kept,omitempty" jsonschema_description:"recipe_delete: planned meals that keep the name as text."`
	DocumentsMoved   *int     `json:"documents_moved,omitempty" jsonschema_description:"recipe_delete: documents about it, now family-level."`
	NotesDeleted     *int     `json:"notes_deleted,omitempty" jsonschema_description:"recipe_delete: notes about it, deleted."`
}

// recipeItem is one recipe in recipe_find's list.
type recipeItem struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Tags            []string    `json:"tags"`
	TotalMinutes    *int        `json:"total_minutes"`
	Servings        *int        `json:"servings"`
	IngredientCount int         `json:"ingredient_count"`
	Rating          *ratingData `json:"rating"`
	TimesMade       int         `json:"times_made"`
	LastMade        *dayData    `json:"last_made"`
	Matched         []string    `json:"matched,omitempty" jsonschema_description:"Ingredients that matched the ingredient filter."`
	AllergyWarnings []string    `json:"allergy_warnings,omitempty" jsonschema_description:"e.g. Nora (peanut): peanut butter. Tell the user."`
}

// ---------------------------------------------------------------------------
// Access
// ---------------------------------------------------------------------------

type caller struct {
	householdID uuid.UUID
	userID      uuid.UUID // uuid.Nil for local-dev claims without a user
	role        string
	tz          string
}

// errNotSignedIn is callerInfo's "no household membership" result.
var errNotSignedIn = errors.New("not signed in to a household")

// callerInfo re-reads the caller's role from memberships (a demoted member
// loses access at once, a removed one gets nothing) and the family timezone.
// It returns errNotSignedIn when the caller has no household.
func callerInfo(ctx context.Context, pool *pgxpool.Pool) (caller, error) {
	c := server.ClaimsFromContext(ctx)
	if c == nil {
		return caller{}, errNotSignedIn
	}
	hid, err := uuid.Parse(c.HouseholdID)
	if err != nil {
		return caller{}, errNotSignedIn
	}
	out := caller{householdID: hid, role: c.Role}
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.AgentTokenID != "" {
			return caller{}, errNotSignedIn
		}
		// Local-dev claims without a user: keep the claims' role.
		err = pool.QueryRow(ctx, `SELECT timezone FROM households WHERE id = $1`, hid).Scan(&out.tz)
	} else {
		out.userID = uid
		err = pool.QueryRow(ctx,
			`SELECT m.role::text, h.timezone FROM memberships m JOIN households h ON h.id = m.household_id
			  WHERE m.household_id = $1 AND m.user_id = $2`, hid, uid).Scan(&out.role, &out.tz)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return caller{}, errNotSignedIn
	}
	return out, err
}

// callerOf is callerInfo for a tool handler: a non-nil result is the isError
// to return.
func callerOf(ctx context.Context, pool *pgxpool.Pool) (caller, *mcp.CallToolResult) {
	c, err := callerInfo(ctx, pool)
	if errors.Is(err, errNotSignedIn) {
		return caller{}, notSignedIn()
	}
	if err != nil {
		return caller{}, respond.Failed("check your access", err)
	}
	return c, nil
}

// canRead: API ResMeals read (rolesSitter). Unknown roles fail closed.
func canRead(role string) bool {
	switch role {
	case "owner", "family_manager", "family_member", "viewer", "housesitter":
		return true
	}
	return false
}

// canWrite: API ResMeals write (rolesFamily).
func canWrite(role string) bool {
	return role == "owner" || role == "family_manager" || role == "family_member"
}

func roleLabel(role string) string {
	if role == "" {
		return "current"
	}
	return role
}

func cannotRead(role string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't see the family's recipes.", roleLabel(role)),
		Fix:     "Tell the user recipes are not shared with this role; retrying will not help.",
	})
}

func cannotWrite(role, tool string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can't change recipes (tool %s): only an owner, family manager or family member can.", roleLabel(role), tool),
		Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
	})
}

func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}

// familyToday is today's date in the family timezone (UTC when unknown).
func familyToday(tz string) time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	y, m, d := time.Now().In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func annotations(title string, readOnly, destructive, idempotent bool) []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithTitleAnnotation(title),
		mcp.WithReadOnlyHintAnnotation(readOnly),
		mcp.WithDestructiveHintAnnotation(destructive),
		mcp.WithIdempotentHintAnnotation(idempotent),
		mcp.WithOpenWorldHintAnnotation(false),
	}
}

// ---------------------------------------------------------------------------
// Argument helpers
// ---------------------------------------------------------------------------

type argError struct{ field, message, fix string }

func (e *argError) Error() string { return e.message }

func badArg(field, message, fix string) error { return &argError{field, message, fix} }

func invalid(err error, example map[string]any) *mcp.CallToolResult {
	var ae *argError
	if errors.As(err, &ae) {
		return respond.Invalid(ae.field, ae.message, ae.fix, example)
	}
	return respond.Invalid("", err.Error(), "Correct the arguments and call again.", example)
}

func given(args map[string]any, key string) bool {
	v, ok := args[key]
	return ok && v != nil
}

// withAliases moves other names small models use to the catalog name (a
// catalog name that is present wins).
func withAliases(args map[string]any, aliases map[string]string) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	for old, name := range aliases {
		if v, ok := out[old]; ok {
			if !given(out, name) {
				out[name] = v
			}
			delete(out, old)
		}
	}
	return out
}

func optionalString(args map[string]any, key string) (*string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, nil
	}
	s, isStr := v.(string)
	if !isStr {
		if f, isNum := v.(float64); isNum && key == "recipe_id" {
			s = strconv.FormatFloat(f, 'f', -1, 64)
		} else {
			return nil, badArg(key, key+" must be text.", fmt.Sprintf("Pass %s as text in quotes.", key))
		}
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	return &s, nil
}

// textField is an optional text param: absent or "" is not given, JSON null
// asks to clear.
type textField struct {
	set, clear bool
	val        string
}

func (f textField) given() bool { return f.set || f.clear }

func (f textField) ptr() *string {
	if !f.set {
		return nil
	}
	v := f.val
	return &v
}

func readText(args map[string]any, key string, maxLen int) (textField, error) {
	v, ok := args[key]
	if !ok {
		return textField{}, nil
	}
	if v == nil {
		return textField{clear: true}, nil
	}
	s, isStr := v.(string)
	if !isStr {
		return textField{}, badArg(key, key+" must be text.", fmt.Sprintf("Pass %s as text in quotes.", key))
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return textField{}, nil
	}
	if utf8.RuneCountInString(s) > maxLen {
		return textField{}, badArg(key, fmt.Sprintf("%s must be at most %d characters.", key, maxLen), fmt.Sprintf("Pass a shorter %s.", key))
	}
	return textField{set: true, val: s}, nil
}

// numField is an optional whole-number param.
type numField struct {
	set, clear bool
	val        int
}

func (f numField) given() bool { return f.set || f.clear }

func (f numField) ptr() *int {
	if !f.set {
		return nil
	}
	v := f.val
	return &v
}

// readNum reads a whole number in [lo, hi]. Quoted numbers and "45 minutes"
// are accepted, because small models send them.
func readNum(args map[string]any, key string, lo, hi int) (numField, error) {
	v, ok := args[key]
	if !ok {
		return numField{}, nil
	}
	if v == nil {
		return numField{clear: true}, nil
	}
	bad := badArg(key, fmt.Sprintf("%s must be a whole number from %d to %d.", key, lo, hi),
		fmt.Sprintf("Pass %s as a whole number, e.g. %d.", key, max(lo, min(hi, 30))))
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case int:
		n = float64(x)
	case int64:
		n = float64(x)
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		if s == "" {
			return numField{}, nil
		}
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(s, "minutes"), "mins"), "min"))
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return numField{}, bad
		}
		n = f
	default:
		return numField{}, bad
	}
	if math.IsNaN(n) || n != math.Trunc(n) || n < float64(lo) || n > float64(hi) {
		return numField{}, bad
	}
	return numField{set: true, val: int(n)}, nil
}

// readStrings reads a list of strings: a JSON array, or one comma-separated
// string. ok is false when absent; null gives clear.
func readStrings(args map[string]any, key string) (vals []string, present, clear bool, err error) {
	v, ok := args[key]
	if !ok {
		return nil, false, false, nil
	}
	if v == nil {
		return nil, true, true, nil
	}
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return nil, false, false, nil // small models send "" for unused params
		}
		for _, p := range strings.Split(x, ",") {
			if p = strings.TrimSpace(p); p != "" {
				vals = append(vals, p)
			}
		}
	case []any:
		for _, e := range x {
			s, isStr := e.(string)
			if !isStr {
				return nil, true, false, badArg(key, key+" must be a list of text.", fmt.Sprintf(`Pass %s as a list, e.g. ["dinner","quick"].`, key))
			}
			if s = strings.TrimSpace(s); s != "" {
				vals = append(vals, s)
			}
		}
	case []string:
		for _, s := range x {
			if s = strings.TrimSpace(s); s != "" {
				vals = append(vals, s)
			}
		}
	default:
		return nil, true, false, badArg(key, key+" must be a list of text.", fmt.Sprintf(`Pass %s as a list, e.g. ["dinner","quick"].`, key))
	}
	return vals, true, false, nil
}

// normTags trims, lower-cases and de-duplicates tags (as the API does).
func normTags(in []string) ([]string, error) {
	out := []string{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#")))
		if t == "" || slices.Contains(out, t) {
			continue
		}
		if utf8.RuneCountInString(t) > maxTagLen {
			return nil, badArg("tags", fmt.Sprintf("Tag %q is longer than %d characters.", t, maxTagLen), "Use short tags, e.g. dinner, vegetarian.")
		}
		out = append(out, t)
	}
	if len(out) > maxTags {
		return nil, badArg("tags", fmt.Sprintf("At most %d tags.", maxTags), "Pass fewer tags.")
	}
	return out, nil
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// ---------------------------------------------------------------------------
// Ingredients
// ---------------------------------------------------------------------------

// unitAliases normalises common units so the grocery list can merge them.
var unitAliases = map[string]string{
	"tablespoon": "tbsp", "tablespoons": "tbsp", "tbsp": "tbsp", "tbsps": "tbsp", "tbs": "tbsp", "tbl": "tbsp", "T": "tbsp",
	"teaspoon": "tsp", "teaspoons": "tsp", "tsp": "tsp", "tsps": "tsp", "t": "tsp",
	"cup": "cup", "cups": "cup", "c": "cup",
	"ounce": "oz", "ounces": "oz", "oz": "oz",
	"fluid ounce": "fl oz", "fluid ounces": "fl oz", "fl oz": "fl oz",
	"pound": "lb", "pounds": "lb", "lb": "lb", "lbs": "lb",
	"gram": "g", "grams": "g", "g": "g", "gr": "g",
	"kilogram": "kg", "kilograms": "kg", "kg": "kg", "kgs": "kg",
	"milliliter": "ml", "milliliters": "ml", "millilitre": "ml", "millilitres": "ml", "ml": "ml",
	"liter": "l", "liters": "l", "litre": "l", "litres": "l", "l": "l",
	"pint": "pint", "pints": "pint", "quart": "quart", "quarts": "quart", "gallon": "gallon", "gallons": "gallon",
	"clove": "clove", "cloves": "clove", "can": "can", "cans": "can", "jar": "jar", "jars": "jar",
	"package": "package", "packages": "package", "pkg": "package", "packet": "packet", "packets": "packet",
	"slice": "slice", "slices": "slice", "stick": "stick", "sticks": "stick", "bunch": "bunch", "bunches": "bunch",
	"pinch": "pinch", "pinches": "pinch", "dash": "dash", "dashes": "dash", "head": "head", "heads": "head",
	"sprig": "sprig", "sprigs": "sprig", "piece": "piece", "pieces": "piece", "handful": "handful", "handfuls": "handful",
}

// normUnit maps a unit to its short form; unknown units are kept, lower-cased.
func normUnit(u string) string {
	u = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(u), "."))
	if u == "T" { // capital T is tablespoon by convention
		return "tbsp"
	}
	l := strings.ToLower(u)
	if v, ok := unitAliases[l]; ok && l != "t" {
		return v
	}
	if l == "t" {
		return "tsp"
	}
	return l
}

var unicodeFractions = map[rune]float64{
	'½': 0.5, '⅓': 1.0 / 3, '⅔': 2.0 / 3, '¼': 0.25, '¾': 0.75, '⅕': 0.2, '⅖': 0.4, '⅗': 0.6, '⅘': 0.8,
	'⅙': 1.0 / 6, '⅚': 5.0 / 6, '⅛': 0.125, '⅜': 0.375, '⅝': 0.625, '⅞': 0.875,
}

// parseAmount reads "2", "1.5", "1/2", "1 1/2", "½" or "1½" as a number
// (rounded to 3 places); ok is false for anything else ("a pinch", "1-2").
func parseAmount(s string) (float64, bool) {
	s = strings.TrimSpace(s)
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

// lineRe splits "2 cups flour, sifted" into amount, unit and item.
var (
	amountPart = `(\d+\s+\d+/\d+|\d+/\d+|\d*[½⅓⅔¼¾⅕⅖⅗⅘⅙⅚⅛⅜⅝⅞]|\d+(?:\.\d+)?)`
	lineRe     = regexp.MustCompile(`^` + amountPart + `\s*(.*)$`)
)

// parseLine turns one pasted ingredient line into an ingredient. Without a
// leading amount the whole line is the item.
func parseLine(line string) storedIngredient {
	line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*•·"))
	ing := storedIngredient{Name: line}
	m := lineRe.FindStringSubmatch(line)
	if m == nil {
		return ing
	}
	q, ok := parseAmount(m[1])
	if !ok {
		return ing
	}
	rest := strings.TrimSpace(m[2])
	if rest == "" {
		return ing // just a number: keep the text
	}
	ing.Quantity = q
	words := strings.Fields(rest)
	// Two-word units first ("fl oz", "fluid ounces").
	for n := 2; n >= 1; n-- {
		if len(words) <= n {
			continue
		}
		cand := strings.TrimSuffix(strings.Join(words[:n], " "), ".")
		if _, known := unitAliases[strings.ToLower(cand)]; known || cand == "T" {
			u := normUnit(cand)
			ing.Unit = &u
			words = words[n:]
			break
		}
	}
	if len(words) > 1 && strings.EqualFold(words[0], "of") {
		words = words[1:]
	}
	item := strings.Join(words, " ")
	if name, note, found := strings.Cut(item, ","); found && strings.TrimSpace(name) != "" {
		item = strings.TrimSpace(name)
		if n := strings.TrimSpace(note); n != "" {
			ing.Note = &n
		}
	}
	ing.Name = item
	return ing
}

// ingredientKeys are the names models use for an ingredient's fields.
var ingredientKeys = map[string][]string{
	"item":     {"item", "name", "ingredient", "food", "text"},
	"quantity": {"quantity", "qty", "amount", "count"},
	"unit":     {"unit", "units", "measure", "uom"},
	"section":  {"section", "group", "part"},
	"note":     {"note", "notes", "prep", "preparation"},
}

func firstKey(m map[string]any, field string) (any, bool) {
	for _, k := range ingredientKeys[field] {
		if v, ok := m[k]; ok && v != nil {
			return v, true
		}
	}
	return nil, false
}

// parseIngredients validates ingredients[]: objects {item, quantity?, unit?,
// section?} or plain lines ("2 cups flour"); one string is split by lines.
func parseIngredients(v any) ([]storedIngredient, error) {
	bad := func(msg string) error {
		return badArg("ingredients", msg,
			`Pass ingredients as a list, e.g. [{"item":"flour","quantity":2,"unit":"cup"},{"item":"eggs","quantity":3}].`)
	}
	var raw []any
	switch x := v.(type) {
	case nil:
		return nil, bad("ingredients can't be empty.")
	case string:
		for _, l := range strings.Split(x, "\n") {
			if strings.TrimSpace(l) != "" {
				raw = append(raw, l)
			}
		}
	case []any:
		raw = x
	default:
		return nil, bad("ingredients must be a list.")
	}
	out := make([]storedIngredient, 0, len(raw))
	for i, e := range raw {
		var ing storedIngredient
		switch x := e.(type) {
		case string:
			if strings.TrimSpace(x) == "" {
				continue
			}
			ing = parseLine(x)
		case map[string]any:
			iv, _ := firstKey(x, "item")
			name, _ := iv.(string)
			name = strings.TrimSpace(name)
			if name == "" {
				return nil, bad(fmt.Sprintf("Ingredient %d has no item.", i+1))
			}
			ing.Name = name
			if q, ok := firstKey(x, "quantity"); ok {
				switch qv := q.(type) {
				case int:
					q = float64(qv)
				case int64:
					q = float64(qv)
				}
				switch qv := q.(type) {
				case float64:
					if qv < 0 || math.IsNaN(qv) || math.IsInf(qv, 0) {
						return nil, bad(fmt.Sprintf("Ingredient %q has a negative quantity.", name))
					}
					ing.Quantity = round3(qv)
				case string:
					qs := strings.TrimSpace(qv)
					if n, ok := parseAmount(qs); ok {
						ing.Quantity = n
					} else if qs != "" {
						if utf8.RuneCountInString(qs) > maxUnitLen {
							return nil, bad(fmt.Sprintf("Ingredient %q has a quantity longer than %d characters.", name, maxUnitLen))
						}
						ing.Quantity = qs
					}
				default:
					return nil, bad(fmt.Sprintf("Ingredient %q: quantity must be a number or text.", name))
				}
			}
			for _, f := range []struct {
				field string
				max   int
				dst   **string
				unit  bool
			}{{"unit", maxUnitLen, &ing.Unit, true}, {"section", maxSectionLen, &ing.Section, false}, {"note", maxItemLen, &ing.Note, false}} {
				val, ok := firstKey(x, f.field)
				if !ok {
					continue
				}
				s, isStr := val.(string)
				if !isStr {
					return nil, bad(fmt.Sprintf("Ingredient %q: %s must be text.", name, f.field))
				}
				if s = strings.TrimSpace(s); s == "" {
					continue
				}
				if f.unit {
					s = normUnit(s)
				}
				if utf8.RuneCountInString(s) > f.max {
					return nil, bad(fmt.Sprintf("Ingredient %q: %s is longer than %d characters.", name, f.field, f.max))
				}
				*f.dst = &s
			}
			// "2 cups flour" sent as the item with no quantity.
			if ing.Quantity == nil && ing.Unit == nil {
				if p := parseLine(name); p.Quantity != nil {
					p.Section, p.Note = ing.Section, coalesce(ing.Note, p.Note)
					ing = p
				}
			}
		default:
			return nil, bad(fmt.Sprintf("Ingredient %d must be an object like {\"item\":\"flour\"}.", i+1))
		}
		if strings.TrimSpace(ing.Name) == "" {
			return nil, bad(fmt.Sprintf("Ingredient %d has no item.", i+1))
		}
		if utf8.RuneCountInString(ing.Name) > maxItemLen {
			return nil, bad(fmt.Sprintf("Ingredient %d is longer than %d characters.", i+1, maxItemLen))
		}
		out = append(out, ing)
	}
	if len(out) == 0 {
		return nil, bad("ingredients can't be empty.")
	}
	if len(out) > maxIngredients {
		return nil, bad(fmt.Sprintf("At most %d ingredients.", maxIngredients))
	}
	return out, nil
}

func coalesce(a, b *string) *string {
	if a != nil {
		return a
	}
	return b
}

func toCatalog(in []storedIngredient) []Ingredient {
	out := make([]Ingredient, len(in))
	for i, s := range in {
		out[i] = Ingredient{Item: s.Name, Quantity: s.Quantity, Unit: s.Unit, Section: s.Section, Note: s.Note}
	}
	return out
}

// ---------------------------------------------------------------------------
// Steps
// ---------------------------------------------------------------------------

var stepNumber = regexp.MustCompile(`(?i)^\s*(?:step\s*)?\d+\s*[.):-]\s*`)

// parseSteps reads steps[]: a list of strings, or one pasted string split
// by lines. Leading numbers ("1.", "Step 2:") are dropped: the order is the
// number.
func parseSteps(v any) ([]string, error) {
	bad := func(msg string) error {
		return badArg("steps", msg, `Pass steps as a list, e.g. ["Preheat the oven to 375F.","Mix the flour and eggs."].`)
	}
	var raw []string
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		raw = strings.Split(x, "\n")
	case []any:
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, bad("Each step must be text.")
			}
			raw = append(raw, strings.Split(s, "\n")...)
		}
	default:
		return nil, bad("steps must be a list of text.")
	}
	out := []string{}
	for _, s := range raw {
		s = strings.TrimSpace(stepNumber.ReplaceAllString(strings.TrimSpace(s), ""))
		if s == "" {
			continue
		}
		if utf8.RuneCountInString(s) > maxStepLen {
			return nil, bad(fmt.Sprintf("A step is longer than %d characters.", maxStepLen))
		}
		out = append(out, s)
	}
	if len(out) > maxSteps {
		return nil, bad(fmt.Sprintf("At most %d steps.", maxSteps))
	}
	return out, nil
}

func splitSteps(s *string) []string {
	out := []string{}
	if s == nil {
		return out
	}
	for _, l := range strings.Split(*s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func joinSteps(steps []string) *string {
	if len(steps) == 0 {
		return nil
	}
	s := strings.Join(steps, "\n")
	return &s
}

// ---------------------------------------------------------------------------
// Word matching (ingredient filter, allergies)
// ---------------------------------------------------------------------------

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func words(s string) []string {
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

// wordForm is the regex body matching w in singular or plural form.
func wordForm(w string) string {
	w = singular(w)
	if strings.HasSuffix(w, "y") && len(w) > 2 {
		return regexp.QuoteMeta(w[:len(w)-1]) + `(?:y|ies)`
	}
	return regexp.QuoteMeta(w) + `(?:s|es)?`
}

// phrasePatterns returns the Postgres (\m...\M) and Go (\b...\b) regexes for
// a phrase: its words in order, each singular or plural. ok is false when
// the phrase has no words.
func phrasePatterns(phrase string) (pg, goRe string, ok bool) {
	ws := words(phrase)
	if len(ws) == 0 {
		return "", "", false
	}
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = wordForm(w)
	}
	body := strings.Join(parts, `[^\w]+`)
	return `\m` + body + `\M`, `(?i)\b` + body + `\b`, true
}

// allergenGroups expands an allergy to the ingredients that usually carry
// it. The allergy's own words always match too.
var allergenGroups = map[string][]string{
	"peanut":    {"peanut", "groundnut", "satay"},
	"nut":       {"almond", "walnut", "pecan", "cashew", "pistachio", "hazelnut", "macadamia", "brazil nut", "pine nut", "nut", "praline", "marzipan", "nutella", "peanut"},
	"tree nut":  {"almond", "walnut", "pecan", "cashew", "pistachio", "hazelnut", "macadamia", "brazil nut", "pine nut", "praline", "marzipan", "nutella"},
	"dairy":     {"milk", "butter", "cheese", "cream", "yogurt", "yoghurt", "ghee", "whey", "casein", "buttermilk", "parmesan", "mozzarella", "cheddar", "ricotta", "feta", "half and half", "custard"},
	"milk":      {"milk", "butter", "cheese", "cream", "yogurt", "yoghurt", "ghee", "whey", "casein", "buttermilk", "parmesan", "mozzarella", "cheddar", "ricotta", "feta", "half and half", "custard"},
	"lactose":   {"milk", "butter", "cheese", "cream", "yogurt", "yoghurt", "buttermilk", "half and half", "custard", "ricotta"},
	"egg":       {"egg", "mayonnaise", "mayo", "meringue", "aioli", "custard"},
	"gluten":    {"wheat", "flour", "bread", "breadcrumb", "panko", "pasta", "spaghetti", "noodle", "couscous", "barley", "rye", "semolina", "farro", "bulgur", "cracker", "tortilla", "soy sauce", "seitan", "malt"},
	"wheat":     {"wheat", "flour", "bread", "breadcrumb", "panko", "pasta", "spaghetti", "noodle", "couscous", "semolina", "farro", "bulgur", "cracker", "tortilla", "soy sauce", "seitan"},
	"celiac":    {"wheat", "flour", "bread", "breadcrumb", "panko", "pasta", "spaghetti", "noodle", "couscous", "barley", "rye", "semolina", "farro", "bulgur", "cracker", "tortilla", "soy sauce", "seitan", "malt"},
	"shellfish": {"shrimp", "prawn", "crab", "lobster", "crayfish", "crawfish", "scallop", "clam", "mussel", "oyster", "shellfish"},
	"fish":      {"fish", "salmon", "tuna", "cod", "tilapia", "halibut", "anchovy", "sardine", "trout", "haddock", "mackerel", "fish sauce", "worcestershire"},
	"soy":       {"soy", "soya", "tofu", "edamame", "tempeh", "miso", "soy sauce"},
	"sesame":    {"sesame", "tahini"},
}

// allergenExceptions are ingredients a group word appears in that don't
// carry the allergen ("peanut butter" is not dairy). They are ignored only
// for the group words, never for the allergy's own words.
var allergenExceptions = map[string][]string{
	"dairy":   dairyFree,
	"milk":    dairyFree,
	"lactose": dairyFree,
	"egg":     {"eggplant"},
	"fish":    {"fish shaped", "goldfish"},
	"nut":     {"nutmeg", "butternut", "coconut", "water chestnut"},
}

var dairyFree = []string{"peanut butter", "almond butter", "cashew butter", "nut butter", "sunflower butter", "seed butter",
	"apple butter", "cocoa butter", "shea butter", "butternut", "coconut milk", "almond milk", "oat milk", "soy milk",
	"rice milk", "cashew milk", "coconut cream", "cream of tartar", "butter bean", "butter lettuce"}

// allergyMatcher matches ingredient names against one person's allergy.
type allergyMatcher struct {
	personID, person, allergy string
	own, group, except        []*regexp.Regexp
}

// ---------------------------------------------------------------------------
// Reading allergy text
// ---------------------------------------------------------------------------
//
// One normaliser, used for people.allergies entries and for preference text
// (#4802). Both are free text a person or a small model typed, so both get
// the same reading: "Shrimp allergy", "allergic to peanuts", "Peanuts -
// anaphylaxis" and "peanuts: EpiPen" all mean an allergen.

var (
	// allergyMention spots allergy talk in a preference.
	allergyMention = regexp.MustCompile(`(?i)\b(allerg\w*|anaphyla\w*|epi-?pens?|intoleran\w*|celiac|coeliac)\b`)

	// allergyFiller is what is left out of "severe peanut allergy" to get the
	// allergen. "celiac" and "lactose" stay: they are allergen group keys.
	allergyFiller = regexp.MustCompile(`(?i)\b(severe(ly)?|mild|serious|life[- ]threatening|anaphylactic|anaphylaxis|food|foods|allergic|allergy|allergies|allergen|allergens|intolerant|intolerance|intolerances|sensitivity|sensitive|reaction|reactions|to|of|a|an|the|has|have|is|no|avoid|avoids|carries|carry|epi-?pens?|must|strictly|strict|very)\b`)

	listSep = regexp.MustCompile(`(?i)\s*(?:,|;|/|&|\band\b|\bor\b|\+)\s*`)

	freeSuffix = regexp.MustCompile(`(?i)[- ]free\b`)

	// noteStart is where an allergy stops being the allergen and becomes a
	// note: parentheses ("peanuts (carries an EpiPen)", #4742), a spaced dash
	// ("peanuts - anaphylaxis", "peanuts – anaphylaxis") or a colon
	// ("peanuts: EpiPen").
	noteStart = regexp.MustCompile(`\([^)]*\)|\s[-–—]\s[^\n]*|:[^\n]*`)
)

// allergensIn reads the allergens out of allergy text: "Severe peanut
// allergy" -> [peanut], "allergic to shrimp and crab" -> [shrimp, crab],
// "nut-free (allergy)" -> [nut]. Empty when nothing is left to match.
func allergensIn(text string) []string {
	text = noteStart.ReplaceAllString(strings.ToLower(text), " ")
	text = strings.ReplaceAll(text, "coeliac", "celiac")
	text = freeSuffix.ReplaceAllString(text, " ")
	text = allergyFiller.ReplaceAllString(text, " ")
	var out []string
	for _, p := range listSep.Split(text, -1) {
		p = strings.Join(strings.Fields(p), " ")
		if len(words(p)) > 0 && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// compilePhrases compiles each phrase's Go regex (phrasePatterns).
func compilePhrases(phrases []string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, p := range phrases {
		if _, goRe, ok := phrasePatterns(p); ok {
			out = append(out, regexp.MustCompile(goRe))
		}
	}
	return out
}

// newAllergyMatcher builds the regexes for one allergen, e.g. "tree nuts".
// allergen is already normalised (allergensIn); it is both the phrase matched
// against ingredient names and the label shown in warnings.
func newAllergyMatcher(personID, person, allergen string) (allergyMatcher, bool) {
	m := allergyMatcher{personID: personID, person: person, allergy: allergen}
	key := strings.Join(func() []string {
		ws := words(allergen)
		for i, w := range ws {
			ws[i] = singular(w)
		}
		return ws
	}(), " ")
	m.own = compilePhrases([]string{allergen})
	m.group = compilePhrases(allergenGroups[key])
	m.except = compilePhrases(allergenExceptions[key])
	return m, len(m.own) > 0
}

// allergyMatchers turns one stored allergy into the matchers it means. The
// entry is read with allergensIn first, so ordinary wording works: "Shrimp
// allergy" matches shrimp and "allergic to peanuts" matches peanuts. One
// entry can mean several allergens ("peanuts and tree nuts"), and an entry
// that normalises to nothing gives no matcher at all, so the caller can say
// it could not be checked instead of silently matching nothing (#4802).
func allergyMatchers(personID, person, allergy string) []allergyMatcher {
	var out []allergyMatcher
	for _, a := range allergensIn(allergy) {
		if m, ok := newAllergyMatcher(personID, person, a); ok {
			out = append(out, m)
		}
	}
	return out
}

// uncheckedAllergyLine says in one line that a saved allergy could not be
// matched against anything. meal_suggest's checks and the recipe and meal
// warnings use it, so the same problem reads the same everywhere.
func uncheckedAllergyLine(person, allergy string) string {
	return fmt.Sprintf("%s has an allergy saved as %q that could not be checked against recipes; confirm the meal is safe.", person, allergy)
}

func (m allergyMatcher) matches(item string) bool {
	for _, re := range m.own {
		if re.MatchString(item) {
			return true
		}
	}
	for _, re := range m.except {
		item = re.ReplaceAllString(item, " ")
	}
	for _, re := range m.group {
		if re.MatchString(item) {
			return true
		}
	}
	return false
}

// familyAllergies loads every person's allergies as matchers. checks holds
// one line per allergy that normalised to nothing: it matches no ingredient,
// so the caller must show it rather than let the warning silently vanish.
func familyAllergies(ctx context.Context, q querier, hid uuid.UUID) (matchers []allergyMatcher, checks []string, err error) {
	rows, err := q.Query(ctx, `SELECT id::text, display_name, allergies FROM people
	                            WHERE household_id = $1 AND cardinality(allergies) > 0 ORDER BY lower(display_name), id`, hid)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		var allergies []string
		if err := rows.Scan(&id, &name, &allergies); err != nil {
			return nil, nil, err
		}
		for _, a := range allergies {
			if ms := allergyMatchers(id, name, a); len(ms) > 0 {
				matchers = append(matchers, ms...)
			} else {
				checks = append(checks, uncheckedAllergyLine(name, a))
			}
		}
	}
	return matchers, checks, rows.Err()
}

// AllergyWarnings lists the ingredients that match someone's allergy.
func AllergyWarnings(matchers []allergyMatcher, items []string) []AllergyWarning {
	out := []AllergyWarning{}
	for _, m := range matchers {
		for _, it := range items {
			if m.matches(it) {
				out = append(out, AllergyWarning{Person: m.person, PersonID: m.personID, Allergy: m.allergy, Ingredient: it})
			}
		}
	}
	return out
}

func warningLines(ws []AllergyWarning) []string {
	var out []string
	for _, w := range ws {
		out = append(out, fmt.Sprintf("%s (%s): %s", w.Person, w.Allergy, w.Ingredient))
	}
	return out
}

func warningSentence(ws []AllergyWarning) string {
	if len(ws) == 0 {
		return ""
	}
	return " Allergy warning: " + strings.Join(warningLines(ws), "; ") + "."
}

// checkSentence says which allergies could not be checked at all, for the
// summaries that have no structured checks field to put them in.
func checkSentence(checks []string) string {
	if len(checks) == 0 {
		return ""
	}
	return " " + strings.Join(checks, " ")
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// statsCols are the rating and history columns for recipe r; $1 is the
// family's today (date).
const statsCols = `
	(SELECT round(avg(f.rating), 1)::float8 FROM meal_feedback f JOIN meal_plans m ON m.id = f.meal_plan_id
	  WHERE m.recipe_id = r.id AND m.household_id = r.household_id AND f.rating IS NOT NULL),
	(SELECT count(f.rating)::int FROM meal_feedback f JOIN meal_plans m ON m.id = f.meal_plan_id
	  WHERE m.recipe_id = r.id AND m.household_id = r.household_id),
	(SELECT count(*)::int FROM meal_plans m WHERE m.recipe_id = r.id AND m.household_id = r.household_id AND m.date <= $1::date),
	(SELECT max(m.date)::text FROM meal_plans m WHERE m.recipe_id = r.id AND m.household_id = r.household_id AND m.date <= $1::date),
	(SELECT min(m.date)::text FROM meal_plans m WHERE m.recipe_id = r.id AND m.household_id = r.household_id AND m.date > $1::date)`

func day(s *string) *dayData {
	if s == nil {
		return nil
	}
	t, err := time.Parse(time.DateOnly, *s)
	if err != nil {
		return nil
	}
	return &dayData{LocalDate: *s, Weekday: t.Format("Mon")}
}

func rating(avg *float64, n int) *ratingData {
	if avg == nil || n == 0 {
		return nil
	}
	return &ratingData{Average: *avg, Count: n}
}

func totalMinutes(prep, cook *int) *int {
	if prep == nil && cook == nil {
		return nil
	}
	t := 0
	if prep != nil {
		t += *prep
	}
	if cook != nil {
		t += *cook
	}
	return &t
}

func decodeIngredients(raw []byte) ([]storedIngredient, error) {
	out := []storedIngredient{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode ingredients: %w", err)
	}
	return out, nil
}

// recipeRow is a recipe as stored, for update.
type recipeRow struct {
	title                   string
	description, steps, url *string
	servings, prep, cook    *int
	ingredients             []storedIngredient
	ingredientsJSON         []byte
	tags                    []string
}

func lockRecipe(ctx context.Context, tx pgx.Tx, hid, id uuid.UUID) (recipeRow, error) {
	var r recipeRow
	err := tx.QueryRow(ctx, `SELECT title, description, servings, prep_minutes, cook_minutes, ingredients, steps, tags, source_url
	                          FROM recipes WHERE id = $1 AND household_id = $2 FOR UPDATE`, id, hid).
		Scan(&r.title, &r.description, &r.servings, &r.prep, &r.cook, &r.ingredientsJSON, &r.steps, &r.tags, &r.url)
	if err != nil {
		return r, err
	}
	r.ingredients, err = decodeIngredients(r.ingredientsJSON)
	return r, err
}

// loadRecipe reads one full recipe with ratings, history and allergy
// warnings; withFeedback adds the newest feedback rows.
func loadRecipe(ctx context.Context, q querier, hid, id uuid.UUID, today time.Time, withFeedback bool) (recipeData, error) {
	var (
		d          recipeData
		rid        uuid.UUID
		raw        []byte
		steps      *string
		avg        *float64
		n          int
		last, next *string
	)
	err := q.QueryRow(ctx, `SELECT r.id, r.title, r.description, r.servings, r.prep_minutes, r.cook_minutes, r.ingredients,
	        r.steps, r.tags, r.source_url, r.created_at, r.updated_at,`+statsCols+`
	   FROM recipes r WHERE r.id = $2 AND r.household_id = $3`, today.Format(time.DateOnly), id, hid).
		Scan(&rid, &d.Name, &d.Notes, &d.Servings, &d.PrepMinutes, &d.CookMinutes, &raw, &steps, &d.Tags, &d.SourceURL,
			&d.CreatedAt, &d.UpdatedAt, &avg, &n, &d.TimesMade, &last, &next)
	if err != nil {
		return d, err
	}
	d.ID = rid.String()
	ings, err := decodeIngredients(raw)
	if err != nil {
		return d, err
	}
	d.Ingredients = toCatalog(ings)
	d.Steps = splitSteps(steps)
	if d.Tags == nil {
		d.Tags = []string{}
	}
	d.TotalMinutes = totalMinutes(d.PrepMinutes, d.CookMinutes)
	d.Rating = rating(avg, n)
	d.LastMade, d.NextPlanned = day(last), day(next)

	matchers, checks, err := familyAllergies(ctx, q, hid)
	if err != nil {
		return d, err
	}
	names := make([]string, len(ings))
	for i, s := range ings {
		names[i] = s.Name
	}
	d.AllergyWarnings = AllergyWarnings(matchers, names)
	d.Checks = checks

	if withFeedback {
		rows, err := q.Query(ctx, `
SELECT m.date::text, m.slot, p.display_name, f.person_id::text, f.rating, f.comment
  FROM meal_feedback f
  JOIN meal_plans m ON m.id = f.meal_plan_id
  LEFT JOIN people p ON p.id = f.person_id AND p.household_id = f.household_id
 WHERE m.recipe_id = $1 AND m.household_id = $2
 ORDER BY m.date DESC, f.updated_at DESC, f.id
 LIMIT $3`, id, hid, feedbackShown)
		if err != nil {
			return d, err
		}
		defer rows.Close()
		for rows.Next() {
			var fb feedbackData
			var r *int16
			if err := rows.Scan(&fb.LocalDate, &fb.Slot, &fb.Person, &fb.PersonID, &r, &fb.Comment); err != nil {
				return d, err
			}
			if r != nil {
				v := int(*r)
				fb.Rating = &v
			}
			if dd := day(&fb.LocalDate); dd != nil {
				fb.Weekday = dd.Weekday
			}
			d.Feedback = append(d.Feedback, fb)
		}
		if err := rows.Err(); err != nil {
			return d, err
		}
	}
	return d, nil
}

// describe is a short phrase for summaries: "4 servings, 45 min, 9
// ingredients".
func (d recipeData) describe() string {
	var parts []string
	if d.Servings != nil {
		parts = append(parts, plural(*d.Servings, "serving"))
	}
	if d.TotalMinutes != nil {
		parts = append(parts, fmt.Sprintf("%d min", *d.TotalMinutes))
	}
	parts = append(parts, plural(len(d.Ingredients), "ingredient"), plural(len(d.Steps), "step"))
	if d.Rating != nil {
		parts = append(parts, fmt.Sprintf("rated %.1f/5", d.Rating.Average))
	}
	return strings.Join(parts, ", ")
}

// resolveRecipe matches recipe_id: a UUID, or the recipe's exact name (any
// case). Several matches or none is an isError listing candidates.
func resolveRecipe(ctx context.Context, q querier, hid uuid.UUID, args map[string]any, example map[string]any) (uuid.UUID, *mcp.CallToolResult, error) {
	ref, err := optionalString(args, "recipe_id")
	if err != nil {
		return uuid.Nil, invalid(err, example), nil
	}
	if ref == nil {
		return uuid.Nil, respond.Invalid("recipe_id", "recipe_id is required.",
			"Pass the recipe's id from recipe_find, or its name, e.g. Chicken tacos.", example), nil
	}
	if id, perr := uuid.Parse(*ref); perr == nil {
		return id, nil, nil
	}
	type cand struct {
		id    uuid.UUID
		title string
		exact bool
	}
	rows, err := q.Query(ctx, `SELECT id, title, lower(btrim(title)) = lower($2) FROM recipes
	                            WHERE household_id = $1 AND (lower(btrim(title)) = lower($2) OR title ILIKE '%' || $3 || '%' ESCAPE '\')
	                            ORDER BY lower(title), created_at, id LIMIT 30`, hid, *ref, escapeLike(*ref))
	if err != nil {
		return uuid.Nil, nil, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cand, error) {
		var c cand
		return c, r.Scan(&c.id, &c.title, &c.exact)
	})
	if err != nil {
		return uuid.Nil, nil, err
	}
	var exact []cand
	for _, c := range all {
		if c.exact {
			exact = append(exact, c)
		}
	}
	label := func(cs []cand) string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = fmt.Sprintf("%s (%s)", c.title, c.id)
		}
		return strings.Join(out, ", ")
	}
	switch {
	case len(exact) == 1:
		return exact[0].id, nil, nil
	case len(exact) > 1:
		return uuid.Nil, respond.Fail(respond.Error{
			Message: fmt.Sprintf("%d recipes are named %s: %s.", len(exact), *ref, label(exact)),
			Fix:     "Pass the recipe_id of the one you mean.", Field: "recipe_id",
		}), nil
	case len(all) > 0:
		return uuid.Nil, respond.Fail(respond.Error{
			Message: fmt.Sprintf("No recipe is named exactly %q. Close matches: %s.", *ref, label(all)),
			Fix:     "Pass the recipe_id of the one you mean, or call recipe_find query=… to search.", Field: "recipe_id",
		}), nil
	}
	return uuid.Nil, respond.Fail(respond.Error{
		Message: fmt.Sprintf("No recipe named %q in this family.", *ref),
		Fix:     "Call recipe_find to search the family's recipes, or save it with recipe_create.", Field: "recipe_id",
	}), nil
}

// planHint is the recipe tools' next step into the meal plan.
func planHint(recipeID string) string {
	return fmt.Sprintf(`Plan it with meal_set meals=[{"date":"…","slot":"dinner","recipe_id":"%s"}].`, recipeID)
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ---------------------------------------------------------------------------
// recipe_find
// ---------------------------------------------------------------------------

var findAliases = map[string]string{
	"recipe_id": "id", "name": "query", "q": "query", "search": "query", "title": "query",
	"tags": "tag", "ingredients": "ingredient", "with": "ingredient",
	"max_time": "max_minutes", "total_minutes": "max_minutes", "minutes": "max_minutes", "under_minutes": "max_minutes",
}

// stopWords are dropped from query: they would match every recipe.
var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "the": true, "with": true, "of": true, "for": true, "to": true, "in": true,
	"recipe": true, "recipes": true, "my": true, "our": true, "some": true, "me": true, "find": true, "on": true,
}

var findExample = map[string]any{"ingredient": "chicken", "max_minutes": 30}

// NewRecipeFindTool creates recipe_find (catalog v2, class R): search saved
// recipes by name, tag or ingredient, with time and rating. query matches
// whole words (singular or plural) in the name (scored 3), tags (2),
// ingredients (2) and notes (1), best first. ingredient keeps only recipes
// containing every listed ingredient. Recipes are a family's own box with no
// Typesense index (the API does not index them), so this is plain SQL.
func NewRecipeFindTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("recipe_find", append([]mcp.ToolOption{
		mcp.WithDescription("Search saved recipes by name, tag or ingredient, with time and rating."),
		mcp.WithString("query", mcp.Description("Words in the name, tags or ingredients, e.g. tacos")),
		mcp.WithString("tag", mcp.Description("Only recipes with this tag, e.g. vegetarian")),
		mcp.WithString("ingredient", mcp.Description("Only recipes using it; comma for several, e.g. chicken, rice")),
		mcp.WithNumber("max_minutes", mcp.Description("Only recipes ready in this many minutes, e.g. 30"), mcp.Min(1)),
		mcp.WithString("id", mcp.Description("A recipe_id to return just that recipe")),
		mcp.WithNumber("limit", mcp.Description("How many to return (default 25, max 100)"), mcp.Min(1), mcp.Max(maxLimit)),
		mcp.WithString("cursor", mcp.Description("next_cursor from the previous page")),
		respond.ListSchema[recipeItem](),
	}, annotations("Find recipes", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canRead(c.role) {
			return cannotRead(c.role), nil
		}
		args := withAliases(req.GetArguments(), findAliases)

		idRef, err := optionalString(args, "id")
		if err != nil {
			return invalid(err, findExample), nil
		}
		var id *uuid.UUID
		if idRef != nil {
			u, perr := uuid.Parse(*idRef)
			if perr != nil {
				return respond.Invalid("id", fmt.Sprintf("id %q is not a recipe_id.", *idRef),
					"Pass a recipe_id from an earlier recipe_find, or search by name with query.", map[string]any{"query": *idRef}), nil
			}
			id = &u
		}
		query, err := optionalString(args, "query")
		if err != nil {
			return invalid(err, findExample), nil
		}
		var qwords []string
		if query != nil {
			for _, w := range words(*query) {
				if !stopWords[w] {
					qwords = append(qwords, wordForm(w))
				}
			}
			if len(qwords) == 0 {
				for _, w := range words(*query) {
					qwords = append(qwords, wordForm(w))
				}
			}
		}
		tagsIn, _, _, err := readStrings(args, "tag")
		if err != nil {
			return invalid(badArg("tag", "tag must be text.", "Pass one tag, e.g. vegetarian."), findExample), nil
		}
		tags, err := normTags(tagsIn)
		if err != nil {
			return invalid(err, findExample), nil
		}
		ingIn, _, _, err := readStrings(args, "ingredient")
		if err != nil {
			return invalid(badArg("ingredient", "ingredient must be text.", "Pass an ingredient, e.g. chicken."), findExample), nil
		}
		var ingPG []string
		var ingGo []*regexp.Regexp
		for _, term := range ingIn {
			for _, t := range strings.Split(term, " and ") {
				if pg, goRe, ok := phrasePatterns(t); ok {
					ingPG = append(ingPG, pg)
					ingGo = append(ingGo, regexp.MustCompile(goRe))
				}
			}
		}
		maxMin, err := readNum(args, "max_minutes", 1, maxMinutes)
		if err != nil {
			return invalid(err, findExample), nil
		}
		limit := defaultLimit
		if lf, err := readNum(args, "limit", 1, maxLimit); err != nil {
			return invalid(err, findExample), nil
		} else if lf.set {
			limit = lf.val
		}
		offset := 0
		if cur, err := optionalString(args, "cursor"); err != nil {
			return invalid(err, findExample), nil
		} else if cur != nil {
			o, ok := respond.DecodeCursor(*cur)
			if !ok {
				return respond.Invalid("cursor", "cursor is not one this tool returned.",
					"Pass next_cursor from the previous recipe_find result unchanged, or leave cursor out.", findExample), nil
			}
			offset = o
		}

		today := familyToday(c.tz)
		var maxArg *int
		if maxMin.set {
			maxArg = &maxMin.val
		}
		if qwords == nil {
			qwords = []string{}
		}
		if ingPG == nil {
			ingPG = []string{}
		}
		rows, err := pool.Query(ctx, `
SELECT r.id, r.title, r.tags, r.servings, r.prep_minutes, r.cook_minutes,
       ARRAY(SELECT e->>'name' FROM jsonb_array_elements(r.ingredients) e),`+statsCols+`,
       count(*) OVER ()
  FROM recipes r,
       LATERAL (SELECT COALESCE((SELECT sum(
                   CASE WHEN r.title ~* ('\m' || w || '\M') THEN 3 ELSE 0 END +
                   CASE WHEN EXISTS (SELECT 1 FROM unnest(r.tags) t WHERE t ~* ('\m' || w || '\M')) THEN 2 ELSE 0 END +
                   CASE WHEN EXISTS (SELECT 1 FROM jsonb_array_elements(r.ingredients) e WHERE (e->>'name') ~* ('\m' || w || '\M')) THEN 2 ELSE 0 END +
                   CASE WHEN COALESCE(r.description, '') ~* ('\m' || w || '\M') THEN 1 ELSE 0 END)
                 FROM unnest($3::text[]) w), 0) AS score) sc
 WHERE r.household_id = $2
   AND ($4::uuid IS NULL OR r.id = $4)
   AND (cardinality($3::text[]) = 0 OR sc.score > 0)
   AND r.tags @> $5::text[]
   AND NOT EXISTS (SELECT 1 FROM unnest($6::text[]) p
                    WHERE NOT EXISTS (SELECT 1 FROM jsonb_array_elements(r.ingredients) e WHERE (e->>'name') ~* p))
   AND ($7::int IS NULL OR (COALESCE(r.prep_minutes, r.cook_minutes) IS NOT NULL
                            AND COALESCE(r.prep_minutes, 0) + COALESCE(r.cook_minutes, 0) <= $7))
 ORDER BY sc.score DESC, lower(r.title), r.id
 LIMIT $8 OFFSET $9`,
			today.Format(time.DateOnly), c.householdID, qwords, id, tags, ingPG, maxArg, limit, offset)
		if err != nil {
			return respond.Failed("search recipes", err), nil
		}
		defer rows.Close()

		matchers, allergyChecks, err := familyAllergies(ctx, pool, c.householdID)
		if err != nil {
			return respond.Failed("search recipes", err), nil
		}
		items := []recipeItem{}
		total := 0
		for rows.Next() {
			var (
				it         recipeItem
				rid        uuid.UUID
				prep, cook *int
				ings       []string
				avg        *float64
				n          int
				last, next *string
			)
			if err := rows.Scan(&rid, &it.Name, &it.Tags, &it.Servings, &prep, &cook, &ings, &avg, &n, &it.TimesMade, &last, &next, &total); err != nil {
				return respond.Failed("search recipes", err), nil
			}
			it.ID = rid.String()
			if it.Tags == nil {
				it.Tags = []string{}
			}
			it.TotalMinutes = totalMinutes(prep, cook)
			it.IngredientCount = len(ings)
			it.Rating = rating(avg, n)
			it.LastMade = day(last)
			for _, ing := range ings {
				for _, re := range ingGo {
					if re.MatchString(ing) && !slices.Contains(it.Matched, ing) {
						it.Matched = append(it.Matched, ing)
					}
				}
			}
			it.AllergyWarnings = warningLines(AllergyWarnings(matchers, ings))
			items = append(items, it)
		}
		if err := rows.Err(); err != nil {
			return respond.Failed("search recipes", err), nil
		}
		if len(items) == 0 && offset > 0 {
			// Past the end: count without paging for an honest total.
			total = offset
		}

		var filters []string
		if query != nil {
			filters = append(filters, fmt.Sprintf("matching %q", *query))
		}
		if len(tags) > 0 {
			filters = append(filters, "tagged "+strings.Join(tags, ", "))
		}
		if len(ingIn) > 0 {
			filters = append(filters, "with "+strings.Join(ingIn, ", "))
		}
		if maxMin.set {
			filters = append(filters, fmt.Sprintf("ready in %d min or less", maxMin.val))
		}
		what := "recipes"
		if len(filters) > 0 {
			what += " " + strings.Join(filters, ", ")
		}
		page := respond.Page{Total: total, NextCursor: respond.CursorAfter(offset+len(items), total),
			Narrow: "Pass query, tag, ingredient or max_minutes to narrow, or cursor for the next page."}
		if len(items) == 0 {
			next := []string{"Save one with recipe_create name=… ingredients=[…] steps=[…]."}
			if len(filters) > 0 {
				next = append([]string{"Try fewer filters, or a single word in query."}, next...)
			}
			return respond.List(fmt.Sprintf("No %s found.", what), items, page, next...), nil
		}
		summary := fmt.Sprintf("Found %s.", plural(total, "recipe"))
		if len(filters) > 0 {
			summary = fmt.Sprintf("Found %d %s.", total, what)
			if total == 1 {
				summary = fmt.Sprintf("Found 1 %s.", strings.Replace(what, "recipes", "recipe", 1))
			}
		}
		warned := 0
		for _, it := range items {
			if len(it.AllergyWarnings) > 0 {
				warned++
			}
		}
		if warned > 0 {
			summary += fmt.Sprintf(" %s contain an allergen for someone in the family (see allergy_warnings).", plural(warned, "recipe"))
		}
		summary += checkSentence(allergyChecks)
		return respond.List(summary, items, page,
			fmt.Sprintf("Read ingredients and steps with recipe_get recipe_id=%s.", items[0].ID)), nil
	}
	return "recipe_find", tool, handler
}

// ---------------------------------------------------------------------------
// recipe_get
// ---------------------------------------------------------------------------

var getAliases = map[string]string{"id": "recipe_id", "recipe": "recipe_id", "name": "recipe_id", "title": "recipe_id"}

// NewRecipeGetTool creates recipe_get (catalog v2, class R): one recipe with
// ingredients, steps, servings, notes, family ratings and allergy warnings.
// recipe_id is the id or the exact name.
func NewRecipeGetTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("recipe_get", append([]mcp.ToolOption{
		mcp.WithDescription("Read one recipe: ingredients, steps, servings, notes, family ratings."),
		mcp.WithString("recipe_id", mcp.Description("The recipe's id from recipe_find, or its name"), mcp.Required()),
		respond.Schema[recipeData](),
	}, annotations("Read recipe", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canRead(c.role) {
			return cannotRead(c.role), nil
		}
		args := withAliases(req.GetArguments(), getAliases)
		example := map[string]any{"recipe_id": "Chicken tacos"}
		id, bad, err := resolveRecipe(ctx, pool, c.householdID, args, example)
		if err != nil {
			return respond.Failed("read the recipe", err), nil
		} else if bad != nil {
			return bad, nil
		}
		d, err := loadRecipe(ctx, pool, c.householdID, id, familyToday(c.tz), true)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("recipe", id.String(), "Call recipe_find to search the family's recipes."), nil
		}
		if err != nil {
			return respond.Failed("read the recipe", err), nil
		}
		summary := fmt.Sprintf("%s: %s.", d.Name, d.describe())
		if d.LastMade != nil {
			summary += fmt.Sprintf(" Last made %s %s.", d.LastMade.Weekday, d.LastMade.LocalDate)
		}
		summary += warningSentence(d.AllergyWarnings) + checkSentence(d.Checks)
		return respond.OK(summary, d,
			planHint(d.ID),
			`Add the ingredients to the grocery list with list_add_items list_id="Grocery" items=[…].`,
			fmt.Sprintf("Change it with recipe_update recipe_id=%s.", d.ID)), nil
	}
	return "recipe_get", tool, handler
}

// ---------------------------------------------------------------------------
// Write fields
// ---------------------------------------------------------------------------

var writeAliases = map[string]string{
	"id": "recipe_id", "recipe": "recipe_id",
	"title": "name", "recipe_name": "name",
	"directions": "steps", "instructions": "steps", "method": "steps",
	"serves": "servings", "yield": "servings", "portions": "servings",
	"time": "total_minutes", "minutes": "total_minutes", "ready_in": "total_minutes", "total_time": "total_minutes",
	"prep_time": "prep_minutes", "cook_time": "cook_minutes",
	"url": "source_url", "source": "source_url", "link": "source_url",
	"description": "notes", "note": "notes",
	"tag": "tags",
}

var (
	createExample = map[string]any{
		"name": "Chicken tacos", "servings": 4, "total_minutes": 30,
		"ingredients": []any{
			map[string]any{"item": "chicken thighs", "quantity": 1, "unit": "lb"},
			map[string]any{"item": "tortillas", "quantity": 8},
			map[string]any{"item": "salsa", "quantity": 0.5, "unit": "cup"},
		},
		"steps": []any{"Cook the chicken.", "Warm the tortillas.", "Fill and top with salsa."},
		"tags":  []any{"dinner", "quick"},
	}
	updateExample = map[string]any{"recipe_id": "Chicken tacos", "servings": 6}
	deleteExample = map[string]any{"recipe_id": "Chicken tacos"}
)

type recipeFields struct {
	name, notes, url       textField
	servings, prep, cook   numField
	total                  numField
	ingredients            []storedIngredient
	ingredientsGiven       bool
	steps                  []string
	stepsGiven, stepsClear bool
	tags                   []string
	tagsGiven, tagsClear   bool
}

func (f recipeFields) given() bool {
	return f.name.given() || f.notes.given() || f.url.given() || f.servings.given() || f.prep.given() ||
		f.cook.given() || f.total.given() || f.ingredientsGiven || f.stepsGiven || f.tagsGiven
}

func parseFields(args map[string]any) (recipeFields, error) {
	var f recipeFields
	var err error
	if f.name, err = readText(args, "name", maxNameLen); err != nil {
		return f, err
	}
	if f.notes, err = readText(args, "notes", maxNotesLen); err != nil {
		return f, err
	}
	if f.url, err = readText(args, "source_url", maxURLLen); err != nil {
		return f, err
	}
	if f.url.set {
		u, perr := url.Parse(f.url.val)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return f, badArg("source_url", fmt.Sprintf("source_url %q is not a web address.", f.url.val),
				"Pass the full address starting with https://, or leave source_url out.")
		}
	}
	if f.servings, err = readNum(args, "servings", 1, maxServings); err != nil {
		return f, err
	}
	if f.prep, err = readNum(args, "prep_minutes", 0, maxMinutes); err != nil {
		return f, err
	}
	if f.cook, err = readNum(args, "cook_minutes", 0, maxMinutes); err != nil {
		return f, err
	}
	if f.total, err = readNum(args, "total_minutes", 0, maxMinutes); err != nil {
		return f, err
	}
	for _, k := range []string{"ingredients", "steps"} {
		if s, isStr := args[k].(string); isStr && strings.TrimSpace(s) == "" {
			delete(args, k) // "" means not given
		}
	}
	if v, ok := args["ingredients"]; ok {
		f.ingredientsGiven = true
		if f.ingredients, err = parseIngredients(v); err != nil {
			return f, err
		}
	}
	if v, ok := args["steps"]; ok {
		f.stepsGiven = true
		if v == nil {
			f.stepsClear = true
		} else if f.steps, err = parseSteps(v); err != nil {
			return f, err
		}
	}
	tags, present, clear, err := readStrings(args, "tags")
	if err != nil {
		return f, err
	}
	if present {
		f.tagsGiven, f.tagsClear = true, clear
		if f.tags, err = normTags(tags); err != nil {
			return f, err
		}
	}
	return f, nil
}

// minutes resolves prep/cook from the fields and the current values:
// total_minutes alone sets cook = total - prep (prep kept); null total
// clears both.
func (f recipeFields) minutes(curPrep, curCook *int) (prep, cook *int, err error) {
	prep, cook = curPrep, curCook
	if f.prep.given() {
		prep = f.prep.ptr()
	}
	if f.cook.given() {
		cook = f.cook.ptr()
	}
	switch {
	case f.total.clear && !f.prep.given() && !f.cook.given():
		return nil, nil, nil
	case f.total.set && !f.cook.given():
		p := 0
		if prep != nil {
			p = *prep
		}
		if f.total.val < p {
			return nil, nil, badArg("total_minutes", fmt.Sprintf("total_minutes %d is less than prep_minutes %d.", f.total.val, p),
				"Pass total_minutes at least as large as prep_minutes, or pass cook_minutes instead.")
		}
		c := f.total.val - p
		cook = &c
	}
	return prep, cook, nil
}

func ingredientNames(ings []storedIngredient) []string {
	out := make([]string, len(ings))
	for i, s := range ings {
		out[i] = s.Name
	}
	return out
}

// fieldParams are the optional params shared by create and update.
func fieldParams() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithNumber("servings", mcp.Description("How many it serves, e.g. 4"), mcp.Min(1)),
		mcp.WithNumber("total_minutes", mcp.Description("Minutes from start to table, e.g. 45"), mcp.Min(0)),
		mcp.WithNumber("prep_minutes", mcp.Description("Hands-on prep minutes, e.g. 15 (optional split of total)"), mcp.Min(0)),
		mcp.WithNumber("cook_minutes", mcp.Description("Cooking minutes, e.g. 30 (optional split of total)"), mcp.Min(0)),
		mcp.WithArray("tags", mcp.Description(`Tags, e.g. ["dinner","vegetarian"]`), mcp.WithStringItems()),
		mcp.WithString("source_url", mcp.Description("Where it came from, e.g. https://example.com/tacos")),
		mcp.WithString("notes", mcp.Description("Tips or variations, e.g. Kids like it mild")),
	}
}

var ingredientSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"item":     map[string]any{"type": "string", "description": "What it is, e.g. flour"},
		"quantity": map[string]any{"type": []string{"number", "string"}, "description": "How much, e.g. 2 or 1.5 or \"a pinch\""},
		"unit":     map[string]any{"type": "string", "description": "e.g. cup, tbsp, g, lb; leave out for a count"},
		"section":  map[string]any{"type": "string", "description": "Part of the recipe, e.g. Sauce"},
	},
	"required": []string{"item"},
}

func ingredientsParam(required bool, desc string) mcp.ToolOption {
	opts := []mcp.PropertyOption{mcp.Description(desc), mcp.Items(ingredientSchema)}
	if required {
		opts = append(opts, mcp.Required())
	}
	return mcp.WithArray("ingredients", opts...)
}

func stepsParam(required bool, desc string) mcp.ToolOption {
	opts := []mcp.PropertyOption{mcp.Description(desc), mcp.WithStringItems()}
	if required {
		opts = append(opts, mcp.Required())
	}
	return mcp.WithArray("steps", opts...)
}

// ---------------------------------------------------------------------------
// recipe_create
// ---------------------------------------------------------------------------

// NewRecipeCreateTool creates recipe_create (catalog v2, class C). A pasted
// recipe is split by the model into ingredients and steps; plain ingredient
// lines and a single steps string are split here too. The loop guard returns
// a recipe with the same name saved in the last 10 minutes (duplicate:true)
// instead of a copy, under an advisory lock so parallel calls make one row.
func NewRecipeCreateTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := append([]mcp.ToolOption{
		mcp.WithDescription("Save a recipe with structured ingredients. If the user pasted text, split it into ingredients and steps first."),
		mcp.WithString("name", mcp.Description("Recipe name, e.g. Chicken tacos"), mcp.Required()),
		ingredientsParam(true, `Ingredients, e.g. [{"item":"flour","quantity":2,"unit":"cup"}]`),
		stepsParam(true, `Steps in order, e.g. ["Preheat oven to 375F.","Mix."]`),
	}, fieldParams()...)
	opts = append(opts, respond.Schema[recipeData]())
	tool := mcp.NewTool("recipe_create", append(opts, annotations("Save recipe", false, false, false)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "recipe_create"), nil
		}
		f, err := parseFields(withAliases(req.GetArguments(), writeAliases))
		if err != nil {
			return invalid(err, createExample), nil
		}
		switch {
		case !f.name.set:
			return respond.Invalid("name", "name is required.", "Pass the recipe's name, e.g. Chicken tacos.", createExample), nil
		case !f.ingredientsGiven:
			return respond.Invalid("ingredients", "ingredients is required.",
				`Split the recipe into ingredients, e.g. [{"item":"flour","quantity":2,"unit":"cup"}].`, createExample), nil
		case len(f.steps) == 0:
			return respond.Invalid("steps", "steps is required.",
				`Split the method into steps, e.g. ["Preheat the oven.","Mix."].`, createExample), nil
		}
		prep, cook, err := f.minutes(nil, nil)
		if err != nil {
			return invalid(err, createExample), nil
		}
		ingJSON, err := json.Marshal(f.ingredients)
		if err != nil {
			return respond.Failed("save the recipe", err), nil
		}
		tags := f.tags
		if tags == nil {
			tags = []string{}
		}
		today := familyToday(c.tz)

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("save the recipe", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('recipe_create:' || $1::text || ':' || lower($2), 0))`,
			c.householdID, f.name.val); err != nil {
			return respond.Failed("save the recipe", err), nil
		}
		var dupID uuid.UUID
		err = tx.QueryRow(ctx, `SELECT id FROM recipes WHERE household_id = $1 AND lower(btrim(title)) = lower($2)
		                         AND created_at > NOW() - $3::interval ORDER BY created_at DESC LIMIT 1`,
			c.householdID, f.name.val, duplicateWindow).Scan(&dupID)
		switch {
		case err == nil:
			d, err := loadRecipe(ctx, tx, c.householdID, dupID, today, false)
			if err != nil {
				return respond.Failed("save the recipe", err), nil
			}
			d.Duplicate = true
			return respond.OK(fmt.Sprintf("%s was already saved a few minutes ago; returned that recipe instead of a copy.%s%s", d.Name, warningSentence(d.AllergyWarnings), checkSentence(d.Checks)), d,
				fmt.Sprintf("Change it with recipe_update recipe_id=%s instead of saving it again.", d.ID)), nil
		case !errors.Is(err, pgx.ErrNoRows):
			return respond.Failed("save the recipe", err), nil
		}

		var id uuid.UUID
		if err := tx.QueryRow(ctx, `
INSERT INTO recipes (household_id, title, description, servings, prep_minutes, cook_minutes, ingredients, steps, tags, source_url)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
			c.householdID, f.name.val, f.notes.ptr(), f.servings.ptr(), prep, cook, ingJSON, joinSteps(f.steps), tags, f.url.ptr()).Scan(&id); err != nil {
			return respond.Failed("save the recipe", err), nil
		}
		var sameName int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM recipes WHERE household_id = $1 AND lower(btrim(title)) = lower($2) AND id <> $3`,
			c.householdID, f.name.val, id).Scan(&sameName); err != nil {
			return respond.Failed("save the recipe", err), nil
		}
		d, err := loadRecipe(ctx, tx, c.householdID, id, today, false)
		if err != nil {
			return respond.Failed("save the recipe", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("save the recipe", err), nil
		}
		next := []string{
			planHint(d.ID),
			`Add the ingredients to the grocery list with list_add_items list_id="Grocery" items=[…].`,
			fmt.Sprintf("Fix anything with recipe_update recipe_id=%s.", d.ID),
		}
		if sameName > 0 {
			next = append(next, fmt.Sprintf("Another recipe is also named %s; pass recipe ids, not the name, from now on.", d.Name))
		}
		return respond.OK(fmt.Sprintf("Saved recipe %s: %s.%s%s", d.Name, d.describe(), warningSentence(d.AllergyWarnings), checkSentence(d.Checks)), d, next...), nil
	}
	return "recipe_create", tool, handler
}

// ---------------------------------------------------------------------------
// recipe_update
// ---------------------------------------------------------------------------

// NewRecipeUpdateTool creates recipe_update (catalog v2, class U). Only the
// given fields change; ingredients and steps replace the whole list. JSON
// null clears notes, servings, minutes, tags, source_url or steps; name and
// ingredients can't be cleared. A call that changes nothing writes nothing.
func NewRecipeUpdateTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := append([]mcp.ToolOption{
		mcp.WithDescription("Change a recipe; only the given fields change. ingredients/steps replace the whole list."),
		mcp.WithString("recipe_id", mcp.Description("The recipe's id from recipe_find, or its name"), mcp.Required()),
		mcp.WithString("name", mcp.Description("New name")),
		ingredientsParam(false, `The full new ingredient list, e.g. [{"item":"flour","quantity":2,"unit":"cup"}]`),
		stepsParam(false, "The full new list of steps"),
	}, fieldParams()...)
	opts = append(opts, respond.Schema[recipeData]())
	tool := mcp.NewTool("recipe_update", append(opts, annotations("Update recipe", false, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "recipe_update"), nil
		}
		args := withAliases(req.GetArguments(), writeAliases)
		f, err := parseFields(args)
		if err != nil {
			return invalid(err, updateExample), nil
		}
		if f.name.clear {
			return respond.Invalid("name", "name cannot be cleared.", "Pass a new name, or leave name out to keep it.", updateExample), nil
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("update the recipe", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		id, bad, err := resolveRecipe(ctx, tx, c.householdID, args, updateExample)
		if err != nil {
			return respond.Failed("update the recipe", err), nil
		} else if bad != nil {
			return bad, nil
		}
		if !f.given() {
			return respond.Invalid("", "No fields to update.",
				"Pass recipe_id plus at least one field, e.g. servings, ingredients or steps.", updateExample), nil
		}
		cur, err := lockRecipe(ctx, tx, c.householdID, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("recipe", id.String(), "Call recipe_find to search the family's recipes."), nil
		}
		if err != nil {
			return respond.Failed("update the recipe", err), nil
		}
		prep, cook, err := f.minutes(cur.prep, cur.cook)
		if err != nil {
			return invalid(err, updateExample), nil
		}

		vals := []any{id, c.householdID}
		var sets, changed []string
		set := func(param, col string, v any) {
			vals = append(vals, v)
			sets = append(sets, fmt.Sprintf("%s = $%d", col, len(vals)))
			if !slices.Contains(changed, param) {
				changed = append(changed, param)
			}
		}
		if f.name.set && f.name.val != cur.title {
			set("name", "title", f.name.val)
		}
		if f.notes.given() && !samePtr(f.notes.ptr(), cur.description) {
			set("notes", "description", f.notes.ptr())
		}
		if f.url.given() && !samePtr(f.url.ptr(), cur.url) {
			set("source_url", "source_url", f.url.ptr())
		}
		if f.servings.given() && !sameInt(f.servings.ptr(), cur.servings) {
			set("servings", "servings", f.servings.ptr())
		}
		if !sameInt(prep, cur.prep) {
			set("prep_minutes", "prep_minutes", prep)
		}
		if !sameInt(cook, cur.cook) {
			set("cook_minutes", "cook_minutes", cook)
		}
		if f.ingredientsGiven {
			newJSON, err := json.Marshal(f.ingredients)
			if err != nil {
				return respond.Failed("update the recipe", err), nil
			}
			oldJSON, _ := json.Marshal(cur.ingredients)
			if string(newJSON) != string(oldJSON) {
				set("ingredients", "ingredients", newJSON)
			}
		}
		if f.stepsGiven {
			want := joinSteps(f.steps)
			if !samePtr(want, joinSteps(splitSteps(cur.steps))) || (want == nil) != (cur.steps == nil) {
				set("steps", "steps", want)
			}
		}
		if f.tagsGiven {
			want := f.tags
			if want == nil {
				want = []string{}
			}
			if !slices.Equal(want, cur.tags) && !(len(want) == 0 && len(cur.tags) == 0) {
				set("tags", "tags", want)
			}
		}
		if len(sets) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE recipes SET `+strings.Join(sets, ", ")+` WHERE id = $1 AND household_id = $2`, vals...); err != nil {
				return respond.Failed("update the recipe", err), nil
			}
		}
		d, err := loadRecipe(ctx, tx, c.householdID, id, familyToday(c.tz), false)
		if err != nil {
			return respond.Failed("update the recipe", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("update the recipe", err), nil
		}
		d.Changed = changed
		next := []string{fmt.Sprintf("Read it with recipe_get recipe_id=%s.", d.ID)}
		if len(changed) == 0 {
			return respond.OK(fmt.Sprintf("Nothing changed: %s already has those details (%s).", d.Name, d.describe()), d, next...), nil
		}
		return respond.OK(fmt.Sprintf("Updated %s (%s): %s.%s%s", d.Name, strings.Join(changed, ", "), d.describe(), warningSentence(d.AllergyWarnings), checkSentence(d.Checks)), d, next...), nil
	}
	return "recipe_update", tool, handler
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ---------------------------------------------------------------------------
// recipe_delete
// ---------------------------------------------------------------------------

// NewRecipeDeleteTool creates recipe_delete (catalog v2, class X). Planned
// and past meals keep the recipe's name as text (047: recipe_id SET NULL,
// title kept). Documents about it move to the family; notes about it are
// deleted (rehome_entity_refs), so the call needs confirm:true only when
// there are notes to lose. A missing recipe is not found, so a repeat is
// safe.
func NewRecipeDeleteTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("recipe_delete", append([]mcp.ToolOption{
		mcp.WithDescription("Delete a recipe. Past meal plans keep the recipe name as text."),
		mcp.WithString("recipe_id", mcp.Description("The recipe's id from recipe_find, or its name"), mcp.Required()),
		mcp.WithBoolean("confirm", mcp.Description("true to also delete the notes about it (asked when there are some)")),
		respond.Schema[recipeData](),
	}, annotations("Delete recipe", false, true, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canWrite(c.role) {
			return cannotWrite(c.role, "recipe_delete"), nil
		}
		args := withAliases(req.GetArguments(), writeAliases)
		confirm := false
		switch v := args["confirm"].(type) {
		case bool:
			confirm = v
		case string:
			confirm = strings.EqualFold(strings.TrimSpace(v), "true")
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("delete the recipe", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		id, bad, err := resolveRecipe(ctx, tx, c.householdID, args, deleteExample)
		if err != nil {
			return respond.Failed("delete the recipe", err), nil
		} else if bad != nil {
			return bad, nil
		}
		if _, err := lockRecipe(ctx, tx, c.householdID, id); errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("recipe", id.String(), "Call recipe_find to search the family's recipes."), nil
		} else if err != nil {
			return respond.Failed("delete the recipe", err), nil
		}
		d, err := loadRecipe(ctx, tx, c.householdID, id, familyToday(c.tz), false)
		if err != nil {
			return respond.Failed("delete the recipe", err), nil
		}
		var meals, docs, notes int
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*)::int FROM meal_plans WHERE household_id = $2 AND recipe_id = $1),
			(SELECT count(*)::int FROM files WHERE household_id = $2 AND entity_type = 'recipe' AND entity_id = $1),
			(SELECT count(*)::int FROM notes WHERE household_id = $2 AND entity_type = 'recipe' AND entity_id = $1)`,
			id, c.householdID).Scan(&meals, &docs, &notes); err != nil {
			return respond.Failed("delete the recipe", err), nil
		}
		if notes > 0 && !confirm {
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("%s has %s that would be deleted with it.", d.Name, plural(notes, "note")),
				Fix:     "Ask the user, then call again with confirm:true to delete the recipe and its notes.",
				Field:   "confirm",
				Example: map[string]any{"recipe_id": d.ID, "confirm": true},
			}), nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM recipes WHERE id = $1 AND household_id = $2`, id, c.householdID); err != nil {
			return respond.Failed("delete the recipe", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("delete the recipe", err), nil
		}
		d.PlannedMealsKept, d.DocumentsMoved, d.NotesDeleted = &meals, &docs, &notes
		summary := fmt.Sprintf("Deleted recipe %s.", d.Name)
		var extra []string
		if meals > 0 {
			extra = append(extra, plural(meals, "planned meal")+" keep the name as text")
		}
		if docs > 0 {
			extra = append(extra, plural(docs, "document")+" moved to the family")
		}
		if notes > 0 {
			extra = append(extra, plural(notes, "note")+" deleted")
		}
		if len(extra) > 0 {
			summary += " " + strings.Join(extra, "; ") + "."
		}
		return respond.OK(summary, d, "See the recipes that are left with recipe_find."), nil
	}
	return "recipe_delete", tool, handler
}
