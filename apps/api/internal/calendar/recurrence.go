package calendar

// Recurrence and reminders for API events (#4458).
//
// A calendar_objects row carries three representations of an event:
//   - ical_data: the JSON subset (title, start, end, ...) every service reads;
//   - ical_raw:  the full VCALENDAR (RRULE, EXDATE, overrides, VALARMs);
//   - rrule:     the master's RRULE value, kept queryable on its own.
//
// CalDAV (apps/calendar renderObjectICal) serves ical_raw only while it still
// matches the JSON on title, description, location, all_day, start and end;
// otherwise it generates iCalendar from the JSON alone. The API follows the
// same rule when it reads (currentObject), and every API write regenerates
// ical_raw and rrule from the same values it writes to the JSON, so the three
// never disagree. All iCalendar parsing, building, validation and expansion
// goes through packages/icalx.

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"home-os/icalx"
)

// Limits on what a client may ask for.
const (
	maxReminders        = 10
	maxReminderMinutes  = 4 * 7 * 24 * 60 // four weeks
	maxExDates          = 1000
	maxRRuleLen         = 1024
	maxExpandRange      = 3 * 366 * 24 * time.Hour
	allDayPrefilterSlop = 48 * time.Hour
)

// Reminder is an alarm relative to the event's start.
type Reminder struct {
	MinutesBefore int `json:"minutes_before"`
}

// errInvalidEvent marks a client error (HTTP 400); the message is safe to
// return to the caller.
type errInvalidEvent struct{ msg string }

func (e *errInvalidEvent) Error() string { return e.msg }

func invalidf(format string, args ...any) error {
	return &errInvalidEvent{msg: fmt.Sprintf(format, args...)}
}

// IsInvalidEvent reports whether err is a client error, and its message.
func IsInvalidEvent(err error) (string, bool) {
	var ie *errInvalidEvent
	if errors.As(err, &ie) {
		return ie.msg, true
	}
	return "", false
}

// normalizeRRule trims the value and an optional "RRULE:" prefix and refuses
// anything that is not a single property value.
func normalizeRRule(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) > 7 && strings.EqualFold(s[:6], "RRULE:") {
		s = strings.TrimSpace(s[6:])
	}
	if s == "" {
		return "", nil
	}
	if len(s) > maxRRuleLen {
		return "", invalidf("rrule is too long")
	}
	if strings.ContainsAny(s, "\r\n:") {
		return "", invalidf("rrule must be a single RRULE value such as FREQ=WEEKLY;BYDAY=TU,TH")
	}
	return s, nil
}

// civilUTC returns t's calendar date (in t's own location) as midnight UTC,
// the form all-day events take in the JSON and in icalx.
func civilUTC(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// normalizeTimes applies the stored conventions: all-day events start at
// midnight UTC of the date written and end (exclusive) at a later midnight;
// timed events default to one hour.
func normalizeTimes(ev *Event) error {
	if ev.Start.IsZero() {
		return invalidf("start required")
	}
	if ev.AllDay {
		ev.Start = civilUTC(ev.Start)
		if !ev.End.IsZero() {
			ev.End = civilUTC(ev.End)
		}
		if !ev.End.After(ev.Start) {
			ev.End = ev.Start.AddDate(0, 0, 1)
		}
		return nil
	}
	if ev.End.Before(ev.Start) && !ev.End.IsZero() {
		return invalidf("end must not be before start")
	}
	if !ev.End.After(ev.Start) {
		// Missing or zero-length: one hour. iCalendar writes a zero-length
		// event without DTEND, which CalDAV reads back as one hour, so
		// storing one hour keeps ical_raw current for the JSON.
		ev.End = ev.Start.Add(time.Hour)
	}
	return nil
}

func validateReminders(rs []Reminder) error {
	if len(rs) > maxReminders {
		return invalidf("at most %d reminders", maxReminders)
	}
	for _, r := range rs {
		if r.MinutesBefore < 0 || r.MinutesBefore > maxReminderMinutes {
			return invalidf("reminder minutes_before must be between 0 and %d", maxReminderMinutes)
		}
	}
	return nil
}

func remindersToAlarms(rs []Reminder) []icalx.Alarm {
	if len(rs) == 0 {
		return nil
	}
	out := make([]icalx.Alarm, 0, len(rs))
	for _, r := range rs {
		out = append(out, icalx.Alarm{
			Action:      icalx.ActionDisplay,
			Description: "Reminder",
			Trigger:     -time.Duration(r.MinutesBefore) * time.Minute,
			Related:     icalx.RelatedStart,
		})
	}
	return out
}

// alarmsToReminders reports the start-relative alarms that fire at or
// before the start. Other alarms (absolute, RELATED=END, after the start)
// are kept in ical_raw but not modelled by the API.
func alarmsToReminders(as []icalx.Alarm) []Reminder {
	var out []Reminder
	for _, a := range as {
		if !a.TriggerAt.IsZero() || strings.EqualFold(a.Related, icalx.RelatedEnd) || a.Trigger > 0 {
			continue
		}
		out = append(out, Reminder{MinutesBefore: int(-a.Trigger / time.Minute)})
	}
	return out
}

// resolveZone returns the location for a TZID the client sent, or a 400.
func resolveZone(tz string) (*time.Location, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, invalidf("unknown timezone %q (use an IANA name such as America/New_York)", tz)
	}
	return loc, nil
}

