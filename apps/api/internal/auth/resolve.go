package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoMembership is returned when a token verifies but the identity does not
// map to a Home OS user with at least one household membership. Callers should
// answer 403 Forbidden (the caller is authenticated, just not a member).
var ErrNoMembership = errors.New("no household membership for this identity")

// ErrMemberLookup wraps database failures while resolving the membership.
// Callers should answer 500 rather than treating it as a bad token.
var ErrMemberLookup = errors.New("membership lookup failed")

// ErrUserDeactivated is returned when a token verifies and maps to a Home OS
// user whose users.active is false (migration 037). Callers should answer 403
// Forbidden, like ErrNoMembership: the caller is authenticated but may not use
// the API.
var ErrUserDeactivated = errors.New("user account is deactivated")

// ErrNotHouseholdMember is returned when the request names a household
// (X-Household-ID) that the caller is not a member of. Callers should answer
// 403 Forbidden. Instance admins get no implicit access: membership is the
// only thing that counts.
var ErrNotHouseholdMember = errors.New("not a member of the requested household")

// HouseholdHeader lets a client that belongs to several households pick one
// per request. When absent, the oldest membership is used.
const HouseholdHeader = "X-Household-ID"

type requestedHouseholdKey struct{}

// WithRequestedHousehold returns a copy of ctx asking the resolver for the
// membership in householdID (a UUID string) instead of the default one.
func WithRequestedHousehold(ctx context.Context, householdID string) context.Context {
	return context.WithValue(ctx, requestedHouseholdKey{}, householdID)
}

// RequestedHousehold returns the household requested with
// WithRequestedHousehold, or "" when none was requested.
func RequestedHousehold(ctx context.Context) string {
	s, _ := ctx.Value(requestedHouseholdKey{}).(string)
	return s
}

// ContextWithHouseholdHeader reads the X-Household-ID header of r. With no
// header it returns r's context unchanged. A value that is not a UUID returns
// a non-empty errMsg for a 400 response. Otherwise the returned context makes
// VerifyToken resolve that household's membership (ErrNotHouseholdMember if
// the caller has none).
func ContextWithHouseholdHeader(r *http.Request) (ctx context.Context, errMsg string) {
	raw := strings.TrimSpace(r.Header.Get(HouseholdHeader))
	if raw == "" {
		return r.Context(), ""
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, HouseholdHeader + " must be a household UUID"
	}
	return WithRequestedHousehold(r.Context(), id.String()), ""
}

// ResolvedMember is the Home OS identity a Dex subject resolves to.
type ResolvedMember struct {
	UserID      string
	HouseholdID string
	Role        string
}

// MemberResolver maps a verified Dex identity (sub + email) to a Home OS
// user and household membership.
type MemberResolver interface {
	Resolve(ctx context.Context, sub, email string) (*ResolvedMember, error)
}

// PGMemberResolver resolves Dex identities against the users and memberships
// tables.
//
// Lookup order:
//  1. users.dex_sub = sub
//  2. lower(users.email) = lower(email); when found and users.dex_sub is NULL,
//     the sub is stored in users.dex_sub so later requests take path 1.
//
// Deactivated users (users.active = false) fail with ErrUserDeactivated.
//
// Users with several memberships resolve deterministically to their OLDEST
// membership (memberships.created_at, then memberships.id), unless the context
// carries a requested household (WithRequestedHousehold, set from the
// X-Household-ID header): then that membership is used, or the call fails with
// ErrNotHouseholdMember when the user has none there.
type PGMemberResolver struct {
	pool *pgxpool.Pool
}

// NewMemberResolver returns a PGMemberResolver backed by pool.
func NewMemberResolver(pool *pgxpool.Pool) *PGMemberResolver {
	return &PGMemberResolver{pool: pool}
}

// Resolve implements MemberResolver.
func (r *PGMemberResolver) Resolve(ctx context.Context, sub, email string) (*ResolvedMember, error) {
	var userID string
	var storedSub *string
	var active bool

	err := r.pool.QueryRow(ctx,
		`SELECT id::text, active FROM users WHERE dex_sub = $1`, sub,
	).Scan(&userID, &active)
	switch {
	case err == nil:
	case errors.Is(err, pgx.ErrNoRows):
		email = strings.TrimSpace(email)
		if email == "" {
			return nil, ErrNoMembership
		}
		err = r.pool.QueryRow(ctx,
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
		r.backfillSub(ctx, userID, sub, storedSub)
	default:
		return nil, fmt.Errorf("%w: user by dex_sub: %w", ErrMemberLookup, err)
	}
	if !active {
		return nil, ErrUserDeactivated
	}

	m := ResolvedMember{UserID: userID}
	if want := RequestedHousehold(ctx); want != "" {
		err = r.pool.QueryRow(ctx,
			`SELECT household_id::text, role::text FROM memberships
			 WHERE user_id = $1 AND household_id = $2`, userID, want,
		).Scan(&m.HouseholdID, &m.Role)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotHouseholdMember
		}
		if err != nil {
			return nil, fmt.Errorf("%w: requested membership: %w", ErrMemberLookup, err)
		}
		return &m, nil
	}
	err = r.pool.QueryRow(ctx,
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
// A user that already carries a different sub is left alone (logged), and a
// failed backfill never fails the request: the email path keeps working.
func (r *PGMemberResolver) backfillSub(ctx context.Context, userID, sub string, storedSub *string) {
	if storedSub != nil {
		if *storedSub != sub {
			slog.Warn("auth: email-matched user already has a different dex_sub; not overwriting",
				"user_id", userID)
		}
		return
	}
	if _, err := r.pool.Exec(ctx,
		`UPDATE users SET dex_sub = $1, updated_at = NOW()
		 WHERE id = $2 AND dex_sub IS NULL`, sub, userID,
	); err != nil {
		slog.Warn("auth: dex_sub backfill failed", "user_id", userID, "error", err)
		return
	}
	slog.Info("auth: backfilled users.dex_sub from email match", "user_id", userID)
}
