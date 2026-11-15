package loan

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the loans table using a pgx connection pool.
// All queries are scoped to household_id for tenant isolation.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new loan repository.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// Filter narrows List. Kind is "property", "vehicle" or "asset" (records
// with that kind of subject) or "household" (family-level records only).
type Filter struct {
	Kind       string
	PropertyID *uuid.UUID
	VehicleID  *uuid.UUID
}

// List returns the loans for the given household, newest first.
func (r *Repo) List(ctx context.Context, householdID uuid.UUID, f Filter) ([]*Loan, error) {
	q := `SELECT id, household_id, name, property_id, vehicle_id, asset_id,
	             lender, original_amount, interest_rate, term_months,
	             monthly_payment, remaining_balance, start_date::text, notes,
	             created_at, updated_at
	      FROM loans
	      WHERE household_id = $1`
	args := []any{householdID}
	switch f.Kind {
	case "property":
		q += ` AND property_id IS NOT NULL`
	case "vehicle":
		q += ` AND vehicle_id IS NOT NULL`
	case "asset":
		q += ` AND asset_id IS NOT NULL`
	case "household":
		q += ` AND property_id IS NULL AND vehicle_id IS NULL AND asset_id IS NULL`
	}
	if f.PropertyID != nil {
		args = append(args, *f.PropertyID)
		q += fmt.Sprintf(` AND property_id = $%d`, len(args))
	}
	if f.VehicleID != nil {
		args = append(args, *f.VehicleID)
		q += fmt.Sprintf(` AND vehicle_id = $%d`, len(args))
	}
	rows, err := r.pool.Query(ctx, q+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list loans: %w", err)
	}
	defer rows.Close()

	loans, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Loan])
	if err != nil {
		return nil, fmt.Errorf("collect loans: %w", err)
	}
	return loans, nil
}

// Get returns a single loan by ID, scoped to the given household.
// Returns nil if not found.
func (r *Repo) Get(ctx context.Context, id, householdID uuid.UUID) (*Loan, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, name, property_id, vehicle_id, asset_id,
		        lender, original_amount, interest_rate, term_months,
		        monthly_payment, remaining_balance, start_date::text, notes,
		        created_at, updated_at
		 FROM loans WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("get loan: %w", err)
	}
	defer rows.Close()

	loan, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Loan])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect loan: %w", err)
	}
	return loan, nil
}

// Create inserts a new loan for the given household and returns the created record.
func (r *Repo) Create(ctx context.Context, householdID uuid.UUID, l *Loan) (*Loan, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO loans (household_id, name, property_id, vehicle_id, asset_id,
		                    lender, original_amount, interest_rate, term_months,
		                    monthly_payment, remaining_balance, start_date, notes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		 RETURNING id, household_id, name, property_id, vehicle_id, asset_id,
		           lender, original_amount, interest_rate, term_months,
		           monthly_payment, remaining_balance, start_date::text, notes,
		           created_at, updated_at`,
		householdID, l.Name, l.PropertyID, l.VehicleID, l.AssetID,
		l.Lender, l.OriginalAmount, l.InterestRate, l.TermMonths,
		l.MonthlyPayment, l.RemainingBalance, l.StartDate, l.Notes,
	)
	if err != nil {
		return nil, fmt.Errorf("create loan: %w", err)
	}
	defer rows.Close()

	created, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Loan])
	if err != nil {
		return nil, fmt.Errorf("collect created loan: %w", err)
	}
	return created, nil
}

// Update modifies an existing loan scoped to the given household.
// Returns nil if the loan was not found.
func (r *Repo) Update(ctx context.Context, id, householdID uuid.UUID, l *Loan) (*Loan, error) {
	rows, err := r.pool.Query(ctx,
		`UPDATE loans
		 SET name = $3,
		     property_id = $4, vehicle_id = $5, asset_id = $6,
		     lender = $7, original_amount = $8, interest_rate = $9,
		     term_months = $10, monthly_payment = $11,
		     remaining_balance = $12, start_date = $13, notes = $14,
		     updated_at = NOW()
		 WHERE id = $1 AND household_id = $2
		 RETURNING id, household_id, name, property_id, vehicle_id, asset_id,
		           lender, original_amount, interest_rate, term_months,
		           monthly_payment, remaining_balance, start_date::text, notes,
		           created_at, updated_at`,
		id, householdID,
		l.Name, l.PropertyID, l.VehicleID, l.AssetID,
		l.Lender, l.OriginalAmount, l.InterestRate, l.TermMonths,
		l.MonthlyPayment, l.RemainingBalance, l.StartDate, l.Notes,
	)
	if err != nil {
		return nil, fmt.Errorf("update loan: %w", err)
	}
	defer rows.Close()

	updated, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Loan])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect updated loan: %w", err)
	}
	return updated, nil
}

// Delete removes a loan scoped to the given household.
// Returns pgx.ErrNoRows if not found.
func (r *Repo) Delete(ctx context.Context, id, householdID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM loans WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return fmt.Errorf("delete loan: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}