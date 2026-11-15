package invite_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"home-os/api/internal/auth"
	"home-os/api/internal/config"
	"home-os/api/internal/household"
	"home-os/api/internal/invite"
	"home-os/api/internal/middleware"
)

const testPublicURL = "https://home.example/"

// fakeDex records CreatePassword calls in place of Dex's gRPC API.
type fakeDex struct {
	mu        sync.Mutex
	err       error
	passwords map[string]dexPassword // by email
}

type dexPassword struct{ hash, userID string }

func (d *fakeDex) CreatePassword(_ context.Context, email, hash, userID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return d.err
	}
	if d.passwords == nil {
		d.passwords = map[string]dexPassword{}
	}
	d.passwords[email] = dexPassword{hash: hash, userID: userID}
	return nil
}

func (d *fakeDex) get(email string) (dexPassword, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.passwords[email]
	return p, ok
}

// linkRouter is router() with PUBLIC_URL set and, when dex is non-nil, Dex
// wired for new-user signup (as cmd/api/main.go does).
func linkRouter(pool *pgxpool.Pool, v *auth.Verifier, dex invite.PasswordStore) http.Handler {
	r := chi.NewRouter()
	ih := invite.NewHandler(invite.NewRepo(pool), &config.Config{PublicURL: testPublicURL}, nil).WithVerifier(v)
	if dex != nil {
		ih.WithDex(dex)
	}
	r.Post("/api/v1/invites/accept", ih.AcceptInvite)
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		r.Post("/api/v1/invites", ih.CreateInvite)
		r.Get("/api/v1/invites", ih.ListInvites)
		r.Get("/api/v1/households/me/members", household.NewHandler(household.NewRepo(pool)).ListMembers)
	})
	return r
}

// withDex switches the fixture to linkRouter and returns its fake Dex.
func (f *fixture) withDex(t *testing.T) *fakeDex {
	t.Helper()
	d := &fakeDex{}
	f.h = linkRouter(f.pool, f.s.verifier(f.pool), d)
	return d
}

type inviteJSON struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Token     string `json:"token"`
	AcceptURL string `json:"accept_url"`
}

func acceptURLFor(token string) string {
	return "https://home.example/invites/accept?token=" + token
}

func TestCreateInvite_ReturnsShareableAcceptURL(t *testing.T) {
	f := setup(t)
	f.withDex(t)
	ctx := context.Background()
	email := "link-" + uuid.NewString()[:8] + "@example.com"

	rec := do(f.h, http.MethodPost, "/api/v1/invites", f.owner, map[string]string{"email": " " + email + " ", "role": "viewer"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct{ Data inviteJSON }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	var token, stored string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT token, email FROM invitations WHERE id = $1`, created.Data.ID).Scan(&token, &stored))
	assert.Equal(t, acceptURLFor(token), created.Data.AcceptURL)
	assert.Equal(t, email, stored, "email is trimmed")
	assert.Equal(t, "viewer", created.Data.Role)
	assert.Empty(t, created.Data.Token, "the bare token is only exposed inside accept_url")

	// A second, expired invite: listed without a link.
	expired := f.invite(t, "exp-"+email, "viewer")
	_, err := f.pool.Exec(ctx, `UPDATE invitations SET expires_at = NOW() - interval '1 hour' WHERE token = $1`, expired)
	require.NoError(t, err)

	rec = do(f.h, http.MethodGet, "/api/v1/invites", f.owner, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list struct{ Data []inviteJSON }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	byEmail := map[string]inviteJSON{}
	for _, inv := range list.Data {
		byEmail[inv.Email] = inv
	}
	require.Contains(t, byEmail, email)
	require.Contains(t, byEmail, "exp-"+email)
	assert.Equal(t, acceptURLFor(token), byEmail[email].AcceptURL, "pending invite can be re-copied")
	assert.Empty(t, byEmail["exp-"+email].AcceptURL, "expired invite has no link")

	t.Run("email without @ is rejected", func(t *testing.T) {
		rec := do(f.h, http.MethodPost, "/api/v1/invites", f.owner, map[string]string{"email": "nobody"})
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	})
}

func TestAcceptInvite_SignupNewPersonThenSignsInThroughDex(t *testing.T) {
	f := setup(t)
	dex := f.withDex(t)
	ctx := context.Background()
	email := "New-" + uuid.NewString()[:8] + "@Example.com"
	f.cleanupUser(t, email)
	tok := f.invite(t, email, "family_member")

	const password = "correct horse battery"
	rec := do(f.h, http.MethodPost, "/api/v1/invites/accept", "",
		map[string]string{"token": tok, "name": "  Nina New  ", "password": password})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var acc struct {
		Data struct {
			UserID      string `json:"user_id"`
			Email       string `json:"email"`
			HouseholdID string `json:"household_id"`
			Role        string `json:"role"`
			UserCreated bool   `json:"user_created"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &acc))
	assert.Equal(t, email, acc.Data.Email, "the account uses the invited email")
	assert.Equal(t, f.hhID, acc.Data.HouseholdID)
	assert.Equal(t, "family_member", acc.Data.Role)
	assert.True(t, acc.Data.UserCreated)

	// Dex got a bcrypt hash of the chosen password for the invited email.
	pw, ok := dex.get(email)
	require.True(t, ok, "no Dex password created")
	assert.Equal(t, acc.Data.UserID, pw.userID)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(pw.hash), []byte(password)))

	var name string
	var active bool
	var dexSub *string
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT name, active, dex_sub FROM users WHERE id = $1`, acc.Data.UserID).Scan(&name, &active, &dexSub))
	assert.Equal(t, "Nina New", name)
	assert.True(t, active)
	assert.Nil(t, dexSub, "dex_sub is backfilled on first sign-in")

	// Signing in through Dex: Dex issues a token for the invited email with a
	// sub Home OS has never seen. The member resolver matches it by email,
	// backfills dex_sub, and the protected API now lets them in.
	sub := "CiQ" + uuid.NewString()
	rec = do(f.h, http.MethodGet, "/api/v1/households/me/members", f.s.token(t, sub, strings.ToLower(email)), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT dex_sub FROM users WHERE id = $1`, acc.Data.UserID).Scan(&dexSub))
	require.NotNil(t, dexSub)
	assert.Equal(t, sub, *dexSub)

	// The link is single-use.
	rec = do(f.h, http.MethodPost, "/api/v1/invites/accept", "",
		map[string]string{"token": tok, "name": "Again", "password": password})
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

