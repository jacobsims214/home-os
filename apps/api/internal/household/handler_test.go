package household_test

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
	"sort"
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
	"home-os/api/internal/household"
	"home-os/api/internal/middleware"
)

const testIssuer = "https://dex.test/dex"

type signer struct{ key *rsa.PrivateKey }

func newSigner(t *testing.T) *signer {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return &signer{key: k}
}

func (s *signer) token(t *testing.T, sub, email string) string {
	t.Helper()
	str, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": testIssuer, "aud": "home-os-ui", "sub": sub, "email": email, "name": "New Parent",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(s.key)
	require.NoError(t, err)
	return str
}

func (s *signer) verifier(pool *pgxpool.Pool) *auth.Verifier {
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{s.key.Public()}}
	return auth.NewVerifierWithKeySet(testIssuer, ks, []string{"home-os-ui"}).
		WithResolver(auth.NewMemberResolver(pool))
}

// testDB connects to TEST_DATABASE_URL (a database with every migration in
// packages/db/migrations applied; a fresh one needs CREATE ROLE app first).
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

// router mirrors cmd/api/main.go: create outside RequireAuth, /me inside.
func router(pool *pgxpool.Pool, v *auth.Verifier) http.Handler {
	repo := household.NewRepo(pool)
	h := household.NewHandler(repo).WithVerifier(v)
	r := chi.NewRouter()
	r.Post("/api/v1/households", h.Create)
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		r.Get("/api/v1/households/me", h.GetMe)
	})
	return r
}

func do(h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type created struct {
	Data struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Timezone string `json:"timezone"`
		UserID   string `json:"user_id"`
		Role     string `json:"role"`
	} `json:"data"`
}

func uniq(prefix string) (sub, email string) {
	id := uuid.NewString()
	return prefix + "-sub-" + id, prefix + "-" + id + "@example.test"
}

func insertUser(t *testing.T, pool *pgxpool.Pool, email string, admin, active bool) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO users (email, name, password_hash, is_instance_admin, active)
		 VALUES ($1, 'Existing', 'x', $2, $3) RETURNING id::text`, email, admin, active,
	).Scan(&id))
	return id
}

func insertHouseholdWithMember(t *testing.T, pool *pgxpool.Pool, userID string) string {
	t.Helper()
	ctx := context.Background()
	var hid string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Existing') RETURNING id::text`).Scan(&hid))
	_, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'family_member')`, hid, userID)
	require.NoError(t, err)
	return hid
}

func countHouseholdsFor(t *testing.T, pool *pgxpool.Pool, userID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM memberships WHERE user_id = $1`, userID).Scan(&n))
	return n
}

func TestCreate_NewDexUserBecomesOwnerWithDefaultCalendars(t *testing.T) {
	pool := testDB(t)
	s := newSigner(t)
	v := s.verifier(pool)
	h := router(pool, v)
	ctx := context.Background()

	sub, email := uniq("newdex")
	tok := s.token(t, sub, email)

	// Before onboarding the token is valid but not a member.
	require.Equal(t, http.StatusForbidden, do(h, http.MethodGet, "/api/v1/households/me", tok, nil).Code)

	rec := do(h, http.MethodPost, "/api/v1/households", tok,
		map[string]string{"name": "  The Sims  ", "timezone": "America/Chicago"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var got created
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "The Sims", got.Data.Name)
	assert.Equal(t, "America/Chicago", got.Data.Timezone)
	assert.Equal(t, "owner", got.Data.Role)

	// A Dex-only user row was created carrying the sub.
	var dbSub, dbEmail, dbHash string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT dex_sub, email, password_hash FROM users WHERE id = $1`, got.Data.UserID,
	).Scan(&dbSub, &dbEmail, &dbHash))
	assert.Equal(t, sub, dbSub)
	assert.Equal(t, email, dbEmail)
	assert.Equal(t, "!dex-oidc", dbHash)

	// Owner membership.
	var role string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
		got.Data.ID, got.Data.UserID).Scan(&role))
	assert.Equal(t, "owner", role)

	// Exactly the three default calendars.
	rows, err := pool.Query(ctx, `SELECT type::text FROM calendars WHERE household_id = $1`, got.Data.ID)
	require.NoError(t, err)
	var types []string
	for rows.Next() {
		var ty string
		require.NoError(t, rows.Scan(&ty))
		types = append(types, ty)
	}
	rows.Close()
	sort.Strings(types)
	assert.Equal(t, []string{"bills", "family", "maintenance"}, types)

	// The same token now resolves to the new household through RequireAuth.
	me := do(h, http.MethodGet, "/api/v1/households/me", tok, nil)
	require.Equal(t, http.StatusOK, me.Code, me.Body.String())
	assert.Contains(t, me.Body.String(), got.Data.ID)
}

func TestCreate_ExistingEmailUserWithoutMembershipBackfillsSub(t *testing.T) {
	pool := testDB(t)
	s := newSigner(t)
	h := router(pool, s.verifier(pool))

	sub, email := uniq("emailmatch")
	uid := insertUser(t, pool, email, false, true)

	rec := do(h, http.MethodPost, "/api/v1/households", s.token(t, sub, email),
		map[string]string{"name": "Email Match", "timezone": "Europe/London"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var got created
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, uid, got.Data.UserID)

	var dbSub string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT dex_sub FROM users WHERE id = $1`, uid).Scan(&dbSub))
	assert.Equal(t, sub, dbSub)
}

