// Package auth provides OIDC token verification for the MCP server.
// Tokens are issued by Dex and validated using Dex's JWKS endpoint
// with RS256 signature verification via coreos/go-oidc/v3.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

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
	// ClientID is the OAuth client the Dex token was issued to: the azp
	// claim, or the audience when the token has no azp (Dex sets azp only
	// for cross-client tokens). It is always a client this server accepts
	// (home-os-mcp, an mcp-<uuid> DCR client, or another configured MCP
	// client id). Empty for agent tokens (see Claims.AgentTokenID) and in
	// local-dev mode. The audit log records it for interactive sessions.
	ClientID string `json:"client_id,omitempty"`
}

// Claims represents the enriched claims used across the application.
// It embeds Identity and adds database-enriched fields (UserID, HouseholdID, Role).
// Handlers access these via ClaimsFromContext.
type Claims struct {
	Identity
	UserID      string `json:"user_id"`
	HouseholdID string `json:"household_id"`
	Role        string `json:"role"`
	// AgentTokenID is set when the caller authenticated with a per-member
	// agent token (hos_...) instead of a Dex JWT.
	AgentTokenID string `json:"agent_token_id,omitempty"`
	// Scopes are the agent token's scopes (ScopeRead, ScopeWrite). They are
	// nil for Dex sessions, which act with the member's full rights.
	Scopes []string `json:"scopes,omitempty"`
}

// Agent token scopes (see apps/api/internal/agenttoken).
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
)

// readOnlyRoles are the roles that may not change household data broadly
// through the MCP (household role policy, #4689; the API's policy is
// apps/api/internal/auth/roles.go). Since #4717 the narrow writes the role
// policy allows are reachable through the MCP too (a housesitter adds and
// checks off list items and adds notes on pets, a vendor updates a
// maintenance task's status): those are decided per tool by
// auth.ToolPolicy, not here. CanWrite answers the broader question "may this
// caller change anything it likes", which is what hints and agent-token
// scopes need.
var readOnlyRoles = map[string]bool{"viewer": true, "housesitter": true, "vendor": true}

// RoleIsReadOnly reports whether role is not a broad writer through the MCP.
// It is not the per-tool answer: ask auth.RequirementFor(tool).Allows(role)
// for that.
func RoleIsReadOnly(role string) bool { return readOnlyRoles[role] }

// CanWrite reports whether the caller may call tools that change data
// without a narrower grant. The member's role (re-read from memberships on
// every request, for Dex sessions and agent tokens alike) must not be
// read-only; then a Dex session can, and an agent token needs ScopeWrite.
// A narrow write (list_check_items for a housesitter) is allowed by the role
// policy even when this is false; see server.requirePolicy.
func (c *Claims) CanWrite() bool {
	if c == nil {
		return false
	}
	if readOnlyRoles[c.Role] {
		return false
	}
	if c.AgentTokenID == "" {
		return true
	}
	for _, s := range c.Scopes {
		if s == ScopeWrite {
			return true
		}
	}
	return false
}

// Verifier validates Dex-issued RS256-signed OIDC tokens via coreos/go-oidc/v3.
//
// An enabled verifier checks the signature (against the issuer's JWKS), the
// issuer, the expiry and the audience. The MCP server is an OAuth resource
// server, so it must only accept tokens issued for it (MCP spec, no token
// passthrough): the aud claim has to contain one of the configured audiences
// (WithAudiences) or start with one of the configured prefixes
// (WithAudiencePrefixes), and the client the token was issued to must be one
// of them too. That client is the azp claim when present; without azp every
// aud value must be accepted. So a Home OS UI token (aud home-os-ui), or a
// cross-client token the UI obtained for the MCP (aud [home-os-mcp,
// home-os-ui], azp home-os-ui), is refused. With no audiences configured the
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

// accepted reports whether a is an audience (or client id) this server
// accepts: an exact configured audience or one with a configured prefix.
func (v *Verifier) accepted(a string) bool {
	if a == "" {
		return false
	}
	if _, ok := v.audiences[a]; ok {
		return true
	}
	for _, p := range v.audPrefixes {
		if strings.HasPrefix(a, p) {
			return true
		}
	}
	return false
}

