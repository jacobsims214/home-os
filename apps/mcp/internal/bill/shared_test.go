package bill

import (
	"testing"
	"time"

	"home-os/billdue"
)

// TestDueRuleIsTheSharedOne: this package has no due-date logic of its own
// (#4819). Its Schedule/Due aliases and BillDueFor must be the shared rule in
// home-os/billdue, which the API's GET /api/v1/budget/due-soon calls too, so
// the two cannot drift.
func TestDueRuleIsTheSharedOne(t *testing.T) {
	today := mustDate(t, "2026-11-15")
	day := 20
	s := Schedule{Frequency: FrequencyMonthly, DueDay: &day}

	// A type assertion through the shared package: this fails to compile if
	// Schedule stops being billdue.Schedule.
	var shared billdue.Schedule = s
	if _, ok := any(shared).(Schedule); !ok {
		t.Fatal("bill.Schedule is not billdue.Schedule")
	}

	got, ok := BillDueFor(today, s, nil, nil)
	if !ok || got.Next.Format(time.DateOnly) != "2026-11-20" {
		t.Fatalf("BillDueFor = %v %v, want 2026-11-20", got.Next, ok)
	}
	// Same answer as asking the shared package directly.
	want, _ := billdue.BillDueFor(today, shared, nil, nil)
	if !got.Next.Equal(want.Next) || got.Status != want.Status {
		t.Errorf("bill.BillDueFor = %v/%s, billdue.BillDueFor = %v/%s", got.Next, got.Status, want.Next, want.Status)
	}
	if statusDue != billdue.StatusDue || statusPaid != billdue.StatusPaid {
		t.Errorf("status constants drifted: %q/%q vs %q/%q", statusDue, statusPaid, billdue.StatusDue, billdue.StatusPaid)
	}
	// The loan rule and the bill rule are one rule for a monthly schedule.
	if got := billdue.NextMonthlyDue(today, 31); !got.Equal(billdue.NextDueDate(today, 31)) {
		t.Errorf("NextMonthlyDue(%s, 31) = %s, want NextDueDate's %s", today.Format(time.DateOnly), got.Format(time.DateOnly), billdue.NextDueDate(today, 31).Format(time.DateOnly))
	}
}
