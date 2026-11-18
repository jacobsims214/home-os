// Package budget serves the family's budget categories and the monthly
// obligations view (bills and loan payments counted against them).
//
// It is budgeting metadata only: no transactions and no ledger (KB
// decisions/2026-11-15-home-os-product-direction.md). Tables come from
// migration 044 (budget_categories, bills/loans.budget_category_id).
package budget

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/billdue"
)

// Category is a budget_categories row. MonthlyAmount is NUMERIC(12,2) carried
// as a string ("450.00") so no precision is lost.
type Category struct {
	ID            uuid.UUID
	HouseholdID   uuid.UUID
	Name          string
	MonthlyAmount *string
	Notes         *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Repo reads and writes budget categories. Every query is scoped to the
// household, so an id from another family is simply not found.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a budget repository.
func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

const categoryColumns = `id, household_id, name, monthly_amount::text, notes, created_at, updated_at`

func scanCategory(row pgx.Row) (*Category, error) {
	c := &Category{}
	if err := row.Scan(&c.ID, &c.HouseholdID, &c.Name, &c.MonthlyAmount, &c.Notes, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return c, nil
}

// List returns the household's categories by name.
func (r *Repo) List(ctx context.Context, householdID uuid.UUID) ([]*Category, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+categoryColumns+` FROM budget_categories
		 WHERE household_id = $1 ORDER BY lower(name), id`, householdID)
	if err != nil {
		return nil, fmt.Errorf("list budget categories: %w", err)
	}
	defer rows.Close()
	out := []*Category{}
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan budget category: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns one category, or nil if the household has none with that id.
func (r *Repo) Get(ctx context.Context, id, householdID uuid.UUID) (*Category, error) {
	c, err := scanCategory(r.pool.QueryRow(ctx,
		`SELECT `+categoryColumns+` FROM budget_categories WHERE id = $1 AND household_id = $2`,
		id, householdID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get budget category: %w", err)
	}
	return c, nil
}

// Create inserts c for c.HouseholdID.
func (r *Repo) Create(ctx context.Context, c *Category) (*Category, error) {
	out, err := scanCategory(r.pool.QueryRow(ctx,
		`INSERT INTO budget_categories (household_id, name, monthly_amount, notes)
		 VALUES ($1, $2, $3::numeric, $4)
		 RETURNING `+categoryColumns,
		c.HouseholdID, c.Name, c.MonthlyAmount, c.Notes))
	if err != nil {
		return nil, fmt.Errorf("create budget category: %w", err)
	}
	return out, nil
}

// Update writes c's name, amount and notes. Returns nil if not found.
func (r *Repo) Update(ctx context.Context, c *Category) (*Category, error) {
	out, err := scanCategory(r.pool.QueryRow(ctx,
		`UPDATE budget_categories
		 SET name = $3, monthly_amount = $4::numeric, notes = $5, updated_at = NOW()
		 WHERE id = $1 AND household_id = $2
		 RETURNING `+categoryColumns,
		c.ID, c.HouseholdID, c.Name, c.MonthlyAmount, c.Notes))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update budget category: %w", err)
	}
	return out, nil
}

// Delete removes a category. Bills and loans that pointed at it keep
// existing with no category (ON DELETE SET NULL). Reports whether it existed.
func (r *Repo) Delete(ctx context.Context, id, householdID uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM budget_categories WHERE id = $1 AND household_id = $2`, id, householdID)
	if err != nil {
		return false, fmt.Errorf("delete budget category: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// MemberRole returns userID's role in householdID, read from memberships, or
// "" if the user is not a member there.
func (r *Repo) MemberRole(ctx context.Context, householdID, userID uuid.UUID) (string, error) {
	var role string
	err := r.pool.QueryRow(ctx,
		`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
		householdID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("member role: %w", err)
	}
	return role, nil
}

// HouseholdTimezone returns the household's IANA timezone ("UTC" if unset).
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

// Obligation is one bill or loan payment in a month, in cents.
//
// MonthlyCents is what it costs per month, nil when the amount is unknown
// (a bill with no amount, a loan with no monthly_payment): those are listed
// but not counted.
type Obligation struct {
	Kind             string // "bill" or "loan"
	ID               uuid.UUID
	Name             string
	BudgetCategoryID *uuid.UUID
	Frequency        string // bills: bills.frequency; loans: "monthly"
	AmountCents      *int64 // the bill amount or loan monthly_payment as stored
	MonthlyCents     *int64
	DueDay           *int
	DueDate          *time.Time
}

// DueSoon returns what falls due from today through today + days for the
// household. It delegates to home-os/billdue, the one implementation of the
// due rule and the due-soon query (#4819): MCP budget_summary's due_soon
// section is filled by the same function, so the API and the agent cannot
// disagree. today is the family's date as midnight UTC.
func (r *Repo) DueSoon(ctx context.Context, householdID uuid.UUID, today time.Time, days int) (*billdue.DueSoon, error) {
	return billdue.LoadDueSoonRows(ctx, r.pool, householdID.String(), today, days)
}

// Obligations returns the household's bills and the loans active in the
// month starting at monthStart (UTC date, first of the month).
//
// Monthly amounts spread a bill over the months it covers, as the MCP tool
// catalog specifies for budget_summary (KB architecture/mcp-tool-catalog.md,
// "Non-monthly bills are spread across months"): monthly x1, quarterly /3,
// semiannual /6, annual /12. A once bill counts in full in the month its
// due_date falls in and is left out of every other month. Each item is rounded to
// the cent; totals add the rounded items.
//
// A loan is active when remaining_balance > 0, it has started by the end of
// the month and term_end is not before the month: the same rule as the MCP
// budget_summary obligations (apps/mcp/internal/finance loadObligations).
//
// This is not a next-due computation: due_day/due_date are reported as
// stored. Next-due stays with the MCP/worker BillDue rule.
func (r *Repo) Obligations(ctx context.Context, householdID uuid.UUID, monthStart time.Time) ([]Obligation, error) {
	monthEnd := monthStart.AddDate(0, 1, 0)
	rows, err := r.pool.Query(ctx,
		`SELECT 'bill', id, name, budget_category_id, frequency,
		        ROUND(amount * 100)::bigint,
		        ROUND(CASE frequency
		                WHEN 'monthly'    THEN amount
		                WHEN 'quarterly'  THEN amount / 3
		                WHEN 'semiannual' THEN amount / 6
		                WHEN 'annual'     THEN amount / 12
		                WHEN 'once'       THEN CASE WHEN due_date >= $2 AND due_date < $3 THEN amount ELSE 0 END
		              END * 100)::bigint,
		        due_day, due_date
		   FROM bills
		  WHERE household_id = $1
		    AND (frequency <> 'once' OR (due_date >= $2 AND due_date < $3))
		 UNION ALL
		 SELECT 'loan', id, name, budget_category_id, 'monthly',
		        ROUND(monthly_payment * 100)::bigint,
		        ROUND(monthly_payment * 100)::bigint,
		        due_day, NULL::date
		   FROM loans
		  WHERE household_id = $1
		    AND remaining_balance > 0
		    AND (start_date IS NULL OR start_date < $3)
		    AND (term_end IS NULL OR term_end >= $2)
		 ORDER BY 3, 1, 2`,
		householdID, monthStart, monthEnd)
	if err != nil {
		return nil, fmt.Errorf("budget obligations: %w", err)
	}
	defer rows.Close()
	var out []Obligation
	for rows.Next() {
		var o Obligation
		if err := rows.Scan(&o.Kind, &o.ID, &o.Name, &o.BudgetCategoryID, &o.Frequency,
			&o.AmountCents, &o.MonthlyCents, &o.DueDay, &o.DueDate); err != nil {
			return nil, fmt.Errorf("scan obligation: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
