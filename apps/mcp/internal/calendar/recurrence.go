package calendar

// Recurrence, reminders and people on event writes (#4465).
//
// A calendar_objects row carries three views of an event: the JSON subset in
// ical_data (title, start, end, ... of the master), the full VCALENDAR in
// ical_raw (RRULE, EXDATE, RECURRENCE-ID overrides, VALARMs, ATTENDEEs) and
// the master's RRULE value in rrule. CalDAV (apps/calendar renderObjectICal)
// and the API (#4458, currentObject) use ical_raw only while its master still
// matches the JSON on title, description, location, all_day, start and end.
// Every MCP write therefore builds the object with packages/icalx, writes the
// JSON from the same master and stores all three together, so a phone sees
// exactly what the tool wrote.
//
// Edits of a repeating event take a scope:
//   - all: the master changes (times move by the same wall-clock offset, so
//     EXDATEs and overrides are moved along and keep matching);
//   - this: one occurrence becomes a RECURRENCE-ID override (or an existing
//     override is changed); delete adds an EXDATE;
//   - following: the series is split. The original keeps the occurrences
//     before the chosen one (UNTIL, or a reduced COUNT) and a new row with a
//     new UID carries the chosen occurrence onward with the changes; delete
//     only truncates.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/icalx"
	"home-os/mcp/internal/respond"
)

// Limits on what a caller may ask for (the same bounds as the API).
const (
	maxReminders       = 10
	maxReminderMinutes = 4 * 7 * 24 * 60 // four weeks
	maxExceptDates     = 1000
	maxRepeatCount     = 1000
	maxRepeatInterval  = 999
	maxPeople          = 50
	// conflictWeeks and conflictInstances bound the overlap check for a
	// repeating event: its occurrences in the next 8 weeks, at most 20.
	conflictWeeks     = 8
	conflictInstances = 20
)

// Scopes for editing or deleting a repeating event.
const (
	scopeThis      = "this"
	scopeFollowing = "following"
	scopeAll       = "all"
)

var scopes = []string{scopeThis, scopeFollowing, scopeAll}

// Repeat is the simple recurrence the tools take and return. It maps to an
// RFC 5545 RRULE (FREQ, INTERVAL, BYDAY, UNTIL, COUNT).
type Repeat struct {
	Freq     string   `json:"freq" jsonschema_description:"daily, weekly, monthly or yearly."`
	Interval int      `json:"interval,omitempty" jsonschema_description:"Every N periods (default 1)."`
	ByDay    []string `json:"by_day,omitempty" jsonschema_description:"Weekdays MO TU WE TH FR SA SU; monthly may use 1MO or -1FR."`
	Until    string   `json:"until,omitempty" jsonschema_description:"Last day it can occur (YYYY-MM-DD, family timezone)."`
	Count    int      `json:"count,omitempty" jsonschema_description:"Number of occurrences."`
}

// EventPerson is a household person attending an event.
type EventPerson struct {
	ID   string `json:"id" jsonschema_description:"people id; pass it in person_ids."`
	Name string `json:"name"`
}

// repeatSchema is the input schema of the repeat object.
func repeatSchema(withNone bool) map[string]any {
	freqs := []string{"daily", "weekly", "monthly", "yearly"}
	desc := "daily, weekly, monthly or yearly"
	if withNone {
		freqs = append(freqs, "none")
		desc += "; none stops repeating"
	}
	return map[string]any{
		"freq":     map[string]any{"type": "string", "enum": freqs, "description": desc},
		"interval": map[string]any{"type": "integer", "minimum": 1, "maximum": maxRepeatInterval, "description": "Every N days/weeks/months/years (default 1)"},
		"by_day": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
			"description": "Weekdays, e.g. [\"TU\",\"TH\"]; monthly may use 2TU or -1FR"},
		"until": map[string]any{"type": "string", "description": "Last day it can happen, YYYY-MM-DD (inclusive)"},
		"count": map[string]any{"type": "integer", "minimum": 1, "maximum": maxRepeatCount, "description": "Number of times (instead of until)"},
	}
}

// ─────────────────────────────────────────────
// Argument parsing
// ─────────────────────────────────────────────

// repeatArg reads the repeat argument. present reports whether the key was
// passed at all; clear is true for null, "" or freq none (stop repeating).
// A JSON string holding the object is accepted, and so is a raw RRULE value
// ("FREQ=WEEKLY;BYDAY=TU,TH") as an escape hatch.
func repeatArg(args map[string]any) (rep *Repeat, rawRule string, present, clear bool, bad *mcp.CallToolResult) {
	v, ok := args["repeat"]
	if !ok {
		return nil, "", false, false, nil
	}
	example := map[string]any{"repeat": map[string]any{"freq": "weekly", "by_day": []string{"TU", "TH"}, "until": "2026-12-15"}}
	invalid := func(msg string) *mcp.CallToolResult {
		return respond.Invalid("repeat", msg, "Pass repeat as an object: freq plus optional interval, by_day, until or count.", example)
	}
	switch x := v.(type) {
	case nil:
		return nil, "", true, true, nil
	case string:
		s := strings.TrimSpace(x)
		switch {
		case s == "" || strings.EqualFold(s, "none"):
			return nil, "", true, true, nil
		case strings.HasPrefix(s, "{"):
			var m map[string]any
			if err := json.Unmarshal([]byte(s), &m); err != nil {
				return nil, "", true, false, invalid("repeat is not a valid object.")
			}
			v = m
		case strings.Contains(strings.ToUpper(s), "FREQ="):
			s = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(s, "RRULE:"), "rrule:"))
			if strings.ContainsAny(s, "\r\n:") || len(s) > 1024 {
				return nil, "", true, false, invalid("repeat must be one RRULE value such as FREQ=WEEKLY;BYDAY=TU,TH.")
			}
			return nil, s, true, false, nil
		default:
			return nil, "", true, false, invalid(fmt.Sprintf("repeat %q is not understood.", s))
		}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, "", true, false, invalid("repeat must be an object.")
	}
	r := &Repeat{}
	r.Freq = strings.ToLower(strings.TrimSpace(fmt.Sprint(valueOr(m["freq"], ""))))
	if r.Freq == "" || r.Freq == "none" || r.Freq == "never" {
		if len(m) == 0 || r.Freq != "" {
			return nil, "", true, true, nil
		}
		return nil, "", true, false, invalid("repeat.freq is required (daily, weekly, monthly or yearly).")
	}
	var err error
	if r.Interval, err = intField(m, "interval"); err != nil {
		return nil, "", true, false, invalid(err.Error())
	}
	if r.Count, err = intField(m, "count"); err != nil {
		return nil, "", true, false, invalid(err.Error())
	}
	r.Until = strings.TrimSpace(fmt.Sprint(valueOr(m["until"], "")))
	switch d := m["by_day"].(type) {
	case nil:
	case string:
		for _, f := range strings.FieldsFunc(d, func(r rune) bool { return r == ',' || r == ' ' }) {
			r.ByDay = append(r.ByDay, f)
		}
	case []any:
		for _, item := range d {
			if s, ok := item.(string); ok {
				r.ByDay = append(r.ByDay, s)
			}
		}
	default:
		return nil, "", true, false, invalid("repeat.by_day must be a list such as [\"TU\",\"TH\"].")
	}
	return r, "", true, false, nil
}

