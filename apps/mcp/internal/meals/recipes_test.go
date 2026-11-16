package meals

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type env struct {
	OK      bool            `json:"ok"`
	Summary string          `json:"summary"`
	Data    json.RawMessage `json:"data"`
	Items   json.RawMessage `json:"items"`
	Total   *int            `json:"total"`
	Next    []string        `json:"next"`
	Cursor  string          `json:"next_cursor"`
	Error   *struct {
		Message string `json:"message"`
		Fix     string `json:"fix"`
		Field   string `json:"field"`
	} `json:"error"`
}

func (e env) recipe(t *testing.T) recipeData {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d recipeData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return d
}

func (e env) items(t *testing.T) []recipeItem {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var out []recipeItem
	if err := json.Unmarshal(e.Items, &out); err != nil {
		t.Fatalf("decode items: %v", err)
	}
	return out
}

func (e env) wantError(t *testing.T, field, contains string) {
	t.Helper()
	if e.OK || e.Error == nil {
		t.Fatalf("want isError on %q, got success: %s", field, e.Summary)
	}
	if e.Error.Field != field {
		t.Fatalf("error field = %q, want %q (%s)", e.Error.Field, field, e.Error.Message)
	}
	if e.Error.Fix == "" {
		t.Errorf("error lacks a fix: %+v", e.Error)
	}
	if contains != "" && !strings.Contains(e.Error.Message, contains) {
		t.Errorf("error %q does not mention %q", e.Error.Message, contains)
	}
}

type tool struct {
	def mcp.Tool
	h   toolHandler
}

type tools struct{ find, get, create, update, del tool }

func newTools(pool *pgxpool.Pool) tools {
	mk := func(_ string, d mcp.Tool, h toolHandler) tool { return tool{d, h} }
	return tools{
		find: mk(NewRecipeFindTool(pool)), get: mk(NewRecipeGetTool(pool)), create: mk(NewRecipeCreateTool(pool)),
		update: mk(NewRecipeUpdateTool(pool)), del: mk(NewRecipeDeleteTool(pool)),
	}
}

func resolvedSchema(t *testing.T, tl mcp.Tool) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(tl)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || len(raw.OutputSchema) == 0 {
		t.Fatalf("%s declares no outputSchema (%v)", tl.Name, err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw.OutputSchema, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve outputSchema: %v", err)
	}
	return rs
}

// run calls the tool and checks the respond invariants: one text block equal
// to structuredContent, valid against the outputSchema, ok == !isError.
func (w tool) run(t *testing.T, ctx context.Context, args map[string]any) env {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = w.def.Name
	// Round-trip through JSON, as the transport does (ints become float64).
	b, _ := json.Marshal(args)
	var wire map[string]any
	_ = json.Unmarshal(b, &wire)
	req.Params.Arguments = wire
	res, err := w.h(ctx, req)
	if err != nil {
		t.Fatalf("%s returned a Go error: %v", w.def.Name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("want 1 content item, got %d", len(res.Content))
	}
	text := res.Content[0].(mcp.TextContent).Text
	sc, err := json.Marshal(res.StructuredContent)
	if err != nil || string(sc) != text {
		t.Fatalf("text differs from structuredContent (%v)", err)
	}
	var instance map[string]any
	if err := json.Unmarshal(sc, &instance); err != nil {
		t.Fatal(err)
	}
	if err := resolvedSchema(t, w.def).Validate(instance); err != nil {
		t.Fatalf("%s result does not match its outputSchema: %v\n%s", w.def.Name, err, text)
	}
	var e env
	if err := json.Unmarshal(sc, &e); err != nil {
		t.Fatal(err)
	}
	if e.OK == res.IsError {
		t.Fatalf("ok=%v but isError=%v", e.OK, res.IsError)
	}
	return e
}

func memberCtx(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) (context.Context, uuid.UUID) {
	t.Helper()
	bg := context.Background()
	email := role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(bg, `INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`, email, role).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hh.HouseholdID, uid, role); err != nil {
		t.Fatal(err)
	}
	return server.ContextWithClaims(bg, &auth.Claims{
		Identity: auth.Identity{Subject: uid.String(), Email: email}, UserID: uid.String(),
		HouseholdID: hh.HouseholdID.String(), Role: role,
	}), uid
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v\n%s", err, sql)
	}
	return n
}

func names(items []recipeItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Name
	}
	return out
}

var tacos = map[string]any{
	"name": "Chicken Tacos", "servings": 4, "total_minutes": 30, "tags": []any{"Dinner", "quick", "dinner"},
	"ingredients": []any{
		map[string]any{"item": "chicken thighs", "quantity": 1.5, "unit": "pounds"},
		map[string]any{"item": "corn tortillas", "quantity": 8},
		map[string]any{"item": "tomatoes", "quantity": "2", "section": "Salsa"},
		map[string]any{"item": "lime", "quantity": "1/2", "section": "Salsa"},
		"2 tablespoons olive oil",
		map[string]any{"item": "salt", "quantity": "a pinch"},
	},
	"steps": []any{"1. Season the chicken.", "Step 2: Grill it.\nSlice thinly.", "Warm the tortillas."},
}

// ---------------------------------------------------------------------------
// Definitions and pure helpers (no database)
// ---------------------------------------------------------------------------

