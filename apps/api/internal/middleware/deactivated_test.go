package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/admin"
	"home-os/api/internal/auth"
	"home-os/api/internal/config"
	"home-os/api/internal/household"
	"home-os/api/internal/invite"
	"home-os/api/internal/middleware"
)

// route is one registration found in the API's source.
type route struct{ method, pattern string }

var routeRe = regexp.MustCompile(`r\.(Get|Post|Put|Patch|Delete)\("([^"]+)"`)

func parseRoutes(t *testing.T, src string) []route {
	t.Helper()
	var out []route
	for _, m := range routeRe.FindAllStringSubmatch(src, -1) {
		out = append(out, route{strings.ToUpper(m[1]), m[2]})
	}
	return out
}

func readSource(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	require.NoError(t, err)
	return string(b)
}

// apiRoutes reads cmd/api/main.go and cmd/api/routes.go and returns the
// routes registered inside the RequireAuth group (registerProtectedRoutes,
// plus agent tokens, which register there) and the
// ones registered outside it. Reading the source keeps the walk complete as
// routes are added: a new protected route is walked automatically, and a new
// route outside RequireAuth fails the test until it is classified below.
func apiRoutes(t *testing.T) (protected, outside []route) {
	t.Helper()
	src := readSource(t, "cmd/api/main.go")
	start := strings.Index(src, "r.Use(middleware.RequireAuth(")
	require.Positive(t, start, "RequireAuth group not found in cmd/api/main.go")
	end := strings.Index(src[start:], "\n\t})\n")
	require.Positive(t, end, "end of RequireAuth group not found")
	// The group mounts registerProtectedRoutes (cmd/api/routes.go, #4689).
	require.Contains(t, src[start:start+end], "registerProtectedRoutes(r,", "protected routes moved; update this test")
	routesSrc := readSource(t, "cmd/api/routes.go")
	pstart := strings.Index(routesSrc, "func registerProtectedRoutes(")
	require.Positive(t, pstart, "registerProtectedRoutes not found in cmd/api/routes.go")
	pend := strings.Index(routesSrc[pstart:], "\n}\n")
	require.Positive(t, pend, "end of registerProtectedRoutes not found")
	body := routesSrc[pstart : pstart+pend]
	protected = parseRoutes(t, body)
	require.Contains(t, body, "agenttoken.NewHandler(", "agent tokens moved; update this test")
	protected = append(protected, parseRoutes(t, readSource(t, "internal/agenttoken/handler.go"))...)

	outside = parseRoutes(t, src[:start])
	require.Contains(t, src[:start], "adminHandler.RegisterRoutes(r)")
	outside = append(outside, parseRoutes(t, readSource(t, "internal/admin/handler.go"))...)
	require.Greater(t, len(protected), 50, "suspiciously few protected routes parsed")
	return protected, outside
}

var paramRe = regexp.MustCompile(`\{[^}]+\}`)

func concrete(pattern string) string {
	return paramRe.ReplaceAllString(pattern, uuid.NewString())
}

type noDex struct{ t *testing.T }

func (d noDex) CreatePassword(context.Context, string, string, string) error {
	d.t.Error("Dex must not be called for a deactivated user")
	return nil
}

type deactivatedFixture struct {
	pool      *pgxpool.Pool
	h         http.Handler
	signer    *tokenSigner
	ownerHH   string            // household the invitations are for
	inviteFor map[string]string // email -> pending invitation token
}

// identity is one caller: a Dex token plus the users row it maps to.
type identity struct {
	name   string
	userID string
	email  string
	token  string
}

