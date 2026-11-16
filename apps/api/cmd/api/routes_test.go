package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/config"
	"home-os/api/internal/household"
	"home-os/api/internal/invite"
	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
)

// protectedRoutes walks the real protected router (no database: handlers are
// built but never called) and returns every "METHOD pattern" it serves.
func protectedRoutes(t *testing.T) []string {
	t.Helper()
	cfg := &config.Config{}
	r := chi.NewRouter()
	registerProtectedRoutes(r, protectedDeps{
		cfg:              cfg,
		search:           search.NewClient(cfg),
		householdHandler: household.NewHandler(nil),
		inviteHandler:    invite.NewHandler(nil, cfg, nil),
	})
	var out []string
	require.NoError(t, chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out = append(out, method+" "+route)
		return nil
	}))
	sort.Strings(out)
	return out
}

// TestProtectedRoutes_EveryRouteHasPolicy: every protected route has a rule
// in middleware/roles.go, and the table has no rule for a route that does
// not exist (a stale or misspelt pattern would silently never apply).
func TestProtectedRoutes_EveryRouteHasPolicy(t *testing.T) {
	routes := protectedRoutes(t)
	require.NotEmpty(t, routes)
	for _, rt := range routes {
		method, pattern, _ := strings.Cut(rt, " ")
		_, ok := middleware.RuleFor(method, pattern)
		assert.Truef(t, ok, "%s has no access policy in middleware/roles.go", rt)
	}
	rules := middleware.RulePatterns()
	sort.Strings(rules)
	assert.Equal(t, routes, rules, "routeRules must list exactly the protected routes")
}

// Role sets for the expected matrix. They are spelled out here, route by
// route, independently of auth/roles.go, so a change to the policy table
// that alters who can call a route fails this test until the expectation is
// updated on purpose.
const (
	o  = auth.RoleOwner
	fm = auth.RoleFamilyManager
	m  = auth.RoleFamilyMember
	v  = auth.RoleViewer
	hs = auth.RoleHousesitter
	vd = auth.RoleVendor
)

var (
	managers   = []string{o, fm}
	family     = []string{o, fm, m}
	readers    = []string{o, fm, m, v}         // family + viewer
	sitters    = []string{o, fm, m, v, hs}     // + housesitter
	everyone   = []string{o, fm, m, v, hs, vd} // all roles
	sitterEdit = []string{o, fm, m, hs}        // family + housesitter, no viewer
	vendorRead = []string{o, fm, m, v, vd}     // + vendor, no housesitter
	vendorEdit = []string{o, fm, m, vd}        // family + vendor, no viewer
)

