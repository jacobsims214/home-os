package icalx

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

// MaxOccurrences caps how many instances one Expand call generates, so a
// FREQ=MINUTELY rule over a wide window cannot exhaust memory.
const MaxOccurrences = 10000

// ErrTooManyOccurrences is returned when expansion would exceed MaxOccurrences.
var ErrTooManyOccurrences = errors.New("icalx: too many occurrences in range")

// Occurrence is one instance of an event within an expansion window.
type Occurrence struct {
	UID string
	// Start and End in the location passed to Expand. All-day occurrences
	// start at local midnight and End is midnight after the last day.
	Start  time.Time
	End    time.Time
	AllDay bool
	// RecurrenceID identifies the instance within its series: its original
	// (unmodified) start, as written in RECURRENCE-ID / EXDATE. Zero for a
	// non-recurring event.
	RecurrenceID time.Time
	// Override is true when the instance comes from a RECURRENCE-ID VEVENT.
	Override bool
	// Event supplies the instance's summary, location, alarms, etc.: the
	// override when there is one, otherwise the master.
	Event *Event
}

// Expand returns the occurrences that overlap [from, to), sorted by start.
// loc is the zone used for floating and all-day events and for the returned
// times; nil means UTC. Cancelled overrides and EXDATEs are omitted.
//
// An event with zero duration overlaps the window if from <= start < to.
func (o *Object) Expand(from, to time.Time, loc *time.Location) ([]Occurrence, error) {
	if loc == nil {
		loc = time.UTC
	}
	if !to.After(from) {
		return nil, nil
	}

	overrides := map[string]*Event{}
	for _, ov := range o.Overrides {
		overrides[instanceKey(ov.RecurrenceID, o.anchor(ov))] = ov
	}

	var out []Occurrence
	if m := o.Master; m != nil && m.Status != StatusCancelled {
		starts, err := m.instances(from, to, loc)
		if err != nil {
			return nil, err
		}
		excluded := map[string]bool{}
		for _, ex := range m.ExDates {
			excluded[instanceKey(ex, m)] = true
		}
		recurring := m.IsRecurring()
		for _, s := range starts {
			key := instanceKey(s, m)
			if excluded[key] {
				continue
			}
			if _, ok := overrides[key]; ok {
				continue // emitted below from the override itself
			}
			occ := Occurrence{UID: o.UID, AllDay: m.AllDay, Event: m}
			occ.Start, occ.End = m.span(s, loc)
			if recurring {
				occ.RecurrenceID = s
			}
			if overlaps(occ.Start, occ.End, from, to) {
				out = append(out, occ)
			}
		}
	}

	for _, ov := range o.Overrides {
		if ov.Status == StatusCancelled {
			continue
		}
		if m := o.Master; m != nil && isExcluded(m, ov.RecurrenceID) {
			continue
		}
		occ := Occurrence{
			UID: o.UID, AllDay: ov.AllDay, Override: true, Event: ov,
			RecurrenceID: ov.RecurrenceID,
		}
		occ.Start, occ.End = ov.span(ov.Start, loc)
		if overlaps(occ.Start, occ.End, from, to) {
			out = append(out, occ)
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// anchor returns the event whose DTSTART form an override's RECURRENCE-ID is
// compared in: the master if present, otherwise the override itself.
func (o *Object) anchor(ov *Event) *Event {
	if o.Master != nil {
		return o.Master
	}
	return ov
}

func isExcluded(m *Event, t time.Time) bool {
	k := instanceKey(t, m)
	for _, ex := range m.ExDates {
		if instanceKey(ex, m) == k {
			return true
		}
	}
	return false
}

// instanceKey identifies a recurrence instance independent of how its time
// was written: civil date for all-day series, wall clock for floating ones,
// the UTC instant otherwise.
func instanceKey(t time.Time, series *Event) string {
	switch {
	case series.AllDay:
		return "date:" + t.Format("20060102")
	case series.Floating:
		return "wall:" + t.Format("20060102T150405")
	default:
		return "utc:" + t.UTC().Format("20060102T150405")
	}
}

func overlaps(start, end, from, to time.Time) bool {
	if !end.After(start) {
		return !start.Before(from) && start.Before(to)
	}
	return start.Before(to) && end.After(from)
}

// place converts an instance start from the series' own representation into
// a real instant in loc.
func (e *Event) place(s time.Time, loc *time.Location) time.Time {
	if e.AllDay || e.Floating {
		return time.Date(s.Year(), s.Month(), s.Day(), s.Hour(), s.Minute(), s.Second(), 0, loc)
	}
	return s.In(loc)
}

// span returns the real start and end of an instance starting at s (in the
// series' representation). All-day spans are counted in calendar days so a
// one-day event is still one day long on a 23- or 25-hour DST day.
func (e *Event) span(s time.Time, loc *time.Location) (time.Time, time.Time) {
	start := e.place(s, loc)
	if e.AllDay {
		days := int(e.End.Sub(e.Start).Hours()+12) / 24
		if days < 1 {
			days = 1
		}
		return start, start.AddDate(0, 0, days)
	}
	return start, start.Add(e.Duration())
}

// instances returns the instance starts of e (in e's own representation)
// that might overlap [from, to): a padded superset; callers filter exactly.
func (e *Event) instances(from, to time.Time, loc *time.Location) ([]time.Time, error) {
	if !e.IsRecurring() {
		return []time.Time{e.Start}, nil
	}

	// Window in the series' representation, padded by the event's length and
	// two days to absorb zone offsets for floating / all-day series.
	pad := e.Duration() + 48*time.Hour
	lo, hi := from.Add(-pad), to.Add(48*time.Hour)
	if e.AllDay || e.Floating {
		lo = wallUTC(from.In(loc)).Add(-pad)
		hi = wallUTC(to.In(loc)).Add(48 * time.Hour)
	}

	set := &rrule.Set{}
	if e.RRule != "" {
		ruleLoc := e.Start.Location()
		opt, err := rrule.StrToROptionInLocation(strings.TrimPrefix(e.RRule, "RRULE:"), ruleLoc)
		if err != nil {
			return nil, fmt.Errorf("icalx: UID %q RRULE: %w", e.UID, err)
		}
		opt.Dtstart = e.Start
		if (e.AllDay || e.Floating) && untilIsUTC(e.RRule) {
			// UNTIL in UTC on a floating/all-day series: convert it to the
			// viewer's wall clock, which is what the series is expanded in.
			opt.Until = wallUTC(opt.Until.In(loc))
			if e.AllDay {
				opt.Until = time.Date(opt.Until.Year(), opt.Until.Month(), opt.Until.Day(), 0, 0, 0, 0, time.UTC)
			}
		}
		r, err := rrule.NewRRule(*opt)
		if err != nil {
			return nil, fmt.Errorf("icalx: UID %q RRULE: %w", e.UID, err)
		}
		set.RRule(r)
	} else {
		// RDATE-only series: DTSTART is itself an instance.
		set.RDate(e.Start)
	}
	for _, rd := range e.RDates {
		set.RDate(rd.In(e.Start.Location()))
	}

	var out []time.Time
	next := set.Iterator()
	for {
		t, ok := next()
		if !ok || !t.Before(hi) {
			break
		}
		if t.Before(lo) {
			continue
		}
		out = append(out, t)
		if len(out) > MaxOccurrences {
			return nil, ErrTooManyOccurrences
		}
	}
	return out, nil
}

// untilIsUTC reports whether the rule's UNTIL is a UTC date-time ("...Z").
func untilIsUTC(rule string) bool {
	for _, part := range strings.Split(strings.ToUpper(rule), ";") {
		if v, ok := strings.CutPrefix(part, "UNTIL="); ok {
			return strings.HasSuffix(v, "Z")
		}
	}
	return false
}

// wallUTC returns t's wall-clock reading as a UTC time.
func wallUTC(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

// AlarmFiring is one moment an alarm should fire.
type AlarmFiring struct {
	At         time.Time // in the location passed to Alarms
	Alarm      Alarm
	Occurrence Occurrence
}

// Alarms returns the alarm firings in [from, to), sorted by time, including
// REPEAT firings. Occurrences are expanded far enough around the window to
// catch alarms set days before (or after) their instance.
func (o *Object) Alarms(from, to time.Time, loc *time.Location) ([]AlarmFiring, error) {
	if loc == nil {
		loc = time.UTC
	}
	// A firing at anchor+offset lands in [from, to) only if the instance
	// starts within [from-maxOff-maxDur, to-minOff].
	var minOff, maxOff, maxDur time.Duration
	for _, ev := range o.events() {
		if d := ev.Duration(); d > maxDur {
			maxDur = d
		}
		for _, a := range ev.Alarms {
			if !a.TriggerAt.IsZero() {
				continue
			}
			minOff = min(minOff, a.Trigger)
			maxOff = max(maxOff, a.Trigger+time.Duration(a.Repeat)*a.Interval)
		}
	}
	occs, err := o.Expand(from.Add(-maxOff-maxDur), to.Add(-minOff+time.Second), loc)
	if err != nil {
		return nil, err
	}

	seenAbs := map[string]bool{}
	var out []AlarmFiring
	for _, occ := range occs {
		for _, a := range occ.Event.Alarms {
			var base time.Time
			switch {
			case !a.TriggerAt.IsZero():
				key := fmt.Sprintf("%p|%s", occ.Event, a.TriggerAt)
				if seenAbs[key] {
					continue // an absolute trigger fires once, not per instance
				}
				seenAbs[key] = true
				base = a.TriggerAt.In(loc)
			case a.Related == RelatedEnd:
				base = occ.End.Add(a.Trigger)
			default:
				base = occ.Start.Add(a.Trigger)
			}
			for i := 0; i <= a.Repeat; i++ {
				at := base.Add(time.Duration(i) * a.Interval)
				if !at.Before(from) && at.Before(to) {
					out = append(out, AlarmFiring{At: at, Alarm: a, Occurrence: occ})
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}
