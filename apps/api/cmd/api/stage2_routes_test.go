package main

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
	"regexp"
	"sort"
	"strings"
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
	"home-os/api/internal/middleware"
)

const stage2TestIssuer = "https://dex.test/dex"

// stage2Routes is every route registerStage2Routes must mount (#4489), as
// "METHOD pattern". The KB contracts: api/people-preferences.md,
// api/budget-categories-obligations.md, apis/lists.md,
// api/meals-recipes-meal-plans-feedback.md, apis/activity-feed.md,
// architecture/guides-api.md.
var stage2Routes = []string{
	"GET /api/v1/people",
	"POST /api/v1/people",
	"GET /api/v1/people/{id}",
	"PUT /api/v1/people/{id}",
	"PATCH /api/v1/people/{id}",
	"DELETE /api/v1/people/{id}",
	"GET /api/v1/people/{id}/preferences",
	"POST /api/v1/people/{id}/preferences",
	"PUT /api/v1/people/{id}/preferences/{prefId}",
	"DELETE /api/v1/people/{id}/preferences/{prefId}",

	"GET /api/v1/budget/categories",
	"POST /api/v1/budget/categories",
	"GET /api/v1/budget/categories/{id}",
	"PUT /api/v1/budget/categories/{id}",
	"DELETE /api/v1/budget/categories/{id}",
	"GET /api/v1/budget/obligations",
	"GET /api/v1/budget/due-soon",

	"GET /api/v1/lists",
	"POST /api/v1/lists",
	"GET /api/v1/lists/{id}",
	"PATCH /api/v1/lists/{id}",
	"DELETE /api/v1/lists/{id}",
	"POST /api/v1/lists/{id}/archive",
	"POST /api/v1/lists/{id}/unarchive",
	"GET /api/v1/lists/{id}/items",
	"POST /api/v1/lists/{id}/items",
	"POST /api/v1/lists/{id}/items/merge",
	"POST /api/v1/lists/{id}/items/check",
	"POST /api/v1/lists/{id}/items/uncheck",
	"POST /api/v1/lists/{id}/items/remove",
	"PATCH /api/v1/lists/{id}/items/{itemId}",
	"DELETE /api/v1/lists/{id}/items/{itemId}",
	"POST /api/v1/lists/{id}/items/{itemId}/move",

	"GET /api/v1/recipes",
	"POST /api/v1/recipes",
	"GET /api/v1/recipes/{id}",
	"PUT /api/v1/recipes/{id}",
	"DELETE /api/v1/recipes/{id}",
	"GET /api/v1/meal-plans",
	"GET /api/v1/meal-plans/grocery",
	"POST /api/v1/meal-plans/grocery",
	"PUT /api/v1/meal-plans/{date}/{slot}",
	"DELETE /api/v1/meal-plans/{date}/{slot}",
	"GET /api/v1/meals/{id}",
	"GET /api/v1/meals/{id}/feedback",
	"PUT /api/v1/meals/{id}/feedback/{personId}",
	"DELETE /api/v1/meals/{id}/feedback/{personId}",

	"GET /api/v1/activity",

	"GET /api/v1/guides",
	"POST /api/v1/guides",
	"GET /api/v1/guides/{id}",
	"PUT /api/v1/guides/{id}",
	"DELETE /api/v1/guides/{id}",
	"GET /api/v1/guides/{id}/revisions",
	"GET /api/v1/guides/{id}/revisions/{revisionId}",
	"POST /api/v1/guides/{id}/revisions/{revisionId}/restore",
}

// recordingRouter counts every route registration so a pattern mounted twice
// (which chi silently overwrites) fails the test.
type recordingRouter struct {
	chi.Router
	seen map[string]int
}

func (r *recordingRouter) add(method, pattern string) { r.seen[method+" "+pattern]++ }

func (r *recordingRouter) Get(p string, h http.HandlerFunc) {
	r.add(http.MethodGet, p)
	r.Router.Get(p, h)
}

