package budget_test

// The third way a due date is expressed in this system is the Bills calendar
// mirror: every bill with a usable schedule gets an all-day event on the
// household's Bills calendar whose RRULE (home-os/billcal) must give exactly
// the bill's due dates, so a phone shows the date the API and the agent report
// (#4716, #4485). apps/mcp's TestBillRuleMatchesDueRule pins that for the MCP;
// this pins it from the API's side, on real rows: every due date the endpoint
// returns is an occurrence of that bill's mirror event.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/billcal"
	"home-os/icalx"
)

// TestDueSoon_DueDatesAreCalendarOccurrences is the "due dates agree with what
// the Bills calendar mirror produces" acceptance criterion: for every bill the
// endpoint lists, the date it reports is one the mirror's own event produces
// when expanded with icalx (the same expander CalDAV PUT validation uses).
func TestDueSoon_DueDatesAreCalendarOccurrences(t *testing.T) {
	d := newDueSoonFixture(t)
	seedDueSoon(t, d)
	ctx := context.Background()

	// The mirror events, built exactly as the API's and MCP's bill writes do.
	type mirror struct {
		start time.Time
		rule  string
	}
	mirrors := map[string]mirror{}
	rows, err := d.pool.Query(ctx,
		`SELECT id::text, name, amount::float8, frequency, due_day, due_date, created_at
		   FROM bills WHERE household_id = $1`, d.hh)
	require.NoError(t, err)
	for rows.Next() {
		var (
			id, name  string
			amount    *float64
			freq      string
			dueDay    *int
			dueDate   *time.Time
			createdAt time.Time
		)
		require.NoError(t, rows.Scan(&id, &name, &amount, &freq, &dueDay, &dueDate, &createdAt))
		start, rule, ok := billcal.Rule(freq, dueDay, dueDate, createdAt)
		if !ok {
			continue // no usable schedule: the mirror writes no event either
		}
		mirrors[id] = mirror{start: start, rule: rule}
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, mirrors, "the fixture must produce mirror events")

	items, rec := d.dueSoon(t, d.as, "detail=items")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	from, err := time.Parse(time.DateOnly, items.From)
	require.NoError(t, err)
	to, err := time.Parse(time.DateOnly, items.To)
	require.NoError(t, err)

	checked := 0
	for _, item := range items.Items {
		if item.Source != "bill" {
			continue
		}
		m, ok := mirrors[item.ID]
		require.True(t, ok, "%s: every listed bill has a mirror event", item.Name)

		// The whole event, expanded over the window. icalx validates the
		// RRULE the way CalDAV PUT validation does.
		occ := expandMirror(t, item.ID, item.Name, m.start, m.rule, from.AddDate(0, 0, -1), to.AddDate(0, 0, 1))
		assert.Contains(t, occ, item.DueDate,
			"%s: endpoint says %s; the mirror's %q produces %v", item.Name, item.DueDate, m.rule, occ)
		checked++
	}
	assert.Positive(t, checked, "at least one bill due date was compared with its mirror event")
}

// expandMirror builds a bill's mirror event from its date rule and expands it
// over [from, to) with icalx, returning the occurrence dates as YYYY-MM-DD.
// A once bill has no RRULE: its single occurrence is the DTSTART.
func expandMirror(t *testing.T, id, name string, start time.Time, rule string, from, to time.Time) []string {
	t.Helper()
	if rule == "" {
		return []string{start.Format(time.DateOnly)}
	}
	ev := &icalx.Event{UID: "uid-" + id, Summary: name, Start: start, End: start.AddDate(0, 0, 1)}
	ev.RRule = rule
	obj := &icalx.Object{UID: ev.UID, Master: ev}
	require.NoError(t, obj.Validate(), "%s: the mirror's RRULE %q must be valid", name, rule)
	occs, err := obj.Expand(from, to, time.UTC)
	require.NoError(t, err, "%s: expand %q", name, rule)
	out := make([]string, 0, len(occs))
	for _, o := range occs {
		out = append(out, o.Start.Format(time.DateOnly))
	}
	return out
}