// placeMaster sets the master's times from ev in the icalx conventions.
func placeMaster(m *icalx.Event, ev *Event) error {
	m.AllDay = ev.AllDay
	m.Floating = false
	m.TZID = ""
	if ev.AllDay {
		m.Start, m.End = ev.Start, ev.End
		return nil
	}
	if ev.TimeZone != "" && ev.TimeZone != "UTC" {
		loc, err := resolveZone(ev.TimeZone)
		if err != nil {
			return err
		}
		m.TZID = ev.TimeZone
		m.Start, m.End = ev.Start.In(loc), ev.End.In(loc)
		return nil
	}
	m.Start, m.End = ev.Start.UTC(), ev.End.UTC()
	return nil
}

// exDatesFor converts the client's EXDATEs to the master's form: civil
// dates for all-day series, instants otherwise.
func exDatesFor(ev *Event) []time.Time {
	if len(ev.ExDates) == 0 {
		return nil
	}
	out := make([]time.Time, 0, len(ev.ExDates))
	for _, t := range ev.ExDates {
		if ev.AllDay {
			out = append(out, civilUTC(t))
		} else {
			out = append(out, t.UTC())
		}
	}
	return out
}

// applyToObject writes ev's fields onto obj's master (creating one when the
// object has none), validates the recurrence and returns the encoded
// VCALENDAR and the RRULE (nil for a single event). Properties and overrides
// icalx keeps from a client's original iCalendar are preserved.
func applyToObject(obj *icalx.Object, ev *Event, sequence int) (string, *string, error) {
	if obj.Master == nil {
		obj.Master = &icalx.Event{UID: obj.UID, Status: icalx.StatusConfirmed}
	}
	m := obj.Master
	m.UID = obj.UID
	m.Summary = ev.Title
	m.Description = ev.Description
	m.Location = ev.Location
	m.Sequence = sequence
	if m.Status == "" {
		m.Status = icalx.StatusConfirmed
	}
	if err := placeMaster(m, ev); err != nil {
		return "", nil, err
	}
	if ev.RRule == "" && m.RRule != "" {
		// The rule is being removed: the event becomes a single event, so
		// the instance list and per-instance overrides go with it.
		m.RDates = nil
		obj.Overrides = nil
	}
	m.RRule = ev.RRule
	m.ExDates = exDatesFor(ev)
	if ev.remindersSet {
		m.Alarms = remindersToAlarms(ev.Reminders)
	}

	if err := obj.Validate(); err != nil {
		if errors.Is(err, icalx.ErrUnsupportedRule) {
			return "", nil, invalidf("unsupported recurrence rule %q: Home OS supports MINUTELY or slower rules that produce at least one occurrence", ev.RRule)
		}
		return "", nil, invalidf("invalid rrule %q: %v", ev.RRule, err)
	}
	raw, err := obj.Encode()
	if err != nil {
		return "", nil, fmt.Errorf("encode ical: %w", err)
	}
	var rrule *string
	if m.RRule != "" {
		r := m.RRule
		rrule = &r
	}
	return string(raw), rrule, nil
}

