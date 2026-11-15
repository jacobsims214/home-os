package bill

import (
	"testing"
	"time"

	"home-os/mcp/internal/testutil"
)

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return d
}

// TestBillDue pins the read tools' due-date rule: this month or next, due_day
// clamped to the month's length, and a paid_date covering the current due
// date moves the bill on to the following one.
func TestBillDue(t *testing.T) {
	tests := []struct {
		name        string
		today       string
		dueDay      int
		paid        string // "" = never paid
		paidFor     string // stored paid_for_due_date; "" = NULL (inferred from paid)
		wantNext    string
		wantStatus  string
		wantPaidFor string // "" = nil
	}{
		{"31 in November is Nov 30", "2026-11-15", 31, "", "", "2026-11-30", statusDue, ""},
		{"31 on Nov 30 is today", "2026-11-30", 31, "", "", "2026-11-30", statusDue, ""},
		{"31 in February is Feb 28", "2027-02-10", 31, "", "", "2027-02-28", statusDue, ""},
		{"31 in leap February is Feb 29", "2028-02-10", 31, "", "", "2028-02-29", statusDue, ""},
		{"30 in leap February is Feb 29", "2028-02-29", 30, "", "", "2028-02-29", statusDue, ""},
		{"29 in February is Feb 28", "2027-02-01", 29, "", "", "2027-02-28", statusDue, ""},
		{"31 after Jan 31 is Feb 28", "2027-02-01", 31, "", "", "2027-02-28", statusDue, ""},
		{"passed this month wraps to next", "2026-11-29", 1, "", "", "2026-12-01", statusDue, ""},
		{"wraps the year", "2026-12-20", 5, "", "", "2027-01-05", statusDue, ""},
		{"due today is today", "2026-11-15", 15, "", "", "2026-11-15", statusDue, ""},

		{"paid early moves to next month", "2026-11-15", 20, "2026-11-14", "", "2026-12-20", statusPaid, "2026-11-20"},
		{"paid on the day moves on", "2026-11-20", 20, "2026-11-20", "", "2026-12-20", statusPaid, "2026-11-20"},
		{"paid Nov 30 for 31 moves to Dec 31", "2026-11-30", 31, "2026-11-30", "", "2026-12-31", statusPaid, "2026-11-30"},
		{"paid for Jan 31 moves to Feb 28", "2027-01-31", 31, "2027-01-31", "", "2027-02-28", statusPaid, "2027-01-31"},
		{"paid for Jan 31 moves to leap Feb 29", "2028-01-31", 31, "2028-01-31", "", "2028-02-29", statusPaid, "2028-01-31"},
		{"paid last month only: still due", "2026-11-15", 20, "2026-10-20", "", "2026-11-20", statusDue, "2026-10-20"},
		{"late payment in grace covers the past due date", "2026-11-25", 20, "2026-11-25", "", "2026-12-20", statusDue, "2026-11-20"},
		{"payment 8 days late pays the next one", "2026-11-28", 20, "2026-11-28", "", "2027-01-20", statusPaid, "2026-12-20"},

		{"stored: 8 days late for the past due date", "2026-11-28", 20, "2026-11-28", "2026-11-20", "2026-12-20", statusDue, "2026-11-20"},
		{"stored: wins over a payment in grace", "2026-11-15", 20, "2026-11-14", "2026-10-20", "2026-11-20", statusDue, "2026-10-20"},
		{"stored: next month paid ahead moves two on", "2026-11-15", 20, "2026-11-15", "2026-12-20", "2027-01-20", statusPaid, "2026-12-20"},
		{"stored: clamped Feb 28 for 31", "2027-03-01", 31, "2027-03-01", "2027-02-28", "2027-03-31", statusDue, "2027-02-28"},
		{"stored: without paid_date", "2026-11-15", 20, "", "2026-11-20", "2026-12-20", statusPaid, "2026-11-20"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var paid *time.Time
			if tc.paid != "" {
				p := mustDate(t, tc.paid)
				paid = &p
			}
			var paidFor *time.Time
			if tc.paidFor != "" {
				p := mustDate(t, tc.paidFor)
				paidFor = &p
			}
			got, _ := billDue(mustDate(t, tc.today), MonthlySchedule(tc.dueDay), paid, paidFor)
			if g := got.next.Format(time.DateOnly); g != tc.wantNext {
				t.Errorf("next = %s, want %s", g, tc.wantNext)
			}
			if got.status != tc.wantStatus {
				t.Errorf("status = %s, want %s", got.status, tc.wantStatus)
			}
			gotPaidFor := ""
			if got.paidFor != nil {
				gotPaidFor = got.paidFor.Format(time.DateOnly)
			}
			if gotPaidFor != tc.wantPaidFor {
				t.Errorf("paid_for = %q, want %q", gotPaidFor, tc.wantPaidFor)
			}
		})
	}
}

