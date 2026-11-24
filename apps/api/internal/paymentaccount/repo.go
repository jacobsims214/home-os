package paymentaccount

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a payment account does not exist in the
// caller's household. Cross-family ids answer the same way an unknown id
// does, so an id cannot be walked to learn that another family's account
// exists.
var ErrNotFound = errors.New("payment account not found")

// ErrSecretNotFound is returned when a linked secret_id is not a secret OF THIS
// HOUSEHOLD. The composite FK (payment_accounts_secret_fkey, 075) refuses the
// write; this error turns that refusal into a 400 the person can act on
// instead of a raw constraint name.
var ErrSecretNotFound = errors.New("secret not found in this household")

// Repo provides read/write access to payment_accounts, always scoped by
// household_id for tenant isolation.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new payment account repository.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// columns is the one SELECT list every read uses. secret_id is read so the
// handler can say "a login is stored"; the uuid itself never leaves the API.
const columns = `id, household_id, name, institution, account_type::text,
                 last_four, notes, secret_id, created_at, updated_at`

// List returns the household's payment accounts, ordered by name so the list a
// person sees is stable between reads (created_at DESC would move a renamed
// account around for no reason).
func (r *Repo) List(ctx context.Context, householdID uuid.UUID) ([]*PaymentAccount, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+columns+` FROM payment_accounts
		 WHERE household_id = $1
		 ORDER BY lower(name), created_at`, householdID)
	if err != nil {
		return nil, fmt.Errorf("list payment accounts: %w", err)
	}
	defer rows.Close()
	out, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[PaymentAccount])
	if err != nil {
		return nil, fmt.Errorf("collect payment accounts: %w", err)
	}
	return out, nil
}

// Get returns one payment account of this household, or ErrNotFound.
func (r *Repo) Get(ctx context.Context, householdID, id uuid.UUID) (*PaymentAccount, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+columns+` FROM payment_accounts
		 WHERE household_id = $1 AND id = $2`, householdID, id)
	if err != nil {
		return nil, fmt.Errorf("get payment account: %w", err)
	}
	defer rows.Close()
	a, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByPos[PaymentAccount])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get payment account: %w", err)
	}
	return a, nil
}

// Create inserts an account for the household.
func (r *Repo) Create(ctx context.Context, a *PaymentAccount) (*PaymentAccount, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO payment_accounts
		     (household_id, name, institution, account_type, last_four, notes)
		 VALUES ($1, $2, $3, $4::payment_account_type, $5, $6)
		 RETURNING `+columns,
		a.HouseholdID, a.Name, a.Institution, a.AccountType, a.LastFour, a.Notes)
	if err != nil {
		return nil, fmt.Errorf("create payment account: %w", err)
	}
	defer rows.Close()
	out, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByPos[PaymentAccount])
	if err != nil {
		return nil, fmt.Errorf("create payment account: %w", err)
	}
	return out, nil
}

// Update replaces the editable fields of an account. A nil pointer CLEARS the
// column (the request is a full replace, as the vendors PUT is), so "remove the
// institution" needs no separate endpoint.
func (r *Repo) Update(ctx context.Context, householdID, id uuid.UUID, a *PaymentAccount) (*PaymentAccount, error) {
	rows, err := r.pool.Query(ctx,
		`UPDATE payment_accounts
		    SET name = $3, institution = $4, account_type = $5::payment_account_type,
		        last_four = $6, notes = $7
		  WHERE household_id = $1 AND id = $2
		 RETURNING `+columns,
		householdID, id, a.Name, a.Institution, a.AccountType, a.LastFour, a.Notes)
	if err != nil {
		return nil, fmt.Errorf("update payment account: %w", err)
	}
	defer rows.Close()
	out, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByPos[PaymentAccount])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update payment account: %w", err)
	}
	return out, nil
}

// Delete removes an account. bills.autopay_account_id is ON DELETE SET NULL
// (075), so a bill that was paid from this account keeps existing and simply
// stops naming an account.
func (r *Repo) Delete(ctx context.Context, householdID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM payment_accounts WHERE household_id = $1 AND id = $2`, householdID, id)
	if err != nil {
		return fmt.Errorf("delete payment account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSecret links (or, with a nil id, unlinks) the bank login for an account.
//
// The link is written in SQL against the composite FK, so a secret from another
// family is refused by the database rather than by a check this function
// forgot. The verification SELECT below exists only to turn that refusal into
// ErrSecretNotFound with a readable message; it is not what enforces it.
func (r *Repo) SetSecret(ctx context.Context, householdID, id uuid.UUID, secretID *uuid.UUID) (*PaymentAccount, error) {
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
		`UPDATE payment_accounts SET secret_id = $3
		  WHERE household_id = $1 AND id = $2
		 RETURNING `+columns,
		householdID, id, secretID)
	if err != nil {
		return nil, fmt.Errorf("link payment account secret: %w", err)
	}
	defer rows.Close()
	out, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByPos[PaymentAccount])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("link payment account secret: %w", err)
	}
	return out, nil
}
