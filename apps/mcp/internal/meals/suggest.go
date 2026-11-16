package meals

// meal_suggest (catalog v2 "meal", class R, ticket #4496): "what's for
// dinner?" in one call. It ranks the family's saved recipes for one date and
// slot and returns a few with a one-line reason each.
//
// Safety first: allergies are safety data. A recipe whose ingredients or name
// match an allergen of anyone eating is never suggested; it is listed in
// excluded with the reason. Allergens come from people.allergies (the part
// before " (", e.g. "peanuts (carries an EpiPen)" -> peanuts) AND from any
// preference that mentions an allergy ("peanut allergy" filed as a diet, a
// dislike noted "allergic"), because a small model may have filed it there.
// Diets (vegetarian, vegan, "no pork", "gluten-free") are hard rules too.
// Matching reuses the recipe tools' allergy matcher (one implementation).
// When the tool can't be sure it says so (checks, cautions) instead of
// hiding it: allergy text it can't read, a recipe with no ingredients saved,
// an allergen of someone who is not eating.
//
// Ranking: the eaters' own ratings (meal_rate) first, the family's when they
// never rated it; recipes eaten in the 7 days before the date rank well
// below everything else, as do ones already planned in the week after;
// likes lift and dislikes sink a recipe.
//
// Roles: owner, family_manager, family_member and viewer use everything;
// a housesitter gets suggestions checked against allergies (they may see
// allergies) but likes, dislikes and diets are not used or shown (limited);
// vendor and unknown roles are refused.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

const (
	defaultSuggestions = 5
	maxSuggestions     = 10
	maxExcludedShown   = 20
	recentDays         = 7  // eaten this recently ranks lower (acceptance)
	plannedAheadDays   = 7  // planned this soon after the date ranks lower
	favouriteGapDays   = 21 // a well-rated recipe not had this long gets a lift
)

// ---------------------------------------------------------------------------
// Output types
// ---------------------------------------------------------------------------

type mealSuggestion struct {
	Rank         int         `json:"rank"`
	RecipeID     string      `json:"recipe_id" jsonschema_description:"Pass to meal_set or recipe_get."`
	Name         string      `json:"name"`
	Reason       string      `json:"reason" jsonschema_description:"One line: why it is suggested, and any catch."`
	TotalMinutes *int        `json:"total_minutes"`
	Tags         []string    `json:"tags"`
	Rating       *ratingData `json:"rating" jsonschema_description:"Ratings by the people eating (the family's when they never rated it)."`
	TimesMade    int         `json:"times_made"`
	LastMade     *dayData    `json:"last_made" jsonschema_description:"Last time before the date it was eaten."`
	Cautions     []string    `json:"cautions,omitempty" jsonschema_description:"Check these with the user before cooking it."`
	Score        float64     `json:"score" jsonschema_description:"Ranking score; higher is better."`
}

type excludedRecipe struct {
	RecipeID string `json:"recipe_id"`
	Name     string `json:"name"`
	Reason   string `json:"reason" jsonschema_description:"e.g. Nora (peanuts): peanut butter."`
}

type mealSuggestResult struct {
	LocalDate      string           `json:"local_date" jsonschema_description:"YYYY-MM-DD in the family timezone."`
	Weekday        string           `json:"weekday"`
	Slot           string           `json:"slot"`
	Eating         []string         `json:"eating" jsonschema_description:"Whose allergies, diets and tastes were used."`
	Suggestions    []mealSuggestion `json:"suggestions" jsonschema_description:"Best first."`
	Excluded       []excludedRecipe `json:"excluded" jsonschema_description:"Left out: an allergen or diet of someone eating. Never suggest these."`
	ExcludedTotal  int              `json:"excluded_total"`
	FilteredOut    int              `json:"filtered_out" jsonschema_description:"Recipes left out by max_minutes or tag."`
	AlreadyPlanned *string          `json:"already_planned" jsonschema_description:"What is already planned for this date and slot."`
	Checks         []string         `json:"checks,omitempty" jsonschema_description:"Tell the user: allergy or diet notes that could not be applied."`
	Limited        bool             `json:"limited,omitempty" jsonschema_description:"true: likes, dislikes and diets are not shared with this role."`
	Timezone       string           `json:"timezone"`
}

// ---------------------------------------------------------------------------
// People and their rules
// ---------------------------------------------------------------------------

type eater struct {
	id, userID, name string
	allergies        []string
}

// rule is one thing about one eater a recipe is checked against.
type rule struct {
	kind string // allergy, diet (hard); like, dislike (soft)
	m    allergyMatcher
}

