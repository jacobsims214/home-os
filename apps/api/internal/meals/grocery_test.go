package meals_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The week plan's grocery list (#4568). These are the acceptance tests the
// Meals page leans on:
//
//   - the PREVIEW writes nothing, so the page can show every row before anyone
//     confirms (manager decision on #4519, comment #13046);
//   - the APPLY is idempotent, so a second tap, a retry after a dropped
//     response, or the assistant adding the same week cannot duplicate 12 rows
//     on a list someone may be working through in store mode right now;
//   - a meal with no recipe is SKIPPED AND NAMED, so a short list is explained
//     rather than looking broken.

type groceryPreviewJSON struct {
	From  string   `json:"from"`
	To    string   `json:"to"`
	Slots []string `json:"slots"`
	List  struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Exists bool   `json:"exists"`
	} `json:"list"`
	Items []struct {
		Text             string   `json:"text"`
		Quantity         *float64 `json:"quantity"`
		Unit             *string  `json:"unit"`
		Section          string   `json:"section"`
		Meals            []string `json:"meals"`
		Note             string   `json:"note"`
		Status           string   `json:"status"`
		ExistingQuantity *float64 `json:"existing_quantity"`
		ExistingUnit     *string  `json:"existing_unit"`
	} `json:"items"`
	Sections []string `json:"sections"`
	Skipped  []struct {
		Date   string `json:"date"`
		Slot   string `json:"slot"`
		Title  string `json:"title"`
		Reason string `json:"reason"`
	} `json:"skipped"`
	Planned     int      `json:"planned"`
	RecipeMeals int      `json:"recipe_meals"`
	Notes       []string `json:"notes"`
	Counts      struct {
		Add         int `json:"add"`
		AlreadyOn   int `json:"already_on"`
		CheckAmount int `json:"check_amount"`
		Reopened    int `json:"reopened"`
	} `json:"counts"`
	Applied bool `json:"applied"`
}

