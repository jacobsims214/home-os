package agenttoken_test

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

	"home-os/api/internal/agenttoken"
	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
)

const testIssuer = "https://dex.test/dex"

// testDB connects to TEST_DATABASE_URL (all migrations applied). Skips when unset.
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

// router mirrors cmd/api/main.go: the agent token routes behind the real
// RequireAuth middleware with the Postgres member resolver.
func router(pool *pgxpool.Pool, v *auth.Verifier) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		agenttoken.NewHandler(agenttoken.NewRepo(pool)).RegisterRoutes(r)
	})
	return r
}

type tokenJSON struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TokenPrefix string   `json:"token_prefix"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   *string  `json:"expires_at"`
	RevokedAt   *string  `json:"revoked_at"`
	CreatedAt   string   `json:"created_at"`
	Token       string   `json:"token"`
}

func call(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
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

// member inserts a user with a household membership and returns a Dex-style
// token for them plus their user and household IDs. hhID reuses a household
// when non-empty.
func member(t *testing.T, pool *pgxpool.Pool, key *rsa.PrivateKey, hhID string) (token, userID, householdID string) {
	t.Helper()
	ctx := context.Background()
	email := "agenttoken-" + uuid.NewString()[:8] + "@example.com"
	sub := "CiQ" + uuid.NewString() + "EgVsb2NhbA"
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, 'Token Member', 'x', $2) RETURNING id::text`,
		email, sub).Scan(&userID))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	if hhID == "" {
		require.NoError(t, pool.QueryRow(ctx,
			`INSERT INTO households (name) VALUES ('Token HH') RETURNING id::text`).Scan(&hhID))
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hhID) })
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'family_member')`, hhID, userID)
	require.NoError(t, err)

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": testIssuer, "aud": "home-os-ui", "sub": sub, "email": email,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	token, err = tok.SignedString(key)
	require.NoError(t, err)
	return token, userID, hhID
}

func setup(t *testing.T) (*pgxpool.Pool, http.Handler, *rsa.PrivateKey) {
	t.Helper()
	pool := testDB(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{key.Public()}}
	v := auth.NewVerifierWithKeySet(testIssuer, ks, []string{"home-os-ui"}).
		WithResolver(auth.NewMemberResolver(pool))
	return pool, router(pool, v), key
}

func TestHashToken(t *testing.T) {
	a := agenttoken.HashToken("hos_abc")
	assert.Len(t, a, 32, "sha-256 fits the octet_length(token_hash) = 32 check")
	assert.Equal(t, a, agenttoken.HashToken("hos_abc"))
	assert.NotEqual(t, a, agenttoken.HashToken("hos_abd"))
}

func TestAgentTokens_CreateListRevoke(t *testing.T) {
	pool, h, key := setup(t)
	ctx := context.Background()
	token, userID, hhID := member(t, pool, key, "")
	// Same household, different member: must not see or revoke the token.
	siblingToken, _, _ := member(t, pool, key, hhID)

	expires := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	rec := call(t, h, http.MethodPost, "/api/v1/agent-tokens", token, map[string]any{
		"name": " Moltis laptop ", "scopes": []string{"read", "write", "read"}, "expires_at": expires.Format(time.RFC3339),
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	created := decode[tokenJSON](t, rec)

	t.Run("create returns the plaintext once with hos_ prefix", func(t *testing.T) {
		require.True(t, strings.HasPrefix(created.Token, "hos_"), created.Token)
		assert.Len(t, created.Token, len("hos_")+43, "32 random bytes, base64url")
		assert.Equal(t, created.Token[:12], created.TokenPrefix)
		assert.Equal(t, "Moltis laptop", created.Name)
		assert.Equal(t, []string{"read", "write"}, created.Scopes)
		require.NotNil(t, created.ExpiresAt)
		assert.Equal(t, expires.Format(time.RFC3339), *created.ExpiresAt)
		assert.Nil(t, created.RevokedAt)
	})

	t.Run("only the hash is stored", func(t *testing.T) {
		var hash []byte
		var owner, hh, prefix string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT token_hash, user_id::text, household_id::text, token_prefix FROM agent_tokens WHERE id = $1`,
			created.ID).Scan(&hash, &owner, &hh, &prefix))
		assert.Equal(t, agenttoken.HashToken(created.Token), hash)
		assert.NotContains(t, string(hash), created.Token)
		assert.Equal(t, userID, owner)
		assert.Equal(t, hhID, hh)
		assert.Equal(t, created.TokenPrefix, prefix)

		var n int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM agent_tokens t WHERE t::text LIKE '%' || $1 || '%'`,
			created.Token[len("hos_"):]).Scan(&n))
		assert.Zero(t, n, "no column holds the plaintext secret")
	})

	t.Run("list never returns secrets", func(t *testing.T) {
		rec := call(t, h, http.MethodGet, "/api/v1/agent-tokens", token, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body := rec.Body.String()
		assert.NotContains(t, body, created.Token)
		assert.NotContains(t, body, `"token"`)
		assert.NotContains(t, body, "hash")
		list := decode[[]tokenJSON](t, rec)
		require.Len(t, list, 1)
		assert.Equal(t, created.ID, list[0].ID)
		assert.Equal(t, created.TokenPrefix, list[0].TokenPrefix)
		assert.Empty(t, list[0].Token)
	})

	t.Run("another member's list is empty", func(t *testing.T) {
		rec := call(t, h, http.MethodGet, "/api/v1/agent-tokens", siblingToken, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Empty(t, decode[[]tokenJSON](t, rec))
	})

	t.Run("another member cannot revoke it", func(t *testing.T) {
		rec := call(t, h, http.MethodDelete, "/api/v1/agent-tokens/"+created.ID, siblingToken, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		var revoked *time.Time
		require.NoError(t, pool.QueryRow(ctx, `SELECT revoked_at FROM agent_tokens WHERE id = $1`, created.ID).Scan(&revoked))
		assert.Nil(t, revoked)
	})

	t.Run("revoke sets revoked_at and is idempotent", func(t *testing.T) {
		rec := call(t, h, http.MethodDelete, "/api/v1/agent-tokens/"+created.ID, token, nil)
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		var first time.Time
		require.NoError(t, pool.QueryRow(ctx, `SELECT revoked_at FROM agent_tokens WHERE id = $1`, created.ID).Scan(&first))

		rec = call(t, h, http.MethodDelete, "/api/v1/agent-tokens/"+created.ID, token, nil)
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		var second time.Time
		require.NoError(t, pool.QueryRow(ctx, `SELECT revoked_at FROM agent_tokens WHERE id = $1`, created.ID).Scan(&second))
		assert.True(t, first.Equal(second), "second revoke keeps the original revoked_at")

		list := decode[[]tokenJSON](t, call(t, h, http.MethodGet, "/api/v1/agent-tokens", token, nil))
		require.Len(t, list, 1)
		assert.NotNil(t, list[0].RevokedAt)
	})

	t.Run("unknown and malformed ids", func(t *testing.T) {
		rec := call(t, h, http.MethodDelete, "/api/v1/agent-tokens/"+uuid.NewString(), token, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		rec = call(t, h, http.MethodDelete, "/api/v1/agent-tokens/not-a-uuid", token, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("each token is unique", func(t *testing.T) {
		rec := call(t, h, http.MethodPost, "/api/v1/agent-tokens", token, map[string]any{"name": "second", "scopes": []string{"read"}})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		second := decode[tokenJSON](t, rec)
		assert.NotEqual(t, created.Token, second.Token)
		assert.Nil(t, second.ExpiresAt, "expiry is optional")
		list := decode[[]tokenJSON](t, call(t, h, http.MethodGet, "/api/v1/agent-tokens", token, nil))
		require.Len(t, list, 2)
		assert.Equal(t, second.ID, list[0].ID, "newest first")
	})
}

func TestAgentTokens_CreateValidation(t *testing.T) {
	pool, h, key := setup(t)
	token, _, _ := member(t, pool, key, "")

	tests := []struct {
		name string
		body any
	}{
		{"missing name", map[string]any{"scopes": []string{"read"}}},
		{"blank name", map[string]any{"name": "   ", "scopes": []string{"read"}}},
		{"name too long", map[string]any{"name": strings.Repeat("x", 256), "scopes": []string{"read"}}},
		{"no scopes", map[string]any{"name": "a"}},
		{"empty scopes", map[string]any{"name": "a", "scopes": []string{}}},
		{"unknown scope", map[string]any{"name": "a", "scopes": []string{"read", "admin"}}},
		{"expiry in the past", map[string]any{"name": "a", "scopes": []string{"read"}, "expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339)}},
		{"expiry not RFC3339", map[string]any{"name": "a", "scopes": []string{"read"}, "expires_at": "tomorrow"}},
		{"not JSON", "nope"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(t, h, http.MethodPost, "/api/v1/agent-tokens", token, tc.body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

func TestAgentTokens_RequireAuth(t *testing.T) {
	_, h, _ := setup(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/agent-tokens"},
		{http.MethodGet, "/api/v1/agent-tokens"},
		{http.MethodDelete, "/api/v1/agent-tokens/" + uuid.NewString()},
	} {
		rec := call(t, h, tc.method, tc.path, "", nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, tc.method+" "+tc.path)
	}
}