// expectedAccess is who may call each protected route (route-level; some
// handlers refine further per record, tested in TestRolePolicy_Records).
var expectedAccess = map[string][]string{
	"GET /api/v1/me/households":                     everyone,
	"GET /api/v1/households/me":                     everyone,
	"PATCH /api/v1/households/{id}":                 managers,
	"GET /api/v1/households/me/members":             sitters,
	"PATCH /api/v1/households/me/members/{userId}":  managers,
	"DELETE /api/v1/households/me/members/{userId}": managers,

	"POST /api/v1/invites":        managers,
	"GET /api/v1/invites":         managers,
	"DELETE /api/v1/invites/{id}": managers,

	"POST /api/v1/agent-tokens":        readers, // viewer: read scope only (handler)
	"GET /api/v1/agent-tokens":         everyone,
	"DELETE /api/v1/agent-tokens/{id}": everyone,

	"GET /api/v1/assets":         everyone,
	"POST /api/v1/assets":        family,
	"GET /api/v1/assets/{id}":    everyone,
	"PUT /api/v1/assets/{id}":    family,
	"DELETE /api/v1/assets/{id}": family,

	"GET /api/v1/loans":         readers,
	"POST /api/v1/loans":        family,
	"GET /api/v1/loans/{id}":    readers,
	"PUT /api/v1/loans/{id}":    family,
	"DELETE /api/v1/loans/{id}": family,

	"GET /api/v1/vehicles":         readers,
	"POST /api/v1/vehicles":        family,
	"GET /api/v1/vehicles/{id}":    readers,
	"PUT /api/v1/vehicles/{id}":    family,
	"DELETE /api/v1/vehicles/{id}": family,

	"GET /api/v1/pets":         sitters,
	"POST /api/v1/pets":        family,
	"GET /api/v1/pets/{id}":    sitters,
	"PUT /api/v1/pets/{id}":    family,
	"DELETE /api/v1/pets/{id}": family,

	"GET /api/v1/vendors":         sitters,
	"POST /api/v1/vendors":        family,
	"GET /api/v1/vendors/{id}":    sitters,
	"PUT /api/v1/vendors/{id}":    family,
	"DELETE /api/v1/vendors/{id}": family,

	"GET /api/v1/calendars":                          sitters,
	"POST /api/v1/calendars":                         family,
	"GET /api/v1/calendars/events":                   sitters,
	"GET /api/v1/calendars/{id}/events":              sitters,
	"POST /api/v1/calendars/{id}/events":             family,
	"PATCH /api/v1/calendars/{id}/events/{eventId}":  family,
	"DELETE /api/v1/calendars/{id}/events/{eventId}": family,

	"GET /api/v1/maintenance/tasks":         vendorRead,
	"POST /api/v1/maintenance/tasks":        family,
	"PATCH /api/v1/maintenance/tasks/{id}":  vendorEdit, // vendor: status/notes only (handler)
	"DELETE /api/v1/maintenance/tasks/{id}": family,
	"GET /api/v1/maintenance/schedules":     vendorRead,
	"POST /api/v1/maintenance/schedules":    family,

	"GET /api/v1/properties":             everyone,
	"POST /api/v1/properties":            family,
	"GET /api/v1/properties/{id}":        everyone,
	"PUT /api/v1/properties/{id}":        family,
	"DELETE /api/v1/properties/{id}":     family,
	"GET /api/v1/properties/{id}/rooms":  everyone,
	"POST /api/v1/properties/{id}/rooms": family,

	"GET /api/v1/bills":         readers,
	"POST /api/v1/bills":        family,
	"GET /api/v1/bills/{id}":    readers,
	"PUT /api/v1/bills/{id}":    family,
	"DELETE /api/v1/bills/{id}": family,

	"GET /api/v1/search": everyone, // #4717: every role, filtered per hit by the handler

	"GET /api/v1/notes":         sitters, // housesitter: notes on visible records only (handler)
	"POST /api/v1/notes":        sitterEdit,
	"GET /api/v1/notes/{id}":    sitters,
	"PUT /api/v1/notes/{id}":    family,
	"PATCH /api/v1/notes/{id}":  family,
	"DELETE /api/v1/notes/{id}": family,

	"GET /api/v1/notifications":             everyone,
	"POST /api/v1/notifications":            family,
	"PATCH /api/v1/notifications/{id}/read": everyone,

	"POST /api/v1/files/upload":      family,
	"GET /api/v1/files":              readers,
	"GET /api/v1/files/{id}":         readers,
	"GET /api/v1/files/{id}/content": readers,
	"PATCH /api/v1/files/{id}":       family,
	"DELETE /api/v1/files/{id}":      family,

	"POST /api/v1/secrets":                    family,
	"GET /api/v1/secrets":                     family,
	"GET /api/v1/secrets/{id}":                family,
	"PATCH /api/v1/secrets/{id}":              family,
	"DELETE /api/v1/secrets/{id}":             family,
	"POST /api/v1/secrets/setup":              family,
	"POST /api/v1/secrets/verify":             family,
	"GET /api/v1/secrets/key":                 family,
	"PUT /api/v1/secrets/{id}/agent-share":    family,
	"DELETE /api/v1/secrets/{id}/agent-share": family,

	"POST /api/v1/links":        family,
	"GET /api/v1/links":         readers,
	"DELETE /api/v1/links/{id}": family,

	"GET /api/v1/people":                              sitters, // housesitter: names + allergies (handler)
	"POST /api/v1/people":                             family,
	"GET /api/v1/people/{id}":                         sitters,
	"PUT /api/v1/people/{id}":                         family,
	"PATCH /api/v1/people/{id}":                       family, // handler: self or manager only
	"DELETE /api/v1/people/{id}":                      family,
	"GET /api/v1/people/{id}/preferences":             readers,
	"POST /api/v1/people/{id}/preferences":            family,
	"PUT /api/v1/people/{id}/preferences/{prefId}":    family,
	"DELETE /api/v1/people/{id}/preferences/{prefId}": family,

	"GET /api/v1/budget/categories":         readers,
	"POST /api/v1/budget/categories":        family,
	"GET /api/v1/budget/categories/{id}":    readers,
	"PUT /api/v1/budget/categories/{id}":    family,
	"DELETE /api/v1/budget/categories/{id}": family,
	"GET /api/v1/budget/obligations":        readers,

	"GET /api/v1/lists":                           sitters,
	"POST /api/v1/lists":                          family,
	"GET /api/v1/lists/{id}":                      sitters,
	"PATCH /api/v1/lists/{id}":                    family,
	"DELETE /api/v1/lists/{id}":                   family,
	"POST /api/v1/lists/{id}/archive":             family,
	"POST /api/v1/lists/{id}/unarchive":           family,
	"GET /api/v1/lists/{id}/items":                sitters,
	"POST /api/v1/lists/{id}/items":               sitterEdit,
	"POST /api/v1/lists/{id}/items/check":         sitterEdit,
	"POST /api/v1/lists/{id}/items/uncheck":       sitterEdit,
	"POST /api/v1/lists/{id}/items/remove":        family,
	"PATCH /api/v1/lists/{id}/items/{itemId}":     sitterEdit, // housesitter: "checked" only (handler)
	"DELETE /api/v1/lists/{id}/items/{itemId}":    family,
	"POST /api/v1/lists/{id}/items/{itemId}/move": family,

	"GET /api/v1/recipes":                           sitters,
	"POST /api/v1/recipes":                          family,
	"GET /api/v1/recipes/{id}":                      sitters,
	"PUT /api/v1/recipes/{id}":                      family,
	"DELETE /api/v1/recipes/{id}":                   family,
	"GET /api/v1/meal-plans":                        sitters,
	"PUT /api/v1/meal-plans/{date}/{slot}":          family,
	"DELETE /api/v1/meal-plans/{date}/{slot}":       family,
	"GET /api/v1/meals/{id}":                        sitters,
	"GET /api/v1/meals/{id}/feedback":               sitters,
	"PUT /api/v1/meals/{id}/feedback/{personId}":    family,
	"DELETE /api/v1/meals/{id}/feedback/{personId}": family,

	"GET /api/v1/activity": readers, // viewer: no secret lines (handler)

	// The caller's own sign-in password: every role, self-scoped (#4822).
	"POST /api/v1/auth/change-password": everyone,

	"GET /api/v1/guides":                                      sitters,
	"POST /api/v1/guides":                                     family,
	"GET /api/v1/guides/{id}":                                 sitters,
	"PUT /api/v1/guides/{id}":                                 family,
	"DELETE /api/v1/guides/{id}":                              family,
	"GET /api/v1/guides/{id}/revisions":                       sitters,
	"GET /api/v1/guides/{id}/revisions/{revisionId}":          sitters,
	"POST /api/v1/guides/{id}/revisions/{revisionId}/restore": family,
}