// TestToolDefinitions pins the tools to the catalog: names, required params,
// descriptions (<= 200 chars, params <= 80), all four hints, outputSchema.
func TestToolDefinitions(t *testing.T) {
	ts := newTools(nil)
	for _, tc := range []struct {
		w                          tool
		name                       string
		required, params           []string
		readOnly, destr, idempoten bool
	}{
		{ts.find, "recipe_find", nil, []string{"query", "tag", "ingredient", "max_minutes", "limit", "cursor", "id"}, true, false, true},
		{ts.get, "recipe_get", []string{"recipe_id"}, nil, true, false, true},
		{ts.create, "recipe_create", []string{"name", "ingredients", "steps"}, []string{"servings", "total_minutes", "tags", "source_url", "notes"}, false, false, false},
		{ts.update, "recipe_update", []string{"recipe_id"}, []string{"name", "ingredients", "steps", "servings", "total_minutes", "tags", "source_url", "notes"}, false, false, true},
		{ts.del, "recipe_delete", []string{"recipe_id"}, nil, false, true, true},
	} {
		d := tc.w.def
		if d.Name != tc.name {
			t.Errorf("name %q, want %q", d.Name, tc.name)
		}
		if n := utf8.RuneCountInString(d.Description); n == 0 || n > 200 {
			t.Errorf("%s description has %d chars", d.Name, n)
		}
		req := slices.Clone(d.InputSchema.Required)
		slices.Sort(req)
		want := slices.Clone(tc.required)
		slices.Sort(want)
		if !slices.Equal(req, want) {
			t.Errorf("%s required = %v, want %v", d.Name, req, want)
		}
		for _, p := range append(slices.Clone(tc.required), tc.params...) {
			prop, ok := d.InputSchema.Properties[p].(map[string]any)
			if !ok {
				t.Errorf("%s lacks param %s", d.Name, p)
				continue
			}
			if desc, _ := prop["description"].(string); desc == "" || utf8.RuneCountInString(desc) > 80 {
				t.Errorf("%s.%s description %q (want 1-80 chars)", d.Name, p, desc)
			}
		}
		a := d.Annotations
		if a.Title == "" || a.ReadOnlyHint == nil || *a.ReadOnlyHint != tc.readOnly || a.DestructiveHint == nil || *a.DestructiveHint != tc.destr ||
			a.IdempotentHint == nil || *a.IdempotentHint != tc.idempoten || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s hints = %+v", d.Name, a)
		}
		resolvedSchema(t, d)
	}
}