// tokenClient checks that a token was issued for this server and returns the
// OAuth client it was issued to. aud must contain an accepted value. The
// client is azp when set, and it must be accepted. Without azp, every aud
// value must be accepted (a multi-audience token without azp cannot say which
// client holds it, so a foreign audience means a foreign client) and the
// first one is the client.
func (v *Verifier) tokenClient(aud []string, azp string) (string, bool) {
	ok := false
	for _, a := range aud {
		if v.accepted(a) {
			ok = true
			break
		}
	}
	if !ok {
		return "", false
	}
	if azp != "" {
		return azp, v.accepted(azp)
	}
	for _, a := range aud {
		if !v.accepted(a) {
			return "", false
		}
	}
	return aud[0], true
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
	var claims struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		AZP     string `json:"azp"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("extract claims: %w", err)
	}
	clientID, ok := v.tokenClient(idToken.Audience, claims.AZP)
	if !ok {
		return nil, fmt.Errorf("verify token: %w (aud %v, azp %q)", ErrInvalidAudience, idToken.Audience, claims.AZP)
	}

	if claims.Subject == "" {
		return nil, fmt.Errorf("verify token: sub claim is empty")
	}

	return &Identity{
		Subject:  claims.Subject,
		Email:    claims.Email,
		Name:     claims.Name,
		ClientID: clientID,
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
// rules. A bearer starting with AgentTokenPrefix is instead checked by
// VerifyAgentToken and never parsed as a JWT. Errors:
//   - ErrNoMembership: token is valid but the identity is not a household
//     member. Answer 403 Forbidden.
//   - ErrUserDeactivated: token is valid but the Home OS user is deactivated
//     (users.active = false). Answer 403 Forbidden, like ErrNoMembership.
//   - ErrMemberLookup: database failure. Answer 500.
//   - anything else (including ErrInvalidAgentToken): the token itself is
//     invalid. Answer 401.
//
// In disabled mode (no issuer) or without a pool, the local-dev / un-enriched
// claims from VerifyToken are returned unchanged.
func (v *Verifier) VerifyTokenAndEnrich(ctx context.Context, pool *pgxpool.Pool, tokenStr string) (*Claims, error) {
	if v.enabled && IsAgentToken(tokenStr) {
		// Agent tokens are opaque secrets, never JWTs: they must not reach
		// the OIDC parser. Without a database they cannot be checked.
		if pool == nil {
			return nil, ErrInvalidAgentToken
		}
		return VerifyAgentToken(ctx, pool, tokenStr)
	}
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
// claims are left untouched and the error is (or wraps) ErrNoMembership,
// ErrUserDeactivated or ErrMemberLookup.
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

// ErrUserDeactivated is returned when a token verifies and maps to a Home OS
// user whose users.active is false (migration 037). It mirrors the REST API's
// auth.ErrUserDeactivated. The caller is authenticated but may not use the
// MCP: answer 403 Forbidden with no WWW-Authenticate challenge, exactly as for
// ErrNoMembership (signing in again cannot reactivate the account). The flag
// is read on every request, so deactivation takes effect immediately, even
// for tokens issued before it.
var ErrUserDeactivated = errors.New("user account is deactivated")

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
// A matched user with users.active = false fails with ErrUserDeactivated on
// either path, before any dex_sub backfill or membership lookup (as in the
// API).
//
// Users in several households resolve deterministically to their OLDEST
// membership (memberships.created_at, then memberships.id).
func ResolveMember(ctx context.Context, db DB, sub, email string) (*ResolvedMember, error) {
	if sub == "" {
		return nil, ErrNoMembership
	}

	var userID string
	var storedSub *string
	var active bool
	err := db.QueryRow(ctx, `SELECT id::text, active FROM users WHERE dex_sub = $1`, sub).Scan(&userID, &active)
	switch {
	case err == nil:
	case errors.Is(err, pgx.ErrNoRows):
		email = strings.TrimSpace(email)
		if email == "" {
			return nil, ErrNoMembership
		}
		err = db.QueryRow(ctx,
			`SELECT id::text, dex_sub, active FROM users WHERE lower(email) = lower($1)`, email,
		).Scan(&userID, &storedSub, &active)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoMembership
		}
		if err != nil {
			return nil, fmt.Errorf("%w: user by email: %w", ErrMemberLookup, err)
		}
		if !active {
			return nil, ErrUserDeactivated
		}
		backfillSub(ctx, db, userID, sub, storedSub)
	default:
		return nil, fmt.Errorf("%w: user by dex_sub: %w", ErrMemberLookup, err)
	}
	if !active {
		return nil, ErrUserDeactivated
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

// AgentTokenPrefix starts every per-member agent token. Format (#4404):
// "hos_" + base64url(32 random bytes, no padding), 47 characters in all.
const AgentTokenPrefix = "hos_"

const (
	agentTokenLen = 47
	// agentTokenTouchInterval throttles last_used_at writes: a token used
	// many times a minute costs one UPDATE per interval, not one per request.
	agentTokenTouchInterval = time.Minute
)

// ErrInvalidAgentToken is returned for any agent token that cannot be used:
// malformed, unknown, revoked, expired, owned by a deactivated user, or whose
// owner is no longer a member of the token's household. The cases are
// deliberately indistinguishable to the caller: answer 401 exactly as for an
// invalid JWT.
var ErrInvalidAgentToken = errors.New("invalid agent token")

// IsAgentToken reports whether a bearer credential is an agent token (by its
// prefix) rather than a Dex JWT. A JWT always starts with "ey".
func IsAgentToken(raw string) bool {
	return strings.HasPrefix(raw, AgentTokenPrefix)
}

// HashAgentToken returns the SHA-256 digest stored in agent_tokens.token_hash.
// It matches agenttoken.HashToken in apps/api: the hash covers the whole
// plaintext, prefix included.
func HashAgentToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// wellFormedAgentToken checks length and the base64url alphabet so garbage
// is refused without a database round trip.
func wellFormedAgentToken(raw string) bool {
	if len(raw) != agentTokenLen || !IsAgentToken(raw) {
		return false
	}
	for _, c := range raw[len(AgentTokenPrefix):] {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// VerifyAgentToken resolves a per-member agent token to Claims for its
// owner's membership in the token's household.
//
// The token is looked up by its SHA-256 digest (unique index on token_hash),
// so the plaintext is never compared or stored; the digest returned by the
// lookup is compared again in constant time as defence in depth. A token is
// accepted only when it is not revoked, not expired, its user is active
// (users.active) and that user is still a member of the token's household.
// Every refusal is ErrInvalidAgentToken; database failures wrap
// ErrMemberLookup (answer 500).
//
// last_used_at is refreshed at most once per agentTokenTouchInterval, and a
// failed refresh never fails the request. The token itself is never logged.
func VerifyAgentToken(ctx context.Context, db DB, raw string) (*Claims, error) {
	if !wellFormedAgentToken(raw) {
		return nil, ErrInvalidAgentToken
	}
	digest := HashAgentToken(raw)

	var (
		tokenID, userID, householdID, email, name string
		role                                      *string
		storedHash                                []byte
		scopes                                    []string
		revokedAt, expiresAt, lastUsedAt          *time.Time
		active                                    bool
		now                                       time.Time
	)
	err := db.QueryRow(ctx, `
		SELECT t.id::text, t.token_hash, t.user_id::text, t.household_id::text,
		       t.scopes, t.revoked_at, t.expires_at, t.last_used_at,
		       u.email, u.name, u.active, m.role::text, NOW()
		FROM agent_tokens t
		JOIN users u ON u.id = t.user_id
		LEFT JOIN memberships m ON m.user_id = t.user_id AND m.household_id = t.household_id
		WHERE t.token_hash = $1`, digest,
	).Scan(&tokenID, &storedHash, &userID, &householdID,
		&scopes, &revokedAt, &expiresAt, &lastUsedAt,
		&email, &name, &active, &role, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidAgentToken
	}
	if err != nil {
		return nil, fmt.Errorf("%w: agent token: %w", ErrMemberLookup, err)
	}
	if subtle.ConstantTimeCompare(storedHash, digest) != 1 {
		return nil, ErrInvalidAgentToken
	}

	var reason string
	switch {
	case revokedAt != nil:
		reason = "revoked"
	case expiresAt != nil && !expiresAt.After(now):
		reason = "expired"
	case !active:
		reason = "user deactivated"
	case role == nil:
		reason = "not a member of the token's household"
	}
	if reason != "" {
		slog.Warn("auth: agent token refused", "token_id", tokenID, "reason", reason)
		return nil, ErrInvalidAgentToken
	}

	if lastUsedAt == nil || now.Sub(*lastUsedAt) >= agentTokenTouchInterval {
		touchAgentToken(ctx, db, tokenID)
	}

	return &Claims{
		Identity:     Identity{Subject: userID, Email: email, Name: name},
		UserID:       userID,
		HouseholdID:  householdID,
		Role:         *role,
		AgentTokenID: tokenID,
		Scopes:       scopes,
	}, nil
}

// touchAgentToken records use of a token. The WHERE clause repeats the
// throttle so concurrent requests do not all write.
func touchAgentToken(ctx context.Context, db DB, tokenID string) {
	if _, err := db.Exec(ctx,
		`UPDATE agent_tokens SET last_used_at = NOW()
		 WHERE id = $1 AND (last_used_at IS NULL OR last_used_at <= NOW() - make_interval(secs => $2))`,
		tokenID, agentTokenTouchInterval.Seconds(),
	); err != nil {
		slog.Warn("auth: agent token last_used_at update failed", "token_id", tokenID, "error", err)
	}
}

// Close is a no-op — kept for interface compatibility.
// coreos/go-oidc/v3 does not use background refresh goroutines like keyfunc.
func (v *Verifier) Close() {}