func valueOr(v, def any) any {
	if v == nil {
		return def
	}
	return v
}

func intField(m map[string]any, key string) (int, error) {
	switch x := m[key].(type) {
	case nil:
		return 0, nil
	case float64:
		if x != float64(int(x)) {
			return 0, fmt.Errorf("repeat.%s must be a whole number", key)
		}
		return int(x), nil
	case string:
		if strings.TrimSpace(x) == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, fmt.Errorf("repeat.%s must be a whole number", key)
		}
		return n, nil
	}
	return 0, fmt.Errorf("repeat.%s must be a whole number", key)
}

// numberList reads an array of whole numbers (or a comma-separated string).
func numberList(args map[string]any, key string) ([]int, bool, error) {
	v, ok := args[key]
	if !ok {
		return nil, false, nil
	}
	var items []any
	switch x := v.(type) {
	case nil:
		return []int{}, true, nil
	case []any:
		items = x
	case float64:
		items = []any{x}
	case string:
		for _, f := range strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == ' ' }) {
			items = append(items, f)
		}
	default:
		return nil, true, fmt.Errorf("%s must be a list of numbers", key)
	}
	out := make([]int, 0, len(items))
	for _, it := range items {
		switch n := it.(type) {
		case float64:
			if n != float64(int(n)) {
				return nil, true, fmt.Errorf("%s must be whole numbers", key)
			}
			out = append(out, int(n))
		case string:
			i, err := strconv.Atoi(strings.TrimSpace(n))
			if err != nil {
				return nil, true, fmt.Errorf("%s must be whole numbers", key)
			}
			out = append(out, i)
		default:
			return nil, true, fmt.Errorf("%s must be whole numbers", key)
		}
	}
	return out, true, nil
}

