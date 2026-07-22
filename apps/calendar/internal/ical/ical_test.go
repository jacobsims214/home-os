package ical

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseEvent_Simple(t *testing.T) {
	icalData := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Home OS//Calendar//EN
BEGIN:VEVENT
UID:test-uid-123@homeos
DTSTART:20260607T100000Z
DTEND:20260607T113000Z
SUMMARY:Emma's Soccer Game
DESCRIPTION:Springfield Youth League — home game
LOCATION:Riverside Park, Springfield
END:VEVENT
END:VCALENDAR`

	ev, err := ParseEvent(icalData)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}

	if ev.UID != "test-uid-123@homeos" {
		t.Errorf("UID = %q, want %q", ev.UID, "test-uid-123@homeos")
	}
	if ev.Title != "Emma's Soccer Game" {
		t.Errorf("Title = %q, want %q", ev.Title, "Emma's Soccer Game")
	}
	if ev.Description != "Springfield Youth League — home game" {
		t.Errorf("Description = %q, want %q", ev.Description, "Springfield Youth League — home game")
	}
	if ev.Location != "Riverside Park, Springfield" {
		t.Errorf("Location = %q, want %q", ev.Location, "Riverside Park, Springfield")
	}
	if ev.AllDay {
		t.Errorf("AllDay = true, want false")
	}

	wantStart := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	if !ev.Start.Equal(wantStart) {
		t.Errorf("Start = %v, want %v", ev.Start, wantStart)
	}
	wantEnd := time.Date(2026, 6, 7, 11, 30, 0, 0, time.UTC)
	if !ev.End.Equal(wantEnd) {
		t.Errorf("End = %v, want %v", ev.End, wantEnd)
	}
}

func TestParseEvent_AllDay(t *testing.T) {
	icalData := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Home OS//Calendar//EN
BEGIN:VEVENT
UID:all-day-test@homeos
DTSTART;VALUE=DATE:20260607
DTEND;VALUE=DATE:20260608
SUMMARY:All Day Event
END:VEVENT
END:VCALENDAR`

	ev, err := ParseEvent(icalData)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}

	if !ev.AllDay {
		t.Errorf("AllDay = false, want true")
	}
	wantStart := time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)
	if !ev.Start.Equal(wantStart) {
		t.Errorf("Start = %v, want %v", ev.Start, wantStart)
	}
	wantEnd := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
	if !ev.End.Equal(wantEnd) {
		t.Errorf("End = %v, want %v", ev.End, wantEnd)
	}
}

func TestParseEvent_MissingUID(t *testing.T) {
	icalData := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Home OS//Calendar//EN
BEGIN:VEVENT
DTSTART:20260607T100000Z
DTEND:20260607T110000Z
SUMMARY:No UID
END:VEVENT
END:VCALENDAR`

	ev, err := ParseEvent(icalData)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.UID == "" {
		t.Errorf("UID should have a default value")
	}
}

func TestParseEvent_MissingDTEND(t *testing.T) {
	icalData := `BEGIN:VCALENDAR
BEGIN:VEVENT
DTSTART:20260607T100000Z
SUMMARY:No End
END:VEVENT
END:VCALENDAR`

	ev, err := ParseEvent(icalData)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	wantEnd := ev.Start.Add(time.Hour)
	if !ev.End.Equal(wantEnd) {
		t.Errorf("End = %v, want %v (1 hour after start)", ev.End, wantEnd)
	}
}

func TestParseEvent_MissingDTSTART(t *testing.T) {
	icalData := `BEGIN:VCALENDAR
BEGIN:VEVENT
SUMMARY:No Start
END:VEVENT
END:VCALENDAR`

	_, err := ParseEvent(icalData)
	if err == nil {
		t.Fatal("ParseEvent: expected error for missing DTSTART, got nil")
	}
}

func TestParseEvent_FoldedLines(t *testing.T) {
	// RFC 5545 continuation line: long lines are folded with leading whitespace
	icalData := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Home OS//Calendar//EN
BEGIN:VEVENT
UID:folded-test@homeos
DTSTART:20260607T100000Z
DTEND:20260607T110000Z
SUMMARY:This is a very long summary that spans
  multiple lines in the iCalendar format
END:VEVENT
END:VCALENDAR`

	ev, err := ParseEvent(icalData)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	want := "This is a very long summary that spans multiple lines in the iCalendar format"
	if ev.Title != want {
		t.Errorf("Title = %q, want %q", ev.Title, want)
	}
}

