package meals_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/meals"
	"home-os/api/internal/middleware"
)

const testIssuer = "https://dex.test/dex"

type fixture struct {
	pool *pgxpool.Pool
	h    http.Handler
	key  *rsa.PrivateKey
}

// setup connects to TEST_DATABASE_URL (all migrations applied; skips when
// unset) and builds a router like cmd/api/main.go: the meals routes behind
// the real RequireAuth middleware with the Postgres member resolver, so
// X-Household-ID is resolved exactly as in production.
func setup(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{key.Public()}}
	v := auth.NewVerifierWithKeySet(testIssuer, ks, []string{"home-os-ui"}).
		WithResolver(auth.NewMemberResolver(pool))

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		meals.NewHandler(meals.NewRepo(pool)).RegisterRoutes(r)
	})
	return &fixture{pool: pool, h: r, key: key}
}

type member struct {
	token, userID, householdID, personID string
}

func (f *fixture) household(t *testing.T, tz string) string {
	t.Helper()
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`INSERT INTO households (name, timezone) VALUES ('Meals HH', $1) RETURNING id::text`, tz).Scan(&id))
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, id) })
	return id
}

// member creates a user with role in hhID and returns a Dex-style token. The
// memberships trigger (041) creates their person record.
func (f *fixture) member(t *testing.T, hhID, role string) member {
	t.Helper()
	ctx := context.Background()
	email := "meals-" + uuid.NewString()[:8] + "@example.com"
	sub := "CiQ" + uuid.NewString() + "EgVsb2NhbA"
	var userID string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, $2, 'x', $3) RETURNING id::text`,
		email, "Member "+role, sub).Scan(&userID))
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	f.join(t, hhID, userID, role)

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": testIssuer, "aud": "home-os-ui", "sub": sub, "email": email,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	token, err := tok.SignedString(f.key)
	require.NoError(t, err)
	return member{token: token, userID: userID, householdID: hhID, personID: f.personOf(t, hhID, userID)}
}

func (f *fixture) join(t *testing.T, hhID, userID, role string) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hhID, userID, role)
	require.NoError(t, err)
}

func (f *fixture) personOf(t *testing.T, hhID, userID string) string {
	t.Helper()
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT id::text FROM people WHERE household_id = $1 AND user_id = $2`, hhID, userID).Scan(&id))
	return id
}

// kid adds a person without a login.
func (f *fixture) kid(t *testing.T, hhID, name string) string {
	t.Helper()
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`INSERT INTO people (household_id, display_name) VALUES ($1, $2) RETURNING id::text`, hhID, name).Scan(&id))
	return id
}

type req struct {
	method, path string
	as           member
	body         any
	household    string // X-Household-ID, optional
}

