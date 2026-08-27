// Package auth provides OIDC token verification for the MCP server.
// Tokens are issued by Dex and validated using Dex's JWKS endpoint
// with RS256 signature verification via coreos/go-oidc/v3.
package auth

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Identity represents the core OIDC identity from a verified ID token.
type Identity struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
}

// Claims represents the enriched claims used across the application.
// It embeds Identity and adds database-enriched fields (UserID, HouseholdID, Role).
// Handlers access these via ClaimsFromContext.
type Claims struct {
	Identity
	UserID      string `json:"user_id"`
	HouseholdID string `json:"household_id"`
	Role        string `json:"role"`
}

// Verifier validates Dex-issued RS256-signed OIDC tokens via coreos/go-oidc/v3.
type Verifier struct {
	inner   *oidc.IDTokenVerifier
	pool    *pgxpool.Pool
	enabled bool
}

// NewVerifier creates a new Verifier that validates OIDC ID tokens.
// If issuerURL is empty, the verifier operates in disabled mode (returns anonymous identity).
// jwksURL is the full URL to the JWKS endpoint (e.g. "http://dex:5556/dex/keys").
// The pool is used for JIT provisioning and claims enrichment.
func NewVerifier(ctx context.Context, issuerURL, jwksURL string, pool *pgxpool.Pool) (*Verifier, error) {
	if issuerURL == "" {
		slog.Info("auth disabled mode: no issuer URL configured")
		return &Verifier{enabled: false}, nil
	}

	keySet := oidc.NewRemoteKeySet(ctx, jwksURL)
	inner := oidc.NewVerifier(issuerURL, keySet, &oidc.Config{
		SkipClientIDCheck: true,
		SkipIssuerCheck:   false,
	})

	slog.Info("oidc verifier created", "issuer", issuerURL, "jwks_url", jwksURL)
	return &Verifier{inner: inner, enabled: true, pool: pool}, nil
}

// Enabled returns whether the verifier is active (true) or in local-dev disabled mode (false).
func (v *Verifier) Enabled() bool {
	return v.enabled
}

// Verify validates the raw OIDC ID token and returns the core Identity.
// In disabled mode, returns a local-dev anonymous identity.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (*Identity, error) {
	if !v.enabled {
		return &Identity{Subject: "local-dev", Email: "local-dev@homeos.local", Name: "Local Dev"}, nil
	}

	idToken, err := v.inner.Verify(ctx, rawToken)
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
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

// VerifyToken validates a Dex-issued OIDC token and returns enriched Claims.
// Kept for backward compatibility during the OIDC migration transition.
func (v *Verifier) VerifyToken(ctx context.Context, rawToken string) (*Claims, error) {
	if !v.enabled {
		return &Claims{
			Identity: Identity{Subject: "local-dev", Email: "local-dev@homeos.local", Name: "Local Dev"},
			UserID:   "local-dev",
		}, nil
	}

	ident, err := v.Verify(ctx, rawToken)
	if err != nil {
		return nil, err
	}

	// Map OIDC sub to UserID by default; enrichment may overwrite this.
	return &Claims{
		Identity: *ident,
		UserID:   ident.Subject,
	}, nil
}

// VerifyTokenAndEnrich validates a Dex-issued OIDC token and enriches the claims
// with the user's household_id and role from the database. This is necessary because
// Dex tokens only carry OIDC standard claims (sub, email, name) — not Home OS-specific
// claims like household_id or role.
func (v *Verifier) VerifyTokenAndEnrich(ctx context.Context, pool *pgxpool.Pool, tokenStr string) (*Claims, error) {
	claims, err := v.VerifyToken(ctx, tokenStr)
	if err != nil {
		return nil, err
	}

	if pool == nil {
		return claims, nil
	}

	var userID, householdID, role string
	err = pool.QueryRow(ctx,
		`SELECT u.id, m.household_id, m.role FROM memberships m
		 JOIN users u ON u.id = m.user_id
		 WHERE u.email = $1`,
		claims.Email,
	).Scan(&userID, &householdID, &role)
	if err != nil {
		// If no membership found, allow but with empty household.
		// This lets the ping tool and health checks still work.
		slog.Warn("auth: no membership found for token email, proceeding without household context",
			"email", claims.Email, "error", err)
		return claims, nil
	}

	claims.UserID = userID
	claims.HouseholdID = householdID
	claims.Role = role

	slog.Debug("auth: enriched token claims with household context",
		"email", claims.Email, "household_id", householdID, "role", role)

	return claims, nil
}

// UpsertUserFromClaims creates or updates a user record in the database
// from the verified identity claims (JIT provisioning).
// Returns the user's database ID (UUID).
func (v *Verifier) UpsertUserFromClaims(ctx context.Context, pool *pgxpool.Pool, ident *Identity) (string, error) {
	if pool == nil {
		return ident.Subject, nil
	}

	var userID string
	err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, dex_sub)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (email) DO UPDATE SET
		   name = EXCLUDED.name,
		   dex_sub = EXCLUDED.dex_sub,
		   updated_at = NOW()
		 RETURNING id`,
		ident.Email, ident.Name, ident.Subject,
	).Scan(&userID)
	if err != nil {
		return "", fmt.Errorf("upsert user: %w", err)
	}

	slog.Debug("auth: upserted user from claims", "email", ident.Email, "user_id", userID)
	return userID, nil
}

// Close is a no-op — kept for interface compatibility.
// coreos/go-oidc/v3 does not use background refresh goroutines like keyfunc.
func (v *Verifier) Close() {}