// planWeek plans one dinner per night for a week, plus a free-text night.
func (f *fixture) planWeek(t *testing.T, owner member) {
	t.Helper()
	// Two recipes with overlapping ingredients, so the merge has work to do.
	fajitas := decode[recipeJSON](t, f.do(t, req{method: http.MethodPost, path: "/api/v1/recipes", as: owner, body: map[string]any{
		"title": "Sheet-pan chicken fajitas", "servings": 5,
		"ingredients": []map[string]any{
			{"name": "chicken thighs", "quantity": 1.5, "unit": "lb"},
			{"name": "bell peppers", "quantity": 3.0},
			{"name": "olive oil", "quantity": 3.0, "unit": "tbsp"},
			{"name": "limes", "quantity": 2.0},
		},
	}}))
	curry := decode[recipeJSON](t, f.do(t, req{method: http.MethodPost, path: "/api/v1/recipes", as: owner, body: map[string]any{
		"title": "Thai red curry", "servings": 5,
		"ingredients": []map[string]any{
			{"name": "chicken thighs", "quantity": 2.0, "unit": "lb"},
			{"name": "Lime", "quantity": 2.0},
			{"name": "olive oil", "quantity": 2.0, "unit": "tbsp"},
		},
	}}))

	days := []string{"2026-11-16", "2026-11-17", "2026-11-18", "2026-11-19"}
	for i, d := range days[:2] {
		_ = i
		rec := f.do(t, req{method: http.MethodPut, path: "/api/v1/meal-plans/" + d + "/dinner", as: owner,
			body: map[string]any{"recipe_id": fajitas.ID}})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	for _, d := range days[2:] {
		rec := f.do(t, req{method: http.MethodPut, path: "/api/v1/meal-plans/" + d + "/dinner", as: owner,
			body: map[string]any{"recipe_id": curry.ID}})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	// A free-text night: no recipe, no ingredients, so it is skipped by name.
	rec := f.do(t, req{method: http.MethodPut, path: "/api/v1/meal-plans/2026-11-20/dinner", as: owner,
		body: map[string]any{"title": "Pizza night"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// The preview names what it will add, groups it by store section, and writes
// nothing — including no list, when the family has none yet.
func TestGrocery_PreviewWritesNothing(t *testing.T) {
	f := setup(t)
	owner := f.member(t, f.household(t, "UTC"), "owner")
	f.planWeek(t, owner)

	rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/meal-plans/grocery?from=2026-11-16&to=2026-11-22&slots=dinner", as: owner})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	preview := decode[groceryPreviewJSON](t, rec)

	assert.False(t, preview.Applied, "a preview has not written anything")
	assert.False(t, preview.List.Exists, "no grocery list yet; the preview says so instead of making one")
	assert.Equal(t, 5, preview.Planned)
	assert.Equal(t, 4, preview.RecipeMeals)
	require.Len(t, preview.Skipped, 1)
	assert.Equal(t, "Pizza night", preview.Skipped[0].Title)
	assert.Contains(t, preview.Skipped[0].Reason, "not a recipe")

	// chicken thighs 1.5+1.5+2+2 lb, olive oil 3+3+2+2 tbsp, limes 2+2, peppers 3
	byText := map[string]int{}
	for i, it := range preview.Items {
		byText[strings.ToLower(it.Text)] = i
	}
	require.Contains(t, byText, "chicken thighs")
	thighs := preview.Items[byText["chicken thighs"]]
	require.NotNil(t, thighs.Quantity)
	assert.InDelta(t, 7.0, *thighs.Quantity, 1e-9)
	require.NotNil(t, thighs.Unit)
	assert.Equal(t, "lb", *thighs.Unit)
	// Every meal that contributed is named, so the page can say why the
	// quantity is what it is.
	assert.Len(t, thighs.Meals, 4, "all four dinners are named on the row")
	assert.Contains(t, thighs.Meals[0], "fajitas")
	assert.Contains(t, thighs.Meals[2], "Thai red curry")

	oil := preview.Items[byText["olive oil"]]
	require.NotNil(t, oil.Quantity)
	require.NotNil(t, oil.Unit)
	assert.Equal(t, "tbsp", *oil.Unit)
	assert.InDelta(t, 10.0, *oil.Quantity, 1e-9) // 3+3 tbsp + 2+2 tbsp

	// The full set of sections, in a store's walking order.
	assert.Equal(t, []string{"Produce", "Meat & seafood", "Pantry"}, preview.Sections)

	// Nothing was written: no list exists for this household.
	var n int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM lists WHERE household_id = $1`, owner.householdID).Scan(&n))
	assert.Zero(t, n, "the preview must not create a list")
}

// A second apply adds nothing: 12 items stay 12 items, and the preview says
// "already_on_list" for every row before the second one is even sent.
func TestGrocery_ApplyIsIdempotent(t *testing.T) {
	f := setup(t)
	owner := f.member(t, f.household(t, "UTC"), "owner")
	f.planWeek(t, owner)

	path := "/api/v1/meal-plans/grocery?from=2026-11-16&to=2026-11-22&slots=dinner"
	first := decode[groceryPreviewJSON](t, f.do(t, req{method: http.MethodPost, path: path, as: owner}))
	assert.True(t, first.Applied)
	assert.True(t, first.List.Exists, "the apply started the grocery list")
	assert.Equal(t, len(first.Items), first.Counts.Add)
	assert.Zero(t, first.Counts.AlreadyOn)

	// The same call again.
	second := decode[groceryPreviewJSON](t, f.do(t, req{method: http.MethodPost, path: path, as: owner}))
	assert.Zero(t, second.Counts.Add, "a re-run adds nothing")
	assert.Equal(t, len(second.Items), second.Counts.AlreadyOn)
	for _, it := range second.Items {
		assert.Equal(t, "already_on_list", it.Status, it.Text)
	}

	var rows int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM list_items WHERE list_id = $1`, first.List.ID).Scan(&rows))
	assert.Equal(t, len(first.Items), rows, "the list holds one row per merged ingredient, not two")

	// And the preview agrees with the apply, because it is the same code.
	preview := decode[groceryPreviewJSON](t, f.do(t, req{method: http.MethodGet, path: path, as: owner}))
	assert.False(t, preview.Applied)
	assert.Zero(t, preview.Counts.Add)
	assert.Equal(t, len(preview.Items), preview.Counts.AlreadyOn)
}

// A row already on the list with a DIFFERENT amount is not changed and not
// duplicated: it is reported as check_amount, because only a person knows
// whether the list's 3 lb or the plan's 7 lb is what to buy.
func TestGrocery_DifferentAmountIsAPersonsDecision(t *testing.T) {
	f := setup(t)
	owner := f.member(t, f.household(t, "UTC"), "owner")
	f.planWeek(t, owner)

	// Someone started the grocery list by hand, with a smaller amount than the
	// week's dinners need. This is the case the preview exists for.
	rec := f.do(t, req{method: http.MethodPost, path: "/api/v1/lists", as: owner, body: map[string]any{
		"name": "Groceries", "kind": "grocery",
		"items": []map[string]any{{"text": "Chicken thigh", "quantity": 3.0, "unit": "lb"}},
	}})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var listID string
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT id::text FROM lists WHERE household_id = $1 AND kind = 'grocery'`, owner.householdID).Scan(&listID))

	path := "/api/v1/meal-plans/grocery?from=2026-11-16&to=2026-11-22&slots=dinner"
	preview := decode[groceryPreviewJSON](t, f.do(t, req{method: http.MethodGet, path: path, as: owner}))
	require.NotNil(t, preview.Items)
	assert.Equal(t, 1, preview.Counts.CheckAmount, "chicken thighs is on the list at a different amount")
	var found bool
	for _, it := range preview.Items {
		if it.Text != "Chicken thighs" {
			continue
		}
		found = true
		assert.Equal(t, "check_amount", it.Status)
		require.NotNil(t, it.ExistingQuantity)
		assert.InDelta(t, 3.0, *it.ExistingQuantity, 1e-9, "what the list says")
		require.NotNil(t, it.Quantity)
		assert.InDelta(t, 7.0, *it.Quantity, 1e-9, "what the plan needs")
	}
	require.True(t, found, "the row is still listed, flagged, not hidden")

	// Applying the merge leaves that row alone: still 3 lb, still the person's
	// wording, and no second row beside it.
	applied := decode[groceryPreviewJSON](t, f.do(t, req{method: http.MethodPost, path: path, as: owner}))
	assert.Equal(t, 1, applied.Counts.CheckAmount)
	var txt string
	var qty float64
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT text, quantity::float8 FROM list_items WHERE list_id = $1 AND unit = 'lb'`, listID).Scan(&txt, &qty))
	assert.Equal(t, "Chicken thigh", txt)
	assert.InDelta(t, 3.0, qty, 1e-9)
	var rows int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM list_items WHERE list_id = $1 AND lower(text) LIKE 'chicken%'`, listID).Scan(&rows))
	assert.Equal(t, 1, rows, "one row for chicken thighs, not two")
}

// A checked-off item comes back on the list instead of being added beside it.
func TestGrocery_ReopensACheckedItem(t *testing.T) {
	f := setup(t)
	owner := f.member(t, f.household(t, "UTC"), "owner")
	f.planWeek(t, owner)

	path := "/api/v1/meal-plans/grocery?from=2026-11-16&to=2026-11-22&slots=dinner"
	first := decode[groceryPreviewJSON](t, f.do(t, req{method: http.MethodPost, path: path, as: owner}))

	var itemID string
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT id::text FROM list_items WHERE list_id = $1 AND lower(text) = 'limes'`, first.List.ID).Scan(&itemID))
	rec := f.do(t, req{method: http.MethodPost, path: "/api/v1/lists/" + first.List.ID + "/items/check",
		as: owner, body: map[string]any{"item_ids": []string{itemID}}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	second := decode[groceryPreviewJSON](t, f.do(t, req{method: http.MethodPost, path: path, as: owner}))
	assert.Equal(t, 1, second.Counts.Reopened, "the checked lime is back on the list")

	var checked bool
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT checked FROM list_items WHERE id = $1`, itemID).Scan(&checked))
	assert.False(t, checked)

	var rows int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM list_items WHERE list_id = $1 AND lower(text) LIKE 'lime%'`, first.List.ID).Scan(&rows))
	assert.Equal(t, 1, rows, "reopened, not duplicated")
}