// stringList reads an array of strings (or a comma-separated string).
// present reports whether the key was passed.
func stringList(args map[string]any, key string) ([]string, bool) {
	v, ok := args[key]
	if !ok {
		return nil, false
	}
	var out []string
	switch x := v.(type) {
	case []any:
		for _, it := range x {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		for _, f := range strings.Split(x, ",") {
			if f = strings.TrimSpace(f); f != "" {
				out = append(out, f)
			}
		}
	}
	if out == nil {
		out = []string{}
	}
	return out, true
}

// reminderArg reads reminder_minutes; present is false when not passed.
func reminderArg(args map[string]any) ([]int, bool, *mcp.CallToolResult) {
	mins, present, err := numberList(args, "reminder_minutes")
	example := map[string]any{"reminder_minutes": []int{30}}
	if err != nil {
		return nil, true, respond.Invalid("reminder_minutes", err.Error()+".", "Pass minutes before the start, e.g. [30] or [1440, 60].", example)
	}
	if len(mins) > maxReminders {
		return nil, true, respond.Invalid("reminder_minutes", fmt.Sprintf("At most %d reminders.", maxReminders), "Pass fewer reminders.", example)
	}
	for _, m := range mins {
		if m < 0 || m > maxReminderMinutes {
			return nil, true, respond.Invalid("reminder_minutes",
				fmt.Sprintf("reminder_minutes must be between 0 and %d (four weeks).", maxReminderMinutes),
				"Pass minutes before the start; 0 alerts at the start.", example)
		}
	}
	return mins, present, nil
}

// ─────────────────────────────────────────────
// RRULE
// ─────────────────────────────────────────────

var weekdayCodes = map[string]string{
	"mo": "MO", "mon": "MO", "monday": "MO",
	"tu": "TU", "tue": "TU", "tues": "TU", "tuesday": "TU",
	"we": "WE", "wed": "WE", "weds": "WE", "wednesday": "WE",
	"th": "TH", "thu": "TH", "thur": "TH", "thurs": "TH", "thursday": "TH",
	"fr": "FR", "fri": "FR", "friday": "FR",
	"sa": "SA", "sat": "SA", "saturday": "SA",
	"su": "SU", "sun": "SU", "sunday": "SU",
}

var codeWeekday = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

var weekdayCode = map[time.Weekday]string{
	time.Sunday: "SU", time.Monday: "MO", time.Tuesday: "TU", time.Wednesday: "WE",
	time.Thursday: "TH", time.Friday: "FR", time.Saturday: "SA",
}

// normalizeByDay turns "Tue", "tuesday", "2tu" or "-1FR" into RRULE BYDAY
// tokens. ordinals reports whether any token has an ordinal.
func normalizeByDay(in []string) ([]string, bool, error) {
	var out []string
	ordinals := false
	seen := map[string]bool{}
	for _, raw := range in {
		s := strings.ToLower(strings.TrimSpace(raw))
		if s == "" {
			continue
		}
		i := 0
		for i < len(s) && (s[i] == '+' || s[i] == '-' || (s[i] >= '0' && s[i] <= '9')) {
			i++
		}
		prefix, name := s[:i], s[i:]
		code, ok := weekdayCodes[name]
		if !ok {
			return nil, false, fmt.Errorf("by_day %q is not a weekday (use MO TU WE TH FR SA SU)", raw)
		}
		if prefix != "" {
			n, err := strconv.Atoi(strings.TrimPrefix(prefix, "+"))
			if err != nil || n == 0 || n < -53 || n > 53 {
				return nil, false, fmt.Errorf("by_day %q has a bad ordinal (use 1MO, 2TU or -1FR)", raw)
			}
			prefix = strconv.Itoa(n)
			ordinals = true
		}
		tok := prefix + code
		if !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return out, ordinals, nil
}

// repeatRule builds the RRULE value for rep on master m. hh is the family
// timezone; it reads until as a family-local day.
func repeatRule(rep *Repeat, m *icalx.Event, hh *time.Location) (string, error) {
	freq := map[string]string{
		"daily": "DAILY", "day": "DAILY", "weekly": "WEEKLY", "week": "WEEKLY",
		"monthly": "MONTHLY", "month": "MONTHLY", "yearly": "YEARLY", "year": "YEARLY", "annually": "YEARLY",
	}[strings.ToLower(rep.Freq)]
	if freq == "" {
		return "", fmt.Errorf("repeat.freq %q must be daily, weekly, monthly or yearly", rep.Freq)
	}
	parts := []string{"FREQ=" + freq}
	if rep.Interval < 0 || rep.Interval > maxRepeatInterval {
		return "", fmt.Errorf("repeat.interval must be between 1 and %d", maxRepeatInterval)
	}
	if rep.Interval > 1 {
		parts = append(parts, "INTERVAL="+strconv.Itoa(rep.Interval))
	}
	if len(rep.ByDay) > 0 {
		days, ordinals, err := normalizeByDay(rep.ByDay)
		if err != nil {
			return "", err
		}
		if ordinals && freq != "MONTHLY" && freq != "YEARLY" {
			return "", fmt.Errorf("by_day ordinals like 2TU need freq monthly or yearly")
		}
		if len(days) > 0 {
			parts = append(parts, "BYDAY="+strings.Join(days, ","))
		}
	}
	if rep.Count != 0 && rep.Until != "" {
		return "", fmt.Errorf("pass repeat.until or repeat.count, not both")
	}
	if rep.Count < 0 || rep.Count > maxRepeatCount {
		return "", fmt.Errorf("repeat.count must be between 1 and %d", maxRepeatCount)
	}
	if rep.Count > 0 {
		parts = append(parts, "COUNT="+strconv.Itoa(rep.Count))
	}
	if rep.Until != "" {
		day, _, err := civilDate(rep.Until)
		if err != nil {
			return "", fmt.Errorf("repeat.until %q is not a date (YYYY-MM-DD)", rep.Until)
		}
		first := seriesCivilStart(m, hh)
		if day.Before(first) {
			return "", fmt.Errorf("repeat.until %s is before the first occurrence on %s", day.Format(dateLayout), first.Format(dateLayout))
		}
		parts = append(parts, "UNTIL="+untilValue(m, day, hh))
	}
	return strings.Join(parts, ";"), nil
}

// untilValue writes an UNTIL for the end of the civil day `day`, in the form
// RFC 5545 requires for the master's DTSTART: a DATE for all-day series, a
// local time for floating ones, otherwise UTC.
func untilValue(m *icalx.Event, day time.Time, hh *time.Location) string {
	switch {
	case m.AllDay:
		return day.Format("20060102")
	case m.Floating:
		return day.Format("20060102") + "T235959"
	}
	loc := hh
	if m.TZID != "" {
		loc = m.Start.Location()
	}
	end := time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, loc)
	return end.UTC().Format("20060102T150405Z")
}

// untilBefore writes an UNTIL just before the instance rid (series form), for
// ending a series before a split.
func untilBefore(m *icalx.Event, rid time.Time) string {
	switch {
	case m.AllDay:
		return rid.AddDate(0, 0, -1).Format("20060102")
	case m.Floating:
		return rid.Add(-time.Second).Format("20060102T150405")
	}
	return rid.Add(-time.Second).UTC().Format("20060102T150405Z")
}

// rulePart returns the value of key in an RRULE ("" when absent).
func rulePart(rule, key string) string {
	for _, p := range strings.Split(rule, ";") {
		k, v, ok := strings.Cut(p, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// withRulePart sets (or with value "" removes) key in an RRULE.
func withRulePart(rule, key, value string) string {
	var out []string
	done := false
	for _, p := range strings.Split(rule, ";") {
		if strings.TrimSpace(p) == "" {
			continue
		}
		k, _, _ := strings.Cut(p, "=")
		if strings.EqualFold(strings.TrimSpace(k), key) {
			if value != "" && !done {
				out = append(out, key+"="+value)
				done = true
			}
			continue
		}
		out = append(out, p)
	}
	if value != "" && !done {
		out = append(out, key+"="+value)
	}
	return strings.Join(out, ";")
}

// repeatFromRule describes an RRULE as a Repeat when it only uses the parts
// Repeat models; otherwise nil (callers still return the raw rrule).
func repeatFromRule(rule string, m *icalx.Event, hh *time.Location) *Repeat {
	if rule == "" {
		return nil
	}
	r := &Repeat{}
	for _, p := range strings.Split(rule, ";") {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil
		}
		v = strings.TrimSpace(v)
		switch strings.ToUpper(strings.TrimSpace(k)) {
		case "FREQ":
			r.Freq = strings.ToLower(v)
		case "INTERVAL":
			r.Interval, _ = strconv.Atoi(v)
			if r.Interval == 1 {
				r.Interval = 0
			}
		case "BYDAY":
			r.ByDay = strings.Split(strings.ToUpper(v), ",")
		case "COUNT":
			r.Count, _ = strconv.Atoi(v)
		case "UNTIL":
			r.Until = untilDay(v, m, hh)
		case "WKST":
		default:
			return nil
		}
	}
	switch r.Freq {
	case "daily", "weekly", "monthly", "yearly":
		return r
	}
	return nil
}

// untilDay turns an UNTIL value into the family-local last day.
func untilDay(v string, m *icalx.Event, hh *time.Location) string {
	if t, err := time.Parse("20060102T150405Z", v); err == nil {
		loc := hh
		if m != nil && m.TZID != "" && !m.AllDay {
			loc = m.Start.Location()
		}
		return t.In(loc).Format(dateLayout)
	}
	for _, layout := range []string{"20060102T150405", "20060102"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Format(dateLayout)
		}
	}
	return v
}

// describeRepeat is a short phrase for summaries, e.g. "every week on Tue,
// Thu until 2026-12-15".
func describeRepeat(rep *Repeat, rule string) string {
	if rep == nil {
		if rule == "" {
			return ""
		}
		return "repeating (" + rule + ")"
	}
	unit := map[string]string{"daily": "day", "weekly": "week", "monthly": "month", "yearly": "year"}[rep.Freq]
	s := "every " + unit
	if rep.Interval > 1 {
		s = fmt.Sprintf("every %d %ss", rep.Interval, unit)
	}
	if len(rep.ByDay) > 0 {
		names := make([]string, 0, len(rep.ByDay))
		for _, d := range rep.ByDay {
			code := strings.TrimLeft(d, "+-0123456789")
			if wd, ok := codeWeekday[code]; ok {
				name := wd.String()[:3]
				if ord := strings.TrimSuffix(d, code); ord != "" {
					name = ord + " " + name
				}
				names = append(names, name)
			} else {
				names = append(names, d)
			}
		}
		s += " on " + strings.Join(names, ", ")
	}
	switch {
	case rep.Until != "":
		s += " until " + rep.Until
	case rep.Count > 0:
		s += fmt.Sprintf(", %d times", rep.Count)
	}
	return s
}

// ─────────────────────────────────────────────
// Series times
// ─────────────────────────────────────────────

// seriesLoc is the zone an instance time of m is read in: the TZID's zone,
// UTC for UTC series, and the family zone for floating and all-day series
// (whose values are wall clocks / civil dates).
func seriesLoc(m *icalx.Event, hh *time.Location) *time.Location {
	switch {
	case m.AllDay || m.Floating:
		return hh
	case m.TZID != "":
		return m.Start.Location()
	}
	return time.UTC
}

// realTime places an instance time of m (series form) as an instant in hh.
func realTime(m *icalx.Event, s time.Time, hh *time.Location) time.Time {
	if m.AllDay || m.Floating {
		return time.Date(s.Year(), s.Month(), s.Day(), s.Hour(), s.Minute(), s.Second(), 0, hh)
	}
	return s.In(hh)
}

// seriesTime converts an instant to m's series form.
func seriesTime(m *icalx.Event, t time.Time, hh *time.Location) time.Time {
	switch {
	case m.AllDay:
		l := t.In(hh)
		return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
	case m.Floating:
		l := t.In(hh)
		return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC)
	case m.TZID != "":
		return t.In(m.Start.Location())
	}
	return t.UTC()
}

// seriesCivilStart is the master's first day as a UTC-midnight civil date.
func seriesCivilStart(m *icalx.Event, hh *time.Location) time.Time {
	if m.AllDay {
		return m.Start
	}
	l := realTime(m, m.Start, hh).In(seriesLoc(m, hh))
	if m.Floating {
		l = m.Start
	}
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

// wall is t's wall clock in its own location, as a UTC value, so wall-clock
// differences ignore DST.
func wall(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

// shiftWall moves a series-form time by a wall-clock offset, keeping its
// location (a 17:30 occurrence stays 17:30-plus-offset across DST).
func shiftWall(t time.Time, d time.Duration) time.Time {
	if d == 0 {
		return t
	}
	w := wall(t).Add(d)
	return time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, t.Location())
}

// sameInstance compares two instance times of series m by the key icalx
// uses: civil date for all-day, wall clock for floating, instant otherwise.
func sameInstance(m *icalx.Event, a, b time.Time) bool {
	switch {
	case m.AllDay:
		return a.Format("20060102") == b.Format("20060102")
	case m.Floating:
		return a.Format("20060102T150405") == b.Format("20060102T150405")
	}
	return a.Equal(b)
}

func instanceBefore(m *icalx.Event, a, b time.Time) bool {
	if m.AllDay || m.Floating {
		return wall(a).Before(wall(b))
	}
	return a.Before(b)
}

func allDayDays(e *icalx.Event) int {
	d := int(e.End.Sub(e.Start).Hours()+12) / 24
	if d < 1 {
		d = 1
	}
	return d
}

// setEnd sets an instance's end from its start and a length taken from the
// master (days for all-day, duration otherwise).
func instanceEnd(m *icalx.Event, start time.Time) time.Time {
	if m.AllDay {
		return start.AddDate(0, 0, allDayDays(m))
	}
	d := m.Duration()
	if d <= 0 {
		d = time.Hour
	}
	return start.Add(d)
}

// ─────────────────────────────────────────────
// Reading the stored object
// ─────────────────────────────────────────────

// storedSeries is how an event row reads for writes.
type storedSeries struct {
	obj *icalx.Object
	// fromRaw: obj came from a current ical_raw (phone properties,
	// overrides and VTIMEZONEs are preserved on write).
	fromRaw bool
	// locked: the raw holds something the tools can't edit safely (only
	// overrides, no master); writes are refused.
	locked bool
}

// recurring reports whether the object is a repeating series (or carries
// overrides of one).
func (s storedSeries) recurring() bool {
	return s.obj != nil && s.obj.Master != nil && (s.obj.Master.IsRecurring() || len(s.obj.Overrides) > 0)
}

// loadSeries returns the object an event row describes, applying the
// staleness rule CalDAV and the API use: ical_raw counts only while its
// master matches the JSON on title, description, location, all_day, start
// and end. A stale or missing raw means the JSON alone describes the event
// (plus the rrule column, when a writer set it without raw).
func loadSeries(ej eventJSON, uid string, rrule, raw *string) storedSeries {
	if raw != nil && strings.TrimSpace(*raw) != "" {
		obj, err := icalx.Parse([]byte(*raw))
		switch {
		case err == nil && obj.Master == nil:
			return storedSeries{locked: true}
		case err == nil && rawCurrent(obj.Master, ej):
			return storedSeries{obj: obj, fromRaw: true}
		case err != nil && isRecurringEvent(rrule, raw):
			// Unparseable raw that looks recurring: CalDAV serves the JSON,
			// but rewriting it here would drop whatever the phone stored.
			return storedSeries{locked: true}
		}
	}
	m, err := masterFromJSON(ej, uid)
	if err != nil {
		return storedSeries{}
	}
	if rrule != nil && strings.TrimSpace(*rrule) != "" && (raw == nil || strings.TrimSpace(*raw) == "") {
		m.RRule = strings.TrimSpace(*rrule)
		obj := &icalx.Object{UID: uid, Master: m}
		if obj.Validate() != nil {
			m.RRule = ""
		}
	}
	return storedSeries{obj: &icalx.Object{UID: uid, Master: m}}
}

// rawCurrent is CalDAV's rawMatchesEvent / the API's currentObject rule.
func rawCurrent(m *icalx.Event, ej eventJSON) bool {
	start, err1 := time.Parse(time.RFC3339, ej.Start)
	end, err2 := time.Parse(time.RFC3339, ej.End)
	if err1 != nil || err2 != nil {
		return false
	}
	mEnd := m.End
	if mEnd.IsZero() || !mEnd.After(m.Start) {
		mEnd = m.Start.Add(time.Hour) // CalDAV ParseEvent's default
	}
	return m.Summary == ej.Title && m.Description == ej.Description && m.Location == ej.Location &&
		m.AllDay == ej.AllDay && m.Start.Equal(start) && mEnd.Equal(end)
}

// masterFromJSON builds a single-event master from the stored JSON.
func masterFromJSON(ej eventJSON, uid string) (*icalx.Event, error) {
	m := &icalx.Event{UID: uid, Summary: ej.Title, Description: ej.Description, Location: ej.Location,
		Status: icalx.StatusConfirmed, AllDay: ej.AllDay}
	if ej.AllDay {
		first, endExcl, err := allDayRange(ej.Start, ej.End)
		if err != nil {
			return nil, err
		}
		m.Start, m.End = first, endExcl
		return m, nil
	}
	start, err := time.Parse(time.RFC3339, ej.Start)
	if err != nil {
		return nil, err
	}
	end, err := time.Parse(time.RFC3339, ej.End)
	if err != nil || !end.After(start) {
		end = start.Add(time.Hour)
	}
	m.Start, m.End = start.UTC(), end.UTC()
	return m, nil
}

// jsonTimes is the master's start/end as stored in ical_data: UTC RFC 3339
// for timed events (wall clock for floating ones, as CalDAV stores them) and
// UTC-midnight dates for all-day events.
func jsonTimes(m *icalx.Event) (string, string) {
	end := m.End
	if !end.After(m.Start) {
		end = m.Start.Add(time.Hour)
	}
	return m.Start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)
}

// peopleFromAttendees lists the household people among an event's attendees.
func peopleFromAttendees(e *icalx.Event) []EventPerson {
	var out []EventPerson
	for _, a := range e.Attendees() {
		if a.PersonID != "" {
			out = append(out, EventPerson{ID: a.PersonID, Name: a.CommonName})
		}
	}
	return out
}

// attachSeries copies the recurrence, reminders and people of a stored
// object onto the event a tool returns.
func attachSeries(ev *Event, s storedSeries, ej map[string]json.RawMessage, hh *time.Location) {
	if s.obj == nil || s.obj.Master == nil {
		if raw, ok := ej["people"]; ok {
			_ = json.Unmarshal(raw, &ev.People)
		}
		return
	}
	m := s.obj.Master
	ev.Recurring = s.recurring() || ev.Recurring
	ev.RRule = m.RRule
	ev.Repeat = repeatFromRule(m.RRule, m, hh)
	for _, x := range m.ExDates {
		ev.ExceptDates = append(ev.ExceptDates, realTime(m, x, hh).Format(dateLayout))
	}
	for _, r := range remindersOf(m.Alarms) {
		ev.ReminderMinutes = append(ev.ReminderMinutes, r)
	}
	if s.fromRaw {
		ev.People = peopleFromAttendees(m)
	} else if raw, ok := ej["people"]; ok {
		_ = json.Unmarshal(raw, &ev.People)
	}
}

// remindersOf reports the start-relative alarms at or before the start, in
// minutes. Other alarms (absolute, RELATED=END) stay in ical_raw unmodelled.
func remindersOf(as []icalx.Alarm) []int {
	var out []int
	for _, a := range as {
		if !a.TriggerAt.IsZero() || strings.EqualFold(a.Related, icalx.RelatedEnd) || a.Trigger > 0 {
			continue
		}
		out = append(out, int(-a.Trigger/time.Minute))
	}
	return out
}

func alarmsFor(mins []int) []icalx.Alarm {
	if len(mins) == 0 {
		return nil
	}
	out := make([]icalx.Alarm, 0, len(mins))
	for _, m := range mins {
		out = append(out, icalx.Alarm{Action: icalx.ActionDisplay, Description: "Reminder",
			Trigger: -time.Duration(m) * time.Minute, Related: icalx.RelatedStart})
	}
	return out
}

// ─────────────────────────────────────────────
// People
// ─────────────────────────────────────────────

// resolvePeople matches person_ids (people ids, user ids or names) to the
// household's people. Failures are corrective results listing the people.
func resolvePeople(ctx context.Context, q querier, householdID string, refs []string) ([]EventPerson, *mcp.CallToolResult) {
	out := []EventPerson{}
	if len(refs) == 0 {
		return out, nil
	}
	if len(refs) > maxPeople {
		return nil, respond.Invalid("person_ids", fmt.Sprintf("At most %d people.", maxPeople), "Pass fewer person_ids.", nil)
	}
	type person struct{ id, userID, name string }
	rows, err := q.Query(ctx,
		`SELECT id::text, COALESCE(user_id::text, ''), display_name FROM people WHERE household_id = $1 ORDER BY lower(display_name)`,
		householdID)
	if err != nil {
		return nil, respond.Failed("look up people", err)
	}
	var all []person
	for rows.Next() {
		var p person
		if err := rows.Scan(&p.id, &p.userID, &p.name); err != nil {
			rows.Close()
			return nil, respond.Failed("look up people", err)
		}
		all = append(all, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, respond.Failed("look up people", err)
	}
	names := func() string {
		var ns []string
		for _, p := range all {
			ns = append(ns, p.name)
		}
		if len(ns) == 0 {
			return "none yet"
		}
		return strings.Join(ns, ", ")
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		var hits []person
		for _, p := range all {
			if strings.EqualFold(ref, p.id) || (p.userID != "" && strings.EqualFold(ref, p.userID)) {
				hits = []person{p}
				break
			}
		}
		if len(hits) == 0 && !isUUID(ref) {
			for _, p := range all {
				if strings.EqualFold(ref, p.name) {
					hits = append(hits, p)
				}
			}
			if len(hits) == 0 { // first name only, e.g. "Nora" for "Nora Smith"
				for _, p := range all {
					if f := strings.Fields(p.name); len(f) > 0 && strings.EqualFold(ref, f[0]) {
						hits = append(hits, p)
					}
				}
			}
		}
		switch len(hits) {
		case 0:
			return nil, respond.Invalid("person_ids", fmt.Sprintf("No person %q in the family (people: %s).", ref, names()),
				"Pass person ids from person_find, or exact names.", nil)
		case 1:
		default:
			var c []string
			for _, h := range hits {
				c = append(c, h.name+" ("+h.id+")")
			}
			return nil, respond.Invalid("person_ids", fmt.Sprintf("%q matches more than one person: %s.", ref, strings.Join(c, "; ")),
				"Pass the person's id instead of the name.", nil)
		}
		if !seen[hits[0].id] {
			seen[hits[0].id] = true
			out = append(out, EventPerson{ID: hits[0].id, Name: hits[0].name})
		}
	}
	return out, nil
}

// setPeople replaces the household-person attendees of e, keeping attendees
// a client added (no person id) and the parameters (PARTSTAT, ...) of people
// who stay. Each person is an ATTENDEE with CN, X-HOMEOS-PERSON-ID and a
// urn:uuid address: never a mailto, so a phone never mistakes the event for
// an invitation addressed to its own user.
func setPeople(e *icalx.Event, people []EventPerson) {
	existing := map[string]icalx.Attendee{}
	var keep []icalx.Attendee
	for _, a := range e.Attendees() {
		if a.PersonID == "" {
			keep = append(keep, a)
		} else {
			existing[a.PersonID] = a
		}
	}
	for _, p := range people {
		a, ok := existing[p.ID]
		if !ok {
			a = icalx.Attendee{Address: "urn:uuid:" + p.ID}
		}
		a.PersonID, a.CommonName = p.ID, p.Name
		keep = append(keep, a)
	}
	e.SetAttendees(keep)
}

// ─────────────────────────────────────────────
// Occurrences
// ─────────────────────────────────────────────

// instance is one occurrence of a series, found by its original date.
type instance struct {
	RID      time.Time    // original start, series form (the RECURRENCE-ID)
	Override *icalx.Event // existing override for it, if any
}

// masterOnly is obj's master as a series of its own (no EXDATEs, no
// overrides): the generated instances.
func masterOnly(obj *icalx.Object) *icalx.Object {
	m := obj.Master.Clone()
	m.ExDates = nil
	return &icalx.Object{UID: obj.UID, Master: m}
}

// findInstance finds the occurrence of obj whose original start is arg: a
// family-local date (YYYY-MM-DD) or an exact start time with offset.
func findInstance(obj *icalx.Object, field, arg string, hh *time.Location) (instance, *mcp.CallToolResult) {
	m := obj.Master
	arg = strings.TrimSpace(arg)
	example := map[string]any{field: "2026-11-19"}
	var exact *time.Time
	var day time.Time
	if t, err := time.Parse(time.RFC3339, arg); err == nil {
		exact = &t
		l := t.In(hh)
		day = time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, hh)
	} else if d, _, err := civilDate(arg); err == nil {
		day = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, hh)
	} else {
		return instance{}, respond.Invalid(field, fmt.Sprintf("%s %q is not a date.", field, arg),
			"Pass the original date of the occurrence, YYYY-MM-DD in the family timezone.", example)
	}
	occs, err := masterOnly(obj).Expand(day.Add(-time.Second), day.AddDate(0, 0, 1), hh)
	if err != nil {
		return instance{}, respond.Failed("list the occurrences", err)
	}
	var hits []time.Time
	for _, o := range occs {
		real := realTime(m, o.RecurrenceID, hh)
		if o.RecurrenceID.IsZero() {
			continue
		}
		if exact != nil && real.Equal(*exact) {
			hits = []time.Time{o.RecurrenceID}
			break
		}
		if l := real.In(hh); l.Year() == day.Year() && l.YearDay() == day.YearDay() {
			hits = append(hits, o.RecurrenceID)
		}
	}
	switch len(hits) {
	case 0:
		next := nextOccurrences(obj, day, hh, 3)
		fix := "Pass the original date of one occurrence; event_find lists them."
		if len(next) > 0 {
			fix = "Occurrences on or after that day: " + strings.Join(next, ", ") + "."
			example = map[string]any{field: next[0]}
		}
		return instance{}, respond.Invalid(field, fmt.Sprintf("%q has no occurrence on %s.", m.Summary, day.Format("Mon 2006-01-02")), fix, example)
	case 1:
	default:
		var ts []string
		for _, h := range hits {
			ts = append(ts, realTime(m, h, hh).Format(time.RFC3339))
		}
		return instance{}, respond.Invalid(field, fmt.Sprintf("%q happens more than once on %s.", m.Summary, day.Format(dateLayout)),
			"Pass the exact start of the occurrence: one of "+strings.Join(ts, ", ")+".", map[string]any{field: ts[0]})
	}
	rid := hits[0]
	for _, x := range m.ExDates {
		if sameInstance(m, x, rid) {
			return instance{}, respond.Invalid(field, fmt.Sprintf("The occurrence on %s was already removed from the series.", day.Format(dateLayout)),
				"Pick another occurrence, or change except_dates with scope all.", nil)
		}
	}
	inst := instance{RID: rid}
	for _, ov := range obj.Overrides {
		if sameInstance(m, ov.RecurrenceID, rid) {
			inst.Override = ov
		}
	}
	return inst, nil
}

