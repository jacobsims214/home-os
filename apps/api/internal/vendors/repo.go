package vendors

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the vendors table using a pgx connection pool.
// All queries are scoped to household_id to enforce tenant isolation.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new vendor repository.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// List returns all vendors for the given household.
func (r *Repo) List(ctx context.Context, householdID uuid.UUID) ([]*Vendor, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, property_id, name, specialty, phone, email, website, notes, created_at, updated_at
		 FROM vendors WHERE household_id = $1 ORDER BY created_at DESC`,
		householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("list vendors: %w", err)
	}
	defer rows.Close()

	vendors, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Vendor])
	if err != nil {
		return nil, fmt.Errorf("collect vendors: %w", err)
	}
	return vendors, nil
}

// Get returns a single vendor by ID, scoped to the given household.
// Returns nil if not found.
func (r *Repo) Get(ctx context.Context, householdID, id uuid.UUID) (*Vendor, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, property_id, name, specialty, phone, email, website, notes, created_at, updated_at
		 FROM vendors WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("get vendor: %w", err)
	}
	defer rows.Close()

	vendor, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Vendor])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect vendor: %w", err)
	}
	return vendor, nil
}

// Create inserts a new vendor for the given household.
func (r *Repo) Create(ctx context.Context, householdID uuid.UUID, v *Vendor) (*Vendor, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO vendors (household_id, property_id, name, specialty, phone, email, website, notes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, household_id, property_id, name, specialty, phone, email, website, notes, created_at, updated_at`,
		householdID, v.PropertyID, v.Name, v.Specialty, v.Phone, v.Email, v.Website, v.Notes,
	)
	if err != nil {
		return nil, fmt.Errorf("create vendor: %w", err)
	}
	defer rows.Close()

	vendor, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Vendor])
	if err != nil {
		return nil, fmt.Errorf("collect created vendor: %w", err)
	}
	return vendor, nil
}

// Update modifies an existing vendor scoped to the given household.
// Returns nil if the vendor was not found.
func (r *Repo) Update(ctx context.Context, householdID, id uuid.UUID, v *Vendor) (*Vendor, error) {
	rows, err := r.pool.Query(ctx,
		`UPDATE vendors
		 SET property_id = $1, name = $2, specialty = $3, phone = $4, email = $5, website = $6, notes = $7,
		     updated_at = NOW()
		 WHERE id = $8 AND household_id = $9
		 RETURNING id, household_id, property_id, name, specialty, phone, email, website, notes, created_at, updated_at`,
		v.PropertyID, v.Name, v.Specialty, v.Phone, v.Email, v.Website, v.Notes,
		id, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("update vendor: %w", err)
	}
	defer rows.Close()

	vendor, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Vendor])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect updated vendor: %w", err)
	}
	return vendor, nil
}

// Delete removes a vendor scoped to the given household.
// Returns pgx.ErrNoRows if not found.
func (r *Repo) Delete(ctx context.Context, householdID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM vendors WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return fmt.Errorf("delete vendor: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