func (r *recordingRouter) Post(p string, h http.HandlerFunc) {
	r.add(http.MethodPost, p)
	r.Router.Post(p, h)
}

func (r *recordingRouter) Put(p string, h http.HandlerFunc) {
	r.add(http.MethodPut, p)
	r.Router.Put(p, h)
}

func (r *recordingRouter) Patch(p string, h http.HandlerFunc) {
	r.add(http.MethodPatch, p)
	r.Router.Patch(p, h)
}

func (r *recordingRouter) Delete(p string, h http.HandlerFunc) {
	r.add(http.MethodDelete, p)
	r.Router.Delete(p, h)
}

func (r *recordingRouter) Method(m, p string, h http.Handler) {
	r.add(m, p)
	r.Router.Method(m, p, h)
}

func (r *recordingRouter) MethodFunc(m, p string, h http.HandlerFunc) {
	r.add(m, p)
	r.Router.MethodFunc(m, p, h)
}

// TestStage2Routes_Registered checks registerStage2Routes mounts exactly the
// Stage 2 routes, each once, and that chi serves every one of them (no
// pattern shadows another). Needs no database.
func TestStage2Routes_Registered(t *testing.T) {
	rec := &recordingRouter{Router: chi.NewRouter(), seen: map[string]int{}}
	registerStage2Routes(rec, nil, nil)

	var got []string
	for k, n := range rec.seen {
		assert.Equalf(t, 1, n, "%s registered %d times", k, n)
		got = append(got, k)
	}
	want := append([]string(nil), stage2Routes...)
	sort.Strings(got)
	sort.Strings(want)
	assert.Equal(t, want, got)

	// chi.Walk sees what the router will actually serve.
	var walked []string
	require.NoError(t, chi.Walk(rec.Router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		walked = append(walked, method+" "+route)
		return nil
	}))
	sort.Strings(walked)
	assert.Equal(t, want, walked)
}

// TestStage2Routes_MountedOnceInMain guards against mounting a Stage 2 route
// outside registerStage2Routes (a duplicate chi would silently overwrite,
// possibly outside RequireAuth / RequirePolicy). main.go mounts the protected
// group, which calls registerProtectedRoutes (routes.go), which calls
// registerStage2Routes once.
func TestStage2Routes_MountedOnceInMain(t *testing.T) {
	mainSrc, err := os.ReadFile("main.go")
	require.NoError(t, err)
	routesSrc, err := os.ReadFile("routes.go")
	require.NoError(t, err)
	m, rs := string(mainSrc), string(routesSrc)

	authAt := strings.Index(m, "r.Use(middleware.RequireAuth(oidcVerifier))")
	policyAt := strings.Index(m, "r.Use(middleware.RequirePolicy)")
	callAt := strings.Index(m, "registerProtectedRoutes(r, protectedDeps{")
	require.Positive(t, authAt)
	assert.Greater(t, policyAt, authAt, "RequirePolicy must run after RequireAuth")
	assert.Greater(t, callAt, policyAt, "protected routes must be inside the RequireAuth + RequirePolicy group")
	assert.Equal(t, 1, strings.Count(m, "registerProtectedRoutes("), "main.go must mount the protected routes once")
	assert.Equal(t, 1, strings.Count(rs, "registerStage2Routes(r, d.pool, d.search)"),
		"routes.go must call registerStage2Routes exactly once")

	literal := regexp.MustCompile(`"/api/v1/(people|budget|lists|recipes|meal-plans|meals|activity|guides)\b`)
	assert.Empty(t, literal.FindAllString(m, -1), "Stage 2 paths must only be mounted by their packages")
	assert.Empty(t, literal.FindAllString(rs, -1), "Stage 2 paths must only be mounted by their packages")
	for _, pkg := range []string{"people", "budget", "lists", "meals", "activity", "guides"} {
		assert.Equalf(t, 1, strings.Count(rs, pkg+".NewHandler("), "%s handler must be built once", pkg)
		assert.Equalf(t, 0, strings.Count(m, pkg+".NewHandler("), "%s handler must not be built in main.go", pkg)
	}
}