func newDeactivatedFixture(t *testing.T) *deactivatedFixture {
	t.Helper()
	pool := testDB(t)
	s := newSigner(t)
	v := s.verifier().WithResolver(auth.NewMemberResolver(pool))

	// The router mirrors cmd/api/main.go: real handlers for every route
	// outside RequireAuth (they verify the token themselves); a sentinel
	// behind RequireAuth for the protected group, since the middleware
	// alone decides whether a deactivated caller gets through.
	householdHandler := household.NewHandler(household.NewRepo(pool)).WithVerifier(v)
	inviteHandler := invite.NewHandler(invite.NewRepo(pool), &config.Config{}, nil).
		WithVerifier(v).WithDex(noDex{t})
	authHandler := auth.NewHandler(auth.NewRepo(pool), &config.Config{}, nil, nil, v)

	r := chi.NewRouter()
	r.Get("/api/v1/auth/me", authHandler.Me)
	r.Post("/api/v1/auth/caldav-password", authHandler.GenerateCaldavPassword)
	r.Post("/api/v1/invites/accept", inviteHandler.AcceptInvite)
	r.Post("/api/v1/households", householdHandler.Create)
	admin.NewHandler(admin.NewRepo(pool), v).RegisterRoutes(r)

	protected, _ := apiRoutes(t)
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		for _, rt := range protected {
			r.Method(rt.method, rt.pattern, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
		}
	})

	f := &deactivatedFixture{pool: pool, h: r, signer: s, inviteFor: map[string]string{}}
	ctx := context.Background()
	var ownerID string
	ownerEmail := "owner-" + uuid.NewString() + "@example.test"
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, 'Owner', 'x') RETURNING id::text`,
		ownerEmail).Scan(&ownerID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Inviting HH') RETURNING id::text`).Scan(&f.ownerHH))
	_, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'owner')`, f.ownerHH, ownerID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM households WHERE id = $1`, f.ownerHH)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, ownerID)
	})
	return f
}

// user inserts an instance admin who is a member of their own household
// (so every route would let them in if active), with a pending invitation
// to the owner's household. linked=false leaves users.dex_sub NULL, so the
// resolver has to match the token by email.
func (f *deactivatedFixture) user(t *testing.T, name string, active, linked bool) identity {
	t.Helper()
	ctx := context.Background()
	sub := "CiQ" + uuid.NewString()
	email := name + "-" + uuid.NewString()[:8] + "@example.test"
	var storedSub *string
	if linked {
		storedSub = &sub
	}
	var id, hh string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub, is_instance_admin, active)
		 VALUES ($1, $2, 'x', $3, true, $4) RETURNING id::text`,
		email, name, storedSub, active).Scan(&id))
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Own HH') RETURNING id::text`).Scan(&hh))
	_, err := f.pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'owner')`, hh, id)
	require.NoError(t, err)
	tok := "inv-" + uuid.NewString()
	_, err = f.pool.Exec(ctx,
		`INSERT INTO invitations (household_id, email, token, role, invited_by, expires_at)
		 SELECT $1, $2, $3, 'family_member', user_id, NOW() + interval '1 day'
		 FROM memberships WHERE household_id = $1 LIMIT 1`, f.ownerHH, email, tok)
	require.NoError(t, err)
	f.inviteFor[email] = tok
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM households WHERE id IN
			(SELECT household_id FROM memberships WHERE user_id = $1)`, id)
		_, _ = f.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	})
	return identity{name: name, userID: id, email: email, token: f.signer.token(t, sub, email, "home-os-ui")}
}

type call struct {
	name        string
	method      string
	path        string
	body        any
	noAuth      bool // the invite signup path sends no token
	householdOf bool // send X-Household-ID with the caller's own household
}