// nextOccurrences lists up to n occurrence dates of obj from day on.
func nextOccurrences(obj *icalx.Object, day time.Time, hh *time.Location, n int) []string {
	occs, err := obj.Expand(day, day.AddDate(1, 0, 0), hh)
	if err != nil {
		return nil
	}
	var out []string
	for _, o := range occs {
		if len(out) == n {
			break
		}
		if o.RecurrenceID.IsZero() {
			continue
		}
		out = append(out, realTime(obj.Master, o.RecurrenceID, hh).In(hh).Format(dateLayout))
	}
	return out
}

// isFirstInstance reports whether rid is the series' first instance.
func isFirstInstance(obj *icalx.Object, rid time.Time) bool {
	return !instanceBefore(obj.Master, obj.Master.Start, rid)
}

// exceptInstances resolves except_dates (dates or exact starts) to instance
// times of obj's rule.
func exceptInstances(obj *icalx.Object, dates []string, hh *time.Location) ([]time.Time, *mcp.CallToolResult) {
	if len(dates) > maxExceptDates {
		return nil, respond.Invalid("except_dates", fmt.Sprintf("At most %d except_dates.", maxExceptDates), "Pass fewer dates.", nil)
	}
	base := masterOnly(obj)
	var out []time.Time
	for _, d := range dates {
		inst, bad := findInstance(base, "except_dates", d, hh)
		if bad != nil {
			return nil, bad
		}
		out = append(out, inst.RID)
	}
	return out, nil
}