func TestGenerateEvent_Simple(t *testing.T) {
	ev := &CalEvent{
		UID:         "gen-test@homeos",
		Title:       "Test Event",
		Description: "A generated event",
		Location:    "Home",
		Start:       time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC),
		End:         time.Date(2026, 6, 7, 11, 0, 0, 0, time.UTC),
	}

	result := GenerateEvent(ev)

	// Verify structure
	if !strings.HasPrefix(result, "BEGIN:VCALENDAR\r\n") {
		t.Errorf("Result should start with BEGIN:VCALENDAR")
	}
	if !strings.HasSuffix(result, "END:VCALENDAR\r\n") {
		t.Errorf("Result should end with END:VCALENDAR")
	}
	if !strings.Contains(result, "BEGIN:VEVENT\r\n") {
		t.Errorf("Result should contain BEGIN:VEVENT")
	}
	if !strings.Contains(result, "END:VEVENT\r\n") {
		t.Errorf("Result should contain END:VEVENT")
	}
	if !strings.Contains(result, "UID:gen-test@homeos\r\n") {
		t.Errorf("Result should contain UID")
	}
	if !strings.Contains(result, "DTSTART:20260607T100000Z\r\n") {
		t.Errorf("Result should contain DTSTART in UTC format")
	}
	if !strings.Contains(result, "DTEND:20260607T110000Z\r\n") {
		t.Errorf("Result should contain DTEND in UTC format")
	}
	if !strings.Contains(result, "SUMMARY:Test Event\r\n") {
		t.Errorf("Result should contain SUMMARY")
	}
	if !strings.Contains(result, "PRODID:-//Home OS//Calendar//EN\r\n") {
		t.Errorf("Result should contain PRODID")
	}
}

func TestGenerateEvent_AllDay(t *testing.T) {
	ev := &CalEvent{
		UID:    "allday-gen@homeos",
		Title:  "All Day Event",
		Start:  time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC),
		End:    time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		AllDay: true,
	}

	result := GenerateEvent(ev)

	if !strings.Contains(result, "DTSTART;VALUE=DATE:20260607\r\n") {
		t.Errorf("All-day events should use DTSTART;VALUE=DATE format")
	}
	if !strings.Contains(result, "DTEND;VALUE=DATE:20260608\r\n") {
		t.Errorf("All-day events should use DTEND;VALUE=DATE format")
	}
}

func TestGenerateEvent_EmptyFields(t *testing.T) {
	ev := &CalEvent{
		UID:   "empty-fields@homeos",
		Title: "",
		Start: time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 7, 11, 0, 0, 0, time.UTC),
	}

	result := GenerateEvent(ev)

	if strings.Contains(result, "SUMMARY:\r\n") {
		t.Errorf("Empty SUMMARY should be omitted")
	}
}

func TestGenerateCalendar_MultipleEvents(t *testing.T) {
	ev1 := &CalEvent{
		UID:   "event1@homeos",
		Title: "Event One",
		Start: time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 7, 11, 0, 0, 0, time.UTC),
	}
	ev2 := &CalEvent{
		UID:   "event2@homeos",
		Title: "Event Two",
		Start: time.Date(2026, 6, 8, 14, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 8, 15, 0, 0, 0, time.UTC),
	}

	result := GenerateCalendar([]*CalEvent{ev1, ev2})

	// Should have exactly 2 VEVENT blocks
	count := strings.Count(result, "BEGIN:VEVENT")
	if count != 2 {
		t.Errorf("Expected 2 VEVENT blocks, got %d", count)
	}
	if !strings.Contains(result, "Event One") || !strings.Contains(result, "Event Two") {
		t.Errorf("Calendar should contain both event titles")
	}
}

func TestParseEvent_EscapedText(t *testing.T) {
	icalData := `BEGIN:VCALENDAR
BEGIN:VEVENT
UID:escaped@homeos
DTSTART:20260607T100000Z
DTEND:20260607T110000Z
SUMMARY:Test\, Semicolon\; Backslash\\ Newline\nSecond line
END:VEVENT
END:VCALENDAR`

	ev, err := ParseEvent(icalData)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}

	want := "Test, Semicolon; Backslash\\ Newline\nSecond line"
	if ev.Title != want {
		t.Errorf("Title = %q, want %q", ev.Title, want)
	}
}