func decodeBills(t *testing.T, text string) []billData {
	t.Helper()
	var out []billData
	if err := unmarshalData(text, &out); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return out
}

func findBill(bills []billData, id string) *billData {
	for i := range bills {
		if bills[i].ID == id {
			return &bills[i]
		}
	}
	return nil
}

// TestReadTools_PaidBillAdvances pays a bill due today with bill_pay and
// checks bill_find due_within_days drops that due date and lists the next one, and
// bill_find reports it as paid with the next due date.
func TestReadTools_PaidBillAdvances(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	// A zone far from UTC so a DB-clock (CURRENT_DATE) answer would differ
	// from the household's today for half of every day.
	setHouseholdTZ(t, pool, hh.HouseholdID, "Pacific/Kiritimati")
	loc, err := time.LoadLocation("Pacific/Kiritimati")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	y, m, d := time.Now().In(loc).Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)

	id := insertPayBill(t, pool, hh.HouseholdID, ptr(80.0), ptr(today.Day()))
	idStr := id.String()
	wantNext := dueDateInMonth(y, m+1, today.Day()).Format(time.DateOnly)
	todayStr := today.Format(time.DateOnly)

	_, _, upcoming := NewBillFindTool(pool)
	_, _, list := NewBillFindTool(pool)
	_, _, pay := NewBillPayTool(pool)

	_, text := callTool(t, ctx, upcoming, "bill_find", map[string]any{"due_within_days": float64(7)})
	b := findBill(decodeBills(t, text), idStr)
	if b == nil {
		t.Fatalf("before pay: bill due today missing from bill_find due_within_days: %s", text)
	}
	if *b.NextDueDate != todayStr || *b.DaysUntilDue != 0 || b.Status != statusDue {
		t.Errorf("before pay: got next=%s days=%d status=%s, want %s 0 due", *b.NextDueDate, *b.DaysUntilDue, b.Status, todayStr)
	}

	paid := payBill(t, ctx, pay, map[string]any{"bill_id": idStr})
	if paid.PaidForDueDate == nil || *paid.PaidForDueDate != todayStr {
		t.Fatalf("bill_pay paid_for_due_date = %v, want %s", paid.PaidForDueDate, todayStr)
	}

	_, text = callTool(t, ctx, upcoming, "bill_find", map[string]any{"due_within_days": float64(7)})
	if findBill(decodeBills(t, text), idStr) != nil {
		t.Errorf("after pay: bill_find due_within_days (7 days) still lists the paid due date: %s", text)
	}

	_, text = callTool(t, ctx, upcoming, "bill_find", map[string]any{"due_within_days": float64(40)})
	b = findBill(decodeBills(t, text), idStr)
	if b == nil {
		t.Fatalf("after pay: bill_find due_within_days (40 days) missing the next due date: %s", text)
	}
	if *b.NextDueDate != wantNext || b.Status != statusPaid || b.PaidDate == nil || *b.PaidDate != todayStr {
		t.Errorf("after pay: got next=%s status=%s paid_date=%v, want %s paid %s", *b.NextDueDate, b.Status, b.PaidDate, wantNext, todayStr)
	}

	_, text = callTool(t, ctx, list, "bill_find", nil)
	b = findBill(decodeBills(t, text), idStr)
	if b == nil {
		t.Fatalf("bill_find missing bill: %s", text)
	}
	if *b.NextDueDate != wantNext || b.Status != statusPaid || b.PaidForDueDate == nil || *b.PaidForDueDate != todayStr {
		t.Errorf("bill_find: got next=%s status=%s paid_for=%v, want %s paid %s", *b.NextDueDate, b.Status, b.PaidForDueDate, wantNext, todayStr)
	}
	if want := daysBetween(today, mustDate(t, wantNext)); *b.DaysUntilDue != want {
		t.Errorf("bill_find days_until_due = %d, want %d", *b.DaysUntilDue, want)
	}
}

