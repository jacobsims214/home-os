package bill

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/testutil"
)

// #5514: bill_pay writes the payment LEDGER, and bills.paid_date /
// bills.paid_for_due_date stay the derived cache of the latest payment.
//
// The point of these tests is the thing the old schema could not express: a
// bill holding TWO payments. bills.paid_date can only ever hold one, so before
// 090 the first payment was destroyed by the second and "how much did we pay
// this bill" was unanswerable past the latest one.

// ledgerRow is one bill_payments row as these tests read it back.
type ledgerRow struct {
	Amount     *float64
	PaidOn     string
	PaidForDue *string
	Method     *string
	Reconstr   bool
}

// ledger reads a bill's payments, newest first.
func ledger(t *testing.T, pool *pgxpool.Pool, billID uuid.UUID) []ledgerRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT amount, paid_on, paid_for_due_date, method, is_reconstructed
		   FROM bill_payments WHERE bill_id = $1 ORDER BY paid_on DESC`, billID)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	defer rows.Close()
	var out []ledgerRow
	for rows.Next() {
		var r ledgerRow
		var paidOn time.Time
		var paidFor *time.Time
		if err := rows.Scan(&r.Amount, &paidOn, &paidFor, &r.Method, &r.Reconstr); err != nil {
			t.Fatalf("scan ledger: %v", err)
		}
		r.PaidOn = paidOn.Format(time.DateOnly)
		if paidFor != nil {
			s := paidFor.Format(time.DateOnly)
			r.PaidForDue = &s
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return out
}

// TestBillPay_WritesExactlyOneLedgerRow is criterion 3 on the MCP path: paying
// a bill writes one bill_payments row — not zero, not two — and the row is
// marked as OBSERVED, not reconstructed.
func TestBillPay_WritesExactlyOneLedgerRow(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	id := insertPayBill(t, pool, hh.HouseholdID, ptr(80.0), ptr(15))

	_, _, pay := NewBillPayTool(pool)
	got := payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": "2026-01-14"})
	if got.AlreadyPaid == nil || *got.AlreadyPaid {
		t.Fatalf("first payment reported already_paid; want it recorded")
	}

	rows := ledger(t, pool, id)
	if len(rows) != 1 {
		t.Fatalf("ledger rows = %d, want exactly 1: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.PaidOn != "2026-01-14" {
		t.Errorf("paid_on = %q, want 2026-01-14", r.PaidOn)
	}
	// due_day 15, paid the 14th: at most 7 days before, so it pays the 15th.
	if r.PaidForDue == nil || *r.PaidForDue != "2026-01-15" {
		t.Errorf("paid_for_due_date = %v, want 2026-01-15", r.PaidForDue)
	}
	// No amount argument: the tool's documented default is the bill's amount.
	if r.Amount == nil || *r.Amount != 80.0 {
		t.Errorf("amount = %v, want the bill's 80.00", r.Amount)
	}
	if r.Reconstr {
		t.Error("is_reconstructed = true for a payment bill_pay recorded; want false")
	}
	if r.Method != nil {
		t.Errorf("method = %q; bill_pay has no method argument and must not invent one", *r.Method)
	}
	// The cache moved in the same transaction, so the two cannot disagree.
	if d := storedPaidDate(t, pool, id); d != "2026-01-14" {
		t.Errorf("bills.paid_date = %q, want 2026-01-14 (the cache)", d)
	}
	if d := storedPaidFor(t, pool, id); d != "2026-01-15" {
		t.Errorf("bills.paid_for_due_date = %q, want 2026-01-15 (the cache)", d)
	}
}

// TestBillPay_ExplicitAmountIsWhatWasPaid checks the amount recorded is the
// one the caller said was paid, which is the whole reason bills.amount (what
// was owed) is not enough: a payment that differs from the bill is exactly the
// case that has to be recognisable afterwards.
func TestBillPay_ExplicitAmountIsWhatWasPaid(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	id := insertPayBill(t, pool, hh.HouseholdID, ptr(314.0), ptr(20))

	_, _, pay := NewBillPayTool(pool)
	payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": "2026-01-20", "amount": 341.0})

	rows := ledger(t, pool, id)
	if len(rows) != 1 {
		t.Fatalf("ledger rows = %d, want 1", len(rows))
	}
	if rows[0].Amount == nil || *rows[0].Amount != 341.0 {
		t.Errorf("amount = %v, want the 341.00 that was paid, not the 314.00 owed", rows[0].Amount)
	}
	// And the bill still says what is owed. A payment does not rewrite the bill.
	var owed *float64
	if err := pool.QueryRow(context.Background(), `SELECT amount FROM bills WHERE id = $1`, id).Scan(&owed); err != nil {
		t.Fatalf("select amount: %v", err)
	}
	if owed == nil || *owed != 314.0 {
		t.Errorf("bills.amount = %v, want 314.00 unchanged (a payment is not a new price)", owed)
	}
}

// TestBillPay_LegacyNullAmountIsUnknownNotZero: a bill with no amount at all
// still gets its payment recorded, with a NULL amount. An invented 0.00 would
// read as "paid, and it cost nothing" and would make every spend total wrong in
// the quiet direction; NULL says what is true — nobody knows how much.
func TestBillPay_LegacyNullAmountIsUnknownNotZero(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO bills (household_id, name, is_autopay) VALUES ($1, 'Old', false) RETURNING id`,
		hh.HouseholdID,
	).Scan(&id); err != nil {
		t.Fatalf("insert bill: %v", err)
	}

	_, _, pay := NewBillPayTool(pool)
	payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": "2026-01-10"})

	rows := ledger(t, pool, id)
	if len(rows) != 1 {
		t.Fatalf("ledger rows = %d, want 1 (the payment happened, even if the amount is unknown)", len(rows))
	}
	if rows[0].Amount != nil {
		t.Errorf("amount = %v, want NULL: unknown is not zero", *rows[0].Amount)
	}
}

