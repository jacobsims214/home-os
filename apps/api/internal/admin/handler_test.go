package admin_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	"golang.org/x/crypto/bcrypt"

	"home-os/api/internal/admin"
	"home-os/api/internal/auth"
	"home-os/api/internal/dex"
	"home-os/api/internal/household"
	"home-os/api/internal/middleware"
)

const testIssuer = "https://dex.test/dex"

// --- token signing ---

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
		"iss": testIssuer, "aud": "home-os-ui", "sub": sub, "email": email, "name": "Someone",
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

// --- fake Dex ---

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
	if _, ok := d.passwords[email]; ok { // like Dex: never overwrite
		return dex.ErrPasswordExists
	}
	d.passwords[email] = dexPassword{hash: hash, userID: userID}
	return nil
}

// preset stores a password as if someone else had set it in Dex earlier.
func (d *fakeDex) preset(email string, p dexPassword) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.passwords == nil {
		d.passwords = map[string]dexPassword{}
	}
	d.passwords[email] = p
}

func (d *fakeDex) get(email string) (dexPassword, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.passwords[email]
	return p, ok
}

// --- fixture ---

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

// router mirrors cmd/api/main.go: admin routes outside RequireAuth, plus a
// protected household route to prove a created user can sign in.
func router(pool *pgxpool.Pool, v *auth.Verifier, dex admin.PasswordStore) http.Handler {
	r := chi.NewRouter()
	ah := admin.NewHandler(admin.NewRepo(pool), v)
	if dex != nil {
		ah.WithDex(dex)
	}
	ah.RegisterRoutes(r)
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		hh := household.NewHandler(household.NewRepo(pool))
		r.Get("/api/v1/households/me", hh.GetMe)
		r.Get("/api/v1/households/me/members", hh.ListMembers)
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
	t    *testing.T
	pool *pgxpool.Pool
	s    *signer
	dex  *fakeDex
	h    http.Handler
	hhID string // an existing family
}

type person struct {
	id, email, sub, token string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	pool := testDB(t)
	s := newSigner(t)
	f := &fixture{t: t, pool: pool, s: s, dex: &fakeDex{}}
	f.h = router(pool, s.verifier(pool), f.dex)
	ctx := context.Background()
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Admin Test HH') RETURNING id::text`).Scan(&f.hhID))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM households WHERE id = $1`, f.hhID) })
	return f
}

// user inserts a user (optionally an instance admin, optionally a member of
// the fixture family) with a Dex sub, and returns a token for them.
func (f *fixture) user(isAdmin, member bool) person {
	f.t.Helper()
	ctx := context.Background()
	p := person{email: "u-" + uuid.NewString()[:8] + "@example.com", sub: "CiQ" + uuid.NewString()}
	require.NoError(f.t, f.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub, is_instance_admin)
		 VALUES ($1, 'Test', 'x', $2, $3) RETURNING id::text`,
		p.email, p.sub, isAdmin).Scan(&p.id))
	f.cleanupEmail(p.email)
	if member {
		_, err := f.pool.Exec(ctx,
			`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'owner')`, f.hhID, p.id)
		require.NoError(f.t, err)
	}
	p.token = f.s.token(f.t, p.sub, p.email)
	return p
}

func (f *fixture) cleanupEmail(email string) {
	f.t.Cleanup(func() {
		ctx := context.Background()
		// Families created for this user (sole member) go too.
		_, _ = f.pool.Exec(ctx,
			`DELETE FROM households h WHERE h.id <> $2 AND h.id IN (
			   SELECT m.household_id FROM memberships m JOIN users u ON u.id = m.user_id
			   WHERE lower(u.email) = lower($1))`, email, f.hhID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM users WHERE lower(email) = lower($1)`, email)
	})
}

func (f *fixture) active(id string) bool {
	f.t.Helper()
	var a bool
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		`SELECT active FROM users WHERE id = $1`, id).Scan(&a))
	return a
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body.Error.Code
}

