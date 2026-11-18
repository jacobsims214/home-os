package household

import (
	"testing"
	"time"

	"home-os/mcp/internal/testutil"
)

// TestFamilyOverviewBillsDueFrequencies: next_days.bills_due uses
// bill.BillDueFor (#4485), so quarterly, annual and once bills appear when
// due in the window; a paid once bill and a bill's own Bills-calendar event
// (event_type 'bill') do not appear in bills_due / events.
func TestFamilyOverviewBillsDueFrequencies(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, tool, h := NewFamilyOverviewTool(pool)
	rs := outputSchema(t, tool)

	now := time.Now().UTC()
	if now.Hour() == 23 && now.Minute() == 59 {
		time.Sleep(time.Minute)
		now = time.Now().UTC()
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	in := func(n int) time.Time { return today.AddDate(0, 0, n) }

	// A quarterly bill anchored 3 months before a date 2 days out (when that
	// keeps its day), an annual one due in 4 days, a once bill due tomorrow,
	// a paid once bill due today, and a once bill far away.
	qAnchor := in(2).AddDate(0, -3, 0)
	quarterly := in(2)
	if qAnchor.Day() != quarterly.Day() {
		qAnchor = quarterly
	}
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, frequency, due_date) VALUES ($1, 'Water', 90, 'quarterly', $2)`, hh.HouseholdID, qAnchor)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, frequency, due_date) VALUES ($1, 'Insurance', 600, 'annual', $2)`, hh.HouseholdID, in(4))
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, frequency, due_date) VALUES ($1, 'Registration', 80, 'once', $2)`, hh.HouseholdID, in(1))
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, frequency, due_date, paid_date) VALUES ($1, 'Paid fee', 5, 'once', $2, $2)`, hh.HouseholdID, today)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, frequency, due_date) VALUES ($1, 'Later fee', 5, 'once', $2)`, hh.HouseholdID, in(30))

	// A bill's mirror event on the Bills calendar is not listed as an event.
	cal := insertCalendar(t, pool, hh.HouseholdID, "Bills", "bills")
	ev := insertEvent(t, pool, cal, "Water due", in(2).Add(9*time.Hour))
	mustExec(t, pool, `UPDATE calendar_objects SET event_type = 'bill' WHERE id = $1`, ev)

	env, res := callEnvelope(t, ctx, h, rs, map[string]any{})
	if !env.OK || res.IsError {
		t.Fatalf("want ok, got %+v", env)
	}
	nd := decodeOverview(t, env).NextDays
	var got []string
	for _, b := range nd.BillsDue.Items {
		got = append(got, b.Name+" "+b.LocalDate)
	}
	want := []string{"Registration " + in(1).Format(time.DateOnly), "Water " + in(2).Format(time.DateOnly),
		"Insurance " + in(4).Format(time.DateOnly)}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("bills_due = %v, want %v", got, want)
	}
	if nd.Events.Total != 0 {
		t.Errorf("events = %+v, want the bill event left out", nd.Events)
	}
}
