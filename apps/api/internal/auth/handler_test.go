package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
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

	t.Run("verified token without membership is forbidden", func(t *testing.T) {
		rec := postCaldavPassword(h, signer.token(t, uniq("CiQnobody"), uniq("nobody")+"@example.com"))
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	})

	t.Run("deactivated member is forbidden", func(t *testing.T) {
		goneSub := uniq("CiQgone")
		goneEmail := uniq("gone") + "@example.com"
		goneUID := insertUser(t, pool, goneEmail, &goneSub)
		insertMembership(t, pool, goneUID, "family_member", time.Now())
		_, err := pool.Exec(context.Background(), `UPDATE users SET active = false WHERE id = $1`, goneUID)
		require.NoError(t, err)

		rec := postCaldavPassword(h, signer.token(t, goneSub, goneEmail))
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		rec = getMe(h, signer.token(t, goneSub, goneEmail))
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	})
}

// stubResolver returns a fixed error (or member) so handler tests can drive
// every VerifyToken error kind without a database.
type stubResolver struct {
	m   *ResolvedMember
	err error
}

func (s stubResolver) Resolve(context.Context, string, string) (*ResolvedMember, error) {
	return s.m, s.err
}

func getMe(h *Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.Me(rec, req)
	return rec
}

// TestSelfVerifyingHandlers_ErrorMapping checks that Me and
// GenerateCaldavPassword, which verify tokens themselves outside RequireAuth,
// answer exactly like middleware.RequireAuth: 401 for a missing/malformed
// header or a bad/expired token, 403 for a non-member or deactivated user, and
// 500 when the membership lookup itself fails.
func TestSelfVerifyingHandlers_ErrorMapping(t *testing.T) {
	signer := newDexTokenSigner(t)
	other := newDexTokenSigner(t)

	expired := func(t *testing.T) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": handlerTestIssuer, "aud": "home-os-ui", "sub": "CiQold", "email": "old@example.com",
			"iat": time.Now().Add(-2 * time.Hour).Unix(), "exp": time.Now().Add(-time.Hour).Unix(),
		})
		str, err := tok.SignedString(signer.key)
		require.NoError(t, err)
		return str
	}

	dbErr := fmt.Errorf("resolve: %w: user by dex_sub: %w", ErrMemberLookup, errors.New("connection refused"))

	tests := []struct {
		name     string
		header   string // full Authorization header; "" sends none
		resolver MemberResolver
		want     int
		code     string
		message  string
	}{
		{"missing header", "", stubResolver{}, http.StatusUnauthorized, "UNAUTHORIZED", "missing authorization header"},
		{"non-bearer scheme", "Basic dXNlcjpwYXNz", stubResolver{}, http.StatusUnauthorized, "UNAUTHORIZED", "invalid authorization header format"},
		{"bearer without token", "Bearer", stubResolver{}, http.StatusUnauthorized, "UNAUTHORIZED", "invalid authorization header format"},
		{"garbage token", "Bearer not-a-jwt", stubResolver{}, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired token"},
		{"token signed by another key", "Bearer " + other.token(t, "CiQx", "x@example.com"), stubResolver{}, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired token"},
		{"expired token", "Bearer " + expired(t), stubResolver{}, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired token"},
		{"non-member", "Bearer " + signer.token(t, "CiQx", "x@example.com"), stubResolver{err: ErrNoMembership}, http.StatusForbidden, "FORBIDDEN", "authenticated, but this account is not a member of any household"},
		{"deactivated user", "Bearer " + signer.token(t, "CiQx", "x@example.com"), stubResolver{err: ErrUserDeactivated}, http.StatusForbidden, "FORBIDDEN", "this account has been deactivated"},
		{"membership lookup DB error", "Bearer " + signer.token(t, "CiQx", "x@example.com"), stubResolver{err: dbErr}, http.StatusInternalServerError, "INTERNAL_ERROR", "could not resolve household membership"},
	}

	handlers := []struct {
		name   string
		method string
		path   string
		serve  func(h *Handler) http.HandlerFunc
	}{
		{"Me", http.MethodGet, "/api/v1/auth/me", func(h *Handler) http.HandlerFunc { return h.Me }},
		{"GenerateCaldavPassword", http.MethodPost, "/api/v1/auth/caldav-password", func(h *Handler) http.HandlerFunc { return h.GenerateCaldavPassword }},
	}

	for _, hd := range handlers {
		for _, tc := range tests {
			t.Run(hd.name+"/"+tc.name, func(t *testing.T) {
				// repo is nil: every case must be answered before the handler
				// touches the database.
				h := NewHandler(nil, nil, &config.Config{}, nil, nil, signer.verifier().WithResolver(tc.resolver))
				req := httptest.NewRequest(hd.method, hd.path, nil)
				if tc.header != "" {
					req.Header.Set("Authorization", tc.header)
				}
				rec := httptest.NewRecorder()
				hd.serve(h)(rec, req)

				assert.Equal(t, tc.want, rec.Code, rec.Body.String())
				var body struct {
					Error struct{ Code, Message string } `json:"error"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				assert.Equal(t, tc.code, body.Error.Code)
				assert.Equal(t, tc.message, body.Error.Message)
			})
		}
	}
}
