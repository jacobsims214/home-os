package bill

// #5514, criterion 3 on the API side. The criterion says "paying a bill through
// the API writes exactly one bill_payments row". **The API has no payment
// path**: it exposes List, Create, Get, Update and Delete (apps/api/cmd/api/
// routes.go), and Update's SQL never sets paid_date — the only writer of
// paid_date in the whole tree is the MCP bill_pay. Rather than invent an
// endpoint to satisfy a criterion that assumed one exists, this test pins the
// fact so the next person does not go looking for a path that is not there, and
// so the day someone adds one the ledger is already the thing it has to write.
//
// Requires TEST_DATABASE_URL; skipped otherwise. Run with -p 1.

import (
	"context"
	"testing"
)

// TestBillUpdateWritesNoLedgerRow proves the API's bill update leaves the
// ledger alone. It is the correct behaviour today (the API does not record
// payments), and it is the assertion that would fail loudly if an API payment
// path were added without writing the ledger.
func TestBillUpdateWritesNoLedgerRow(t *testing.T) {
	pool := testPool(t)
	household, _ := newHousehold(t, pool, false)
	repo := NewRepo(pool)
	ctx := context.Background()

	b := &Bill{HouseholdID: household, Name: "Electric", Frequency: FrequencyMonthly, DueDay: intPtr(15)}
	if err := b.Amount.Scan("80.00"); err != nil {
		t.Fatalf("scan amount: %v", err)
	}
	created, err := repo.Create(ctx, b)
	if err != nil {
		t.Fatalf("create bill: %v", err)
	}

	// An update that touches the money and the schedule — the fields that make
	// billdue re-infer coverage. It must still write no payment.
	created.Name = "Electric (budget plan)"
	if err := created.Amount.Scan("95.00"); err != nil {
		t.Fatalf("scan new amount: %v", err)
	}
	dueDay := 18
	created.DueDay = &dueDay
	if _, err := repo.Update(ctx, created); err != nil {
		t.Fatalf("update bill: %v", err)
	}

	var rows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM bill_payments WHERE bill_id = $1`, created.ID).Scan(&rows); err != nil {
		t.Fatalf("count ledger rows: %v", err)
	}
	if rows != 0 {
		t.Errorf("bill_payments rows after an API bill update = %d, want 0: the API does not record payments, "+
			"and a payment row that no payment produced would be worse than none", rows)
	}
	// And the cache columns are untouched, which is the other half of the same
	// fact: nothing at all was recorded as paid.
	var paidDate, paidFor *string
	if err := pool.QueryRow(ctx,
		`SELECT paid_date::text, paid_for_due_date::text FROM bills WHERE id = $1`, created.ID,
	).Scan(&paidDate, &paidFor); err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if paidDate != nil || paidFor != nil {
		t.Errorf("bills.paid_date = %v, paid_for_due_date = %v after an update; want both NULL", paidDate, paidFor)
	}
}

// TestBillReadStillUsesTheCache is the "did not switch the readers" guard on
// the API side. If a payment exists in the ledger but the cache says otherwise,
// the API must report the CACHE's value, because that is what the ticket kept
// in place for this release. When the follow-up switches packages/billdue over
// to the ledger, this test is the one that has to be rewritten — deliberately,
// by that ticket, with the change in front of it. Keeping it green by accident
// is not possible: the two values differ by construction.
func TestBillReadStillUsesTheCache(t *testing.T) {
	pool := testPool(t)
	household, _ := newHousehold(t, pool, false)
	repo := NewRepo(pool)
	ctx := context.Background()

	b := &Bill{HouseholdID: household, Name: "Mortgage", Frequency: FrequencyMonthly, DueDay: intPtr(1)}
	if err := b.Amount.Scan("1850.00"); err != nil {
		t.Fatalf("scan amount: %v", err)
	}
	created, err := repo.Create(ctx, b)
	if err != nil {
		t.Fatalf("create bill: %v", err)
	}

	// A payment in the LEDGER on 2026-10-02 covering 2026-10-01 …
	if _, err := pool.Exec(ctx,
		`INSERT INTO bill_payments (household_id, bill_id, amount, paid_on, paid_for_due_date)
		 VALUES ($1, $2, 1850.00, '2026-10-02', '2026-10-01')`, household, created.ID); err != nil {
		t.Fatalf("insert ledger row: %v", err)
	}
	// … and a DIFFERENT pair in the cache, as a later payment would leave it.
	if _, err := pool.Exec(ctx,
		`UPDATE bills SET paid_date = '2026-11-02', paid_for_due_date = '2026-11-01' WHERE id = $1`,
		created.ID); err != nil {
		t.Fatalf("set cache: %v", err)
	}

	got, err := repo.Get(ctx, created.ID, household)
	if err != nil {
		t.Fatalf("get bill: %v", err)
	}
	if got == nil {
		t.Fatal("get bill: not found")
	}
	if got.PaidDate == nil || got.PaidDate.Format("2006-01-02") != "2026-11-02" {
		t.Errorf("PaidDate = %v, want the CACHE's 2026-11-02: the ledger is not the reader yet (#5514)", got.PaidDate)
	}
	if got.PaidForDueDate == nil || got.PaidForDueDate.Format("2006-01-02") != "2026-11-01" {
		t.Errorf("PaidForDueDate = %v, want the CACHE's 2026-11-01: the ledger is not the reader yet (#5514)", got.PaidForDueDate)
	}
}
