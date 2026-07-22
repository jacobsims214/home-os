// Package ical implements iCalendar (RFC 5545) parsing and generation.
//
// This package provides the foundational iCalendar types and operations
// used by the Home OS calendar service. It supports VCALENDAR/VEVENT
// components with standard fields: UID, DTSTART, DTEND, SUMMARY,
// DESCRIPTION, LOCATION, and RRULE. All-day events are handled via
// the VALUE=DATE parameter.
package ical

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CalEvent represents a single calendar event in the Home OS domain model.
//
// # JSON schema unification
//
// The JSON tags on the stored-schema fields below are aligned with
// apps/api/internal/calendar eventJSON so the two services produce
// byte-identical JSON for the same event when stored in the shared
// `ical_data` column. The stored-schema fields are ordered to match
// eventJSON's field declaration order, which makes `json.Marshal` output
// byte-identical between the two services (Go marshals struct fields in
// declaration order).
//
// Fields that are NOT part of the stored JSON schema are tagged `json:"-"`
// so their zero values never pollute the stored payload. These come from
// calendar_objects columns (ID, CalendarID, UID, CreatedAt, UpdatedAt),
// joined entity tables (EntityType, EntityID), or are iCalendar
// regeneration metadata (Timezone, DTStamp) used only while building
// iCalendar text in memory.
//
// Sequence is the exception: it IS persisted in the stored JSON with the
// snake_case `sequence` key so it survives write/read round-trips and the
// CalDAV PUT handler can increment it on each update (RFC 5545 §3.8.7.4).
// The API's eventJSON does not carry `sequence`, so API-written events
// simply omit the key (omitempty skips the zero value); Go's json.Unmarshal
// ignores unknown keys, so reading an API-written event yields Sequence=0,
// which is correct for a brand-new event.
//
// DTStamp is intentionally NOT persisted: it is sourced from the
// calendar_objects.updated_at column at GET/REPORT time so the iCalendar
// DTSTAMP/LAST-MODIFIED properties reflect the actual last-modified
// timestamp in the DB. Persisting it would go stale whenever a row is
// updated by the API (which doesn't know about DTStamp).
//
// Start and End are always stored in UTC. Timezone holds the IANA timezone
// name (e.g. "America/New_York") when the source event carried a TZID
// parameter; empty or "UTC" means the event is in UTC. GenerateEvent uses
// Timezone to emit DTSTART;TZID=... so that Apple Calendar sees wall-clock
// times in the original zone instead of a UTC instant.
//
// Sequence and DTStamp carry in-memory state for iCalendar generation.
// SEQUENCE is incremented on each PUT update (RFC 5545 §3.8.7.4) so CalDAV
// clients like Apple Calendar can detect revisions. DTStamp carries the
// timestamp used for the iCalendar DTSTAMP and LAST-MODIFIED properties on
// GET/REPORT.
type CalEvent struct {
	// Metadata fields — never serialized to JSON. Populated from the
	// calendar_objects row or derived at runtime after unmarshalling.
	ID         string    `json:"-"`
	CalendarID string    `json:"-"`
	UID        string    `json:"-"`
	Timezone   string    `json:"-"`
	EntityType string    `json:"-"`
	EntityID   string    `json:"-"`
	DTStamp    time.Time `json:"-"`
	CreatedAt  time.Time `json:"-"`
	UpdatedAt  time.Time `json:"-"`

	// Sequence is persisted in the stored JSON so it survives round-trips
	// and can be incremented on each PUT update. See the struct doc comment
	// for the rationale on why this is the only metadata field that is
	// persisted.
	Sequence int `json:"sequence,omitempty"`

	// Stored JSON schema — declaration order matches apps/api eventJSON
	// so both services emit byte-identical JSON for the same event.
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	AllDay      bool      `json:"all_day"`
	Location    string    `json:"location"`
	Color       string    `json:"color"`
	EventType   string    `json:"event_type"`
}

const (
	prodID       = "-//Home OS//Calendar//EN"
	dateFormat   = "20060102"         // YYYYMMDD for all-day events
	dateTimeFmt  = "20060102T150405Z" // YYYYMMDDTHHMMSSZ for UTC datetime
	localTimeFmt = "20060102T150405"  // YYYYMMDDTHHMMSS for TZID/naive-local datetimes
)

