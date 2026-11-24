package billdue

// #5498: the "is this bill on me?" rule. These tests are the rule's own
// spelling: the API's bill response, MCP bill_find and the notification policy
// (#5500) all call AttentionFor, so a case missing here is a case three
// callers get wrong together.

import (
	"testing"
	"time"
)

func TestAttentionFor(t *testing.T) {
	// 21 Nov 2026. A monthly bill due on the 5th is past due (last due date
	// 5 Nov, not covered) or not, depending on paidDate.
	today := time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC)
	// A bill that has been on the books since the spring: every due date before
	// today has already happened while it existed.
	established := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	// A bill added this month. Its "last due date" (24 Oct for a 24th bill)
	// is before the row existed, so it cannot be late over it: this is the
	// ordinary case of entering a bill you have been paying for years.
	recent := time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)
	monthly5 := MonthlySchedule(5)
	// Due in 3 days: 24 Nov.
	monthly24 := MonthlySchedule(24)
	// A once bill whose date has passed (10 Nov).
	pastOnce := Schedule{Frequency: FrequencyOnce, DueDate: optDate(t, "2026-11-10")}
	// A bill with no usable schedule at all (monthly, no due_day).
	noSchedule := Schedule{Frequency: FrequencyMonthly}

	tests := []struct {
		name       string
		schedule   Schedule
		paidDate   *time.Time
		paidFor    *time.Time
		createdAt  time.Time
		autopay    bool
		hasAccount bool
		want       string
	}{
		// automatic: autopay on AND an account set. The state the notifier
		// stays quiet for.
		{"autopay with an account", monthly24, nil, nil, recent, true, true, AttentionAutomatic},
		{"autopay with an account, due today", MonthlySchedule(21), nil, nil, recent, true, true, AttentionAutomatic},
		{"autopay with an account, paid", monthly5, optDate(t, "2026-11-05"), nil, recent, true, true, AttentionAutomatic},

		// needs_setup: the trap case. Nothing automatic will pay it and
		// nothing says where the money would come from.
		{"manual, no account (the trap)", monthly24, nil, nil, recent, false, false, AttentionNeedsSetup},
		{"manual, no account, far out", MonthlySchedule(28), nil, nil, recent, false, false, AttentionNeedsSetup},
		{"manual, no account, paid", monthly5, optDate(t, "2026-11-05"), nil, recent, false, false, AttentionNeedsSetup},
		{"autopay on but no account", monthly24, nil, nil, recent, true, false, AttentionNeedsSetup},
		{"manual, no account, no schedule", noSchedule, nil, nil, recent, false, false, AttentionNeedsSetup},

		// due_soon: a person pays it, an account is on record.
		{"manual with an account, due this week", monthly24, nil, nil, recent, false, true, AttentionDueSoon},
		{"manual with an account, due today", MonthlySchedule(21), nil, nil, recent, false, true, AttentionDueSoon},
		{"manual with an account, far out", MonthlySchedule(28), nil, nil, recent, false, true, AttentionDueSoon},
		{"manual with an account, paid for this one", monthly5, optDate(t, "2026-11-05"), nil, recent, false, true, AttentionDueSoon},

		// overdue: past due and unpaid. Outranks the configuration, because
		// the money is already late.
		{"manual, overdue, no account", monthly5, nil, nil, established, false, false, AttentionOverdue},
		{"manual, overdue, account set", monthly5, nil, nil, established, false, true, AttentionOverdue},
		{"autopay that did not pay, overdue", monthly5, nil, nil, established, true, true, AttentionOverdue},
		{"once bill past its date, unpaid", pastOnce, nil, nil, established, false, false, AttentionOverdue},
		// ...and paying it clears it.
		{"once bill past its date, paid", pastOnce, optDate(t, "2026-11-10"), nil, established, false, false, AttentionNeedsSetup},
		{"paid for an older date is still late", monthly5, optDate(t, "2026-09-05"), nil, established, false, true, AttentionOverdue},
		// paid_for_due_date wins over paid_date: it says which date the money
		// covered, so a late payment of the November date is not overdue.
		{"late payment that named its due date", monthly5, optDate(t, "2026-11-12"), optDate(t, "2026-11-05"), established, false, true, AttentionDueSoon},
		// The import case: an old bill entered today is not "late" over a due
		// date that passed before anyone recorded it. It is still on the
		// family (needs_setup / due_soon), which is the honest answer.
		{"old bill entered this month, due 5th, no account", monthly5, nil, nil, time.Date(2026, time.November, 20, 0, 0, 0, 0, time.UTC), false, false, AttentionNeedsSetup},
		{"old bill entered this year, due 5th, account set", monthly5, nil, nil, time.Date(2026, time.November, 20, 0, 0, 0, 0, time.UTC), false, true, AttentionDueSoon},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AttentionFor(today, tc.schedule, tc.paidDate, tc.paidFor, tc.createdAt, tc.autopay, tc.hasAccount)
			if got != tc.want {
				t.Fatalf("AttentionFor(autopay=%v, account=%v) = %q, want %q", tc.autopay, tc.hasAccount, got, tc.want)
			}
		})
	}
}