// dropOverrides removes overrides of the given instances.
func dropOverrides(obj *icalx.Object, rids []time.Time) {
	m := obj.Master
	kept := obj.Overrides[:0]
	for _, ov := range obj.Overrides {
		drop := false
		for _, r := range rids {
			if sameInstance(m, ov.RecurrenceID, r) {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, ov)
		}
	}
	obj.Overrides = kept
}

// countBefore counts the generated instances (EXDATEs included, as COUNT
// does) of obj before rid.
func countBefore(obj *icalx.Object, rid time.Time, hh *time.Location) (int, error) {
	mo := masterOnly(obj)
	from := realTime(mo.Master, mo.Master.Start, hh).Add(-time.Second)
	to := realTime(mo.Master, rid, hh)
	occs, err := mo.Expand(from, to, hh)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range occs {
		if instanceBefore(mo.Master, o.RecurrenceID, rid) {
			n++
		}
	}
	return n, nil
}

// endSeriesBefore ends obj's series just before rid: UNTIL (or a COUNT of
// the instances before it), dropping EXDATEs and overrides at or after it.
// It returns the original COUNT value's remainder for a following series
// (0 when the rule has no COUNT).
func endSeriesBefore(obj *icalx.Object, rid time.Time, hh *time.Location) (remaining int, err error) {
	m := obj.Master
	if c := rulePart(m.RRule, "COUNT"); c != "" {
		total, _ := strconv.Atoi(c)
		n, err := countBefore(obj, rid, hh)
		if err != nil {
			return 0, err
		}
		m.RRule = withRulePart(m.RRule, "COUNT", strconv.Itoa(max(n, 1)))
		remaining = max(total-n, 1)
	} else {
		m.RRule = withRulePart(m.RRule, "UNTIL", untilBefore(m, rid))
	}
	var ex []time.Time
	for _, x := range m.ExDates {
		if instanceBefore(m, x, rid) {
			ex = append(ex, x)
		}
	}
	m.ExDates = ex
	var ovs []*icalx.Event
	for _, ov := range obj.Overrides {
		if instanceBefore(m, ov.RecurrenceID, rid) {
			ovs = append(ovs, ov)
		}
	}
	obj.Overrides = ovs
	var rd []time.Time
	for _, r := range m.RDates {
		if instanceBefore(m, r, rid) {
			rd = append(rd, r)
		}
	}
	m.RDates = rd
	return remaining, nil
}