// TestBillFind_MonthEndClamp checks a due_day 31 bill is reported on
// this month's last day, not overflowed into next month, and that the window
// is inclusive of today and of today+days.
func TestBillFind_MonthEndClamp(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	y, m, d := time.Now().UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)

	id := insertPayBill(t, pool, hh.HouseholdID, ptr(10.0), ptr(31))
	want := nextDueDate(today, 31)
	wantDays := daysBetween(today, want)
	if want.Month() != m {
		// Only possible if the clamp is wrong: the last day of this month is
		// always on or after today.
		t.Fatalf("nextDueDate(%s, 31) = %s, outside this month", today.Format(time.DateOnly), want.Format(time.DateOnly))
	}

	_, _, upcoming := NewBillFindTool(pool)
	_, _, list := NewBillFindTool(pool)

	// Exactly wantDays ahead (minimum 1) is inside the window.
	days := wantDays
	if days < 1 {
		days = 1
	}
	_, text := callTool(t, ctx, upcoming, "bill_find", map[string]any{"due_within_days": float64(days)})
	b := findBill(decodeBills(t, text), id.String())
	if b == nil {
		t.Fatalf("due_day 31 bill due %s (in %d days) missing with days=%d: %s", want.Format(time.DateOnly), wantDays, days, text)
	}
	if *b.NextDueDate != want.Format(time.DateOnly) || *b.DaysUntilDue != wantDays {
		t.Errorf("got next=%s days=%d, want %s %d", *b.NextDueDate, *b.DaysUntilDue, want.Format(time.DateOnly), wantDays)
	}

	if wantDays > 1 {
		_, text = callTool(t, ctx, upcoming, "bill_find", map[string]any{"due_within_days": float64(wantDays - 1)})
		if findBill(decodeBills(t, text), id.String()) != nil {
			t.Errorf("bill due in %d days listed with days=%d: %s", wantDays, wantDays-1, text)
		}
	}

	_, text = callTool(t, ctx, list, "bill_find", nil)
	b = findBill(decodeBills(t, text), id.String())
	if b == nil || *b.NextDueDate != want.Format(time.DateOnly) || b.Status != statusDue || b.PaidDate != nil {
		t.Errorf("bill_find: got %+v, want next %s, status due, no paid_date", b, want.Format(time.DateOnly))
	}
}

// TestBillFind_LegacyNullDueDay checks a bill without due_day is listed
// with null due-date fields and never appears in bill_find due_within_days.
func TestBillFind_LegacyNullDueDay(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	id := insertPayBill(t, pool, hh.HouseholdID, nil, nil)

	_, _, list := NewBillFindTool(pool)
	_, text := callTool(t, ctx, list, "bill_find", nil)
	b := findBill(decodeBills(t, text), id.String())
	if b == nil {
		t.Fatalf("bill_find missing legacy bill: %s", text)
	}
	if b.NextDueDate != nil || b.DaysUntilDue != nil || b.Status != "" {
		t.Errorf("legacy bill due fields = %v %v %q, want null", b.NextDueDate, b.DaysUntilDue, b.Status)
	}

	_, _, upcoming := NewBillFindTool(pool)
	_, text = callTool(t, ctx, upcoming, "bill_find", map[string]any{"due_within_days": float64(60)})
	if findBill(decodeBills(t, text), id.String()) != nil {
		t.Errorf("legacy bill listed in bill_find due_within_days: %s", text)
	}
}
