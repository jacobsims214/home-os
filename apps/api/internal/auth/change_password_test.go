package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/config"
	"home-os/api/internal/dex"
)

// fakePasswords is an in-memory stand-in for Dex's password and refresh-token
// admin API. Hashes are real bcrypt hashes, and VerifyPassword really runs
// bcrypt, because the point of most of these tests is what the handler does
// around a check that costs what a real one costs.
type fakePasswords struct {
	mu       sync.Mutex
	hashes   map[string]string   // email -> bcrypt hash ("missing" means no entry)
	refresh  map[string][]string // Dex sub -> client IDs holding a refresh token
	verified int                 // VerifyPassword calls
	updated  []string            // emails UpdatePassword was called for
	revoked  []string            // "sub/client" pairs RevokeRefresh was called for
	verifyFn func(email, plaintext string) error
	updateFn func(email, hash string) error
	listFn   func(sub string) ([]string, error)
	revokeFn func(sub, clientID string) error
}

func newFakePasswords() *fakePasswords {
	return &fakePasswords{hashes: map[string]string{}, refresh: map[string][]string{}}
}

// setPassword stores password as email's Dex password.
func (f *fakePasswords) setPassword(t *testing.T, email, password string) {
	t.Helper()
	hash, err := HashPassword(password)
	require.NoError(t, err)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hashes[email] = hash
}

// VerifyPassword mirrors Dex's server: bcrypt against the stored hash, and
// NotFound (which the client reports as ErrPasswordLookup under
// ErrPasswordNotVerified) when there is no entry.
func (f *fakePasswords) VerifyPassword(_ context.Context, email, plaintext string) error {
	f.mu.Lock()
	f.verified++
	hash, ok := f.hashes[email]
	fn := f.verifyFn
	f.mu.Unlock()
	if fn != nil {
		return fn(email, plaintext)
	}
	if !ok {
		return fmt.Errorf("%w: %w", dex.ErrPasswordNotVerified, dex.ErrPasswordLookup)
	}
	if plaintext == "" {
		return dex.ErrPasswordNotVerified
	}
	if err := CheckPassword(hash, plaintext); err != nil {
		return dex.ErrPasswordNotVerified
	}
	return nil
}

func (f *fakePasswords) UpdatePassword(_ context.Context, email, hash string) error {
	f.mu.Lock()
	fn := f.updateFn
	f.mu.Unlock()
	if fn != nil {
		return fn(email, hash)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hashes[email] = hash
	f.updated = append(f.updated, email)
	return nil
}

func (f *fakePasswords) RefreshClientIDs(_ context.Context, sub string) ([]string, error) {
	f.mu.Lock()
	fn := f.listFn
	f.mu.Unlock()
	if fn != nil {
		return fn(sub)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.refresh[sub]...), nil
}

func (f *fakePasswords) RevokeRefresh(_ context.Context, sub, clientID string) error {
	f.mu.Lock()
	fn := f.revokeFn
	f.mu.Unlock()
	if fn != nil {
		return fn(sub, clientID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, sub+"/"+clientID)
	delete(f.refresh, sub)
	return nil
}

func (f *fakePasswords) counts() (verified int, updated, revoked []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.verified, append([]string(nil), f.updated...), append([]string(nil), f.revoked...)
}

// checkPassword reports whether plaintext is email's current fake Dex
// password.
func (f *fakePasswords) checkPassword(t *testing.T, email, plaintext string) bool {
	t.Helper()
	f.mu.Lock()
	hash, ok := f.hashes[email]
	f.mu.Unlock()
	if !ok {
		return false
	}
	return CheckPassword(hash, plaintext) == nil
}

// countLimiter is a ChangePasswordLimiter that spends a fixed budget per key.
type countLimiter struct {
	mu     sync.Mutex
	budget int
	wait   time.Duration
	left   map[string]int
	spent  []string
}

func newCountLimiter(budget int, wait time.Duration) *countLimiter {
	return &countLimiter{budget: budget, wait: wait, left: map[string]int{}}
}

func (l *countLimiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.spent = append(l.spent, key)
	n, ok := l.left[key]
	if !ok {
		n = l.budget
	}
	if n <= 0 {
		return false, l.wait
	}
	l.left[key] = n - 1
	return true, 0
}

func (l *countLimiter) keys() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.spent...)
}