// currentObject returns the parsed ical_raw when it is still current for
// the stored JSON (the rule CalDAV's renderObjectICal applies), or nil when
// there is no raw, it does not parse, or an edit through a JSON-only writer
// made it stale. A nil result means the JSON alone describes the event.
func currentObject(ev *Event, icalRaw *string) *icalx.Object {
	if icalRaw == nil || *icalRaw == "" {
		return nil
	}
	obj, err := icalx.Parse([]byte(*icalRaw))
	if err != nil || obj.Master == nil {
		return nil
	}
	m := obj.Master
	end := m.End
	if end.IsZero() || !end.After(m.Start) {
		end = m.Start.Add(time.Hour) // CalDAV ParseEvent's default
	}
	if m.Summary != ev.Title || m.Description != ev.Description || m.Location != ev.Location ||
		m.AllDay != ev.AllDay || !m.Start.Equal(ev.Start) || !end.Equal(ev.End) {
		return nil
	}
	return obj
}

// attachRecurrence copies the recurrence fields of a current raw object onto
// the event the API returns.
func attachRecurrence(ev *Event, obj *icalx.Object) {
	if obj == nil || obj.Master == nil {
		return
	}
	m := obj.Master
	ev.RRule = m.RRule
	ev.ExDates = append([]time.Time(nil), m.ExDates...)
	ev.TimeZone = m.TZID
	if m.TZID != "" && m.Start.Location() != nil && m.Start.Location() != time.UTC {
		ev.TimeZone = m.Start.Location().String()
	}
	ev.Reminders = alarmsToReminders(m.Alarms)
	ev.Recurring = m.IsRecurring() || len(obj.Overrides) > 0
}

// expandEvent returns the occurrences of a recurring event overlapping
// [from, to) as API events. Times use the JSON conventions: instants in UTC,
// all-day dates at midnight UTC. On an expansion error (a rule stored before
// validation existed, or one over the cost budget) the series is returned
// as-is, the way CalDAV serves it, rather than dropped.
func expandEvent(base Event, obj *icalx.Object, from, to time.Time, loc *time.Location) []Event {
	occs, err := obj.Expand(from, to, loc)
	if err != nil {
		log.Printf("calendar: expand event id=%s: %v (returning the series unexpanded)", base.ID, err)
		return []Event{base}
	}
	out := make([]Event, 0, len(occs))
	for _, occ := range occs {
		ev := base
		ev.Start, ev.End = occ.Start.UTC(), occ.End.UTC()
		if occ.AllDay {
			ev.Start, ev.End = civilUTC(occ.Start), civilUTC(occ.End)
		}
		ev.AllDay = occ.AllDay
		if occ.Override && occ.Event != nil {
			ev.Title = occ.Event.Summary
			ev.Description = occ.Event.Description
			ev.Location = occ.Event.Location
			ev.Reminders = alarmsToReminders(occ.Event.Alarms)
		}
		if !occ.RecurrenceID.IsZero() {
			rid := occ.RecurrenceID.UTC()
			if obj.Master != nil && obj.Master.AllDay {
				rid = civilUTC(occ.RecurrenceID)
			}
			ev.RecurrenceID = &rid
		}
		out = append(out, ev)
	}
	return out
}

// inRange is the overlap test for single (non-recurring) events, unchanged
// from before recurrence support: an event touching either bound counts.
func inRange(ev *Event, start, end *time.Time) bool {
	if start != nil && ev.End.Before(*start) {
		return false
	}
	if end != nil && ev.Start.After(*end) {
		return false
	}
	return true
}

func sortByStart(evs []Event) {
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].Start.Before(evs[j].Start) })
}
