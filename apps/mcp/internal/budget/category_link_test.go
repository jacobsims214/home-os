package budget

import (
	"context"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/bill"
	"home-os/mcp/internal/loan"
	"home-os/mcp/internal/testutil"
)

// TestLoad_CategoryFromWriteTools (#4702): a bill and a loan filed under a
// budget category by bill_create / loan_create are grouped under it by
// budget_summary's budget section, not under uncategorized.
func TestLoad_CategoryFromWriteTools(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, _, billCreate := bill.NewBillCreateTool(pool)
	_, _, loanCreate := loan.NewLoanCreateTool(pool)
	run := func(name string, h toolHandler, args map[string]any) {
		t.Helper()
		var req mcp.CallToolRequest
		req.Params.Name, req.Params.Arguments = name, args
		res, err := h(ctx, req)
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
	}
	run("bill_create", billCreate, map[string]any{"name": "Electric", "amount": 120.0, "due_day": 5.0, "budget_category": "Utilities"})
	run("loan_create", loanCreate, map[string]any{"name": "Mortgage", "original_amount": 300000.0, "remaining_balance": 250000.0,
		"monthly_payment": 1850.0, "due_day": 1.0, "category": "Housing"})

	now := time.Now().UTC()
	r, err := Load(context.Background(), pool, hh.HouseholdID.String(), time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got := lineNames(group(t, r, "Utilities")); got != "bill:Electric" {
		t.Errorf("Utilities lines = %s", got)
	}
	if got := lineNames(group(t, r, "Housing")); got != "loan:Mortgage" {
		t.Errorf("Housing lines = %s", got)
	}
	if r.Uncategorized.LineCount != 0 {
		t.Errorf("uncategorized = %+v", r.Uncategorized)
	}
}