func TestAcceptInvite_SignupRejections(t *testing.T) {
	f := setup(t)
	dex := f.withDex(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	existing := "has-account-" + suffix + "@example.com"
	inactive := "inactive-" + suffix + "@example.com"
	fresh := "fresh-" + suffix + "@example.com"
	for _, e := range []string{existing, inactive, fresh} {
		f.cleanupUser(t, e)
	}
	_, err := f.pool.Exec(ctx,
		`INSERT INTO users (email, name, password_hash, active) VALUES ($1, 'Has', 'x', true), ($2, 'Gone', 'x', false)`,
		strings.ToUpper(existing), inactive)
	require.NoError(t, err)

	existingTok := f.invite(t, existing, "viewer")
	inactiveTok := f.invite(t, inactive, "viewer")
	freshTok := f.invite(t, fresh, "viewer")
	expiredTok := f.invite(t, fresh, "viewer")
	_, err = f.pool.Exec(ctx, `UPDATE invitations SET expires_at = NOW() - interval '1 hour' WHERE token = $1`, expiredTok)
	require.NoError(t, err)

	const pw = "long enough pw"
	tests := []struct {
		name                  string
		token, uname, passwrd string
		want                  int
	}{
		{"existing account must sign in instead", existingTok, "Has", pw, http.StatusConflict},
		{"deactivated account", inactiveTok, "Gone", pw, http.StatusForbidden},
		{"missing name", freshTok, "   ", pw, http.StatusBadRequest},
		{"name too long", freshTok, strings.Repeat("n", 101), pw, http.StatusBadRequest},
		{"short password", freshTok, "Fresh", "short", http.StatusBadRequest},
		{"password over bcrypt limit", freshTok, "Fresh", strings.Repeat("p", 73), http.StatusBadRequest},
		{"missing token", "", "Fresh", pw, http.StatusBadRequest},
		{"unknown token", uuid.NewString(), "Fresh", pw, http.StatusNotFound},
		{"expired invite", expiredTok, "Fresh", pw, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(f.h, http.MethodPost, "/api/v1/invites/accept", "",
				map[string]string{"token": tc.token, "name": tc.uname, "password": tc.passwrd})
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}

	t.Run("dex failure writes nothing and leaves the invite usable", func(t *testing.T) {
		dex.mu.Lock()
		dex.err = errors.New("dex down")
		dex.mu.Unlock()
		rec := do(f.h, http.MethodPost, "/api/v1/invites/accept", "",
			map[string]string{"token": freshTok, "name": "Fresh", "password": pw})
		assert.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
		var n int
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE lower(email) = $1`, fresh).Scan(&n))
		assert.Equal(t, 0, n)

		dex.mu.Lock()
		dex.err = nil
		dex.mu.Unlock()
		rec = do(f.h, http.MethodPost, "/api/v1/invites/accept", "",
			map[string]string{"token": freshTok, "name": "Fresh", "password": pw})
		assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	})

	t.Run("without Dex signup is unavailable", func(t *testing.T) {
		h := linkRouter(f.pool, f.s.verifier(f.pool), nil)
		rec := do(h, http.MethodPost, "/api/v1/invites/accept", "",
			map[string]string{"token": existingTok, "name": "X", "password": pw})
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	})

	// The refused invites are still pending and no account was touched.
	for _, tok := range []string{existingTok, inactiveTok} {
		var accepted *time.Time
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT accepted_at FROM invitations WHERE token = $1`, tok).Scan(&accepted))
		assert.Nil(t, accepted)
	}
	_, ok := dex.get(existing)
	assert.False(t, ok, "existing account's Dex password must not be overwritten")
	var users int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE lower(email) = $1`, existing).Scan(&users))
	assert.Equal(t, 1, users)
}

func TestAcceptInvite_DeactivatedUserWithDexTokenRefused(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	email := "deact-" + uuid.NewString()[:8] + "@example.com"
	f.cleanupUser(t, email)
	sub := "CiQ" + uuid.NewString()
	var userID string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub, active) VALUES ($1, 'Off', 'x', $2, false) RETURNING id::text`,
		email, sub).Scan(&userID))
	tok := f.invite(t, email, "family_member")

	rec := do(f.h, http.MethodPost, "/api/v1/invites/accept", f.s.token(t, sub, email), map[string]string{"token": tok})
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	var n int
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT count(*) FROM memberships WHERE user_id = $1`, userID).Scan(&n))
	assert.Equal(t, 0, n)
}
