package billdue

import (
	"testing"
	"time"
)

// These two tables are the due rule's own tests (#4468), moved here with the
// rule in #4819. apps/worker/cmd/worker/billdue.go is still a copy of the
// rule, and its TestNextUnpaidDueFrequencies / TestPaidForDueDateFrequencies
// tables must stay the same as these: a change to either side changes both,
// and TestBillRuleMatchesDueRule (apps/mcp) keeps the calendar RRULE in step.

// testSchedule builds a Schedule from a test row (0 / "" = NULL).
func testSchedule(t *testing.T, frequency string, dueDay int, dueDate string) Schedule {
	t.Helper()
	s := Schedule{Frequency: frequency}
	if dueDay != 0 {
		s.DueDay = &dueDay
	}
	if dueDate != "" {
		d := mustDate(t, dueDate)
		s.DueDate = &d
	}
	return s
}

// mustDate parses a YYYY-MM-DD date.
func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return d
}

// optDate parses s, nil when "".
func optDate(t *testing.T, s string) *time.Time {
	t.Helper()
	if s == "" {
		return nil
	}
	d := mustDate(t, s)
	return &d
}

// TestBillDueFrequencies pins the rule for every frequency (#4468): monthly
// by due_day; quarterly/semiannual/annual every 3/6/12 months on due_date's
// day (clamped, nothing before due_date); once on due_date only. The worker
// repeats this table in TestNextUnpaidDueFrequencies: keep them the same.
func TestBillDueFrequencies(t *testing.T) {
	tests := []struct {
		name       string
		today      string
		frequency  string
		dueDay     int    // 0 = NULL
		dueDate    string // "" = NULL
		paid       string // "" = never paid
		paidFor    string // stored paid_for_due_date; "" = NULL
		wantNext   string // "" = no next due date
		wantStatus string // "" = no schedule
		wantPaid   string // covered due date; "" = nil
	}{
		{"monthly through the same rule", "2026-11-15", "monthly", 20, "", "", "", "2026-11-20", "due", ""},
		{"quarterly: next quarter", "2026-11-15", "quarterly", 0, "2026-01-15", "", "", "2027-01-15", "due", ""},
		{"quarterly: due today", "2026-10-15", "quarterly", 0, "2026-01-15", "", "", "2026-10-15", "due", ""},
		{"quarterly: day after wraps a quarter", "2026-10-16", "quarterly", 0, "2026-01-15", "", "", "2027-01-15", "due", ""},
		{"quarterly: before the anchor is the anchor", "2025-12-01", "quarterly", 0, "2026-01-15", "", "", "2026-01-15", "due", ""},
		{"quarterly: Jan 31 clamps to Apr 30", "2026-04-02", "quarterly", 0, "2026-01-31", "", "", "2026-04-30", "due", ""},
		{"quarterly: after Apr 30 back to Jul 31", "2026-05-01", "quarterly", 0, "2026-01-31", "", "", "2026-07-31", "due", ""},
		{"semiannual: every 6 months", "2026-11-15", "semiannual", 0, "2026-03-10", "", "", "2027-03-10", "due", ""},
		{"annual: this year's date passed", "2026-11-15", "annual", 0, "2026-04-01", "", "", "2027-04-01", "due", ""},
		{"annual: leap day clamps to Feb 28", "2026-01-10", "annual", 0, "2024-02-29", "", "", "2026-02-28", "due", ""},
		{"annual: leap day is Feb 29 in a leap year", "2028-01-10", "annual", 0, "2024-02-29", "", "", "2028-02-29", "due", ""},
		{"once: upcoming", "2026-11-15", "once", 0, "2026-12-05", "", "", "2026-12-05", "due", ""},
		{"once: past and unpaid stays due", "2026-11-15", "once", 0, "2026-11-01", "", "", "2026-11-01", "due", ""},

		{"quarterly: paid early moves a quarter on", "2027-01-12", "quarterly", 0, "2026-01-15", "2027-01-10", "", "2027-04-15", "paid", "2027-01-15"},
		{"quarterly: paid last quarter only: still due", "2026-11-15", "quarterly", 0, "2026-01-15", "2026-10-14", "", "2027-01-15", "due", "2026-10-15"},
		{"quarterly: in grace covers the past due date", "2026-10-20", "quarterly", 0, "2026-01-15", "2026-10-20", "", "2027-01-15", "due", "2026-10-15"},
		{"annual: paid this year moves to next year", "2026-03-25", "annual", 0, "2026-04-01", "2026-03-20", "", "2027-04-01", "paid", "2026-04-01"},
		{"annual: stored paid_for wins", "2026-03-25", "annual", 0, "2026-04-01", "2026-03-20", "2025-04-01", "2026-04-01", "due", "2025-04-01"},
		{"once: paid has no next due date", "2026-11-15", "once", 0, "2026-12-05", "2026-11-10", "", "", "paid", "2026-12-05"},
		{"once: paid late still pays it", "2026-11-15", "once", 0, "2026-11-01", "2026-11-14", "", "", "paid", "2026-11-01"},

		{"no schedule: annual without due_date", "2026-11-15", "annual", 0, "", "", "", "", "", ""},
		{"no schedule: monthly without due_day", "2026-11-15", "monthly", 0, "", "", "", "", "", ""},
		{"no schedule: unknown frequency", "2026-11-15", "weekly", 0, "2026-11-20", "", "", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			today := mustDate(t, tc.today)
			due, ok := BillDueFor(today, testSchedule(t, tc.frequency, tc.dueDay, tc.dueDate), optDate(t, tc.paid), optDate(t, tc.paidFor))
			if !ok {
				if tc.wantStatus != "" {
					t.Fatalf("no schedule, want next %q status %q", tc.wantNext, tc.wantStatus)
				}
				return
			}
			gotNext := ""
			if !due.Next.IsZero() {
				gotNext = due.Next.Format(time.DateOnly)
				if want := DaysBetween(today, due.Next); due.DaysUntil != want {
					t.Errorf("days = %d, want %d", due.DaysUntil, want)
				}
			}
			gotPaid := ""
			if due.PaidFor != nil {
				gotPaid = due.PaidFor.Format(time.DateOnly)
			}
			if gotNext != tc.wantNext || due.Status != tc.wantStatus || gotPaid != tc.wantPaid {
				t.Errorf("got next %q status %q paid_for %q, want %q %q %q", gotNext, due.Status, gotPaid, tc.wantNext, tc.wantStatus, tc.wantPaid)
			}
		})
	}
}

