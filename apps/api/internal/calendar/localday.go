package calendar

// LocalDate and Weekday on event reads (#4550).
//
// Every event a listing returns already carries `start` as an RFC 3339 instant.
// That is enough for a machine and not enough for a person: "2026-11-17T00:30:00Z"
// is Monday the 16th at 19:30 in New York, and any UI that turns that instant
// into a weekday has to reproduce the household's timezone rules to do it — a
// copy that drifts. The R1 war game's round-one calendar shifted an all-day
// event by a day exactly that way.
//
// So the reads answer the day themselves, in the household's zone, in the same
// shape the MCP's event_find does (#4346):
//
//	local_date  YYYY-MM-DD of the event's day in the household's zone
//	weekday     that day's weekday, e.g. "Tuesday"
//
// They are read-only: eventJSON (what is stored in ical_data) does not carry
// them, so a write never persists them and a stored event cannot go stale.

import "time"

// weekdayNames matches time.Weekday.String(), which the MCP emits.
var weekdayNames = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// setLocalDay fills LocalDate and Weekday from the event's start.
//
// An all-day event is stored as a civil date at midnight UTC (civilUTC), so its
// own UTC date IS its day: converting that instant into a zone west of UTC
// would move it to the previous day, which is the round-one bug. A timed event
// is dated by the wall clock in the household's zone (23:30 in New York is that
// New York day, even though it is 04:30Z the next day).
func setLocalDay(ev *Event, loc *time.Location) {
	ev.LocalDate, ev.Weekday = "", ""
	if ev.Start.IsZero() {
		return
	}
	if loc == nil {
		loc = time.UTC
	}
	var day time.Time
	if ev.AllDay {
		// The stored instant already is the civil date (midnight UTC).
		y, m, d := ev.Start.UTC().Date()
		day = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	} else {
		y, m, d := ev.Start.In(loc).Date()
		day = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	ev.LocalDate = day.Format("2006-01-02")
	ev.Weekday = weekdayNames[int(day.Weekday())]
}

// setLocalDays is setLocalDay over a page of events.
func setLocalDays(evs []Event, loc *time.Location) {
	for i := range evs {
		setLocalDay(&evs[i], loc)
	}
}

// LocalTime and the end fields on event reads (#4550).
//
// The same reason as local_date, one step further: a UI that renders "4:00 PM"
// from an RFC 3339 instant has to know the household's zone, and a page that
// asks the browser for it can be an hour out on the two days a year the clocks
// move. The listing therefore answers the wall clock it already used to place
// the event:
//
//	local_time       HH:MM in the household's zone; "" for an all-day event
//	end_local_date   the last day it occupies, inclusive (an all-day event's
//	                 stored end is exclusive midnight, so this is the day
//	                 before it)
//	end_local_time   HH:MM; "" for an all-day event
//
// Read-only, like LocalDate: not stored, so nothing to keep in sync.

// setLocalTimes fills LocalTime, EndLocalDate and EndLocalTime from the start
// and end instants, in the same zone setLocalDay used.
func setLocalTimes(ev *Event, loc *time.Location) {
	ev.LocalTime, ev.EndLocalDate, ev.EndLocalTime = "", "", ""
	if loc == nil {
		loc = time.UTC
	}
	if ev.AllDay {
		// The stored end is the exclusive next midnight; the last day it
		// occupies is the day before. A missing or one-day end stays one day.
		last := ev.Start
		if ev.End.After(ev.Start) {
			last = ev.End.AddDate(0, 0, -1)
		}
		y, m, d := last.UTC().Date()
		ev.EndLocalDate = time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		return
	}
	ev.LocalTime = ev.Start.In(loc).Format("15:04")
	if !ev.End.IsZero() {
		end := ev.End.In(loc)
		ev.EndLocalDate = end.Format("2006-01-02")
		ev.EndLocalTime = end.Format("15:04")
	}
}

// setLocalTimesAll is setLocalTimes over a page of events.
func setLocalTimesAll(evs []Event, loc *time.Location) {
	for i := range evs {
		setLocalTimes(&evs[i], loc)
	}
}

// DayInfo is one day of a requested range, as a listing answers it.
type DayInfo struct {
	// LocalDate is YYYY-MM-DD.
	LocalDate string `json:"local_date"`
	// Weekday is that day's name, e.g. "Tuesday".
	Weekday string `json:"weekday"`
}

// maxRangeDays bounds the day table a listing returns (a 3-year range would be
// a thousand rows nobody asked for; the UI views are days and weeks).
const maxRangeDays = 400

// rangeDays is the weekday table for a requested range: one entry per day, so
// a calendar grid draws its column headers from data instead of deriving a
// weekday from a date. The bounds are read as their UTC civil dates, the same
// convention the clients send them in (a date at 00:00Z), so the table lines up
// with the all-day events stored that way.
func rangeDays(from, to time.Time, limit int) []DayInfo {
	if limit <= 0 || limit > maxRangeDays {
		limit = maxRangeDays
	}
	out := []DayInfo{}
	if to.Before(from) || to.Equal(from) {
		return out
	}
	y, m, d := from.UTC().Date()
	for day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC); day.Before(to.UTC()); day = day.AddDate(0, 0, 1) {
		if len(out) >= limit {
			break
		}
		out = append(out, DayInfo{
			LocalDate: day.Format("2006-01-02"),
			Weekday:   weekdayNames[int(day.Weekday())],
		})
	}
	return out
}