func (f *deactivatedFixture) do(t *testing.T, id identity, c call) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if c.body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(c.body))
	}
	req := httptest.NewRequest(c.method, c.path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if !c.noAuth {
		req.Header.Set("Authorization", "Bearer "+id.token)
	}
	if c.householdOf {
		var hh string
		require.NoError(t, f.pool.QueryRow(context.Background(),
			`SELECT household_id::text FROM memberships WHERE user_id = $1 LIMIT 1`, id.userID).Scan(&hh))
		req.Header.Set(auth.HouseholdHeader, hh)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

// calls lists every API path a token can reach: each protected route, each
// route outside RequireAuth that checks a token itself, and both invite
// accept paths. Public routes that take no token are listed in public.
func (f *deactivatedFixture) calls(t *testing.T, id identity) []call {
	t.Helper()
	protected, outside := apiRoutes(t)
	var cs []call
	for _, rt := range protected {
		cs = append(cs, call{name: rt.method + " " + rt.pattern, method: rt.method, path: concrete(rt.pattern)})
	}
	cs = append(cs, call{name: "GET /api/v1/households/me with X-Household-ID", method: http.MethodGet,
		path: "/api/v1/households/me", householdOf: true})

	self := map[route]call{
		{http.MethodGet, "/api/v1/auth/me"}:               {},
		{http.MethodPost, "/api/v1/auth/caldav-password"}: {},
		{http.MethodPost, "/api/v1/households"}: {body: map[string]string{
			"name": "Should Not Exist", "timezone": "America/Chicago"}},
		{http.MethodPost, "/api/v1/invites/accept"}: {body: map[string]string{"token": f.inviteFor[id.email]}},
	}
	public := map[route]bool{ // no token: nothing to refuse
		{http.MethodGet, "/api/v1/health"}:                true,
		{http.MethodPost, "/api/v1/auth/forgot-password"}: true,
		{http.MethodPost, "/api/v1/auth/reset-password"}:  true,
	}
	for _, rt := range outside {
		if public[rt] {
			continue
		}
		c, ok := self[rt]
		if !ok && strings.HasPrefix(rt.pattern, "/api/v1/admin/") {
			c, ok = call{body: map[string]string{}}, true
		}
		require.True(t, ok, "route %s %s is outside RequireAuth and not classified in this test", rt.method, rt.pattern)
		c.name, c.method, c.path = rt.method+" "+rt.pattern, rt.method, concrete(rt.pattern)
		cs = append(cs, c)
	}
	cs = append(cs, call{name: "POST /api/v1/invites/accept signup (no token)", method: http.MethodPost,
		path: "/api/v1/invites/accept", noAuth: true, body: map[string]string{
			"token": f.inviteFor[id.email], "name": "Signup", "password": "long-enough-password"}})
	return cs
}

// TestDeactivatedUserRefusedEverywhere walks every API path with the token of
// a deactivated user (an instance admin and household owner, so only
// users.active stands in the way) and expects 403 "this account has been
// deactivated" from each, and no writes. An active twin walks the same paths
// as a control: none of them may answer 401 or mention deactivation.
func TestDeactivatedUserRefusedEverywhere(t *testing.T) {
	f := newDeactivatedFixture(t)
	ctx := context.Background()

	deactivated := []identity{
		f.user(t, "deact-linked", false, true),
		f.user(t, "deact-email", false, false),
	}
	for _, id := range deactivated {
		t.Run(id.name, func(t *testing.T) {
			var householdsBefore int
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM households`).Scan(&householdsBefore))

			for _, c := range f.calls(t, id) {
				t.Run(c.name, func(t *testing.T) {
					rec := f.do(t, id, c)
					require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
					var body struct {
						Error struct{ Code, Message string } `json:"error"`
					}
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
					assert.Equal(t, "FORBIDDEN", body.Error.Code)
					assert.Equal(t, "this account has been deactivated", body.Error.Message)
				})
			}

			var householdsAfter int
			var accepted, hasCaldav, linked bool
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM households`).Scan(&householdsAfter))
			require.NoError(t, f.pool.QueryRow(ctx,
				`SELECT accepted_at IS NOT NULL FROM invitations WHERE token = $1`, f.inviteFor[id.email]).Scan(&accepted))
			require.NoError(t, f.pool.QueryRow(ctx,
				`SELECT caldav_password_hash IS NOT NULL, dex_sub IS NOT NULL FROM users WHERE id = $1`, id.userID).
				Scan(&hasCaldav, &linked))
			assert.Equal(t, householdsBefore, householdsAfter, "no household may be created")
			assert.False(t, accepted, "the invitation must stay pending")
			assert.False(t, hasCaldav, "no CalDAV password may be stored")
			if id.name == "deact-email" {
				assert.False(t, linked, "dex_sub must not be backfilled for a deactivated user")
			}
		})
	}

	t.Run("active control", func(t *testing.T) {
		id := f.user(t, "active", true, true)
		for _, c := range f.calls(t, id) {
			t.Run(c.name, func(t *testing.T) {
				rec := f.do(t, id, c)
				assert.NotEqual(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
				assert.NotContains(t, rec.Body.String(), "deactivated")
			})
		}
	})
}
