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
//
// kind (080, #5529) picks WHICH list is being asked for, and the two lists
// never mix:
//
//	""                 the contacts list: everyone who does work, i.e.
//	                   kind <> 'payee' (service_provider and both).
//	                   This is the default because the only creation path
//	                   before 080 was the contacts list, so an existing
//	                   caller keeps the set it had, minus payee-only rows.
//	"service_provider" the same contacts list, explicitly.
//	"payee"            the bill payee picker: kind <> 'service_provider'
//	                   (payee and both).
//	"both"             exactly the rows that are both.
//
// The household_id + kind predicate is served by idx_vendors_household_kind
// (080).
func (r *Repo) List(ctx context.Context, householdID uuid.UUID, trade, kind string) ([]*Vendor, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, name, kind, trades, specialty, phone, email, website, address, notes, portal_secret_id, created_at, updated_at
		 FROM vendors
		 WHERE household_id = $1
		   AND ($2::text = '' OR trades @> ARRAY[$2::text])
		   AND CASE $3::text
		         WHEN 'payee' THEN kind <> 'service_provider'
		         WHEN 'both'  THEN kind = 'both'
		         ELSE              kind <> 'payee'
		       END
		 ORDER BY created_at DESC`,
		householdID, trade, kind,
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
		`SELECT id, household_id, name, kind, trades, specialty, phone, email, website, address, notes, portal_secret_id, created_at, updated_at
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
		`INSERT INTO vendors (household_id, name, kind, trades, specialty, phone, email, website, address, notes)
		 VALUES ($1, $2, COALESCE(NULLIF($3::text, '')::vendor_kind, 'service_provider'), $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id, household_id, name, kind, trades, specialty, phone, email, website, address, notes, portal_secret_id, created_at, updated_at`,
		householdID, v.Name, v.Kind, nonNil(v.Trades), v.Specialty, v.Phone, v.Email, v.Website, v.Address, v.Notes,
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
		     kind = COALESCE(NULLIF($12::text, '')::vendor_kind, kind),
		     updated_at = NOW()
		 WHERE id = $7 AND household_id = $8
		 RETURNING id, household_id, name, kind, trades, specialty, phone, email, website, address, notes, portal_secret_id, created_at, updated_at`,
		v.Name, v.Specialty, v.Phone, v.Email, v.Website, v.Notes,
		id, householdID, v.Trades, keepAddress, v.Address, v.Kind,
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


// ErrSecretNotFound is returned when a portal_secret_id is not a secret of this
// household. The composite FK (vendors_portal_secret_fkey, 075) refuses the
// write; this error turns that refusal into a 400 a person can act on.
var ErrSecretNotFound = errors.New("secret not found in this household")

// SetPortalSecret links (or, with nil, unlinks) the payee portal login for a
// vendor. As with payment accounts, the write is a uuid against the composite
// FK, so a secret from another family cannot be attached even if a caller skips
// the existence check below.
func (r *Repo) SetPortalSecret(ctx context.Context, householdID, id uuid.UUID, secretID *uuid.UUID) (*Vendor, error) {
	if secretID != nil {
		var exists bool
		if err := r.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM secrets WHERE id = $1 AND household_id = $2)`,
			*secretID, householdID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check secret: %w", err)
		}
		if !exists {
			return nil, ErrSecretNotFound
		}
	}
	rows, err := r.pool.Query(ctx,
		`UPDATE vendors SET portal_secret_id = $3, updated_at = NOW()
		  WHERE id = $1 AND household_id = $2
		 RETURNING id, household_id, name, kind, trades, specialty, phone, email, website, address, notes, portal_secret_id, created_at, updated_at`,
		id, householdID, secretID)
	if err != nil {
		return nil, fmt.Errorf("link vendor portal secret: %w", err)
	}
	defer rows.Close()
	v, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Vendor])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect vendor: %w", err)
	}
	return v, nil
}