// ParseEvent parses an iCalendar VCALENDAR/VEVENT string into a CalEvent.
// It extracts UID, DTSTART, DTEND, SUMMARY, DESCRIPTION, LOCATION, and RRULE.
// DTSTART with VALUE=DATE is parsed as an all-day event.
//
// VTIMEZONE blocks in the VCALENDAR are parsed first so that DTSTART;TZID=...
// values can be resolved against the timezone definitions. For IANA TZIDs
// (e.g. "America/New_York") the system zoneinfo is used via time.LoadLocation;
// for non-IANA TZIDs a fixed offset is built from the VTIMEZONE STANDARD
// subcomponent. When a TZID is present but no VTIMEZONE block defines it,
// time.LoadLocation is still attempted as a fallback.
func ParseEvent(icalData string) (*CalEvent, error) {
	lines := unfoldLines(icalData)
	ev := &CalEvent{}

	// First pass: collect VTIMEZONE definitions so DTSTART;TZID=... can resolve.
	timezones := parseVTimezones(lines)

	var inCalendar, inEvent bool

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")

		switch {
		case strings.HasPrefix(line, "BEGIN:VCALENDAR"):
			inCalendar = true
		case strings.HasPrefix(line, "END:VCALENDAR"):
			inCalendar = false
		case strings.HasPrefix(line, "BEGIN:VEVENT"):
			inEvent = true
		case strings.HasPrefix(line, "END:VEVENT"):
			inEvent = false
		}

		if !inEvent && !inCalendar {
			continue
		}
		if !inEvent {
			continue
		}

		prop, params, value := parsePropertyLine(line)
		if prop == "" {
			continue
		}

		switch prop {
		case "UID":
			ev.UID = value
		case "SUMMARY":
			ev.Title = unescapeText(value)
		case "DESCRIPTION":
			ev.Description = unescapeText(value)
		case "LOCATION":
			ev.Location = unescapeText(value)
		case "DTSTART":
			t, allDay, tzid, err := parseDateValue(params, value, timezones)
			if err != nil {
				return nil, fmt.Errorf("ical: parse DTSTART: %w", err)
			}
			ev.Start = t
			ev.AllDay = allDay
			if tzid != "" {
				ev.Timezone = tzid
			}
		case "DTEND":
			t, _, _, err := parseDateValue(params, value, timezones)
			if err != nil {
				return nil, fmt.Errorf("ical: parse DTEND: %w", err)
			}
			ev.End = t
		case "RRULE":
			// Store RRULE as a semicolon-delimited string in Description as a suffix
			// for now. Full RRULE expansion is not yet implemented.
		}
	}

	if ev.Start.IsZero() {
		return nil, fmt.Errorf("ical: missing DTSTART")
	}

	// Default UID to a placeholder if missing
	if ev.UID == "" {
		ev.UID = fmt.Sprintf("%d@homeos", ev.Start.UnixMilli())
	}

	// If no DTEND, default to 1 hour after start
	if ev.End.IsZero() {
		ev.End = ev.Start.Add(time.Hour)
	}

	return ev, nil
}

// GenerateCalendar generates an iCalendar VCALENDAR string wrapping one or
// more VEVENT components. Returns valid RFC 5545 text.
func GenerateCalendar(events []*CalEvent) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:" + prodID + "\r\n")
	b.WriteString("CALSCALE:GREGORIAN\r\n")

	for _, ev := range events {
		b.WriteString(generateEventBody(ev))
	}

	b.WriteString("END:VCALENDAR\r\n")
	return b.String()
}

// GenerateEvent generates a complete iCalendar VCALENDAR with a single
// VEVENT component. This is a convenience wrapper around GenerateCalendar.
func GenerateEvent(ev *CalEvent) string {
	return GenerateCalendar([]*CalEvent{ev})
}

