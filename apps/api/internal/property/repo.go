package property

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/legacyfinance"
)

// Repo provides read/write access to the properties and rooms tables
// using a pgx connection pool. All property methods are scoped to a
// household_id to enforce multi-tenant isolation.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new property repository backed by the given connection pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// selectProperty reads a property with its legacy finance fields derived
// from its loan and bills (#4704). properties.mortgage_*, property_tax_*,
// insurance_* and hoa_fee_monthly are never read. The column order matches
// the Property struct (RowToAddrOfStructByPos).
var selectProperty = `SELECT p.id, p.household_id, p.name, p.address, p.property_type, p.notes, p.created_at, p.updated_at,
        p.purchase_price, p.purchase_date::text, p.current_value, p.down_payment,
        m.original_amount::text, m.interest_rate::text, m.term_months::text, m.start_date::text,
        m.lender, m.account_number,
        ` + legacyfinance.AmountSQL(legacyfinance.PropertyTax, "tx") + `,
        ` + legacyfinance.DueMonthsTextSQL("tx") + `,
        ` + legacyfinance.AmountSQL(legacyfinance.PropertyInsurance, "ins") + `,
        ` + legacyfinance.ProviderSQL("ins") + `,
        ` + legacyfinance.AmountSQL(legacyfinance.HOAFee, "hoa") + `
   FROM properties p` +
	legacyfinance.LoanJoin(legacyfinance.Mortgage, "m", "property_id", "p") +
	legacyfinance.BillJoin(legacyfinance.PropertyTax, "tx", "property_id", "p") +
	legacyfinance.BillJoin(legacyfinance.PropertyInsurance, "ins", "property_id", "p") +
	legacyfinance.BillJoin(legacyfinance.HOAFee, "hoa", "property_id", "p")

// Finance is the legacy finance fields of a request, parsed. Each one is a
// partial update of the property's loan or bill; nil leaves it alone.
type Finance struct {
	Mortgage  legacyfinance.LoanFields
	Tax       legacyfinance.BillFields
	Insurance legacyfinance.BillFields
	HOA       legacyfinance.BillFields
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func getProperty(ctx context.Context, q querier, propertyID, householdID uuid.UUID) (*Property, error) {
	rows, err := q.Query(ctx, selectProperty+` WHERE p.id = $1 AND p.household_id = $2`, propertyID, householdID)
	if err != nil {
		return nil, fmt.Errorf("get property: %w", err)
	}
	defer rows.Close()

	property, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Property])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect property: %w", err)
	}
	return property, nil
}

// applyFinance writes the legacy finance fields to the property's loan and
// bills, in the caller's transaction.
func applyFinance(ctx context.Context, tx pgx.Tx, s legacyfinance.Subject, f Finance) error {
	if err := legacyfinance.ApplyLoan(ctx, tx, s, legacyfinance.Mortgage, f.Mortgage); err != nil {
		return err
	}
	if err := legacyfinance.ApplyBill(ctx, tx, s, legacyfinance.PropertyTax, f.Tax); err != nil {
		return err
	}
	if err := legacyfinance.ApplyBill(ctx, tx, s, legacyfinance.PropertyInsurance, f.Insurance); err != nil {
		return err
	}
	return legacyfinance.ApplyBill(ctx, tx, s, legacyfinance.HOAFee, f.HOA)
}

// ListProperties returns all properties for a household.
func (r *Repo) ListProperties(ctx context.Context, householdID uuid.UUID) ([]*Property, error) {
	rows, err := r.pool.Query(ctx, selectProperty+` WHERE p.household_id = $1 ORDER BY p.name`, householdID)
	if err != nil {
		return nil, fmt.Errorf("list properties: %w", err)
	}
	defer rows.Close()

	properties, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Property])
	if err != nil {
		return nil, fmt.Errorf("collect properties: %w", err)
	}
	return properties, nil
}

// GetProperty returns a single property by ID, scoped to the household.
func (r *Repo) GetProperty(ctx context.Context, propertyID, householdID uuid.UUID) (*Property, error) {
	return getProperty(ctx, r.pool, propertyID, householdID)
}