func postChangePassword(h *Handler, token string, body any) *httptest.ResponseRecorder {
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/change-password", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ChangePassword(rec, req)
	return rec
}

type errorBody struct {
	Error struct{ Code, Message string } `json:"error"`
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var body errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body
}

// --- tests ---

// TestChangePassword_RequiresAuth: it is a credential endpoint, so a missing or
// bad token must never reach Dex or the database.
func TestChangePassword_RequiresAuth(t *testing.T) {
	signer := newDexTokenSigner(t)
	fake := newFakePasswords()
	other := newDexTokenSigner(t)
	h := NewHandler(nil, &config.Config{}, nil, nil, signer.verifier()).WithPasswordService(fake)

	for _, tc := range []struct{ name, header string }{
		{"no header", ""},
		{"garbage", "Bearer not-a-jwt"},
		{"wrong key", "Bearer " + other.token(t, "CiQx", "x@example.com")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/change-password",
				strings.NewReader(`{"current_password":"a","new_password":"long-enough"}`))
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ChangePassword(rec, req)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
		})
	}
	verified, updated, revoked := fake.counts()
	assert.Zero(t, verified, "no token, no Dex call")
	assert.Empty(t, updated)
	assert.Empty(t, revoked)
}

// TestChangePassword_ValidatesNewPassword: the new password follows the rules
// signup and reset already apply, and a refusal happens before Dex is asked to
// verify anything (so a caller who mistypes their current password and picks a
// short new one learns about the short one, and no budget is spent guessing).
func TestChangePassword_ValidatesNewPassword(t *testing.T) {
	signer := newDexTokenSigner(t)
	fake := newFakePasswords()
	h := NewHandler(nil, &config.Config{}, nil, nil, signer.verifier()).WithPasswordService(fake)

	// The exact rules auth.ValidatePasswordLength states, which is what invite
	// signup, admin create and password reset call.
	tests := []struct {
		name, password, want string
	}{
		{"empty", "", "password must be at least 8 characters"},
		{"seven characters", "1234567", "password must be at least 8 characters"},
		{"seventy-three bytes", strings.Repeat("a", 73), "password must be at most 72 bytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tok := signer.token(t, uniq("CiQshort"), uniq("short")+"@example.com")
			rec := postChangePassword(h, tok, map[string]string{
				"current_password": "whatever-they-type", "new_password": tc.password})
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.want)
		})
	}

	// The boundary values are accepted, as signup accepts them.
	for _, password := range []string{"12345678", strings.Repeat("a", 72)} {
		assert.NoError(t, ValidatePasswordLength(password), "%d bytes must be allowed", len(password))
	}

	verified, updated, _ := fake.counts()
	assert.Zero(t, verified, "a new password the rules refuse is not worth a Dex verify call")
	assert.Empty(t, updated)
}

// TestChangePassword_RequiresCurrentPassword: an empty current_password is a
// 400, not an attempt to verify "".
func TestChangePassword_RequiresCurrentPassword(t *testing.T) {
	signer := newDexTokenSigner(t)
	fake := newFakePasswords()
	h := NewHandler(nil, &config.Config{}, nil, nil, signer.verifier()).WithPasswordService(fake)

	tok := signer.token(t, uniq("CiQnocur"), uniq("nocur")+"@example.com")
	rec := postChangePassword(h, tok, map[string]string{"new_password": "long-enough-pw"})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "current_password is required")
	verified, updated, _ := fake.counts()
	assert.Zero(t, verified)
	assert.Empty(t, updated)
}

