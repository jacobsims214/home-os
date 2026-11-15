package invite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
		 RETURNING id, household_id, email, token, role, COALESCE(invited_by::text, ''), expires_at, accepted_at, created_at`,
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
		`SELECT id, household_id, email, token, role, COALESCE(invited_by::text, ''), expires_at, accepted_at, created_at
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
		`SELECT id, household_id, email, token, role, COALESCE(invited_by::text, ''), expires_at, accepted_at, created_at
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

// Errors returned by Accept and Signup. The handler maps them to HTTP statuses.
var (
	ErrInviteNotFound      = errors.New("invitation not found")
	ErrInviteAccepted      = errors.New("invitation already accepted")
	ErrInviteExpired       = errors.New("invitation has expired")
	ErrInviteEmailMismatch = errors.New("invitation was sent to a different email address")
	ErrAlreadyMember       = errors.New("user is already a member of this household")
	ErrUserInactive        = errors.New("user account is deactivated")
	ErrAccountExists       = errors.New("an account with this email already exists")
)

// ErrNoEmail is returned by FindOrCreateUser when no user matches and the
// identity has no email: users.email is NOT NULL UNIQUE, so no row can be
// created for it.
var ErrNoEmail = errors.New("identity has no email address")

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

// AcceptResult describes the membership created by Accept or Signup.
type AcceptResult struct {
	Invitation  *Invitation
	UserID      string
	Email       string
	HouseholdID string
	Role        string
	UserCreated bool
}

// lockPendingInvitation loads the invitation for token with SELECT ... FOR
// UPDATE, so concurrent accepts of the same token serialise, and refuses
// unknown (ErrInviteNotFound), used (ErrInviteAccepted) and expired
// (ErrInviteExpired) invitations.
func lockPendingInvitation(ctx context.Context, tx pgx.Tx, token string, now time.Time) (*Invitation, error) {
	inv := &Invitation{}
	err := tx.QueryRow(ctx,
		`SELECT id, household_id, email, token, role, COALESCE(invited_by::text, ''), expires_at, accepted_at, created_at
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
	return inv, nil
}

// joinHousehold inserts the membership with the invitation's role
// (ErrAlreadyMember if the user already belongs to the household) and marks
// the invitation accepted.
func joinHousehold(ctx context.Context, tx pgx.Tx, inv *Invitation, userID uuid.UUID, now time.Time) (string, error) {
	var role string
	err := tx.QueryRow(ctx,
		`INSERT INTO memberships (household_id, user_id, role)
		 VALUES ($1, $2, $3::membership_role)
		 ON CONFLICT (household_id, user_id) DO NOTHING
		 RETURNING role::text`,
		inv.HouseholdID, userID, inv.Role,
	).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrAlreadyMember
	}
	if err != nil {
		return "", fmt.Errorf("accept invitation: create membership: %w", err)
	}

	acceptedAt := now
	if _, err := tx.Exec(ctx,
		`UPDATE invitations SET accepted_at = $1 WHERE id = $2`,
		acceptedAt, inv.ID,
	); err != nil {
		return "", fmt.Errorf("accept invitation: mark accepted: %w", err)
	}
	inv.AcceptedAt = &acceptedAt
	return role, nil
}

// Accept accepts the invitation with the given token for ident, in a single
// transaction:
//
//  1. locks the invitation row (lockPendingInvitation);
//  2. requires ident.Email to match the invitation email (case-insensitive);
//  3. finds or creates the Home OS user (FindOrCreateUser) and refuses a
//     deactivated one (ErrUserInactive);
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

	inv, err := lockPendingInvitation(ctx, tx, token, now)
	if err != nil {
		return nil, err
	}
	email := strings.TrimSpace(ident.Email)
	if email == "" || !strings.EqualFold(email, strings.TrimSpace(inv.Email)) {
		return nil, ErrInviteEmailMismatch
	}

	userID, created, err := FindOrCreateUser(ctx, tx, strings.TrimSpace(ident.Subject), email, ident.Name)
	if err != nil {
		return nil, fmt.Errorf("accept invitation: %w", err)
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT active FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&active); err != nil {
		return nil, fmt.Errorf("accept invitation: load user: %w", err)
	}
	if !active {
		return nil, ErrUserInactive
	}

	role, err := joinHousehold(ctx, tx, inv, userID, now)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("accept invitation: commit: %w", err)
	}
	return &AcceptResult{
		Invitation:  inv,
		UserID:      userID.String(),
		Email:       email,
		HouseholdID: inv.HouseholdID,
		Role:        role,
		UserCreated: created,
	}, nil
}

// SignupPasswordFunc registers the new user's password with Dex. Signup calls
// it inside the transaction, after the user row exists and before commit, so
// a Dex failure leaves nothing behind in Postgres and the invitation unused.
type SignupPasswordFunc func(ctx context.Context, email, userID string) error