func TestParseAmountAndLines(t *testing.T) {
	for in, want := range map[string]float64{"2": 2, "1.5": 1.5, "1/2": 0.5, "1 1/2": 1.5, "½": 0.5, "1½": 1.5, "1/3": 0.333} {
		if got, ok := parseAmount(in); !ok || got != want {
			t.Errorf("parseAmount(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"a pinch", "1-2", "", "1/0", "x"} {
		if got, ok := parseAmount(in); ok {
			t.Errorf("parseAmount(%q) = %v, want not a number", in, got)
		}
	}
	for _, tc := range []struct {
		in, name, unit string
		qty            any
		note           string
	}{
		{"2 cups flour, sifted", "flour", "cup", 2.0, "sifted"},
		{"1 1/2 tbsp. olive oil", "olive oil", "tbsp", 1.5, ""},
		{"3 cloves of garlic", "garlic", "clove", 3.0, ""},
		{"½ cup milk", "milk", "cup", 0.5, ""},
		{"4 eggs", "eggs", "", 4.0, ""},
		{"1 T butter", "butter", "tbsp", 1.0, ""},
		{"Salt to taste", "Salt to taste", "", nil, ""},
		{"- 8 oz cream cheese", "cream cheese", "oz", 8.0, ""},
	} {
		got := parseLine(tc.in)
		unit := ""
		if got.Unit != nil {
			unit = *got.Unit
		}
		note := ""
		if got.Note != nil {
			note = *got.Note
		}
		if got.Name != tc.name || unit != tc.unit || got.Quantity != tc.qty || note != tc.note {
			t.Errorf("parseLine(%q) = %+v (unit %q note %q)", tc.in, got, unit, note)
		}
	}
}

func TestParseSteps(t *testing.T) {
	got, err := parseSteps("1. Boil water.\n\n2) Add pasta.\nStep 3: Drain.")
	if err != nil || !slices.Equal(got, []string{"Boil water.", "Add pasta.", "Drain."}) {
		t.Errorf("parseSteps = %q, %v", got, err)
	}
	if _, err := parseSteps([]any{"ok", 3}); err == nil {
		t.Error("a number step was accepted")
	}
}

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
		_, goRe, _ := phrasePatterns(tc.phrase)
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
// allergic to peanuts" normalised to "deathly peanuts"; and newAllergyMatcher
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
		if got := allergensIn(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("allergensIn(%q) = %q, want %q", tc.in, got, tc.want)
		}
		ms, unchecked := allergyMatchers("p", "Nora", tc.in)
		if len(unchecked) != 0 {
			t.Errorf("%q: unchecked = %q, want a usable matcher", tc.in, unchecked)
		}
		if !slices.ContainsFunc(ms, func(m allergyMatcher) bool { return m.matches(tc.matches) }) {
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
		ms, unchecked := allergyMatchers("p", "Nora", junk)
		if len(ms) != 0 {
			t.Errorf("allergyMatchers(%q) gave %d matchers, want none (fail closed)", junk, len(ms))
		}
		if len(unchecked) == 0 {
			t.Errorf("allergyMatchers(%q) reported nothing unchecked, want the phrase", junk)
		}
	}

	// The guard itself, straight at newAllergyMatcher with the pre-normalised
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
		if m, ok := newAllergyMatcher("p", "Nora", junk); ok {
			t.Errorf("newAllergyMatcher(%q) = ok, want fail closed (%d own, %d group)", junk, len(m.own), len(m.group))
		}
	}

	// A real allergen the vocabulary does not know is still one word, so it
	// keeps working: fail-closed is about opaque phrases, not unknown foods.
	for _, oneWord := range []string{"strawberry", "kiwi", "mango", "cinnamon"} {
		ms, unchecked := allergyMatchers("p", "Nora", oneWord)
		if len(ms) != 1 || len(unchecked) != 0 {
			t.Errorf("allergyMatchers(%q) = %d matchers / %q unchecked, want 1 and none", oneWord, len(ms), unchecked)
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
		ms, unchecked := allergyMatchers("p", "Nora", tc.in)
		if len(ms) == 0 || len(unchecked) != 0 {
			t.Errorf("%q: %d matchers / unchecked %q, want a matcher", tc.in, len(ms), unchecked)
		}
		if !slices.ContainsFunc(ms, func(m allergyMatcher) bool { return m.matches(tc.matches) }) {
			t.Errorf("%q did not match %q", tc.in, tc.matches)
		}
	}

	// A partly readable entry keeps the half it can read and reports the rest.
	ms, unchecked := allergyMatchers("p", "Nora", "peanuts, red dye 40")
	if len(ms) != 1 || !slices.Equal(unchecked, []string{"red dye 40"}) {
		t.Errorf("mixed entry = %d matchers / unchecked %q, want 1 and [red dye 40]", len(ms), unchecked)
	}
	if !ms[0].matches("peanut butter") {
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
		if !slices.ContainsFunc(preferenceMatchers("p", "Sam", tc.text), func(m allergyMatcher) bool { return m.matches(tc.item) }) {
			t.Errorf("preferenceMatchers(%q) did not match %q", tc.text, tc.item)
		}
	}
}

// anyMatch is how a stored allergy is used everywhere now: read the text
// with allergensIn, then match each allergen it gives.
func anyMatch(allergy, item string) bool {
	ms, _ := allergyMatchers("p", "Nora", allergy)
	for _, m := range ms {
		if m.matches(item) {
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
// when the whole chain agrees: allergensIn, the group key, the group word and
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
// matching is for allergenGroups and diet words. An allergy's own word keeps
// the whole-word rule, and so do the exceptions, so "nut" cannot start
// matching "doughnut" while its group stays unchanged.
func TestCompoundWordsAreGroupWordsOnly(t *testing.T) {
	// The own word is compiled by phrasePatterns, not groupPatterns.
	if _, ok := groupPatterns("catfish"); !ok {
		t.Fatal("groupPatterns(catfish) gave no regex")
	}
	for _, w := range []string{"fish", "bread", "cheese", "egg", "milk", "butter", "corn"} {
		goRe, ok := groupPatterns(w)
		if !ok {
			t.Fatalf("groupPatterns(%q) gave no regex", w)
		}
		if _, whole, _ := phrasePatterns(w); goRe == whole {
			t.Errorf("%q: groupPatterns did not widen phrasePatterns", w)
		}
	}
	// Not curated: nut (the ticket's explicit exclusion) and every other word.
	for _, w := range []string{"nut", "almond", "walnut", "shrimp", "cream", "cake", "flour", "tahini", "sea bass"} {
		goRe, ok := groupPatterns(w)
		if !ok {
			t.Fatalf("groupPatterns(%q) gave no regex", w)
		}
		if _, whole, _ := phrasePatterns(w); goRe != whole {
			t.Errorf("%q got a compound form (%q); only compoundWords may", w, goRe)
		}
	}
	// Every curated word must still be a group word somewhere, or the pattern
	// is dead weight.
	for w := range compoundWords {
		found := false
		for _, members := range allergenGroups {
			for _, m := range members {
				if singular(strings.ToLower(m)) == w {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("compoundWords has %q, which no allergen group lists", w)
		}
	}
}

// TestRecipeFindIngredientFilterUnchanged documents and pins the #4805
// decision about recipe_find: its query and ingredient filters are built with
// phrasePatterns, not groupPatterns, so searching for an ingredient still
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
		pg, goRe, ok := phrasePatterns(tc.phrase)
		if !ok {
			t.Fatalf("%q gave no pattern", tc.phrase)
		}
		if got := mustMatch(goRe, tc.text); got != tc.want {
			t.Errorf("%s: phrasePatterns(%q) vs %q = %v, want %v", tc.why, tc.phrase, tc.text, got, tc.want)
		}
		// The Postgres form is what recipe_find actually sends; it has the
		// same shape, so the two agree.
		if !strings.HasPrefix(pg, `\m`) || !strings.HasSuffix(pg, `\M`) || strings.Contains(pg, `\w*`) {
			t.Errorf("phrasePatterns(%q) Postgres form = %q, want a plain \\m...\\M", tc.phrase, pg)
		}
	}
	// A group word compiled for matching is NOT what the filter uses: this is
	// the assertion that keeps a future edit from pointing both at one helper.
	if goRe, _ := groupPatterns("fish"); goRe == func() string { _, r, _ := phrasePatterns("fish"); return r }() {
		t.Error("groupPatterns(fish) and phrasePatterns(fish) agree; the filter and the warnings would be the same pattern")
	}
}

// TestAllergenGroupVocabulary is #4804's acceptance test: every synonym and
// every group member the ticket named matches the ingredient it named. The
// data is what changed, so there is a row per pair: a shortened group shows
// up here as one failing row naming the word that went missing, not as a
// vague count.
//
// It goes through anyMatch, the path a stored allergy takes (allergensIn then
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
		// Exceptions are for the group words only: an allergy's own word still
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

// TestDietUsesWidenedGroups is the other half of #4804: dietRules reuses
// allergenGroups through the same newAllergyMatcher, so "gluten-free",
// "dairy-free" and "no seafood" inherit the wider vocabulary. Without this a
// celiac's allergy would flag penne while their own diet let it through.
func TestDietUsesWidenedGroups(t *testing.T) {
	for _, tc := range []struct {
		diet, item string
		want       bool
	}{
		{"gluten-free", "penne", true},
		{"gluten-free", "naan", true},
		{"gluten-free", "root beer", false},
		{"gluten free", "puff pastry", true},
		{"dairy free", "mascarpone", true},
		{"dairy-free", "peanut butter", false},
		{"dairy free", "paneer", true},
		{"no seafood", "squid", true},
		{"no seafood", "salmon", true},
		{"no seafood", "chicken", false},
		{"no shellfish", "calamari", true},
		{"no soya", "soybeans", true},
		{"no sesame seeds", "tahini", true},
		{"no gluten", "beer", true},
		{"no gluten", "ginger beer", false},
		// #4805: diets go through compileGroupPhrases too, so the compound
		// forms reach them. A vegan offered catfish is the same bug.
		{"vegan", "catfish fillets", true},
		{"vegetarian", "swordfish steaks", true},
		{"gluten-free", "cornbread", true},
		{"dairy-free", "cheesecake", true},
		{"dairy free", "peanut butter", false},
		{"egg-free", "eggnog", true},
		{"egg free", "eggplant", false},
		{"corn-free", "cornbread", true},
		{"no fish", "catfish fillets", true},
	} {
		rules, ok := dietRules(eater{id: "p", name: "Nora"}, tc.diet)
		if !ok {
			t.Fatalf("diet %q was not applied", tc.diet)
		}
		got := false
		for _, r := range rules {
			if r.m.matches(tc.item) {
				got = true
			}
		}
		if got != tc.want {
			t.Errorf("diet %q vs %q = %v, want %v", tc.diet, tc.item, got, tc.want)
		}
	}
}

// TestAllergenAliasGroups pins the mapping itself, so a future edit that
// drops a group from an alias is visible even if the ingredient lists happen
// to overlap. It also keeps the groups honest: an alias must point at a real
// group, and every group must be reachable by its own key.
func TestAllergenAliasGroups(t *testing.T) {
	for key := range allergenGroups {
		if len(compilePhrases(allergenGroups[key])) == 0 {
			t.Errorf("group %q has no usable ingredient", key)
		}
	}
	for alias, groups := range allergenAliases {
		for _, g := range groups {
			if _, ok := allergenGroups[g]; !ok {
				t.Errorf("alias %q points at %q, which is not a group", alias, g)
			}
		}
	}
	// The synonyms the ticket named, by the keys allergensIn produces.
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
		for _, a := range allergensIn(tc.allergy) {
			got = append(got, allergyGroupKeys(a)...)
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
	ms, unchecked := allergyMatchers("p", "Nora", "peanuts and tree nuts")
	if len(unchecked) != 0 {
		t.Errorf("unchecked = %q, want none", unchecked)
	}
	if len(ms) != 2 {
		t.Fatalf("matchers = %+v, want peanut and tree nut", ms)
	}
	var labels []string
	for _, m := range ms {
		labels = append(labels, m.allergy)
	}
	if !slices.Equal(labels, []string{"peanuts", "tree nuts"}) {
		t.Errorf("labels = %q", labels)
	}
	// The label stays the bare allergen, so warnings read as they always did.
	if got := warningLines(AllergyWarnings(ms, []string{"peanut butter", "cashews"})); len(got) != 2 ||
		got[0] != "Nora (peanuts): peanut butter" || got[1] != "Nora (tree nuts): cashews" {
		t.Errorf("warnings = %q", got)
	}
	// Nothing left to match: no matchers, so the caller can say so.
	for _, nothing := range []string{"allergy", "anaphylactic", "  ", "severe", "(carries an EpiPen)"} {
		if ms, _ := allergyMatchers("p", "Nora", nothing); len(ms) != 0 {
			t.Errorf("allergyMatchers(%q) = %+v, want none", nothing, ms)
		}
	}
}

// ---------------------------------------------------------------------------
// Database tests
// ---------------------------------------------------------------------------

// TestRecipeCreateAndFindByIngredient is the ticket's acceptance test: a
// recipe saved with structured ingredients is stored in 047's shape and is
// found by ingredient (singular or plural), name, tag and time.
func TestRecipeCreateAndFindByIngredient(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)

	e := ts.create.run(t, ctx, tacos)
	d := e.recipe(t)
	if d.Name != "Chicken Tacos" || *d.Servings != 4 || *d.TotalMinutes != 30 || *d.CookMinutes != 30 || d.PrepMinutes != nil {
		t.Errorf("created = %+v", d)
	}
	if !slices.Equal(d.Tags, []string{"dinner", "quick"}) {
		t.Errorf("tags = %v", d.Tags)
	}
	if !slices.Equal(d.Steps, []string{"Season the chicken.", "Grill it.", "Slice thinly.", "Warm the tortillas."}) {
		t.Errorf("steps = %q", d.Steps)
	}
	if len(d.Ingredients) != 6 {
		t.Fatalf("ingredients = %+v", d.Ingredients)
	}
	oil := d.Ingredients[4]
	if oil.Item != "olive oil" || oil.Quantity != 2.0 || *oil.Unit != "tbsp" {
		t.Errorf("parsed line = %+v", oil)
	}
	if d.Ingredients[0].Quantity != 1.5 || *d.Ingredients[0].Unit != "lb" || d.Ingredients[3].Quantity != 0.5 || d.Ingredients[5].Quantity != "a pinch" {
		t.Errorf("quantities = %+v", d.Ingredients)
	}
	if !strings.Contains(e.Summary, "Saved recipe Chicken Tacos") {
		t.Errorf("summary %q", e.Summary)
	}

	// 047 shape: name/quantity/unit/section; the API reads it unchanged.
	var stored string
	if err := pool.QueryRow(context.Background(), `SELECT (ingredients->0)::text FROM recipes WHERE id = $1`, d.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var first map[string]any
	_ = json.Unmarshal([]byte(stored), &first)
	if first["name"] != "chicken thighs" || first["quantity"] != 1.5 || first["unit"] != "lb" {
		t.Errorf("stored ingredient = %s", stored)
	}
	if n := count(t, pool, `SELECT count(*) FROM recipes WHERE id = $1 AND steps = E'Season the chicken.\nGrill it.\nSlice thinly.\nWarm the tortillas.'`, d.ID); n != 1 {
		t.Error("steps not stored one per line")
	}

	ts.create.run(t, ctx, map[string]any{"name": "Veggie Pasta", "ingredients": []any{"1 lb pasta", "2 cups tomato sauce", "1 zucchini"},
		"steps": "Boil pasta.\nAdd sauce.", "tags": "vegetarian, dinner", "prep_minutes": 10, "cook_minutes": 50})
	ts.create.run(t, ctx, map[string]any{"name": "Berry Smoothie", "ingredients": []any{map[string]any{"item": "mixed berries", "quantity": 1, "unit": "cup"}, "1 cup milk"},
		"steps": []any{"Blend."}, "total_minutes": 5})

	for _, tc := range []struct {
		args map[string]any
		want []string
	}{
		{map[string]any{"ingredient": "chicken"}, []string{"Chicken Tacos"}},
		{map[string]any{"ingredient": "tomato"}, []string{"Chicken Tacos", "Veggie Pasta"}},   // tomatoes / tomato sauce
		{map[string]any{"ingredient": "tomatoes"}, []string{"Chicken Tacos", "Veggie Pasta"}}, // plural
		{map[string]any{"ingredient": "tomato, pasta"}, []string{"Veggie Pasta"}},             // all of them
		{map[string]any{"ingredient": "berry"}, []string{"Berry Smoothie"}},
		{map[string]any{"ingredient": "beef"}, nil},
		{map[string]any{"query": "tacos"}, []string{"Chicken Tacos"}},
		{map[string]any{"query": "chicken recipe"}, []string{"Chicken Tacos"}},
		{map[string]any{"query": "pasta"}, []string{"Veggie Pasta"}},
		{map[string]any{"tag": "dinner"}, []string{"Chicken Tacos", "Veggie Pasta"}},
		{map[string]any{"tag": "Vegetarian"}, []string{"Veggie Pasta"}},
		{map[string]any{"max_minutes": 30}, []string{"Berry Smoothie", "Chicken Tacos"}},
		{map[string]any{"max_minutes": "45 minutes", "tag": "dinner"}, []string{"Chicken Tacos"}},
		{map[string]any{"name": "smoothie"}, []string{"Berry Smoothie"}}, // alias
		{map[string]any{"query": "100%"}, nil},                           // no wildcard
		{map[string]any{}, []string{"Berry Smoothie", "Chicken Tacos", "Veggie Pasta"}},
	} {
		items := ts.find.run(t, ctx, tc.args).items(t)
		if got := names(items); !slices.Equal(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
			t.Errorf("recipe_find %v = %v, want %v", tc.args, got, tc.want)
		}
	}
	items := ts.find.run(t, ctx, map[string]any{"ingredient": "tomato"}).items(t)
	if !slices.Equal(items[0].Matched, []string{"tomatoes"}) || items[0].IngredientCount != 6 || *items[0].TotalMinutes != 30 {
		t.Errorf("find item = %+v", items[0])
	}

	// Paging.
	e = ts.find.run(t, ctx, map[string]any{"limit": 2})
	if len(e.items(t)) != 2 || *e.Total != 3 || e.Cursor == "" {
		t.Fatalf("page 1 = %+v", e)
	}
	e = ts.find.run(t, ctx, map[string]any{"limit": 2, "cursor": e.Cursor})
	if got := names(e.items(t)); !slices.Equal(got, []string{"Veggie Pasta"}) || e.Cursor != "" {
		t.Errorf("page 2 = %v / %q", got, e.Cursor)
	}
	ts.find.run(t, ctx, map[string]any{"cursor": "nope"}).wantError(t, "cursor", "")
	ts.find.run(t, ctx, map[string]any{"id": "tacos"}).wantError(t, "id", "")
	if got := names(ts.find.run(t, ctx, map[string]any{"id": d.ID}).items(t)); !slices.Equal(got, []string{"Chicken Tacos"}) {
		t.Errorf("find by id = %v", got)
	}

	// recipe_get by exact name, any case.
	g := ts.get.run(t, ctx, map[string]any{"recipe_id": "chicken tacos"}).recipe(t)
	if g.ID != d.ID || len(g.Ingredients) != 6 {
		t.Errorf("get = %+v", g)
	}
	ts.get.run(t, ctx, map[string]any{"recipe_id": "Taco"}).wantError(t, "recipe_id", "Close matches: Chicken Tacos")
	ts.get.run(t, ctx, map[string]any{"recipe_id": "Lasagna"}).wantError(t, "recipe_id", "No recipe named")
	ts.get.run(t, ctx, map[string]any{}).wantError(t, "recipe_id", "required")

	// Another family can't see it.
	otherCtx, _ := testutil.SeedHousehold(t, pool)
	if items := ts.find.run(t, otherCtx, map[string]any{"ingredient": "chicken"}).items(t); len(items) != 0 {
		t.Errorf("other family found %v", names(items))
	}
	if e := ts.get.run(t, otherCtx, map[string]any{"recipe_id": d.ID}); e.OK {
		t.Error("other family read the recipe")
	}
	_ = hh
}

func TestRecipeCreateValidation(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)
	ok := func() map[string]any {
		return map[string]any{"name": "Toast", "ingredients": []any{"1 slice bread"}, "steps": []any{"Toast it."}}
	}
	with := func(k string, v any) map[string]any { m := ok(); m[k] = v; return m }
	without := func(k string) map[string]any { m := ok(); delete(m, k); return m }

	ts.create.run(t, ctx, without("name")).wantError(t, "name", "required")
	ts.create.run(t, ctx, without("ingredients")).wantError(t, "ingredients", "required")
	ts.create.run(t, ctx, without("steps")).wantError(t, "steps", "required")
	ts.create.run(t, ctx, with("ingredients", []any{})).wantError(t, "ingredients", "empty")
	ts.create.run(t, ctx, with("ingredients", []any{map[string]any{"quantity": 2}})).wantError(t, "ingredients", "no item")
	ts.create.run(t, ctx, with("ingredients", []any{map[string]any{"item": "x", "quantity": -1}})).wantError(t, "ingredients", "negative")
	ts.create.run(t, ctx, with("ingredients", []any{map[string]any{"item": "x", "quantity": true}})).wantError(t, "ingredients", "number or text")
	ts.create.run(t, ctx, with("servings", 0)).wantError(t, "servings", "")
	ts.create.run(t, ctx, with("total_minutes", 2.5)).wantError(t, "total_minutes", "")
	ts.create.run(t, ctx, with("source_url", "javascript:alert(1)")).wantError(t, "source_url", "")
	ts.create.run(t, ctx, with("name", strings.Repeat("x", 256))).wantError(t, "name", "")
	m := ok()
	m["prep_minutes"], m["total_minutes"] = 20, 10
	ts.create.run(t, ctx, m).wantError(t, "total_minutes", "less than prep")
	if n := count(t, pool, `SELECT count(*) FROM recipes`); n != 0 {
		t.Errorf("%d recipes saved by invalid calls", n)
	}

	// Aliases: title, instructions, serves, url.
	d := ts.create.run(t, ctx, map[string]any{"title": "Pancakes", "ingredients": "2 cups flour\n2 eggs\n1 1/2 cups milk",
		"instructions": "Mix.\nFry.", "serves": "4", "url": "https://example.com/pancakes", "description": "Sunday"}).recipe(t)
	if d.Name != "Pancakes" || len(d.Ingredients) != 3 || len(d.Steps) != 2 || *d.Servings != 4 || *d.SourceURL != "https://example.com/pancakes" || *d.Notes != "Sunday" {
		t.Errorf("aliased create = %+v", d)
	}
	if d.Ingredients[2].Item != "milk" || d.Ingredients[2].Quantity != 1.5 || *d.Ingredients[2].Unit != "cup" {
		t.Errorf("pasted line = %+v", d.Ingredients[2])
	}
}

func TestRecipeCreateLoopGuard(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)

	first := ts.create.run(t, ctx, tacos).recipe(t)
	again := ts.create.run(t, ctx, map[string]any{"name": "  chicken tacos ", "ingredients": []any{"chicken"}, "steps": []any{"Cook."}})
	d := again.recipe(t)
	if !d.Duplicate || d.ID != first.ID || !strings.Contains(again.Summary, "already saved") {
		t.Errorf("repeat = %+v / %s", d, again.Summary)
	}
	// Parallel identical calls make one row.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var req mcp.CallToolRequest
			req.Params.Arguments = map[string]any{"name": "Soup", "ingredients": []any{"water"}, "steps": []any{"Boil."}}
			_, _ = ts.create.h(ctx, req)
		}()
	}
	wg.Wait()
	if n := count(t, pool, `SELECT count(*) FROM recipes WHERE title = 'Soup'`); n != 1 {
		t.Errorf("parallel creates made %d rows", n)
	}
	// Older than 10 minutes: a new row, with a hint to use ids.
	if _, err := pool.Exec(context.Background(), `UPDATE recipes SET created_at = now() - interval '11 minutes'`); err != nil {
		t.Fatal(err)
	}
	e := ts.create.run(t, ctx, tacos)
	if d := e.recipe(t); d.Duplicate || d.ID == first.ID || !strings.Contains(strings.Join(e.Next, " "), "pass recipe ids") {
		t.Errorf("after window = %+v / %v", d, e.Next)
	}
	ts.get.run(t, ctx, map[string]any{"recipe_id": "Chicken Tacos"}).wantError(t, "recipe_id", "2 recipes are named")
}

func TestRecipeUpdate(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)
	d := ts.create.run(t, ctx, tacos).recipe(t)
	bg := context.Background()
	var before string
	_ = pool.QueryRow(bg, `SELECT updated_at::text FROM recipes WHERE id = $1`, d.ID).Scan(&before)

	// Only the given fields change.
	e := ts.update.run(t, ctx, map[string]any{"recipe_id": "Chicken Tacos", "servings": 6, "notes": "Kids like it mild", "tags": "", "steps": ""})
	u := e.recipe(t)
	if !slices.Equal(u.Changed, []string{"notes", "servings"}) || *u.Servings != 6 || len(u.Ingredients) != 6 || len(u.Steps) != 4 ||
		!slices.Equal(u.Tags, []string{"dinner", "quick"}) {
		t.Errorf("update = %+v", u)
	}
	// ingredients replace the whole list; total_minutes keeps prep.
	u = ts.update.run(t, ctx, map[string]any{"recipe_id": d.ID, "ingredients": []any{"1 lb ground beef", "8 taco shells"}, "prep_minutes": 10, "total_minutes": 25}).recipe(t)
	if len(u.Ingredients) != 2 || u.Ingredients[0].Item != "ground beef" || *u.PrepMinutes != 10 || *u.CookMinutes != 15 || *u.TotalMinutes != 25 {
		t.Errorf("replace = %+v", u)
	}
	if got := names(ts.find.run(t, ctx, map[string]any{"ingredient": "chicken"}).items(t)); len(got) != 0 {
		t.Errorf("old ingredient still found: %v", got)
	}
	// No-op writes nothing.
	var mid string
	_ = pool.QueryRow(bg, `SELECT updated_at::text FROM recipes WHERE id = $1`, d.ID).Scan(&mid)
	e = ts.update.run(t, ctx, map[string]any{"recipe_id": d.ID, "servings": 6, "ingredients": []any{"1 lb ground beef", "8 taco shells"}})
	if u := e.recipe(t); len(u.Changed) != 0 || !strings.HasPrefix(e.Summary, "Nothing changed") {
		t.Errorf("no-op = %+v / %s", u, e.Summary)
	}
	var after string
	_ = pool.QueryRow(bg, `SELECT updated_at::text FROM recipes WHERE id = $1`, d.ID).Scan(&after)
	if after != mid || mid == before {
		t.Errorf("updated_at before %s mid %s after %s", before, mid, after)
	}
	// null clears; name and ingredients can't be cleared.
	u = ts.update.run(t, ctx, map[string]any{"recipe_id": d.ID, "notes": nil, "tags": nil, "total_minutes": nil, "name": "Beef Tacos"}).recipe(t)
	if u.Notes != nil || len(u.Tags) != 0 || u.TotalMinutes != nil || u.Name != "Beef Tacos" {
		t.Errorf("clear = %+v", u)
	}
	ts.update.run(t, ctx, map[string]any{"recipe_id": d.ID, "name": nil}).wantError(t, "name", "cannot be cleared")
	ts.update.run(t, ctx, map[string]any{"recipe_id": d.ID, "ingredients": nil}).wantError(t, "ingredients", "")
	ts.update.run(t, ctx, map[string]any{"recipe_id": d.ID}).wantError(t, "", "No fields")
	ts.update.run(t, ctx, map[string]any{"recipe_id": uuid.NewString(), "servings": 2}).wantError(t, "", "No recipe with id")
	ts.update.run(t, ctx, map[string]any{"recipe_id": "Nope", "servings": 2}).wantError(t, "recipe_id", "")
}