// TestUnescapeText_BackslashEscapeRoundTrip locks in the fix for the bug where
// sequential strings.ReplaceAll calls in unescapeText corrupted text containing
// a literal backslash followed by n/N/;/, — the exact combinations where the
// \n / \; / \, replacements overlapped with the escaped-backslash sequence.
// See discoveries/ical-unescape-text-bug.md.
func TestUnescapeText_BackslashEscapeRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		// raw is the original (unescaped) text — what should survive a
		// full escape → unescape round trip.
		raw string
	}{
		{"backslash-n (literal \\n)", "path\\nfile"},
		{"backslash-N (literal \\N)", "path\\Nfile"},
		{"backslash-semicolon (literal \\;)", "path\\;file"},
		{"backslash-comma (literal \\,)", "path\\,file"},
		{"regex example \\d+\\.\\w+", `C:\Users\new\nested`},
		{"mixed: \\n plus newline plus \\;", "line1\\nX\nY\\;Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			escaped := escapeText(tc.raw)
			got := unescapeText(escaped)
			if got != tc.raw {
				t.Errorf("round-trip mismatch\n  raw:     %q\n  escaped: %q\n  got:     %q", tc.raw, escaped, got)
			}
		})
	}
}

// TestUnescapeText_Direct verifies unescapeText against hand-written escaped
// inputs whose expected unescaped form is known from RFC 5545 §3.3.11.
func TestUnescapeText_Direct(t *testing.T) {
	cases := []struct {
		escaped string
		want    string
	}{
		{`a\\nb`, `a\nb`},       // escaped backslash + letter n → literal \n
		{`a\\;b`, `a\;b`},        // escaped backslash + semicolon → literal \;
		{`a\\,b`, `a\,b`},        // escaped backslash + comma → literal \,
		{`a\\Nb`, `a\Nb`},        // escaped backslash + letter N → literal \N
		{`line1\nline2`, "line1\nline2"}, // \n escape → newline
		{`line1\nLine2`, "line1\nLine2"}, // \N escape → newline (case-insensitive)
		{`semi\;colon`, `semi;colon`},    // \; escape → semicolon
		{`com\,ma`, `com,ma`},            // \, escape → comma
		{`back\\slash`, `back\slash`},    // \\ escape → single backslash
		{`trailing\`, `trailing\`},       // lone trailing backslash preserved
	}
	for _, tc := range cases {
		t.Run(tc.escaped, func(t *testing.T) {
			got := unescapeText(tc.escaped)
			if got != tc.want {
				t.Errorf("unescapeText(%q) = %q, want %q", tc.escaped, got, tc.want)
			}
		})
	}
}

func TestGenerateEvent_EscapedText(t *testing.T) {
	ev := &CalEvent{
		UID:   "escape-gen@homeos",
		Title: "Test, Semicolon; Backslash\\ Newline\nSecond line",
		Start: time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 7, 11, 0, 0, 0, time.UTC),
	}

	result := GenerateEvent(ev)

	// The generated text should have escaped characters
	if !strings.Contains(result, "Test\\,") {
		t.Errorf("Comma should be escaped")
	}
	if !strings.Contains(result, "Semicolon\\;") {
		t.Errorf("Semicolon should be escaped")
	}
	if !strings.Contains(result, "Backslash\\\\") {
		t.Errorf("Backslash should be escaped")
	}
	if !strings.Contains(result, "Newline\\n") {
		t.Errorf("Newline should be escaped as \\n")
	}
}

func TestRoundTrip(t *testing.T) {
	original := &CalEvent{
		UID:         "roundtrip@homeos",
		Title:       "Round Trip Test",
		Description: "Description with special chars: comma, semicolon; backslash\\ newline\nhere",
		Location:    "Living Room",
		Start:       time.Date(2026, 7, 4, 15, 30, 0, 0, time.UTC),
		End:         time.Date(2026, 7, 4, 17, 0, 0, 0, time.UTC),
	}

	ical := GenerateEvent(original)
	parsed, err := ParseEvent(ical)
	if err != nil {
		t.Fatalf("Round-trip ParseEvent: %v", err)
	}

	if parsed.UID != original.UID {
		t.Errorf("UID: got %q, want %q", parsed.UID, original.UID)
	}
	if parsed.Title != original.Title {
		t.Errorf("Title: got %q, want %q", parsed.Title, original.Title)
	}
	if parsed.Description != original.Description {
		t.Errorf("Description: got %q, want %q", parsed.Description, original.Description)
	}
	if parsed.Location != original.Location {
		t.Errorf("Location: got %q, want %q", parsed.Location, original.Location)
	}
	if !parsed.Start.Equal(original.Start) {
		t.Errorf("Start: got %v, want %v", parsed.Start, original.Start)
	}
	if !parsed.End.Equal(original.End) {
		t.Errorf("End: got %v, want %v", parsed.End, original.End)
	}
	if parsed.AllDay {
		t.Errorf("AllDay should be false")
	}
}

func TestRoundTrip_AllDay(t *testing.T) {
	original := &CalEvent{
		UID:    "allday-rt@homeos",
		Title:  "All Day Round Trip",
		Start:  time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC),
		End:    time.Date(2026, 12, 26, 0, 0, 0, 0, time.UTC),
		AllDay: true,
	}

	ical := GenerateEvent(original)
	parsed, err := ParseEvent(ical)
	if err != nil {
		t.Fatalf("Round-trip all-day ParseEvent: %v", err)
	}

	if !parsed.AllDay {
		t.Errorf("AllDay should be true")
	}
	if !parsed.Start.Equal(original.Start) {
		t.Errorf("Start: got %v, want %v", parsed.Start, original.Start)
	}
	if !parsed.End.Equal(original.End) {
		t.Errorf("End: got %v, want %v", parsed.End, original.End)
	}
}

func TestParseRRULE(t *testing.T) {
	rrule := "FREQ=WEEKLY;BYDAY=MO,WE,FR;INTERVAL=2"
	result := ParseRRULE(rrule)

	if result["FREQ"] != "WEEKLY" {
		t.Errorf("FREQ = %q, want %q", result["FREQ"], "WEEKLY")
	}
	if result["BYDAY"] != "MO,WE,FR" {
		t.Errorf("BYDAY = %q, want %q", result["BYDAY"], "MO,WE,FR")
	}
	if result["INTERVAL"] != "2" {
		t.Errorf("INTERVAL = %q, want %q", result["INTERVAL"], "2")
	}
}

func TestFormatRRULE(t *testing.T) {
	rules := map[string]string{
		"FREQ":       "MONTHLY",
		"BYMONTHDAY": "15",
	}
	result := FormatRRULE(rules)

	if !strings.Contains(result, "FREQ=MONTHLY") {
		t.Errorf("FormatRRULE missing FREQ")
	}
	if !strings.Contains(result, "BYMONTHDAY=15") {
		t.Errorf("FormatRRULE missing BYMONTHDAY")
	}
}

func TestFormatRRULE_Empty(t *testing.T) {
	if s := FormatRRULE(nil); s != "" {
		t.Errorf("FormatRRULE(nil) = %q, want empty", s)
	}
	if s := FormatRRULE(map[string]string{}); s != "" {
		t.Errorf("FormatRRULE({}) = %q, want empty", s)
	}
}

func TestFormatDate(t *testing.T) {
	tm := time.Date(2026, 6, 7, 10, 5, 3, 0, time.UTC)
	result := FormatDate(tm)
	want := "20260607T100503Z"
	if result != want {
		t.Errorf("FormatDate = %q, want %q", result, want)
	}
}

func TestFormatDateAllDay(t *testing.T) {
	tm := time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)
	result := FormatDateAllDay(tm)
	want := "20260607"
	if result != want {
		t.Errorf("FormatDateAllDay = %q, want %q", result, want)
	}
}

func TestParseEvent_EmptyCalendarBody(t *testing.T) {
	// VCALENDAR with no VEVENT should fail (missing DTSTART)
	icalData := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Home OS//Calendar//EN
END:VCALENDAR`

	_, err := ParseEvent(icalData)
	if err == nil {
		t.Fatal("ParseEvent: expected error for empty calendar, got nil")
	}
}

func TestUnfoldLines(t *testing.T) {
	data := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nSUMMARY:Long line\r\n  continuation\r\nEND:VEVENT\r\nEND:VCALENDAR"
	lines := unfoldLines(data)

	found := false
	for _, line := range lines {
		if strings.HasPrefix(line, "SUMMARY:") {
			found = true
			want := "SUMMARY:Long line continuation"
			if line != want {
				t.Errorf("unfolded line = %q, want %q", line, want)
			}
		}
	}
	if !found {
		t.Errorf("unfolded lines missing SUMMARY")
	}
}

func TestParsePropertyLine(t *testing.T) {
	prop, params, value := parsePropertyLine("DTSTART;VALUE=DATE:20260607")
	if prop != "DTSTART" {
		t.Errorf("prop = %q, want %q", prop, "DTSTART")
	}
	if params["VALUE"] != "DATE" {
		t.Errorf("params[VALUE] = %q, want %q", params["VALUE"], "DATE")
	}
	if value != "20260607" {
		t.Errorf("value = %q, want %q", value, "20260607")
	}
}

func TestParsePropertyLine_NoParams(t *testing.T) {
	prop, params, value := parsePropertyLine("SUMMARY:Test Event")
	if prop != "SUMMARY" {
		t.Errorf("prop = %q, want %q", prop, "SUMMARY")
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got %v", params)
	}
	if value != "Test Event" {
		t.Errorf("value = %q, want %q", value, "Test Event")
	}
}

func TestParsePropertyLine_Empty(t *testing.T) {
	prop, _, _ := parsePropertyLine("")
	if prop != "" {
		t.Errorf("expected empty prop for empty line")
	}
}

// TestGenerateEvent_SequenceAndDTStamp verifies that SEQUENCE is emitted from
// the CalEvent.Sequence field (not hardcoded to 0) and that DTSTAMP and
// LAST-MODIFIED are sourced from ev.DTStamp when set, so the same stored event
// produces byte-identical output on every GET.
func TestGenerateEvent_SequenceAndDTStamp(t *testing.T) {
	dt := time.Date(2026, 6, 20, 12, 30, 0, 0, time.UTC)
	ev := &CalEvent{
		UID:      "seq-test@homeos",
		Title:    "Seq Test",
		Start:    time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC),
		End:      time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC),
		Sequence: 3,
		DTStamp:  dt,
	}

	out := GenerateEvent(ev)

	wantSeq := "SEQUENCE:3\r\n"
	if !strings.Contains(out, wantSeq) {
		t.Errorf("output missing %q; output:\n%s", wantSeq, out)
	}
	wantDTStamp := "DTSTAMP:20260620T123000Z\r\n"
	if !strings.Contains(out, wantDTStamp) {
		t.Errorf("output missing %q; output:\n%s", wantDTStamp, out)
	}
	wantLastMod := "LAST-MODIFIED:20260620T123000Z\r\n"
	if !strings.Contains(out, wantLastMod) {
		t.Errorf("output missing %q; output:\n%s", wantLastMod, out)
	}
}