// TestChangePassword_WrongCurrentPassword: the old password stays, the new one
// is not set, nothing is revoked, and the answer is 401.
func TestChangePassword_WrongCurrentPassword(t *testing.T) {
	pool := resolverTestDB(t)
	signer := newDexTokenSigner(t)
	v := signer.verifier().WithResolver(NewMemberResolver(pool))
	fake := newFakePasswords()
	h := NewHandler(NewRepo(pool), &config.Config{}, nil, nil, v).WithPasswordService(fake)

	sub, email := uniq("CiQwrong"), uniq("wrong")+"@example.com"
	userID := insertUser(t, pool, email, &sub)
	insertMembership(t, pool, userID, RoleFamilyMember, time.Now())
	fake.setPassword(t, email, "the-real-password")
	fake.refresh[sub] = []string{"home-os-ui"}

	rec := postChangePassword(h, signer.token(t, sub, email), map[string]string{
		"current_password": "not-the-real-password", "new_password": "brand-new-password"})
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	body := decodeError(t, rec)
	assert.Equal(t, "INVALID_CREDENTIALS", body.Error.Code)
	assert.Equal(t, "current password is incorrect", body.Error.Message)

	assert.True(t, fake.checkPassword(t, email, "the-real-password"), "the stored password must not change")
	assert.False(t, fake.checkPassword(t, email, "brand-new-password"), "the new password must not be set")
	_, updated, revoked := fake.counts()
	assert.Empty(t, updated, "nothing may be written for a failed check")
	assert.Empty(t, revoked, "other sessions must not be logged out by a failed attempt")
}

// TestChangePassword_WrongPasswordAndUnknownUserAreIndistinguishable is the
// core of the ticket: a wrong current password and an account Dex has no
// password for must produce the same status, the same body, and take the same
// time. A different answer (or a faster one) tells an attacker with a live
// session whether they are guessing against a real credential.
func TestChangePassword_WrongPasswordAndUnknownUserAreIndistinguishable(t *testing.T) {
	pool := resolverTestDB(t)
	signer := newDexTokenSigner(t)
	v := signer.verifier().WithResolver(NewMemberResolver(pool))
	fake := newFakePasswords()
	h := NewHandler(NewRepo(pool), &config.Config{}, nil, nil, v).WithPasswordService(fake)

	// The caller exists in Home OS (the token is real and resolves), but the
	// email has no Dex password: the "nonexistent user" case, which in a
	// signed-in flow is an account that signs in some other way.
	sub, email := uniq("CiQnopw"), uniq("nopw")+"@example.com"
	userID := insertUser(t, pool, email, &sub)
	insertMembership(t, pool, userID, RoleFamilyMember, time.Now())

	call := func(t *testing.T, current string) (*httptest.ResponseRecorder, time.Duration) {
		t.Helper()
		start := time.Now()
		rec := postChangePassword(h, signer.token(t, sub, email), map[string]string{
			"current_password": current, "new_password": "brand-new-password"})
		return rec, time.Since(start)
	}

	// Control: an account that does have a Dex password, guessed wrong, in the
	// very same handler with the very same fake.
	sub2, email2 := uniq("CiQhaspw"), uniq("haspw")+"@example.com"
	userID2 := insertUser(t, pool, email2, &sub2)
	insertMembership(t, pool, userID2, RoleFamilyMember, time.Now())
	fake.setPassword(t, email2, "the-real-password")
	wrongStart := time.Now()
	wrongRec := postChangePassword(h, signer.token(t, sub2, email2), map[string]string{
		"current_password": "not-the-real-password", "new_password": "brand-new-password"})
	wrongElapsed := time.Since(wrongStart)

	unknownRec, unknownElapsed := call(t, "not-the-real-password")

	// Same status, same bytes.
	require.Equal(t, http.StatusUnauthorized, wrongRec.Code, wrongRec.Body.String())
	require.Equal(t, http.StatusUnauthorized, unknownRec.Code, unknownRec.Body.String())
	assert.Equal(t, wrongRec.Body.String(), unknownRec.Body.String(),
		"a wrong password and an account with no Dex password must be byte-identical")

	// Same order of magnitude of time. A tolerance this wide still fails if
	// the no-entry path skips the bcrypt comparison entirely, which is the
	// leak: without it that path returns in microseconds while the real check
	// takes hundreds of milliseconds. Both runs are in this process against
	// the same fake, so scheduling noise is all the slack that is needed.
	ratio := float64(unknownElapsed) / float64(wrongElapsed)
	t.Logf("wrong password: %v, no password entry: %v (ratio %.2f)", wrongElapsed, unknownElapsed, ratio)
	assert.Greater(t, ratio, 0.5, "answering for a missing password entry must cost what a real comparison costs")
	assert.Less(t, ratio, 2.0, "answering for a missing password entry must not cost more")

	_, updated, revoked := fake.counts()
	assert.Empty(t, updated)
	assert.Empty(t, revoked)
}