// The household's time zone and the range are the caller's to state, and a
// nonsense range is a 400 with a message the page can show.
func TestGrocery_Validation(t *testing.T) {
	f := setup(t)
	owner := f.member(t, f.household(t, "UTC"), "owner")
	f.planWeek(t, owner)

	for name, path := range map[string]string{
		"missing from":   "/api/v1/meal-plans/grocery?to=2026-11-22",
		"missing to":     "/api/v1/meal-plans/grocery?from=2026-11-16",
		"bad date":       "/api/v1/meal-plans/grocery?from=yesterday&to=2026-11-22",
		"reversed range": "/api/v1/meal-plans/grocery?from=2026-11-22&to=2026-11-16",
		"too long":       "/api/v1/meal-plans/grocery?from=2026-01-01&to=2026-12-31",
		"bad slot":       "/api/v1/meal-plans/grocery?from=2026-11-16&to=2026-11-22&slots=brunch",
	} {
		assert.Equalf(t, http.StatusBadRequest, f.do(t, req{method: http.MethodGet, path: path, as: owner}).Code, name)
	}
}

// The preview is readable by everyone who can see meals (a housesitter needs
// to know what to buy); writing it is the plan's write, so not a housesitter.
func TestGrocery_Roles(t *testing.T) {
	f := setup(t)
	hh := f.household(t, "UTC")
	owner := f.member(t, hh, "owner")
	f.planWeek(t, owner)
	path := "/api/v1/meal-plans/grocery?from=2026-11-16&to=2026-11-22&slots=dinner"

	// Everyone who can see meals may preview it; writing it is the plan's
	// write (family roles), so a housesitter reads but cannot add.
	for _, role := range []string{"family_manager", "family_member", "viewer", "housesitter"} {
		m := f.member(t, hh, role)
		assert.Equalf(t, http.StatusOK, f.do(t, req{method: http.MethodGet, path: path, as: m}).Code,
			"%s may preview the grocery list", role)
		if role == "viewer" || role == "housesitter" {
			assert.Equalf(t, http.StatusForbidden, f.do(t, req{method: http.MethodPost, path: path, as: m}).Code,
				"%s may not write to the family's grocery list", role)
		}
	}
	vendor := f.member(t, hh, "vendor")
	assert.Equal(t, http.StatusForbidden, f.do(t, req{method: http.MethodGet, path: path, as: vendor}).Code)
}

// A week with nothing planned is not an error: the page offers to plan a
// dinner instead of showing a red box.
func TestGrocery_EmptyWeek(t *testing.T) {
	f := setup(t)
	owner := f.member(t, f.household(t, "UTC"), "owner")
	rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/meal-plans/grocery?from=2027-03-01&to=2027-03-07&slots=dinner", as: owner})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	preview := decode[groceryPreviewJSON](t, rec)
	assert.Empty(t, preview.Items)
	assert.Empty(t, preview.Skipped)
	assert.Zero(t, preview.Planned)
	assert.False(t, preview.List.Exists)
}