// CreateProperty inserts a new property, applies the legacy finance fields
// to loans/bills about it in the same transaction, and returns the created
// record. Nullable columns are passed through as-is (nil => NULL).
func (r *Repo) CreateProperty(ctx context.Context, householdID uuid.UUID, p *Property, fin Finance) (*Property, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("create property: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var id uuid.UUID
	if err := tx.QueryRow(ctx,
		`INSERT INTO properties (
			household_id, name, address, property_type, notes,
			purchase_price, purchase_date, current_value, down_payment
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id`,
		householdID, p.Name, p.Address, p.PropertyType, p.Notes,
		p.PurchasePrice, p.PurchaseDate, p.CurrentValue, p.DownPayment,
	).Scan(&id); err != nil {
		return nil, fmt.Errorf("create property: %w", err)
	}
	s := legacyfinance.Subject{Col: "property_id", ID: id, HouseholdID: householdID, Name: p.Name}
	if err := applyFinance(ctx, tx, s, fin); err != nil {
		return nil, fmt.Errorf("create property: %w", err)
	}
	property, err := getProperty(ctx, tx, id, householdID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("create property: commit: %w", err)
	}
	return property, nil
}

// UpdateProperty updates an existing property and returns the updated record.
// name uses COALESCE(NULLIF($3, ”), name) so an empty/omitted name leaves the
// column unchanged (partial update); every nullable financial field uses
// COALESCE so nil leaves the column unchanged and a provided value overwrites
// it. address, property_type, and notes keep their original direct-assignment
// semantics (nil clears the column). The legacy finance fields are applied
// to the property's loan/bills in the same transaction, after the property
// is known to exist. Returns nil if the property was not found.
func (r *Repo) UpdateProperty(ctx context.Context, propertyID, householdID uuid.UUID, p *Property, fin Finance) (*Property, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("update property: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var name string
	err = tx.QueryRow(ctx,
		`UPDATE properties
		 SET name = COALESCE(NULLIF($3, ''), name),
		     address = $4,
		     property_type = $5,
		     notes = $6,
		     purchase_price = COALESCE($7, purchase_price),
		     purchase_date = COALESCE($8, purchase_date),
		     current_value = COALESCE($9, current_value),
		     down_payment = COALESCE($10, down_payment),
		     updated_at = NOW()
		 WHERE id = $1 AND household_id = $2
		 RETURNING name`,
		propertyID, householdID,
		p.Name, p.Address, p.PropertyType, p.Notes,
		p.PurchasePrice, p.PurchaseDate, p.CurrentValue, p.DownPayment,
	).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update property: %w", err)
	}
	s := legacyfinance.Subject{Col: "property_id", ID: propertyID, HouseholdID: householdID, Name: name}
	if err := applyFinance(ctx, tx, s, fin); err != nil {
		return nil, fmt.Errorf("update property: %w", err)
	}
	property, err := getProperty(ctx, tx, propertyID, householdID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("update property: commit: %w", err)
	}
	return property, nil
}

// CountAssets returns how many assets are located in the property (directly
// or through one of its rooms). Deleting the property deletes them too: the
// assets.property_id FK is ON DELETE CASCADE since migration 038. Returns
// found=false when the property is missing or belongs to another family.
func (r *Repo) CountAssets(ctx context.Context, propertyID, householdID uuid.UUID) (count int, found bool, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM assets a WHERE a.property_id = p.id AND a.household_id = p.household_id)
		 FROM properties p WHERE p.id = $1 AND p.household_id = $2`,
		propertyID, householdID,
	).Scan(&count)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("count property assets: %w", err)
	}
	return count, true, nil
}

// DeleteProperty deletes a property by ID, scoped to the household, with its
// rooms and assets (CASCADE). Files and secrets about them are re-homed to the
// family by the rehome_entity_refs trigger. Returns the number of assets
// deleted, and found=false if the property was not found.
func (r *Repo) DeleteProperty(ctx context.Context, propertyID, householdID uuid.UUID) (assets int, found bool, err error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("delete property: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	err = tx.QueryRow(ctx,
		`SELECT count(*) FROM assets WHERE property_id = $1 AND household_id = $2`,
		propertyID, householdID,
	).Scan(&assets)
	if err != nil {
		return 0, false, fmt.Errorf("delete property: count assets: %w", err)
	}
	tag, err := tx.Exec(ctx,
		`DELETE FROM properties WHERE id = $1 AND household_id = $2`,
		propertyID, householdID,
	)
	if err != nil {
		return 0, false, fmt.Errorf("delete property: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return 0, false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("delete property: commit: %w", err)
	}
	return assets, true, nil
}

// ListRooms returns all rooms for a property.
func (r *Repo) ListRooms(ctx context.Context, propertyID uuid.UUID) ([]*Room, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, property_id, name, floor, notes, created_at
		 FROM rooms WHERE property_id = $1 ORDER BY floor, name`,
		propertyID,
	)
	if err != nil {
		return nil, fmt.Errorf("list rooms: %w", err)
	}
	defer rows.Close()

	rooms, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Room])
	if err != nil {
		return nil, fmt.Errorf("collect rooms: %w", err)
	}
	return rooms, nil
}

// CreateRoom inserts a new room under a property and returns the created
// record. rooms.household_id is written explicitly (domain model v2); the
// database rejects a property of another family.
func (r *Repo) CreateRoom(ctx context.Context, householdID, propertyID uuid.UUID, name string, floor *int, notes *string) (*Room, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO rooms (household_id, property_id, name, floor, notes)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, property_id, name, floor, notes, created_at`,
		householdID, propertyID, name, floor, notes,
	)
	if err != nil {
		return nil, fmt.Errorf("create room: %w", err)
	}
	defer rows.Close()

	room, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Room])
	if err != nil {
		return nil, fmt.Errorf("collect created room: %w", err)
	}
	return room, nil
}
