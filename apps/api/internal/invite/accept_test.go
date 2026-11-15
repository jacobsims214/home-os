package invite_test

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
	"sync"
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
	"home-os/api/internal/household"
	"home-os/api/internal/invite"
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
		"iss": testIssuer, "aud": "home-os-ui", "sub": sub, "email": email, "name": "Invitee",
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

// router mirrors cmd/api/main.go: accept outside RequireAuth, the rest inside.
func router(pool *pgxpool.Pool, v *auth.Verifier) http.Handler {
	r := chi.NewRouter()
	ih := invite.NewHandler(invite.NewRepo(pool), &config.Config{}, nil).WithVerifier(v)
	r.Post("/api/v1/invites/accept", ih.AcceptInvite)
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		r.Post("/api/v1/invites", ih.CreateInvite)
		r.Get("/api/v1/households/me/members", household.NewHandler(household.NewRepo(pool)).ListMembers)
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

type fixture struct {
	pool    *pgxpool.Pool
	s       *signer
	h       http.Handler
	hhID    string
	owner   string // owner's Dex token
	ownerID string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	pool := testDB(t)
	ctx := context.Background()
	s := newSigner(t)
	f := &fixture{pool: pool, s: s, h: router(pool, s.verifier(pool))}

	ownerEmail := "owner-" + uuid.NewString()[:8] + "@example.com"
	ownerSub := "CiQ" + uuid.NewString()
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, 'Owner', 'x', $2) RETURNING id::text`,
		ownerEmail, ownerSub).Scan(&f.ownerID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Invite HH') RETURNING id::text`).Scan(&f.hhID))
	_, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'owner')`, f.hhID, f.ownerID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM households WHERE id = $1`, f.hhID) // cascades memberships, invitations
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.ownerID)
	})
	f.owner = s.token(t, ownerSub, ownerEmail)
	return f
}

// invite creates an invitation through the API and returns its token.
func (f *fixture) invite(t *testing.T, email, role string) string {
	t.Helper()
	rec := do(f.h, http.MethodPost, "/api/v1/invites", f.owner, map[string]string{"email": email, "role": role})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var body struct {
		Data struct{ ID string } `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	var tok string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT token FROM invitations WHERE id = $1`, body.Data.ID).Scan(&tok))
	return tok
}

