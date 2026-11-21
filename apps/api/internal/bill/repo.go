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

	"home-os/billcal"
)

// Repo provides read/write access to the bills table using a pgx connection pool.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new bill repository backed by the given connection pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// billColumns is the select list scanBill reads, in its order.
//
// The payment account and payee are scalar subqueries on their primary keys,
// not joins, so the SAME list works in a RETURNING clause (#5498). A LEFT JOIN
// would need a second column list for insert/update, and two lists that must
// agree are a second place for them to disagree.
const billColumns = `id, household_id, property_id, name, amount, due_day,
	category, vendor_id, rrule, notes, created_at, updated_at,
	vehicle_id, asset_id, paid_date, is_autopay, account_number, payment_url,
	frequency, due_date, budget_category_id, paid_for_due_date, source_document_id,
	autopay_account_id, payee_name,
	(SELECT pa.name FROM payment_accounts pa WHERE pa.id = bills.autopay_account_id),
	(SELECT pa.institution FROM payment_accounts pa WHERE pa.id = bills.autopay_account_id),
	(SELECT pa.last_four FROM payment_accounts pa WHERE pa.id = bills.autopay_account_id),
	(SELECT pa.account_type::text FROM payment_accounts pa WHERE pa.id = bills.autopay_account_id),
	(SELECT v.name FROM vendors v WHERE v.id = bills.vendor_id),
	(SELECT v.phone FROM vendors v WHERE v.id = bills.vendor_id),
	(SELECT v.email FROM vendors v WHERE v.id = bills.vendor_id),
	(SELECT v.website FROM vendors v WHERE v.id = bills.vendor_id)`

// scanBill scans a single Bill from a pgx.Row.
func scanBill(row pgx.Row) (*Bill, error) {
	b := &Bill{}
	// The account and payee columns are nullable subqueries; scanBill folds
	// them into b.PaymentAccount and b.Payee.
	var accountName, accountInstitution, accountLastFour, accountType *string
	var payeeName, payeePhone, payeeEmail, payeeWebsite *string
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
		&b.VehicleID,
		&b.AssetID,
		&b.PaidDate,
		&b.IsAutopay,
		&b.AccountNumber,
		&b.PaymentURL,
		&b.Frequency,
		&b.DueDate,
		&b.BudgetCategoryID,
		&b.PaidForDueDate,
		&b.SourceDocumentID,
		&b.AutopayAccountID,
		&b.PayeeName,
		&accountName, &accountInstitution, &accountLastFour, &accountType,
		&payeeName, &payeePhone, &payeeEmail, &payeeWebsite,
	)
	if err != nil {
		return nil, err
	}
	// A linked account is one row: if the id resolved, the name resolved too
	// (it is NOT NULL on payment_accounts). A dangling id cannot happen (the
	// composite FK's ON DELETE SET NULL), so a nil name means "no account".
	if b.AutopayAccountID != nil && accountName != nil {
		b.PaymentAccount = &PaymentAccount{
			ID: *b.AutopayAccountID, Name: *accountName, Institution: accountInstitution,
			LastFour: accountLastFour, AccountType: orDefault(accountType, "checking"),
		}
	}
	// The payee: the vendors row when vendor_id is set (one fact, one place,
	// 075), otherwise the free-text name of a payee that is not a vendor.
	switch {
	case b.VendorID != nil && payeeName != nil && *payeeName != "":
		id := *b.VendorID
		b.Payee = &Payee{VendorID: &id, Name: *payeeName, Phone: payeePhone, Email: payeeEmail, Website: payeeWebsite}
	case b.PayeeName != nil && *b.PayeeName != "":
		b.Payee = &Payee{Name: *b.PayeeName}
	}
	return b, nil
}

// orDefault is s, or def when s is nil (the test files have their own deref).
func orDefault(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}

// HouseholdTimezone returns the household's IANA timezone ("UTC" when unset),
// for the attention state and every due date: they are judged against the
// family's date, not the server's.
func (r *Repo) HouseholdTimezone(ctx context.Context, householdID uuid.UUID) (string, error) {
	var tz *string
	if err := r.pool.QueryRow(ctx, `SELECT timezone FROM households WHERE id = $1`, householdID).Scan(&tz); err != nil {
		return "", fmt.Errorf("household timezone: %w", err)
	}
	if tz == nil || *tz == "" {
		return "UTC", nil
	}
	return *tz, nil
}