// encodeObject validates obj and returns ical_raw and rrule.
func encodeObject(obj *icalx.Object) (string, *string, *mcp.CallToolResult) {
	if err := obj.Validate(); err != nil {
		rule := ""
		if obj.Master != nil {
			rule = obj.Master.RRule
		}
		if errors.Is(err, icalx.ErrUnsupportedRule) {
			return "", nil, respond.Invalid("repeat",
				fmt.Sprintf("The repeat rule %q is not supported or never happens.", rule),
				"Use freq daily, weekly, monthly or yearly with dates that exist, e.g. by_day [\"TU\",\"TH\"].",
				map[string]any{"repeat": map[string]any{"freq": "weekly", "by_day": []string{"TU", "TH"}}})
		}
		return "", nil, respond.Invalid("repeat", fmt.Sprintf("The repeat rule %q is invalid: %v.", rule, err),
			"Use freq daily, weekly, monthly or yearly.", nil)
	}
	raw, err := obj.Encode()
	if err != nil {
		return "", nil, respond.Failed("encode the event", err)
	}
	var rr *string
	if obj.Master != nil && obj.Master.RRule != "" {
		r := obj.Master.RRule
		rr = &r
	}
	return string(raw), rr, nil
}

// alignWeekly moves a weekly master with plain BYDAY weekdays forward to the
// first listed weekday when its start is not one of them, since phones and
// rrule libraries disagree on whether such a DTSTART counts. It returns the
// number of days moved.
func alignWeekly(m *icalx.Event, hh *time.Location) int {
	if !strings.EqualFold(rulePart(m.RRule, "FREQ"), "WEEKLY") {
		return 0
	}
	byday := rulePart(m.RRule, "BYDAY")
	if byday == "" {
		return 0
	}
	want := map[time.Weekday]bool{}
	for _, d := range strings.Split(strings.ToUpper(byday), ",") {
		wd, ok := codeWeekday[d]
		if !ok {
			return 0 // ordinals: leave as written
		}
		want[wd] = true
	}
	local := m.Start
	if !m.AllDay && !m.Floating {
		local = m.Start.In(seriesLoc(m, hh))
	}
	for n := 0; n < 7; n++ {
		if want[local.AddDate(0, 0, n).Weekday()] {
			if n > 0 {
				d := m.End.Sub(m.Start)
				m.Start = m.Start.AddDate(0, 0, n)
				if m.AllDay {
					m.End = m.Start.AddDate(0, 0, int(d.Hours()+12)/24)
				} else {
					m.End = m.Start.Add(d)
				}
			}
			return n
		}
	}
	return 0
}

