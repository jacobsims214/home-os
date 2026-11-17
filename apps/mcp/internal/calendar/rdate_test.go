package calendar

// Regression test for #4871: a range read dropped an RDATE-only series (no
// RRULE). The prefilter in queryOccurrences (tools.go) kept a series only
// while "rrule IS NOT NULL" or while its first occurrence still overlapped the
// window. An RDATE-only series has rrule NULL -- its instance list lives in
// ical_raw and nowhere else -- so a query for a later window returned nothing
// even though icalx.Expand finds the later instances.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// rdateOnlyRaw renders an RDATE-only series as a phone PUTs it over CalDAV:
// the first occurrence in DTSTART/DTEND and two later instances in RDATE, with
// no RRULE anywhere. prop is the property name as the client wrote it;
// RFC 5545 property names are case-insensitive (go-ical upper-cases them on
// parse), so every spelling describes the same instances.
func rdateOnlyRaw(uid, summary, prop string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Apple Inc.//iOS 18//EN\r\nBEGIN:VEVENT\r\n" +
		"UID:" + uid + "\r\nDTSTAMP:20271101T000000Z\r\nSUMMARY:" + summary + "\r\n" +
		"DTSTART;TZID=America/New_York:20271103T190000\r\nDTEND;TZID=America/New_York:20271103T200000\r\n" +
		prop + ";TZID=America/New_York:20271201T190000\r\n" +
		prop + ";TZID=America/New_York:20280105T190000\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
}

// seedRdateOnly creates the event through the real event_create tool (so
// ical_data and the stored times come from the production writer), then
// replaces ical_raw with the phone's RDATE list and leaves rrule NULL: that
// combination is the one the range prefilter has to understand. The JSON still
// matches the raw master (same summary, Nov 3 19:00 New York), so the
// staleness rule keeps the raw in play.
func seedRdateOnly(t *testing.T, env recurrenceEnv, title, prop string) string {
	t.Helper()
	written := writtenEvent(t, env.mustCall(t, env.create, map[string]any{
		"title": title, "start": "2027-11-03T19:00:00-04:00", "end": "2027-11-03T20:00:00-04:00"}))
	id := written.Event.ID
	setRecurrence(t, env.pool, id, nil, ptr(rdateOnlyRaw(id, title, prop)))
	return id
}