// stage2Fixture is a router like cmd/api/main.go's protected group: the real
// RequireAuth with the Postgres member resolver (X-Household-ID aware, #4429)
// in front of registerStage2Routes.
type stage2Fixture struct {
	pool *pgxpool.Pool
	h    http.Handler
	key  *rsa.PrivateKey
}

func newStage2Fixture(t *testing.T) *stage2Fixture {
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
	v := auth.NewVerifierWithKeySet(stage2TestIssuer, ks, []string{"home-os-ui"}).
		WithResolver(auth.NewMemberResolver(pool))

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		r.Use(middleware.RequirePolicy)
		registerStage2Routes(r, pool, nil)
	})
	return &stage2Fixture{pool: pool, h: r, key: key}
}

type stage2Member struct{ token, householdID, personID string }

func (f *stage2Fixture) household(t *testing.T) string {
	t.Helper()
	var hhID string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`INSERT INTO households (name, timezone) VALUES ('Stage 2 HH', 'America/Chicago') RETURNING id::text`).Scan(&hhID))
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hhID) })
	return hhID
}

// member adds a user with role to household hhID and signs a Dex token for
// them.
func (f *stage2Fixture) member(t *testing.T, hhID, role string) stage2Member {
	t.Helper()
	ctx := context.Background()

	email := "stage2-" + uuid.NewString()[:8] + "@example.com"
	sub := "CiQ" + uuid.NewString() + "EgVsb2NhbA"
	var userID string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, 'Stage Two', 'x', $2) RETURNING id::text`,
		email, sub).Scan(&userID))
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	_, err := f.pool.Exec(ctx, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hhID, userID, role)
	require.NoError(t, err)

	// Joining a household creates the member's people row (migration 041).
	var personID string
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT id::text FROM people WHERE household_id = $1 AND user_id = $2`, hhID, userID).Scan(&personID))

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": stage2TestIssuer, "aud": "home-os-ui", "sub": sub, "email": email,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	token, err := tok.SignedString(f.key)
	require.NoError(t, err)
	return stage2Member{token: token, householdID: hhID, personID: personID}
}

// do sends one request; as nil sends no Authorization header.
func (f *stage2Fixture) do(t *testing.T, method, path string, body any, as *stage2Member) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	if as != nil {
		r.Header.Set("Authorization", "Bearer "+as.token)
		r.Header.Set(auth.HouseholdHeader, as.householdID)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

// stage2Step is one request in the walk through every Stage 2 route. path
// and body are built when the step runs, from ids earlier steps captured.
type stage2Step struct {
	route string // "METHOD pattern", must be one of stage2Routes
	path  func() string
	body  func() any
	save  func(data json.RawMessage)
	// managers marks a route the handler's role policy limits to owners and
	// family managers: a family_member must get 403 and an owner 2xx.
	managers bool
}

func idOf(t *testing.T, data json.RawMessage) string {
	t.Helper()
	var v struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(data, &v), string(data))
	require.NotEmpty(t, v.ID, string(data))
	return v.ID
}

func idsOf(t *testing.T, data json.RawMessage) []string {
	t.Helper()
	var v []struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(data, &v), string(data))
	out := make([]string, 0, len(v))
	for _, x := range v {
		out = append(out, x.ID)
	}
	return out
}

