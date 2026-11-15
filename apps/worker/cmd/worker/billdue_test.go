package main

import (
	"testing"
	"time"
)

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return d
}

// TestNextUnpaidDue is apps/mcp/internal/bill TestBillDue (next column):
// keep the two tables the same.
func TestNextUnpaidDue(t *testing.T) {
	tests := []struct {
		name     string
		today    string
		dueDay   int
		paid     string // "" = never paid
		paidFor  string // stored paid_for_due_date; "" = NULL (inferred from paid)
		wantNext string
	}{
		{"31 in November is Nov 30", "2026-11-15", 31, "", "", "2026-11-30"},
		{"31 on Nov 30 is today", "2026-11-30", 31, "", "", "2026-11-30"},
		{"31 in February is Feb 28", "2027-02-10", 31, "", "", "2027-02-28"},
		{"31 in leap February is Feb 29", "2028-02-10", 31, "", "", "2028-02-29"},
		{"30 in leap February is Feb 29", "2028-02-29", 30, "", "", "2028-02-29"},
		{"29 in February is Feb 28", "2027-02-01", 29, "", "", "2027-02-28"},
		{"31 after Jan 31 is Feb 28", "2027-02-01", 31, "", "", "2027-02-28"},
		{"passed this month wraps to next", "2026-11-29", 1, "", "", "2026-12-01"},
		{"wraps the year", "2026-12-20", 5, "", "", "2027-01-05"},
		{"due today is today", "2026-11-15", 15, "", "", "2026-11-15"},

		{"paid early moves to next month", "2026-11-15", 20, "2026-11-14", "", "2026-12-20"},
		{"paid on the day moves on", "2026-11-20", 20, "2026-11-20", "", "2026-12-20"},
		{"paid Nov 30 for 31 moves to Dec 31", "2026-11-30", 31, "2026-11-30", "", "2026-12-31"},
		{"paid for Jan 31 moves to Feb 28", "2027-01-31", 31, "2027-01-31", "", "2027-02-28"},
		{"paid for Jan 31 moves to leap Feb 29", "2028-01-31", 31, "2028-01-31", "", "2028-02-29"},
		{"paid last month only: still due", "2026-11-15", 20, "2026-10-20", "", "2026-11-20"},
		{"late payment in grace covers the past due date", "2026-11-25", 20, "2026-11-25", "", "2026-12-20"},
		{"payment 8 days late pays the next one", "2026-11-28", 20, "2026-11-28", "", "2027-01-20"},

		{"stored: 8 days late for the past due date", "2026-11-28", 20, "2026-11-28", "2026-11-20", "2026-12-20"},
		{"stored: wins over a payment in grace", "2026-11-15", 20, "2026-11-14", "2026-10-20", "2026-11-20"},
		{"stored: next month paid ahead moves two on", "2026-11-15", 20, "2026-11-15", "2026-12-20", "2027-01-20"},
		{"stored: clamped Feb 28 for 31", "2027-03-01", 31, "2027-03-01", "2027-02-28", "2027-03-31"},
		{"stored: without paid_date", "2026-11-15", 20, "", "2026-11-20", "2026-12-20"},
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
			if got, _ := nextUnpaidDue(mustDate(t, tc.today), monthlySchedule(tc.dueDay), paid, paidFor); got.Format(time.DateOnly) != tc.wantNext {
				t.Errorf("next = %s, want %s", got.Format(time.DateOnly), tc.wantNext)
			}
		})
	}
}

// TestPaidForDueDate is apps/mcp/internal/bill TestPaidForDueDate: keep the
// two tables the same.
func TestPaidForDueDate(t *testing.T) {
	tests := []struct {
		paid   string
		dueDay int
		want   string
	}{
		{"2026-01-18", 20, "2026-01-20"}, // early
		{"2026-01-20", 20, "2026-01-20"}, // on the day
		{"2026-01-27", 20, "2026-01-20"}, // 7 days late: still January's
		{"2026-01-28", 20, "2026-02-20"}, // 8 days late: next one
		{"2026-01-02", 5, "2026-01-05"},  // previous (Dec 5) is too far back
		{"2026-01-03", 28, "2025-12-28"}, // late across the year boundary
		{"2026-02-10", 31, "2026-02-28"}, // Jan 31 too far back; Feb clamps
		{"2026-03-03", 31, "2026-02-28"}, // 3 days after the clamped Feb 28
		{"2028-02-29", 30, "2028-02-29"}, // leap year clamp, on the day
		{"2025-12-31", 31, "2025-12-31"},
	}
	for _, tc := range tests {
		if got := paidForDueDate(mustDate(t, tc.paid), mustCadence(t, monthlySchedule(tc.dueDay))).Format(time.DateOnly); got != tc.want {
			t.Errorf("paidForDueDate(%s, %d) = %s, want %s", tc.paid, tc.dueDay, got, tc.want)
		}
	}
}

func mustCadence(t *testing.T, s schedule) cadence {
	t.Helper()
	c, ok := s.cadence()
	if !ok {
		t.Fatalf("no cadence for %+v", s)
	}
	return c
}

// testSchedule builds a schedule from a test row (0 / "" = NULL).
func testSchedule(t *testing.T, frequency string, dueDay int, dueDate string) schedule {
	t.Helper()
	s := schedule{frequency: frequency}
	if dueDay != 0 {
		s.dueDay = &dueDay
	}
	if dueDate != "" {
		d := mustDate(t, dueDate)
		s.dueDate = &d
	}
	return s
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

// TestNextUnpaidDueFrequencies is apps/mcp/internal/bill
// TestBillDueFrequencies (next column; status and paid_for are the MCP
// side's): keep the two tables the same.
func TestNextUnpaidDueFrequencies(t *testing.T) {
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
			next, ok := nextUnpaidDue(mustDate(t, tc.today), testSchedule(t, tc.frequency, tc.dueDay, tc.dueDate), optDate(t, tc.paid), optDate(t, tc.paidFor))
			got := ""
			if ok {
				got = next.Format(time.DateOnly)
			}
			if got != tc.wantNext {
				t.Errorf("next = %q, want %q", got, tc.wantNext)
			}
		})
	}
}

// TestPaidForDueDateFrequencies is apps/mcp/internal/bill
// TestPaidForDueDateFrequencies: keep the two tables the same.
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

func TestHouseholdToday(t *testing.T) {
	// 03:00 UTC on Nov 10 is still the evening of Nov 9 in the US.
	now := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		tz   string
		want string
	}{
		{"UTC", "2026-11-10"},
		{"", "2026-11-10"},
		{"Not/AZone", "2026-11-10"},
		{"America/Chicago", "2026-11-09"},
		{"Pacific/Auckland", "2026-11-10"},
	}
	for _, tc := range tests {
		got := householdToday(now, tc.tz)
		if got.Format(time.DateOnly) != tc.want || got.Location() != time.UTC || got.Hour() != 0 {
			t.Errorf("householdToday(%q) = %v, want %s midnight UTC", tc.tz, got, tc.want)
		}
	}
}
