// Package budgetcat resolves the budget category (budget_categories,
// migration 044) a bill or loan is filed under, so bill_create/bill_update
// and loan_create/loan_update can set bills/loans.budget_category_id and
// budget_summary groups them (#4702). Names follow budget_set_category: one
// category per family whatever the case or spacing (the 044 unique index on
// lower(btrim(name))), and inner whitespace is collapsed before storing.
//
// It is a separate package because internal/budget imports internal/bill.
package budgetcat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MaxNameLen is budget_categories.name VARCHAR(255).
const MaxNameLen = 255

// ErrNotFound is returned by Resolve for a UUID that is not a budget
// category of the family. A category of another family looks the same.
var ErrNotFound = errors.New("budget category not found")

// ErrTooLong is returned for a name longer than MaxNameLen.
var ErrTooLong = fmt.Errorf("budget category name must be at most %d characters", MaxNameLen)

// Category is one resolved budget category.
type Category struct {
	ID   uuid.UUID
	Name string // the stored spelling
	// Created is true when Resolve added the category.
	Created bool
}

// Querier is what Resolve and Lookup need: a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Normalize trims a category name and collapses inner whitespace, as
// budget_set_category does before storing it.
func Normalize(s string) string { return strings.Join(strings.Fields(s), " ") }

// Resolve returns the family's budget category for ref: a UUID of one of
// its categories, or a name (any case or spacing). A name the family does
// not have yet is created with no monthly amount; an unknown UUID is
// ErrNotFound (no category is named after it). ref must not be blank.
func Resolve(ctx context.Context, q Querier, householdID, ref string) (Category, error) {
	ref = Normalize(ref)
	if ref == "" {
		return Category{}, errors.New("budget category is blank")
	}
	if id, err := uuid.Parse(ref); err == nil {
		var c Category
		err := q.QueryRow(ctx, `SELECT id, name FROM budget_categories WHERE id = $1 AND household_id = $2`,
			id, householdID).Scan(&c.ID, &c.Name)
		if errors.Is(err, pgx.ErrNoRows) {
			return Category{}, ErrNotFound
		}
		return c, err
	}
	if utf8.RuneCountInString(ref) > MaxNameLen {
		return Category{}, ErrTooLong
	}
	if c, ok, err := Lookup(ctx, q, householdID, ref); err != nil || ok {
		return c, err
	}
	c := Category{Created: true}
	err := q.QueryRow(ctx,
		`INSERT INTO budget_categories (household_id, name) VALUES ($1, $2)
		 ON CONFLICT (household_id, lower(btrim(name))) DO NOTHING
		 RETURNING id, name`, householdID, ref).Scan(&c.ID, &c.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		// Added by a concurrent call since the lookup: use that one.
		c, ok, err := Lookup(ctx, q, householdID, ref)
		if err == nil && !ok {
			err = fmt.Errorf("budget category %q vanished after a conflicting insert", ref)
		}
		return c, err
	}
	return c, err
}

// Lookup finds the family's budget category with this name (any case or
// spacing) without creating one; ok is false when there is none.
func Lookup(ctx context.Context, q Querier, householdID, name string) (c Category, ok bool, err error) {
	name = Normalize(name)
	if name == "" {
		return Category{}, false, nil
	}
	err = q.QueryRow(ctx,
		`SELECT id, name FROM budget_categories
		 WHERE household_id = $1 AND lower(btrim(name)) = lower($2)
		 ORDER BY created_at, id LIMIT 1`, householdID, name).Scan(&c.ID, &c.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return Category{}, false, nil
	}
	return c, err == nil, err
}

// Describe is a short phrase for summaries: "budget category Housing", with
// " (new)" when it was just created.
func (c Category) Describe() string {
	if c.Created {
		return "budget category " + c.Name + " (new)"
	}
	return "budget category " + c.Name
}