// dietGroups are what a diet word rules out.
var (
	meatWords = []string{"chicken", "beef", "pork", "bacon", "ham", "sausage", "lamb", "mutton", "turkey", "steak", "veal",
		"duck", "goose", "venison", "mince", "ground beef", "meatball", "meatloaf", "brisket", "rib", "prosciutto", "pancetta",
		"pepperoni", "salami", "chorizo", "hot dog", "burger patty", "gelatin", "lard", "chicken stock", "beef stock", "bone broth", "meat"}
	porkWords = []string{"pork", "bacon", "ham", "sausage", "prosciutto", "pancetta", "pepperoni", "salami", "chorizo", "lard", "hot dog"}
	redMeat   = []string{"beef", "pork", "lamb", "mutton", "veal", "venison", "steak", "bacon", "ham", "sausage", "mince",
		"ground beef", "brisket", "prosciutto", "pancetta", "pepperoni", "salami", "chorizo"}
)

// dietRules turns a diet preference into hard rules. ok is false when the
// diet is not one the tool can apply ("low carb"): it is then reported in
// checks instead of silently ignored.
func dietRules(e eater, value string) ([]rule, bool) {
	v := strings.ToLower(strings.TrimSpace(regexp.MustCompile(`\([^)]*\)`).ReplaceAllString(value, " ")))
	v = strings.Join(strings.Fields(v), " ")
	label := strings.TrimSpace(value)
	mk := func(phrases ...[]string) rule {
		m := allergyMatcher{personID: e.id, person: e.name, allergy: label}
		for _, p := range phrases {
			// Group words, so a vegan's "no fish" catches catfish and
			// swordfish too (#4805). Only curated words get the compound
			// forms, so the meat lists are unchanged.
			m.group = append(m.group, compileGroupPhrases(p)...)
		}
		return rule{kind: "diet", m: m}
	}
	group := func(key string) rule {
		// key is a normalised allergen already (a group name like "dairy").
		m, _ := newAllergyMatcher(e.id, e.name, key)
		m.allergy = label
		return rule{kind: "diet", m: m}
	}
	fish, shellfish := allergenGroups["fish"], allergenGroups["shellfish"]
	switch {
	case strings.Contains(v, "vegan") || strings.Contains(v, "plant based") || strings.Contains(v, "plant-based"):
		return []rule{mk(meatWords, fish, shellfish, []string{"honey"}), group("dairy"), group("egg")}, true
	case strings.Contains(v, "pescatarian") || strings.Contains(v, "pescetarian"):
		return []rule{mk(meatWords)}, true
	case strings.Contains(v, "vegetarian") || v == "veggie":
		return []rule{mk(meatWords, fish, shellfish)}, true
	case strings.Contains(v, "halal"):
		return []rule{mk(porkWords)}, true
	case strings.Contains(v, "kosher"):
		return []rule{mk(porkWords, shellfish)}, true
	}
	var targets []string
	if m := regexp.MustCompile(`^(?:no|avoids?|without|never|doesn'?t eat|does not eat|can'?t eat|cannot eat|won'?t eat|not)\s+(.+)$`).FindStringSubmatch(v); m != nil {
		targets = listSep.Split(m[1], -1)
	} else if m := regexp.MustCompile(`^(.+?)[- ]free$`).FindStringSubmatch(v); m != nil {
		targets = listSep.Split(m[1], -1)
	}
	var out []rule
	for _, t := range targets {
		t = strings.TrimSpace(t)
		switch t {
		case "":
			continue
		case "meat":
			out = append(out, mk(meatWords))
		case "red meat":
			out = append(out, mk(redMeat))
		case "pork":
			out = append(out, mk(porkWords))
		default:
			if r := group(t); len(r.m.own) > 0 {
				out = append(out, r)
			}
		}
	}
	return out, len(out) > 0
}

// foodCategory: likes and dislikes about other things (gifts, activities)
// are not about meals.
func foodCategory(c string) bool {
	switch strings.ToLower(strings.TrimSpace(c)) {
	case "", "general", "food", "foods", "meal", "meals", "cuisine", "dinner", "lunch", "breakfast", "snack", "snacks",
		"drink", "drinks", "diet", "dietary", "ingredient", "ingredients", "dish", "dishes", "cooking", "eating":
		return true
	}
	return false
}

