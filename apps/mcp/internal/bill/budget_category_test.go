package bill

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/testutil"
)

// storedBudgetCategory reads bills.budget_category_id as text ("<nil>" for NULL).
func storedBudgetCategory(t *testing.T, pool *pgxpool.Pool, billID string) string {
	t.Helper()
	var id *string
	if err := pool.QueryRow(context.Background(), `SELECT budget_category_id::text FROM bills WHERE id = $1`, billID).Scan(&id); err != nil {
		t.Fatalf("read budget_category_id: %v", err)
	}
	return strOrNil(id)
}

func countBudgetCategories(t *testing.T, pool *pgxpool.Pool, hh uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM budget_categories WHERE household_id = $1`, hh).Scan(&n); err != nil {
		t.Fatalf("count budget categories: %v", err)
	}
	return n
}

// TestBill_BudgetCategory (#4702): bill_create/bill_update file a bill under
// a budget category by name (resolved any case, created when new) or id; a
// bill's own category only links an existing budget category of that name;
// "" clears; another family's category id is refused.
func TestBill_BudgetCategory(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, other := testutil.SeedHousehold(t, pool)
	_, _, create := NewBillCreateTool(pool)
	_, _, update := NewBillUpdateTool(pool)

	var utilities, theirs string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO budget_categories (household_id, name, monthly_amount) VALUES ($1, 'Utilities', 400) RETURNING id::text`,
		hh.HouseholdID).Scan(&utilities); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO budget_categories (household_id, name) VALUES ($1, 'Housing') RETURNING id::text`,
		other.HouseholdID).Scan(&theirs); err != nil {
		t.Fatal(err)
	}

	// The bill's own category names an existing budget category: linked.
	res, text := callTool(t, ctx, create, "bill_create", map[string]any{"name": "Electric", "amount": 120.0, "due_day": 5.0, "category": "utilities"})
	if res.IsError {
		t.Fatalf("create: %s", text)
	}
	b := decodeUpdated(t, text)
	if strOrNil(b.BudgetCategoryID) != utilities || strOrNil(b.BudgetCategory) != "Utilities" || strOrNil(b.Category) != "utilities" {
		t.Errorf("category fallback = %s", text)
	}
	if !strings.Contains(text, "Filed under budget category Utilities.") {
		t.Errorf("summary lacks budget category: %s", text)
	}

	// A bill category with no such budget category creates none.
	before := countBudgetCategories(t, pool, hh.HouseholdID)
	res, text = callTool(t, ctx, create, "bill_create", map[string]any{"name": "Netflix", "amount": 15.0, "due_day": 9.0, "category": "Subscription"})
	if res.IsError {
		t.Fatalf("create: %s", text)
	}
	netflix := decodeUpdated(t, text)
	if netflix.BudgetCategoryID != nil || countBudgetCategories(t, pool, hh.HouseholdID) != before {
		t.Errorf("free-text category made a budget category: %s", text)
	}

	// budget_category by a new name creates it (spacing collapsed).
	res, text = callTool(t, ctx, create, "bill_create", map[string]any{"name": "Disney+", "amount": 10.0, "due_day": 3.0,
		"category": "Subscription", "budget_category": "  Fun   money "})
	if res.IsError {
		t.Fatalf("create: %s", text)
	}
	disney := decodeUpdated(t, text)
	if strOrNil(disney.BudgetCategory) != "Fun money" || disney.BudgetCategoryID == nil ||
		!strings.Contains(text, "budget category Fun money (new)") {
		t.Errorf("new budget category = %s", text)
	}

	// bill_update: by name (any case) reuses it; the alias budget works too.
	res, text = callTool(t, ctx, update, "bill_update", map[string]any{"bill_id": netflix.ID, "budget": "FUN MONEY"})
	if res.IsError {
		t.Fatalf("update: %s", text)
	}
	if got := decodeUpdated(t, text); strOrNil(got.BudgetCategoryID) != strOrNil(disney.BudgetCategoryID) ||
		!strings.Contains(text, "(budget_category)") {
		t.Errorf("update by name = %s", text)
	}
	// By id.
	res, text = callTool(t, ctx, update, "bill_update", map[string]any{"bill_id": netflix.ID, "budget_category_id": utilities})
	if res.IsError || storedBudgetCategory(t, pool, netflix.ID) != utilities {
		t.Errorf("update by id = %s", text)
	}
	// Another family's id: refused, nothing changes.
	res, text = callTool(t, ctx, update, "bill_update", map[string]any{"bill_id": netflix.ID, "budget_category": theirs})
	if !res.IsError || !strings.Contains(text, `"field":"budget_category"`) || storedBudgetCategory(t, pool, netflix.ID) != utilities {
		t.Errorf("other family's category = %s", text)
	}
	// Changing only the bill's own category leaves the budget category.
	if res, text = callTool(t, ctx, update, "bill_update", map[string]any{"bill_id": netflix.ID, "category": "Streaming"}); res.IsError ||
		storedBudgetCategory(t, pool, netflix.ID) != utilities {
		t.Errorf("category-only update = %s", text)
	}
	// "" clears.
	res, text = callTool(t, ctx, update, "bill_update", map[string]any{"bill_id": netflix.ID, "budget_category": ""})
	if got := decodeUpdated(t, text); res.IsError || got.BudgetCategoryID != nil || got.BudgetCategory != nil ||
		storedBudgetCategory(t, pool, netflix.ID) != "<nil>" {
		t.Errorf("clear = %s", text)
	}
	if n := countBudgetCategories(t, pool, hh.HouseholdID); n != 2 {
		t.Errorf("budget categories = %d, want 2 (Utilities, Fun money)", n)
	}

	// Too long a name is a field error.
	res, text = callTool(t, ctx, create, "bill_create", map[string]any{"name": "X", "amount": 1.0, "due_day": 1.0,
		"budget_category": strings.Repeat("x", 256)})
	if !res.IsError || !strings.Contains(text, `"field":"budget_category"`) {
		t.Errorf("long name = %s", text)
	}
}