// TestStage2Routes_AuthAndMemberAccess proves, through the same middleware
// and household resolution as main.go, that every Stage 2 route answers 401
// without a token and 2xx for a family_member of the household.
func TestStage2Routes_AuthAndMemberAccess(t *testing.T) {
	f := newStage2Fixture(t)
	hh := f.household(t)
	m := f.member(t, hh, auth.RoleFamilyMember)
	owner := f.member(t, hh, auth.RoleOwner)

	var personID, prefID, categoryID, listID, recipeID, mealID, guideID, revisionID string
	var itemIDs []string
	const mealDate, mealSlot = "2026-11-01", "dinner"
	p := func(s string) func() string { return func() string { return s } }
	b := func(v any) func() any { return func() any { return v } }

	steps := []stage2Step{
		// People and preferences.
		{route: "POST /api/v1/people", path: p("/api/v1/people"), body: b(map[string]any{"display_name": "Kiddo"}), managers: true,
			save: func(d json.RawMessage) { personID = idOf(t, d) }},
		{route: "GET /api/v1/people", path: p("/api/v1/people")},
		{route: "GET /api/v1/people/{id}", path: func() string { return "/api/v1/people/" + personID }},
		{route: "PUT /api/v1/people/{id}", managers: true, path: func() string { return "/api/v1/people/" + personID },
			body: b(map[string]any{"display_name": "Kiddo Two"})},
		// PATCH sets the colour (#4821): a family_member may do it for
		// themselves, so the kid's own record is not the member's to touch
		// here — use the caller's own person.
		{route: "PATCH /api/v1/people/{id}", path: func() string { return "/api/v1/people/" + m.personID },
			body: b(map[string]any{"color": "teal"})},
		{route: "POST /api/v1/people/{id}/preferences", managers: true, path: func() string { return "/api/v1/people/" + personID + "/preferences" },
			body: b(map[string]any{"kind": "like", "value": "dinosaurs"}), save: func(d json.RawMessage) { prefID = idOf(t, d) }},
		{route: "GET /api/v1/people/{id}/preferences", path: func() string { return "/api/v1/people/" + personID + "/preferences" }},
		{route: "PUT /api/v1/people/{id}/preferences/{prefId}", managers: true,
			path: func() string { return "/api/v1/people/" + personID + "/preferences/" + prefID },
			body: b(map[string]any{"kind": "like", "value": "trains"})},
		{route: "DELETE /api/v1/people/{id}/preferences/{prefId}", managers: true,
			path: func() string { return "/api/v1/people/" + personID + "/preferences/" + prefID }},
		{route: "DELETE /api/v1/people/{id}", managers: true, path: func() string { return "/api/v1/people/" + personID }},

		// Budget categories and obligations.
		{route: "POST /api/v1/budget/categories", path: p("/api/v1/budget/categories"),
			body: b(map[string]any{"name": "Groceries", "monthly_amount": "500.00"}), save: func(d json.RawMessage) { categoryID = idOf(t, d) }},
		{route: "GET /api/v1/budget/categories", path: p("/api/v1/budget/categories")},
		{route: "GET /api/v1/budget/categories/{id}", path: func() string { return "/api/v1/budget/categories/" + categoryID }},
		{route: "PUT /api/v1/budget/categories/{id}", path: func() string { return "/api/v1/budget/categories/" + categoryID },
			body: b(map[string]any{"name": "Food"})},
		{route: "GET /api/v1/budget/obligations", path: p("/api/v1/budget/obligations")},
		{route: "GET /api/v1/budget/due-soon", path: p("/api/v1/budget/due-soon")},
		{route: "DELETE /api/v1/budget/categories/{id}", path: func() string { return "/api/v1/budget/categories/" + categoryID }},

		// Lists and items.
		{route: "POST /api/v1/lists", path: p("/api/v1/lists"),
			body: b(map[string]any{"name": "Groceries", "kind": "grocery", "items": []map[string]any{{"text": "milk"}, {"text": "eggs"}}}),
			save: func(d json.RawMessage) { listID = idOf(t, d) }},
		{route: "GET /api/v1/lists", path: p("/api/v1/lists")},
		{route: "GET /api/v1/lists/{id}", path: func() string { return "/api/v1/lists/" + listID }},
		{route: "PATCH /api/v1/lists/{id}", path: func() string { return "/api/v1/lists/" + listID }, body: b(map[string]any{"name": "Weekly shop"})},
		{route: "POST /api/v1/lists/{id}/archive", path: func() string { return "/api/v1/lists/" + listID + "/archive" }},
		{route: "POST /api/v1/lists/{id}/unarchive", path: func() string { return "/api/v1/lists/" + listID + "/unarchive" }},
		{route: "POST /api/v1/lists/{id}/items", path: func() string { return "/api/v1/lists/" + listID + "/items" },
			body: b(map[string]any{"items": []map[string]any{{"text": "bread"}}})},
		{route: "POST /api/v1/lists/{id}/items/merge", path: func() string { return "/api/v1/lists/" + listID + "/items/merge" },
			body: b(map[string]any{"items": []map[string]any{{"text": "oat milk"}, {"text": "jam"}}})},
		{route: "GET /api/v1/lists/{id}/items", path: func() string { return "/api/v1/lists/" + listID + "/items" },
			save: func(d json.RawMessage) { itemIDs = idsOf(t, d); require.GreaterOrEqual(t, len(itemIDs), 3) }},
		{route: "POST /api/v1/lists/{id}/items/check", path: func() string { return "/api/v1/lists/" + listID + "/items/check" },
			body: func() any { return map[string]any{"item_ids": itemIDs[:1]} }},
		{route: "POST /api/v1/lists/{id}/items/uncheck", path: func() string { return "/api/v1/lists/" + listID + "/items/uncheck" },
			body: func() any { return map[string]any{"item_ids": itemIDs[:1]} }},
		{route: "PATCH /api/v1/lists/{id}/items/{itemId}", path: func() string { return "/api/v1/lists/" + listID + "/items/" + itemIDs[0] },
			body: b(map[string]any{"text": "oat milk"})},
		{route: "POST /api/v1/lists/{id}/items/{itemId}/move",
			path: func() string { return "/api/v1/lists/" + listID + "/items/" + itemIDs[0] + "/move" },
			body: func() any { return map[string]any{"after_id": itemIDs[1]} }},
		{route: "POST /api/v1/lists/{id}/items/remove", path: func() string { return "/api/v1/lists/" + listID + "/items/remove" },
			body: func() any { return map[string]any{"item_ids": itemIDs[2:3]} }},
		{route: "DELETE /api/v1/lists/{id}/items/{itemId}", path: func() string { return "/api/v1/lists/" + listID + "/items/" + itemIDs[0] }},
		{route: "DELETE /api/v1/lists/{id}", path: func() string { return "/api/v1/lists/" + listID }},

		// Recipes, meal plans and feedback.
		{route: "POST /api/v1/recipes", path: p("/api/v1/recipes"), body: b(map[string]any{"title": "Tacos"}),
			save: func(d json.RawMessage) { recipeID = idOf(t, d) }},
		{route: "GET /api/v1/recipes", path: p("/api/v1/recipes")},
		{route: "GET /api/v1/recipes/{id}", path: func() string { return "/api/v1/recipes/" + recipeID }},
		{route: "PUT /api/v1/recipes/{id}", path: func() string { return "/api/v1/recipes/" + recipeID }, body: b(map[string]any{"title": "Fish tacos"})},
		{route: "PUT /api/v1/meal-plans/{date}/{slot}", path: p("/api/v1/meal-plans/" + mealDate + "/" + mealSlot),
			body: func() any { return map[string]any{"recipe_id": recipeID} }, save: func(d json.RawMessage) { mealID = idOf(t, d) }},
		{route: "GET /api/v1/meal-plans", path: p("/api/v1/meal-plans?from=2026-11-01&to=2026-11-07")},
		{route: "GET /api/v1/meal-plans/grocery", path: p("/api/v1/meal-plans/grocery?from=2026-11-01&to=2026-11-07&slots=dinner")},
		{route: "POST /api/v1/meal-plans/grocery", path: p("/api/v1/meal-plans/grocery?from=2026-11-01&to=2026-11-07&slots=dinner")},
		{route: "GET /api/v1/meals/{id}", path: func() string { return "/api/v1/meals/" + mealID }},
		{route: "PUT /api/v1/meals/{id}/feedback/{personId}", path: func() string { return "/api/v1/meals/" + mealID + "/feedback/" + m.personID },
			body: b(map[string]any{"rating": 5, "comment": "great"})},
		{route: "GET /api/v1/meals/{id}/feedback", path: func() string { return "/api/v1/meals/" + mealID + "/feedback" }},
		{route: "DELETE /api/v1/meals/{id}/feedback/{personId}", path: func() string { return "/api/v1/meals/" + mealID + "/feedback/" + m.personID }},
		{route: "DELETE /api/v1/meal-plans/{date}/{slot}", path: p("/api/v1/meal-plans/" + mealDate + "/" + mealSlot)},
		{route: "DELETE /api/v1/recipes/{id}", path: func() string { return "/api/v1/recipes/" + recipeID }},

		// Activity feed.
		{route: "GET /api/v1/activity", path: p("/api/v1/activity")},

		// Guides and revisions.
		{route: "POST /api/v1/guides", path: p("/api/v1/guides"), body: b(map[string]any{"title": "Reset the router", "body": "1. Unplug it."}),
			save: func(d json.RawMessage) { guideID = idOf(t, d) }},
		{route: "GET /api/v1/guides", path: p("/api/v1/guides")},
		{route: "GET /api/v1/guides/{id}", path: func() string { return "/api/v1/guides/" + guideID }},
		{route: "PUT /api/v1/guides/{id}", path: func() string { return "/api/v1/guides/" + guideID }, body: b(map[string]any{"body": "1. Unplug it.\n2. Wait."})},
		{route: "GET /api/v1/guides/{id}/revisions", path: func() string { return "/api/v1/guides/" + guideID + "/revisions" },
			save: func(d json.RawMessage) {
				ids := idsOf(t, d)
				require.NotEmpty(t, ids)
				revisionID = ids[len(ids)-1]
			}},
		{route: "GET /api/v1/guides/{id}/revisions/{revisionId}",
			path: func() string { return "/api/v1/guides/" + guideID + "/revisions/" + revisionID }},
		{route: "POST /api/v1/guides/{id}/revisions/{revisionId}/restore",
			path: func() string { return "/api/v1/guides/" + guideID + "/revisions/" + revisionID + "/restore" }},
		{route: "DELETE /api/v1/guides/{id}", path: func() string { return "/api/v1/guides/" + guideID }},
	}

	// Every Stage 2 route is exercised exactly once.
	covered := map[string]bool{}
	for _, s := range steps {
		require.Falsef(t, covered[s.route], "step for %s listed twice", s.route)
		covered[s.route] = true
	}
	for _, r := range stage2Routes {
		require.Truef(t, covered[r], "no step exercises %s", r)
	}

	for _, s := range steps {
		method := strings.SplitN(s.route, " ", 2)[0]
		path := s.path()
		var body any
		if s.body != nil {
			body = s.body()
		}

		// No token: 401 before any handler runs.
		rec := f.do(t, method, path, body, nil)
		require.Equalf(t, http.StatusUnauthorized, rec.Code, "%s (%s) without a token: %s", s.route, path, rec.Body.String())

		// Member token: 2xx, or 403 where the role policy reserves the route
		// for owners and family managers (who then get 2xx).
		rec = f.do(t, method, path, body, &m)
		if s.managers {
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s (%s) as family_member: want 403, got %d %s", s.route, path, rec.Code, rec.Body.String())
			}
			rec = f.do(t, method, path, body, &owner)
		}
		require.Truef(t, rec.Code >= 200 && rec.Code < 300, "%s (%s): %d %s", s.route, path, rec.Code, rec.Body.String())
		if s.save != nil {
			var env struct {
				Data json.RawMessage `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
			s.save(env.Data)
		}
	}
}
