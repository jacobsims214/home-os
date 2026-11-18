package bill

import (
	"strings"
	"testing"
	"time"

	"home-os/billcal"
	"home-os/icalx"
)

// TestBillRuleMatchesDueRule checks that the RRULE home-os/billcal writes to
// the Bills calendar (for the MCP, the API and the worker backfill alike)
// gives exactly the due dates of this module's bill rule (cadence) for six
// years, for every frequency, including day 29-31 clamping and leap days, by
// expanding it with icalx (the expander CalDAV PUT validation and the
// calendar tools use). This is what proves the shared RRULE and the due rule
// identical.
func TestBillRuleMatchesDueRule(t *testing.T) {
	d := func(s string) *time.Time {
		v, err := time.Parse(time.DateOnly, s)
		if err != nil {
			t.Fatal(err)
		}
		return &v
	}
	day := func(n int) *int { return &n }
	created := time.Date(2026, time.November, 15, 22, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		s         Schedule
		wantRule  string
		wantStart string
	}{
		{"monthly 1st", MonthlySchedule(1), "FREQ=MONTHLY;BYMONTHDAY=1", "2026-11-01"},
		{"monthly 15th", MonthlySchedule(15), "FREQ=MONTHLY;BYMONTHDAY=15", "2026-11-15"},
		{"monthly 28th", MonthlySchedule(28), "FREQ=MONTHLY;BYMONTHDAY=28", "2026-11-28"},
		{"monthly 29th", MonthlySchedule(29), "FREQ=MONTHLY;BYMONTHDAY=28,29;BYSETPOS=-1", "2026-11-29"},
		{"monthly 30th", MonthlySchedule(30), "FREQ=MONTHLY;BYMONTHDAY=28,29,30;BYSETPOS=-1", "2026-11-30"},
		{"monthly 31st", MonthlySchedule(31), "FREQ=MONTHLY;BYMONTHDAY=-1", "2026-11-30"},
		{"legacy monthly", Schedule{DueDay: day(10)}, "FREQ=MONTHLY;BYMONTHDAY=10", "2026-11-10"},
		{"quarterly Jan 31", Schedule{Frequency: FrequencyQuarterly, DueDate: d("2027-01-31")}, "FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=-1", "2027-01-31"},
		{"quarterly Nov 30", Schedule{Frequency: FrequencyQuarterly, DueDate: d("2026-11-30")}, "FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=28,29,30;BYSETPOS=-1", "2026-11-30"},
		{"quarterly 5th", Schedule{Frequency: FrequencyQuarterly, DueDate: d("2026-12-05")}, "FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=5", "2026-12-05"},
		{"semiannual Aug 31", Schedule{Frequency: FrequencySemiannual, DueDate: d("2027-08-31")}, "FREQ=MONTHLY;INTERVAL=6;BYMONTHDAY=-1", "2027-08-31"},
		{"annual Jun 15", Schedule{Frequency: FrequencyAnnual, DueDate: d("2027-06-15")}, "FREQ=YEARLY", "2027-06-15"},
		{"annual Feb 29", Schedule{Frequency: FrequencyAnnual, DueDate: d("2028-02-29")}, "FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=28,29;BYSETPOS=-1", "2028-02-29"},
		{"annual Jan 31", Schedule{Frequency: FrequencyAnnual, DueDate: d("2027-01-31")}, "FREQ=YEARLY;BYMONTH=1;BYMONTHDAY=-1", "2027-01-31"},
		{"once", Schedule{Frequency: FrequencyOnce, DueDate: d("2027-03-09")}, "", "2027-03-09"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, rule, ok := billcal.Rule(tt.s.Frequency, tt.s.DueDay, tt.s.DueDate, created)
			if !ok {
				t.Fatal("billcal.Rule not ok")
			}
			if rule != tt.wantRule || start.Format(time.DateOnly) != tt.wantStart {
				t.Fatalf("billcal.Rule = %s %q, want %s %q", start.Format(time.DateOnly), rule, tt.wantStart, tt.wantRule)
			}

			// The due rule's dates from the series start for six years.
			c, _ := cadence(tt.s)
			end := start.AddDate(6, 0, 0)
			var want []string
			for due, ok := c.OnOrAfter(start); ok && due.Before(end); due, ok = c.After(due) {
				want = append(want, due.Format(time.DateOnly))
			}

			r := billRow{Name: "Power", Frequency: tt.s.Frequency, DueDay: tt.s.DueDay, DueDate: tt.s.DueDate, CreatedAt: created}
			m, ok := billcal.Master(calendarBill(r), "uid-1", 0)
			if !ok {
				t.Fatal("billcal.Master not ok")
			}
			obj := &icalx.Object{UID: m.UID, Master: m}
			if err := obj.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			occs, err := obj.Expand(start, end, time.UTC)
			if err != nil {
				t.Fatalf("Expand: %v", err)
			}
			var got []string
			for _, o := range occs {
				got = append(got, o.Start.Format(time.DateOnly))
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("occurrences\n got %v\nwant %v", got, want)
			}
		})
	}

	t.Run("unusable schedules give no event", func(t *testing.T) {
		for _, s := range []Schedule{{Frequency: FrequencyMonthly}, {Frequency: FrequencyAnnual}, {Frequency: "weekly", DueDay: day(3)}} {
			if _, _, ok := billcal.Rule(s.Frequency, s.DueDay, s.DueDate, created); ok {
				t.Errorf("billcal.Rule(%+v) ok", s)
			}
			if _, ok := billcal.Master(calendarBill(billRow{Name: "X", Frequency: s.Frequency, DueDay: s.DueDay, DueDate: s.DueDate}), "u", 0); ok {
				t.Errorf("billcal.Master(%+v) ok", s)
			}
		}
	})
}