// TestChangePassword_ChangesThePassword: the happy path writes the new bcrypt
// hash for the caller's own email, keeps users.password_hash in step, and
// revokes the caller's refresh tokens.
func TestChangePassword_ChangesThePassword(t *testing.T) {
	pool := resolverTestDB(t)
	signer := newDexTokenSigner(t)
	v := signer.verifier().WithResolver(NewMemberResolver(pool))
	fake := newFakePasswords()
	h := NewHandler(NewRepo(pool), &config.Config{}, nil, nil, v).WithPasswordService(fake)
	ctx := context.Background()

	sub, email := uniq("CiQhappy"), uniq("happy")+"@example.com"
	userID := insertUser(t, pool, email, &sub)
	insertMembership(t, pool, userID, RoleFamilyMember, time.Now())
	fake.setPassword(t, email, "the-real-password")
	fake.refresh[sub] = []string{"home-os-ui", "home-os-mcp"}

	// A second member must be untouched: this endpoint only ever acts on the
	// caller (the account comes from the verified token).
	otherSub, otherEmail := uniq("CiQother2"), uniq("other2")+"@example.com"
	otherID := insertUser(t, pool, otherEmail, &otherSub)
	insertMembership(t, pool, otherID, RoleFamilyMember, time.Now())
	fake.setPassword(t, otherEmail, "someone-elses-password")
	fake.refresh[otherSub] = []string{"home-os-ui"}

	rec := postChangePassword(h, signer.token(t, sub, email), map[string]string{
		"current_password": "the-real-password", "new_password": "the-new-password"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"), "a credential response must not be cached")

	var ok struct{ Message string }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ok))
	assert.Equal(t, "password changed", ok.Message)
	assert.NotContains(t, rec.Body.String(), "the-new-password", "never echo a password")

	assert.True(t, fake.checkPassword(t, email, "the-new-password"), "the new password must be set in Dex")
	assert.False(t, fake.checkPassword(t, email, "the-real-password"), "the old password must stop working")

	// The new hash is cost 12, the same as signup writes.
	fake.mu.Lock()
	hash := fake.hashes[email]
	fake.mu.Unlock()
	assert.True(t, strings.HasPrefix(hash, "$2a$12$"), "bcrypt cost 12, as everywhere else: %s", hash)

	// users.password_hash is kept in step with the credential that now signs in.
	var legacy string
	require.NoError(t, pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, userID).Scan(&legacy))
	assert.Equal(t, hash, legacy, "the legacy column must match what Dex now holds")

	// Every refresh token the caller held is revoked, one per client...
	_, _, revoked := fake.counts()
	sort.Strings(revoked)
	// ...and only the caller's: the other member's token is untouched.
	assert.Equal(t, []string{sub + "/home-os-mcp", sub + "/home-os-ui"}, revoked)
	assert.True(t, fake.checkPassword(t, otherEmail, "someone-elses-password"), "another member's password is untouched")

	// The old password no longer authenticates and the new one does, which is
	// one more round trip through the same check.
	rec = postChangePassword(h, signer.token(t, sub, email), map[string]string{
		"current_password": "the-real-password", "new_password": "third-password"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "the replaced password must be refused")

	rec = postChangePassword(h, signer.token(t, sub, email), map[string]string{
		"current_password": "the-new-password", "new_password": "third-password"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, fake.checkPassword(t, email, "third-password"))
}

// TestChangePassword_NoRefreshTokens: a caller with no offline session has
// nothing to revoke, and that is not an error.
func TestChangePassword_NoRefreshTokens(t *testing.T) {
	pool := resolverTestDB(t)
	signer := newDexTokenSigner(t)
	v := signer.verifier().WithResolver(NewMemberResolver(pool))
	fake := newFakePasswords()
	h := NewHandler(NewRepo(pool), &config.Config{}, nil, nil, v).WithPasswordService(fake)

	sub, email := uniq("CiQnosess"), uniq("nosess")+"@example.com"
	userID := insertUser(t, pool, email, &sub)
	insertMembership(t, pool, userID, RoleFamilyMember, time.Now())
	fake.setPassword(t, email, "the-real-password")

	rec := postChangePassword(h, signer.token(t, sub, email), map[string]string{
		"current_password": "the-real-password", "new_password": "the-new-password"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, fake.checkPassword(t, email, "the-new-password"))
}

// TestChangePassword_DexFailureIsNotAWrongPassword: if Dex cannot be reached
// the caller must not be told their password is wrong (which would send them
// to the reset flow over an outage), and nothing may be written.
func TestChangePassword_DexFailureIsNotAWrongPassword(t *testing.T) {
	pool := resolverTestDB(t)
	signer := newDexTokenSigner(t)
	v := signer.verifier().WithResolver(NewMemberResolver(pool))
	fake := newFakePasswords()
	outage := errors.New("connection refused")
	fake.verifyFn = func(string, string) error { return fmt.Errorf("dex verify password: %w", outage) }
	h := NewHandler(NewRepo(pool), &config.Config{}, nil, nil, v).WithPasswordService(fake)

	sub, email := uniq("CiQoutage"), uniq("outage")+"@example.com"
	userID := insertUser(t, pool, email, &sub)
	insertMembership(t, pool, userID, RoleFamilyMember, time.Now())

	rec := postChangePassword(h, signer.token(t, sub, email), map[string]string{
		"current_password": "the-real-password", "new_password": "the-new-password"})
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	body := decodeError(t, rec)
	assert.Equal(t, "BAD_GATEWAY", body.Error.Code)
	assert.NotContains(t, rec.Body.String(), "password is incorrect")
	_, updated, _ := fake.counts()
	assert.Empty(t, updated)
}

// TestChangePassword_UpdateFailureKeepsTheOldPassword: a failed write answers
// 502 and says nothing changed, because nothing did.
func TestChangePassword_UpdateFailureKeepsTheOldPassword(t *testing.T) {
	pool := resolverTestDB(t)
	signer := newDexTokenSigner(t)
	v := signer.verifier().WithResolver(NewMemberResolver(pool))
	fake := newFakePasswords()
	fake.updateFn = func(string, string) error { return errors.New("unavailable") }
	h := NewHandler(NewRepo(pool), &config.Config{}, nil, nil, v).WithPasswordService(fake)

	sub, email := uniq("CiQupd"), uniq("upd")+"@example.com"
	userID := insertUser(t, pool, email, &sub)
	insertMembership(t, pool, userID, RoleFamilyMember, time.Now())
	fake.setPassword(t, email, "the-real-password")
	fake.refresh[sub] = []string{"home-os-ui"}

	rec := postChangePassword(h, signer.token(t, sub, email), map[string]string{
		"current_password": "the-real-password", "new_password": "the-new-password"})
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "could not change your password")
	assert.True(t, fake.checkPassword(t, email, "the-real-password"), "the old password still works")
	_, _, revoked := fake.counts()
	assert.Empty(t, revoked, "a failed change must not end sessions")
}

// TestChangePassword_RevokesEveryRefreshClient: the caller may hold a refresh
// token for the UI and for an MCP client at once; both go. A revoke that fails
// still answers 200 (the password has changed and the failure is logged), and a
// NotFound from a racy rotation is not a failure.
func TestChangePassword_RevokesEveryRefreshClient(t *testing.T) {
	t.Run("one client already gone", func(t *testing.T) {
		pool := resolverTestDB(t)
		signer := newDexTokenSigner(t)
		v := signer.verifier().WithResolver(NewMemberResolver(pool))
		fake := newFakePasswords()
		fake.revokeFn = func(_ string, clientID string) error {
			if clientID == "home-os-mcp" {
				return dex.ErrRefreshNotFound
			}
			return nil
		}
		h := NewHandler(NewRepo(pool), &config.Config{}, nil, nil, v).WithPasswordService(fake)

		sub, email := uniq("CiQrace"), uniq("race")+"@example.com"
		userID := insertUser(t, pool, email, &sub)
		insertMembership(t, pool, userID, RoleFamilyMember, time.Now())
		fake.setPassword(t, email, "the-real-password")
		fake.refresh[sub] = []string{"home-os-ui", "home-os-mcp"}

		rec := postChangePassword(h, signer.token(t, sub, email), map[string]string{
			"current_password": "the-real-password", "new_password": "the-new-password"})
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	t.Run("revoke fails but the password did change", func(t *testing.T) {
		pool := resolverTestDB(t)
		signer := newDexTokenSigner(t)
		v := signer.verifier().WithResolver(NewMemberResolver(pool))
		fake := newFakePasswords()
		fake.revokeFn = func(string, string) error { return errors.New("unavailable") }
		h := NewHandler(NewRepo(pool), &config.Config{}, nil, nil, v).WithPasswordService(fake)

		sub, email := uniq("CiQrvk"), uniq("rvk")+"@example.com"
		userID := insertUser(t, pool, email, &sub)
		insertMembership(t, pool, userID, RoleFamilyMember, time.Now())
		fake.setPassword(t, email, "the-real-password")
		fake.refresh[sub] = []string{"home-os-ui"}

		rec := postChangePassword(h, signer.token(t, sub, email), map[string]string{
			"current_password": "the-real-password", "new_password": "the-new-password"})
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.True(t, fake.checkPassword(t, email, "the-new-password"),
			"the password change stands even though the revoke failed")
	})

	t.Run("listing the refresh tokens fails", func(t *testing.T) {
		pool := resolverTestDB(t)
		signer := newDexTokenSigner(t)
		v := signer.verifier().WithResolver(NewMemberResolver(pool))
		fake := newFakePasswords()
		fake.listFn = func(string) ([]string, error) { return nil, errors.New("unavailable") }
		h := NewHandler(NewRepo(pool), &config.Config{}, nil, nil, v).WithPasswordService(fake)

		sub, email := uniq("CiQlst"), uniq("lst")+"@example.com"
		userID := insertUser(t, pool, email, &sub)
		insertMembership(t, pool, userID, RoleFamilyMember, time.Now())
		fake.setPassword(t, email, "the-real-password")

		rec := postChangePassword(h, signer.token(t, sub, email), map[string]string{
			"current_password": "the-real-password", "new_password": "the-new-password"})
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.True(t, fake.checkPassword(t, email, "the-new-password"))
	})
}

// TestChangePassword_RateLimited: past the per-account budget the endpoint
// answers 429 with Retry-After and does not reach Dex, so guessing is bounded
// even when the guesses come from many addresses and even for a caller whose
// session is stolen. The budget is keyed by the account, not the request.
func TestChangePassword_RateLimited(t *testing.T) {
	pool := resolverTestDB(t)
	signer := newDexTokenSigner(t)
	v := signer.verifier().WithResolver(NewMemberResolver(pool))
	fake := newFakePasswords()
	limiter := newCountLimiter(3, 42*time.Second)
	h := NewHandler(NewRepo(pool), &config.Config{}, nil, nil, v).
		WithPasswordService(fake).WithChangePasswordLimiter(limiter)

	sub, email := uniq("CiQlimit"), uniq("limit")+"@example.com"
	userID := insertUser(t, pool, email, &sub)
	insertMembership(t, pool, userID, RoleFamilyMember, time.Now())
	fake.setPassword(t, email, "the-real-password")

	// Three guesses inside the budget, all refused as wrong passwords.
	for i := range 3 {
		rec := postChangePassword(h, signer.token(t, sub, email), map[string]string{
			"current_password": fmt.Sprintf("guess-%d", i), "new_password": "the-new-password"})
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "attempt %d: %s", i, rec.Body.String())
	}
	verifiedBefore, _, _ := fake.counts()

	rec := postChangePassword(h, signer.token(t, sub, email), map[string]string{
		"current_password": "guess-4", "new_password": "the-new-password"})
	require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	assert.Equal(t, "42", rec.Header().Get("Retry-After"), "Retry-After is the wait the limiter reported")
	body := decodeError(t, rec)
	assert.Equal(t, "RATE_LIMITED", body.Error.Code)

	verifiedAfter, _, _ := fake.counts()
	assert.Equal(t, verifiedBefore, verifiedAfter, "a rate-limited attempt must not reach Dex")

	// The budget is the account's, not the token's: a fresh token for the same
	// user is still limited, because a stolen session can mint tokens from the
	// same identity.
	rec = postChangePassword(h, signer.token(t, sub, email), map[string]string{
		"current_password": "the-real-password", "new_password": "the-new-password"})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	// Another account is unaffected: the budget is per user, not global.
	otherSub, otherEmail := uniq("CiQlimit2"), uniq("limit2")+"@example.com"
	otherID := insertUser(t, pool, otherEmail, &otherSub)
	insertMembership(t, pool, otherID, RoleFamilyMember, time.Now())
	fake.setPassword(t, otherEmail, "their-real-password")
	rec = postChangePassword(h, signer.token(t, otherSub, otherEmail), map[string]string{
		"current_password": "their-real-password", "new_password": "their-new-password"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	for _, key := range limiter.keys() {
		assert.Contains(t, key, "user:", "the budget is keyed by account")
		assert.NotContains(t, key, email, "the key must not carry the email")
	}
}

// TestChangePassword_NoDexConfigured: without Dex there is nothing to verify a
// password against or write one to, so the endpoint says so instead of
// pretending.
func TestChangePassword_NoDexConfigured(t *testing.T) {
	signer := newDexTokenSigner(t)
	h := NewHandler(nil, &config.Config{}, nil, nil, signer.verifier())
	require.Nil(t, h.passwords, "a nil *dex.Client must not become a non-nil PasswordService")

	rec := postChangePassword(h, signer.token(t, "CiQnodex", "nodex@example.com"), map[string]string{
		"current_password": "whatever", "new_password": "long-enough-pw"})
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Equal(t, "SERVICE_UNAVAILABLE", decodeError(t, rec).Error.Code)
}

// TestChangePassword_RejectsOversizedBody: the body is capped before it is
// parsed.
func TestChangePassword_RejectsOversizedBody(t *testing.T) {
	signer := newDexTokenSigner(t)
	fake := newFakePasswords()
	h := NewHandler(nil, &config.Config{}, nil, nil, signer.verifier()).WithPasswordService(fake)

	big := fmt.Sprintf(`{"current_password":%q,"new_password":"long-enough-pw"}`, strings.Repeat("a", changePasswordBodyLimit+1))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/change-password", strings.NewReader(big))
	req.Header.Set("Authorization", "Bearer "+signer.token(t, "CiQbig", "big@example.com"))
	rec := httptest.NewRecorder()
	h.ChangePassword(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	verified, _, _ := fake.counts()
	assert.Zero(t, verified)
}