// eaterRules builds every rule for the eaters. checks are things the user
// should hear: allergy text that could not be read, diets not applied.
// limited (housesitter) uses only allergy data.
//
// Allergies come from allergySources, the same loader the recipe and meal
// warnings use, so a recipe meal_suggest refuses is a recipe the warnings
// warn about (#4803). Diets, likes and dislikes are meal_suggest's own.
func eaterRules(ctx context.Context, q querier, hid uuid.UUID, eaters []eater, limited bool) (rules []rule, checks []string, err error) {
	ids := make([]string, len(eaters))
	byID := map[string]eater{}
	for i, e := range eaters {
		ids[i] = e.id
		byID[e.id] = e
	}
	matchers, checks, err := allergySources(ctx, q, hid, ids)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range matchers {
		rules = append(rules, rule{kind: "allergy", m: m})
	}
	rows, err := q.Query(ctx, `
SELECT person_id::text, kind::text, category, value, coalesce(notes, '')
  FROM person_preferences
 WHERE household_id = $1 AND person_id = ANY($2::uuid[])
 ORDER BY person_id, kind, lower(value)`, hid, ids)
	if err != nil {
		return nil, nil, err
	}
	type pref struct{ person, kind, category, value, notes string }
	prefs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pref, error) {
		var p pref
		return p, r.Scan(&p.person, &p.kind, &p.category, &p.value, &p.notes)
	})
	if err != nil {
		return nil, nil, err
	}
	for _, p := range prefs {
		e := byID[p.person]
		// Allergy talk anywhere is an allergy, whatever kind it was filed as;
		// allergySources already turned those rows into rules and checks.
		if allergyMention.MatchString(p.value) || allergyMention.MatchString(p.notes) {
			continue
		}
		if limited {
			continue
		}
		switch p.kind {
		case "dietary":
			rs, ok := dietRules(e, p.value)
			if !ok {
				checks = append(checks, fmt.Sprintf("%s's diet %q was not applied; check the suggestions fit it.", e.name, p.value))
			}
			rules = append(rules, rs...)
		case "like", "dislike":
			if !foodCategory(p.category) {
				continue
			}
			// A like or dislike is one food, read the same way as an allergy
			// ("shrimp - anaphylaxis" means shrimp). The label stays the saved
			// value, so reasons read "Sam dislikes chicken" (#4802).
			for _, m := range preferenceMatchers(e.id, e.name, p.value) {
				m.allergy = p.value
				rules = append(rules, rule{kind: p.kind, m: m})
			}
		}
	}
	return rules, checks, nil
}

// ---------------------------------------------------------------------------
// Recipes, history, ratings
// ---------------------------------------------------------------------------

type candidate struct {
	id, name    string
	tags        []string
	prep, cook  *int
	ingredients []string
	lastBefore  *string
	nextAfter   *string
	timesMade   int
	ratings     map[string][]int // person id -> ratings
}

// sameRecipe joins meal_plans m to recipes r: by recipe_id, or a free-text
// meal whose title is the recipe's name.
const mealIsRecipe = `r.household_id = m.household_id AND (r.id = m.recipe_id OR (m.recipe_id IS NULL AND lower(btrim(m.title)) = lower(btrim(r.title))))`

func loadCandidates(ctx context.Context, q querier, hid uuid.UUID, date time.Time, slot string, today time.Time) ([]*candidate, error) {
	rows, err := q.Query(ctx, `
SELECT r.id::text, r.title, r.tags, r.prep_minutes, r.cook_minutes,
       ARRAY(SELECT e->>'name' FROM jsonb_array_elements(r.ingredients) e)
  FROM recipes r WHERE r.household_id = $1 ORDER BY lower(r.title), r.id`, hid)
	if err != nil {
		return nil, err
	}
	cands, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*candidate, error) {
		c := &candidate{ratings: map[string][]int{}}
		return c, r.Scan(&c.id, &c.name, &c.tags, &c.prep, &c.cook, &c.ingredients)
	})
	if err != nil || len(cands) == 0 {
		return cands, err
	}
	byID := map[string]*candidate{}
	for _, c := range cands {
		byID[c.id] = c
		if c.tags == nil {
			c.tags = []string{}
		}
	}

	// History around the date; the date+slot being asked about is left out.
	d := date.Format(time.DateOnly)
	hist, err := q.Query(ctx, `
SELECT r.id::text,
       (max(m.date) FILTER (WHERE m.date <= $2::date))::text,
       (min(m.date) FILTER (WHERE m.date > $2::date AND m.date <= $2::date + $5::int))::text,
       (count(*) FILTER (WHERE m.date <= $4::date))::int
  FROM meal_plans m JOIN recipes r ON `+mealIsRecipe+`
 WHERE m.household_id = $1 AND NOT (m.date = $2::date AND m.slot = $3)
 GROUP BY r.id`, hid, d, slot, today.Format(time.DateOnly), plannedAheadDays)
	if err != nil {
		return nil, err
	}
	defer hist.Close()
	for hist.Next() {
		var id string
		var last, next *string
		var n int
		if err := hist.Scan(&id, &last, &next, &n); err != nil {
			return nil, err
		}
		if c := byID[id]; c != nil {
			c.lastBefore, c.nextAfter, c.timesMade = last, next, n
		}
	}
	if err := hist.Err(); err != nil {
		return nil, err
	}

	fb, err := q.Query(ctx, `
SELECT r.id::text, f.person_id::text, f.rating
  FROM meal_feedback f
  JOIN meal_plans m ON m.id = f.meal_plan_id AND m.household_id = f.household_id
  JOIN recipes r ON `+mealIsRecipe+`
 WHERE m.household_id = $1 AND f.rating IS NOT NULL AND f.person_id IS NOT NULL`, hid)
	if err != nil {
		return nil, err
	}
	defer fb.Close()
	for fb.Next() {
		var id, person string
		var r int16
		if err := fb.Scan(&id, &person, &r); err != nil {
			return nil, err
		}
		if c := byID[id]; c != nil {
			c.ratings[person] = append(c.ratings[person], int(r))
		}
	}
	return cands, fb.Err()
}