// generateEventBody generates the VEVENT portion of an iCalendar object
// without the VCALENDAR wrapper.
func generateEventBody(ev *CalEvent) string {
	var b strings.Builder

	b.WriteString("BEGIN:VEVENT\r\n")
	uid := ev.UID
	if uid == "" && ev.ID != "" {
		uid = ev.ID
	}
	if uid == "" {
		uid = fmt.Sprintf("%d@homeos", time.Now().UnixMilli())
	}
	b.WriteString("UID:" + uid + "\r\n")

	// DTSTAMP is required by RFC 5545 and CalDAV clients. It must be the time
	// the event was last modified. Source it from ev.DTStamp (populated from
	// the DB updated_at column by callers) so re-generating the same stored
	// event produces an identical DTSTAMP on every GET. Fall back to ev.UpdatedAt
	// then time.Now() only when no authoritative timestamp is available (e.g.
	// freshly-parsed client bodies that have never been persisted).
	dtstamp := ev.DTStamp
	if dtstamp.IsZero() {
		dtstamp = ev.UpdatedAt
	}
	if dtstamp.IsZero() {
		dtstamp = time.Now().UTC()
	}
	b.WriteString(fmt.Sprintf("DTSTAMP:%s\r\n", dtstamp.UTC().Format(dateTimeFmt)))

	// SEQUENCE is required by Apple Calendar — it bumps on every revision so
	// clients can tell which version is newer. Defaults to 0 for new events
	// and is incremented by the PUT handler on updates (stored in JSON).
	b.WriteString(fmt.Sprintf("SEQUENCE:%d\r\n", ev.Sequence))

	// LAST-MODIFIED is used by Apple Calendar for cache validation. Same
	// sourcing rationale as DTSTAMP — use the DB updated_at via ev.DTStamp so
	// the value is stable across re-generations of the same stored event.
	lastMod := ev.DTStamp
	if lastMod.IsZero() {
		lastMod = ev.UpdatedAt
	}
	if lastMod.IsZero() {
		lastMod = time.Now().UTC()
	}
	b.WriteString(fmt.Sprintf("LAST-MODIFIED:%s\r\n", lastMod.UTC().Format(dateTimeFmt)))

	// STATUS is required by Apple Calendar — without it, events may be ignored.
	b.WriteString("STATUS:CONFIRMED\r\n")

	if ev.AllDay {
		b.WriteString(fmt.Sprintf("DTSTART;VALUE=DATE:%s\r\n", ev.Start.Format(dateFormat)))
		b.WriteString(fmt.Sprintf("DTEND;VALUE=DATE:%s\r\n", ev.End.Format(dateFormat)))
	} else if ev.Timezone != "" && ev.Timezone != "UTC" {
		// Emit wall-clock times in the original timezone when the event
		// carries a TZID (Apple Calendar sends/receives local-time events).
		// If the timezone cannot be loaded (e.g. corrupted stored value),
		// fall back to UTC so the response is still valid iCalendar.
		if loc, err := time.LoadLocation(ev.Timezone); err == nil {
			b.WriteString(fmt.Sprintf("DTSTART;TZID=%s:%s\r\n", ev.Timezone, ev.Start.In(loc).Format(localTimeFmt)))
			b.WriteString(fmt.Sprintf("DTEND;TZID=%s:%s\r\n", ev.Timezone, ev.End.In(loc).Format(localTimeFmt)))
		} else {
			b.WriteString(fmt.Sprintf("DTSTART:%s\r\n", ev.Start.UTC().Format(dateTimeFmt)))
			b.WriteString(fmt.Sprintf("DTEND:%s\r\n", ev.End.UTC().Format(dateTimeFmt)))
		}
	} else {
		b.WriteString(fmt.Sprintf("DTSTART:%s\r\n", ev.Start.UTC().Format(dateTimeFmt)))
		b.WriteString(fmt.Sprintf("DTEND:%s\r\n", ev.End.UTC().Format(dateTimeFmt)))
	}

	if ev.Title != "" {
		b.WriteString("SUMMARY:" + escapeText(ev.Title) + "\r\n")
	}
	if ev.Description != "" {
		b.WriteString("DESCRIPTION:" + escapeText(ev.Description) + "\r\n")
	}
	if ev.Location != "" {
		b.WriteString("LOCATION:" + escapeText(ev.Location) + "\r\n")
	}

	b.WriteString("END:VEVENT\r\n")
	return b.String()
}

// unfoldLines joins continuation lines (RFC 5545 §3.1).
// Lines starting with space or tab are continuations of the previous line.
func unfoldLines(data string) []string {
	raw := strings.Split(data, "\n")
	var unfolded []string
	for _, line := range raw {
		line = strings.TrimRight(line, "\r")
		if len(line) == 0 {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if len(unfolded) > 0 {
				unfolded[len(unfolded)-1] += line[1:]
			}
		} else {
			unfolded = append(unfolded, line)
		}
	}
	return unfolded
}

// parsePropertyLine splits a line into property name, parameters, and value.
// Format: PROPNAME[;PARAM1=VALUE1;PARAM2=VALUE2]:VALUE
func parsePropertyLine(line string) (prop string, params map[string]string, value string) {
	// Find the first colon that separates the property name/params from the value.
	// We need to be careful because colons can appear inside parameters.
	colonIdx := findUnquotedColon(line, ';')
	if colonIdx < 0 {
		return "", nil, ""
	}

	head := line[:colonIdx]
	value = line[colonIdx+1:]

	// Split head on semicolons
	parts := strings.SplitN(head, ";", 2)
	prop = parts[0]

	params = make(map[string]string)
	if len(parts) > 1 {
		for _, p := range strings.Split(parts[1], ";") {
			if kv := strings.SplitN(p, "=", 2); len(kv) == 2 {
				params[kv[0]] = kv[1]
			} else {
				params[p] = ""
			}
		}
	}

	return prop, params, value
}