// shiftByDay moves plain and ordinal BYDAY weekdays by n days, so a series
// moved from Tue/Thu by one day repeats on Wed/Fri.
func shiftByDay(rule string, n int) string {
	if n%7 == 0 {
		return rule
	}
	byday := rulePart(rule, "BYDAY")
	if byday == "" {
		return rule
	}
	var out []string
	for _, tok := range strings.Split(strings.ToUpper(byday), ",") {
		code := strings.TrimLeft(tok, "+-0123456789")
		wd, ok := codeWeekday[code]
		if !ok {
			return rule
		}
		nw := time.Weekday(((int(wd)+n)%7 + 7) % 7)
		out = append(out, strings.TrimSuffix(tok, code)+weekdayCode[nw])
	}
	return withRulePart(rule, "BYDAY", strings.Join(out, ","))
}

// toZone moves a UTC-based timed master into the family zone, so a weekly
// rule repeats at the same local time across DST and BYDAY means local days.
// EXDATEs and RECURRENCE-IDs are instants and keep matching.
func toZone(m *icalx.Event, hh *time.Location) {
	if m.AllDay || m.Floating || m.TZID != "" || hh == time.UTC {
		return
	}
	m.TZID = hh.String()
	m.Start, m.End = m.Start.In(hh), m.End.In(hh)
}

