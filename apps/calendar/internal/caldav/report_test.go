package caldav

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"home-os/calendar/internal/db"
	"home-os/calendar/internal/ical"
)

// TestFormatAndParseSyncToken verifies the round-trip between formatSyncToken
// and parseSyncTokenRevision for the boundary revisions sync-collection
// depends on (0 = no changes yet, and a typical mid-stream revision).
func TestFormatAndParseSyncToken(t *testing.T) {
	cases := []struct {
		name string
		rev  int64
	}{
		{"zero revision (initial state)", 0},
		{"one", 1},
		{"large revision", 9223372036854775807},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := formatSyncToken(tc.rev)
			got, ok := parseSyncTokenRevision(tok)
			if !ok {
				t.Fatalf("parseSyncTokenRevision(%q) = _, false, want true", tok)
			}
			if got != tc.rev {
				t.Errorf("parseSyncTokenRevision(%q) = %d, want %d", tok, got, tc.rev)
			}
		})
	}
}

// TestParseSyncTokenRevisionRejectsForeign confirms that tokens we did not
// emit (old CTag-based tokens, tokens from other servers, garbage) are
// rejected so the handler can fall back to a full initial sync.
func TestParseSyncTokenRevisionRejectsForeign(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"empty string", ""},
		{"old ctag token", "http://home-os.local/ns/sync/abc-uuid-ctag"},
		{"foreign server", "https://example.com/dav/sync/42"},
		{"bare integer", "42"},
		{"non-numeric revision", syncTokenPrefix + "not-a-number"},
		{"revision with trailing garbage", syncTokenPrefix + "42abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := parseSyncTokenRevision(tc.token); ok {
				t.Errorf("parseSyncTokenRevision(%q) = _, true, want false", tc.token)
			}
		})
	}
}

// TestExtractSyncToken covers the three request shapes Apple Calendar sends:
// no token (initial sync), an empty self-closing token (also initial sync),
// and a populated token (incremental sync). Namespace prefixes vary between
// clients so the matcher must rely on the local name only.
func TestExtractSyncToken(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "no sync-token element",
			body: `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:"><D:prop/></D:sync-collection>`,
			want: "",
		},
		{
			name: "empty self-closing token",
			body: `<?xml version="1.0"?><sync-collection xmlns="DAV:"><sync-token/><prop/></sync-collection>`,
			want: "",
		},
		{
			name: "empty paired token",
			body: `<?xml version="1.0"?><sync-collection><sync-token></sync-token></sync-collection>`,
			want: "",
		},
		{
			name: "populated token with D prefix",
			body: `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:"><D:sync-token>http://home-os.local/ns/sync/rev/42</D:sync-token></D:sync-collection>`,
			want: "http://home-os.local/ns/sync/rev/42",
		},
		{
			name: "populated token with whitespace",
			body: `<?xml version="1.0"?><sync-collection><sync-token>  http://home-os.local/ns/sync/rev/7  </sync-token></sync-collection>`,
			want: "http://home-os.local/ns/sync/rev/7",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractSyncToken(tc.body); got != tc.want {
				t.Errorf("extractSyncToken() = %q, want %q", got, tc.want)
			}
		})
	}
}

// weeklyFixture builds a weekly Tuesday 16:00 America/Chicago series that
// started on 2026-06-02, months before the "this week" ranges below. extra
// is spliced in after the RRULE (EXDATE, an override VEVENT, ...).
func weeklyFixture(uid, rrule, extra, overrides string) string {
	return "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//Test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:" + uid + "\r\n" +
		"DTSTAMP:20260601T120000Z\r\n" +
		"SUMMARY:Swim practice\r\n" +
		"DTSTART;TZID=America/Chicago:20260602T160000\r\n" +
		"DTEND;TZID=America/Chicago:20260602T170000\r\n" +
		"RRULE:" + rrule + "\r\n" +
		extra +
		"END:VEVENT\r\n" +
		overrides +
		"END:VCALENDAR\r\n"
}