func TestRecipeRatingsAndAllergies(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)
	bg := context.Background()

	var nora, sam string
	if err := pool.QueryRow(bg, `INSERT INTO people (household_id, display_name, allergies) VALUES ($1, 'Nora', ARRAY['peanuts','Dairy']) RETURNING id::text`, hh.HouseholdID).Scan(&nora); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(bg, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Sam') RETURNING id::text`, hh.HouseholdID).Scan(&sam); err != nil {
		t.Fatal(err)
	}
	e := ts.create.run(t, ctx, map[string]any{"name": "Satay Noodles", "ingredients": []any{"200 g rice noodles", "3 tbsp peanut butter", "1 tbsp butter", "1 lime"},
		"steps": []any{"Cook.", "Toss."}})
	d := e.recipe(t)
	// Three hits now: peanut butter and the name ("Satay" is a peanut synonym),
	// plus butter for the dairy allergy (#4803 checks the title too).
	if len(d.AllergyWarnings) != 3 || !strings.Contains(e.Summary, "Allergy warning: Nora (peanuts): peanut butter") {
		t.Fatalf("warnings = %+v / %s", d.AllergyWarnings, e.Summary)
	}
	for _, w := range d.AllergyWarnings {
		if w.PersonID != nora {
			t.Errorf("warning person = %+v", w)
		}
	}
	ts.create.run(t, ctx, map[string]any{"name": "Plain Rice", "ingredients": []any{"1 cup rice"}, "steps": []any{"Cook."}})

	// Meal history and ratings.
	var past, future string
	if err := pool.QueryRow(bg, `INSERT INTO meal_plans (household_id, date, slot, recipe_id) VALUES ($1, current_date - 7, 'dinner', $2) RETURNING id::text`, hh.HouseholdID, d.ID).Scan(&past); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO meal_plans (household_id, date, slot, recipe_id) VALUES ($1, current_date - 1, 'lunch', $2)`, hh.HouseholdID, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(bg, `INSERT INTO meal_plans (household_id, date, slot, recipe_id) VALUES ($1, current_date + 3, 'dinner', $2) RETURNING id::text`, hh.HouseholdID, d.ID).Scan(&future); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO meal_feedback (meal_plan_id, person_id, rating, comment) VALUES ($1, $2, 5, 'Yum'), ($1, $3, 4, NULL)`, past, sam, nora); err != nil {
		t.Fatal(err)
	}

	g := ts.get.run(t, ctx, map[string]any{"recipe_id": d.ID}).recipe(t)
	if g.Rating == nil || g.Rating.Average != 4.5 || g.Rating.Count != 2 || g.TimesMade != 2 || g.LastMade == nil || g.NextPlanned == nil || len(g.Feedback) != 2 {
		t.Fatalf("get stats = %+v", g)
	}
	if g.Feedback[0].Weekday == "" || g.Feedback[0].Slot != "dinner" {
		t.Errorf("feedback = %+v", g.Feedback)
	}
	items := ts.find.run(t, ctx, map[string]any{"query": "noodles"})
	it := items.items(t)
	if len(it) != 1 || it[0].Rating == nil || it[0].Rating.Average != 4.5 || it[0].TimesMade != 2 ||
		!slices.Contains(it[0].AllergyWarnings, "Nora (peanuts): peanut butter") || !strings.Contains(items.Summary, "allergen") {
		t.Errorf("find = %+v / %s", it, items.Summary)
	}
	if it := ts.find.run(t, ctx, map[string]any{"query": "rice"}).items(t); len(it) != 2 || it[0].Name != "Plain Rice" || it[0].AllergyWarnings != nil {
		t.Errorf("rice find = %+v", it)
	}

	// Delete keeps the meal history's title.
	del := ts.del.run(t, ctx, map[string]any{"recipe_id": "satay noodles"})
	dd := del.recipe(t)
	if *dd.PlannedMealsKept != 3 || !strings.Contains(del.Summary, "3 planned meals keep the name") {
		t.Errorf("delete = %+v / %s", dd, del.Summary)
	}
	if n := count(t, pool, `SELECT count(*) FROM meal_plans WHERE household_id = $1 AND recipe_id IS NULL AND title = 'Satay Noodles'`, hh.HouseholdID); n != 3 {
		t.Errorf("%d meals kept the title", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM meal_feedback WHERE meal_plan_id = $1`, past); n != 2 {
		t.Errorf("feedback lost: %d", n)
	}
	_ = future
}

// TestRecipeWarningsForWordedAllergies is #4802's other acceptance test: an
// allergy saved as a sentence ("allergic to peanuts") must produce the same
// recipe and meal warnings a bare allergen does, and one that normalises to
// nothing must be visible in checks instead of silently matching nothing.
func TestRecipeWarningsForWordedAllergies(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)
	bg := context.Background()

	if _, err := pool.Exec(bg, `INSERT INTO people (household_id, display_name, allergies)
		VALUES ($1, 'Nora', ARRAY['allergic to peanuts']), ($1, 'Sam', ARRAY['Shrimp allergy']), ($1, 'Bea', ARRAY['allergy'])`,
		hh.HouseholdID); err != nil {
		t.Fatal(err)
	}
	d := ts.create.run(t, ctx, map[string]any{"name": "Satay Noodles", "ingredients": []any{"200 g rice noodles", "3 tbsp peanut butter"}, "steps": []any{"Cook."}}).recipe(t)

	// recipe_get: the ingredient matches, and the label stays the bare allergen.
	g := ts.get.run(t, ctx, map[string]any{"recipe_id": d.ID}).recipe(t)
	// The ingredient and the name: "Satay" is a peanut synonym, so the title
	// is a second hit for Nora (#4803).
	if len(g.AllergyWarnings) != 2 || g.AllergyWarnings[0].Allergy != "peanuts" || g.AllergyWarnings[0].Ingredient != "peanut butter" ||
		g.AllergyWarnings[1].Ingredient != "Satay Noodles" {
		t.Errorf("get warnings = %+v", g.AllergyWarnings)
	}
	if !slices.Contains(g.Checks, `Bea has an allergy saved as "allergy" that could not be checked against recipes; confirm the meal is safe.`) {
		t.Errorf("get checks = %v", g.Checks)
	}

	// recipe_find: the same hit, and the summary names the unreadable allergy.
	e := ts.find.run(t, ctx, map[string]any{"query": "satay"})
	it := e.items(t)
	if len(it) != 1 || !slices.Contains(it[0].AllergyWarnings, "Nora (peanuts): peanut butter") ||
		!slices.Contains(it[0].AllergyWarnings, "Nora (peanuts): Satay Noodles") {
		t.Errorf("find = %+v", it)
	}
	if !strings.Contains(e.Summary, "contain an allergen") || !strings.Contains(e.Summary, `Bea has an allergy saved as "allergy"`) {
		t.Errorf("find summary = %s", e.Summary)
	}

	// meal_set: planning it warns, and the plan read back (agenda_get) agrees.
	today := familyToday("UTC")
	date := today.Format(time.DateOnly)
	res := newPlanTools(pool).set.run(t, ctx, map[string]any{"date": date, "slot": "dinner", "recipe_id": d.ID}).set(t)
	if len(res.Meals) != 1 || res.Meals[0].Meal == nil ||
		!slices.Contains(res.Meals[0].Meal.AllergyWarnings, "Nora (peanuts): peanut butter") {
		t.Fatalf("meal_set = %+v", res.Meals)
	}
	if !slices.Contains(res.Checks, `Bea has an allergy saved as "allergy" that could not be checked against recipes; confirm the meal is safe.`) {
		t.Errorf("meal_set checks = %v", res.Checks)
	}
	plan, checks, allowed, err := AgendaMeals(ctx, pool, today, today)
	if err != nil || !allowed || len(plan) != 1 {
		t.Fatalf("agenda = %+v %v %v %v", plan, checks, allowed, err)
	}
	if !slices.Contains(plan[0].AllergyWarnings, "Nora (peanuts): peanut butter") ||
		!slices.Contains(plan[0].AllergyWarnings, "Nora (peanuts): Satay Noodles") || len(checks) != 1 {
		t.Errorf("agenda meal = %+v / checks %v", plan[0], checks)
	}

	// Free text too: "peanut satay" is matched by the normalised allergen.
	res = newPlanTools(pool).set.run(t, ctx, map[string]any{"date": date, "slot": "lunch", "text": "Peanut satay skewers"}).set(t)
	if len(res.Meals) != 1 || !slices.Contains(res.Meals[0].Meal.AllergyWarnings, "Nora (peanuts): Peanut satay skewers") {
		t.Errorf("free text = %+v", res.Meals)
	}

	// A worded allergy that names another food does not warn on this one.
	ts.create.run(t, ctx, map[string]any{"name": "Plain Rice", "ingredients": []any{"1 cup rice"}, "steps": []any{"Cook."}})
	rice := ts.get.run(t, ctx, map[string]any{"recipe_id": "Plain Rice"}).recipe(t)
	if len(rice.AllergyWarnings) != 0 {
		t.Errorf("rice warnings = %+v", rice.AllergyWarnings)
	}
}

func TestRecipeDelete(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)
	d := ts.create.run(t, ctx, tacos).recipe(t)
	bg := context.Background()
	if _, err := pool.Exec(bg, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'recipe', $2, 'Tip', 'More lime')`, hh.HouseholdID, d.ID); err != nil {
		t.Fatal(err)
	}
	ts.del.run(t, ctx, map[string]any{"recipe_id": d.ID}).wantError(t, "confirm", "1 note that would be deleted")
	if n := count(t, pool, `SELECT count(*) FROM recipes WHERE id = $1`, d.ID); n != 1 {
		t.Fatal("refused delete removed the recipe")
	}
	dd := ts.del.run(t, ctx, map[string]any{"recipe_id": d.ID, "confirm": true}).recipe(t)
	if *dd.NotesDeleted != 1 || dd.Name != "Chicken Tacos" {
		t.Errorf("delete = %+v", dd)
	}
	ts.del.run(t, ctx, map[string]any{"recipe_id": d.ID}).wantError(t, "", "No recipe with id")
}