// List returns all bills for a household, ordered by name.
func (r *Repo) List(ctx context.Context, householdID uuid.UUID) ([]*Bill, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+billColumns+`
		 FROM bills
		 WHERE household_id = $1
		 -- b-qualified: the payee subquery also selects a vendors.name (#5498)
		 ORDER BY bills.name`,
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
		`SELECT `+billColumns+`
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
		                    vehicle_id, asset_id, is_autopay, account_number, payment_url,
		                    frequency, due_date, budget_category_id,
		                    autopay_account_id, payee_name)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
		         -- A caller that says nothing about autopay gets false, not NULL
		         -- (075: bills.is_autopay is NOT NULL, and false is the value
		         -- that asks a person to pay instead of staying silent).
		         COALESCE($12, false), $13, $14,
		         COALESCE(NULLIF($15, ''), 'monthly'), $16, $17, $18, $19)
		 RETURNING `+billColumns,
		b.HouseholdID, b.PropertyID, b.Name, b.Amount, b.DueDay,
		b.Category, b.VendorID, b.Rrule, b.Notes,
		b.VehicleID, b.AssetID, b.IsAutopay, b.AccountNumber, b.PaymentURL,
		b.Frequency, b.DueDate, b.BudgetCategoryID,
		b.AutopayAccountID, b.PayeeName,
	)
	created, err := scanBill(row)
	if err != nil {
		return nil, fmt.Errorf("create bill: insert: %w", err)
	}

	// Mirror the bill onto the household's Bills calendar, as the MCP
	// bill_create does (#4716).
	if _, err := billcal.SyncByID(ctx, tx, created.HouseholdID.String(), created.ID.String()); err != nil {
		return nil, fmt.Errorf("create bill: %w", err)
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
		     -- a stored paid_for_due_date is on the old schedule: clear it
		     -- when the due day, frequency or due date changes (migration
		     -- 052, #4655), so coverage is inferred again for the new one
		     paid_for_due_date = CASE WHEN due_day IS DISTINCT FROM $5
		                                OR frequency IS DISTINCT FROM $16
		                                OR due_date IS DISTINCT FROM $17::date
		                              THEN NULL ELSE paid_for_due_date END,
		     category = $6, vendor_id = $7, rrule = $8, notes = $9,
		     -- absent keeps the current value; the column is NOT NULL (075).
		     vehicle_id = $10, asset_id = $11, is_autopay = COALESCE($12, is_autopay),
		     account_number = $13, payment_url = $14,
		     frequency = $16, due_date = $17, budget_category_id = $18,
		     -- $19/$20 are the whole new value each time (the handler merged the
		     -- stored row with the request), so null clears the link.
		     autopay_account_id = $19, payee_name = $20,
		     updated_at = NOW()
		 WHERE id = $1 AND household_id = $15
		 RETURNING `+billColumns,
		b.ID, b.PropertyID, b.Name, b.Amount, b.DueDay,
		b.Category, b.VendorID, b.Rrule, b.Notes,
		b.VehicleID, b.AssetID, b.IsAutopay, b.AccountNumber, b.PaymentURL,
		b.HouseholdID,
		b.Frequency, b.DueDate, b.BudgetCategoryID,
		b.AutopayAccountID, b.PayeeName,
	)
	updated, err := scanBill(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("update bill: %w", err)
	}

	// Keep the bill's Bills-calendar event in step (#4716); unchanged when
	// only fields the event doesn't show changed.
	if _, err := billcal.SyncByID(ctx, tx, updated.HouseholdID.String(), updated.ID.String()); err != nil {
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

	// The mirror event goes first: the 038 rehome trigger would only unlink
	// it from the deleted bill, leaving it on phones. Rolled back with the
	// transaction when the bill is not found.
	if _, err := billcal.Delete(ctx, tx, householdID.String(), id.String()); err != nil {
		return fmt.Errorf("delete bill: %w", err)
	}

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