// TestGenerateEvent_SequenceDefaultsToZero confirms a brand-new event with no
// Sequence set emits SEQUENCE:0 (the struct zero value), which CalDAV clients
// interpret as the first revision.
func TestGenerateEvent_SequenceDefaultsToZero(t *testing.T) {
	ev := &CalEvent{
		UID:   "new-event@homeos",
		Start: time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC),
	}
	out := GenerateEvent(ev)
	if !strings.Contains(out, "SEQUENCE:0\r\n") {
		t.Errorf("new event should emit SEQUENCE:0; output:\n%s", out)
	}
}

// TestGenerateEvent_DTStampStableAcrossCalls confirms that calling GenerateEvent
// twice on the same CalEvent (with DTStamp set) produces identical DTSTAMP and
// LAST-MODIFIED values. This is the fix for the Apple Calendar re-sync loop.
func TestGenerateEvent_DTStampStableAcrossCalls(t *testing.T) {
	ev := &CalEvent{
		UID:     "stable@homeos",
		Start:   time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC),
		End:     time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC),
		DTStamp: time.Date(2026, 6, 20, 12, 30, 0, 0, time.UTC),
	}
	first := GenerateEvent(ev)
	second := GenerateEvent(ev)
	if first != second {
		t.Errorf("GenerateEvent output is not stable across calls for the same event:\nfirst:  %q\nsecond: %q", first, second)
	}
}

