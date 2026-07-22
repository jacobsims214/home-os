package household

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// Member represents a household member with user info.
type Member struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	JoinedAt  string `json:"joined_at"`
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

// UpdateMemberRole updates a member's role in a household.
func (r *Repo) UpdateMemberRole(ctx context.Context, householdID, userID uuid.UUID, role string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE memberships SET role = $3 WHERE household_id = $1 AND user_id = $2`,
		householdID, userID, role,
	)
	if err != nil {
		return fmt.Errorf("update member role: %w", err)
	}
	return nil
}

// RemoveMember removes a member from a household.
func (r *Repo) RemoveMember(ctx context.Context, householdID, userID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM memberships WHERE household_id = $1 AND user_id = $2`,
		householdID, userID,
	)
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	return nil
}