// findUnquotedColon finds the first unquoted colon after the given delimiter.
// In iCalendar, params are separated by ; and the value comes after the first unquoted :
func findUnquotedColon(line string, paramDelim byte) int {
	for i := 0; i < len(line); i++ {
		if line[i] == ':' {
			// After a semicolon-delimited param section, the first unescaped colon
			// is the value separator. We just need to skip quoted strings.
			// iCalendar doesn't use quotes per se, but we need to find the
			// colon that separates params from value.
			// Parameters are before the colon, and they use semicolons.
			// So the first colon after the property name is the separator.
			return i
		}
	}
	return -1
}

// parseDateValue parses an iCalendar date or datetime value.
// If params["VALUE"] == "DATE", it's an all-day event (YYYYMMDD).
// If params["TZID"] is set, the value is a naive local datetime interpreted in
// that timezone — the location is looked up in timezones (populated from
// VTIMEZONE blocks) and falls back to time.LoadLocation for IANA TZIDs that
// were not declared via VTIMEZONE. The returned tzid is non-empty when a TZID
// parameter was used so callers can persist it on the CalEvent for round-trip.
// Otherwise the value is a UTC datetime (YYYYMMDDTHHMMSSZ) or a local time
// with explicit offset (YYYYMMDDTHHMMSS[+-]HHMM).
func parseDateValue(params map[string]string, value string, timezones map[string]*time.Location) (time.Time, bool, string, error) {
	if params["VALUE"] == "DATE" {
		t, err := time.Parse(dateFormat, value)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("parse all-day date %q: %w", value, err)
		}
		return t, true, "", nil
	}

	// TZID parameter: interpret the naive local time in the named zone.
	if tzid, ok := params["TZID"]; ok && tzid != "" {
		loc, ok := timezones[tzid]
		if !ok {
			// Fall back to system zoneinfo — handles IANA TZIDs that arrive
			// without a VTIMEZONE block (e.g. events from non-Apple clients).
			var err error
			loc, err = time.LoadLocation(tzid)
			if err != nil {
				return time.Time{}, false, "", fmt.Errorf("ical: unknown TZID %q: %w", tzid, err)
			}
		}
		t, err := time.ParseInLocation(localTimeFmt, value, loc)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("ical: parse datetime with TZID %q: %w", tzid, err)
		}
		// Store as UTC; the tzid is returned so the caller can preserve it.
		return t.UTC(), false, tzid, nil
	}

	// Try UTC datetime format: YYYYMMDDTHHMMSSZ
	if strings.HasSuffix(value, "Z") {
		t, err := time.Parse(dateTimeFmt, value)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("parse UTC datetime %q: %w", value, err)
		}
		return t, false, "", nil
	}

	// Try local time with timezone offset: YYYYMMDDTHHMMSS[+-]HHMM
	// Format: YYYYMMDDTHHMMSS
	for _, fmt := range []string{"20060102T150405-0700", "20060102T150405-07", "20060102T150405"} {
		t, err := time.Parse(fmt, value)
		if err == nil {
			return t.UTC(), false, "", nil
		}
	}

	return time.Time{}, false, "", fmt.Errorf("cannot parse datetime %q", value)
}

// parseVTimezones scans unfolded iCalendar lines and returns a map of TZID to
// *time.Location for every VTIMEZONE block. Resolution strategy per TZID:
//
//  1. If time.LoadLocation(tzid) succeeds (IANA zone), use the system
//     location — this is the common case for Apple Calendar, which sends
//     TZID=America/New_York etc. System locations correctly handle DST.
//  2. Otherwise, build a fixed-offset location from the TZOFFSETTO of the
//     STANDARD subcomponent. This is a simplification: full RRULE-based
//     DST transitions inside VTIMEZONE are not implemented. Non-IANA TZIDs
//     are rare in practice for personal calendar sync.
//
// The map is consumed by parseDateValue when resolving DTSTART;TZID=... values.
func parseVTimezones(lines []string) map[string]*time.Location {
	out := make(map[string]*time.Location)

	var inTZ bool
	var inStandard bool
	var currentTZID string
	var currentOffsetSecs int
	var haveOffset bool

	flush := func() {
		if currentTZID == "" {
			return
		}
		if loc, err := time.LoadLocation(currentTZID); err == nil {
			out[currentTZID] = loc
		} else if haveOffset {
			out[currentTZID] = time.FixedZone(currentTZID, currentOffsetSecs)
		}
	}

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")

		switch {
		case strings.HasPrefix(line, "BEGIN:VTIMEZONE"):
			inTZ = true
			inStandard = false
			currentTZID = ""
			currentOffsetSecs = 0
			haveOffset = false
		case strings.HasPrefix(line, "END:VTIMEZONE"):
			flush()
			inTZ = false
			inStandard = false
			currentTZID = ""
			haveOffset = false
		case strings.HasPrefix(line, "BEGIN:STANDARD"):
			inStandard = true
		case strings.HasPrefix(line, "END:STANDARD"):
			inStandard = false
		}

		if !inTZ {
			continue
		}

		prop, _, value := parsePropertyLine(line)
		if prop == "" {
			continue
		}
		switch prop {
		case "TZID":
			currentTZID = value
		case "TZOFFSETTO":
			// Prefer the STANDARD subcomponent's offset (non-DST). If no
			// STANDARD block is present, fall back to the last TZOFFSETTO
			// seen at the VTIMEZONE top level.
			if inStandard || !haveOffset {
				currentOffsetSecs = parseOffsetSeconds(value)
				haveOffset = true
			}
		}
	}
	return out
}

