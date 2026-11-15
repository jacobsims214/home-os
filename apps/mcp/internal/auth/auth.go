// Package auth provides OIDC token verification for the MCP server.
// Tokens are issued by Dex and validated using Dex's JWKS endpoint
// with RS256 signature verification via coreos/go-oidc/v3.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
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
//
// An enabled verifier checks the signature (against the issuer's JWKS), the
// issuer, the expiry and the audience. The MCP server is an OAuth resource
// server, so it must only accept tokens issued for it: the aud claim has to
// contain one of the configured audiences (WithAudiences) or start with one of
// the configured prefixes (WithAudiencePrefixes). With neither configured the
// verifier fails closed and rejects every token.
type Verifier struct {
	inner       *oidc.IDTokenVerifier
	pool        *pgxpool.Pool
	enabled     bool
	audiences   map[string]struct{}
	audPrefixes []string
}

// Option configures a Verifier.
type Option func(*Verifier)

// WithAudiences accepts tokens whose aud claim contains any of auds exactly.
// Pass the accepted client IDs (Dex puts the client_id in aud) and the
// server's canonical resource URI (RFC 8707). Empty values are ignored.
func WithAudiences(auds ...string) Option {
	return func(v *Verifier) {
		for _, a := range auds {
			if a = strings.TrimSpace(a); a != "" {
				v.audiences[a] = struct{}{}
			}
		}
	}
}

// WithAudiencePrefixes accepts tokens whose aud claim contains a value
// starting with any of prefixes. It exists for clients registered through the
// metadata server's /register endpoint, whose Dex client IDs (and so token
// audiences) are minted as "mcp-<uuid>". Empty prefixes are ignored: they
// would accept every audience.
func WithAudiencePrefixes(prefixes ...string) Option {
	return func(v *Verifier) {
		for _, p := range prefixes {
			if p = strings.TrimSpace(p); p != "" {
				v.audPrefixes = append(v.audPrefixes, p)
			}
		}
	}
}

// ErrInvalidAudience is returned (wrapped) when a token verifies but was not
// issued for this server. Answer 401 like any other invalid token.
var ErrInvalidAudience = errors.New("token audience not accepted by this server")

func newVerifier(inner *oidc.IDTokenVerifier, pool *pgxpool.Pool, opts []Option) *Verifier {
	v := &Verifier{inner: inner, enabled: true, pool: pool, audiences: map[string]struct{}{}}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// NewVerifier creates a new Verifier that validates OIDC ID tokens.
// If issuerURL is empty, the verifier operates in disabled mode (returns anonymous identity).
// jwksURL is the full URL to the JWKS endpoint (e.g. "http://dex:5556/dex/keys").
// The pool is used for claims enrichment. An enabled verifier needs at least
// one accepted audience or audience prefix; otherwise NewVerifier fails.
func NewVerifier(ctx context.Context, issuerURL, jwksURL string, pool *pgxpool.Pool, opts ...Option) (*Verifier, error) {
	if issuerURL == "" {
		slog.Info("auth disabled mode: no issuer URL configured")
		return &Verifier{enabled: false}, nil
	}

	keySet := oidc.NewRemoteKeySet(ctx, jwksURL)
	// go-oidc can only check a single client ID, so the audience check is
	// done in Verify against the configured set.
	inner := oidc.NewVerifier(issuerURL, keySet, &oidc.Config{SkipClientIDCheck: true})
	v := newVerifier(inner, pool, opts)
	if len(v.audiences) == 0 && len(v.audPrefixes) == 0 {
		return nil, errors.New("auth: no accepted token audiences configured")
	}

	slog.Info("oidc verifier created", "issuer", issuerURL, "jwks_url", jwksURL,
		"audiences", v.audienceList(), "audience_prefixes", v.audPrefixes)
	return v, nil
}

// NewVerifierWithKeySet creates an enabled Verifier that checks signatures
// against keySet instead of a remote JWKS endpoint. Tests use it with an
// oidc.StaticKeySet to verify locally signed tokens without a Dex server.
// Without audience options it rejects every token (fail closed).
func NewVerifierWithKeySet(issuerURL string, keySet oidc.KeySet, pool *pgxpool.Pool, opts ...Option) *Verifier {
	inner := oidc.NewVerifier(issuerURL, keySet, &oidc.Config{SkipClientIDCheck: true})
	return newVerifier(inner, pool, opts)
}

// audienceAccepted reports whether any aud value is one this server accepts.
func (v *Verifier) audienceAccepted(aud []string) bool {
	for _, a := range aud {
		if _, ok := v.audiences[a]; ok {
			return true
		}
		for _, p := range v.audPrefixes {
			if strings.HasPrefix(a, p) {
				return true
			}
		}
	}
	return false
}

func (v *Verifier) audienceList() []string {
	out := make([]string, 0, len(v.audiences))
	for a := range v.audiences {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// Enabled returns whether the verifier is active (true) or in local-dev disabled mode (false).
func (v *Verifier) Enabled() bool {
	return v.enabled
}

// Verify validates the raw OIDC ID token (signature, issuer, expiry and
// audience) and returns the core Identity.
// In disabled mode, returns a local-dev anonymous identity.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (*Identity, error) {
	if !v.enabled {
		return &Identity{Subject: "local-dev", Email: "local-dev@homeos.local", Name: "Local Dev"}, nil
	}

	idToken, err := v.inner.Verify(ctx, rawToken)
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}
	if !v.audienceAccepted(idToken.Audience) {
		return nil, fmt.Errorf("verify token: %w (aud %v)", ErrInvalidAudience, idToken.Audience)
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
			return nil, fmt.Errorf("%w: user by email: %w", ErrMemberLookup, err)
		}
		backfillSub(ctx, db, userID, sub, storedSub)
	default:
		return nil, fmt.Errorf("%w: user by dex_sub: %w", ErrMemberLookup, err)
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
		return nil, fmt.Errorf("%w: membership: %w", ErrMemberLookup, err)
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
