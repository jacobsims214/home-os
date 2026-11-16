package billcal

import (
	"strings"
	"testing"
	"time"

	"home-os/icalx"
)

// TestRuleShapes pins the RRULE text and DTSTART for every frequency. That
// these give exactly the due dates of the bill due rule is checked against
// the rule itself in apps/mcp (TestBillRuleMatchesDueRule) and apps/worker
// (TestBillCalendarRuleMatchesNextUnpaidDue).
func TestRuleShapes(t *testing.T) {
	d := func(s string) *time.Time {
		v, _ := time.Parse(time.DateOnly, s)
		return &v
	}
	day := func(n int) *int { return &n }
	created := time.Date(2026, time.November, 15, 22, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		freq      string
		dueDay    *int
		dueDate   *time.Time
		wantRule  string
		wantStart string
	}{
		{"monthly 15th", FrequencyMonthly, day(15), nil, "FREQ=MONTHLY;BYMONTHDAY=15", "2026-11-15"},
		{"monthly 31st", FrequencyMonthly, day(31), nil, "FREQ=MONTHLY;BYMONTHDAY=-1", "2026-11-30"},
		{"legacy monthly", "", day(10), nil, "FREQ=MONTHLY;BYMONTHDAY=10", "2026-11-10"},
		{"quarterly 30th", FrequencyQuarterly, nil, d("2026-11-30"), "FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=28,29,30;BYSETPOS=-1", "2026-11-30"},
		{"semiannual 5th", FrequencySemiannual, nil, d("2027-02-05"), "FREQ=MONTHLY;INTERVAL=6;BYMONTHDAY=5", "2027-02-05"},
		{"annual", FrequencyAnnual, nil, d("2027-06-15"), "FREQ=YEARLY", "2027-06-15"},
		{"annual Feb 29", FrequencyAnnual, nil, d("2028-02-29"), "FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=28,29;BYSETPOS=-1", "2028-02-29"},
		{"once", FrequencyOnce, nil, d("2027-03-09"), "", "2027-03-09"},
	}
	for _, tt := range tests {
		start, rule, ok := Rule(tt.freq, tt.dueDay, tt.dueDate, created)
		if !ok || rule != tt.wantRule || start.Format(time.DateOnly) != tt.wantStart {
			t.Errorf("%s: Rule = %s %q %v, want %s %q", tt.name, start.Format(time.DateOnly), rule, ok, tt.wantStart, tt.wantRule)
		}
	}
	for _, s := range []struct {
		freq    string
		dueDay  *int
		dueDate *time.Time
	}{{FrequencyMonthly, nil, nil}, {FrequencyMonthly, day(0), nil}, {FrequencyMonthly, day(32), nil},
		{FrequencyAnnual, nil, nil}, {"weekly", day(3), d("2027-01-01")}} {
		if _, _, ok := Rule(s.freq, s.dueDay, s.dueDate, created); ok {
			t.Errorf("Rule(%q, %v, %v) ok", s.freq, s.dueDay, s.dueDate)
		}
	}
}

// TestEventEncoding checks the stored views of a mirror event: an all-day
// VEVENT with the RRULE and one VALARM 3 days before at 9:00, no attendees,
// and JSON whose times match the raw.
func TestEventEncoding(t *testing.T) {
	amount, cat := 142.5, "Utilities"
	due := 31
	b := Bill{Name: "Electric", Amount: &amount, Category: &cat, AutoPay: true,
		Frequency: FrequencyMonthly, DueDay: &due,
		CreatedAt: time.Date(2027, time.February, 3, 0, 0, 0, 0, time.UTC)}
	m, ok := Master(b, "uid-e", 2)
	if !ok {
		t.Fatal("no master")
	}
	data, raw, rule, err := Encode(m)
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
	if !strings.Contains(data, `"start":"2027-02-28T00:00:00Z"`) || !strings.Contains(data, `"all_day":true`) ||
		!strings.Contains(data, `"sequence":2`) || !strings.Contains(data, `"event_type":"bill"`) {
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

	dueDate := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
	m, _ = Master(Bill{Name: "Registration", Frequency: FrequencyOnce, DueDate: &dueDate}, "uid-o", 0)
	data, raw, rule, err = Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if rule != nil || strings.Contains(raw, "RRULE") || !strings.Contains(raw, "DTSTART;VALUE=DATE:20270501") ||
		strings.Contains(data, "sequence") {
		t.Errorf("once bill: rrule %v json %s raw\n%s", rule, data, raw)
	}
}
