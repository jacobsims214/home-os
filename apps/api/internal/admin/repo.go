package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/seed"
)

// Errors returned by Repo.
var (
	// ErrFamilyNotFound: the household_id given for a new user does not exist.
	ErrFamilyNotFound = errors.New("family not found")
	// ErrEmailTaken: a user with that email (case-insensitive) already exists.
	ErrEmailTaken = errors.New("email already registered")
	// ErrUserNotFound: no user with that id.
	ErrUserNotFound = errors.New("user not found")
	// ErrSelfDeactivation: an admin tried to deactivate their own account.
	ErrSelfDeactivation = errors.New("you cannot deactivate your own account")
	// ErrLastAdmin: deactivating the user would leave no active instance admin.
	ErrLastAdmin = errors.New("cannot deactivate the last active instance admin")
	// ErrCallerNotAdmin: the caller stopped being an active instance admin
	// between the middleware check and the locked re-check in the transaction.
	ErrCallerNotAdmin = errors.New("caller is no longer an active instance admin")
)

// Repo reads and writes users, households and memberships for the instance
// admin endpoints. Every method works across households: callers must have
// passed RequireInstanceAdmin.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo returns a Repo backed by pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// Caller is the Home OS user behind an admin request.
type Caller struct {
	ID              uuid.UUID
	Active          bool
	IsInstanceAdmin bool
}

// LookupCaller finds the user for a verified Dex identity in the same order
// as auth.PGMemberResolver (users.dex_sub, then lower(email)). It never
// creates or modifies a row. A nil Caller means no such user.
func (r *Repo) LookupCaller(ctx context.Context, sub, email string) (*Caller, error) {
	var c Caller
	if sub != "" {
		err := r.pool.QueryRow(ctx,
			`SELECT id, active, is_instance_admin FROM users WHERE dex_sub = $1`, sub,
		).Scan(&c.ID, &c.Active, &c.IsInstanceAdmin)
		if err == nil {
			return &c, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("lookup caller by dex_sub: %w", err)
		}
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, nil
	}
	err := r.pool.QueryRow(ctx,
		`SELECT id, active, is_instance_admin FROM users WHERE lower(email) = lower($1)`, email,
	).Scan(&c.ID, &c.Active, &c.IsInstanceAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup caller by email: %w", err)
	}
	return &c, nil
}

// Family is a household with its member count.
type Family struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Timezone    string    `json:"timezone"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// ListFamilies returns every household, oldest first, with member counts.
func (r *Repo) ListFamilies(ctx context.Context) ([]Family, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT h.id, h.name, h.timezone, count(m.id)::int, h.created_at
		 FROM households h
		 LEFT JOIN memberships m ON m.household_id = h.id
		 GROUP BY h.id
		 ORDER BY h.created_at ASC, h.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list families: %w", err)
	}
	defer rows.Close()
	out := []Family{}
	for rows.Next() {
		var f Family
		if err := rows.Scan(&f.ID, &f.Name, &f.Timezone, &f.MemberCount, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan family: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list families: %w", err)
	}
	return out, nil
}

// UserMembership is one household a user belongs to.
type UserMembership struct {
	HouseholdID   uuid.UUID `json:"household_id"`
	HouseholdName string    `json:"household_name"`
	Role          string    `json:"role"`
}

// User is the admin view of a user. It deliberately has no password,
// CalDAV hash or Dex subject: those never leave the server.
type User struct {
	ID              uuid.UUID        `json:"id"`
	Email           string           `json:"email"`
	Name            string           `json:"name"`
	Active          bool             `json:"active"`
	IsInstanceAdmin bool             `json:"is_instance_admin"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
	Memberships     []UserMembership `json:"memberships"`
}

// userSelect selects the admin User columns plus its memberships as JSON.
const userSelect = `
	SELECT u.id, u.email, u.name, u.active, u.is_instance_admin, u.created_at, u.updated_at,
	       COALESCE(
	         json_agg(json_build_object(
	           'household_id', h.id, 'household_name', h.name, 'role', m.role
	         ) ORDER BY m.created_at, m.id) FILTER (WHERE m.id IS NOT NULL),
	         '[]'::json)
	FROM users u
	LEFT JOIN memberships m ON m.user_id = u.id
	LEFT JOIN households h ON h.id = m.household_id`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	var memberships []byte
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Active, &u.IsInstanceAdmin,
		&u.CreatedAt, &u.UpdatedAt, &memberships); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(memberships, &u.Memberships); err != nil {
		return nil, fmt.Errorf("decode memberships: %w", err)
	}
	return &u, nil
}

// ListUsers returns every user, ordered by email, with their memberships.
func (r *Repo) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := r.pool.Query(ctx, userSelect+` GROUP BY u.id ORDER BY lower(u.email), u.id`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return out, nil
}

