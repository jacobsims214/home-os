package bill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the bills table using a pgx connection pool.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new bill repository backed by the given connection pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// scanBill scans a single Bill from a pgx.Row.
func scanBill(row pgx.Row) (*Bill, error) {
	b := &Bill{}
	err := row.Scan(
		&b.ID,
		&b.HouseholdID,
		&b.PropertyID,
		&b.Name,
		&b.Amount,
		&b.DueDay,
		&b.Category,
		&b.VendorID,
		&b.Rrule,
		&b.Notes,
		&b.CreatedAt,
		&b.UpdatedAt,
		&b.EntityType,
		&b.EntityID,
		&b.PaidDate,
		&b.IsAutopay,
		&b.AccountNumber,
		&b.PaymentURL,
	)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// List returns all bills for a household, ordered by name.
func (r *Repo) List(ctx context.Context, householdID uuid.UUID) ([]*Bill, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, property_id, name, amount, due_day,
		        category, vendor_id, rrule, notes, created_at, updated_at,
		        entity_type, entity_id, paid_date, is_autopay, account_number, payment_url
		 FROM bills
		 WHERE household_id = $1
		 ORDER BY name`,
		householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("list bills: %w", err)
	}
	defer rows.Close()

	var bills []*Bill
	for rows.Next() {
		b, err := scanBill(rows)
		if err != nil {
			return nil, fmt.Errorf("scan bill: %w", err)
		}
		bills = append(bills, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bills: %w", err)
	}
	return bills, nil
}

// Get returns a single bill by ID, scoped to the household.
func (r *Repo) Get(ctx context.Context, id, householdID uuid.UUID) (*Bill, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, household_id, property_id, name, amount, due_day,
		        category, vendor_id, rrule, notes, created_at, updated_at,
		        entity_type, entity_id, paid_date, is_autopay, account_number, payment_url
		 FROM bills
		 WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	b, err := scanBill(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get bill: %w", err)
	}
	return b, nil
}

// Create inserts a new bill and writes a bill.created outbox event in a single
// transaction. Returns the created bill.
func (r *Repo) Create(ctx context.Context, b *Bill) (*Bill, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("create bill: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx,
		`INSERT INTO bills (household_id, property_id, name, amount, due_day,
		                    category, vendor_id, rrule, notes,
		                    entity_type, entity_id, is_autopay, account_number, payment_url)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		 RETURNING id, household_id, property_id, name, amount, due_day,
		           category, vendor_id, rrule, notes, created_at, updated_at,
		           entity_type, entity_id, paid_date, is_autopay, account_number, payment_url`,
		b.HouseholdID, b.PropertyID, b.Name, b.Amount, b.DueDay,
		b.Category, b.VendorID, b.Rrule, b.Notes,
		b.EntityType, b.EntityID, b.IsAutopay, b.AccountNumber, b.PaymentURL,
	)
	created, err := scanBill(row)
	if err != nil {
		return nil, fmt.Errorf("create bill: insert: %w", err)
	}

	payload, err := json.Marshal(billEventPayload{ID: created.ID.String(), Name: created.Name})
	if err != nil {
		return nil, fmt.Errorf("create bill: marshal payload: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO outbox_events (type, payload, household_id)
		 VALUES ($1, $2, $3)`,
		"bill.created", payload, created.HouseholdID,
	)
	if err != nil {
		return nil, fmt.Errorf("create bill: outbox event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("create bill: commit: %w", err)
	}
	return created, nil
}

// Update updates an existing bill and writes a bill.updated outbox event in a
// single transaction. Returns the updated bill.
func (r *Repo) Update(ctx context.Context, b *Bill) (*Bill, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("update bill: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx,
		`UPDATE bills
		 SET property_id = $2, name = $3, amount = $4, due_day = $5,
		     category = $6, vendor_id = $7, rrule = $8, notes = $9,
		     entity_type = $10, entity_id = $11, is_autopay = $12,
		     account_number = $13, payment_url = $14,
		     updated_at = NOW()
		 WHERE id = $1 AND household_id = $15
		 RETURNING id, household_id, property_id, name, amount, due_day,
		           category, vendor_id, rrule, notes, created_at, updated_at,
		           entity_type, entity_id, paid_date, is_autopay, account_number, payment_url`,
		b.ID, b.PropertyID, b.Name, b.Amount, b.DueDay,
		b.Category, b.VendorID, b.Rrule, b.Notes,
		b.EntityType, b.EntityID, b.IsAutopay, b.AccountNumber, b.PaymentURL,
		b.HouseholdID,
	)
	updated, err := scanBill(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("update bill: %w", err)
	}

	payload, err := json.Marshal(billEventPayload{ID: updated.ID.String(), Name: updated.Name})
	if err != nil {
		return nil, fmt.Errorf("update bill: marshal payload: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO outbox_events (type, payload, household_id)
		 VALUES ($1, $2, $3)`,
		"bill.updated", payload, updated.HouseholdID,
	)
	if err != nil {
		return nil, fmt.Errorf("update bill: outbox event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("update bill: commit: %w", err)
	}
	return updated, nil
}

// Delete removes a bill by ID, scoped to the household. Writes a bill.deleted
// outbox event in a single transaction.
func (r *Repo) Delete(ctx context.Context, id, householdID uuid.UUID) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("delete bill: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`DELETE FROM bills WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return fmt.Errorf("delete bill: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}

	payload, err := json.Marshal(billEventPayload{ID: id.String()})
	if err != nil {
		return fmt.Errorf("delete bill: marshal payload: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO outbox_events (type, payload, household_id)
		 VALUES ($1, $2, $3)`,
		"bill.deleted", payload, householdID,
	)
	if err != nil {
		return fmt.Errorf("delete bill: outbox event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("delete bill: commit: %w", err)
	}
	return nil
}

// billEventPayload is the JSON payload written to outbox_events for bill events.
type billEventPayload struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// isUniqueViolation returns true if the error is a PostgreSQL unique constraint
// violation (code 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
