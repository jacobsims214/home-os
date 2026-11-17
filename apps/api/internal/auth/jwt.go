// Package auth provides OIDC token verification for the Home OS API.
// Tokens issued by Dex are validated using Dex's JWKS endpoint with RS256 signature
// verification via coreos/go-oidc/v3. Nothing here mints a token: every credential
// Home OS accepts is issued by Dex (authorization code + PKCE), and #531 removed
// the last HS256 signer so no hand-rolled JWT path can come back.
package auth

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Identity represents the core OIDC identity from a verified ID token.
type Identity struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
}

// Verifier validates Dex-issued RS256-signed OIDC tokens using coreos/go-oidc/v3.
// When audiences are configured, it validates that the token's "aud" claim
// contains at least one of the expected audience values after the go-oidc
// library has verified the signature, issuer, and expiry.
type Verifier struct {
	inner     *oidc.IDTokenVerifier
	enabled   bool
	audiences []string
	resolver  MemberResolver
}

// WithResolver attaches a MemberResolver. When set, VerifyToken maps the Dex
// identity to the Home OS user (Claims.UserID = users.id), household and role,
// and fails with ErrNoMembership for verified tokens that are not members.
func (v *Verifier) WithResolver(r MemberResolver) *Verifier {
	v.resolver = r
	return v
}

// NewVerifierWithKeySet builds an enabled Verifier over a caller-supplied key
// set (e.g. oidc.StaticKeySet). Intended for tests that sign their own tokens.
func NewVerifierWithKeySet(issuerURL string, keySet oidc.KeySet, audiences []string) *Verifier {
	inner := oidc.NewVerifier(issuerURL, keySet, &oidc.Config{SkipClientIDCheck: true})
	return &Verifier{inner: inner, enabled: true, audiences: audiences}
}

// NewVerifier creates a new Verifier that validates OIDC ID tokens.
// If issuerURL is empty, the verifier operates in disabled mode.
// jwksURL is the full URL to the JWKS endpoint.
// audiences is the list of acceptable "aud" (audience) claims; the token
// must contain at least one matching audience to be considered valid.
func NewVerifier(ctx context.Context, issuerURL, jwksURL string, audiences []string) (*Verifier, error) {
	if issuerURL == "" {
		slog.Info("auth disabled mode: no issuer URL configured")
		return &Verifier{enabled: false}, nil
	}

	keySet := oidc.NewRemoteKeySet(ctx, jwksURL)
	inner := oidc.NewVerifier(issuerURL, keySet, &oidc.Config{
		SkipClientIDCheck: true,
		SkipIssuerCheck:   false,
	})

	slog.Info("oidc verifier created", "issuer", issuerURL, "jwks_url", jwksURL, "audiences", audiences)
	return &Verifier{inner: inner, enabled: true, audiences: audiences}, nil
}

// Enabled returns whether the verifier is active (true) or in disabled mode (false).
func (v *Verifier) Enabled() bool {
	return v.enabled
}

// Verify validates the raw OIDC ID token and returns the core Identity.
// In disabled mode, returns a local-dev anonymous identity.
// After go-oidc verifies the signature, issuer, and expiry, it also validates
// that the token's "aud" (audience) claim contains at least one of the
// expected audiences configured on the verifier.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (*Identity, error) {
	if !v.enabled {
		return &Identity{Subject: "local-dev", Email: "local-dev@homeos.local", Name: "Local Dev"}, nil
	}

	idToken, err := v.inner.Verify(ctx, rawToken)
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}

	// Validate audience: the token must contain at least one of the
	// expected audience values. go-oidc's SkipClientIDCheck skips the
	// single-audience check, so we do our own multi-audience check here.
	if !v.hasValidAudience(idToken.Audience) {
		return nil, fmt.Errorf("verify token: audience %v does not match any expected audience %v", idToken.Audience, v.audiences)
	}

	var claims struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("extract claims: %w", err)
	}

	if claims.Subject == "" {
		return nil, fmt.Errorf("verify token: sub claim is empty")
	}

	return &Identity{
		Subject: claims.Subject,
		Email:   claims.Email,
		Name:    claims.Name,
	}, nil
}

// VerifyToken parses and validates a Dex-issued OIDC token string.
// It verifies the RS256 signature using Dex's JWKS, then validates the
// issuer, audience, and expiration claims.
//
// When a MemberResolver is attached (WithResolver), the returned Claims carry
// the Home OS users.id as UserID plus HouseholdID and Role; a verified token
// with no membership returns an error wrapping ErrNoMembership, a deactivated
// user one wrapping ErrUserDeactivated, and a database failure one wrapping
// ErrMemberLookup (see WriteVerifyError for the HTTP mapping). Without a resolver, UserID is the raw
// Dex sub and HouseholdID/Role are empty.
func (v *Verifier) VerifyToken(ctx context.Context, tokenStr string) (*Claims, error) {
	if !v.enabled {
		return &Claims{
			UserID: "local-dev",
			Email:  "local-dev@homeos.local",
			Name:   "Local Dev",
		}, nil
	}

	ident, err := v.Verify(ctx, tokenStr)
	if err != nil {
		return nil, err
	}

	claims := &Claims{
		UserID: ident.Subject,
		Email:  ident.Email,
		Name:   ident.Name,
		DexSub: ident.Subject,
	}
	if v.resolver == nil {
		return claims, nil
	}

	m, err := v.resolver.Resolve(ctx, ident.Subject, ident.Email)
	if err != nil {
		return nil, fmt.Errorf("resolve member (email=%q): %w", ident.Email, err)
	}
	claims.UserID = m.UserID
	claims.HouseholdID = m.HouseholdID
	claims.Role = m.Role
	return claims, nil
}

// Close is a no-op — kept for interface compatibility.
// coreos/go-oidc/v3 does not use background refresh goroutines like keyfunc.
func (v *Verifier) Close() {}

// hasValidAudience checks whether the token's audience claim contains at
// least one of the expected audiences configured on the verifier. If no
// audiences are configured, any audience is accepted (backward compatibility).
func (v *Verifier) hasValidAudience(tokenAudience []string) bool {
	if len(v.audiences) == 0 {
		return true
	}
	for _, expected := range v.audiences {
		for _, aud := range tokenAudience {
			if aud == expected {
				return true
			}
		}
	}
	return false
}
