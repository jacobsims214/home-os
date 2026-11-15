package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"home-os/api/internal/config"
)

const handlerTestIssuer = "https://dex.test/dex"

// dexTokenSigner signs RS256 tokens shaped like Dex ID tokens and builds a
// Verifier that trusts them, so handler tests exercise real verification.
type dexTokenSigner struct{ key *rsa.PrivateKey }

func newDexTokenSigner(t *testing.T) *dexTokenSigner {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return &dexTokenSigner{key: k}
}

func (s *dexTokenSigner) token(t *testing.T, sub, email string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":   handlerTestIssuer,
		"aud":   "home-os-ui",
		"sub":   sub,
		"email": email,
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	str, err := tok.SignedString(s.key)
	require.NoError(t, err)
	return str
}

func (s *dexTokenSigner) verifier() *Verifier {
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{s.key.Public()}}
	return NewVerifierWithKeySet(handlerTestIssuer, ks, []string{"home-os-ui", "home-os-mcp"})
}

func postCaldavPassword(h *Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/caldav-password", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.GenerateCaldavPassword(rec, req)
	return rec
}

// caldavHashFor reads the hash exactly the way the calendar service's Basic
// auth does (apps/calendar/internal/db GetUserByEmailForCalDAV: lookup by the
// email used as the CalDAV username).
func caldavHashFor(t *testing.T, pool *pgxpool.Pool, username string) string {
	t.Helper()
	var hash *string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT caldav_password_hash FROM users WHERE email = $1`, username).Scan(&hash))
	require.NotNil(t, hash, "caldav_password_hash not set")
	return *hash
}

// caldavAuthenticates mirrors the calendar service's credential check.
func caldavAuthenticates(t *testing.T, pool *pgxpool.Pool, username, password string) bool {
	t.Helper()
	return bcrypt.CompareHashAndPassword([]byte(caldavHashFor(t, pool, username)), []byte(password)) == nil
}

func TestGenerateCaldavPassword_Unauthenticated(t *testing.T) {
	signer := newDexTokenSigner(t)
	h := NewHandler(nil, nil, &config.Config{}, nil, nil, signer.verifier())

	t.Run("no header", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, postCaldavPassword(h, "").Code)
	})
	t.Run("bad token", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, postCaldavPassword(h, "not-a-jwt").Code)
	})
	t.Run("token signed by another key", func(t *testing.T) {
		other := newDexTokenSigner(t)
		assert.Equal(t, http.StatusUnauthorized,
			postCaldavPassword(h, other.token(t, "sub", "x@example.com")).Code)
	})
}

func TestGenerateCaldavPasswordStrength(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		p, err := generateCaldavPassword()
		require.NoError(t, err)
		assert.Len(t, p, caldavPasswordLength)
		for _, c := range p {
			assert.Contains(t, caldavPasswordAlphabet, string(c))
		}
		assert.False(t, seen[p], "duplicate password generated")
		seen[p] = true
	}
}

func TestGenerateCaldavPassword_Member(t *testing.T) {
	pool := resolverTestDB(t)
	signer := newDexTokenSigner(t)
	v := signer.verifier().WithResolver(NewMemberResolver(pool))
	h := NewHandler(NewRepo(pool), nil, &config.Config{PublicURL: "https://home.example.com/"}, nil, nil, v)

	sub := uniq("CiQcaldav")
	email := uniq("caldav") + "@example.com"
	uid := insertUser(t, pool, email, &sub)
	insertMembership(t, pool, uid, "family_member", time.Now())
	// A second member must be unaffected by the caller's regeneration.
	otherSub := uniq("CiQother")
	otherEmail := uniq("other") + "@example.com"
	otherUID := insertUser(t, pool, otherEmail, &otherSub)
	insertMembership(t, pool, otherUID, "family_member", time.Now())

	gen := func(t *testing.T, sub, email string) caldavPasswordResponse {
		t.Helper()
		rec := postCaldavPassword(h, signer.token(t, sub, email))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		var resp caldavPasswordResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		return resp
	}

	first := gen(t, sub, email)
	assert.Len(t, first.Password, caldavPasswordLength)
	assert.Equal(t, email, first.Username)
	assert.Equal(t, "https://home.example.com/dav/", first.ServerURL)
	assert.True(t, caldavAuthenticates(t, pool, first.Username, first.Password),
		"returned password must authenticate against the CalDAV credential check")
	assert.False(t, caldavAuthenticates(t, pool, first.Username, first.Password+"x"))
	assert.Contains(t, caldavHashFor(t, pool, email), "$2a$12$", "bcrypt cost 12")

	other := gen(t, otherSub, otherEmail)

	second := gen(t, sub, email)
	assert.NotEqual(t, first.Password, second.Password)
	assert.True(t, caldavAuthenticates(t, pool, email, second.Password))
	assert.False(t, caldavAuthenticates(t, pool, email, first.Password),
		"regenerating must invalidate the previous password")
	assert.True(t, caldavAuthenticates(t, pool, otherEmail, other.Password),
		"another member's password must survive the caller's regeneration")

	t.Run("verified token without membership is refused", func(t *testing.T) {
		rec := postCaldavPassword(h, signer.token(t, uniq("CiQnobody"), uniq("nobody")+"@example.com"))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}
