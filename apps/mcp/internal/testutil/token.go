package testutil

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
)

// TestIssuer is the issuer URL tokens from TokenIssuer carry.
const TestIssuer = "https://dex.test.homeos.local/dex"

// TestAudience is the aud tokens from TokenIssuer carry by default, and the
// audience Verifier accepts (Dex sets aud to the OAuth client_id).
const TestAudience = "home-os-mcp"

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

// Verifier returns an enabled auth.Verifier that accepts this issuer's
// tokens for TestAudience. Pass opts to accept other audiences instead.
func (ti *TokenIssuer) Verifier(pool *pgxpool.Pool, opts ...auth.Option) *auth.Verifier {
	if len(opts) == 0 {
		opts = []auth.Option{auth.WithAudiences(TestAudience)}
	}
	return auth.NewVerifierWithKeySet(TestIssuer,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&ti.key.PublicKey}}, pool, opts...)
}

// Token returns a signed id_token for sub/email, valid for one hour.
func (ti *TokenIssuer) Token(sub, email string) string {
	ti.t.Helper()
	return ti.TokenWith(sub, email, nil)
}

// TokenWith is Token with claims overridden or added (for example "aud",
// "exp" or "iss"); a nil value removes the claim.
func (ti *TokenIssuer) TokenWith(sub, email string, overrides map[string]any) string {
	ti.t.Helper()
	now := time.Now()
	claims := map[string]any{
		"iss":   TestIssuer,
		"aud":   TestAudience,
		"sub":   sub,
		"email": email,
		"name":  "Test User",
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	for k, v := range overrides {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	payload, err := json.Marshal(claims)
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

// UnsignedToken returns an alg "none" JWT with the same claims as Token: an
// attacker-made token no verifier may accept.
func (ti *TokenIssuer) UnsignedToken(sub, email string) string {
	ti.t.Helper()
	signed := ti.Token(sub, email)
	var payload string
	if _, rest, ok := strings.Cut(signed, "."); ok {
		payload, _, _ = strings.Cut(rest, ".")
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	return header + "." + payload + "."
}