func mean(xs []int) float64 {
	s := 0
	for _, x := range xs {
		s += x
	}
	return float64(s) / float64(len(xs))
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// fmtRating is "4.5/5" or "4/5".
func fmtRating(f float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", round1(f)), ".0") + "/5"
}

// joinNames is "Nora", "Nora and Sam", "Ann, Nora and Sam".
func joinNames(ns []string) string {
	switch len(ns) {
	case 0:
		return ""
	case 1:
		return ns[0]
	}
	return strings.Join(ns[:len(ns)-1], ", ") + " and " + ns[len(ns)-1]
}

func ago(days int) string {
	switch {
	case days == 0:
		return "earlier that day"
	case days == 1:
		return "the day before"
	case days < 14:
		return fmt.Sprintf("%d days before", days)
	}
	return fmt.Sprintf("%d weeks before", days/7)
}

// score ranks one safe candidate for the eaters. It returns the score, the
// positive reasons and the catches, best first.
func (c *candidate) score(date time.Time, eaters []eater, rules []rule) (float64, *ratingData, []string, []string) {
	var s float64
	var pros, cons []string
	names := map[string]string{}
	for _, e := range eaters {
		names[e.id] = e.name
	}

	// Ratings: the eaters' own first.
	var eaterAvgs []float64
	var raters []string
	eaterN := 0
	for _, e := range eaters {
		if rs := c.ratings[e.id]; len(rs) > 0 {
			avg := mean(rs)
			eaterAvgs = append(eaterAvgs, avg)
			raters = append(raters, e.name)
			eaterN += len(rs)
			if avg <= 2 {
				s -= 1.5
				cons = append(cons, fmt.Sprintf("%s rated it %s", e.name, fmtRating(avg)))
			}
		}
	}
	var rd *ratingData
	if len(eaterAvgs) > 0 {
		var sum float64
		for _, a := range eaterAvgs {
			sum += a
		}
		avg := sum / float64(len(eaterAvgs))
		s += (avg - 3) * 2
		rd = &ratingData{Average: round1(avg), Count: eaterN}
		if avg >= 3.5 {
			pros = append(pros, fmt.Sprintf("rated %s by %s", fmtRating(avg), joinNames(raters)))
		}
	} else {
		var all []int
		for _, rs := range c.ratings {
			all = append(all, rs...)
		}
		if len(all) > 0 {
			avg := mean(all)
			s += (avg - 3) * 1.5
			rd = &ratingData{Average: round1(avg), Count: len(all)}
			if avg >= 3.5 {
				pros = append(pros, fmt.Sprintf("the family rates it %s", fmtRating(avg)))
			} else if avg < 2.5 {
				cons = append(cons, fmt.Sprintf("the family rates it %s", fmtRating(avg)))
			}
		} else if c.timesMade == 0 {
			s += 0.5
			pros = append(pros, "something new: not made yet")
		}
	}

	// Recent repeats rank lower; a favourite not had in a while is lifted.
	if c.lastBefore != nil {
		if last, err := time.Parse(time.DateOnly, *c.lastBefore); err == nil {
			days := int(math.Round(date.Sub(last).Hours() / 24))
			switch {
			case days <= 3:
				s -= 8
				cons = append(cons, "had "+ago(days))
			case days <= recentDays:
				s -= 5
				cons = append(cons, "had "+ago(days))
			case days <= 14:
				s -= 1.5
				cons = append(cons, "had "+ago(days))
			case days >= favouriteGapDays && rd != nil && rd.Average >= 4:
				s += 1
				pros = append(pros, fmt.Sprintf("not had in %d weeks", days/7))
			}
		}
	}
	if c.nextAfter != nil {
		if next, err := time.Parse(time.DateOnly, *c.nextAfter); err == nil {
			s -= 5
			cons = append(cons, "already planned for "+next.Weekday().String())
		}
	}

	// Tastes.
	items := append(append([]string{c.name}, c.ingredients...), c.tags...)
	likes := 0.0
	for _, r := range rules {
		if r.kind != "like" && r.kind != "dislike" {
			continue
		}
		hit := ""
		for _, it := range items {
			if r.m.matches(it) {
				hit = it
				break
			}
		}
		if hit == "" {
			continue
		}
		if r.kind == "like" {
			if likes < 3 {
				likes += 1.5
				s += 1.5
			}
			pros = append(pros, fmt.Sprintf("%s likes %s", r.m.person, r.m.allergy))
		} else {
			s -= 3
			cons = append(cons, fmt.Sprintf("%s dislikes %s", r.m.person, r.m.allergy))
		}
	}
	return round1(s), rd, pros, cons
}

// reasonLine is the one-line reason: the pros, then "but" the catches.
func reasonLine(pros, cons []string) string {
	p := strings.Join(pros, "; ")
	c := strings.Join(cons, "; ")
	switch {
	case p != "" && c != "":
		return capitalize(p) + "; but " + c + "."
	case p != "":
		return capitalize(p) + "."
	case c != "":
		return "Safe for everyone eating, but " + c + "."
	}
	return "Safe for everyone eating."
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ---------------------------------------------------------------------------
// The tool
// ---------------------------------------------------------------------------

var suggestAliases = map[string]string{
	"day": "date", "when": "date", "meal_type": "slot", "meal": "slot",
	"people": "person_ids", "person_id": "person_ids", "persons": "person_ids", "who": "person_ids",
	"eaters": "person_ids", "eating": "person_ids", "guests": "person_ids", "for": "person_ids",
	"max_time": "max_minutes", "total_minutes": "max_minutes", "minutes": "max_minutes", "within_minutes": "max_minutes",
	"tags": "tag", "limit": "count", "n": "count", "number": "count", "how_many": "count",
}

var suggestExample = map[string]any{"date": "today", "slot": "dinner", "max_minutes": 45}

var everyoneWords = map[string]bool{"everyone": true, "everybody": true, "all": true, "family": true, "the family": true,
	"whole family": true, "the whole family": true, "us": true, "all of us": true}

var selfWords = map[string]bool{"me": true, "myself": true, "i": true, "self": true}

// pickEaters resolves person_ids against the family's people: a people id,
// a login id, an exact name or a unique first name; "me" is the caller.
func pickEaters(people []eater, refs []string, self uuid.UUID) ([]eater, *mcp.CallToolResult) {
	var out []eater
	add := func(e eater) {
		if !slices.ContainsFunc(out, func(x eater) bool { return x.id == e.id }) {
			out = append(out, e)
		}
	}
	allNames := func() string {
		ns := make([]string, len(people))
		for i, p := range people {
			ns[i] = p.name
		}
		return strings.Join(ns, ", ")
	}
	var split []string
	for _, r := range refs {
		split = append(split, regexp.MustCompile(`(?i)\s+(?:and|&)\s+`).Split(r, -1)...)
	}
	for _, ref := range split {
		ref = strings.TrimSpace(ref)
		l := strings.ToLower(ref)
		if ref == "" {
			continue
		}
		if everyoneWords[l] {
			return people, nil
		}
		if selfWords[l] {
			i := slices.IndexFunc(people, func(p eater) bool { return self != uuid.Nil && p.userID == self.String() })
			if i < 0 {
				return nil, respond.Invalid("person_ids", "Your login is not linked to a person in this family.",
					"Pass people by name or person_id from person_find.", suggestExample)
			}
			add(people[i])
			continue
		}
		var hits []eater
		for _, p := range people {
			if p.id == l || p.userID == l || strings.EqualFold(strings.TrimSpace(p.name), ref) {
				hits = append(hits, p)
			}
		}
		if len(hits) == 0 {
			for _, p := range people {
				if f := strings.Fields(p.name); len(f) > 0 && strings.EqualFold(f[0], ref) {
					hits = append(hits, p)
				}
			}
		}
		switch len(hits) {
		case 1:
			add(hits[0])
		case 0:
			return nil, respond.Invalid("person_ids", fmt.Sprintf("No person %q in this family (people: %s).", ref, allNames()),
				"Pass names or person_ids from person_find, or leave person_ids out for everyone.", suggestExample)
		default:
			var labels []string
			for _, h := range hits {
				labels = append(labels, fmt.Sprintf("%s (%s)", h.name, h.id))
			}
			return nil, respond.Invalid("person_ids", fmt.Sprintf("%d people match %q: %s.", len(hits), ref, strings.Join(labels, ", ")),
				"Pass the person_id of the one you mean.", suggestExample)
		}
	}
	if len(out) == 0 {
		return people, nil
	}
	return out, nil
}

// NewMealSuggestTool creates meal_suggest (catalog v2, class R).
func NewMealSuggestTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("meal_suggest", append([]mcp.ToolOption{
		mcp.WithDescription(`"What's for dinner?" Ranks saved recipes by ratings and recent meals, never one with an allergen of anyone eating, with a reason each.`),
		mcp.WithString("date", mcp.Description("Day: YYYY-MM-DD or today, tomorrow, friday (default today)")),
		mcp.WithString("slot", mcp.Enum(slots...), mcp.Description("Which meal (default dinner)")),
		mcp.WithArray("person_ids", mcp.Description("Who is eating: names or person_ids (default everyone)"), mcp.WithStringItems()),
		mcp.WithNumber("max_minutes", mcp.Min(1), mcp.Description("Only recipes ready in this many minutes, e.g. 30")),
		mcp.WithString("tag", mcp.Description("Only recipes with this tag, e.g. vegetarian")),
		mcp.WithNumber("count", mcp.Min(1), mcp.Max(maxSuggestions), mcp.Description("How many ideas (default 5, max 10)")),
		respond.Schema[mealSuggestResult](),
	}, annotations("Suggest meals", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, bad := callerOf(ctx, pool)
		if bad != nil {
			return bad, nil
		}
		if !canRead(c.role) {
			return cannotRead(c.role), nil
		}
		limited := c.role == "housesitter"
		loc := familyLocation(c.tz)
		today := familyToday(c.tz)
		args := withAliases(req.GetArguments(), suggestAliases)

		date := today
		if raw, err := optionalString(args, "date"); err != nil {
			return invalid(err, suggestExample), nil
		} else if raw != nil {
			d, ok := resolveDay(*raw, today, loc, true)
			if !ok {
				return respond.Invalid("date", fmt.Sprintf("date %q is not a date.", *raw),
					fmt.Sprintf("Pass date as YYYY-MM-DD or a word like tomorrow (family timezone %s), or leave it out for today.", loc), suggestExample), nil
			}
			if days := int(math.Round(d.Sub(today).Hours() / 24)); days > maxPlanDays || days < -maxPlanDays {
				return respond.Invalid("date", fmt.Sprintf("date %s is more than a year from today.", d.Format(time.DateOnly)),
					"Pass a date within a year, or leave it out for today.", suggestExample), nil
			}
			date = d
		}
		slot := "dinner"
		if s, err := optionalString(args, "slot"); err != nil {
			return invalid(err, suggestExample), nil
		} else if s != nil {
			v, ok := parseSlot(*s)
			if !ok {
				return respond.Invalid("slot", fmt.Sprintf("slot %q is not a meal slot.", *s), "Pass slot as breakfast, lunch, dinner or snack.", suggestExample), nil
			}
			slot = v
		}
		maxMin, err := readNum(args, "max_minutes", 1, maxMinutes)
		if err != nil {
			return invalid(err, suggestExample), nil
		}
		count := defaultSuggestions
		if n, err := readNum(args, "count", 1, maxSuggestions); err != nil {
			return invalid(err, suggestExample), nil
		} else if n.set {
			count = n.val
		}
		tagsIn, _, _, err := readStrings(args, "tag")
		if err != nil {
			return invalid(badArg("tag", "tag must be text.", "Pass one tag, e.g. vegetarian."), suggestExample), nil
		}
		tags, err := normTags(tagsIn)
		if err != nil {
			return invalid(badArg("tag", err.Error(), "Pass one tag, e.g. vegetarian."), suggestExample), nil
		}
		refs, _, _, err := readStrings(args, "person_ids")
		if err != nil {
			return invalid(badArg("person_ids", "person_ids must be a list of names or ids.", `Pass person_ids as a list, e.g. ["Nora","Sam"].`), suggestExample), nil
		}

		// Everyone in the family, with their allergies.
		rows, err := pool.Query(ctx, `SELECT id::text, coalesce(user_id::text, ''), display_name, allergies FROM people
		                               WHERE household_id = $1 ORDER BY lower(display_name), id`, c.householdID)
		if err != nil {
			return respond.Failed("suggest meals", err), nil
		}
		people, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (eater, error) {
			var e eater
			return e, r.Scan(&e.id, &e.userID, &e.name, &e.allergies)
		})
		if err != nil {
			return respond.Failed("suggest meals", err), nil
		}
		eaters, bad := pickEaters(people, refs, c.userID)
		if bad != nil {
			return bad, nil
		}
		rules, checks, err := eaterRules(ctx, pool, c.householdID, eaters, limited)
		if err != nil {
			return respond.Failed("suggest meals", err), nil
		}
		// Allergies of people not eating: flagged on a suggestion, never hidden.
		// Same loader as the eaters' rules, so a preference-filed allergy counts
		// here too (#4803).
		var others []rule
		var otherIDs []string
		for _, p := range people {
			if !slices.ContainsFunc(eaters, func(e eater) bool { return e.id == p.id }) {
				otherIDs = append(otherIDs, p.id)
			}
		}
		if len(otherIDs) > 0 {
			otherMatchers, _, err := allergySources(ctx, pool, c.householdID, otherIDs)
			if err != nil {
				return respond.Failed("suggest meals", err), nil
			}
			for _, m := range otherMatchers {
				others = append(others, rule{kind: "allergy", m: m})
			}
		}

		cands, err := loadCandidates(ctx, pool, c.householdID, date, slot, today)
		if err != nil {
			return respond.Failed("suggest meals", err), nil
		}
		var planned *string
		var title string
		switch err := pool.QueryRow(ctx, `SELECT title FROM meal_plans WHERE household_id = $1 AND date = $2 AND slot = $3`,
			c.householdID, date.Format(time.DateOnly), slot).Scan(&title); {
		case err == nil:
			planned = &title
		case !errors.Is(err, pgx.ErrNoRows):
			return respond.Failed("suggest meals", err), nil
		}

		res := mealSuggestResult{
			LocalDate: date.Format(time.DateOnly), Weekday: date.Weekday().String(), Slot: slot,
			Suggestions: []mealSuggestion{}, Excluded: []excludedRecipe{}, AlreadyPlanned: planned,
			Checks: checks, Limited: limited, Timezone: loc.String(),
		}
		for _, e := range eaters {
			res.Eating = append(res.Eating, e.name)
		}
		if res.Eating == nil {
			res.Eating = []string{}
		}
		// hasAllergy is the fail-closed trigger: an unknown recipe is not a safe
		// recipe, so a recipe with no ingredients saved is left out when anyone
		// eating has an allergy (manager decision on #4803). A diet alone keeps
		// the old behaviour: suggested, ranked lower, with a caution.
		hasAllergy := slices.ContainsFunc(rules, func(r rule) bool { return r.kind == "allergy" })
		hasHardRules := slices.ContainsFunc(rules, func(r rule) bool { return r.kind == "allergy" || r.kind == "diet" })

		for _, cd := range cands {
			total := totalMinutes(cd.prep, cd.cook)
			if maxMin.set && (total == nil || *total > maxMin.val) {
				res.FilteredOut++
				continue
			}
			if len(tags) > 0 && !slices.ContainsFunc(tags, func(t string) bool { return slices.Contains(cd.tags, t) }) {
				res.FilteredOut++
				continue
			}
			// Hard rules: allergies and diets of anyone eating. Each rule
			// reports its first hit, an ingredient before the name. checkItems
			// is the same list the recipe and meal warnings use (#4803).
			items := checkItems(cd.name, cd.ingredients)
			var why []string
			for _, r := range rules {
				if r.kind != "allergy" && r.kind != "diet" {
					continue
				}
				for _, it := range items {
					if r.m.matches(it) {
						line := fmt.Sprintf("%s (%s): %s", r.m.person, r.m.allergy, it)
						if r.kind == "diet" {
							line = fmt.Sprintf("%s (diet: %s): %s", r.m.person, r.m.allergy, it)
						}
						if !slices.Contains(why, line) {
							why = append(why, line)
						}
						break
					}
				}
			}
			// No ingredients saved and someone eating has an allergy: the name
			// was all that could be checked, so it is not a safe recipe. The
			// reason is the same line the recipe and meal warnings report.
			if len(why) == 0 && len(cd.ingredients) == 0 && hasAllergy {
				why = append(why, noIngredientsLine)
			}
			if len(why) > 0 {
				res.ExcludedTotal++
				if len(res.Excluded) < maxExcludedShown {
					res.Excluded = append(res.Excluded, excludedRecipe{RecipeID: cd.id, Name: cd.name, Reason: strings.Join(why, "; ")})
				}
				continue
			}
			sc, rd, pros, cons := cd.score(date, eaters, rules)
			sg := mealSuggestion{RecipeID: cd.id, Name: cd.name, TotalMinutes: total, Tags: cd.tags, Rating: rd,
				TimesMade: cd.timesMade, LastMade: day(cd.lastBefore), Score: sc}
			if len(cd.ingredients) == 0 && hasHardRules {
				sg.Score = round1(sg.Score - 2)
				sg.Cautions = append(sg.Cautions, "No ingredients are saved, so allergies and diets could only be checked against the name; confirm it is safe.")
			}
			for _, r := range others {
				for _, it := range items {
					if r.m.matches(it) {
						sg.Cautions = append(sg.Cautions, fmt.Sprintf("Contains %s for %s (%s), who is not eating.", it, r.m.person, r.m.allergy))
						break
					}
				}
			}
			sg.Reason = reasonLine(pros, cons)
			res.Suggestions = append(res.Suggestions, sg)
		}
		sort.SliceStable(res.Suggestions, func(i, j int) bool {
			a, b := res.Suggestions[i], res.Suggestions[j]
			if a.Score != b.Score {
				return a.Score > b.Score
			}
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		})
		if len(res.Suggestions) > count {
			res.Suggestions = res.Suggestions[:count]
		}
		for i := range res.Suggestions {
			res.Suggestions[i].Rank = i + 1
		}
		if !limited && len(eaters) < len(people) && len(others) > 0 {
			res.Checks = append(res.Checks, "Only the allergies of the people eating were applied; tell me if anyone else joins.")
		}
		if limited {
			res.Checks = append(res.Checks, "Likes, dislikes and diets are not shared with the housesitter role; allergies were still checked.")
		}
		return respond.OK(suggestSummary(res, len(cands)), res, suggestHints(res, len(cands), canWrite(c.role))...), nil
	}
	return "meal_suggest", tool, handler
}

func suggestSummary(res mealSuggestResult, recipes int) string {
	when := fmt.Sprintf("%s on %s", res.Slot, describeDay(mustDate(res.LocalDate)))
	var b strings.Builder
	switch {
	case recipes == 0:
		b.WriteString(fmt.Sprintf("No saved recipes to suggest from for %s.", when))
	case len(res.Suggestions) == 0:
		b.WriteString(fmt.Sprintf("No recipe fits %s", when))
		var why []string
		if res.ExcludedTotal > 0 {
			why = append(why, fmt.Sprintf("%s left out for allergies or diets", plural(res.ExcludedTotal, "recipe")))
		}
		if res.FilteredOut > 0 {
			why = append(why, fmt.Sprintf("%d by time or tag", res.FilteredOut))
		}
		if len(why) > 0 {
			b.WriteString(" (" + strings.Join(why, ", ") + ")")
		}
		b.WriteString(".")
	default:
		b.WriteString(fmt.Sprintf("Ideas for %s", when))
		if len(res.Eating) > 0 {
			b.WriteString(" (eating: " + joinNames(res.Eating) + ")")
		}
		b.WriteString(":")
		for _, s := range res.Suggestions {
			b.WriteString(fmt.Sprintf(" %d. %s", s.Rank, s.Name))
			if s.TotalMinutes != nil {
				b.WriteString(fmt.Sprintf(" (%d min)", *s.TotalMinutes))
			}
			b.WriteString(": " + strings.TrimSuffix(s.Reason, ".") + ".")
			for _, c := range s.Cautions {
				b.WriteString(" Caution: " + c)
			}
		}
	}
	if res.ExcludedTotal > 0 && len(res.Suggestions) > 0 {
		var ex []string
		for _, e := range res.Excluded {
			if len(ex) == 3 {
				break
			}
			ex = append(ex, fmt.Sprintf("%s [%s]", e.Name, e.Reason))
		}
		more := ""
		if res.ExcludedTotal > len(ex) {
			more = fmt.Sprintf(" and %d more", res.ExcludedTotal-len(ex))
		}
		b.WriteString(fmt.Sprintf(" Left out for allergies or diets: %s%s.", strings.Join(ex, "; "), more))
	}
	if res.AlreadyPlanned != nil {
		b.WriteString(fmt.Sprintf(" Already planned for that %s: %s.", res.Slot, *res.AlreadyPlanned))
	}
	for _, c := range res.Checks {
		b.WriteString(" Check: " + c)
	}
	return b.String()
}

func mustDate(s string) time.Time {
	d, _ := time.Parse(time.DateOnly, s)
	return d
}

func suggestHints(res mealSuggestResult, recipes int, write bool) []string {
	var out []string
	if len(res.Suggestions) > 0 {
		if write {
			out = append(out, fmt.Sprintf(`When the user picks one, plan it with meal_set meals=[{"date":"%s","slot":"%s","recipe_id":"…"}] using its recipe_id.`, res.LocalDate, res.Slot))
		}
		out = append(out, fmt.Sprintf("See ingredients and steps with recipe_get recipe_id=%s.", res.Suggestions[0].RecipeID))
		return out
	}
	if recipes > 0 && res.FilteredOut > 0 {
		out = append(out, "Call meal_suggest again without max_minutes or tag.")
	}
	if write {
		out = append(out, "Save a recipe with recipe_create name=… ingredients=[…] steps=[…].")
	}
	if len(out) == 0 {
		out = append(out, "Search the family's recipes with recipe_find.")
	}
	return out
}