// adminEndpoints lists every admin route with a plausible request.
func adminEndpoints(target string) []struct{ method, path string } {
	return []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/families"},
		{http.MethodGet, "/api/v1/admin/users"},
		{http.MethodPost, "/api/v1/admin/users"},
		{http.MethodPost, "/api/v1/admin/users/" + target + "/deactivate"},
		{http.MethodPost, "/api/v1/admin/users/" + target + "/reactivate"},
	}
}

// --- tests ---

func TestAdmin_NonAdminsAreRefusedOnEveryEndpoint(t *testing.T) {
	f := setup(t)
	member := f.user(false, true) // household owner, but not an instance admin
	target := f.user(false, true)

	deactivatedAdmin := f.user(true, true)
	_, err := f.pool.Exec(context.Background(), `UPDATE users SET active = false WHERE id = $1`, deactivatedAdmin.id)
	require.NoError(t, err)

	unknown := f.s.token(t, "CiQ"+uuid.NewString(), "nobody-"+uuid.NewString()[:8]+"@example.com")

	// A token claiming admin in its payload changes nothing: the flag is read from the DB.
	body := map[string]any{"name": "X", "email": "x-" + uuid.NewString()[:8] + "@example.com",
		"password": "longenough", "household_id": f.hhID}
	for _, ep := range adminEndpoints(target.id) {
		for name, tok := range map[string]string{
			"household owner":   member.token,
			"deactivated admin": deactivatedAdmin.token,
			"unknown identity":  unknown,
		} {
			rec := do(f.h, ep.method, ep.path, tok, body)
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s as %s: %s", ep.method, ep.path, name, rec.Body.String())
		}
		rec := do(f.h, ep.method, ep.path, "", body)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s without token", ep.method, ep.path)
		rec = do(f.h, ep.method, ep.path, "not-a-jwt", body)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s forged token", ep.method, ep.path)
	}
	assert.True(t, f.active(target.id), "refused deactivate must not change anything")
	_, ok := f.dex.get(body["email"].(string))
	assert.False(t, ok, "refused create must not reach Dex")
}