func TestRecipeRoles(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ownerCtx, hh := testutil.SeedHousehold(t, pool)
	ts := newTools(pool)
	bg := context.Background()
	r := ts.create.run(t, ownerCtx, tacos).recipe(t)

	for _, role := range []string{"family_manager", "family_member"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		d := ts.create.run(t, ctx, map[string]any{"name": "Dish of " + role, "ingredients": []any{"rice"}, "steps": []any{"Cook."}}).recipe(t)
		ts.update.run(t, ctx, map[string]any{"recipe_id": d.ID, "notes": role}).recipe(t)
		ts.del.run(t, ctx, map[string]any{"recipe_id": d.ID}).recipe(t)
	}
	for _, role := range []string{"viewer", "housesitter"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		if got := names(ts.find.run(t, ctx, map[string]any{}).items(t)); !slices.Equal(got, []string{"Chicken Tacos"}) {
			t.Errorf("%s find = %v", role, got)
		}
		ts.get.run(t, ctx, map[string]any{"recipe_id": r.ID}).recipe(t)
	}
	for _, role := range []string{"viewer", "housesitter", "vendor"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		for _, c := range []struct {
			w    tool
			args map[string]any
		}{
			{ts.create, map[string]any{"name": "Nope", "ingredients": []any{"x"}, "steps": []any{"x"}}},
			{ts.update, map[string]any{"recipe_id": r.ID, "notes": "nope"}},
			{ts.del, map[string]any{"recipe_id": r.ID}},
		} {
			e := c.w.run(t, ctx, c.args)
			if e.OK || !strings.Contains(e.Summary, "The "+role+" role can't change recipes") {
				t.Errorf("%s %s = %+v", role, c.w.def.Name, e)
			}
		}
	}
	vendorCtx, _ := memberCtx(t, pool, hh, "vendor")
	for _, w := range []tool{ts.find, ts.get} {
		if e := w.run(t, vendorCtx, map[string]any{"recipe_id": r.ID}); e.OK || !strings.Contains(e.Summary, "can't see the family's recipes") {
			t.Errorf("vendor %s = %+v", w.def.Name, e)
		}
	}

	// Demoted: claims say family_member, memberships say viewer.
	ctx, uid := memberCtx(t, pool, hh, "family_member")
	if _, err := pool.Exec(bg, `UPDATE memberships SET role = 'viewer' WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if e := ts.update.run(t, ctx, map[string]any{"recipe_id": r.ID, "notes": "nope"}); e.OK {
		t.Error("demoted member updated a recipe")
	}
	if _, err := pool.Exec(bg, `DELETE FROM memberships WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if e := ts.find.run(t, ctx, map[string]any{}); e.OK || !strings.Contains(e.Summary, "not signed in") {
		t.Errorf("removed member = %+v", e)
	}
	if n := count(t, pool, `SELECT count(*) FROM recipes WHERE id = $1 AND description IS NULL`, r.ID); n != 1 {
		t.Error("refused calls changed the recipe")
	}
	// Writes are attributed to the member (050 authorship via the pool).
	if n := count(t, pool, `SELECT count(*) FROM recipes WHERE id = $1 AND created_by = $2`, r.ID, hh.UserID); n != 1 {
		t.Error("recipe not attributed to its creator")
	}
}
