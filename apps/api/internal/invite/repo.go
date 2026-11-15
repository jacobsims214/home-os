package invite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the invitations table.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new invitation repository backed by the given connection pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// Create inserts a new invitation and returns the created record.
func (r *Repo) Create(ctx context.Context, householdID, email, token, role string, invitedByID uuid.UUID, expiresAt time.Time) (*Invitation, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO invitations (household_id, email, token, role, invited_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, household_id, email, token, role, invited_by, expires_at, accepted_at, created_at`,
		householdID, email, token, role, invitedByID, expiresAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create invitation: %w", err)
	}
	defer rows.Close()

	inv, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Invitation])
	if err != nil {
		return nil, fmt.Errorf("collect invitation: %w", err)
	}
	return inv, nil
}

// ListByHousehold returns all pending invitations for a household.
func (r *Repo) ListByHousehold(ctx context.Context, householdID string) ([]Invitation, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, email, token, role, invited_by, expires_at, accepted_at, created_at
		 FROM invitations
		 WHERE household_id = $1
		 ORDER BY created_at`,
		householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	defer rows.Close()

	var invs []Invitation
	for rows.Next() {
		var inv Invitation
		if err := rows.Scan(&inv.ID, &inv.HouseholdID, &inv.Email, &inv.Token, &inv.Role, &inv.InvitedBy, &inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan invitation: %w", err)
		}
		invs = append(invs, inv)
	}
	return invs, nil
}

// GetByToken returns an invitation by its token.
func (r *Repo) GetByToken(ctx context.Context, token string) (*Invitation, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, household_id, email, token, role, invited_by, expires_at, accepted_at, created_at
		 FROM invitations
		 WHERE token = $1`,
		token,
	)

	inv := &Invitation{}
	err := row.Scan(&inv.ID, &inv.HouseholdID, &inv.Email, &inv.Token, &inv.Role, &inv.InvitedBy, &inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get invitation by token: %w", err)
	}
	return inv, nil
}

// Errors returned by Accept. The handler maps them to HTTP statuses.
var (
	ErrInviteNotFound      = errors.New("invitation not found")
	ErrInviteAccepted      = errors.New("invitation already accepted")
	ErrInviteExpired       = errors.New("invitation has expired")
	ErrInviteEmailMismatch = errors.New("invitation was sent to a different email address")
	ErrAlreadyMember       = errors.New("user is already a member of this household")
)

// dexOnlyPasswordHash is stored in users.password_hash (NOT NULL) for users
// created from a Dex identity on invite acceptance. It is not a bcrypt hash,
// so it can never match a password: these users sign in through Dex.
const dexOnlyPasswordHash = "!dex-oidc"

// AcceptIdentity is the verified Dex identity accepting an invitation.
type AcceptIdentity struct {
	Subject string
	Email   string
	Name    string
}

// AcceptResult describes the membership created by Accept.
type AcceptResult struct {
	Invitation  *Invitation
	UserID      string
	HouseholdID string
	Role        string
	UserCreated bool
}

// Accept accepts the invitation with the given token for ident, in a single
// transaction:
//
//  1. locks the invitation row (SELECT ... FOR UPDATE) so concurrent accepts of
//     the same token serialise; a used token returns ErrInviteAccepted and an
//     expired one ErrInviteExpired;
//  2. requires ident.Email to match the invitation email (case-insensitive);
//  3. finds the Home OS user by users.dex_sub, then by lower(email); when none
//     exists it creates one carrying dex_sub (a Dex-only user);
//  4. inserts the membership with the invitation's role (ErrAlreadyMember if
//     the user already belongs to the household);
//  5. sets invitations.accepted_at.
//
// Nothing is written unless every step succeeds.
func (r *Repo) Accept(ctx context.Context, token string, ident AcceptIdentity, now time.Time) (*AcceptResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("accept invitation: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	inv := &Invitation{}
	err = tx.QueryRow(ctx,
		`SELECT id, household_id, email, token, role, invited_by, expires_at, accepted_at, created_at
		 FROM invitations
		 WHERE token = $1
		 FOR UPDATE`,
		token,
	).Scan(&inv.ID, &inv.HouseholdID, &inv.Email, &inv.Token, &inv.Role, &inv.InvitedBy, &inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInviteNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("accept invitation: lock invitation: %w", err)
	}
	if inv.AcceptedAt != nil {
		return nil, ErrInviteAccepted
	}
	if now.After(inv.ExpiresAt) {
		return nil, ErrInviteExpired
	}
	email := strings.TrimSpace(ident.Email)
	if email == "" || !strings.EqualFold(email, strings.TrimSpace(inv.Email)) {
		return nil, ErrInviteEmailMismatch
	}

	userID, created, err := findOrCreateUser(ctx, tx, ident.Subject, email, ident.Name)
	if err != nil {
		return nil, err
	}

	var role string
	err = tx.QueryRow(ctx,
		`INSERT INTO memberships (household_id, user_id, role)
		 VALUES ($1, $2, $3::membership_role)
		 ON CONFLICT (household_id, user_id) DO NOTHING
		 RETURNING role::text`,
		inv.HouseholdID, userID, inv.Role,
	).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAlreadyMember
	}
	if err != nil {
		return nil, fmt.Errorf("accept invitation: create membership: %w", err)
	}

	acceptedAt := now
	if _, err := tx.Exec(ctx,
		`UPDATE invitations SET accepted_at = $1 WHERE id = $2`,
		acceptedAt, inv.ID,
	); err != nil {
		return nil, fmt.Errorf("accept invitation: mark accepted: %w", err)
	}
	inv.AcceptedAt = &acceptedAt

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("accept invitation: commit: %w", err)
	}
	return &AcceptResult{
		Invitation:  inv,
		UserID:      userID,
		HouseholdID: inv.HouseholdID,
		Role:        role,
		UserCreated: created,
	}, nil
}

// findOrCreateUser resolves the accepting identity to users.id with the same
// order as auth.PGMemberResolver (dex_sub, then case-insensitive email, with a
// dex_sub backfill when it is NULL), creating a Dex-only user when neither
// matches.
func findOrCreateUser(ctx context.Context, tx pgx.Tx, sub, email, name string) (string, bool, error) {
	var userID string
	if sub != "" {
		err := tx.QueryRow(ctx, `SELECT id::text FROM users WHERE dex_sub = $1`, sub).Scan(&userID)
		if err == nil {
			return userID, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", false, fmt.Errorf("accept invitation: user by dex_sub: %w", err)
		}
	}

	err := tx.QueryRow(ctx,
		`SELECT id::text FROM users WHERE lower(email) = lower($1)`, email,
	).Scan(&userID)
	if err == nil {
		if sub != "" {
			if _, err := tx.Exec(ctx,
				`UPDATE users SET dex_sub = $1, updated_at = NOW()
				 WHERE id = $2 AND dex_sub IS NULL`, sub, userID,
			); err != nil {
				return "", false, fmt.Errorf("accept invitation: backfill dex_sub: %w", err)
			}
		}
		return userID, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("accept invitation: user by email: %w", err)
	}

	if strings.TrimSpace(name) == "" {
		name = email
		if at := strings.IndexByte(email, '@'); at > 0 {
			name = email[:at]
		}
	}
	var dexSub *string
	if sub != "" {
		dexSub = &sub
	}
	err = tx.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id::text`,
		email, name, dexOnlyPasswordHash, dexSub,
	).Scan(&userID)
	if err != nil {
		return "", false, fmt.Errorf("accept invitation: create user: %w", err)
	}
	return userID, true, nil
}

// GetHouseholdName returns the name of a household by its ID.
func (r *Repo) GetHouseholdName(ctx context.Context, householdID string) (string, error) {
	var name string
	err := r.pool.QueryRow(ctx, `SELECT name FROM households WHERE id = $1`, householdID).Scan(&name)
	if err != nil {
		return "", fmt.Errorf("get household name: %w", err)
	}
	return name, nil
}
