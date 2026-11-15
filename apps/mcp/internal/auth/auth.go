// Package auth provides OIDC token verification for the MCP server.
// Tokens are issued by Dex and validated using Dex's JWKS endpoint
// with RS256 signature verification via coreos/go-oidc/v3.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// NewVerifierWithKeySet creates an enabled Verifier that checks signatures
// against keySet instead of a remote JWKS endpoint. Tests use it with an
// oidc.StaticKeySet to verify locally signed tokens without a Dex server.
func NewVerifierWithKeySet(issuerURL string, keySet oidc.KeySet, pool *pgxpool.Pool) *Verifier {
	inner := oidc.NewVerifier(issuerURL, keySet, &oidc.Config{SkipClientIDCheck: true})
	return &Verifier{inner: inner, enabled: true, pool: pool}
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
// with the user's Home OS id, household_id and role from the database. Dex
// tokens only carry OIDC standard claims (sub, email, name), not Home OS ones.
//
// It is VerifyToken followed by EnrichClaims; see ResolveMember for the lookup
// rules. Errors:
//   - ErrNoMembership: token is valid but the identity is not a household
//     member. Answer 403 Forbidden.
//   - ErrMemberLookup: database failure. Answer 500.
//   - anything else: the token itself is invalid. Answer 401.
//
// In disabled mode (no issuer) or without a pool, the local-dev / un-enriched
// claims from VerifyToken are returned unchanged.
func (v *Verifier) VerifyTokenAndEnrich(ctx context.Context, pool *pgxpool.Pool, tokenStr string) (*Claims, error) {
	claims, err := v.VerifyToken(ctx, tokenStr)
	if err != nil {
		return nil, err
	}
	if !v.enabled || pool == nil {
		return claims, nil
	}
	if err := EnrichClaims(ctx, pool, claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// EnrichClaims resolves claims.Subject / claims.Email to a Home OS member (see
// ResolveMember) and sets claims.UserID, HouseholdID and Role. On error the
// claims are left untouched and the error wraps ErrNoMembership or
// ErrMemberLookup.
func EnrichClaims(ctx context.Context, db DB, claims *Claims) error {
	m, err := ResolveMember(ctx, db, claims.Subject, claims.Email)
	if err != nil {
		return err
	}
	claims.UserID = m.UserID
	claims.HouseholdID = m.HouseholdID
	claims.Role = m.Role
	slog.Debug("auth: enriched token claims with household context",
		"sub", claims.Subject, "user_id", m.UserID, "household_id", m.HouseholdID, "role", m.Role)
	return nil
}

// ErrNoMembership is returned when a token verifies but the identity does not
// map to a Home OS user with at least one household membership. The caller is
// authenticated but not a member: answer 403 Forbidden.
var ErrNoMembership = errors.New("no household membership for this identity")

// ErrMemberLookup wraps database failures while resolving the membership.
// Answer 500 rather than treating it as a bad token.
var ErrMemberLookup = errors.New("membership lookup failed")

// DB is the subset of *pgxpool.Pool (or pgx.Tx) ResolveMember needs.
type DB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ResolvedMember is the Home OS identity a Dex subject resolves to.
type ResolvedMember struct {
	UserID      string
	HouseholdID string
	Role        string
}

// ResolveMember maps a verified Dex identity to a Home OS user and household
// membership. It mirrors the REST API's PGMemberResolver (apps/api/internal/
// auth/resolve.go) so both servers resolve the same token to the same member.
//
// Lookup order:
//  1. users.dex_sub = sub (the stable OIDC key; migration 033).
//  2. lower(users.email) = lower(email). Email is mutable, so this exists only
//     to adopt users created before dex_sub was recorded: when the matched
//     user's dex_sub is NULL it is backfilled with sub, and every later
//     request takes path 1. A user already carrying a different dex_sub is not
//     overwritten (logged) but still resolves, as in the API.
//
// Users in several households resolve deterministically to their OLDEST
// membership (memberships.created_at, then memberships.id).
func ResolveMember(ctx context.Context, db DB, sub, email string) (*ResolvedMember, error) {
	if sub == "" {
		return nil, ErrNoMembership
	}

	var userID string
	var storedSub *string
	err := db.QueryRow(ctx, `SELECT id::text FROM users WHERE dex_sub = $1`, sub).Scan(&userID)
	switch {
	case err == nil:
	case errors.Is(err, pgx.ErrNoRows):
		email = strings.TrimSpace(email)
		if email == "" {
			return nil, ErrNoMembership
		}
		err = db.QueryRow(ctx,
			`SELECT id::text, dex_sub FROM users WHERE lower(email) = lower($1)`, email,
		).Scan(&userID, &storedSub)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoMembership
		}
		if err != nil {
			return nil, fmt.Errorf("%w: user by email: %v", ErrMemberLookup, err)
		}
		backfillSub(ctx, db, userID, sub, storedSub)
	default:
		return nil, fmt.Errorf("%w: user by dex_sub: %v", ErrMemberLookup, err)
	}

	m := ResolvedMember{UserID: userID}
	err = db.QueryRow(ctx,
		`SELECT household_id::text, role::text FROM memberships
		 WHERE user_id = $1
		 ORDER BY created_at ASC, id ASC
		 LIMIT 1`, userID,
	).Scan(&m.HouseholdID, &m.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoMembership
	}
	if err != nil {
		return nil, fmt.Errorf("%w: membership: %v", ErrMemberLookup, err)
	}
	return &m, nil
}

// backfillSub stores sub on an email-matched user whose dex_sub is still NULL.
// A failed backfill never fails the request: the email path keeps working.
func backfillSub(ctx context.Context, db DB, userID, sub string, storedSub *string) {
	if storedSub != nil {
		if *storedSub != sub {
			slog.Warn("auth: email-matched user already has a different dex_sub; not overwriting",
				"user_id", userID)
		}
		return
	}
	if _, err := db.Exec(ctx,
		`UPDATE users SET dex_sub = $1, updated_at = NOW()
		 WHERE id = $2 AND dex_sub IS NULL`, sub, userID,
	); err != nil {
		slog.Warn("auth: dex_sub backfill failed", "user_id", userID, "error", err)
		return
	}
	slog.Info("auth: backfilled users.dex_sub from email match", "user_id", userID)
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
