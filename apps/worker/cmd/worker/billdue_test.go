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
			if got := nextUnpaidDue(mustDate(t, tc.today), tc.dueDay, paid, paidFor).Format(time.DateOnly); got != tc.wantNext {
				t.Errorf("next = %s, want %s", got, tc.wantNext)
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
		if got := paidForDueDate(mustDate(t, tc.paid), tc.dueDay).Format(time.DateOnly); got != tc.want {
			t.Errorf("paidForDueDate(%s, %d) = %s, want %s", tc.paid, tc.dueDay, got, tc.want)
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
