package property

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// ListProperties returns all properties for a household.
func (r *Repo) ListProperties(ctx context.Context, householdID uuid.UUID) ([]*Property, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, name, address, property_type, notes, created_at, updated_at,
		        purchase_price, purchase_date::text, current_value, down_payment,
		        mortgage_amount, mortgage_rate, mortgage_term_months, mortgage_start_date::text,
		        mortgage_lender, mortgage_account_number, property_tax_annual,
		        property_tax_due_months, insurance_annual, insurance_provider, hoa_fee_monthly
		 FROM properties WHERE household_id = $1 ORDER BY name`,
		householdID,
	)
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
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, name, address, property_type, notes, created_at, updated_at,
		        purchase_price, purchase_date::text, current_value, down_payment,
		        mortgage_amount, mortgage_rate, mortgage_term_months, mortgage_start_date::text,
		        mortgage_lender, mortgage_account_number, property_tax_annual,
		        property_tax_due_months, insurance_annual, insurance_provider, hoa_fee_monthly
		 FROM properties WHERE id = $1 AND household_id = $2`,
		propertyID, householdID,
	)
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

// CreateProperty inserts a new property and returns the created record.
// All financial fields are taken from the supplied *Property. Nullable columns
// are passed through as-is (nil => NULL).
func (r *Repo) CreateProperty(ctx context.Context, householdID uuid.UUID, p *Property) (*Property, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO properties (
			household_id, name, address, property_type, notes,
			purchase_price, purchase_date, current_value, down_payment,
			mortgage_amount, mortgage_rate, mortgage_term_months, mortgage_start_date,
			mortgage_lender, mortgage_account_number, property_tax_annual,
			property_tax_due_months, insurance_annual, insurance_provider, hoa_fee_monthly
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12, $13,
			$14, $15, $16,
			$17, $18, $19, $20
		)
		RETURNING id, household_id, name, address, property_type, notes, created_at, updated_at,
			purchase_price, purchase_date::text, current_value, down_payment,
			mortgage_amount, mortgage_rate, mortgage_term_months, mortgage_start_date::text,
			mortgage_lender, mortgage_account_number, property_tax_annual,
			property_tax_due_months, insurance_annual, insurance_provider, hoa_fee_monthly`,
		householdID, p.Name, p.Address, p.PropertyType, p.Notes,
		p.PurchasePrice, p.PurchaseDate, p.CurrentValue, p.DownPayment,
		p.MortgageAmount, p.MortgageRate, p.MortgageTermMonths, p.MortgageStartDate,
		p.MortgageLender, p.MortgageAccountNumber, p.PropertyTaxAnnual,
		p.PropertyTaxDueMonths, p.InsuranceAnnual, p.InsuranceProvider, p.HOAFeeMonthly,
	)
	if err != nil {
		return nil, fmt.Errorf("create property: %w", err)
	}
	defer rows.Close()

	property, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Property])
	if err != nil {
		return nil, fmt.Errorf("collect created property: %w", err)
	}
	return property, nil
}

// UpdateProperty updates an existing property and returns the updated record.
// name uses COALESCE(NULLIF($3, ”), name) so an empty/omitted name leaves the
// column unchanged (partial update); every nullable financial field uses
// COALESCE so nil leaves the column unchanged and a provided value overwrites
// it. address, property_type, and notes keep their original direct-assignment
// semantics (nil clears the column). Returns nil if the property was not found.
func (r *Repo) UpdateProperty(ctx context.Context, propertyID, householdID uuid.UUID, p *Property) (*Property, error) {
	rows, err := r.pool.Query(ctx,
		`UPDATE properties
		 SET name = COALESCE(NULLIF($3, ''), name),
		     address = $4,
		     property_type = $5,
		     notes = $6,
		     purchase_price = COALESCE($7, purchase_price),
		     purchase_date = COALESCE($8, purchase_date),
		     current_value = COALESCE($9, current_value),
		     down_payment = COALESCE($10, down_payment),
		     mortgage_amount = COALESCE($11, mortgage_amount),
		     mortgage_rate = COALESCE($12, mortgage_rate),
		     mortgage_term_months = COALESCE($13, mortgage_term_months),
		     mortgage_start_date = COALESCE($14, mortgage_start_date),
		     mortgage_lender = COALESCE($15, mortgage_lender),
		     mortgage_account_number = COALESCE($16, mortgage_account_number),
		     property_tax_annual = COALESCE($17, property_tax_annual),
		     property_tax_due_months = COALESCE($18, property_tax_due_months),
		     insurance_annual = COALESCE($19, insurance_annual),
		     insurance_provider = COALESCE($20, insurance_provider),
		     hoa_fee_monthly = COALESCE($21, hoa_fee_monthly),
		     updated_at = NOW()
		 WHERE id = $1 AND household_id = $2
		 RETURNING id, household_id, name, address, property_type, notes, created_at, updated_at,
			purchase_price, purchase_date::text, current_value, down_payment,
			mortgage_amount, mortgage_rate, mortgage_term_months, mortgage_start_date::text,
			mortgage_lender, mortgage_account_number, property_tax_annual,
			property_tax_due_months, insurance_annual, insurance_provider, hoa_fee_monthly`,
		propertyID, householdID,
		p.Name, p.Address, p.PropertyType, p.Notes,
		p.PurchasePrice, p.PurchaseDate, p.CurrentValue, p.DownPayment,
		p.MortgageAmount, p.MortgageRate, p.MortgageTermMonths, p.MortgageStartDate,
		p.MortgageLender, p.MortgageAccountNumber, p.PropertyTaxAnnual,
		p.PropertyTaxDueMonths, p.InsuranceAnnual, p.InsuranceProvider, p.HOAFeeMonthly,
	)
	if err != nil {
		return nil, fmt.Errorf("update property: %w", err)
	}
	defer rows.Close()

	property, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Property])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect updated property: %w", err)
	}
	return property, nil
}

// DeleteProperty deletes a property by ID, scoped to the household.
// Returns false if the property was not found.
func (r *Repo) DeleteProperty(ctx context.Context, propertyID, householdID uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM properties WHERE id = $1 AND household_id = $2`,
		propertyID, householdID,
	)
	if err != nil {
		return false, fmt.Errorf("delete property: %w", err)
	}
	return tag.RowsAffected() > 0, nil
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

// CreateRoom inserts a new room under a property and returns the created record.
func (r *Repo) CreateRoom(ctx context.Context, propertyID uuid.UUID, name string, floor *int, notes *string) (*Room, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO rooms (property_id, name, floor, notes)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, property_id, name, floor, notes, created_at`,
		propertyID, name, floor, notes,
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