// GetUser returns one user, or ErrUserNotFound.
func (r *Repo) GetUser(ctx context.Context, id uuid.UUID) (*User, error) {
	u, err := scanUser(r.pool.QueryRow(ctx, userSelect+` WHERE u.id = $1 GROUP BY u.id`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

// CreateUserInput is a validated request to create a user.
type CreateUserInput struct {
	Email        string
	Name         string
	PasswordHash string // bcrypt
	Role         string // a membership_role value
	// Exactly one of HouseholdID (join an existing family) or
	// NewHouseholdName (create a family) is set.
	HouseholdID      *uuid.UUID
	NewHouseholdName string
	NewTimezone      string
}

// CreateUserResult describes what CreateUser wrote.
type CreateUserResult struct {
	UserID           uuid.UUID
	Email            string
	Name             string
	HouseholdID      uuid.UUID
	HouseholdName    string
	Role             string
	HouseholdCreated bool
}

// SetPasswordFunc registers the new user's password with Dex. It runs inside
// CreateUser's transaction, before commit: if it fails nothing is written.
type SetPasswordFunc func(ctx context.Context, email, userID string) error

// CreateUser creates a user with a password and puts them into a family, in
// one transaction:
//
//  1. resolves the family: locks the existing household (FOR SHARE, so it
//     cannot be deleted under us) or inserts a new one with its default
//     Family/Bills/Maintenance calendars (seed.EnsureDefaultCalendars);
//  2. refuses an email that already exists (ErrEmailTaken); it never takes
//     over an account;
//  3. inserts the user (dex_sub NULL: the member resolver backfills it by
//     email on first Dex sign-in) and the membership;
//  4. calls setPassword (Dex CreatePassword), then commits.
func (r *Repo) CreateUser(ctx context.Context, in CreateUserInput, setPassword SetPasswordFunc) (*CreateUserResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("admin create user: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	res := &CreateUserResult{Email: in.Email, Name: in.Name, Role: in.Role}
	if in.HouseholdID != nil {
		err := tx.QueryRow(ctx,
			`SELECT id, name FROM households WHERE id = $1 FOR SHARE`, *in.HouseholdID,
		).Scan(&res.HouseholdID, &res.HouseholdName)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrFamilyNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("admin create user: lock household: %w", err)
		}
	} else {
		if err := tx.QueryRow(ctx,
			`INSERT INTO households (name, timezone) VALUES ($1, $2) RETURNING id, name`,
			in.NewHouseholdName, in.NewTimezone,
		).Scan(&res.HouseholdID, &res.HouseholdName); err != nil {
			return nil, fmt.Errorf("admin create user: insert household: %w", err)
		}
		if err := seed.EnsureDefaultCalendars(ctx, tx, res.HouseholdID.String()); err != nil {
			return nil, fmt.Errorf("admin create user: %w", err)
		}
		res.HouseholdCreated = true
	}

	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE lower(email) = lower($1))`, in.Email,
	).Scan(&exists); err != nil {
		return nil, fmt.Errorf("admin create user: check email: %w", err)
	}
	if exists {
		return nil, ErrEmailTaken
	}

	if err := tx.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		in.Email, in.Name, in.PasswordHash,
	).Scan(&res.UserID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // a concurrent create won the email
			return nil, ErrEmailTaken
		}
		return nil, fmt.Errorf("admin create user: insert user: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3::membership_role)`,
		res.HouseholdID, res.UserID, in.Role,
	); err != nil {
		return nil, fmt.Errorf("admin create user: insert membership: %w", err)
	}

	if err := setPassword(ctx, in.Email, res.UserID.String()); err != nil {
		return nil, fmt.Errorf("admin create user: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("admin create user: commit: %w", err)
	}
	return res, nil
}

// SetActive deactivates (active=false) or reactivates (active=true) target on
// behalf of caller, in one transaction that locks the caller, the target and
// every active instance admin (in id order, so concurrent calls cannot
// deadlock). Under those locks it re-checks that:
//   - the caller is still an active instance admin (ErrCallerNotAdmin);
//   - the target exists (ErrUserNotFound);
//   - when deactivating, the target is not the caller (ErrSelfDeactivation)
//     and at least one other active instance admin remains (ErrLastAdmin).
//
// Two admins deactivating each other at the same moment therefore cannot
// leave the instance with no admin: the second transaction sees the first's
// write and fails ErrCallerNotAdmin.
//
// The change is effective immediately: the API member resolver, the CalDAV
// basic-auth check and the admin middleware all read users.active per request.
// Setting the current value again is a no-op that still returns the user.
func (r *Repo) SetActive(ctx context.Context, callerID, targetID uuid.UUID, active bool) (*User, error) {
	if !active && callerID == targetID {
		return nil, ErrSelfDeactivation
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("admin set active: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	rows, err := tx.Query(ctx,
		`SELECT id, active, is_instance_admin FROM users
		 WHERE id = $1 OR id = $2 OR (is_instance_admin AND active)
		 ORDER BY id
		 FOR UPDATE`, callerID, targetID)
	if err != nil {
		return nil, fmt.Errorf("admin set active: lock users: %w", err)
	}
	var caller, target *Caller
	activeAdmins := 0
	for rows.Next() {
		var c Caller
		if err := rows.Scan(&c.ID, &c.Active, &c.IsInstanceAdmin); err != nil {
			rows.Close()
			return nil, fmt.Errorf("admin set active: scan: %w", err)
		}
		if c.Active && c.IsInstanceAdmin {
			activeAdmins++
		}
		switch c.ID {
		case callerID:
			cc := c
			caller = &cc
		case targetID:
			tc := c
			target = &tc
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("admin set active: lock users: %w", err)
	}

	if caller == nil || !caller.Active || !caller.IsInstanceAdmin {
		return nil, ErrCallerNotAdmin
	}
	if target == nil {
		return nil, ErrUserNotFound
	}
	if !active && target.Active && target.IsInstanceAdmin && activeAdmins <= 1 {
		return nil, ErrLastAdmin
	}

	if target.Active != active {
		if _, err := tx.Exec(ctx,
			`UPDATE users SET active = $1, updated_at = NOW() WHERE id = $2`, active, targetID,
		); err != nil {
			return nil, fmt.Errorf("admin set active: update: %w", err)
		}
	}

	u, err := scanUser(tx.QueryRow(ctx, userSelect+` WHERE u.id = $1 GROUP BY u.id`, targetID))
	if err != nil {
		return nil, fmt.Errorf("admin set active: reload user: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("admin set active: commit: %w", err)
	}
	return u, nil
}