func (f *fixture) cleanupUser(t *testing.T, email string) {
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE lower(email) = lower($1)`, email)
	})
}

type member struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

func (f *fixture) members(t *testing.T) []member {
	t.Helper()
	rec := do(f.h, http.MethodGet, "/api/v1/households/me/members", f.owner, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct{ Data []member }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body.Data
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body.Error.Code
}

func TestAcceptInvite_NewDexUserBecomesMember(t *testing.T) {
	f := setup(t)
	email := "newbie-" + uuid.NewString()[:8] + "@example.com"
	f.cleanupUser(t, email)
	tok := f.invite(t, email, "viewer")

	// A brand-new Dex identity: no users row, no membership.
	sub := "CiQ" + uuid.NewString()
	inviteeTok := f.s.token(t, sub, email)

	// Before accepting, the protected API refuses them (403, not a member).
	rec := do(f.h, http.MethodGet, "/api/v1/households/me/members", inviteeTok, nil)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	rec = do(f.h, http.MethodPost, "/api/v1/invites/accept", inviteeTok, map[string]string{"token": tok})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var acc struct {
		Data struct {
			UserID      string `json:"user_id"`
			HouseholdID string `json:"household_id"`
			Role        string `json:"role"`
			Invitation  struct {
				AcceptedAt *time.Time `json:"accepted_at"`
			} `json:"invitation"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &acc))
	assert.Equal(t, f.hhID, acc.Data.HouseholdID)
	assert.Equal(t, "viewer", acc.Data.Role)
	assert.NotNil(t, acc.Data.Invitation.AcceptedAt)

	// The owner sees the invitee in GET /households/me/members with the invited role.
	var found *member
	for _, m := range f.members(t) {
		if m.Email == email {
			m := m
			found = &m
		}
	}
	require.NotNil(t, found, "invitee missing from members")
	assert.Equal(t, "viewer", found.Role)
	assert.Equal(t, acc.Data.UserID, found.UserID)

	// The created user carries the Dex sub, so the invitee now resolves.
	var storedSub *string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT dex_sub FROM users WHERE id = $1`, acc.Data.UserID).Scan(&storedSub))
	require.NotNil(t, storedSub)
	assert.Equal(t, sub, *storedSub)
	rec = do(f.h, http.MethodGet, "/api/v1/households/me/members", inviteeTok, nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Accepting again: 409 and still exactly one membership.
	rec = do(f.h, http.MethodPost, "/api/v1/invites/accept", inviteeTok, map[string]string{"token": tok})
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "CONFLICT", errCode(t, rec))
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM memberships WHERE household_id = $1 AND user_id = $2`,
		f.hhID, acc.Data.UserID).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestAcceptInvite_ExistingUserMatchedByEmail(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	email := "Existing-" + uuid.NewString()[:8] + "@Example.com"
	f.cleanupUser(t, email)
	var userID string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, 'Existing', 'x') RETURNING id::text`,
		email).Scan(&userID))
	tok := f.invite(t, email, "family_manager")

	rec := do(f.h, http.MethodPost, "/api/v1/invites/accept",
		f.s.token(t, "CiQ"+uuid.NewString(), email), map[string]string{"token": tok})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var role string
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`, f.hhID, userID).Scan(&role))
	assert.Equal(t, "family_manager", role)
	var users int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE lower(email) = lower($1)`, email).Scan(&users))
	assert.Equal(t, 1, users, "must reuse the existing user, not create a second one")
}

func TestAcceptInvite_Rejections(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	email := "rej-" + uuid.NewString()[:8] + "@example.com"
	f.cleanupUser(t, email)
	tok := f.invite(t, email, "family_member")

	expired := f.invite(t, email, "family_member")
	_, err := f.pool.Exec(ctx, `UPDATE invitations SET expires_at = NOW() - interval '1 hour' WHERE token = $1`, expired)
	require.NoError(t, err)

	good := f.s.token(t, "CiQ"+uuid.NewString(), email)
	tests := []struct {
		name, token, invite string
		want                int
	}{
		{"no bearer token", "", tok, http.StatusUnauthorized},
		{"forged token", newSigner(t).token(t, "CiQx", email), tok, http.StatusUnauthorized},
		{"different email", f.s.token(t, "CiQ"+uuid.NewString(), "someone@example.com"), tok, http.StatusForbidden},
		{"unknown invite", good, uuid.NewString(), http.StatusNotFound},
		{"expired invite", good, expired, http.StatusBadRequest},
		{"member using invite for another email", f.owner, f.invite(t, "owner-dup@example.com", "viewer"), http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(f.h, http.MethodPost, "/api/v1/invites/accept", tc.token, map[string]string{"token": tc.invite})
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}

	// None of the rejections created a user or a membership, nor consumed the invite.
	var users int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE lower(email) = lower($1)`, email).Scan(&users))
	assert.Equal(t, 0, users)
	var accepted *time.Time
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT accepted_at FROM invitations WHERE token = $1`, tok).Scan(&accepted))
	assert.Nil(t, accepted)

	t.Run("invalid role rejected at create", func(t *testing.T) {
		rec := do(f.h, http.MethodPost, "/api/v1/invites", f.owner, map[string]string{"email": email, "role": "admin"})
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	})
}

func TestAcceptInvite_AlreadyMemberIs409(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var ownerEmail string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, f.ownerID).Scan(&ownerEmail))
	tok := f.invite(t, ownerEmail, "viewer")

	rec := do(f.h, http.MethodPost, "/api/v1/invites/accept", f.owner, map[string]string{"token": tok})
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var role string
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`, f.hhID, f.ownerID).Scan(&role))
	assert.Equal(t, "owner", role, "existing membership must be untouched")
}

func TestAcceptInvite_ConcurrentAcceptsCreateOneMembership(t *testing.T) {
	f := setup(t)
	email := "race-" + uuid.NewString()[:8] + "@example.com"
	f.cleanupUser(t, email)
	tok := f.invite(t, email, "family_member")
	inviteeTok := f.s.token(t, "CiQ"+uuid.NewString(), email)

	const n = 5
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = do(f.h, http.MethodPost, "/api/v1/invites/accept", inviteeTok, map[string]string{"token": tok}).Code
		}(i)
	}
	wg.Wait()

	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		}
	}
	assert.Equal(t, 1, ok, "codes: %v", codes)
	assert.Equal(t, n-1, conflict, "codes: %v", codes)
	var m int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM memberships m JOIN users u ON u.id = m.user_id
		 WHERE m.household_id = $1 AND lower(u.email) = lower($2)`, f.hhID, email).Scan(&m))
	assert.Equal(t, 1, m)
}