func TestAdmin_AdminWithoutMembershipIsAllowed(t *testing.T) {
	f := setup(t)
	adm := f.user(true, false)
	rec := do(f.h, http.MethodGet, "/api/v1/admin/families", adm.token, nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestAdmin_ListFamiliesAndUsers(t *testing.T) {
	f := setup(t)
	adm := f.user(true, true)
	f.user(false, true)

	rec := do(f.h, http.MethodGet, "/api/v1/admin/families", adm.token, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var fams struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Timezone    string `json:"timezone"`
			MemberCount int    `json:"member_count"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fams))
	found := false
	for _, fam := range fams.Data {
		if fam.ID == f.hhID {
			found = true
			assert.Equal(t, 2, fam.MemberCount)
			assert.Equal(t, "Admin Test HH", fam.Name)
			assert.Equal(t, "UTC", fam.Timezone)
		}
	}
	assert.True(t, found, "fixture family listed")

	rec = do(f.h, http.MethodGet, "/api/v1/admin/users", adm.token, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	raw := rec.Body.String()
	for _, secret := range []string{"password", "caldav", "dex_sub", "hash"} {
		assert.NotContains(t, strings.ToLower(raw), secret, "user list must not leak %s", secret)
	}
	var users struct {
		Data []struct {
			ID              string `json:"id"`
			Email           string `json:"email"`
			Active          bool   `json:"active"`
			IsInstanceAdmin bool   `json:"is_instance_admin"`
			Memberships     []struct {
				HouseholdID   string `json:"household_id"`
				HouseholdName string `json:"household_name"`
				Role          string `json:"role"`
			} `json:"memberships"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &users))
	var me *struct {
		ID              string `json:"id"`
		Email           string `json:"email"`
		Active          bool   `json:"active"`
		IsInstanceAdmin bool   `json:"is_instance_admin"`
		Memberships     []struct {
			HouseholdID   string `json:"household_id"`
			HouseholdName string `json:"household_name"`
			Role          string `json:"role"`
		} `json:"memberships"`
	}
	for i := range users.Data {
		if users.Data[i].ID == adm.id {
			me = &users.Data[i]
		}
	}
	require.NotNil(t, me)
	assert.True(t, me.Active)
	assert.True(t, me.IsInstanceAdmin)
	require.Len(t, me.Memberships, 1)
	assert.Equal(t, f.hhID, me.Memberships[0].HouseholdID)
	assert.Equal(t, "owner", me.Memberships[0].Role)
}

type createResp struct {
	Data struct {
		UserID           string `json:"user_id"`
		Email            string `json:"email"`
		HouseholdID      string `json:"household_id"`
		HouseholdName    string `json:"household_name"`
		Role             string `json:"role"`
		HouseholdCreated bool   `json:"household_created"`
	} `json:"data"`
}

func TestAdmin_CreateUserIntoExistingFamilyThenSignsIn(t *testing.T) {
	f := setup(t)
	adm := f.user(true, true)
	email := "New-" + uuid.NewString()[:8] + "@Example.com"
	f.cleanupEmail(email)

	rec := do(f.h, http.MethodPost, "/api/v1/admin/users", adm.token, map[string]any{
		"name": "  New Kid  ", "email": "  " + email + " ", "password": "temp-pass-123",
		"household_id": f.hhID,
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.NotContains(t, strings.ToLower(rec.Body.String()), "password")
	var res createResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	assert.Equal(t, f.hhID, res.Data.HouseholdID)
	assert.Equal(t, "Admin Test HH", res.Data.HouseholdName)
	assert.Equal(t, "family_member", res.Data.Role, "default role for an existing family")
	assert.False(t, res.Data.HouseholdCreated)
	assert.Equal(t, email, res.Data.Email)

	// Dex got a bcrypt hash of the temporary password, keyed to the new user.
	p, ok := f.dex.get(email)
	require.True(t, ok, "Dex password created")
	assert.Equal(t, res.Data.UserID, p.userID)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(p.hash), []byte("temp-pass-123")))

	// The user signs in through Dex (a sub Home OS has never seen) and lands in the family.
	sub := "CiQ" + uuid.NewString()
	tok := f.s.token(t, sub, strings.ToLower(email))
	rec = do(f.h, http.MethodGet, "/api/v1/households/me", tok, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), f.hhID)
	var storedSub string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT dex_sub FROM users WHERE id = $1`, res.Data.UserID).Scan(&storedSub))
	assert.Equal(t, sub, storedSub, "dex_sub backfilled on first sign-in")

	// A chosen role is honoured.
	email2 := "viewer-" + uuid.NewString()[:8] + "@example.com"
	f.cleanupEmail(email2)
	rec = do(f.h, http.MethodPost, "/api/v1/admin/users", adm.token, map[string]any{
		"name": "Sitter", "email": email2, "password": "temp-pass-123", "household_id": f.hhID, "role": "housesitter",
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	assert.Equal(t, "housesitter", res.Data.Role)
}

func TestAdmin_CreateUserIntoNewFamily(t *testing.T) {
	f := setup(t)
	adm := f.user(true, false)
	email := "founder-" + uuid.NewString()[:8] + "@example.com"
	f.cleanupEmail(email)

	rec := do(f.h, http.MethodPost, "/api/v1/admin/users", adm.token, map[string]any{
		"name": "Founder", "email": email, "password": "temp-pass-123",
		"household_name": "Founder Family", "timezone": "America/Chicago",
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var res createResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	assert.True(t, res.Data.HouseholdCreated)
	assert.Equal(t, "owner", res.Data.Role)
	assert.Equal(t, "Founder Family", res.Data.HouseholdName)
	assert.NotEqual(t, f.hhID, res.Data.HouseholdID)

	ctx := context.Background()
	var tz string
	var calendars int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT timezone FROM households WHERE id = $1`, res.Data.HouseholdID).Scan(&tz))
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM calendars WHERE household_id = $1`, res.Data.HouseholdID).Scan(&calendars))
	assert.Equal(t, "America/Chicago", tz)
	assert.Equal(t, 3, calendars, "default Family/Bills/Maintenance calendars")

	rec = do(f.h, http.MethodGet, "/api/v1/households/me", f.s.token(t, "CiQ"+uuid.NewString(), email), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), res.Data.HouseholdID)
}

func TestAdmin_CreateUserRejections(t *testing.T) {
	f := setup(t)
	adm := f.user(true, true)
	existing := f.user(false, true)

	valid := func(mut func(m map[string]any)) map[string]any {
		m := map[string]any{"name": "Ok", "email": "ok-" + uuid.NewString()[:8] + "@example.com",
			"password": "temp-pass-123", "household_id": f.hhID}
		mut(m)
		return m
	}
	cases := []struct {
		name   string
		body   any
		status int
	}{
		{"bad json", "not an object", http.StatusBadRequest},
		{"no name", valid(func(m map[string]any) { m["name"] = "  " }), http.StatusBadRequest},
		{"long name", valid(func(m map[string]any) { m["name"] = strings.Repeat("n", 101) }), http.StatusBadRequest},
		{"no email", valid(func(m map[string]any) { m["email"] = "" }), http.StatusBadRequest},
		{"bad email", valid(func(m map[string]any) { m["email"] = "no-at-sign" }), http.StatusBadRequest},
		{"short password", valid(func(m map[string]any) { m["password"] = "short" }), http.StatusBadRequest},
		{"long password", valid(func(m map[string]any) { m["password"] = strings.Repeat("p", 73) }), http.StatusBadRequest},
		{"no family", valid(func(m map[string]any) { delete(m, "household_id") }), http.StatusBadRequest},
		{"both family kinds", valid(func(m map[string]any) { m["household_name"] = "Also" }), http.StatusBadRequest},
		{"bad household id", valid(func(m map[string]any) { m["household_id"] = "nope" }), http.StatusBadRequest},
		{"bad role", valid(func(m map[string]any) { m["role"] = "superuser" }), http.StatusBadRequest},
		{"new family non-owner", valid(func(m map[string]any) {
			delete(m, "household_id")
			m["household_name"] = "Fam"
			m["role"] = "viewer"
		}), http.StatusBadRequest},
		{"bad timezone", valid(func(m map[string]any) {
			delete(m, "household_id")
			m["household_name"] = "Fam"
			m["timezone"] = "Mars/Olympus"
		}), http.StatusBadRequest},
		{"unknown family", valid(func(m map[string]any) { m["household_id"] = uuid.NewString() }), http.StatusNotFound},
		{"existing email, other case", valid(func(m map[string]any) { m["email"] = strings.ToUpper(existing.email) }), http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(f.h, http.MethodPost, "/api/v1/admin/users", adm.token, tc.body)
			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
		})
	}

	t.Run("dex failure writes nothing, retry succeeds", func(t *testing.T) {
		email := "dexfail-" + uuid.NewString()[:8] + "@example.com"
		f.cleanupEmail(email)
		body := map[string]any{"name": "D", "email": email, "password": "temp-pass-123",
			"household_name": "Dex Fail Family"}
		f.dex.mu.Lock()
		f.dex.err = errors.New("dex down")
		f.dex.mu.Unlock()
		rec := do(f.h, http.MethodPost, "/api/v1/admin/users", adm.token, body)
		assert.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
		var n int
		require.NoError(t, f.pool.QueryRow(context.Background(),
			`SELECT (SELECT count(*) FROM users WHERE email = $1) + (SELECT count(*) FROM households WHERE name = 'Dex Fail Family')`,
			email).Scan(&n))
		assert.Zero(t, n, "no user or family written")

		f.dex.mu.Lock()
		f.dex.err = nil
		f.dex.mu.Unlock()
		rec = do(f.h, http.MethodPost, "/api/v1/admin/users", adm.token, body)
		assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	})

	t.Run("existing dex password is 409, writes nothing, keeps dex entry", func(t *testing.T) {
		email := "dexstale-" + uuid.NewString()[:8] + "@example.com"
		f.cleanupEmail(email)
		stale := dexPassword{hash: "someone-elses-hash", userID: "stale-user-id"}
		f.dex.preset(email, stale)
		body := map[string]any{"name": "S", "email": email, "password": "temp-pass-123",
			"household_name": "Dex Stale Family"}
		rec := do(f.h, http.MethodPost, "/api/v1/admin/users", adm.token, body)
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "an account with this email already exists")
		var n int
		require.NoError(t, f.pool.QueryRow(context.Background(),
			`SELECT (SELECT count(*) FROM users WHERE lower(email) = lower($1)) + (SELECT count(*) FROM households WHERE name = 'Dex Stale Family')`,
			email).Scan(&n))
		assert.Zero(t, n, "no user or family written")
		got, _ := f.dex.get(email)
		assert.Equal(t, stale, got, "the existing Dex password is left as it was")

		// Into an existing family: the membership is not written either.
		body = map[string]any{"name": "S", "email": email, "password": "temp-pass-123",
			"household_id": f.hhID, "role": "viewer"}
		rec = do(f.h, http.MethodPost, "/api/v1/admin/users", adm.token, body)
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		require.NoError(t, f.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM users WHERE lower(email) = lower($1)`, email).Scan(&n))
		assert.Zero(t, n)
	})

	t.Run("no dex configured is 503", func(t *testing.T) {
		h := router(f.pool, f.s.verifier(f.pool), nil)
		rec := do(h, http.MethodPost, "/api/v1/admin/users", adm.token, valid(func(map[string]any) {}))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	})
}