// parseOffsetSeconds parses an iCalendar UTC offset value (RFC 5545 §3.3.14)
// such as "-0500", "+0930", or "-05" into a signed number of seconds.
func parseOffsetSeconds(s string) int {
	if len(s) < 3 {
		return 0
	}
	sign := 1
	switch s[0] {
	case '-':
		sign = -1
	case '+':
		// default sign = +1
	default:
		return 0
	}
	hh, err := strconv.Atoi(s[1:3])
	if err != nil {
		return 0
	}
	mm := 0
	if len(s) >= 5 {
		if mm, err = strconv.Atoi(s[3:5]); err != nil {
			mm = 0
		}
	}
	return sign * (hh*3600 + mm*60)
}

// iCalTextEscaper handles escaping rules from RFC 5545 §3.3.11.
// Text values escape: \, ; \n as \\, \; \\n
// Newlines are literal newlines (not \n) in the iCalendar text.

// escapeText escapes a text value for inclusion in an iCalendar property value.
func escapeText(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", "\\;")
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, "\r\n", "\\n")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

// unescapeText unescapes an iCalendar text property value per RFC 5545 §3.3.11.
//
// It is implemented as a single-pass character-by-character parser rather than
// sequential strings.ReplaceAll calls. Sequential replacement corrupts text
// containing a literal backslash followed by n/N/;/, because the \n / \; / \,
// replacements run before the \\ → \ replacement, so an escaped-backslash
// sequence (e.g. "\\n" representing a literal backslash-n) is misread as a
// newline. A single pass recognizes \\, \n, \N, \;, \, without overlap.
func unescapeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n', 'N':
				b.WriteByte('\n')
				i++
			case ';':
				b.WriteByte(';')
				i++
			case ',':
				b.WriteByte(',')
				i++
			case '\\':
				b.WriteByte('\\')
				i++
			default:
				// Lone backslash followed by an unrecognized character —
				// preserve the backslash as-is per a tolerant reading of
				// RFC 5545. (Strict parsers would drop the backslash, but
				// preserving it is safer for round-trip fidelity.)
				b.WriteByte(s[i])
			}
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// ParseRRULE parses an RRULE string into a map of property names to values.
// Example input: "FREQ=WEEKLY;BYDAY=MO,WE,FR;INTERVAL=2"
func ParseRRULE(rrule string) map[string]string {
	result := make(map[string]string)
	for _, part := range strings.Split(rrule, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			result[kv[0]] = kv[1]
		} else {
			result[part] = ""
		}
	}
	return result
}

// FormatRRULE creates an RRULE string from a map of properties.
func FormatRRULE(rules map[string]string) string {
	if len(rules) == 0 {
		return ""
	}
	var parts []string
	for k, v := range rules {
		if v != "" {
			parts = append(parts, k+"="+v)
		} else {
			parts = append(parts, k)
		}
	}
	return strings.Join(parts, ";")
}

// MustParseDate is a test helper that parses an RFC 3339 date string
// or panics. Use in tests only.
func MustParseDate(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(fmt.Sprintf("MustParseDate: %v", err))
	}
	return t
}

// FormatDate is a shortcut to iCalendar datetime formatting.
// It always produces UTC format.
func FormatDate(t time.Time) string {
	return t.UTC().Format(dateTimeFmt)
}

// FormatDateAllDay formats a time as an all-day DATE value.
func FormatDateAllDay(t time.Time) string {
	return t.Format(dateFormat)
}
