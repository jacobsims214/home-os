package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

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
// membership (memberships.created_at, then memberships.id).
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