func (f *fixture) do(t *testing.T, rq req) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if rq.body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(rq.body))
	}
	r := httptest.NewRequest(rq.method, rq.path, &buf)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+rq.as.token)
	if rq.household != "" {
		r.Header.Set("X-Household-ID", rq.household)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out struct {
		Data T `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out.Data
}

type ingredientJSON struct {
	Name     string  `json:"name"`
	Quantity any     `json:"quantity"`
	Unit     *string `json:"unit"`
	Section  *string `json:"section"`
}

type recipeJSON struct {
	ID          string           `json:"id"`
	HouseholdID string           `json:"household_id"`
	Title       string           `json:"title"`
	Description *string          `json:"description"`
	Servings    *int             `json:"servings"`
	Ingredients []ingredientJSON `json:"ingredients"`
	Tags        []string         `json:"tags"`
	SourceURL   *string          `json:"source_url"`
}

type feedbackJSON struct {
	ID         string  `json:"id"`
	MealPlanID string  `json:"meal_plan_id"`
	PersonID   *string `json:"person_id"`
	Rating     *int    `json:"rating"`
	Comment    *string `json:"comment"`
}

type mealJSON struct {
	ID       string         `json:"id"`
	Date     string         `json:"date"`
	Slot     string         `json:"slot"`
	RecipeID *string        `json:"recipe_id"`
	Title    string         `json:"title"`
	Notes    *string        `json:"notes"`
	Feedback []feedbackJSON `json:"feedback"`
}

func tacoBody() map[string]any {
	return map[string]any{
		"title": "  Fish Tacos ", "description": "Friday favourite", "servings": 4,
		"prep_minutes": 15, "cook_minutes": 10,
		"ingredients": []map[string]any{
			{"name": " cod ", "quantity": 1.5, "unit": "lb", "section": "seafood"},
			{"name": "tortillas", "quantity": "a dozen"},
			{"name": "lime"},
		},
		"steps": "Fry. Assemble.", "tags": []string{"Weeknight", " weeknight", "", "Fish"},
		"source_url": "https://example.com/tacos",
	}
}

func (f *fixture) createRecipe(t *testing.T, as member, body map[string]any) recipeJSON {
	t.Helper()
	rec := f.do(t, req{method: http.MethodPost, path: "/api/v1/recipes", as: as, body: body})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	return decode[recipeJSON](t, rec)
}

func (f *fixture) setMeal(t *testing.T, as member, date, slot string, body map[string]any) mealJSON {
	t.Helper()
	rec := f.do(t, req{method: http.MethodPut, path: "/api/v1/meal-plans/" + date + "/" + slot, as: as, body: body})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decode[mealJSON](t, rec)
}

func TestRecipes_CRUD(t *testing.T) {
	f := setup(t)
	hh := f.household(t, "UTC")
	owner := f.member(t, hh, "owner")
	fam := f.member(t, hh, "family_member")

	r := f.createRecipe(t, fam, tacoBody())
	assert.Equal(t, "Fish Tacos", r.Title)
	assert.Equal(t, hh, r.HouseholdID)
	assert.Equal(t, []string{"weeknight", "fish"}, r.Tags)
	require.Len(t, r.Ingredients, 3)
	assert.Equal(t, "cod", r.Ingredients[0].Name)
	assert.Equal(t, 1.5, r.Ingredients[0].Quantity)
	assert.Equal(t, "a dozen", r.Ingredients[1].Quantity)
	assert.Nil(t, r.Ingredients[2].Quantity)

	soup := f.createRecipe(t, owner, map[string]any{"title": "Tomato soup", "tags": []string{"vegetarian"}})
	assert.Empty(t, soup.Ingredients)

	t.Run("list, search and tag filter", func(t *testing.T) {
		all := decode[[]recipeJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes", as: owner}))
		require.Len(t, all, 2)
		assert.Equal(t, "Fish Tacos", all[0].Title) // ordered by title

		byQ := decode[[]recipeJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes?q=TACO", as: owner}))
		require.Len(t, byQ, 1)
		assert.Equal(t, r.ID, byQ[0].ID)

		pct := decode[[]recipeJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes?q=%25", as: owner}))
		assert.Empty(t, pct, "LIKE wildcards in q are literal")

		byTag := decode[[]recipeJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes?tag=Vegetarian", as: owner}))
		require.Len(t, byTag, 1)
		assert.Equal(t, soup.ID, byTag[0].ID)
	})

	t.Run("get and update", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes/" + r.ID, as: fam})
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "Fish Tacos", decode[recipeJSON](t, rec).Title)

		rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/recipes/" + r.ID, as: owner,
			body: map[string]any{"title": "Shrimp Tacos", "ingredients": []map[string]any{{"name": "shrimp"}}}})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		u := decode[recipeJSON](t, rec)
		assert.Equal(t, "Shrimp Tacos", u.Title)
		assert.Nil(t, u.Description, "PUT replaces: omitted fields are cleared")
		assert.Empty(t, u.Tags)
		require.Len(t, u.Ingredients, 1)
	})

	t.Run("validation", func(t *testing.T) {
		for name, body := range map[string]map[string]any{
			"blank title":         {"title": " "},
			"bad servings":        {"title": "x", "servings": 0},
			"negative minutes":    {"title": "x", "prep_minutes": -1},
			"nameless ingredient": {"title": "x", "ingredients": []map[string]any{{"name": " "}}},
			"object quantity":     {"title": "x", "ingredients": []map[string]any{{"name": "a", "quantity": map[string]any{}}}},
			"bad url":             {"title": "x", "source_url": "javascript:alert(1)"},
		} {
			rec := f.do(t, req{method: http.MethodPost, path: "/api/v1/recipes", as: owner, body: body})
			assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		}
		rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes/not-a-uuid", as: owner})
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("delete", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodDelete, path: "/api/v1/recipes/" + soup.ID, as: fam})
		assert.Equal(t, http.StatusNoContent, rec.Code)
		rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes/" + soup.ID, as: fam})
		assert.Equal(t, http.StatusNotFound, rec.Code)
		rec = f.do(t, req{method: http.MethodDelete, path: "/api/v1/recipes/" + soup.ID, as: fam})
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestMealPlans_SetGetClear(t *testing.T) {
	f := setup(t)
	hh := f.household(t, "UTC")
	owner := f.member(t, hh, "owner")
	tacos := f.createRecipe(t, owner, tacoBody())

	m := f.setMeal(t, owner, "2026-03-06", "dinner", map[string]any{"recipe_id": tacos.ID, "notes": "double batch"})
	assert.Equal(t, "Fish Tacos", m.Title, "title defaults to the recipe's")
	assert.Equal(t, "2026-03-06", m.Date)
	require.NotNil(t, m.RecipeID)
	assert.Empty(t, m.Feedback)

	f.setMeal(t, owner, "2026-03-06", "breakfast", map[string]any{"title": "Pancakes"})
	f.setMeal(t, owner, "2026-03-07", "lunch", map[string]any{"title": "Leftovers"})
	f.setMeal(t, owner, "2026-04-01", "dinner", map[string]any{"recipe_id": tacos.ID, "title": "Taco night"})

	t.Run("range is ordered by date then slot", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/meal-plans?from=2026-03-06&to=2026-03-07", as: owner})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		list := decode[[]mealJSON](t, rec)
		require.Len(t, list, 3)
		assert.Equal(t, []string{"breakfast", "dinner", "lunch"}, []string{list[0].Slot, list[1].Slot, list[2].Slot})
		assert.Equal(t, "2026-03-07", list[2].Date)
	})

	t.Run("history by recipe", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodGet,
			path: "/api/v1/meal-plans?from=2026-01-01&to=2026-12-31&recipe_id=" + tacos.ID, as: owner})
		list := decode[[]mealJSON](t, rec)
		require.Len(t, list, 2)
		assert.Equal(t, "Taco night", list[1].Title)
	})

	t.Run("set replaces the slot", func(t *testing.T) {
		m2 := f.setMeal(t, owner, "2026-03-06", "dinner", map[string]any{"title": "Pizza"})
		assert.Equal(t, m.ID, m2.ID)
		assert.Equal(t, "Pizza", m2.Title)
		assert.Nil(t, m2.RecipeID)
		assert.Nil(t, m2.Notes)
	})

	t.Run("get by id", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/meals/" + m.ID, as: owner})
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "Pizza", decode[mealJSON](t, rec).Title)
	})

	t.Run("deleting a recipe keeps the meal's title", func(t *testing.T) {
		require.Equal(t, http.StatusNoContent,
			f.do(t, req{method: http.MethodDelete, path: "/api/v1/recipes/" + tacos.ID, as: owner}).Code)
		list := decode[[]mealJSON](t, f.do(t, req{method: http.MethodGet,
			path: "/api/v1/meal-plans?from=2026-04-01&to=2026-04-01", as: owner}))
		require.Len(t, list, 1)
		assert.Equal(t, "Taco night", list[0].Title)
		assert.Nil(t, list[0].RecipeID)
	})

	t.Run("validation", func(t *testing.T) {
		for name, rq := range map[string]req{
			"bad date":       {method: http.MethodPut, path: "/api/v1/meal-plans/2026-13-01/dinner", body: map[string]any{"title": "x"}},
			"bad slot":       {method: http.MethodPut, path: "/api/v1/meal-plans/2026-03-01/brunch", body: map[string]any{"title": "x"}},
			"no title":       {method: http.MethodPut, path: "/api/v1/meal-plans/2026-03-01/dinner", body: map[string]any{"notes": "x"}},
			"bad recipe":     {method: http.MethodPut, path: "/api/v1/meal-plans/2026-03-01/dinner", body: map[string]any{"recipe_id": "nope"}},
			"reversed range": {method: http.MethodGet, path: "/api/v1/meal-plans?from=2026-03-02&to=2026-03-01"},
			"huge range":     {method: http.MethodGet, path: "/api/v1/meal-plans?from=2026-01-01&to=2027-06-01"},
			"bad from":       {method: http.MethodGet, path: "/api/v1/meal-plans?from=yesterday"},
		} {
			rq.as = owner
			assert.Equal(t, http.StatusBadRequest, f.do(t, rq).Code, name)
		}
		rec := f.do(t, req{method: http.MethodPut, path: "/api/v1/meal-plans/2026-03-01/dinner", as: owner,
			body: map[string]any{"recipe_id": uuid.NewString()}})
		assert.Equal(t, http.StatusNotFound, rec.Code, "unknown recipe")
	})

	t.Run("clear", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodDelete, path: "/api/v1/meal-plans/2026-03-06/dinner", as: owner})
		assert.Equal(t, http.StatusNoContent, rec.Code)
		rec = f.do(t, req{method: http.MethodDelete, path: "/api/v1/meal-plans/2026-03-06/dinner", as: owner})
		assert.Equal(t, http.StatusNoContent, rec.Code, "clearing an empty slot is fine")
		rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/meals/" + m.ID, as: owner})
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestMealPlans_DefaultRangeUsesHouseholdTimezone(t *testing.T) {
	f := setup(t)
	// Pacific/Kiritimati is UTC+14 and Pacific/Pago_Pago UTC-11: at any
	// instant they are on different calendar dates.
	east := f.household(t, "Pacific/Kiritimati")
	west := f.household(t, "Pacific/Pago_Pago")
	eo := f.member(t, east, "owner")
	wo := f.member(t, west, "owner")

	now := time.Now()
	for _, tc := range []struct {
		as member
		tz string
	}{{eo, "Pacific/Kiritimati"}, {wo, "Pacific/Pago_Pago"}} {
		loc, err := time.LoadLocation(tc.tz)
		require.NoError(t, err)
		today := now.In(loc).Format("2006-01-02")
		f.setMeal(t, tc.as, today, "dinner", map[string]any{"title": "Tonight"})

		var out struct {
			Data []mealJSON `json:"data"`
			From string     `json:"from"`
			To   string     `json:"to"`
		}
		rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/meal-plans", as: tc.as})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		assert.Equal(t, today, out.From, tc.tz)
		assert.Equal(t, now.In(loc).AddDate(0, 0, 6).Format("2006-01-02"), out.To, tc.tz)
		require.Len(t, out.Data, 1, tc.tz)
	}
}

func TestMealFeedback_Permissions(t *testing.T) {
	f := setup(t)
	hh := f.household(t, "UTC")
	owner := f.member(t, hh, "owner")
	fam := f.member(t, hh, "family_member")
	viewer := f.member(t, hh, "viewer")
	kid := f.kid(t, hh, "Ada")

	m := f.setMeal(t, owner, "2026-02-01", "dinner", map[string]any{"title": "Lasagna"})
	fbPath := func(person string) string { return "/api/v1/meals/" + m.ID + "/feedback/" + person }

	t.Run("member rates for themselves", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodPut, path: fbPath(fam.personID), as: fam,
			body: map[string]any{"rating": 4, "comment": " yum "}})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		fb := decode[feedbackJSON](t, rec)
		assert.Equal(t, 4, *fb.Rating)
		assert.Equal(t, "yum", *fb.Comment)

		// Replacing keeps one row per person.
		rec = f.do(t, req{method: http.MethodPut, path: fbPath(fam.personID), as: fam, body: map[string]any{"rating": 5}})
		require.Equal(t, http.StatusOK, rec.Code)
		fb2 := decode[feedbackJSON](t, rec)
		assert.Equal(t, fb.ID, fb2.ID)
		assert.Nil(t, fb2.Comment)
	})

	t.Run("member can't rate for a kid", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodPut, path: fbPath(kid), as: fam, body: map[string]any{"rating": 1}})
		assert.Equal(t, http.StatusForbidden, rec.Code)
		rec = f.do(t, req{method: http.MethodPut, path: fbPath(owner.personID), as: fam, body: map[string]any{"rating": 1}})
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("owner rates for the kid", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodPut, path: fbPath(kid), as: owner, body: map[string]any{"comment": "picked out the spinach"}})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Nil(t, decode[feedbackJSON](t, rec).Rating)
	})

	t.Run("viewer reads but can't rate", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodPut, path: fbPath(viewer.personID), as: viewer, body: map[string]any{"rating": 3}})
		assert.Equal(t, http.StatusForbidden, rec.Code)
		rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/meals/" + m.ID + "/feedback", as: viewer})
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Len(t, decode[[]feedbackJSON](t, rec), 2)
	})

	t.Run("validation", func(t *testing.T) {
		for name, body := range map[string]map[string]any{
			"empty":      {},
			"rating 0":   {"rating": 0},
			"rating 6":   {"rating": 6},
			"blank only": {"comment": "  "},
		} {
			rec := f.do(t, req{method: http.MethodPut, path: fbPath(kid), as: owner, body: body})
			assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		}
		rec := f.do(t, req{method: http.MethodPut, path: fbPath(uuid.NewString()), as: owner, body: map[string]any{"rating": 3}})
		assert.Equal(t, http.StatusNotFound, rec.Code, "unknown person")
	})

	t.Run("meal includes feedback; deleting a person keeps it", func(t *testing.T) {
		_, err := f.pool.Exec(context.Background(), `DELETE FROM people WHERE id = $1`, kid)
		require.NoError(t, err)
		rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/meals/" + m.ID, as: owner})
		require.Equal(t, http.StatusOK, rec.Code)
		got := decode[mealJSON](t, rec)
		require.Len(t, got.Feedback, 2)
		var orphan int
		for _, fb := range got.Feedback {
			if fb.PersonID == nil {
				orphan++
			}
		}
		assert.Equal(t, 1, orphan)
	})

	t.Run("delete feedback", func(t *testing.T) {
		rec := f.do(t, req{method: http.MethodDelete, path: fbPath(fam.personID), as: fam})
		assert.Equal(t, http.StatusNoContent, rec.Code)
		rec = f.do(t, req{method: http.MethodDelete, path: fbPath(fam.personID), as: fam})
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestMeals_RolesAndHouseholdScope(t *testing.T) {
	f := setup(t)
	hhA := f.household(t, "UTC")
	hhB := f.household(t, "UTC")
	ownerA := f.member(t, hhA, "owner")
	ownerB := f.member(t, hhB, "owner")
	viewer := f.member(t, hhA, "viewer")
	sitter := f.member(t, hhA, "housesitter")
	vendor := f.member(t, hhA, "vendor")

	rA := f.createRecipe(t, ownerA, tacoBody())
	mA := f.setMeal(t, ownerA, "2026-05-01", "dinner", map[string]any{"recipe_id": rA.ID})

	t.Run("viewer and housesitter are read-only", func(t *testing.T) {
		for _, as := range []member{viewer, sitter} {
			assert.Equal(t, http.StatusOK, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes", as: as}).Code)
			assert.Equal(t, http.StatusOK, f.do(t, req{method: http.MethodGet,
				path: "/api/v1/meal-plans?from=2026-05-01", as: as}).Code)
			assert.Equal(t, http.StatusForbidden, f.do(t, req{method: http.MethodPost, path: "/api/v1/recipes",
				as: as, body: tacoBody()}).Code)
			assert.Equal(t, http.StatusForbidden, f.do(t, req{method: http.MethodPut,
				path: "/api/v1/recipes/" + rA.ID, as: as, body: tacoBody()}).Code)
			assert.Equal(t, http.StatusForbidden, f.do(t, req{method: http.MethodDelete,
				path: "/api/v1/recipes/" + rA.ID, as: as}).Code)
			assert.Equal(t, http.StatusForbidden, f.do(t, req{method: http.MethodPut,
				path: "/api/v1/meal-plans/2026-05-02/lunch", as: as, body: map[string]any{"title": "x"}}).Code)
			assert.Equal(t, http.StatusForbidden, f.do(t, req{method: http.MethodDelete,
				path: "/api/v1/meal-plans/2026-05-01/dinner", as: as}).Code)
		}
	})

	t.Run("vendor sees nothing", func(t *testing.T) {
		assert.Equal(t, http.StatusForbidden, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes", as: vendor}).Code)
		assert.Equal(t, http.StatusForbidden, f.do(t, req{method: http.MethodGet, path: "/api/v1/meal-plans", as: vendor}).Code)
	})

	t.Run("other family's ids are 404", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes/" + rA.ID, as: ownerB}).Code)
		assert.Equal(t, http.StatusNotFound, f.do(t, req{method: http.MethodPut, path: "/api/v1/recipes/" + rA.ID,
			as: ownerB, body: tacoBody()}).Code)
		assert.Equal(t, http.StatusNotFound, f.do(t, req{method: http.MethodDelete, path: "/api/v1/recipes/" + rA.ID, as: ownerB}).Code)
		assert.Equal(t, http.StatusNotFound, f.do(t, req{method: http.MethodGet, path: "/api/v1/meals/" + mA.ID, as: ownerB}).Code)
		assert.Equal(t, http.StatusNotFound, f.do(t, req{method: http.MethodPut,
			path: "/api/v1/meals/" + mA.ID + "/feedback/" + ownerB.personID, as: ownerB, body: map[string]any{"rating": 1}}).Code)
		// B can't plan A's recipe, nor rate A's person on B's own meal.
		assert.Equal(t, http.StatusNotFound, f.do(t, req{method: http.MethodPut, path: "/api/v1/meal-plans/2026-05-01/dinner",
			as: ownerB, body: map[string]any{"recipe_id": rA.ID}}).Code)
		mB := f.setMeal(t, ownerB, "2026-05-01", "dinner", map[string]any{"title": "B dinner"})
		assert.Equal(t, http.StatusNotFound, f.do(t, req{method: http.MethodPut,
			path: "/api/v1/meals/" + mB.ID + "/feedback/" + ownerA.personID, as: ownerB, body: map[string]any{"rating": 1}}).Code)

		// B's lists don't include A's rows, and A's meal is untouched.
		assert.Len(t, decode[[]recipeJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/recipes", as: ownerB})), 0)
		listB := decode[[]mealJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/meal-plans?from=2026-05-01", as: ownerB}))
		require.Len(t, listB, 1)
		assert.Equal(t, "B dinner", listB[0].Title)
		got := decode[mealJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/meals/" + mA.ID, as: ownerA}))
		assert.Equal(t, "Fish Tacos", got.Title)
	})

	t.Run("X-Household-ID switches family", func(t *testing.T) {
		// ownerA also joins B as a family_member; with the header they see B's plan.
		f.join(t, hhB, ownerA.userID, "family_member")
		list := decode[[]mealJSON](t, f.do(t, req{method: http.MethodGet, path: "/api/v1/meal-plans?from=2026-05-01",
			as: ownerA, household: hhB}))
		require.Len(t, list, 1)
		assert.Equal(t, "B dinner", list[0].Title)
		// As a family_member of B they can't rate B's owner.
		rec := f.do(t, req{method: http.MethodPut, path: "/api/v1/meals/" + list[0].ID + "/feedback/" + ownerB.personID,
			as: ownerA, household: hhB, body: map[string]any{"rating": 2}})
		assert.Equal(t, http.StatusForbidden, rec.Code)
		// But can rate themselves there (their B person record).
		rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/meals/" + list[0].ID + "/feedback/" + f.personOf(t, hhB, ownerA.userID),
			as: ownerA, household: hhB, body: map[string]any{"rating": 2}})
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
}
