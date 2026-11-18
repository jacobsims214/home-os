package loan

import (
	"context"
	"strings"
	"testing"

	"home-os/mcp/internal/testutil"
)

// TestLoan_Category (#4702): loan_create/loan_update file a loan under a
// budget category by name (any case, created when new) or id, "" clears it,
// an unknown or other family's id is refused, and loan_find shows it.
func TestLoan_Category(t *testing.T) {
	f := setup(t, "UTC")
	_, other := testutil.SeedHousehold(t, f.pool)
	var theirs string
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO budget_categories (household_id, name) VALUES ($1, 'Housing') RETURNING id::text`,
		other.HouseholdID).Scan(&theirs); err != nil {
		t.Fatal(err)
	}
	housing := insertID(t, f.pool, `INSERT INTO budget_categories (household_id, name, monthly_amount) VALUES ($1, 'Housing', 2500) RETURNING id`,
		f.hh.HouseholdID)

	// By name, any case: the existing category.
	env := call(t, f.ctx, f.create, "loan_create", map[string]any{"name": "Mortgage", "original_amount": 300000.0,
		"remaining_balance": 250000.0, "monthly_payment": 1850.0, "category": " housing "})
	l := env.loan(t)
	if deref(l.BudgetCategoryID) != housing || deref(l.BudgetCategory) != "Housing" ||
		!strings.HasSuffix(env.Summary, "Filed under budget category Housing.") {
		t.Errorf("create by name = %+v %q", l, env.Summary)
	}

	// A new name creates it; the alias budget_category works.
	env = call(t, f.ctx, f.create, "loan_create", map[string]any{"name": "Car loan", "original_amount": 20000.0,
		"remaining_balance": 15000.0, "budget_category": "Transportation"})
	car := env.loan(t)
	if deref(car.BudgetCategory) != "Transportation" || car.BudgetCategoryID == nil || !strings.Contains(env.Summary, "Transportation (new)") {
		t.Errorf("create new = %+v %q", car, env.Summary)
	}

	// Update by id, then "" clears; other fields unchanged.
	env = call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": "Car loan", "category": housing})
	if u := env.loan(t); deref(u.BudgetCategoryID) != housing || u.RemainingBalance != "15000.00" ||
		!strings.HasPrefix(env.Summary, "Updated loan Car loan (category):") {
		t.Errorf("update by id = %+v %q", u, env.Summary)
	}
	if u := call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": car.ID, "category": ""}).loan(t); u.BudgetCategoryID != nil || u.BudgetCategory != nil {
		t.Errorf("clear = %+v", u)
	}

	// Another family's category id (or an unknown one) is refused.
	wantError(t, call(t, f.ctx, f.upd, "loan_update", map[string]any{"loan_id": car.ID, "category": theirs}), "category")
	wantError(t, call(t, f.ctx, f.create, "loan_create", map[string]any{"name": "HELOC", "original_amount": 1.0,
		"remaining_balance": 1.0, "category": theirs}), "category")
	wantError(t, call(t, f.ctx, f.create, "loan_create", map[string]any{"name": "HELOC", "original_amount": 1.0,
		"remaining_balance": 1.0, "category": strings.Repeat("x", 256)}), "category")

	// loan_find shows it.
	got := call(t, f.ctx, f.find, "loan_find", map[string]any{"id": l.ID}).loans(t)
	if len(got) != 1 || deref(got[0].BudgetCategory) != "Housing" {
		t.Errorf("loan_find = %+v", got)
	}
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM budget_categories WHERE household_id = $1`, f.hh.HouseholdID).Scan(&n); err != nil || n != 2 {
		t.Errorf("budget categories = %d (%v), want 2", n, err)
	}
}
