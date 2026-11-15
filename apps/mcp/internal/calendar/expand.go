package calendar

// Expanding repeating events into occurrences for range reads (#4483):
// event_find, agenda_get, calendar_find_free_time, family_overview and the
// overlap checks of event_create / event_update all read through
// queryOccurrences, which calls expandSeries for every repeating row.

import (
	"errors"
	"log/slog"
	"time"

	"home-os/icalx"
)

// maxSeriesOccurrences caps how many occurrences of one series a range read
// returns, so a never-ending, frequent series (a phone-made hourly rule)
// cannot blow up a response. A daily series over event_find's longest range
// (maxFindRangeDays) still fits whole.
const maxSeriesOccurrences = 400

// expandSeries returns the occurrences of the repeating series ev (read from
// s) that overlap [from, to), each as an Event: the series' fields with the
// occurrence's times, its recurrence_id and, for a moved or changed
// occurrence (a RECURRENCE-ID override), the override's title, times,
// location, reminders and people. Skipped dates (EXDATE) and cancelled
// overrides are left out; an override moved into the range from outside it
// is included. Expansion is icalx's (Object.Expand, in the family zone loc),
// the same the writes use to find an occurrence.
//
// truncated is true when the series had more occurrences in the range than
// maxSeriesOccurrences (or than icalx expands in one call); only the first
// ones are returned, so a page boundary never moves between calls. When the
// rule can't be expanded at all, ev is returned as stored (the caller's
// overlap check then decides) and the failure is logged.
func expandSeries(ev Event, s storedSeries, from, to time.Time, loc *time.Location) (occs []Event, truncated bool) {
	obj := s.obj
	m := obj.Master
	window := to
	found, err := obj.Expand(from, window, loc)
	// Too many instances for icalx's per-call cap: shrink the window until
	// it fits. The first occurrences are what a page shows first anyway.
	for errors.Is(err, icalx.ErrTooManyOccurrences) && window.Sub(from) > time.Hour {
		truncated = true
		window = from.Add(window.Sub(from) / 4)
		found, err = obj.Expand(from, window, loc)
	}
	if err != nil {
		slog.Warn("calendar: expand series", "event_id", ev.ID, "rrule", m.RRule, "error", err)
		return []Event{ev}, false
	}
	if len(found) > maxSeriesOccurrences {
		found, truncated = found[:maxSeriesOccurrences], true
	}

	occs = make([]Event, 0, len(found))
	for _, o := range found {
		if o.RecurrenceID.IsZero() {
			// A single event carrying overrides of itself (phone data): the
			// master is its only instance.
			occs = append(occs, ev)
			continue
		}
		var ov *icalx.Event
		if o.Override {
			ov = o.Event
		}
		occ := occurrenceEvent(ev, m, o.RecurrenceID, ov, loc)
		if !s.fromRaw {
			// The JSON (not ical_raw) describes the series: people live in
			// ical_data and there are no alarms to read from it.
			occ.People, occ.ReminderMinutes = ev.People, ev.ReminderMinutes
		}
		occs = append(occs, occ)
	}
	return occs, truncated
}