// Signup accepts the invitation with the given token for a person who has no
// Home OS account yet, in a single transaction:
//
//  1. locks the invitation row (lockPendingInvitation);
//  2. refuses when a user with the invitation's email already exists: a
//     deactivated one with ErrUserInactive, an active one with
//     ErrAccountExists (they sign in through Dex and use Accept instead);
//  3. creates the user with the invitation's email, so the strict email match
//     holds by construction, storing passwordHash (bcrypt);
//  4. inserts the membership and marks the invitation accepted;
//  5. calls setPassword to create the Dex password entry, then commits.
//
// dex_sub stays NULL: the member resolver backfills it, matching by email, on
// the user's first Dex sign-in.
func (r *Repo) Signup(ctx context.Context, token, name, passwordHash string, now time.Time, setPassword SignupPasswordFunc) (*AcceptResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("invite signup: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	inv, err := lockPendingInvitation(ctx, tx, token, now)
	if err != nil {
		return nil, err
	}
	email := strings.TrimSpace(inv.Email)

	var active bool
	err = tx.QueryRow(ctx, `SELECT active FROM users WHERE lower(email) = lower($1)`, email).Scan(&active)
	switch {
	case err == nil && !active:
		return nil, ErrUserInactive
	case err == nil:
		return nil, ErrAccountExists
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("invite signup: user by email: %w", err)
	}

	var userID uuid.UUID
	if err := tx.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash)
		 VALUES ($1, $2, $3)
		 RETURNING id`,
		email, name, passwordHash,
	).Scan(&userID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // concurrent signup won the email
			return nil, ErrAccountExists
		}
		return nil, fmt.Errorf("invite signup: create user: %w", err)
	}

	role, err := joinHousehold(ctx, tx, inv, userID, now)
	if err != nil {
		return nil, err
	}

	if err := setPassword(ctx, email, userID.String()); err != nil {
		return nil, fmt.Errorf("invite signup: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("invite signup: commit: %w", err)
	}
	return &AcceptResult{
		Invitation:  inv,
		UserID:      userID.String(),
		Email:       email,
		HouseholdID: inv.HouseholdID,
		Role:        role,
		UserCreated: true,
	}, nil
}

// FindOrCreateUser resolves a verified Dex identity to users.id inside tx, in
// the same order as auth.PGMemberResolver: users.dex_sub, then
// case-insensitive email (backfilling a NULL dex_sub). When neither matches it
// creates a Dex-only user (password_hash can never match a password) carrying
// dex_sub; with no email it returns ErrNoEmail. created reports whether a row
// was inserted. It does not look at users.active: callers decide whether a
// deactivated user is refused.
//
// Shared by invite acceptance and household onboarding (household.CreateFamily).
func FindOrCreateUser(ctx context.Context, tx pgx.Tx, sub, email, name string) (userID uuid.UUID, created bool, err error) {
	if sub != "" {
		err := tx.QueryRow(ctx, `SELECT id FROM users WHERE dex_sub = $1`, sub).Scan(&userID)
		if err == nil {
			return userID, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, false, fmt.Errorf("find user by dex_sub: %w", err)
		}
	}

	if email != "" {
		err := tx.QueryRow(ctx,
			`SELECT id FROM users WHERE lower(email) = lower($1)`, email,
		).Scan(&userID)
		if err == nil {
			if sub != "" {
				if _, err := tx.Exec(ctx,
					`UPDATE users SET dex_sub = $1, updated_at = NOW()
					 WHERE id = $2 AND dex_sub IS NULL`, sub, userID,
				); err != nil {
					return uuid.Nil, false, fmt.Errorf("backfill dex_sub: %w", err)
				}
			}
			return userID, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, false, fmt.Errorf("find user by email: %w", err)
		}
	}

	if email == "" {
		return uuid.Nil, false, ErrNoEmail
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = email
		if at := strings.IndexByte(email, '@'); at > 0 {
			name = email[:at]
		}
	}
	var dexSub *string
	if sub != "" {
		dexSub = &sub
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id`,
		email, name, dexOnlyPasswordHash, dexSub,
	).Scan(&userID); err != nil {
		return uuid.Nil, false, fmt.Errorf("create user: %w", err)
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

// MemberRole returns userID's current role in householdID, read from
// memberships, or "" if the user is not a member there. The invite handlers
// authorize against this rather than trusting the role in the claims alone.
func (r *Repo) MemberRole(ctx context.Context, householdID string, userID uuid.UUID) (string, error) {
	var role string
	err := r.pool.QueryRow(ctx,
		`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
		householdID, userID,
	).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("member role: %w", err)
	}
	return role, nil
}
