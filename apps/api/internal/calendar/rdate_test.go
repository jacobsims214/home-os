package calendar

// Regression test for #4871: a range listing dropped an RDATE-only series (no
// RRULE). rangeFilter (repo.go) kept a series only while "rrule IS NOT NULL"
// or while its first occurrence still overlapped the window, plus an
// "ical_raw LIKE '%RDATE%'" clause. A series defined only by RDATEs has
// rrule NULL, so that raw clause was the only thing that could keep it -- and
// it was case-sensitive, so a client spelling the property the RFC's way
// allows (any case; go-ical upper-cases on parse) was dropped outright, even
// though icalx.Expand finds its later instances. The MCP query had no raw
// clause at all (apps/mcp/internal/calendar/rdate_test.go).

import (
	"net/http"
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

// seedRdateOnly creates the event through the real POST /events handler (so
// the JSON comes from the production writer), then stores the phone's RDATE
// list in ical_raw the way a CalDAV PUT does and leaves rrule NULL: that is the
// combination the range prefilter has to understand. The JSON still matches the
// raw master (same summary, Nov 3 19:00 New York), so the staleness rule keeps
// the raw in play.
func seedRdateOnly(t *testing.T, f *recurrenceFixture, title, prop string) string {
	t.Helper()
	code, body := f.do(t, http.MethodPost, f.eventsPath(), map[string]any{
		"title": title, "start": "2027-11-03T19:00:00-04:00", "end": "2027-11-03T20:00:00-04:00"})
	if code != http.StatusCreated {
		t.Fatalf("create %q: %d %s", title, code, body)
	}
	id := decodeOne(t, body).ID
	if _, err := f.pool.Exec(f.ctx,
		`UPDATE calendar_objects SET ical_raw = $2, rrule = NULL WHERE id = $1`,
		id, rdateOnlyRaw(id, title, prop)); err != nil {
		t.Fatalf("store raw: %v", err)
	}
	return id
}

// TestRangeKeepsRdateOnlySeries is the #4871 regression: a window that starts
// after the series' first occurrence must still list the later RDATE
// instances. Nov 3 2027 19:00 New York is EDT (23:00Z); Dec 1 and Jan 5 are
// EST (00:00Z the next day), so the instances cross the DST change too.
func TestRangeKeepsRdateOnlySeries(t *testing.T) {
	f := newRecurrenceFixture(t, "America/New_York")
	id := seedRdateOnly(t, f, "Read-aloud", "RDATE")

	list := func(q string) []Event {
		t.Helper()
		code, body := f.do(t, http.MethodGet, f.eventsPath()+q, nil)
		if code != http.StatusOK {
			t.Fatalf("list %s: %d %s", q, code, body)
		}
		return decodeList(t, body)
	}

	// Control: the first occurrence overlaps the window, so the stored
	// ends_at alone keeps the row. This is what worked before the fix.
	if got := startsUTC(list("?start=2027-11-01T00:00:00Z&end=2027-11-30T00:00:00Z")); strings.Join(got, ",") != "2027-11-03T23:00Z" {
		t.Fatalf("November = %v", got)
	}

	// The regression: the window starts after the series' stored ends_at
	// (2027-11-04T00:00Z), so only an RDATE-aware prefilter keeps the row.
	evs := list("?start=2028-01-01T00:00:00Z&end=2028-01-10T00:00:00Z")
	if got := strings.Join(startsUTC(evs), ","); got != "2028-01-06T00:00Z" {
		t.Fatalf("January = %s, want the Jan 5 19:00 New York RDATE instance", got)
	}
	if len(evs) != 1 || evs[0].ID != id || !evs[0].Recurring || evs[0].RecurrenceID == nil {
		t.Fatalf("January occurrence = %+v", evs)
	}
	if !evs[0].RecurrenceID.Equal(time.Date(2028, 1, 6, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("recurrence_id = %s, want the original instance start", evs[0].RecurrenceID)
	}

	// The December instance sits between the two windows; a range that only
	// touches it must find it too.
	if got := strings.Join(startsUTC(list("?start=2027-12-01T00:00:00Z&end=2027-12-03T00:00:00Z")), ","); got != "2027-12-02T00:00Z" {
		t.Fatalf("December = %s", got)
	}

	// The household-wide listing shares rangeFilter, so it must agree.
	code, body := f.do(t, http.MethodGet, "/api/v1/calendars/events?start=2028-01-01T00:00:00Z&end=2028-01-10T00:00:00Z", nil)
	if code != http.StatusOK {
		t.Fatalf("list all: %d %s", code, body)
	}
	if got := strings.Join(startsUTC(decodeList(t, body)), ","); got != "2028-01-06T00:00Z" {
		t.Fatalf("January (all calendars) = %s", got)
	}

	// Keeping the row is not the same as inventing occurrences: a window the
	// RDATE list does not touch stays empty, so the looser prefilter is not
	// hiding a false positive behind the Go-side overlap check.
	if got := startsUTC(list("?start=2027-11-10T00:00:00Z&end=2027-11-20T00:00:00Z")); len(got) != 0 {
		t.Fatalf("mid-November = %v, want nothing", got)
	}

	// RFC 5545 property names are case-insensitive, so a client that writes
	// "rdate" must behave exactly like one that writes "RDATE".
	seedRdateOnly(t, f, "Book club", "rdate")
	evs = list("?start=2028-01-01T00:00:00Z&end=2028-01-10T00:00:00Z")
	if got := strings.Join(startsUTC(evs), ","); got != "2028-01-06T00:00Z,2028-01-06T00:00Z" {
		t.Fatalf("January with a lower-case rdate = %s", got)
	}
}

// The seeded series carries rrule NULL and its instances only in ical_raw,
// which is the shape the old prefilter could not see. Keep that explicit so a
// future writer change does not quietly turn the regression test into a no-op.
func TestSeedRdateOnlyHasNoRule(t *testing.T) {
	f := newRecurrenceFixture(t, "America/New_York")
	id := seedRdateOnly(t, f, "No rule", "RDATE")
	row := f.row(t, id)
	if row.rrule != nil || row.icalRaw == nil || !strings.Contains(strings.ToUpper(*row.icalRaw), "RDATE") {
		t.Fatalf("seeded row: rrule=%v raw=%v", row.rrule, row.icalRaw)
	}
}
