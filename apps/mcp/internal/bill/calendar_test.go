package bill

import (
	"strings"
	"testing"
	"time"

	"home-os/icalx"
)

// TestBillRuleMatchesDueRule checks that the RRULE written to the Bills
// calendar gives exactly the due dates of the bill rule (cadence) for six
// years, including day 29-31 clamping and leap days, by expanding it with
// icalx (the expander CalDAV PUT validation and the calendar tools use).
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
			start, rule, ok := billRule(tt.s, created)
			if !ok {
				t.Fatal("billRule not ok")
			}
			if rule != tt.wantRule || start.Format(time.DateOnly) != tt.wantStart {
				t.Fatalf("billRule = %s %q, want %s %q", start.Format(time.DateOnly), rule, tt.wantStart, tt.wantRule)
			}

			// The due rule's dates from the series start for six years.
			c, _ := tt.s.cadence()
			end := start.AddDate(6, 0, 0)
			var want []string
			for due, ok := c.onOrAfter(start); ok && due.Before(end); due, ok = c.after(due) {
				want = append(want, due.Format(time.DateOnly))
			}

			r := billRow{Name: "Power", Frequency: tt.s.Frequency, DueDay: tt.s.DueDay, DueDate: tt.s.DueDate, CreatedAt: created}
			m, ok := billMaster(r, "uid-1", 0)
			if !ok {
				t.Fatal("billMaster not ok")
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
			if _, _, ok := billRule(s, created); ok {
				t.Errorf("billRule(%+v) ok", s)
			}
			if _, ok := billMaster(billRow{Name: "X", Frequency: s.Frequency, DueDay: s.DueDay, DueDate: s.DueDate}, "u", 0); ok {
				t.Errorf("billMaster(%+v) ok", s)
			}
		}
	})
}

// TestBillEventEncoding checks the stored views of a mirror event: an
// all-day VEVENT with the RRULE and one VALARM 3 days before at 9:00, no
// attendees, and JSON whose times match the raw.
func TestBillEventEncoding(t *testing.T) {
	amount, cat := 142.5, "Utilities"
	r := billRow{Name: "Electric", Amount: &amount, Category: &cat, AutoPay: true,
		Frequency: FrequencyMonthly, DueDay: func() *int { n := 31; return &n }(),
		CreatedAt: time.Date(2027, time.February, 3, 0, 0, 0, 0, time.UTC)}
	m, ok := billMaster(r, "uid-e", 2)
	if !ok {
		t.Fatal("no master")
	}
	data, raw, rule, err := encodeBillEvent(m)
	if err != nil {
		t.Fatal(err)
	}
	if rule == nil || *rule != "FREQ=MONTHLY;BYMONTHDAY=-1" {
		t.Errorf("rrule = %v", rule)
	}
	for _, want := range []string{"DTSTART;VALUE=DATE:20270228", "DTEND;VALUE=DATE:20270301", "RRULE:FREQ=MONTHLY;BYMONTHDAY=-1",
		"BEGIN:VALARM", "TRIGGER:-P2DT15H", "SUMMARY:Electric due", "SEQUENCE:2"} {
		if !strings.Contains(raw, want) {
			t.Errorf("raw lacks %q:\n%s", want, raw)
		}
	}
	if strings.Contains(raw, "ATTENDEE") || strings.Contains(strings.ToLower(raw), "mailto") {
		t.Errorf("raw has attendees:\n%s", raw)
	}
	wantJSON := `"start":"2027-02-28T00:00:00Z"`
	if !strings.Contains(data, wantJSON) || !strings.Contains(data, `"all_day":true`) || !strings.Contains(data, `"sequence":2`) ||
		!strings.Contains(data, `"event_type":"bill"`) {
		t.Errorf("json = %s", data)
	}
	obj, err := icalx.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := obj.Master.Description; !strings.Contains(got, "Amount: $142.50") || !strings.Contains(got, "Category: Utilities") ||
		!strings.Contains(got, "autopay") {
		t.Errorf("description = %q", got)
	}
	if len(obj.Master.Alarms) != 1 || obj.Master.Alarms[0].Trigger != -63*time.Hour {
		t.Errorf("alarms = %+v", obj.Master.Alarms)
	}

	once := billRow{Name: "Registration", Frequency: FrequencyOnce,
		DueDate: func() *time.Time { v := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC); return &v }()}
	m, _ = billMaster(once, "uid-o", 0)
	_, raw, rule, err = encodeBillEvent(m)
	if err != nil {
		t.Fatal(err)
	}
	if rule != nil || strings.Contains(raw, "RRULE") || !strings.Contains(raw, "DTSTART;VALUE=DATE:20270501") {
		t.Errorf("once bill: rrule %v raw\n%s", rule, raw)
	}
}