// TestCalEvent_SequencePersistedInJSON confirms that SEQUENCE is marshaled into
// JSON under the snake_case `sequence` key (so it survives a write/read
// round-trip through the DB), and that unmarshaling restores it.
func TestCalEvent_SequencePersistedInJSON(t *testing.T) {
	ev := &CalEvent{
		Title:    "Persist Test",
		Sequence: 7,
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"sequence":7`) {
		t.Errorf("JSON output missing `\"sequence\":7`: %s", data)
	}

	var roundTripped CalEvent
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if roundTripped.Sequence != 7 {
		t.Errorf("Sequence round-trip = %d, want 7", roundTripped.Sequence)
	}
}

// TestCalEvent_DTStampNotPersistedInJSON confirms that DTStamp (tagged json:"-")
// is NOT serialized, because it is sourced from the DB updated_at column at
// GET/REPORT time rather than from stored JSON.
func TestCalEvent_DTStampNotPersistedInJSON(t *testing.T) {
	ev := &CalEvent{
		Title:   "No DTStamp Persist",
		DTStamp: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "dtstamp") {
		t.Errorf("DTStamp should not be serialized, but found in JSON: %s", data)
	}
}

// TestParseEvent_TZID verifies that DTSTART;TZID=America/New_York:... is
// parsed into the correct UTC instant. America/New_York on 2026-06-20 is
// EDT (-0400), so 14:30 local = 18:30 UTC.
func TestParseEvent_TZID(t *testing.T) {
	icalData := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Home OS//Calendar//EN
BEGIN:VEVENT
UID:tzid-test@homeos
DTSTART;TZID=America/New_York:20260620T143000
DTEND;TZID=America/New_York:20260620T153000
SUMMARY:Dentist
END:VEVENT
END:VCALENDAR`

	ev, err := ParseEvent(icalData)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}

	wantStart := time.Date(2026, 6, 20, 18, 30, 0, 0, time.UTC)
	if !ev.Start.Equal(wantStart) {
		t.Errorf("Start = %v, want %v (NY 14:30 EDT = 18:30 UTC)", ev.Start, wantStart)
	}
	wantEnd := time.Date(2026, 6, 20, 19, 30, 0, 0, time.UTC)
	if !ev.End.Equal(wantEnd) {
		t.Errorf("End = %v, want %v", ev.End, wantEnd)
	}
	if ev.Timezone != "America/New_York" {
		t.Errorf("Timezone = %q, want %q", ev.Timezone, "America/New_York")
	}
}

// TestParseEvent_TZID_WithVTimezone verifies that a VTIMEZONE block in the
// input is parsed and the IANA TZID it declares resolves via time.LoadLocation.
// Apple Calendar always sends VTIMEZONE alongside TZID parameters.
func TestParseEvent_TZID_WithVTimezone(t *testing.T) {
	icalData := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Home OS//Calendar//EN
BEGIN:VTIMEZONE
TZID:America/New_York
BEGIN:STANDARD
DTSTART:19701101T020000
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
END:STANDARD
BEGIN:DAYLIGHT
DTSTART:19700308T020000
TZOFFSETFROM:-0500
TZOFFSETTO:-0400
END:DAYLIGHT
END:VTIMEZONE
BEGIN:VEVENT
UID:tzid-vtz@homeos
DTSTART;TZID=America/New_York:20260620T143000
DTEND;TZID=America/New_York:20260620T153000
SUMMARY:With VTIMEZONE
END:VEVENT
END:VCALENDAR`

	ev, err := ParseEvent(icalData)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}

	wantStart := time.Date(2026, 6, 20, 18, 30, 0, 0, time.UTC)
	if !ev.Start.Equal(wantStart) {
		t.Errorf("Start = %v, want %v", ev.Start, wantStart)
	}
	if ev.Timezone != "America/New_York" {
		t.Errorf("Timezone = %q, want America/New_York", ev.Timezone)
	}
}

