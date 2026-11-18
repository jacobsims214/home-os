package main

import (
	"strings"
	"testing"
	"time"

	"home-os/billcal"
	"home-os/icalx"
)

// TestBillCalendarRuleMatchesNextUnpaidDue checks that the Bills-calendar
// RRULE (home-os/billcal, written by the MCP, the API and this worker's
// backfill) falls on exactly the due dates this worker's bill_due job
// notifies for (its copy of the due rule), for every frequency, for six
// years. apps/mcp's TestBillRuleMatchesDueRule does the same against the
// MCP's rule, so the phone's event, its VALARM and the in-app reminder agree.
func TestBillCalendarRuleMatchesNextUnpaidDue(t *testing.T) {
	d := func(s string) *time.Time { v := mustDate(t, s); return &v }
	day := func(n int) *int { return &n }
	created := time.Date(2026, time.November, 15, 22, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		s    schedule
	}{
		{"monthly 1st", monthlySchedule(1)},
		{"monthly 15th", monthlySchedule(15)},
		{"monthly 28th", monthlySchedule(28)},
		{"monthly 29th", monthlySchedule(29)},
		{"monthly 30th", monthlySchedule(30)},
		{"monthly 31st", monthlySchedule(31)},
		{"legacy monthly", schedule{dueDay: day(10)}},
		{"quarterly Jan 31", schedule{frequency: "quarterly", dueDate: d("2027-01-31")}},
		{"quarterly Nov 29", schedule{frequency: "quarterly", dueDate: d("2026-11-29")}},
		{"quarterly 5th", schedule{frequency: "quarterly", dueDate: d("2026-12-05")}},
		{"semiannual Aug 31", schedule{frequency: "semiannual", dueDate: d("2027-08-31")}},
		{"semiannual Mar 30", schedule{frequency: "semiannual", dueDate: d("2027-03-30")}},
		{"annual Jun 15", schedule{frequency: "annual", dueDate: d("2027-06-15")}},
		{"annual Feb 29", schedule{frequency: "annual", dueDate: d("2028-02-29")}},
		{"annual Jan 31", schedule{frequency: "annual", dueDate: d("2027-01-31")}},
		{"once", schedule{frequency: "once", dueDate: d("2027-03-09")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, _, ok := billcal.Rule(tt.s.frequency, tt.s.dueDay, tt.s.dueDate, created)
			if !ok {
				t.Fatal("billcal.Rule not ok")
			}
			end := start.AddDate(6, 0, 0)

			// The worker's due dates: walk nextUnpaidDue day by day.
			var want []string
			for today := start; today.Before(end); today = today.AddDate(0, 0, 1) {
				next, ok := nextUnpaidDue(today, tt.s, nil, nil)
				if !ok || !next.Equal(today) {
					continue
				}
				want = append(want, next.Format(time.DateOnly))
			}

			m, ok := billcal.Master(billcal.Bill{Name: "Power", Frequency: tt.s.frequency, DueDay: tt.s.dueDay,
				DueDate: tt.s.dueDate, CreatedAt: created}, "uid", 0)
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
}