// TestAttentionOverdueAgreesWithBillDue is the guard against the rule drifting
// from the due-date rule: "overdue" must mean exactly "BillDueFor's next due
// date is not the current period's, because the last one went unpaid". It
// cannot be checked through DaysUntil alone (that is never negative for a
// recurring bill), so it compares against the due rule's own coverage.
func TestAttentionOverdueAgreesWithBillDue(t *testing.T) {
	today := time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC)
	established := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	// Due on the 5th, last paid 5 Sep: the Oct and Nov dates are unpaid.
	s := MonthlySchedule(5)
	due, ok := BillDueFor(today, s, optDate(t, "2026-09-05"), nil)
	if !ok {
		t.Fatal("BillDueFor has no answer for a monthly bill with due_day 5")
	}
	if due.DaysUntil < 0 {
		t.Fatalf("BillDueFor moved the next due date into the past (DaysUntil=%d); this test assumes it never does", due.DaysUntil)
	}
	if got := AttentionFor(today, s, optDate(t, "2026-09-05"), nil, established, false, true); got != AttentionOverdue {
		t.Fatalf("attention = %q with two unpaid due dates behind it, want %q", got, AttentionOverdue)
	}
	// Paid for the current one: not overdue any more.
	if got := AttentionFor(today, s, optDate(t, "2026-11-05"), nil, established, false, true); got != AttentionDueSoon {
		t.Fatalf("attention = %q when the current due date is paid, want %q", got, AttentionDueSoon)
	}
}

// TestAttentionValuesCoverEveryState pins that the advertised list is the four
// states AttentionFor can return, so a schema or a doc that enumerates it
// cannot fall behind the rule.
func TestAttentionValuesCoverEveryState(t *testing.T) {
	today := time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC)
	established := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)
	seen := map[string]bool{
		AttentionFor(today, MonthlySchedule(24), nil, nil, recent, true, true):       true, // automatic
		AttentionFor(today, MonthlySchedule(24), nil, nil, recent, false, false):     true, // needs_setup
		AttentionFor(today, MonthlySchedule(24), nil, nil, recent, false, true):      true, // due_soon
		AttentionFor(today, MonthlySchedule(24), nil, nil, established, false, true): true, // overdue over the 24 Oct date
	}
	if len(seen) != len(AttentionValues) {
		t.Fatalf("AttentionFor produces %d distinct states, AttentionValues lists %d", len(seen), len(AttentionValues))
	}
	for _, v := range AttentionValues {
		if !seen[v] {
			t.Fatalf("AttentionValues lists %q, which no input produces", v)
		}
	}
}