// seriesSpan is the first/last instant covered by obj's next occurrences,
// for the conflict check, and the occurrences themselves.
func upcomingOccurrences(obj *icalx.Object, hh *time.Location) []icalx.Occurrence {
	from := realTime(obj.Master, obj.Master.Start, hh)
	if today := todayIn(hh); from.Before(today) {
		from = today
	}
	occs, err := obj.Expand(from, from.AddDate(0, 0, 7*conflictWeeks), hh)
	if err != nil {
		return nil
	}
	if len(occs) > conflictInstances {
		occs = occs[:conflictInstances]
	}
	return occs
}

// seriesConflicts returns other events overlapping any of the series' next
// occurrences (8 weeks, at most 20), sorted by start. Other repeating events
// are expanded (findConflicts reads occurrences, #4483), and each is
// reported once, at its first occurrence that overlaps.
func seriesConflicts(ctx context.Context, q querier, householdID string, hh *time.Location, obj *icalx.Object, excludeIDs ...string) ([]ConflictEvent, string) {
	occs := upcomingOccurrences(obj, hh)
	if len(occs) == 0 {
		return []ConflictEvent{}, ""
	}
	lo, hi := occs[0].Start, occs[0].End
	for _, o := range occs {
		if o.Start.Before(lo) {
			lo = o.Start
		}
		if o.End.After(hi) {
			hi = o.End
		}
	}
	candidates, err := findConflicts(ctx, q, householdID, hh, nil, lo, hi, "")
	if err != nil {
		return []ConflictEvent{}, "The overlap check failed; check with event_find from/to."
	}
	out := make([]ConflictEvent, 0)
	seen := map[string]bool{}
	for _, c := range candidates {
		if seen[c.ID] || slices.Contains(excludeIDs, c.ID) {
			continue
		}
		cs, ce, err := parseEventTime(Event{Start: c.Start, End: c.End})
		if err != nil {
			continue
		}
		for _, o := range occs {
			s, e := o.Start, o.End
			if !e.After(s) {
				e = s.Add(time.Minute)
			}
			if cs.Before(e) && ce.After(s) {
				out = append(out, c)
				seen[c.ID] = true
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return startsBefore(out[i].Start, out[j].Start) })
	return out, ""
}

// newUID returns a fresh id usable as both row id and iCalendar UID.
func newUID() string { return uuid.New().String() }