// thisWeek is the week of 2026-11-16 (Mon) to 2026-11-23 in UTC. The series'
// instance falls on Tue 2026-11-17 16:00 CST = 22:00Z.
var (
	thisWeekStart = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	thisWeekEnd   = time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
)

func TestObjectInTimeRangeRecurring(t *testing.T) {
	const weekly = "FREQ=WEEKLY;BYDAY=TU"
	override := "BEGIN:VEVENT\r\n" +
		"UID:evt-weekly\r\n" +
		"DTSTAMP:20260601T120000Z\r\n" +
		"SUMMARY:Swim practice (moved)\r\n" +
		"RECURRENCE-ID;TZID=America/Chicago:20261110T160000\r\n" +
		"DTSTART;TZID=America/Chicago:20261118T160000\r\n" +
		"DTEND;TZID=America/Chicago:20261118T170000\r\n" +
		"END:VEVENT\r\n"
	cases := []struct {
		name       string
		body       string
		start, end time.Time
		want       bool
	}{
		{"weekly series created months ago, this week", weeklyFixture("evt-weekly", weekly, "", ""), thisWeekStart, thisWeekEnd, true},
		{"range before the series began", weeklyFixture("evt-weekly", weekly, "", ""),
			time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC), false},
		{"range between two instances", weeklyFixture("evt-weekly", weekly, "", ""),
			time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC), false},
		{"this week's instance EXDATE'd", weeklyFixture("evt-weekly", weekly, "EXDATE;TZID=America/Chicago:20261117T160000\r\n", ""), thisWeekStart, thisWeekEnd, false},
		{"EXDATE in UTC also removes it", weeklyFixture("evt-weekly", weekly, "EXDATE:20261117T220000Z\r\n", ""), thisWeekStart, thisWeekEnd, false},
		{"series ended by UNTIL", weeklyFixture("evt-weekly", weekly+";UNTIL=20260901T050000Z", "", ""), thisWeekStart, thisWeekEnd, false},
		{"series ended by COUNT", weeklyFixture("evt-weekly", weekly+";COUNT=4", "", ""), thisWeekStart, thisWeekEnd, false},
		{"open-ended range (no end)", weeklyFixture("evt-weekly", weekly, "", ""), thisWeekStart, time.Time{}, true},
		{"open-ended range after UNTIL", weeklyFixture("evt-weekly", weekly+";UNTIL=20260901T050000Z", "", ""), thisWeekStart, time.Time{}, false},
		{"instance moved into the range by an override", weeklyFixture("evt-weekly", weekly+";UNTIL=20261111T000000Z", "", override), thisWeekStart, thisWeekEnd, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj := storedFromPUT(t, "evt-weekly", tc.body)
			icalText, err := renderObjectICal(obj)
			if err != nil {
				t.Fatalf("renderObjectICal: %v", err)
			}
			if got := objectInTimeRange(obj, icalText, tc.start, tc.end); got != tc.want {
				t.Errorf("objectInTimeRange = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestObjectInTimeRangeSingle checks that non-recurring events keep the
// existing JSON start/end behaviour.
func TestObjectInTimeRangeSingle(t *testing.T) {
	mk := func(start time.Time) *db.CalendarObject {
		b, _ := json.Marshal(&ical.CalEvent{Title: "One-off", Start: start, End: start.Add(time.Hour)})
		return &db.CalendarObject{UID: "evt-single", ICALData: string(b), ETag: "e"}
	}
	cases := []struct {
		name string
		obj  *db.CalendarObject
		want bool
	}{
		{"inside this week", mk(time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)), true},
		{"months ago", mk(time.Date(2026, 6, 2, 21, 0, 0, 0, time.UTC)), false},
		{"next month", mk(time.Date(2026, 12, 18, 15, 0, 0, 0, time.UTC)), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			icalText, err := renderObjectICal(tc.obj)
			if err != nil {
				t.Fatal(err)
			}
			if got := objectInTimeRange(tc.obj, icalText, thisWeekStart, thisWeekEnd); got != tc.want {
				t.Errorf("objectInTimeRange = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExpandRecurringICal(t *testing.T) {
	body := weeklyFixture("evt-weekly", "FREQ=WEEKLY;BYDAY=TU", "EXDATE;TZID=America/Chicago:20261124T160000\r\n", "")
	got, ok := expandRecurringICal(body, thisWeekStart, thisWeekStart.AddDate(0, 0, 21))
	if !ok {
		t.Fatal("expandRecurringICal ok = false, want true")
	}
	u := unfold(got)
	// Three weeks, one EXDATE'd: 11-17 and 12-01, in UTC (CST = UTC-6).
	for _, want := range []string{
		"RECURRENCE-ID:20261117T220000Z", "DTSTART:20261117T220000Z", "DTEND:20261117T230000Z",
		"RECURRENCE-ID:20261201T220000Z", "SUMMARY:Swim practice",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("expanded output missing %q:\n%s", want, u)
		}
	}
	if n := strings.Count(u, "BEGIN:VEVENT"); n != 2 {
		t.Errorf("expanded VEVENT count = %d, want 2:\n%s", n, u)
	}
	for _, unwanted := range []string{"RRULE", "EXDATE", "TZID", "20261124"} {
		if strings.Contains(u, unwanted) {
			t.Errorf("expanded output contains %q:\n%s", unwanted, u)
		}
	}

	// A non-recurring object is left alone.
	single := strings.Replace(body, "RRULE:FREQ=WEEKLY;BYDAY=TU\r\n", "", 1)
	single = strings.Replace(single, "EXDATE;TZID=America/Chicago:20261124T160000\r\n", "", 1)
	if _, ok := expandRecurringICal(single, thisWeekStart, thisWeekEnd); ok {
		t.Error("expandRecurringICal on a single event: ok = true, want false")
	}
}

func TestExtractExpandRange(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		wantOK bool
	}{
		{"C prefix", `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:prop><C:calendar-data><C:expand start="20261116T000000Z" end="20261123T000000Z"/></C:calendar-data></D:prop></C:calendar-query>`, true},
		{"default namespace", `<calendar-query xmlns="urn:ietf:params:xml:ns:caldav"><prop xmlns="DAV:"><calendar-data xmlns="urn:ietf:params:xml:ns:caldav"><expand start="20261116T000000Z" end="20261123T000000Z"/></calendar-data></prop></calendar-query>`, true},
		{"no expand", `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:calendar-data/></C:calendar-query>`, false},
		{"missing end", `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:expand start="20261116T000000Z"/></C:calendar-query>`, false},
		{"end before start", `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:expand start="20261123T000000Z" end="20261116T000000Z"/></C:calendar-query>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, to, ok := extractExpandRange([]byte(tc.body))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && (!from.Equal(thisWeekStart) || !to.Equal(thisWeekEnd)) {
				t.Errorf("range = %v..%v, want %v..%v", from, to, thisWeekStart, thisWeekEnd)
			}
		})
	}
}

// TestCalendarQueryRecurringIntegration PUTs a weekly series that began in
// June and a one-off from June, then runs the calendar-query REPORT an
// Apple/Thunderbird client sends for this week: the series (master object,
// RRULE intact) must come back, the one-off must not. With <C:expand> the
// series comes back as this week's single UTC instance.
func TestCalendarQueryRecurringIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	hid, calUID, _ := seedCalendar(t, pool)
	colPath := "/dav/" + calUID + "/"

	weekly := weeklyFixture("evt-weekly", "FREQ=WEEKLY;BYDAY=TU", "", "")
	oneOff := strings.Replace(weeklyFixture("evt-oneoff", "X", "", ""), "RRULE:X\r\n", "", 1)
	for uid, body := range map[string]string{"evt-weekly": weekly, "evt-oneoff": oneOff} {
		path := colPath + uid + ".ics"
		w := httptest.NewRecorder()
		HandlePUT(w, davRequest(http.MethodPut, path, hid, body, nil), repo, path)
		if w.Code != http.StatusCreated {
			t.Fatalf("PUT %s = %d: %s", uid, w.Code, w.Body)
		}
	}

	query := func(calendarData string) string {
		t.Helper()
		body := `<?xml version="1.0" encoding="utf-8"?>` +
			`<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
			`<D:prop><D:getetag/>` + calendarData + `</D:prop>` +
			`<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT">` +
			`<C:time-range start="20261116T000000Z" end="20261123T000000Z"/>` +
			`</C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`
		w := httptest.NewRecorder()
		HandleREPORT(w, davRequest("REPORT", colPath, hid, body, nil), repo, colPath)
		if w.Code != http.StatusMultiStatus {
			t.Fatalf("REPORT = %d: %s", w.Code, w.Body)
		}
		return w.Body.String()
	}

	got := query(`<C:calendar-data/>`)
	if !strings.Contains(got, "<D:href>"+colPath+"evt-weekly.ics</D:href>") {
		t.Errorf("weekly series created months ago missing from this week's REPORT:\n%s", got)
	}
	if strings.Contains(got, "evt-oneoff") {
		t.Errorf("June one-off returned for this week:\n%s", got)
	}
	if !strings.Contains(got, "<![CDATA["+weekly+"]]>") {
		t.Errorf("REPORT without <C:expand> should return the master object verbatim:\n%s", got)
	}

	got = unfold(query(`<C:calendar-data><C:expand start="20261116T000000Z" end="20261123T000000Z"/></C:calendar-data>`))
	if !strings.Contains(got, "RECURRENCE-ID:20261117T220000Z") || strings.Contains(got, "RRULE:FREQ=WEEKLY") {
		t.Errorf("REPORT with <C:expand> should return this week's UTC instance:\n%s", got)
	}
}

// TestExtractTimeRangeFromXML covers the shapes clients actually send. The
// prefixed, self-closing form is what Apple Calendar and Thunderbird use.
func TestExtractTimeRangeFromXML(t *testing.T) {
	cases := []struct {
		name               string
		body               string
		wantStart, wantEnd time.Time
		wantOK             bool
	}{
		{"C prefix self-closing", `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"><C:time-range start="20261116T000000Z" end="20261123T000000Z"/></C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`, thisWeekStart, thisWeekEnd, true},
		{"no prefix, paired tags", `<calendar-query xmlns="urn:ietf:params:xml:ns:caldav"><filter><comp-filter name="VCALENDAR"><comp-filter name="VEVENT"><time-range start='20261116T000000Z' end='20261123T000000Z'></time-range></comp-filter></comp-filter></filter></calendar-query>`, thisWeekStart, thisWeekEnd, true},
		{"start only", `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:time-range start="20261116T000000Z"/></C:calendar-query>`, thisWeekStart, time.Time{}, true},
		{"end only", `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:time-range end="20261123T000000Z"/></C:calendar-query>`, time.Time{}, thisWeekEnd, true},
		{"no time-range", `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:filter><C:comp-filter name="VCALENDAR"/></C:filter></C:calendar-query>`, time.Time{}, time.Time{}, false},
		{"unparseable values", `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:time-range start="yesterday"/></C:calendar-query>`, time.Time{}, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end, ok := extractTimeRangeFromXML(tc.body)
			if ok != tc.wantOK || !start.Equal(tc.wantStart) || !end.Equal(tc.wantEnd) {
				t.Errorf("got (%v, %v, %v), want (%v, %v, %v)", start, end, ok, tc.wantStart, tc.wantEnd, tc.wantOK)
			}
		})
	}
}

// TestEventInTimeRangeBoundaries pins the RFC 4791 §9.9 overlap rule for
// single events: the range end is exclusive and an event that ends exactly at
// the range start does not overlap.
func TestEventInTimeRangeBoundaries(t *testing.T) {
	mk := func(start, end time.Time) string {
		b, _ := json.Marshal(&ical.CalEvent{Title: "x", Start: start, End: end})
		return string(b)
	}
	h := func(d, hr int) time.Time { return time.Date(2026, 11, d, hr, 0, 0, 0, time.UTC) }
	cases := []struct {
		name       string
		data       string
		start, end time.Time
		want       bool
	}{
		{"ends exactly at range start", mk(h(15, 23), h(16, 0)), thisWeekStart, thisWeekEnd, false},
		{"starts exactly at range end", mk(h(23, 0), h(23, 1)), thisWeekStart, thisWeekEnd, false},
		{"starts at range start", mk(h(16, 0), h(16, 1)), thisWeekStart, thisWeekEnd, true},
		{"spans the whole range", mk(h(10, 0), h(30, 0)), thisWeekStart, thisWeekEnd, true},
		{"zero-length at range start", mk(h(16, 0), h(16, 0)), thisWeekStart, thisWeekEnd, true},
		{"open-ended range, later event", mk(h(30, 0), h(30, 1)), thisWeekStart, time.Time{}, true},
		{"end-only range, earlier event", mk(h(1, 0), h(1, 1)), time.Time{}, thisWeekEnd, true},
		{"bad JSON", "{", thisWeekStart, thisWeekEnd, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := eventInTimeRange(tc.data, tc.start, tc.end); got != tc.want {
				t.Errorf("eventInTimeRange = %v, want %v", got, tc.want)
			}
		})
	}
}

// ----- XML-illegal characters in stored iCalendar (#4659) -----

func TestXMLSafeText(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "SUMMARY:Bin day\r\n", "SUMMARY:Bin day\r\n"},
		{"tab lf cr kept", "a\tb\nc\rd", "a\tb\nc\rd"},
		{"emoji and accents kept", "SUMMARY:Café 🎂 naïve\r\n", "SUMMARY:Café 🎂 naïve\r\n"},
		{"vertical tab and escape", "SUMMARY:a\x0Bb\x1Bc", "SUMMARY:a\uFFFDb\uFFFDc"},
		{"other C0", "\x01\x08\x0C\x0E\x1F", "\uFFFD\uFFFD\uFFFD\uFFFD\uFFFD"},
		{"nul", "a\x00b", "a\uFFFDb"},
		{"FFFE FFFF", "a\uFFFEb\uFFFFc", "a\uFFFDb\uFFFDc"},
		{"FFFD and BOM are legal", "\uFFFD\uFEFF", "\uFFFD\uFEFF"},
		{"DEL is legal", "a\x7Fb", "a\x7Fb"},
		{"invalid UTF-8", "a\xffb", "a\uFFFDb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := xmlSafeText(tt.in); got != tt.want {
				t.Errorf("xmlSafeText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// parseXMLDoc fails the test unless doc is well-formed XML, and returns the
// character data of every C:calendar-data element in document order.
func parseXMLDoc(t *testing.T, doc string) []string {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(doc))
	var out []string
	var cur *strings.Builder
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("response is not well-formed XML: %v\n%q", err, doc)
		}
		switch el := tok.(type) {
		case xml.StartElement:
			if el.Name.Space == nsC && el.Name.Local == "calendar-data" {
				cur = &strings.Builder{}
			}
		case xml.CharData:
			if cur != nil {
				cur.Write(el)
			}
		case xml.EndElement:
			if el.Name.Space == nsC && el.Name.Local == "calendar-data" && cur != nil {
				out = append(out, cur.String())
				cur = nil
			}
		}
	}
}

func TestWriteReportResponseNonXMLChars(t *testing.T) {
	text := "BEGIN:VCALENDAR\r\nSUMMARY:a\x0Bb\x1Bc\uFFFF\r\nDESCRIPTION:x]]>y \x01\r\nEND:VCALENDAR\r\n"
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">`)
	writeReportResponse(&b, "cal", "evt", "e", text)
	b.WriteString(`</D:multistatus>`)
	out := b.String()
	if !strings.Contains(out, "]]]]><![CDATA[>") {
		t.Errorf("]]> not split across CDATA sections:\n%s", out)
	}
	got := parseXMLDoc(t, out)
	want := "BEGIN:VCALENDAR\r\nSUMMARY:a\uFFFDb\uFFFDc\uFFFD\r\nDESCRIPTION:x]]>y \uFFFD\r\nEND:VCALENDAR\r\n"
	// encoding/xml normalises CRLF to LF in character data.
	want = strings.ReplaceAll(want, "\r\n", "\n")
	if len(got) != 1 || got[0] != want {
		t.Errorf("calendar-data = %q, want %q", got, want)
	}
}

// nonXMLFixture is a client VCALENDAR with accents and emoji (legal) whose
// SUMMARY is the placeholder TITLE.
func nonXMLFixture(uid, title string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\n" +
		"UID:" + uid + "\r\nDTSTAMP:20261101T000000Z\r\nDTSTART:20261120T070000Z\r\n" +
		"DTEND:20261120T073000Z\r\nSUMMARY:" + title + "\r\n" +
		"DESCRIPTION:Crème brûlée 🎂 for Zoë\\, naïve façade\r\nX-CUSTOM:kept ✓\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
}

// TestCalDAVNonXMLCharsIntegration: PUT refuses XML-illegal runes and keeps
// valid UTF-8 byte for byte; rows stored before that check (raw, and
// JSON-only rows edited by the API) still produce well-formed REPORT and
// PROPFIND multistatus responses that include the event.
func TestCalDAVNonXMLCharsIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	ctx := context.Background()
	hid, calUID, calID := seedCalendar(t, pool)
	colPath := "/dav/" + calUID + "/"

	put := func(uid, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		HandlePUT(w, davRequest(http.MethodPut, colPath+uid+".ics", hid, body, nil), repo, colPath+uid+".ics")
		return w
	}

	// PUT with \x0B / \x1B / U+FFFF is refused and nothing is stored.
	for name, title := range map[string]string{"vt": "a\x0Bb", "esc": "a\x1Bb", "ffff": "a\uFFFFb"} {
		w := put("evt-refused", nonXMLFixture("evt-refused", title))
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "<C:valid-calendar-data/>") {
			t.Errorf("%s: PUT = %d %s, want 403 valid-calendar-data", name, w.Code, w.Body)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM calendar_objects WHERE calendar_id = $1 AND uid = 'evt-refused'`, calID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("refused PUT stored %d rows", n)
	}

	// Valid UTF-8 (accents, emoji) round-trips byte for byte.
	good := nonXMLFixture("evt-good", "Zoë's 🎉 party")
	if w := put("evt-good", good); w.Code != http.StatusCreated {
		t.Fatalf("PUT good = %d: %s", w.Code, w.Body)
	}
	var raw string
	if err := pool.QueryRow(ctx, `SELECT ical_raw FROM calendar_objects WHERE calendar_id = $1 AND uid = 'evt-good'`, calID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != good {
		t.Fatalf("ical_raw not byte-exact:\n got %q\nwant %q", raw, good)
	}

	// Rows as stored before #4659: one raw (served verbatim), one JSON-only
	// (generated from ical_data). Put them in through valid PUTs, then put
	// the control characters into the stored text directly.
	const bad = "Bad\x0Bsum\x1Bmary"
	for _, uid := range []string{"evt-badraw", "evt-badjson"} {
		if w := put(uid, nonXMLFixture(uid, "PLACEHOLDER")); w.Code != http.StatusCreated {
			t.Fatalf("PUT %s = %d: %s", uid, w.Code, w.Body)
		}
		var jsonData string
		if err := pool.QueryRow(ctx, `SELECT ical_data FROM calendar_objects WHERE calendar_id = $1 AND uid = $2`, calID, uid).Scan(&jsonData); err != nil {
			t.Fatal(err)
		}
		var ev ical.CalEvent
		if err := json.Unmarshal([]byte(jsonData), &ev); err != nil {
			t.Fatal(err)
		}
		ev.Title = bad
		b, _ := json.Marshal(&ev)
		q := `UPDATE calendar_objects SET ical_data = $1, ical_raw = replace(ical_raw, 'PLACEHOLDER', $2) WHERE calendar_id = $3 AND uid = $4`
		if uid == "evt-badjson" {
			q = `UPDATE calendar_objects SET ical_data = $1, ical_raw = NULL WHERE calendar_id = $3 AND uid = $4 AND $2::text IS NOT NULL`
		}
		if _, err := pool.Exec(ctx, q, string(b), bad, calID, uid); err != nil {
			t.Fatal(err)
		}
	}

	// GET keeps the stored bytes (text/calendar, not XML).
	w := httptest.NewRecorder()
	HandleGET(w, davRequest(http.MethodGet, colPath+"evt-badraw.ics", hid, "", nil), repo, colPath+"evt-badraw.ics")
	if want := nonXMLFixture("evt-badraw", bad); w.Body.String() != want {
		t.Errorf("GET of pre-fix raw row changed:\n got %q\nwant %q", w.Body, want)
	}

	hrefs := func(body string) []string {
		var out []string
		for _, uid := range []string{"evt-good", "evt-badraw", "evt-badjson"} {
			if strings.Contains(body, "<D:href>"+colPath+uid+".ics</D:href>") {
				out = append(out, uid)
			}
		}
		return out
	}
	check := func(name, body string) {
		t.Helper()
		datas := parseXMLDoc(t, body)
		if got := hrefs(body); len(got) != 3 {
			t.Errorf("%s: events returned = %v, want all three:\n%s", name, got, body)
		}
		var sawBad, sawGood int
		for _, d := range datas {
			if strings.Contains(d, "SUMMARY:Bad\uFFFDsum\uFFFDmary") {
				sawBad++
			}
			if strings.Contains(d, "SUMMARY:Zoë's 🎉 party") && strings.Contains(d, "Crème brûlée 🎂 for Zoë") {
				sawGood++
			}
		}
		if sawBad != 2 || sawGood != 1 {
			t.Errorf("%s: calendar-data bad=%d good=%d, want 2 and 1:\n%q", name, sawBad, sawGood, datas)
		}
		// The valid client body is still served verbatim inside the XML.
		if !strings.Contains(body, "<![CDATA["+good+"]]>") {
			t.Errorf("%s: valid raw body not served byte for byte", name)
		}
	}

	hrefList := ""
	for _, uid := range []string{"evt-good", "evt-badraw", "evt-badjson"} {
		hrefList += "<D:href>" + colPath + uid + ".ics</D:href>"
	}
	for name, body := range map[string]string{
		"calendar-query": `<?xml version="1.0"?><C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:getetag/><C:calendar-data/></D:prop><C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"><C:time-range start="20261119T000000Z" end="20261121T000000Z"/></C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`,
		"multiget":       `<?xml version="1.0"?><C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:getetag/><C:calendar-data/></D:prop>` + hrefList + `</C:calendar-multiget>`,
		"sync":           `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:sync-token/><D:prop><C:calendar-data/></D:prop></D:sync-collection>`,
	} {
		w := httptest.NewRecorder()
		HandleREPORT(w, davRequest("REPORT", colPath, hid, body, nil), repo, colPath)
		if w.Code != http.StatusMultiStatus {
			t.Fatalf("%s REPORT = %d: %s", name, w.Code, w.Body)
		}
		check(name, w.Body.String())
	}

	w = httptest.NewRecorder()
	HandlePROPFIND(w, davRequest("PROPFIND", colPath, hid, propfindEventPropsBody, map[string]string{"Depth": "1"}), repo, colPath)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND = %d: %s", w.Code, w.Body)
	}
	check("propfind", w.Body.String())
}
