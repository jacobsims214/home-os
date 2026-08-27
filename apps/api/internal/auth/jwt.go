// Package auth provides JWT signing, OIDC verification, and claims extraction
// for the Home OS API. Tokens are issued by Dex and validated using Dex's JWKS
// endpoint with RS256 signature verification via coreos/go-oidc/v3.
//
// SignToken is retained for the registration/login flow (creates hand-rolled
// HS256 JWTs). It will be removed in the cleanup phase once all auth goes
// through Dex.
package auth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"

	"home-os/api/internal/config"
)

// Identity represents the core OIDC identity from a verified ID token.
type Identity struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
}

// Claims represents the JWT claims carried by every Home OS token.
// UserID, HouseholdID, and Email are required; Role and standard registered claims
// are set automatically by SignToken.
//
// For Dex-issued OIDC tokens, UserID is extracted from the sub claim, while
// HouseholdID and Role are extracted from custom claims (if present) or left empty.
//
// Note: Claims does NOT embed Identity because Identity.Subject conflicts with
// jwt.RegisteredClaims.Subject. Identity is used only for the OIDC verification step.
type Claims struct {
	UserID      string `json:"user_id"`
	HouseholdID string `json:"household_id"`
	Role        string `json:"role"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	jwt.RegisteredClaims
}

// SignToken creates a signed JWT string with HS256 signing, 24-hour expiry,
// and the standard issued-at / not-before timestamps.
//
// Deprecated: Will be removed once all auth flows go through Dex.
func SignToken(cfg *config.Config, claims Claims) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("sign token: config is nil")
	}
	if cfg.JWTSecret == "" {
		return "", fmt.Errorf("sign token: JWTSecret is empty")
	}

	now := time.Now()
	claims.RegisteredClaims = jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(cfg.JWTSecret))
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// Verifier validates Dex-issued RS256-signed OIDC tokens using coreos/go-oidc/v3.
// When audiences are configured, it validates that the token's "aud" claim
// contains at least one of the expected audience values after the go-oidc
// library has verified the signature, issuer, and expiry.
type Verifier struct {
	inner     *oidc.IDTokenVerifier
	enabled   bool
	audiences []string
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

	return &Claims{
		UserID: ident.Subject,
		Email:  ident.Email,
		Name:   ident.Name,
	}, nil
}

// OldVerifyToken is the legacy HS256-based verification function.
// Kept for backward compatibility until all callers are migrated.
// Deprecated: Use Verifier.VerifyToken instead.
func OldVerifyToken(cfg *config.Config, tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{},
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("verify token: unexpected signing method: %v", t.Header["alg"])
			}
			return []byte(cfg.JWTSecret), nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("verify token: invalid claims")
	}
	return claims, nil
}

// Compile-time assertion.
var _ jwt.Claims = (*Claims)(nil)

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