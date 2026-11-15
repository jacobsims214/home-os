package household

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/auth"
	"home-os/api/internal/invite"
	"home-os/api/internal/seed"
)

// Repo provides read/write access to the households and memberships tables
// using a pgx connection pool.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new household repository backed by the given connection pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// CreateHousehold inserts a new household with the given name and returns the created record.
// Default timezone is UTC and default settings is an empty JSON object.
func (r *Repo) CreateHousehold(ctx context.Context, name string) (*Household, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO households (name) VALUES ($1)
		 RETURNING id, name, timezone, settings, created_at, updated_at`,
		name,
	)
	if err != nil {
		return nil, fmt.Errorf("create household: %w", err)
	}
	defer rows.Close()

	hh, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Household])
	if err != nil {
		return nil, fmt.Errorf("collect household: %w", err)
	}
	return hh, nil
}

// CreateMembership inserts a new membership row linking a user to a household with a given role.
func (r *Repo) CreateMembership(ctx context.Context, householdID, userID uuid.UUID, role string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
		householdID, userID, role,
	)
	if err != nil {
		return fmt.Errorf("create membership: %w", err)
	}
	return nil
}

// GetHousehold returns a household by ID, scoped to the household.
func (r *Repo) GetHousehold(ctx context.Context, id uuid.UUID) (*Household, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, name, timezone, settings, created_at, updated_at
		 FROM households WHERE id = $1`, id)
	hh := &Household{}
	err := row.Scan(&hh.ID, &hh.Name, &hh.Timezone, &hh.Settings, &hh.CreatedAt, &hh.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get household: %w", err)
	}
	return hh, nil
}

// UserHousehold is one household the caller belongs to, with their role.
type UserHousehold struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Timezone string    `json:"timezone"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
}

// ListForUser returns every household userID is a member of, oldest
// membership first (memberships.created_at, then memberships.id). The first
// entry is the one the API uses when a request carries no X-Household-ID.
func (r *Repo) ListForUser(ctx context.Context, userID uuid.UUID) ([]UserHousehold, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT h.id::text, h.name, h.timezone, m.role::text, m.created_at
		 FROM memberships m
		 JOIN households h ON h.id = m.household_id
		 WHERE m.user_id = $1
		 ORDER BY m.created_at ASC, m.id ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list households for user: %w", err)
	}
	defer rows.Close()

	out := []UserHousehold{}
	for rows.Next() {
		var h UserHousehold
		if err := rows.Scan(&h.ID, &h.Name, &h.Timezone, &h.Role, &h.JoinedAt); err != nil {
			return nil, fmt.Errorf("scan user household: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list households for user: %w", err)
	}
	return out, nil
}

// Member represents a household member with user info.
type Member struct {
	UserID   string `json:"user_id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	JoinedAt string `json:"joined_at"`
}

// ListMembers returns all members of a household with their user info.
func (r *Repo) ListMembers(ctx context.Context, householdID uuid.UUID) ([]Member, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT m.user_id, u.email, u.name, m.role, m.created_at::text
		 FROM memberships m
		 JOIN users u ON u.id = m.user_id
		 WHERE m.household_id = $1
		 ORDER BY m.created_at`,
		householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	var members []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Email, &m.Name, &m.Role, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		members = append(members, m)
	}
	return members, nil
}

// Errors returned by ChangeMemberRole and RemoveMember.
var (
	// ErrNotManager: the actor is not an owner or family_manager of the
	// household, or is a family_manager acting on an owner or granting owner.
	ErrNotManager = errors.New("not allowed to manage this member")
	// ErrMemberNotFound: the target user is not a member of the household.
	ErrMemberNotFound = errors.New("member not found")
	// ErrLastOwner: the change would leave the household with no owner.
	ErrLastOwner = errors.New("household must keep at least one owner")
	// ErrInvalidRole: the role is not a membership_role value.
	ErrInvalidRole = errors.New("invalid role")
)

// lockMemberRoles locks the household's membership rows and returns the
// actor's and target's roles ("" for a non-member) and the owner count. The
// lock serialises concurrent role changes so the last-owner check holds.
func lockMemberRoles(ctx context.Context, tx pgx.Tx, householdID, actorID, targetID uuid.UUID) (actorRole, targetRole string, owners int, err error) {
	rows, err := tx.Query(ctx,
		`SELECT user_id, role::text FROM memberships WHERE household_id = $1 FOR UPDATE`, householdID)
	if err != nil {
		return "", "", 0, fmt.Errorf("lock memberships: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var uid uuid.UUID
		var role string
		if err := rows.Scan(&uid, &role); err != nil {
			return "", "", 0, fmt.Errorf("scan membership: %w", err)
		}
		if uid == actorID {
			actorRole = role
		}
		if uid == targetID {
			targetRole = role
		}
		if role == auth.RoleOwner {
			owners++
		}
	}
	return actorRole, targetRole, owners, rows.Err()
}

// ChangeMemberRole sets targetID's role in householdID on behalf of actorID.
// Authorization is decided inside the transaction from memberships (never
// from request claims): see auth.CanModifyMember and auth.CanGrantRole. The
// last owner cannot be demoted.
func (r *Repo) ChangeMemberRole(ctx context.Context, householdID, actorID, targetID uuid.UUID, role string) error {
	if !auth.ValidRole(role) {
		return ErrInvalidRole
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	actorRole, targetRole, owners, err := lockMemberRoles(ctx, tx, householdID, actorID, targetID)
	if err != nil {
		return err
	}
	if !auth.CanManageMembers(actorRole) {
		return ErrNotManager
	}
	if targetRole == "" {
		return ErrMemberNotFound
	}
	if !auth.CanModifyMember(actorRole, targetRole) || !auth.CanGrantRole(actorRole, role) {
		return ErrNotManager
	}
	if targetRole == auth.RoleOwner && role != auth.RoleOwner && owners <= 1 {
		return ErrLastOwner
	}
	if _, err := tx.Exec(ctx,
		`UPDATE memberships SET role = $3::membership_role WHERE household_id = $1 AND user_id = $2`,
		householdID, targetID, role,
	); err != nil {
		return fmt.Errorf("update member role: %w", err)
	}
	return tx.Commit(ctx)
}

// RemoveMember removes targetID from householdID on behalf of actorID, with
// the same rules as ChangeMemberRole. The last owner cannot be removed.
func (r *Repo) RemoveMember(ctx context.Context, householdID, actorID, targetID uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	actorRole, targetRole, owners, err := lockMemberRoles(ctx, tx, householdID, actorID, targetID)
	if err != nil {
		return err
	}
	if !auth.CanManageMembers(actorRole) {
		return ErrNotManager
	}
	if targetRole == "" {
		return ErrMemberNotFound
	}
	if !auth.CanModifyMember(actorRole, targetRole) {
		return ErrNotManager
	}
	if targetRole == auth.RoleOwner && owners <= 1 {
		return ErrLastOwner
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM memberships WHERE household_id = $1 AND user_id = $2`,
		householdID, targetID,
	); err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	return tx.Commit(ctx)
}