// TestParseEvent_TZID_FixedOffsetVTimezone verifies the VTIMEZONE fallback
// path: a non-IANA TZID is resolved from the TZOFFSETTO of the STANDARD
// subcomponent as a fixed offset. "Custom/Eastern" is not an IANA zone, so
// time.LoadLocation fails and we build a FixedZone from -0500.
func TestParseEvent_TZID_FixedOffsetVTimezone(t *testing.T) {
	icalData := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Home OS//Calendar//EN
BEGIN:VTIMEZONE
TZID:Custom/Eastern
BEGIN:STANDARD
DTSTART:19701101T020000
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
UID:tzid-fixed@homeos
DTSTART;TZID=Custom/Eastern:20260620T143000
DTEND;TZID=Custom/Eastern:20260620T153000
SUMMARY:Fixed offset TZID
END:VEVENT
END:VCALENDAR`

	ev, err := ParseEvent(icalData)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}

	// -0500 offset → 14:30 local = 19:30 UTC
	wantStart := time.Date(2026, 6, 20, 19, 30, 0, 0, time.UTC)
	if !ev.Start.Equal(wantStart) {
		t.Errorf("Start = %v, want %v (Custom/Eastern -05:00)", ev.Start, wantStart)
	}
	if ev.Timezone != "Custom/Eastern" {
		t.Errorf("Timezone = %q, want Custom/Eastern", ev.Timezone)
	}
}

// TestGenerateEvent_TZID verifies that GenerateEvent emits DTSTART;TZID=...
// in local wall-clock form when ev.Timezone is set to a non-UTC IANA zone.
func TestGenerateEvent_TZID(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	// 2026-06-20T18:30:00Z = 14:30 EDT
	startUTC := time.Date(2026, 6, 20, 18, 30, 0, 0, time.UTC)
	endUTC := time.Date(2026, 6, 20, 19, 30, 0, 0, time.UTC)
	wantLocal := startUTC.In(loc).Format(localTimeFmt) // "20260620T143000"

	ev := &CalEvent{
		UID:      "gen-tzid@homeos",
		Title:    "Generate TZID",
		Start:    startUTC,
		End:      endUTC,
		Timezone: "America/New_York",
	}

	result := GenerateEvent(ev)

	if !strings.Contains(result, "DTSTART;TZID=America/New_York:"+wantLocal+"\r\n") {
		t.Errorf("DTSTART should use TZID with local wall-clock time\ngot:\n%s", result)
	}
	if !strings.Contains(result, "DTEND;TZID=America/New_York:") {
		t.Errorf("DTEND should use TZID")
	}
}

// TestGenerateEvent_TimezoneUTC verifies that an explicitly-set "UTC" Timezone
// falls back to the plain UTC datetime form (no TZID parameter emitted).
func TestGenerateEvent_TimezoneUTC(t *testing.T) {
	ev := &CalEvent{
		UID:      "utc-tz@homeos",
		Start:    time.Date(2026, 6, 20, 18, 30, 0, 0, time.UTC),
		End:      time.Date(2026, 6, 20, 19, 30, 0, 0, time.UTC),
		Timezone: "UTC",
	}
	result := GenerateEvent(ev)
	if strings.Contains(result, "TZID=") {
		t.Errorf("UTC timezone should not emit TZID parameter\ngot:\n%s", result)
	}
	if !strings.Contains(result, "DTSTART:20260620T183000Z\r\n") {
		t.Errorf("Expected UTC DTSTART, got:\n%s", result)
	}
}

// TestRoundTrip_TZID verifies that a parsed TZID event survives a
// generate → parse round-trip with the same UTC instant and timezone label.
func TestRoundTrip_TZID(t *testing.T) {
	icalData := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Home OS//Calendar//EN
BEGIN:VTIMEZONE
TZID:America/New_York
BEGIN:STANDARD
DTSTART:19701101T020000
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
END:STANDARD
BEGIN:DAYLIGHT
DTSTART:19700308T020000
TZOFFSETFROM:-0500
TZOFFSETTO:-0400
END:DAYLIGHT
END:VTIMEZONE
BEGIN:VEVENT
UID:rt-tzid@homeos
DTSTART;TZID=America/New_York:20260620T143000
DTEND;TZID=America/New_York:20260620T153000
SUMMARY:Round-trip with TZID
END:VEVENT
END:VCALENDAR`

	original, err := ParseEvent(icalData)
	if err != nil {
		t.Fatalf("initial ParseEvent: %v", err)
	}

	regen := GenerateEvent(original)
	parsed, err := ParseEvent(regen)
	if err != nil {
		t.Fatalf("round-trip ParseEvent: %v", err)
	}

	if !parsed.Start.Equal(original.Start) {
		t.Errorf("Start: got %v, want %v", parsed.Start, original.Start)
	}
	if !parsed.End.Equal(original.End) {
		t.Errorf("End: got %v, want %v", parsed.End, original.End)
	}
	if parsed.Timezone != original.Timezone {
		t.Errorf("Timezone: got %q, want %q", parsed.Timezone, original.Timezone)
	}
	if parsed.Title != original.Title {
		t.Errorf("Title: got %q, want %q", parsed.Title, original.Title)
	}
}

