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