// TestBillPay_RepeatWritesNoSecondRow: paying for a due date that is already
// covered is a no-op, and a no-op writes no event. A ledger that recorded a
// payment that did not happen would be worse than no ledger.
func TestBillPay_RepeatWritesNoSecondRow(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	id := insertPayBill(t, pool, hh.HouseholdID, ptr(80.0), ptr(15))

	_, _, pay := NewBillPayTool(pool)
	payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": "2026-01-14"})
	got := payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": "2026-01-16"})
	if got.AlreadyPaid == nil || !*got.AlreadyPaid {
		t.Fatalf("second payment for the same due date was not reported already_paid")
	}
	if rows := ledger(t, pool, id); len(rows) != 1 {
		t.Fatalf("ledger rows after a repeat = %d, want 1: %+v", len(rows), rows)
	}
}

// TestBillPay_TwoPaymentsOnOneBill is criteria 2 and 5: the old schema could
// not express this at all.
//
// Two payments for two different due cycles both survive. bills.paid_date and
// bills.paid_for_due_date reflect the LATER one, because that is what the
// cache means and what every existing reader needs. And "how much did we pay
// this bill this year" is ONE query over the ledger — no activity_log, no
// before/after inference.
func TestBillPay_TwoPaymentsOnOneBill(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	id := insertPayBill(t, pool, hh.HouseholdID, ptr(80.0), ptr(15))

	_, _, pay := NewBillPayTool(pool)
	// November's cycle, then December's. The dates are in the past relative to
	// any clock this test could run on only if they are before today; bill_pay
	// refuses a future paid_date, so use two past dates in the same month pair
	// the bill's schedule actually has.
	today := todayIn(t, pool, hh.HouseholdID)
	// Two distinct cycles: last month's and this month's due date.
	c, ok := cadence(Schedule{Frequency: FrequencyMonthly, DueDay: ptr(15)})
	if !ok {
		t.Fatal("monthly schedule with due_day 15 has no cadence")
	}
	thisDue, _ := c.OnOrBefore(today)
	prevDue, _ := c.OnOrBefore(thisDue.AddDate(0, 0, -1))

	// Payment 1: the previous cycle, made one day before it fell due.
	if err := insertPaymentDirect(t, pool, hh.HouseholdID, id, 80.0, prevDue.AddDate(0, 0, -1), prevDue); err != nil {
		t.Fatalf("seed first payment: %v", err)
	}
	// Move the cache to the first payment, as bill_pay would have.
	setCache(t, pool, id, prevDue.AddDate(0, 0, -1), prevDue)

	// Payment 2 through the tool: this month's cycle.
	got := payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": thisDue.Format(time.DateOnly)})
	if got.AlreadyPaid == nil || *got.AlreadyPaid {
		t.Fatalf("paying the next cycle was reported already_paid; the ledger must accept a new cycle")
	}

	rows := ledger(t, pool, id)
	if len(rows) != 2 {
		t.Fatalf("ledger rows = %d, want 2 (the old schema could express ONE): %+v", len(rows), rows)
	}
	// Newest first.
	if rows[0].PaidOn != thisDue.Format(time.DateOnly) || rows[1].PaidOn != prevDue.AddDate(0, 0, -1).Format(time.DateOnly) {
		t.Errorf("ledger dates = %q, %q; want %q then %q",
			rows[0].PaidOn, rows[1].PaidOn,
			thisDue.Format(time.DateOnly), prevDue.AddDate(0, 0, -1).Format(time.DateOnly))
	}
	// The cache reflects the LATER payment.
	if d := storedPaidDate(t, pool, id); d != thisDue.Format(time.DateOnly) {
		t.Errorf("bills.paid_date = %q, want the later payment %q", d, thisDue.Format(time.DateOnly))
	}
	if d := storedPaidFor(t, pool, id); d != thisDue.Format(time.DateOnly) {
		t.Errorf("bills.paid_for_due_date = %q, want %q", d, thisDue.Format(time.DateOnly))
	}

	// Criterion 5: the sum, in ONE query, straight off the ledger.
	var sum float64
	if err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(amount), 0) FROM bill_payments
		  WHERE household_id = $1 AND paid_on >= date_trunc('year', CURRENT_DATE)
		    AND paid_on < date_trunc('year', CURRENT_DATE) + interval '1 year'`,
		hh.HouseholdID,
	).Scan(&sum); err != nil {
		t.Fatalf("sum payments: %v", err)
	}
	// Both payments are 80.00 and both are inside this year by construction.
	if sum != 160.0 {
		t.Errorf("sum of this year's payments = %v, want 160.00 (both payments, which bills.paid_date alone cannot show)", sum)
	}

	// And what the old schema would have said, for the record: one payment.
	var cacheCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM bills WHERE id = $1 AND paid_date IS NOT NULL`, id).Scan(&cacheCount); err != nil {
		t.Fatalf("count cache: %v", err)
	}
	if cacheCount != 1 {
		t.Errorf("cache rows = %d, want 1: the cache is ONE value, which is exactly the defect", cacheCount)
	}
}

