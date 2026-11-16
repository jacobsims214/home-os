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

// List returns the vendors for the given household, newest first. A
// non-empty trade (already normalized, see NormalizeTrade) keeps only the
// vendors that have that trade; the containment test is served by the GIN
// index on vendors.trades (043).
func (r *Repo) List(ctx context.Context, householdID uuid.UUID, trade string) ([]*Vendor, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, name, trades, specialty, phone, email, website, address, notes, created_at, updated_at
		 FROM vendors
		 WHERE household_id = $1 AND ($2::text = '' OR trades @> ARRAY[$2::text])
		 ORDER BY created_at DESC`,
		householdID, trade,
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
		`SELECT id, household_id, name, trades, specialty, phone, email, website, address, notes, created_at, updated_at
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
		`INSERT INTO vendors (household_id, name, trades, specialty, phone, email, website, address, notes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id, household_id, name, trades, specialty, phone, email, website, address, notes, created_at, updated_at`,
		householdID, v.Name, nonNil(v.Trades), v.Specialty, v.Phone, v.Email, v.Website, v.Address, v.Notes,
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
// A nil v.Trades keeps the stored trades (clients that predate trades send
// none); an empty, non-nil slice clears them.
// keepAddress keeps the stored address instead of writing v.Address
// (clients that predate the address column, 065, do not send it).
// Returns nil if the vendor was not found.
func (r *Repo) Update(ctx context.Context, householdID, id uuid.UUID, v *Vendor, keepAddress bool) (*Vendor, error) {
	rows, err := r.pool.Query(ctx,
		`UPDATE vendors
		 SET name = $1, specialty = $2, phone = $3, email = $4, website = $5, notes = $6,
		     trades = COALESCE($9::text[], trades),
		     address = CASE WHEN $10::bool THEN address ELSE $11 END,
		     updated_at = NOW()
		 WHERE id = $7 AND household_id = $8
		 RETURNING id, household_id, name, trades, specialty, phone, email, website, address, notes, created_at, updated_at`,
		v.Name, v.Specialty, v.Phone, v.Email, v.Website, v.Notes,
		id, householdID, v.Trades, keepAddress, v.Address,
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

// nonNil returns trades, or an empty slice for nil (vendors.trades is NOT NULL).
func nonNil(trades []string) []string {
	if trades == nil {
		return []string{}
	}
	return trades
}