var paramRe = regexp.MustCompile(`\{[^}]+\}`)

// TestProtectedRoutes_RoleMatrix sends one request per protected route per
// role through middleware.RequirePolicy (with a stub handler behind it) and
// checks the route is allowed (200) or refused (403 naming the role) exactly
// as expectedAccess says.
func TestProtectedRoutes_RoleMatrix(t *testing.T) {
	routes := protectedRoutes(t)

	var expected []string
	for k := range expectedAccess {
		expected = append(expected, k)
	}
	sort.Strings(expected)
	require.Equal(t, routes, expected, "expectedAccess must list exactly the protected routes")

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				claims := &auth.Claims{UserID: uuid.NewString(), HouseholdID: uuid.NewString(), Role: req.Header.Get("X-Test-Role")}
				next.ServeHTTP(w, req.WithContext(middleware.WithClaims(req.Context(), claims)))
			})
		})
		r.Use(middleware.RequirePolicy)
		for _, rt := range routes {
			method, pattern, _ := strings.Cut(rt, " ")
			r.Method(method, pattern, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
		}
	})

	for _, rt := range routes {
		method, pattern, _ := strings.Cut(rt, " ")
		allowed := map[string]bool{}
		for _, role := range expectedAccess[rt] {
			allowed[role] = true
		}
		path := paramRe.ReplaceAllStringFunc(pattern, func(string) string { return uuid.NewString() })
		for _, role := range auth.AllRoles {
			t.Run(rt+" as "+role, func(t *testing.T) {
				req := httptest.NewRequest(method, path, nil)
				req.Header.Set("X-Test-Role", role)
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, req)
				if allowed[role] {
					assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					return
				}
				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
				assert.Contains(t, rec.Body.String(), "the "+role+" role cannot", "403 names the role")
			})
		}
	}

	t.Run("unknown role and missing role are refused everywhere", func(t *testing.T) {
		for _, role := range []string{"", "admin", "OWNER"} {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/households/me", nil)
			req.Header.Set("X-Test-Role", role)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusForbidden, rec.Code, "role %q", role)
		}
	})
}

// TestRequirePolicy_UnmappedRoute: a route without a rule fails closed, even
// for an owner.
func TestRequirePolicy_UnmappedRoute(t *testing.T) {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				claims := &auth.Claims{UserID: uuid.NewString(), HouseholdID: uuid.NewString(), Role: auth.RoleOwner}
				next.ServeHTTP(w, req.WithContext(middleware.WithClaims(req.Context(), claims)))
			})
		})
		r.Use(middleware.RequirePolicy)
		r.Get("/api/v1/not-in-the-table", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/not-in-the-table", nil))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "no access policy")
}
