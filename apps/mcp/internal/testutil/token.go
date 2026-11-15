package testutil

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
)

// TestIssuer is the issuer URL tokens from TokenIssuer carry.
const TestIssuer = "https://dex.test.homeos.local/dex"

// TokenIssuer signs RS256 OIDC id_tokens the way Dex does, and builds an
// enabled auth.Verifier that trusts them, so auth can be tested end to end
// without a Dex server.
type TokenIssuer struct {
	t   testing.TB
	key *rsa.PrivateKey
}

// NewTokenIssuer generates a fresh signing key.
func NewTokenIssuer(t testing.TB) *TokenIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("testutil: generate rsa key: %v", err)
	}
	return &TokenIssuer{t: t, key: key}
}

// Verifier returns an enabled auth.Verifier that accepts this issuer's tokens.
func (ti *TokenIssuer) Verifier(pool *pgxpool.Pool) *auth.Verifier {
	return auth.NewVerifierWithKeySet(TestIssuer,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&ti.key.PublicKey}}, pool)
}

// Token returns a signed id_token for sub/email, valid for one hour.
func (ti *TokenIssuer) Token(sub, email string) string {
	ti.t.Helper()
	now := time.Now()
	payload, err := json.Marshal(map[string]any{
		"iss":   TestIssuer,
		"aud":   "home-os-mcp",
		"sub":   sub,
		"email": email,
		"name":  "Test User",
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	if err != nil {
		ti.t.Fatalf("testutil: marshal claims: %v", err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: ti.key},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		ti.t.Fatalf("testutil: new signer: %v", err)
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		ti.t.Fatalf("testutil: sign token: %v", err)
	}
	raw, err := obj.CompactSerialize()
	if err != nil {
		ti.t.Fatalf("testutil: serialize token: %v", err)
	}
	return raw
}