// TestPaidForDueDateFrequencies pins which due date a payment pays for the
// frequencies that are not monthly. The worker repeats this table: keep them
// the same.
func TestPaidForDueDateFrequencies(t *testing.T) {
	tests := []struct {
		paid      string
		frequency string
		dueDate   string
		want      string
	}{
		{"2026-10-14", "quarterly", "2026-01-15", "2026-10-15"},  // early
		{"2026-10-22", "quarterly", "2026-01-15", "2026-10-15"},  // 7 days late: still October's
		{"2026-10-23", "quarterly", "2026-01-15", "2027-01-15"},  // 8 days late: next quarter
		{"2025-12-01", "quarterly", "2026-01-15", "2026-01-15"},  // before the anchor: the first due date
		{"2026-05-03", "quarterly", "2026-01-31", "2026-04-30"},  // 3 days after the clamped Apr 30
		{"2026-02-27", "annual", "2024-02-29", "2026-02-28"},     // leap-day anchor clamps
		{"2027-01-02", "semiannual", "2026-07-01", "2027-01-01"}, // 1 day late
		{"2026-11-30", "once", "2026-11-01", "2026-11-01"},       // a once bill: any payment pays it
		{"2026-10-01", "once", "2026-11-01", "2026-11-01"},
	}
	for _, tc := range tests {
		c, ok := testSchedule(t, tc.frequency, 0, tc.dueDate).cadence()
		if !ok {
			t.Fatalf("%s %s: no cadence", tc.frequency, tc.dueDate)
		}
		if got := paidForDueDate(mustDate(t, tc.paid), c).Format(time.DateOnly); got != tc.want {
			t.Errorf("paidForDueDate(%s, %s from %s) = %s, want %s", tc.paid, tc.frequency, tc.dueDate, got, tc.want)
		}
	}
}