// TestBillPay_NoAmountArgOnAmountlessBillRecordsNull covers the COALESCE's
// other arm: no amount argument and no bill amount leaves NULL, not a Go zero.
func TestBillPay_NoAmountArgOnAmountlessBillRecordsNull(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO bills (household_id, name, is_autopay) VALUES ($1, 'Old', false) RETURNING id`,
		hh.HouseholdID,
	).Scan(&id); err != nil {
		t.Fatalf("insert bill: %v", err)
	}
	_, _, pay := NewBillPayTool(pool)
	// A bill with no due_day: no schedule, so no coverage to record.
	payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": "2026-02-01"})

	rows := ledger(t, pool, id)
	if len(rows) != 1 {
		t.Fatalf("ledger rows = %d, want 1", len(rows))
	}
	if rows[0].Amount != nil || rows[0].PaidForDue != nil {
		t.Errorf("amount = %v, paid_for_due_date = %v; want NULL for both (no amount, no schedule)",
			rows[0].Amount, rows[0].PaidForDue)
	}
}

// TestBillPay_OtherHouseholdWritesNoLedgerRow: the tenancy rule holds on the
// ledger too. A refused payment must leave no row anywhere.
func TestBillPay_OtherHouseholdWritesNoLedgerRow(t *testing.T) {
	pool := testutil.NewPool(t)
	_, owner := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)
	id := insertPayBill(t, pool, owner.HouseholdID, ptr(80.0), ptr(15))

	_, _, pay := NewBillPayTool(pool)
	res, _ := callTool(t, otherCtx, pay, "bill_pay", map[string]any{"bill_id": id.String(), "paid_date": "2026-01-10"})
	if !res.IsError {
		t.Fatalf("another household's bill_pay succeeded")
	}
	if rows := ledger(t, pool, id); len(rows) != 0 {
		t.Errorf("ledger rows = %d for a refused payment, want 0", len(rows))
	}
}

// --- helpers ---

// todayIn is the household's today, as bill_pay computes it.
func todayIn(t *testing.T, pool *pgxpool.Pool, householdID uuid.UUID) time.Time {
	t.Helper()
	var d time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT (NOW() AT TIME ZONE timezone)::date FROM households WHERE id = $1`,
		householdID,
	).Scan(&d); err != nil {
		t.Fatalf("household today: %v", err)
	}
	return d
}

// insertPaymentDirect writes a ledger row the way an earlier payment would have
// been recorded, without going through the tool (which refuses a repeat for a
// covered cycle). It is the test's stand-in for "this bill was already paid last
// month".
func insertPaymentDirect(t *testing.T, pool *pgxpool.Pool, householdID, billID uuid.UUID, amount float64, paidOn, paidFor time.Time) error {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO bill_payments (household_id, bill_id, amount, paid_on, paid_for_due_date)
		 VALUES ($1, $2, $3, $4, $5)`,
		householdID, billID, amount, paidOn, paidFor)
	if err != nil {
		return fmt.Errorf("insert payment: %w", err)
	}
	return nil
}

// setCache sets the two derived-cache columns, as bill_pay's UPDATE does.
func setCache(t *testing.T, pool *pgxpool.Pool, billID uuid.UUID, paidOn, paidFor time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE bills SET paid_date = $2, paid_for_due_date = $3 WHERE id = $1`,
		billID, paidOn, paidFor); err != nil {
		t.Fatalf("set cache: %v", err)
	}
}
