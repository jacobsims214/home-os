package auth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/testutil"
)

const testResource = "https://home.example.test/mcp"

// TestVerify_ResourceServerChecks covers the token checks an OAuth resource
// server must make: signature, issuer, expiry and audience. No database is
// needed: Verify does not enrich.
func TestVerify_ResourceServerChecks(t *testing.T) {
	iss := testutil.NewTokenIssuer(t)
	other := testutil.NewTokenIssuer(t)
	v := iss.Verifier(nil,
		auth.WithAudiences("home-os-ui", testutil.TestAudience, testResource),
		auth.WithAudiencePrefixes("mcp-"),
	)
	expired := time.Now().Add(-time.Hour).Unix()

	tests := []struct {
		name    string
		token   string
		wantErr bool
		wantAud bool // error must be ErrInvalidAudience
	}{
		{"valid client-id audience", iss.Token("sub-1", "a@test.local"), false, false},
		{"valid ui audience", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": "home-os-ui"}), false, false},
		{"resource URI audience (RFC 8707)", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": testResource}), false, false},
		{"audience list containing an accepted value", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": []string{"other-api", testutil.TestAudience}}), false, false},
		{"dynamically registered client", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": "mcp-0f8e2c4a"}), false, false},
		{"wrong audience", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": "home-os-api"}), true, true},
		{"audience list without an accepted value", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": []string{"other-api", "billing"}}), true, true},
		{"resource URI of another server", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": "https://other.example.test/mcp"}), true, true},
		{"no audience", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": nil}), true, true},
		{"expired", iss.TokenWith("sub-1", "a@test.local", map[string]any{"exp": expired}), true, false},
		{"wrong issuer", iss.TokenWith("sub-1", "a@test.local", map[string]any{"iss": "https://evil.example/dex"}), true, false},
		{"unsigned (alg none)", iss.UnsignedToken("sub-1", "a@test.local"), true, false},
		{"signed by another key", other.Token("sub-1", "a@test.local"), true, false},
		{"empty sub", iss.Token("", "a@test.local"), true, false},
		{"garbage", "not-a-jwt", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ident, err := v.Verify(context.Background(), tc.token)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("Verify: %v", err)
				}
				if ident.Subject != "sub-1" || ident.Email != "a@test.local" {
					t.Fatalf("identity = %+v", ident)
				}
				return
			}
			if err == nil {
				t.Fatal("Verify accepted the token")
			}
			if got := errors.Is(err, auth.ErrInvalidAudience); got != tc.wantAud {
				t.Fatalf("errors.Is(err, ErrInvalidAudience) = %v, want %v (err %v)", got, tc.wantAud, err)
			}
			if errors.Is(err, auth.ErrNoMembership) || errors.Is(err, auth.ErrMemberLookup) {
				t.Fatalf("token error classified as membership error: %v", err)
			}
		})
	}
}

// TestVerify_FailsClosedWithoutAudiences: an enabled verifier with no
// accepted audience rejects even a perfectly signed token.
func TestVerify_FailsClosedWithoutAudiences(t *testing.T) {
	iss := testutil.NewTokenIssuer(t)
	v := iss.Verifier(nil, auth.WithAudiences(" ", ""), auth.WithAudiencePrefixes(""))
	_, err := v.Verify(context.Background(), iss.Token("sub-1", "a@test.local"))
	if !errors.Is(err, auth.ErrInvalidAudience) {
		t.Fatalf("err = %v, want ErrInvalidAudience", err)
	}
}

func TestNewVerifier(t *testing.T) {
	ctx := context.Background()

	v, err := auth.NewVerifier(ctx, "", "", nil)
	if err != nil || v.Enabled() {
		t.Fatalf("no issuer: v.Enabled()=%v err=%v, want disabled verifier", v != nil && v.Enabled(), err)
	}

	if _, err := auth.NewVerifier(ctx, testutil.TestIssuer, "http://127.0.0.1:1/keys", nil); err == nil ||
		!strings.Contains(err.Error(), "audiences") {
		t.Fatalf("issuer without audiences: err = %v, want a configuration error", err)
	}

	v, err = auth.NewVerifier(ctx, testutil.TestIssuer, "http://127.0.0.1:1/keys", nil,
		auth.WithAudiences(testutil.TestAudience))
	if err != nil || !v.Enabled() {
		t.Fatalf("issuer with audiences: err = %v", err)
	}
}

// newAgentToken returns a plaintext in the #4404 format.
func newAgentToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return auth.AgentTokenPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// seedAgentToken stores the hash of a fresh token for userID in householdID
// and returns the plaintext and token id. Extra SQL sets columns afterwards.
func seedAgentToken(t *testing.T, pool *pgxpool.Pool, userID, householdID uuid.UUID, scopes []string, expiresAt *time.Time) (string, uuid.UUID) {
	t.Helper()
	raw := newAgentToken(t)
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO agent_tokens (user_id, household_id, name, token_prefix, token_hash, scopes, expires_at)
		 VALUES ($1, $2, 'agent', $3, $4, $5, $6) RETURNING id`,
		userID, householdID, raw[:12], auth.HashAgentToken(raw), scopes, expiresAt,
	).Scan(&id); err != nil {
		t.Fatalf("insert agent token: %v", err)
	}
	return raw, id
}

func TestHashAgentToken_MatchesAPI(t *testing.T) {
	// agenttoken.HashToken in apps/api is SHA-256 over the full plaintext.
	want := sha256.Sum256([]byte("hos_abc"))
	if got := auth.HashAgentToken("hos_abc"); !bytes.Equal(got, want[:]) {
		t.Fatalf("hash mismatch")
	}
}

func TestVerifyAgentToken(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()
	t0 := time.Now().Add(-48 * time.Hour)

	uid := seedUser(t, pool, "agent-owner@test.local", "sub-agent-owner")
	hid := seedMembership(t, pool, uid, "family_member", t0)

	readTok, readID := seedAgentToken(t, pool, uid, hid, []string{"read"}, nil)
	future := time.Now().Add(time.Hour)
	rwTok, _ := seedAgentToken(t, pool, uid, hid, []string{"read", "write"}, &future)

	revokedTok, revokedID := seedAgentToken(t, pool, uid, hid, []string{"read", "write"}, nil)
	if _, err := pool.Exec(ctx, `UPDATE agent_tokens SET revoked_at = NOW() WHERE id = $1`, revokedID); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	expiredTok, _ := seedAgentToken(t, pool, uid, hid, []string{"read"}, &past)

	inactiveUID := seedUser(t, pool, "inactive@test.local", "sub-inactive")
	inactiveHID := seedMembership(t, pool, inactiveUID, "family_member", t0)
	inactiveTok, _ := seedAgentToken(t, pool, inactiveUID, inactiveHID, []string{"read"}, nil)
	if _, err := pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, inactiveUID); err != nil {
		t.Fatal(err)
	}

	// Token for a household the user has since left.
	var otherHID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO households (name) VALUES ('Old') RETURNING id`).Scan(&otherHID); err != nil {
		t.Fatal(err)
	}
	leftTok, _ := seedAgentToken(t, pool, uid, otherHID, []string{"read"}, nil)

	t.Run("valid read token", func(t *testing.T) {
		c, err := auth.VerifyAgentToken(ctx, pool, readTok)
		if err != nil {
			t.Fatalf("VerifyAgentToken: %v", err)
		}
		if c.UserID != uid.String() || c.HouseholdID != hid.String() || c.Role != "family_member" ||
			c.AgentTokenID != readID.String() || c.Email != "agent-owner@test.local" {
			t.Fatalf("claims = %+v", c)
		}
		if c.CanWrite() {
			t.Fatal("read-only token reports CanWrite")
		}
	})

	t.Run("valid read+write token", func(t *testing.T) {
		c, err := auth.VerifyAgentToken(ctx, pool, rwTok)
		if err != nil {
			t.Fatalf("VerifyAgentToken: %v", err)
		}
		if !c.CanWrite() {
			t.Fatalf("write token cannot write: %+v", c)
		}
	})

	malformed := []string{
		"hos_short",
		readTok[:len(readTok)-1] + "!", // right length, bad alphabet
		readTok + "A",                  // too long
		newAgentToken(t),               // well formed but unknown
		strings.Replace(readTok, "hos_", "hoz_", 1), // wrong prefix
	}
	rejected := map[string]string{
		"revoked":         revokedTok,
		"expired":         expiredTok,
		"inactive user":   inactiveTok,
		"left household":  leftTok,
		"malformed short": malformed[0],
		"bad alphabet":    malformed[1],
		"too long":        malformed[2],
		"unknown":         malformed[3],
		"wrong prefix":    malformed[4],
	}
	for name, tok := range rejected {
		t.Run(name, func(t *testing.T) {
			_, err := auth.VerifyAgentToken(ctx, pool, tok)
			if !errors.Is(err, auth.ErrInvalidAgentToken) {
				t.Fatalf("err = %v, want ErrInvalidAgentToken", err)
			}
			if errors.Is(err, auth.ErrMemberLookup) || errors.Is(err, auth.ErrNoMembership) {
				t.Fatalf("refusal classified as %v; must be a plain 401", err)
			}
		})
	}

	t.Run("database failure is ErrMemberLookup", func(t *testing.T) {
		broken := testutil.NewPool(t)
		broken.Close()
		_, err := auth.VerifyAgentToken(ctx, broken, readTok)
		if !errors.Is(err, auth.ErrMemberLookup) {
			t.Fatalf("err = %v, want ErrMemberLookup", err)
		}
	})
}

func TestVerifyAgentToken_LastUsedThrottled(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()
	uid := seedUser(t, pool, "touch@test.local", "sub-touch")
	hid := seedMembership(t, pool, uid, "owner", time.Now())
	tok, id := seedAgentToken(t, pool, uid, hid, []string{"read"}, nil)

	lastUsed := func() *time.Time {
		var ts *time.Time
		if err := pool.QueryRow(ctx, `SELECT last_used_at FROM agent_tokens WHERE id = $1`, id).Scan(&ts); err != nil {
			t.Fatal(err)
		}
		return ts
	}

	if _, err := auth.VerifyAgentToken(ctx, pool, tok); err != nil {
		t.Fatal(err)
	}
	first := lastUsed()
	if first == nil {
		t.Fatal("last_used_at not set on first use")
	}
	if _, err := auth.VerifyAgentToken(ctx, pool, tok); err != nil {
		t.Fatal(err)
	}
	if second := lastUsed(); !second.Equal(*first) {
		t.Fatalf("last_used_at moved within the throttle window: %v -> %v", first, second)
	}

	// Once the last use is older than the interval it is refreshed.
	if _, err := pool.Exec(ctx, `UPDATE agent_tokens SET last_used_at = NOW() - interval '2 minutes' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	stale := lastUsed()
	if _, err := auth.VerifyAgentToken(ctx, pool, tok); err != nil {
		t.Fatal(err)
	}
	if got := lastUsed(); !got.After(*stale) {
		t.Fatalf("last_used_at not refreshed after the interval: %v -> %v", stale, got)
	}
}

// TestVerifyTokenAndEnrich_AgentTokenPath: an enabled Dex verifier sends hos_
// bearers down the agent-token path (they never reach the JWT parser), and
// JWTs still take the Dex path.
func TestVerifyTokenAndEnrich_AgentTokenPath(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()
	uid := seedUser(t, pool, "both@test.local", "sub-both")
	hid := seedMembership(t, pool, uid, "owner", time.Now())
	tok, id := seedAgentToken(t, pool, uid, hid, []string{"read"}, nil)

	iss := testutil.NewTokenIssuer(t)
	v := iss.Verifier(pool)

	c, err := v.VerifyTokenAndEnrich(ctx, pool, tok)
	if err != nil {
		t.Fatalf("agent token: %v", err)
	}
	if c.AgentTokenID != id.String() || c.HouseholdID != hid.String() {
		t.Fatalf("claims = %+v", c)
	}

	if _, err := v.VerifyTokenAndEnrich(ctx, pool, newAgentToken(t)); !errors.Is(err, auth.ErrInvalidAgentToken) {
		t.Fatalf("unknown agent token: err = %v", err)
	}
	if _, err := v.VerifyTokenAndEnrich(ctx, nil, tok); !errors.Is(err, auth.ErrInvalidAgentToken) {
		t.Fatalf("no pool: err = %v, want ErrInvalidAgentToken", err)
	}

	c, err = v.VerifyTokenAndEnrich(ctx, pool, iss.Token("sub-both", "both@test.local"))
	if err != nil {
		t.Fatalf("dex token: %v", err)
	}
	if c.AgentTokenID != "" || c.Scopes != nil || !c.CanWrite() || c.UserID != uid.String() {
		t.Fatalf("dex claims = %+v", c)
	}
}
