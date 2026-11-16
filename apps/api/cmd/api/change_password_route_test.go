package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
	"home-os/api/internal/config"
	"home-os/api/internal/dex"
	"home-os/api/internal/household"
	"home-os/api/internal/invite"
	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
)

const changePasswordIssuer = "https://dex.test/dex"

// changePasswordFixture mounts the real protected group - RequireAuth over the
// Postgres member resolver, RequirePolicy, the real routers - with the
// change-password route wired exactly as cmd/api/main.go wires it: the auth
// handler in front of the per-client-IP limiter and behind the role policy.
type changePasswordFixture struct {
	pool    *pgxpool.Pool
	h       http.Handler
	key     *rsa.PrivateKey
	handler *auth.Handler
}

func newChangePasswordFixture(t *testing.T, perMinute, burst int) *changePasswordFixture {
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
	v := auth.NewVerifierWithKeySet(changePasswordIssuer, ks, []string{"home-os-ui"}).
		WithResolver(auth.NewMemberResolver(pool))

	cfg := &config.Config{TypesenseHost: "127.0.0.1", TypesensePort: "1"}
	limiter := middleware.NewRateLimiter(perMinute, burst, nil)
	authHandler := auth.NewHandler(auth.NewRepo(pool), cfg, nil, nil, v).
		WithChangePasswordLimiter(limiter)
	// The handler's Dex backend is replaced below, per test.

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		r.Use(middleware.RequirePolicy)
		registerProtectedRoutes(r, protectedDeps{
			pool:                  pool,
			cfg:                   cfg,
			search:                search.NewClient(cfg),
			householdHandler:      household.NewHandler(household.NewRepo(pool)),
			inviteHandler:         invite.NewHandler(invite.NewRepo(pool), cfg, nil),
			authHandler:           authHandler,
			changePasswordLimiter: limiter,
		})
	})
	return &changePasswordFixture{pool: pool, h: r, key: key, handler: authHandler}
}

// token signs a Dex-shaped id token for sub/email.
func (f *changePasswordFixture) token(t *testing.T, sub, email string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": changePasswordIssuer, "aud": "home-os-ui", "sub": sub, "email": email,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	str, err := tok.SignedString(f.key)
	require.NoError(t, err)
	return str
}

// member inserts a user on role in a fresh household and returns its token.
func (f *changePasswordFixture) member(t *testing.T, role string) (token, sub, email, userID string) {
	t.Helper()
	ctx := context.Background()
	sub = "CiQ" + uuid.NewString() + "EgVsb2NhbA"
	email = "chpw-" + uuid.NewString()[:8] + "@example.com"
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, 'Chpw', 'x', $2) RETURNING id::text`,
		email, sub).Scan(&userID))
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	var hhID string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Chpw HH') RETURNING id::text`).Scan(&hhID))
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hhID) })
	_, err := f.pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hhID, userID, role)
	require.NoError(t, err)
	return f.token(t, sub, email), sub, email, userID
}

func (f *changePasswordFixture) post(t *testing.T, token, remoteAddr string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/change-password", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

// recordingPasswords is a Dex stand-in that records what reached it.
type recordingPasswords struct {
	verified int
	updated  []string
}

func (p *recordingPasswords) VerifyPassword(_ context.Context, _, plaintext string) error {
	p.verified++
	if plaintext != "the-real-password" {
		return dex.ErrPasswordNotVerified
	}
	return nil
}

func (p *recordingPasswords) UpdatePassword(_ context.Context, email, _ string) error {
	p.updated = append(p.updated, email)
	return nil
}

func (p *recordingPasswords) RefreshClientIDs(context.Context, string) ([]string, error) {
	return []string{"home-os-ui"}, nil
}

func (p *recordingPasswords) RevokeRefresh(context.Context, string, string) error { return nil }

// TestChangePasswordRoute_PerClientIPLimit: the route sits behind the per-IP
// budget as well as the per-account one, so a flood from one address answers
// 429 with Retry-After before the token is verified and before Dex is called,
// and a different address is unaffected.
func TestChangePasswordRoute_PerClientIPLimit(t *testing.T) {
	f := newChangePasswordFixture(t, 10, 2) // burst 2
	fake := &recordingPasswords{}
	f.handler.WithPasswordService(fake)

	token, _, _, _ := f.member(t, auth.RoleFamilyMember)
	body := map[string]string{"current_password": "the-real-password", "new_password": "the-new-password"}

	for i := range 2 {
		rec := f.post(t, token, "203.0.113.7:1234", body)
		require.Equal(t, http.StatusOK, rec.Code, "attempt %d: %s", i, rec.Body.String())
	}
	verifiedBefore := fake.verified

	rec := f.post(t, token, "203.0.113.7:1234", body)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	assert.NotEmpty(t, rec.Header().Get("Retry-After"), "429 must say how long to wait")
	assert.Equal(t, "RATE_LIMITED", decodeRouteError(t, rec).Error.Code)
	assert.Equal(t, verifiedBefore, fake.verified, "a limited request must not reach Dex")

	// A bad token from a fresh address is refused by RequireAuth, not by the
	// limiter, and does not spend another client's budget.
	rec = f.post(t, "not-a-jwt", "198.51.100.9:1234", body)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
}

type routeErrorBody struct {
	Error struct{ Code, Message string } `json:"error"`
}

func decodeRouteError(t *testing.T, rec *httptest.ResponseRecorder) routeErrorBody {
	t.Helper()
	var body routeErrorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body
}

// TestChangePasswordRoute_EveryRoleMayChangeItsOwnPassword: the route rule
// grants password/write to every role, and the handler only ever acts on the
// verified caller, so a viewer or a vendor changing their own password is not a
// privilege question. Kept here (not only in the policy matrix) because it is
// the route's reason for existing.
func TestChangePasswordRoute_EveryRoleMayChangeItsOwnPassword(t *testing.T) {
	f := newChangePasswordFixture(t, 100, 20)
	fake := &recordingPasswords{}
	f.handler.WithPasswordService(fake)

	for _, role := range auth.AllRoles {
		t.Run(role, func(t *testing.T) {
			token, _, email, _ := f.member(t, role)
			rec := f.post(t, token, "203.0.113."+fmt.Sprint(len(role))+":1000", map[string]string{
				"current_password": "the-real-password", "new_password": "the-new-password"})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Contains(t, fake.updated, email, "the caller's own account changed")
		})
	}
}

// TestChangePasswordRoute_IsInsideAuthAndPolicy: a request with no token is
// 401 (RequireAuth runs first), and the route has a policy rule (a route
// without one answers 403 for everyone and cmd/api's route test fails).
func TestChangePasswordRoute_IsInsideAuthAndPolicy(t *testing.T) {
	f := newChangePasswordFixture(t, 100, 20)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/change-password",
		strings.NewReader(`{"current_password":"a","new_password":"the-new-password"}`))
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())

	_, ok := middleware.RuleFor(http.MethodPost, "/api/v1/auth/change-password")
	assert.True(t, ok, "the route must have a rule in middleware/roles.go")
}
