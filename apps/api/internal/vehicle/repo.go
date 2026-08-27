package vehicle

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the vehicles table using a pgx connection pool.
// All queries are scoped to household_id to enforce tenant isolation.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new vehicle repository.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// List returns all vehicles for the given household.
func (r *Repo) List(ctx context.Context, householdID uuid.UUID) ([]*Vehicle, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, year, make, model, vin, license_plate, color, notes,
		        purchase_price, purchase_date::text, current_value, loan_amount, insurance_cost, registration_cost,
		        lender, loan_term_months, monthly_payment, registration_renewal_month, insurance_provider,
		        created_at, updated_at
		 FROM vehicles WHERE household_id = $1 ORDER BY created_at DESC`,
		householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("list vehicles: %w", err)
	}
	defer rows.Close()

	vehicles, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Vehicle])
	if err != nil {
		return nil, fmt.Errorf("collect vehicles: %w", err)
	}
	return vehicles, nil
}

// Get returns a single vehicle by ID, scoped to the given household.
// Returns nil if not found.
func (r *Repo) Get(ctx context.Context, householdID, id uuid.UUID) (*Vehicle, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, year, make, model, vin, license_plate, color, notes,
		        purchase_price, purchase_date::text, current_value, loan_amount, insurance_cost, registration_cost,
		        lender, loan_term_months, monthly_payment, registration_renewal_month, insurance_provider,
		        created_at, updated_at
		 FROM vehicles WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("get vehicle: %w", err)
	}
	defer rows.Close()

	vehicle, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Vehicle])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect vehicle: %w", err)
	}
	return vehicle, nil
}

// Create inserts a new vehicle for the given household.
func (r *Repo) Create(ctx context.Context, householdID uuid.UUID, v *Vehicle) (*Vehicle, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO vehicles (household_id, year, make, model, vin, license_plate, color, notes,
		                      purchase_price, purchase_date, current_value, lender, loan_amount, loan_term_months,
		                      monthly_payment, registration_renewal_month, registration_cost, insurance_provider,
		                      insurance_cost)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		 RETURNING id, household_id, year, make, model, vin, license_plate, color, notes,
		           purchase_price, purchase_date::text, current_value, loan_amount, insurance_cost, registration_cost,
		           lender, loan_term_months, monthly_payment, registration_renewal_month, insurance_provider,
		           created_at, updated_at`,
		householdID, v.Year, v.Make, v.Model, v.VIN, v.LicensePlate, v.Color, v.Notes,
		v.PurchasePrice, v.PurchaseDate, v.CurrentValue, v.Lender, v.LoanAmount, v.LoanTermMonths,
		v.MonthlyPayment, v.RegistrationRenewalMonth, v.RegistrationCost, v.InsuranceProvider, v.InsuranceCost,
	)
	if err != nil {
		return nil, fmt.Errorf("create vehicle: %w", err)
	}
	defer rows.Close()

	vehicle, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Vehicle])
	if err != nil {
		return nil, fmt.Errorf("collect created vehicle: %w", err)
	}
	return vehicle, nil
}

// Update modifies an existing vehicle scoped to the given household.
// Returns nil if the vehicle was not found.
func (r *Repo) Update(ctx context.Context, householdID, id uuid.UUID, v *Vehicle) (*Vehicle, error) {
	rows, err := r.pool.Query(ctx,
		`UPDATE vehicles
		 SET year = $1, make = $2, model = $3, vin = $4, license_plate = $5, color = $6, notes = $7,
		     purchase_price = $8, purchase_date = $9, current_value = $10, loan_amount = $11,
		     insurance_cost = $12, registration_cost = $13,
		     lender = $14, loan_term_months = $15, monthly_payment = $16,
		     registration_renewal_month = $17, insurance_provider = $18,
		     updated_at = NOW()
		 WHERE id = $19 AND household_id = $20
		 RETURNING id, household_id, year, make, model, vin, license_plate, color, notes,
		           purchase_price, purchase_date::text, current_value, loan_amount, insurance_cost, registration_cost,
		           lender, loan_term_months, monthly_payment, registration_renewal_month, insurance_provider,
		           created_at, updated_at`,
		v.Year, v.Make, v.Model, v.VIN, v.LicensePlate, v.Color, v.Notes,
		v.PurchasePrice, v.PurchaseDate, v.CurrentValue, v.LoanAmount, v.InsuranceCost, v.RegistrationCost,
		v.Lender, v.LoanTermMonths, v.MonthlyPayment, v.RegistrationRenewalMonth, v.InsuranceProvider,
		id, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("update vehicle: %w", err)
	}
	defer rows.Close()

	vehicle, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Vehicle])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect updated vehicle: %w", err)
	}
	return vehicle, nil
}

// Delete removes a vehicle scoped to the given household.
// Returns pgx.ErrNoRows if not found.
func (r *Repo) Delete(ctx context.Context, householdID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM vehicles WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return fmt.Errorf("delete vehicle: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
