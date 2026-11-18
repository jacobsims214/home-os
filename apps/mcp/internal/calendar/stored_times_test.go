package calendar

import (
	"context"
	"slices"
	"testing"
	"time"

	"home-os/mcp/internal/testutil"
)

// Range queries read calendar_objects.starts_at / ends_at (migration 035,
// #4387) instead of casting ical_data per row.
func TestQueryEventsStoredTimes(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, other := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	loc := mustLoc(t, "America/New_York")
	cal := seedCalendar(t, pool, hh.HouseholdID)
	otherCal := seedCalendar(t, pool, other.HouseholdID)

	inRange := seedEvent(t, pool, cal, "in", `{"title":"Dentist","start":"2026-11-18T15:00:00Z","end":"2026-11-18T16:00:00Z"}`)
	before := seedEvent(t, pool, cal, "before", `{"title":"Old","start":"2026-10-01T15:00:00Z","end":"2026-10-01T16:00:00Z"}`)
	after := seedEvent(t, pool, cal, "after", `{"title":"Later","start":"2026-12-20T15:00:00Z","end":"2026-12-20T16:00:00Z"}`)
	// All-day on the first local day of the range: stored as UTC midnights.
	allDay := seedEvent(t, pool, cal, "allday", `{"title":"Bill","all_day":true,"start":"2026-11-16T00:00:00Z","end":"2026-11-17T00:00:00Z"}`)
	// A malformed row used to fail the whole query (::jsonb cast); now it is skipped.
	seedEvent(t, pool, cal, "bad", `not json at all`)
	// A weekly series that started in January: its first occurrence ended
	// long ago, but the row must still reach Go.
	series := seedEvent(t, pool, cal, "series", `{"title":"Swim","start":"2026-01-06T23:00:00Z","end":"2026-01-07T00:00:00Z"}`)
	setRecurrence(t, pool, series, ptr("FREQ=WEEKLY;BYDAY=TU"), nil)
	// Another household's event in range is never returned.
	seedEvent(t, pool, otherCal, "other", `{"title":"Theirs","start":"2026-11-18T15:00:00Z","end":"2026-11-18T16:00:00Z"}`)

	from := time.Date(2026, 11, 16, 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 7)

	events, err := queryEvents(ctx, pool, hh.HouseholdID.String(), loc, nil, from, to)
	if err != nil {
		t.Fatalf("queryEvents: %v", err)
	}
	var ids []string
	for _, ev := range events {
		ids = append(ids, ev.ID)
	}
	for name, id := range map[string]string{"in range": inRange, "all-day": allDay, "series": series} {
		if !slices.Contains(ids, id) {
			t.Errorf("%s event missing from queryEvents: %v", name, ids)
		}
	}
	for name, id := range map[string]string{"before": before, "after": after} {
		if slices.Contains(ids, id) {
			t.Errorf("%s event returned by queryEvents", name)
		}
	}
	if len(ids) != 3 {
		t.Errorf("queryEvents returned %d rows, want 3: %v", len(ids), ids)
	}

	// event_find over the same week: the malformed row doesn't fail it, and
	// the series shows as its one occurrence in the week (#4483).
	text, isErr := callTool(t, bind(t, NewEventFindTool, pool), ctx, map[string]any{"from": "2026-11-16", "to": "2026-11-22"})
	if isErr {
		t.Fatalf("event_find failed: %s", text)
	}
	var titles []string
	for _, ev := range eventItems(t, text) {
		titles = append(titles, ev.Title)
	}
	if !slices.Equal(titles, []string{"Bill", "Swim", "Dentist"}) {
		t.Errorf("event_find titles = %v, want [Bill Swim Dentist]", titles)
	}

	// Moving an event (ical_data rewrite) moves it in range queries too.
	if _, err := pool.Exec(context.Background(), `UPDATE calendar_objects SET ical_data = $2 WHERE id = $1`,
		after, `{"title":"Later","start":"2026-11-19T15:00:00Z","end":"2026-11-19T16:00:00Z"}`); err != nil {
		t.Fatal(err)
	}
	events, err = queryEvents(ctx, pool, hh.HouseholdID.String(), loc, nil, from, to)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		found = found || ev.ID == after
	}
	if !found {
		t.Error("moved event not in range after ical_data update")
	}
}
