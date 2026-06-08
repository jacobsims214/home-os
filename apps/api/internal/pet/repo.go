package pet

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the pets table using a pgx connection pool.
// All queries are scoped to household_id to enforce tenant isolation.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new pet repository.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// List returns all pets for the given household.
func (r *Repo) List(ctx context.Context, householdID uuid.UUID) ([]*Pet, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, name, species, breed, date_of_birth, vet_name, vet_phone, notes, created_at, updated_at
		 FROM pets WHERE household_id = $1 ORDER BY created_at DESC`,
		householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("list pets: %w", err)
	}
	defer rows.Close()

	pets, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Pet])
	if err != nil {
		return nil, fmt.Errorf("collect pets: %w", err)
	}
	return pets, nil
}

// Get returns a single pet by ID, scoped to the given household.
// Returns nil if not found.
func (r *Repo) Get(ctx context.Context, householdID, id uuid.UUID) (*Pet, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, name, species, breed, date_of_birth, vet_name, vet_phone, notes, created_at, updated_at
		 FROM pets WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("get pet: %w", err)
	}
	defer rows.Close()

	pet, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Pet])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect pet: %w", err)
	}
	return pet, nil
}

// Create inserts a new pet for the given household.
func (r *Repo) Create(ctx context.Context, householdID uuid.UUID, p *Pet) (*Pet, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO pets (household_id, name, species, breed, date_of_birth, vet_name, vet_phone, notes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, household_id, name, species, breed, date_of_birth, vet_name, vet_phone, notes, created_at, updated_at`,
		householdID, p.Name, p.Species, p.Breed, p.DateOfBirth, p.VetName, p.VetPhone, p.Notes,
	)
	if err != nil {
		return nil, fmt.Errorf("create pet: %w", err)
	}
	defer rows.Close()

	pet, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Pet])
	if err != nil {
		return nil, fmt.Errorf("collect created pet: %w", err)
	}
	return pet, nil
}

// Update modifies an existing pet scoped to the given household.
// Returns nil if the pet was not found.
func (r *Repo) Update(ctx context.Context, householdID, id uuid.UUID, p *Pet) (*Pet, error) {
	rows, err := r.pool.Query(ctx,
		`UPDATE pets
		 SET name = $1, species = $2, breed = $3, date_of_birth = $4, vet_name = $5, vet_phone = $6, notes = $7,
		     updated_at = NOW()
		 WHERE id = $8 AND household_id = $9
		 RETURNING id, household_id, name, species, breed, date_of_birth, vet_name, vet_phone, notes, created_at, updated_at`,
		p.Name, p.Species, p.Breed, p.DateOfBirth, p.VetName, p.VetPhone, p.Notes,
		id, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("update pet: %w", err)
	}
	defer rows.Close()

	pet, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Pet])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect updated pet: %w", err)
	}
	return pet, nil
}

// Delete removes a pet scoped to the given household.
// Returns pgx.ErrNoRows if not found.
func (r *Repo) Delete(ctx context.Context, householdID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM pets WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return fmt.Errorf("delete pet: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