// TestEventFindKeepsRdateOnlySeries is the #4871 regression: a window that
// starts after the series' first occurrence must still list the later RDATE
// instances. Nov 3 2027 19:00 New York is EDT (23:00Z); Dec 1 and Jan 5 are
// EST (00:00Z the next day), so the instances cross the DST change too.
func TestEventFindKeepsRdateOnlySeries(t *testing.T) {
	env := newRecurrenceEnv(t)
	find := bind(t, NewEventFindTool, env.pool)
	id := seedRdateOnly(t, env, "Read-aloud", "RDATE")

	t.Run("the first occurrence is in an early window", func(t *testing.T) {
		evs := eventItems(t, env.mustCall(t, find, map[string]any{"from": "2027-11-01", "to": "2027-11-30"}))
		if got, want := occurrenceList(t, evs, env.ny), []string{"11-03 19:00 Read-aloud"}; !eq(got, want) {
			t.Fatalf("November = %v, want %v", got, want)
		}
	})

	// The regression: the window starts after the series' stored ends_at
	// (2027-11-04), so only an RDATE-aware prefilter keeps the row.
	t.Run("a later window lists the later RDATE instances", func(t *testing.T) {
		evs := eventItems(t, env.mustCall(t, find, map[string]any{"from": "2027-12-20", "to": "2028-01-10"}))
		if got, want := occurrenceList(t, evs, env.ny), []string{"01-05 19:00 Read-aloud"}; !eq(got, want) {
			t.Fatalf("January = %v, want %v", got, want)
		}
		for _, ev := range evs {
			if ev.ID != id || !ev.Recurring || ev.RecurrenceID != ev.Start {
				t.Errorf("occurrence lost its series: %+v", ev)
			}
			s, err := time.Parse(time.RFC3339, ev.Start)
			if err != nil {
				t.Fatalf("start %q: %v", ev.Start, err)
			}
			if h := s.In(env.ny).Hour(); h != 19 {
				t.Errorf("occurrence at %s is not 19:00 New York", ev.Start)
			}
		}
	})

	// The December instance is between the two windows, so a range that only
	// touches it must find it too (the RDATE list is not just a second date).
	t.Run("the middle instance has its own window", func(t *testing.T) {
		evs := eventItems(t, env.mustCall(t, find, map[string]any{"from": "2027-12-01", "to": "2027-12-02"}))
		if got, want := occurrenceList(t, evs, env.ny), []string{"12-01 19:00 Read-aloud"}; !eq(got, want) {
			t.Fatalf("December = %v, want %v", got, want)
		}
	})

	// agenda_get goes through the same query; the point here is only that the
	// series is not silently missing from a family-facing listing. It asks for
	// detail=full because that is the shape with one occurrence per entry: #4810
	// made the default a summary, one entry per series, which would collapse the
	// instant this case checks. occurrencesOf is expand_test.go's adapter from
	// that shape back to Events.
	t.Run("agenda shows the later instance", func(t *testing.T) {
		agenda := bind(t, NewAgendaGetTool, env.pool)
		var res agendaResult
		if err := json.Unmarshal(data(t, env.mustCall(t, agenda, map[string]any{
			"from": "2028-01-01", "to": "2028-01-10", "areas": []string{"events"}, "detail": "full"})), &res); err != nil {
			t.Fatal(err)
		}
		if got, want := occurrenceList(t, occurrencesOf(t, res.Events), env.ny), []string{"01-05 19:00 Read-aloud"}; !eq(got, want) {
			t.Fatalf("agenda = %v, want %v", got, want)
		}
	})

	// Keeping the row is not the same as inventing occurrences: a window the
	// RDATE list does not touch stays empty, so the looser prefilter is not
	// hiding a false positive behind the Go-side overlap check.
	t.Run("a window between the instances stays empty", func(t *testing.T) {
		evs := eventItems(t, env.mustCall(t, find, map[string]any{"from": "2027-11-10", "to": "2027-11-20"}))
		if len(evs) != 0 {
			t.Fatalf("mid-November = %v, want nothing", occurrenceList(t, evs, env.ny))
		}
	})

	// RFC 5545 property names are case-insensitive, so a client that writes
	// "rdate" must behave exactly like one that writes "RDATE".
	t.Run("a lower-case rdate property still expands", func(t *testing.T) {
		seedRdateOnly(t, env, "Book club", "rdate")
		evs := eventItems(t, env.mustCall(t, find, map[string]any{"from": "2027-12-20", "to": "2028-01-10", "query": "book"}))
		if got, want := occurrenceList(t, evs, env.ny), []string{"01-05 19:00 Book club"}; !eq(got, want) {
			t.Fatalf("January (lower-case property) = %v, want %v", got, want)
		}
	})
}

// The seeded series carries rrule NULL and its instances only in ical_raw,
// which is the shape the old prefilter could not see. Keep that explicit so a
// future writer change does not quietly turn the regression test into a no-op.
func TestSeedRdateOnlyHasNoRule(t *testing.T) {
	env := newRecurrenceEnv(t)
	id := seedRdateOnly(t, env, "No rule", "RDATE")
	var rrule *string
	var raw string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT rrule, ical_raw FROM calendar_objects WHERE id = $1`, id).Scan(&rrule, &raw); err != nil {
		t.Fatal(err)
	}
	if rrule != nil || !strings.Contains(raw, "RDATE") {
		t.Fatalf("seeded row: rrule=%v, raw has RDATE=%v", rrule, strings.Contains(raw, "RDATE"))
	}
}
