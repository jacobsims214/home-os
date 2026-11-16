package vehicle

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/legacyfinance"
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

// selectVehicle reads a vehicle with its legacy finance fields derived from
// its loan and bills (#4704). vehicles.lender, loan_*, monthly_payment,
// registration_* and insurance_* are never read. The column order matches
// the Vehicle struct (RowToAddrOfStructByPos).
var selectVehicle = `SELECT v.id, v.household_id, v.year, v.make, v.model, v.vin, v.license_plate, v.color, v.notes,
        v.purchase_price, v.purchase_date::text, v.current_value,
        l.original_amount::text,
        ` + legacyfinance.AmountSQL(legacyfinance.VehicleInsurance, "ins") + `,
        ` + legacyfinance.AmountSQL(legacyfinance.VehicleRegistration, "reg") + `,
        l.lender, l.term_months, l.monthly_payment::text,
        ` + legacyfinance.DueMonthSQL("reg") + `,
        ` + legacyfinance.ProviderSQL("ins") + `,
        v.created_at, v.updated_at
   FROM vehicles v` +
	legacyfinance.LoanJoin(legacyfinance.VehicleLoan, "l", "vehicle_id", "v") +
	legacyfinance.BillJoin(legacyfinance.VehicleInsurance, "ins", "vehicle_id", "v") +
	legacyfinance.BillJoin(legacyfinance.VehicleRegistration, "reg", "vehicle_id", "v")

// Finance is the legacy finance fields of a request, parsed. Each one is a
// partial update of the vehicle's loan or bill; nil leaves it alone.
type Finance struct {
	Loan         legacyfinance.LoanFields
	Registration legacyfinance.BillFields
	Insurance    legacyfinance.BillFields
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func get(ctx context.Context, q querier, householdID, id uuid.UUID) (*Vehicle, error) {
	rows, err := q.Query(ctx, selectVehicle+` WHERE v.id = $1 AND v.household_id = $2`, id, householdID)
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

// applyFinance writes the legacy finance fields to the vehicle's loan and
// bills, in the caller's transaction.
func applyFinance(ctx context.Context, tx pgx.Tx, s legacyfinance.Subject, f Finance) error {
	if err := legacyfinance.ApplyLoan(ctx, tx, s, legacyfinance.VehicleLoan, f.Loan); err != nil {
		return err
	}
	if err := legacyfinance.ApplyBill(ctx, tx, s, legacyfinance.VehicleRegistration, f.Registration); err != nil {
		return err
	}
	return legacyfinance.ApplyBill(ctx, tx, s, legacyfinance.VehicleInsurance, f.Insurance)
}

// subjectName is "year make model" for naming a new loan or bill.
func subjectName(year *int, make_, model *string) string {
	return (&Vehicle{Year: year, Make: make_, Model: model}).MakeString()
}

// List returns all vehicles for the given household.
func (r *Repo) List(ctx context.Context, householdID uuid.UUID) ([]*Vehicle, error) {
	rows, err := r.pool.Query(ctx, selectVehicle+` WHERE v.household_id = $1 ORDER BY v.created_at DESC`, householdID)
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
	return get(ctx, r.pool, householdID, id)
}

// Create inserts a new vehicle for the given household and applies the
// legacy finance fields to loans/bills about it in the same transaction.
func (r *Repo) Create(ctx context.Context, householdID uuid.UUID, v *Vehicle, fin Finance) (*Vehicle, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("create vehicle: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var id uuid.UUID
	if err := tx.QueryRow(ctx,
		`INSERT INTO vehicles (household_id, year, make, model, vin, license_plate, color, notes,
		                      purchase_price, purchase_date, current_value)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		 RETURNING id`,
		householdID, v.Year, v.Make, v.Model, v.VIN, v.LicensePlate, v.Color, v.Notes,
		v.PurchasePrice, v.PurchaseDate, v.CurrentValue,
	).Scan(&id); err != nil {
		return nil, fmt.Errorf("create vehicle: %w", err)
	}
	s := legacyfinance.Subject{Col: "vehicle_id", ID: id, HouseholdID: householdID, Name: subjectName(v.Year, v.Make, v.Model)}
	if err := applyFinance(ctx, tx, s, fin); err != nil {
		return nil, fmt.Errorf("create vehicle: %w", err)
	}
	vehicle, err := get(ctx, tx, householdID, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("create vehicle: commit: %w", err)
	}
	return vehicle, nil
}

// Update modifies an existing vehicle scoped to the given household. The
// vehicle's own columns are replaced; the legacy finance fields are applied
// to its loan/bills as partial updates. Returns nil if the vehicle was not
// found (and then writes nothing).
func (r *Repo) Update(ctx context.Context, householdID, id uuid.UUID, v *Vehicle, fin Finance) (*Vehicle, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("update vehicle: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var (
		year         *int
		make_, model *string
	)
	err = tx.QueryRow(ctx,
		`UPDATE vehicles
		 SET year = $1, make = $2, model = $3, vin = $4, license_plate = $5, color = $6, notes = $7,
		     purchase_price = $8, purchase_date = $9, current_value = $10,
		     updated_at = NOW()
		 WHERE id = $11 AND household_id = $12
		 RETURNING year, make, model`,
		v.Year, v.Make, v.Model, v.VIN, v.LicensePlate, v.Color, v.Notes,
		v.PurchasePrice, v.PurchaseDate, v.CurrentValue,
		id, householdID,
	).Scan(&year, &make_, &model)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update vehicle: %w", err)
	}
	s := legacyfinance.Subject{Col: "vehicle_id", ID: id, HouseholdID: householdID, Name: subjectName(year, make_, model)}
	if err := applyFinance(ctx, tx, s, fin); err != nil {
		return nil, fmt.Errorf("update vehicle: %w", err)
	}
	vehicle, err := get(ctx, tx, householdID, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("update vehicle: commit: %w", err)
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