// TestParseOffsetSeconds covers the RFC 5545 §3.3.14 offset parser used by
// the VTIMEZONE fallback path.
func TestParseOffsetSeconds(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"-0500", -5 * 3600},
		{"+0900", 9 * 3600},
		{"+0930", 9*3600 + 30*60},
		{"-05", -5 * 3600},
		{"+0000", 0},
		{"", 0},
		{"abc", 0},
	}
	for _, c := range cases {
		got := parseOffsetSeconds(c.in)
		if got != c.want {
			t.Errorf("parseOffsetSeconds(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestCalEvent_JSONSchema_NewEvent locks in the unified JSON schema for a
// brand-new event (Sequence=0). CalEvent must emit exactly the 8 snake_case
// keys that apps/api/internal/calendar eventJSON emits, in the same order, so
// the two services produce byte-identical JSON for the same event when stored
// in ical_data. Metadata fields (ID, CalendarID, UID, Timezone, EntityType,
// EntityID, DTStamp, CreatedAt, UpdatedAt) must not appear even when
// non-zero, since the API schema does not carry them. Sequence is the sole
// exception: it uses omitempty so a zero value is omitted, making new-event
// JSON byte-identical to the API's output.
func TestCalEvent_JSONSchema_NewEvent(t *testing.T) {
	start := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 7, 11, 30, 0, 0, time.UTC)
	ev := &CalEvent{
		// Metadata — must be excluded from JSON even when populated.
		ID:         "obj-id-1234",
		CalendarID: "cal-id-5678",
		UID:        "event-uid@homeos",
		Timezone:   "America/New_York",
		EntityType: "property",
		EntityID:   "prop-42",
		DTStamp:    start.Add(-time.Hour),
		CreatedAt:  start.Add(-2 * time.Hour),
		UpdatedAt:  start.Add(-30 * time.Minute),
		// Sequence intentionally left at zero (new event) so it is omitted.
		// Stored schema — must appear with snake_case keys.
		Title:       "Soccer Game",
		Description: "Home game",
		Start:       start,
		End:         end,
		AllDay:      false,
		Location:    "Riverside Park",
		Color:       "#3B82F6",
		EventType:   "event",
	}

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(b)

	// Expected JSON: exactly the 8 schema keys, snake_case, in declaration
	// order. time.Time marshals to RFC3339Nano which for second-precision
	// times matches RFC3339 byte-for-byte. No metadata keys appear.
	want := `{"title":"Soccer Game","description":"Home game","start":"2026-06-07T10:00:00Z","end":"2026-06-07T11:30:00Z","all_day":false,"location":"Riverside Park","color":"#3B82F6","event_type":"event"}`
	if got != want {
		t.Errorf("CalEvent JSON mismatch\ngot:  %s\nwant: %s", got, want)
	}

	// Round-trip: unmarshal back and verify schema fields survive and
	// metadata fields (other than Sequence which was zero) are untouched
	// since they were never in the JSON.
	var parsed CalEvent
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if parsed.Title != ev.Title || parsed.Color != ev.Color || parsed.EventType != ev.EventType {
		t.Errorf("round-trip lost schema field: %+v", parsed)
	}
	if parsed.ID != "" || parsed.UID != "" || parsed.Timezone != "" || !parsed.CreatedAt.IsZero() {
		t.Errorf("round-trip populated metadata from JSON — those keys must not be in the payload: %+v", parsed)
	}
}

// TestCalEvent_JSONSchema_SequencePersisted verifies that a non-zero
// Sequence (an event that has been updated via CalDAV PUT) is persisted in
// the stored JSON with the snake_case `sequence` key. The API's eventJSON
// does not carry this field, but Go's json.Unmarshal ignores unknown keys,
// so the API can still parse CalDAV-written JSON without error. This is the
// sole deliberate exception to the byte-identical-schemas rule, documented
// on the CalEvent struct comment, and exists to support RFC 5545 §3.8.7.4
// revision tracking across CalDAV PUT updates.
func TestCalEvent_JSONSchema_SequencePersisted(t *testing.T) {
	start := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 7, 11, 30, 0, 0, time.UTC)
	ev := &CalEvent{
		UID:       "seq-event@homeos",
		Title:     "Updated Event",
		Start:     start,
		End:       end,
		Color:     "#10B981",
		EventType: "event",
		Sequence:  3,
	}

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(b)

	// The sequence key MUST be present with the non-zero value. All 8
	// schema keys are still present in the same order.
	want := `{"sequence":3,"title":"Updated Event","description":"","start":"2026-06-07T10:00:00Z","end":"2026-06-07T11:30:00Z","all_day":false,"location":"","color":"#10B981","event_type":"event"}`
	if got != want {
		t.Errorf("CalEvent JSON with sequence mismatch\ngot:  %s\nwant: %s", got, want)
	}

	// Round-trip preserves Sequence.
	var parsed CalEvent
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if parsed.Sequence != 3 {
		t.Errorf("Sequence lost in round trip: got %d, want 3", parsed.Sequence)
	}
	if parsed.Color != "#10B981" {
		t.Errorf("Color lost in round trip: got %q, want %q", parsed.Color, "#10B981")
	}
}

// TestCalEvent_JSON_ColorRoundTrip confirms Color survives a CalDAV PUT →
// DB → GET round trip via JSON. Before the schema was unified, CalEvent had
// no Color field so CalDAV writes silently dropped it.
func TestCalEvent_JSON_ColorRoundTrip(t *testing.T) {
	start := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	ev := &CalEvent{
		UID:       "color-rt@homeos",
		Title:     "Color Round Trip",
		Start:     start,
		End:       start.Add(time.Hour),
		Color:     "#EF4444",
		EventType: "event",
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// Simulate storage: strip everything but JSON, then reload.
	var loaded CalEvent
	if err := json.Unmarshal(b, &loaded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if loaded.Color != "#EF4444" {
		t.Errorf("Color lost in round trip: got %q, want %q", loaded.Color, "#EF4444")
	}
	if loaded.EventType != "event" {
		t.Errorf("EventType lost in round trip: got %q, want %q", loaded.EventType, "event")
	}
}
