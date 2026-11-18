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

// MaxIterations caps how many instances one expansion pulls from the RRULE
// iterator in total, including those before the window that are skipped.
// Together with the skip-ahead and the rule checks in checkRule it bounds the
// CPU one Expand call can spend, whatever the rule and however old DTSTART is.
const MaxIterations = 100000

// ErrTooManyOccurrences is returned when expansion would exceed MaxOccurrences.
var ErrTooManyOccurrences = errors.New("icalx: too many occurrences in range")

// ErrExpansionBudget is returned when expansion would pull more than
// MaxIterations instances from the rule (counting those before the window).
// Whether the series overlaps the window is then unknown.
var ErrExpansionBudget = errors.New("icalx: recurrence expansion budget exceeded")

// ErrUnsupportedRule is returned (wrapped, with detail) for RRULEs icalx
// refuses to expand because they are sub-minute (FREQ=SECONDLY), can loop
// without producing instances (BYSETPOS on HOURLY/MINUTELY, BYHOUR/BYMINUTE
// the INTERVAL never reaches), or never produce any instance. Families never
// need these; CalDAV PUT rejects them so they are never stored.
var ErrUnsupportedRule = errors.New("icalx: unsupported recurrence rule")

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
		opt, err := e.ruleOption(loc)
		if err != nil {
			return nil, err
		}
		if err := checkRule(e.UID, opt); err != nil {
			return nil, err
		}
		if opt.Count == 0 {
			opt.Dtstart = skipAhead(opt, lo)
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
	iterations := 0
	next := set.Iterator()
	for {
		t, ok := next()
		if !ok || !t.Before(hi) {
			break
		}
		// Count every instance, including skipped ones: MaxOccurrences
		// alone only bounds what lands inside the window.
		if iterations++; iterations > MaxIterations {
			return nil, ErrExpansionBudget
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

// ruleOption parses e.RRule into rrule-go options anchored at e.Start. loc is
// the viewer's zone, used for a UTC UNTIL on a floating or all-day series.
func (e *Event) ruleOption(loc *time.Location) (*rrule.ROption, error) {
	opt, err := rrule.StrToROptionInLocation(strings.TrimPrefix(e.RRule, "RRULE:"), e.Start.Location())
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
	if opt.Interval < 1 {
		opt.Interval = 1
	}
	return opt, nil
}

// Validate reports whether every RRULE in o can be expanded. It returns an
// error wrapping ErrUnsupportedRule for rules Expand refuses (see
// ErrUnsupportedRule), or the parse error for a malformed RRULE. CalDAV PUT
// calls it so such rules are rejected instead of stored.
func (o *Object) Validate() error {
	for _, ev := range o.events() {
		if ev.RRule == "" {
			continue
		}
		opt, err := ev.ruleOption(time.UTC)
		if err != nil {
			return err
		}
		if err := checkRule(ev.UID, opt); err != nil {
			return err
		}
	}
	return nil
}

// checkRule rejects rules that rrule-go cannot iterate in bounded time.
// rrule-go only checks UNTIL and COUNT on produced instances, so a rule that
// produces nothing runs to year 9999 (or, for the lattice case, forever).
func checkRule(uid string, opt *rrule.ROption) error {
	unsupported := func(why string) error {
		return fmt.Errorf("%w: UID %q: %s", ErrUnsupportedRule, uid, why)
	}
	switch opt.Freq {
	case rrule.SECONDLY:
		return unsupported("FREQ=SECONDLY is not supported")
	case rrule.HOURLY, rrule.MINUTELY:
		if len(opt.Bysetpos) > 0 {
			return unsupported("BYSETPOS with FREQ=HOURLY or MINUTELY is not supported")
		}
		if !latticeReachable(opt) {
			return unsupported("BYHOUR/BYMINUTE are never reached at this INTERVAL")
		}
	}
	if neverMatches(*opt) {
		return unsupported("rule produces no instances")
	}
	return nil
}

// latticeReachable reports whether a HOURLY/MINUTELY rule's BYHOUR/BYMINUTE
// can ever be hit. rrule-go advances the hour (minute) counter by INTERVAL and
// loops until it lands on an allowed value, with no exit, so the visited
// values, DTSTART's value plus multiples of gcd(INTERVAL, 24) hours
// (gcd(INTERVAL, 1440) minutes) mod one day, must include an allowed one.
func latticeReachable(opt *rrule.ROption) bool {
	h0, m0 := opt.Dtstart.Hour(), opt.Dtstart.Minute()
	hours := opt.Byhour
	if len(hours) == 0 {
		hours = seq(24)
	}
	if opt.Freq == rrule.HOURLY {
		if len(opt.Byhour) == 0 {
			return true
		}
		g := gcd(opt.Interval, 24)
		for _, h := range hours {
			if (h-h0)%g == 0 {
				return true
			}
		}
		return false
	}
	if len(opt.Byhour) == 0 && len(opt.Byminute) == 0 {
		return true
	}
	minutes := opt.Byminute
	if len(minutes) == 0 {
		minutes = seq(60)
	}
	g := gcd(opt.Interval, 1440)
	for _, h := range hours {
		for _, m := range minutes {
			if (h*60+m-(h0*60+m0))%g == 0 {
				return true
			}
		}
	}
	return false
}

// neverMatches reports whether the rule produces no instance at all. Every
// rrule-go day mask (month, weekday, week number, year day, month day) repeats
// every 400 Gregorian years (146097 days, a whole number of weeks), so the
// rule is run from DTSTART moved forward by whole 400-year cycles to between
// 400 and 800 years before rrule-go's MAXYEAR (9999), keeping its alignment.
// No instance there means none ever. The probe costs at most those 800 years
// of iteration, and returns at the first instance for any rule that matches.
// A rule whose INTERVAL period doesn't divide the cycle may be judged empty;
// such rules (e.g. YEARLY;INTERVAL=401) are not something families write.
func neverMatches(opt rrule.ROption) bool {
	d := opt.Dtstart
	y := d.Year()
	if y < 9600 {
		y += (9600 - y) / 400 * 400
	}
	opt.Dtstart = time.Date(y, d.Month(), d.Day(), d.Hour(), d.Minute(), d.Second(), 0, time.UTC)
	opt.Count = 0
	// Without UNTIL rrule-go stops at DTSTART+~292y; look at the whole span.
	opt.Until = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	r, err := rrule.NewRRule(opt)
	if err != nil {
		return false // NewRRule on the real DTSTART reports it
	}
	_, ok := r.Iterator()()
	return !ok
}

// skipAhead returns a DTSTART for a COUNT-less HOURLY, MINUTELY, DAILY or
// WEEKLY rule moved forward by whole INTERVAL periods to at least one period
// before lo, so expansion doesn't walk every instance since the original
// DTSTART. rrule-go advances those rules by adding to wall-clock fields
// (normalising overflow), which is plain arithmetic on the wall clock read as
// UTC, so the moved DTSTART is on the same lattice and yields exactly the same
// instances from there on. Defaults derived from DTSTART (minute and second,
// weekday) are unchanged because periods are whole hours, minutes, days or
// weeks. Other frequencies, and short histories, return DTSTART as is.
func skipAhead(opt *rrule.ROption, lo time.Time) time.Time {
	var unit int64
	switch opt.Freq {
	case rrule.MINUTELY:
		unit = 60
	case rrule.HOURLY:
		unit = 3600
	case rrule.DAILY:
		unit = 86400
	case rrule.WEEKLY:
		unit = 7 * 86400
	default:
		return opt.Dtstart
	}
	ds := opt.Dtstart
	ruleLoc := ds.Location()
	period := unit * int64(opt.Interval)
	dWall := wallUTC(ds).Unix()
	target := wallUTC(lo.In(ruleLoc)).Unix()
	k := (target-dWall)/period - 1
	// A wall time in a DST gap is normalised by time.Date, which would move
	// the counters off the lattice: step back a period and retry.
	for ; k > 0; k-- {
		w := time.Unix(dWall+k*period, 0).UTC()
		s := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, ruleLoc)
		if wallUTC(s).Equal(w) {
			return s
		}
	}
	return ds
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
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