func TestCreate_Validation(t *testing.T) {
	pool := testDB(t)
	s := newSigner(t)
	h := router(pool, s.verifier(pool))

	tests := []struct {
		name string
		body any
	}{
		{"unknown timezone", map[string]string{"name": "F", "timezone": "Mars/Olympus_Mons"}},
		{"garbage timezone", map[string]string{"name": "F", "timezone": "not a zone"}},
		{"Local is not a zone name", map[string]string{"name": "F", "timezone": "Local"}},
		{"missing timezone", map[string]string{"name": "F"}},
		{"blank timezone", map[string]string{"name": "F", "timezone": "   "}},
		{"missing name", map[string]string{"timezone": "UTC"}},
		{"blank name", map[string]string{"name": "  ", "timezone": "UTC"}},
		{"name too long", map[string]string{"name": string(bytes.Repeat([]byte("a"), 256)), "timezone": "UTC"}},
		{"not json", "{"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sub, email := uniq("invalid")
			rec := do(h, http.MethodPost, "/api/v1/households", s.token(t, sub, email), tc.body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

			// Nothing was written: no user row for the identity.
			var n int
			require.NoError(t, pool.QueryRow(context.Background(),
				`SELECT count(*) FROM users WHERE dex_sub = $1 OR email = $2`, sub, email).Scan(&n))
			assert.Zero(t, n)
		})
	}
}

func TestCreate_AccessRules(t *testing.T) {
	pool := testDB(t)
	s := newSigner(t)
	h := router(pool, s.verifier(pool))
	body := map[string]string{"name": "Another Family", "timezone": "UTC"}

	t.Run("existing member is refused", func(t *testing.T) {
		sub, email := uniq("member")
		uid := insertUser(t, pool, email, false, true)
		insertHouseholdWithMember(t, pool, uid)

		rec := do(h, http.MethodPost, "/api/v1/households", s.token(t, sub, email), body)
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, 1, countHouseholdsFor(t, pool, uid))
	})

	t.Run("instance admin with a membership may create another", func(t *testing.T) {
		sub, email := uniq("admin")
		uid := insertUser(t, pool, email, true, true)
		insertHouseholdWithMember(t, pool, uid)

		rec := do(h, http.MethodPost, "/api/v1/households", s.token(t, sub, email), body)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		assert.Equal(t, 2, countHouseholdsFor(t, pool, uid))
	})

	t.Run("deactivated user is refused", func(t *testing.T) {
		sub, email := uniq("inactive")
		uid := insertUser(t, pool, email, false, false)

		rec := do(h, http.MethodPost, "/api/v1/households", s.token(t, sub, email), body)
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.Equal(t, 0, countHouseholdsFor(t, pool, uid))
	})

	t.Run("missing token", func(t *testing.T) {
		rec := do(h, http.MethodPost, "/api/v1/households", "", body)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("token signed by another key", func(t *testing.T) {
		sub, email := uniq("forged")
		rec := do(h, http.MethodPost, "/api/v1/households", newSigner(t).token(t, sub, email), body)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}