func TestAdmin_DeactivateTakesEffectImmediatelyAndReactivates(t *testing.T) {
	f := setup(t)
	adm := f.user(true, true)
	u := f.user(false, true)

	rec := do(f.h, http.MethodGet, "/api/v1/households/me", u.token, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = do(f.h, http.MethodPost, "/api/v1/admin/users/"+u.id+"/deactivate", adm.token, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"active":false`)
	assert.NotContains(t, strings.ToLower(rec.Body.String()), "password")

	// The same, still-valid token is refused on the very next request.
	rec = do(f.h, http.MethodGet, "/api/v1/households/me", u.token, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	// Idempotent.
	rec = do(f.h, http.MethodPost, "/api/v1/admin/users/"+u.id+"/deactivate", adm.token, nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = do(f.h, http.MethodPost, "/api/v1/admin/users/"+u.id+"/reactivate", adm.token, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"active":true`)
	rec = do(f.h, http.MethodGet, "/api/v1/households/me", u.token, nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// A deactivated admin loses admin access at once, too.
	adm2 := f.user(true, false)
	rec = do(f.h, http.MethodPost, "/api/v1/admin/users/"+adm2.id+"/deactivate", adm.token, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = do(f.h, http.MethodGet, "/api/v1/admin/users", adm2.token, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestAdmin_DeactivateGuards(t *testing.T) {
	f := setup(t)
	adm := f.user(true, true)

	rec := do(f.h, http.MethodPost, "/api/v1/admin/users/"+adm.id+"/deactivate", adm.token, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.True(t, f.active(adm.id), "admin cannot deactivate themselves")

	rec = do(f.h, http.MethodPost, "/api/v1/admin/users/"+uuid.NewString()+"/deactivate", adm.token, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, "NOT_FOUND", errCode(t, rec))

	rec = do(f.h, http.MethodPost, "/api/v1/admin/users/not-a-uuid/deactivate", adm.token, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// Two admins deactivating each other at the same time must not leave the
// instance without an active admin.
func TestAdmin_ConcurrentMutualDeactivationKeepsAnAdmin(t *testing.T) {
	f := setup(t)
	for i := 0; i < 10; i++ {
		a := f.user(true, false)
		b := f.user(true, false)
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for j, pair := range [][2]person{{a, b}, {b, a}} {
			wg.Add(1)
			go func(j int, caller, target person) {
				defer wg.Done()
				codes[j] = do(f.h, http.MethodPost, "/api/v1/admin/users/"+target.id+"/deactivate", caller.token, nil).Code
			}(j, pair[0], pair[1])
		}
		wg.Wait()
		assert.True(t, f.active(a.id) || f.active(b.id), "round %d: at least one admin stays active (codes %v)", i, codes)
		ok := 0
		for _, c := range codes {
			if c == http.StatusOK {
				ok++
			} else {
				assert.Equal(t, http.StatusForbidden, c)
			}
		}
		assert.Equal(t, 1, ok, "round %d: exactly one deactivation wins (codes %v)", i, codes)
	}
}