// Errors returned by CreateFamily.
var (
	// ErrAlreadyMember: the caller already belongs to a household and is not
	// an instance admin, so onboarding is not for them.
	ErrAlreadyMember = errors.New("user already belongs to a household")
	// ErrUserInactive: the caller's user row is deactivated (users.active=false).
	ErrUserInactive = errors.New("user is deactivated")
	// ErrNoIdentity: neither a Dex subject nor an email was supplied.
	ErrNoIdentity = errors.New("identity has no subject or email")
)

// CreatorIdentity is the verified Dex identity creating a family.
type CreatorIdentity struct {
	Subject string
	Email   string
	Name    string
}

// CreateFamilyResult describes what CreateFamily wrote.
type CreateFamilyResult struct {
	Household   *Household
	UserID      uuid.UUID
	Role        string
	UserCreated bool
}

// CreateFamily creates a household with the caller as its owner, in a single
// transaction:
//
//  1. finds the Home OS user by users.dex_sub, then lower(email) (backfilling
//     a NULL dex_sub), or creates a Dex-only user when neither matches
//     (invite.FindOrCreateUser);
//  2. locks that user row (FOR UPDATE) so two concurrent onboarding requests
//     from the same user cannot both create a family;
//  3. refuses deactivated users (ErrUserInactive) and users who already have
//     a membership unless they are an instance admin (ErrAlreadyMember);
//  4. inserts the household, the owner membership, and the default
//     Family/Bills/Maintenance calendars (seed.EnsureDefaultCalendars) and
//     the starter list templates (seed.EnsureStarterLists).
//
// Nothing is written unless every step succeeds. name and timezone must
// already be validated by the caller.
func (r *Repo) CreateFamily(ctx context.Context, ident CreatorIdentity, name, timezone string) (*CreateFamilyResult, error) {
	sub := strings.TrimSpace(ident.Subject)
	email := strings.TrimSpace(ident.Email)
	if sub == "" && email == "" {
		return nil, ErrNoIdentity
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("create family: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	userID, created, err := invite.FindOrCreateUser(ctx, tx, sub, email, ident.Name)
	if errors.Is(err, invite.ErrNoEmail) {
		return nil, ErrNoIdentity
	}
	if err != nil {
		return nil, fmt.Errorf("create family: %w", err)
	}

	var active, instanceAdmin, hasMembership bool
	if err := tx.QueryRow(ctx,
		`SELECT u.active, u.is_instance_admin,
		        EXISTS (SELECT 1 FROM memberships m WHERE m.user_id = u.id)
		 FROM users u WHERE u.id = $1
		 FOR UPDATE OF u`, userID,
	).Scan(&active, &instanceAdmin, &hasMembership); err != nil {
		return nil, fmt.Errorf("create family: lock user: %w", err)
	}
	if !active {
		return nil, ErrUserInactive
	}
	if hasMembership && !instanceAdmin {
		return nil, ErrAlreadyMember
	}

	hh := &Household{}
	if err := tx.QueryRow(ctx,
		`INSERT INTO households (name, timezone) VALUES ($1, $2)
		 RETURNING id, name, timezone, settings, created_at, updated_at`,
		name, timezone,
	).Scan(&hh.ID, &hh.Name, &hh.Timezone, &hh.Settings, &hh.CreatedAt, &hh.UpdatedAt); err != nil {
		return nil, fmt.Errorf("create family: insert household: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'owner')`,
		hh.ID, userID,
	); err != nil {
		return nil, fmt.Errorf("create family: insert owner membership: %w", err)
	}

	if err := seed.EnsureDefaultCalendars(ctx, tx, hh.ID.String()); err != nil {
		return nil, fmt.Errorf("create family: %w", err)
	}
	if err := seed.EnsureStarterLists(ctx, tx, hh.ID.String()); err != nil {
		return nil, fmt.Errorf("create family: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("create family: commit: %w", err)
	}
	return &CreateFamilyResult{Household: hh, UserID: userID, Role: "owner", UserCreated: created}, nil
}
