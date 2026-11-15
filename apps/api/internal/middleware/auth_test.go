package middleware_test

import (
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
	"home-os/api/internal/bill"
	"home-os/api/internal/config"
	"home-os/api/internal/middleware"
)

const testIssuer = "https://dex.test/dex"

type tokenSigner struct{ key *rsa.PrivateKey }

func newSigner(t *testing.T) *tokenSigner {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return &tokenSigner{key: k}
}

func (s *tokenSigner) token(t *testing.T, sub, email, aud string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":   testIssuer,
		"aud":   aud,
		"sub":   sub,
		"email": email,
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	str, err := tok.SignedString(s.key)
	require.NoError(t, err)
	return str
}

func (s *tokenSigner) verifier() *auth.Verifier {
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{s.key.Public()}}
	return auth.NewVerifierWithKeySet(testIssuer, ks, []string{"home-os-ui", "home-os-mcp"})
}

func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// router mirrors cmd/api/main.go: /auth/me outside the middleware (it verifies
// the token itself), /bills behind RequireAuth.
func router(pool *pgxpool.Pool, v *auth.Verifier) http.Handler {
	r := chi.NewRouter()
	authHandler := auth.NewHandler(auth.NewRepo(pool), nil, &config.Config{}, nil, nil, v)
	r.Get("/api/v1/auth/me", authHandler.Me)
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		r.Get("/api/v1/bills", bill.NewHandler(bill.NewRepo(pool)).List)
		r.Get("/api/v1/whoami", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(middleware.ClaimsFromContext(r.Context()))
		})
	})
	return r
}

func do(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRequireAuth_ResolvesDexUserToMember(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	s := newSigner(t)
	v := s.verifier().WithResolver(auth.NewMemberResolver(pool))
	h := router(pool, v)

	// A legacy member: no dex_sub yet, matched by email on first request.
	email := "member-" + uuid.NewString()[:8] + "@example.com"
	var userID, hhID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, 'Member', 'x') RETURNING id::text`,
		email).Scan(&userID))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Test HH') RETURNING id::text`).Scan(&hhID))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM households WHERE id = $1`, hhID) })
	_, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'owner')`, hhID, userID)
	require.NoError(t, err)

	// Dex local-user style sub: base64 protobuf wrapping a UUID.
	sub := "CiQ" + uuid.NewString() + "EgVsb2NhbA"
	tok := s.token(t, sub, email, "home-os-ui")

	t.Run("auth/me 200", func(t *testing.T) {
		rec := do(h, "/api/v1/auth/me", tok)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body struct{ ID, Email string }
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, userID, body.ID)
		assert.Equal(t, email, body.Email)
	})

	t.Run("dex_sub backfilled after email match", func(t *testing.T) {
		var got *string
		require.NoError(t, pool.QueryRow(ctx, `SELECT dex_sub FROM users WHERE id = $1`, userID).Scan(&got))
		require.NotNil(t, got)
		assert.Equal(t, sub, *got)
	})

	t.Run("bills 200", func(t *testing.T) {
		rec := do(h, "/api/v1/bills", tok)
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	t.Run("claims carry users.id, household and role", func(t *testing.T) {
		// Token with a different email still resolves via the stored dex_sub.
		rec := do(h, "/api/v1/whoami", s.token(t, sub, "changed@example.com", "home-os-mcp"))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var c auth.Claims
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &c))
		assert.Equal(t, userID, c.UserID)
		assert.Equal(t, hhID, c.HouseholdID)
		assert.Equal(t, "owner", c.Role)
	})
}

func TestRequireAuth_NonMemberAndBadTokens(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	s := newSigner(t)
	h := router(pool, s.verifier().WithResolver(auth.NewMemberResolver(pool)))

	// A user row with no membership.
	email := "nomember-" + uuid.NewString()[:8] + "@example.com"
	var userID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, 'X', 'x') RETURNING id::text`,
		email).Scan(&userID))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })

	nonMember := s.token(t, "CiQ"+uuid.NewString(), email, "home-os-ui")
	stranger := s.token(t, "CiQ"+uuid.NewString(), "stranger-"+uuid.NewString()[:8]+"@example.com", "home-os-ui")
	other := newSigner(t).token(t, "CiQforged", email, "home-os-ui")
	wrongAud := s.token(t, "CiQ"+uuid.NewString(), email, "someone-else")

	tests := []struct {
		name, path, token string
		want              int
		code              string
	}{
		{"non-member bills", "/api/v1/bills", nonMember, http.StatusForbidden, "FORBIDDEN"},
		{"non-member auth/me", "/api/v1/auth/me", nonMember, http.StatusForbidden, "FORBIDDEN"},
		{"unknown identity bills", "/api/v1/bills", stranger, http.StatusForbidden, "FORBIDDEN"},
		{"missing token", "/api/v1/bills", "", http.StatusUnauthorized, "UNAUTHORIZED"},
		{"wrong signing key", "/api/v1/bills", other, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"wrong audience", "/api/v1/bills", wrongAud, http.StatusUnauthorized, "UNAUTHORIZED"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(h, tc.path, tc.token)
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
			var body struct {
				Error struct{ Code, Message string } `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tc.code, body.Error.Code)
		})
	}
}
