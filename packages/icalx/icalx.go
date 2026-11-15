// Package icalx parses, builds and expands iCalendar (RFC 5545) events for
// Home OS. It is shared by the CalDAV server, the API, the MCP server and the
// alarm worker, so it has no dependency on any of them.
//
// One Object is one calendar object resource as CalDAV stores it: all the
// VEVENTs that share a UID, which is a master event (with RRULE, RDATE and
// EXDATE) plus any overridden instances (VEVENTs with RECURRENCE-ID).
//
// Time representation:
//   - Timed events with a TZID carry that location (resolved from IANA,
//     Windows or vendor-prefixed names). Recurrences keep the wall-clock time
//     in that zone, so a 09:00 meeting stays at 09:00 across a DST change.
//   - UTC events ("...Z") are in time.UTC.
//   - Floating events (no TZID, no Z) and all-day events (VALUE=DATE) are
//     stored in time.UTC as wall-clock / civil-date values with Floating or
//     AllDay set. Expand places them in the requested location.
package icalx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

// ProdID is written into VCALENDARs this package builds.
const ProdID = "-//Home OS//icalx//EN"

// Event status values (RFC 5545 3.8.1.11).
const (
	StatusTentative = "TENTATIVE"
	StatusConfirmed = "CONFIRMED"
	StatusCancelled = "CANCELLED"
)

// Alarm actions and trigger anchors (RFC 5545 3.8.6).
const (
	ActionDisplay = "DISPLAY"
	ActionAudio   = "AUDIO"
	ActionEmail   = "EMAIL"

	RelatedStart = "START"
	RelatedEnd   = "END"
)

// ErrNoEvent is returned when the iCalendar data holds no VEVENT.
var ErrNoEvent = errors.New("icalx: no VEVENT in calendar")

// ErrMultipleUIDs is returned by Parse when the data holds VEVENTs with more
// than one UID. CalDAV allows a single UID per resource; use ParseAll for
// .ics imports that bundle many events.
var ErrMultipleUIDs = errors.New("icalx: calendar holds more than one UID")

// Event is one VEVENT: a master event or an overridden instance.
type Event struct {
	UID         string
	Summary     string
	Description string
	Location    string
	Status      string // TENTATIVE, CONFIRMED, CANCELLED, or ""
	Sequence    int

	// Start and End (exclusive). For AllDay events these are civil dates at
	// midnight UTC and End is the day after the last day.
	Start    time.Time
	End      time.Time
	AllDay   bool
	Floating bool

	// TZID as written in the source (or the IANA name for built events).
	// Empty for UTC, floating and all-day events.
	TZID string

	// RRule is the RRULE value without the "RRULE:" prefix, e.g.
	// "FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261231T235959Z". Master only.
	RRule   string
	RDates  []time.Time
	ExDates []time.Time

	// RecurrenceID is the original start of the instance this VEVENT
	// overrides. Zero for the master.
	RecurrenceID time.Time

	Alarms []Alarm

	// Properties and child components this package does not model
	// (ATTENDEE, ORGANIZER, CATEGORIES, X-*, ...), kept for round trips.
	extra ical.Props
}

// IsOverride reports whether e overrides a single instance of a series.
func (e *Event) IsOverride() bool { return !e.RecurrenceID.IsZero() }

// IsRecurring reports whether e defines a recurrence (RRULE or RDATE).
func (e *Event) IsRecurring() bool { return e.RRule != "" || len(e.RDates) > 0 }

// Duration returns End-Start. For all-day events this is a multiple of 24h.
func (e *Event) Duration() time.Duration { return e.End.Sub(e.Start) }

// Alarm is one VALARM.
type Alarm struct {
	Action      string // DISPLAY, AUDIO, EMAIL
	Description string
	Summary     string

	// Trigger is relative to the event's start (or end when Related is
	// "END"); negative means before. Ignored when TriggerAt is set.
	Trigger time.Duration
	Related string // START (default) or END
	// TriggerAt is an absolute trigger (TRIGGER;VALUE=DATE-TIME), in UTC.
	TriggerAt time.Time

	// Repeat extra firings, Interval apart (REPEAT / DURATION).
	Repeat   int
	Interval time.Duration

	extra ical.Props
}

// Object is one calendar object resource: the VEVENTs sharing a UID.
type Object struct {
	UID       string
	Master    *Event   // nil when the resource holds only overrides
	Overrides []*Event // VEVENTs with RECURRENCE-ID, in source order

	timezones []*ical.Component // VTIMEZONEs from the source, kept for round trips
}

// Parse parses a VCALENDAR holding exactly one UID (a CalDAV resource).
func Parse(data []byte) (*Object, error) {
	objs, err := ParseAll(data)
	if err != nil {
		return nil, err
	}
	if len(objs) > 1 {
		return nil, ErrMultipleUIDs
	}
	return objs[0], nil
}

// ParseAll parses a VCALENDAR and groups its VEVENTs by UID, in order of first
// appearance. Non-VEVENT components other than VTIMEZONE are ignored.
func ParseAll(data []byte) ([]*Object, error) {
	cal, err := ical.NewDecoder(bytes.NewReader(data)).Decode()
	if err != nil {
		return nil, fmt.Errorf("icalx: decode: %w", err)
	}

	tzHints := map[string]string{} // TZID -> X-LIC-LOCATION
	var timezones []*ical.Component
	for _, c := range cal.Children {
		if c.Name != ical.CompTimezone {
			continue
		}
		timezones = append(timezones, c)
		if id, _ := c.Props.Text(ical.PropTimezoneID); id != "" {
			if p := c.Props.Get("X-LIC-LOCATION"); p != nil {
				tzHints[id] = p.Value
			}
		}
	}

	byUID := map[string]*Object{}
	var order []*Object
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent {
			continue
		}
		ev, err := parseEvent(c, tzHints)
		if err != nil {
			return nil, err
		}
		obj := byUID[ev.UID]
		if obj == nil {
			obj = &Object{UID: ev.UID, timezones: timezones}
			byUID[ev.UID] = obj
			order = append(order, obj)
		}
		if ev.IsOverride() {
			obj.Overrides = append(obj.Overrides, ev)
		} else if obj.Master == nil {
			obj.Master = ev
		} else {
			return nil, fmt.Errorf("icalx: UID %q has two master VEVENTs", ev.UID)
		}
	}
	if len(order) == 0 {
		return nil, ErrNoEvent
	}
	return order, nil
}

// Modelled VEVENT properties; everything else goes to Event.extra.
var eventProps = map[string]bool{
	ical.PropUID: true, ical.PropSummary: true, ical.PropDescription: true,
	ical.PropLocation: true, ical.PropStatus: true, ical.PropSequence: true,
	ical.PropDateTimeStart: true, ical.PropDateTimeEnd: true, ical.PropDuration: true,
	ical.PropRecurrenceRule: true, ical.PropRecurrenceDates: true,
	ical.PropExceptionDates: true, ical.PropRecurrenceID: true,
	ical.PropDateTimeStamp: true,
}

func parseEvent(c *ical.Component, tzHints map[string]string) (*Event, error) {
	ev := &Event{extra: ical.Props{}}
	text := func(name string) string {
		if p := c.Props.Get(name); p != nil {
			s, err := p.Text()
			if err == nil {
				return s
			}
			return p.Value
		}
		return ""
	}
	ev.UID = text(ical.PropUID)
	if ev.UID == "" {
		return nil, errors.New("icalx: VEVENT without UID")
	}
	ev.Summary = text(ical.PropSummary)
	ev.Description = text(ical.PropDescription)
	ev.Location = text(ical.PropLocation)
	ev.Status = strings.ToUpper(text(ical.PropStatus))
	if s := text(ical.PropSequence); s != "" {
		ev.Sequence, _ = strconv.Atoi(strings.TrimSpace(s))
	}

	dtstart := c.Props.Get(ical.PropDateTimeStart)
	if dtstart == nil {
		return nil, fmt.Errorf("icalx: VEVENT %q without DTSTART", ev.UID)
	}
	start, kind, err := parseTime(dtstart.Value, dtstart.Params, tzHints)
	if err != nil {
		return nil, fmt.Errorf("icalx: VEVENT %q DTSTART: %w", ev.UID, err)
	}
	ev.Start = start.t
	ev.AllDay = kind == kindDate
	ev.Floating = kind == kindFloating
	ev.TZID = start.tzid

	switch {
	case c.Props.Get(ical.PropDateTimeEnd) != nil:
		p := c.Props.Get(ical.PropDateTimeEnd)
		end, _, err := parseTime(p.Value, p.Params, tzHints)
		if err != nil {
			return nil, fmt.Errorf("icalx: VEVENT %q DTEND: %w", ev.UID, err)
		}
		ev.End = end.t
	case c.Props.Get(ical.PropDuration) != nil:
		d, err := parseDuration(c.Props.Get(ical.PropDuration).Value)
		if err != nil {
			return nil, fmt.Errorf("icalx: VEVENT %q DURATION: %w", ev.UID, err)
		}
		ev.End = ev.Start.Add(d)
	case ev.AllDay:
		ev.End = ev.Start.AddDate(0, 0, 1)
	default:
		ev.End = ev.Start
	}
	if ev.End.Before(ev.Start) {
		ev.End = ev.Start
	}

	if p := c.Props.Get(ical.PropRecurrenceRule); p != nil {
		ev.RRule = strings.TrimSpace(p.Value)
	}
	for _, name := range []string{ical.PropRecurrenceDates, ical.PropExceptionDates} {
		for _, p := range c.Props.Values(name) {
			for _, v := range strings.Split(p.Value, ",") {
				if v = strings.TrimSpace(v); v == "" {
					continue
				}
				if i := strings.IndexByte(v, '/'); i >= 0 { // RDATE;VALUE=PERIOD
					v = v[:i]
				}
				params := p.Params
				if strings.EqualFold(params.Get(ical.ParamValue), "PERIOD") {
					params = cloneParams(params)
					params.Del(ical.ParamValue)
				}
				t, _, err := parseTime(v, params, tzHints)
				if err != nil {
					return nil, fmt.Errorf("icalx: VEVENT %q %s: %w", ev.UID, name, err)
				}
				if name == ical.PropRecurrenceDates {
					ev.RDates = append(ev.RDates, t.t)
				} else {
					ev.ExDates = append(ev.ExDates, t.t)
				}
			}
		}
	}
	if p := c.Props.Get(ical.PropRecurrenceID); p != nil {
		rid, _, err := parseTime(p.Value, p.Params, tzHints)
		if err != nil {
			return nil, fmt.Errorf("icalx: VEVENT %q RECURRENCE-ID: %w", ev.UID, err)
		}
		ev.RecurrenceID = rid.t
	}

	for name, props := range c.Props {
		if !eventProps[name] {
			ev.extra[name] = append([]ical.Prop(nil), props...)
		}
	}

	for _, child := range c.Children {
		if child.Name != ical.CompAlarm {
			continue // RFC 5545 allows only VALARM inside VEVENT
		}
		a, err := parseAlarm(child, tzHints)
		if err != nil {
			return nil, fmt.Errorf("icalx: VEVENT %q VALARM: %w", ev.UID, err)
		}
		ev.Alarms = append(ev.Alarms, a)
	}
	return ev, nil
}

var alarmProps = map[string]bool{
	ical.PropAction: true, ical.PropDescription: true, ical.PropSummary: true,
	ical.PropTrigger: true, ical.PropRepeat: true, ical.PropDuration: true,
}

func parseAlarm(c *ical.Component, tzHints map[string]string) (Alarm, error) {
	a := Alarm{Related: RelatedStart, extra: ical.Props{}}
	if p := c.Props.Get(ical.PropAction); p != nil {
		a.Action = strings.ToUpper(strings.TrimSpace(p.Value))
	}
	if p := c.Props.Get(ical.PropDescription); p != nil {
		a.Description, _ = p.Text()
	}
	if p := c.Props.Get(ical.PropSummary); p != nil {
		a.Summary, _ = p.Text()
	}
	p := c.Props.Get(ical.PropTrigger)
	if p == nil {
		return a, errors.New("missing TRIGGER")
	}
	if strings.EqualFold(p.Params.Get(ical.ParamValue), "DATE-TIME") {
		t, _, err := parseTime(p.Value, p.Params, tzHints)
		if err != nil {
			return a, fmt.Errorf("TRIGGER: %w", err)
		}
		a.TriggerAt = t.t.UTC()
	} else {
		d, err := parseDuration(p.Value)
		if err != nil {
			return a, fmt.Errorf("TRIGGER: %w", err)
		}
		a.Trigger = d
		if strings.EqualFold(p.Params.Get(ical.ParamRelated), RelatedEnd) {
			a.Related = RelatedEnd
		}
	}
	if p := c.Props.Get(ical.PropRepeat); p != nil {
		a.Repeat, _ = strconv.Atoi(strings.TrimSpace(p.Value))
	}
	if p := c.Props.Get(ical.PropDuration); p != nil {
		d, err := parseDuration(p.Value)
		if err != nil {
			return a, fmt.Errorf("DURATION: %w", err)
		}
		a.Interval = d
	}
	for name, props := range c.Props {
		if !alarmProps[name] {
			a.extra[name] = append([]ical.Prop(nil), props...)
		}
	}
	return a, nil
}

// ---- encoding ----

// Encode serialises the object as a VCALENDAR with the master first, then the
// overrides, then a VTIMEZONE for every TZID referenced (taken from the
// source when it had one, generated from the Go tz database otherwise).
func (o *Object) Encode() ([]byte, error) {
	return Build(o.events(), o.timezones...)
}

func (o *Object) events() []*Event {
	evs := make([]*Event, 0, 1+len(o.Overrides))
	if o.Master != nil {
		evs = append(evs, o.Master)
	}
	return append(evs, o.Overrides...)
}

// Build serialises events as one VCALENDAR. Extra VTIMEZONE components may be
// passed to reuse a client's definitions; missing ones are generated.
func Build(events []*Event, timezones ...*ical.Component) ([]byte, error) {
	if len(events) == 0 {
		return nil, ErrNoEvent
	}
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, ProdID)

	have := map[string]bool{}
	for _, tz := range timezones {
		id, _ := tz.Props.Text(ical.PropTimezoneID)
		if id == "" || have[id] {
			continue
		}
		have[id] = true
		cal.Children = append(cal.Children, tz)
	}

	stamp := time.Now().UTC().Truncate(time.Second)
	var needTZ []*time.Location
	var needIDs []string
	for _, ev := range events {
		comp, err := encodeEvent(ev, stamp)
		if err != nil {
			return nil, err
		}
		if ev.TZID != "" && !ev.AllDay && !ev.Floating && !have[ev.TZID] {
			have[ev.TZID] = true
			needTZ = append(needTZ, ev.Start.Location())
			needIDs = append(needIDs, ev.TZID)
		}
		cal.Children = append(cal.Children, comp)
	}
	for i, loc := range needTZ {
		year := events[0].Start.Year()
		tz := vtimezone(needIDs[i], loc, year)
		// VTIMEZONEs conventionally precede the events that use them.
		cal.Children = append([]*ical.Component{tz}, cal.Children...)
	}

	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, fmt.Errorf("icalx: encode: %w", err)
	}
	return buf.Bytes(), nil
}

func encodeEvent(ev *Event, stamp time.Time) (*ical.Component, error) {
	if ev.UID == "" {
		return nil, errors.New("icalx: event without UID")
	}
	if ev.Start.IsZero() {
		return nil, fmt.Errorf("icalx: event %q without start", ev.UID)
	}
	c := ical.NewComponent(ical.CompEvent)
	for name, props := range ev.extra {
		c.Props[name] = append([]ical.Prop(nil), props...)
	}
	if c.Props.Get(ical.PropDateTimeStamp) == nil {
		c.Props.SetDateTime(ical.PropDateTimeStamp, stamp)
	}
	c.Props.SetText(ical.PropUID, ev.UID)
	setText := func(name, v string) {
		if v != "" {
			c.Props.SetText(name, v)
		}
	}
	setText(ical.PropSummary, ev.Summary)
	setText(ical.PropDescription, ev.Description)
	setText(ical.PropLocation, ev.Location)
	if ev.Status != "" {
		p := ical.NewProp(ical.PropStatus)
		p.Value = ev.Status
		c.Props.Set(p)
	}
	if ev.Sequence != 0 {
		p := ical.NewProp(ical.PropSequence)
		p.Value = strconv.Itoa(ev.Sequence)
		c.Props.Set(p)
	}

	c.Props.Set(ev.timeProp(ical.PropDateTimeStart, ev.Start))
	if ev.End.After(ev.Start) {
		c.Props.Set(ev.timeProp(ical.PropDateTimeEnd, ev.End))
	}
	if ev.RRule != "" {
		p := ical.NewProp(ical.PropRecurrenceRule)
		p.Value = strings.TrimPrefix(ev.RRule, "RRULE:")
		c.Props.Set(p)
	}
	for _, t := range ev.RDates {
		c.Props.Add(ev.timeProp(ical.PropRecurrenceDates, t))
	}
	for _, t := range ev.ExDates {
		c.Props.Add(ev.timeProp(ical.PropExceptionDates, t))
	}
	if !ev.RecurrenceID.IsZero() {
		c.Props.Set(ev.timeProp(ical.PropRecurrenceID, ev.RecurrenceID))
	}
	for _, a := range ev.Alarms {
		ac, err := encodeAlarm(a)
		if err != nil {
			return nil, fmt.Errorf("icalx: event %q: %w", ev.UID, err)
		}
		c.Children = append(c.Children, ac)
	}
	return c, nil
}

// timeProp writes t in the same form as the event's DTSTART (RFC 5545
// requires EXDATE, RDATE and RECURRENCE-ID to match it).
func (ev *Event) timeProp(name string, t time.Time) *ical.Prop {
	p := ical.NewProp(name)
	switch {
	case ev.AllDay:
		p.Params.Set(ical.ParamValue, string(ical.ValueDate))
		p.Value = t.Format("20060102")
	case ev.Floating:
		p.Value = t.Format("20060102T150405")
	case ev.TZID != "":
		p.Params.Set(ical.ParamTimezoneID, ev.TZID)
		p.Value = t.In(ev.Start.Location()).Format("20060102T150405")
	default:
		p.Value = t.UTC().Format("20060102T150405Z")
	}
	return p
}

func encodeAlarm(a Alarm) (*ical.Component, error) {
	c := ical.NewComponent(ical.CompAlarm)
	for name, props := range a.extra {
		c.Props[name] = append([]ical.Prop(nil), props...)
	}
	action := a.Action
	if action == "" {
		action = ActionDisplay
	}
	p := ical.NewProp(ical.PropAction)
	p.Value = action
	c.Props.Set(p)

	desc := a.Description
	if desc == "" && action != ActionAudio {
		desc = "Reminder" // DESCRIPTION is required for DISPLAY and EMAIL
	}
	if desc != "" {
		c.Props.SetText(ical.PropDescription, desc)
	}
	if a.Summary != "" {
		c.Props.SetText(ical.PropSummary, a.Summary)
	}

	trig := ical.NewProp(ical.PropTrigger)
	if !a.TriggerAt.IsZero() {
		trig.Params.Set(ical.ParamValue, string(ical.ValueDateTime))
		trig.Value = a.TriggerAt.UTC().Format("20060102T150405Z")
	} else {
		trig.Value = formatDuration(a.Trigger)
		if a.Related == RelatedEnd {
			trig.Params.Set(ical.ParamRelated, RelatedEnd)
		}
	}
	c.Props.Set(trig)

	if a.Repeat > 0 {
		if a.Interval <= 0 {
			return nil, errors.New("VALARM REPEAT needs a positive interval")
		}
		r := ical.NewProp(ical.PropRepeat)
		r.Value = strconv.Itoa(a.Repeat)
		c.Props.Set(r)
		d := ical.NewProp(ical.PropDuration)
		d.Value = formatDuration(a.Interval)
		c.Props.Set(d)
	}
	return c, nil
}

// ---- value helpers ----

type timeKind int

const (
	kindUTC timeKind = iota
	kindZoned
	kindFloating
	kindDate
)

type parsedTime struct {
	t    time.Time
	tzid string
}

// parseTime parses one DATE or DATE-TIME value.
func parseTime(value string, params ical.Params, tzHints map[string]string) (parsedTime, timeKind, error) {
	value = strings.TrimSpace(value)
	if strings.EqualFold(params.Get(ical.ParamValue), "DATE") || len(value) == 8 {
		t, err := time.ParseInLocation("20060102", value, time.UTC)
		return parsedTime{t: t}, kindDate, err
	}
	if strings.HasSuffix(value, "Z") || strings.HasSuffix(value, "z") {
		t, err := time.ParseInLocation("20060102T150405", value[:len(value)-1], time.UTC)
		return parsedTime{t: t}, kindUTC, err
	}
	if tzid := params.Get(ical.ParamTimezoneID); tzid != "" {
		if loc := ResolveTZID(tzid, tzHints[tzid]); loc != nil {
			t, err := time.ParseInLocation("20060102T150405", value, loc)
			return parsedTime{t: t, tzid: tzid}, kindZoned, err
		}
		// Unknown zone: treat as floating rather than reject the event.
	}
	t, err := time.ParseInLocation("20060102T150405", value, time.UTC)
	return parsedTime{t: t}, kindFloating, err
}

// windowsZones maps the Windows zone names Outlook/Exchange emit as TZID.
var windowsZones = map[string]string{
	"Eastern Standard Time":        "America/New_York",
	"Central Standard Time":        "America/Chicago",
	"Mountain Standard Time":       "America/Denver",
	"US Mountain Standard Time":    "America/Phoenix",
	"Pacific Standard Time":        "America/Los_Angeles",
	"Alaskan Standard Time":        "America/Anchorage",
	"Hawaiian Standard Time":       "Pacific/Honolulu",
	"Atlantic Standard Time":       "America/Halifax",
	"GMT Standard Time":            "Europe/London",
	"Greenwich Standard Time":      "Atlantic/Reykjavik",
	"W. Europe Standard Time":      "Europe/Berlin",
	"Romance Standard Time":        "Europe/Paris",
	"Central Europe Standard Time": "Europe/Budapest",
	"E. Europe Standard Time":      "Europe/Chisinau",
	"AUS Eastern Standard Time":    "Australia/Sydney",
	"Tokyo Standard Time":          "Asia/Tokyo",
	"India Standard Time":          "Asia/Kolkata",
	"China Standard Time":          "Asia/Shanghai",
	"UTC":                          "UTC",
}

// ResolveTZID maps a TZID to a Go location. It accepts IANA names, vendor
// prefixed names ("/mozilla.org/20050126_1/America/New_York"), common Windows
// names and a VTIMEZONE's X-LIC-LOCATION hint. It returns nil if nothing
// matches.
func ResolveTZID(tzid, hint string) *time.Location {
	tzid = strings.Trim(strings.TrimSpace(tzid), `"`)
	try := func(name string) *time.Location {
		if name == "" {
			return nil
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
		return nil
	}
	if loc := try(tzid); loc != nil {
		return loc
	}
	if loc := try(windowsZones[tzid]); loc != nil {
		return loc
	}
	if loc := try(hint); loc != nil {
		return loc
	}
	parts := strings.Split(strings.Trim(tzid, "/"), "/")
	for n := 3; n >= 1; n-- {
		if len(parts) > n {
			if loc := try(strings.Join(parts[len(parts)-n:], "/")); loc != nil {
				return loc
			}
		}
	}
	return nil
}

func parseDuration(s string) (time.Duration, error) {
	p := ical.NewProp(ical.PropDuration)
	p.Value = strings.TrimSpace(s)
	return p.Duration()
}

// formatDuration writes an RFC 5545 duration using the largest exact units,
// e.g. -PT15M, P1D, PT1H30M.
func formatDuration(d time.Duration) string {
	var b strings.Builder
	if d < 0 {
		b.WriteByte('-')
		d = -d
	}
	b.WriteByte('P')
	if d == 0 {
		b.WriteString("T0S")
		return b.String()
	}
	day := 24 * time.Hour
	if d%(7*day) == 0 {
		fmt.Fprintf(&b, "%dW", d/(7*day))
		return b.String()
	}
	if days := d / day; days > 0 {
		fmt.Fprintf(&b, "%dD", days)
		d -= days * day
	}
	if d > 0 {
		b.WriteByte('T')
		if h := d / time.Hour; h > 0 {
			fmt.Fprintf(&b, "%dH", h)
			d -= h * time.Hour
		}
		if m := d / time.Minute; m > 0 {
			fmt.Fprintf(&b, "%dM", m)
			d -= m * time.Minute
		}
		if s := d / time.Second; s > 0 {
			fmt.Fprintf(&b, "%dS", s)
		}
	}
	return b.String()
}

func cloneParams(p ical.Params) ical.Params {
	out := make(ical.Params, len(p))
	for k, v := range p {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// ---- VTIMEZONE generation ----

// vtimezone builds a VTIMEZONE for loc from the Go tz database, describing
// the rules in effect in the given year as yearly RRULEs. That matches how
// clients write zones and is exact for zones whose rules are stable.
func vtimezone(tzid string, loc *time.Location, year int) *ical.Component {
	tz := ical.NewComponent(ical.CompTimezone)
	tz.Props.SetText(ical.PropTimezoneID, tzid)

	jan := time.Date(year, 1, 1, 0, 0, 0, 0, loc)
	transitions := findTransitions(jan, jan.AddDate(1, 0, 0))
	if len(transitions) == 0 {
		name, off := jan.Zone()
		tz.Children = append(tz.Children, tzSub(ical.CompTimezoneStandard, name,
			time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), off, off, ""))
		return tz
	}
	for _, tr := range transitions {
		before := tr.Add(-time.Second)
		_, fromOff := before.Zone()
		name, toOff := tr.Zone()
		kind := ical.CompTimezoneStandard
		if toOff > fromOff {
			kind = ical.CompTimezoneDaylight
		}
		// DTSTART is the local time, in the old offset, when the change happens.
		local := tr.In(time.FixedZone("", fromOff))
		wall := time.Date(1970, local.Month(), local.Day(), local.Hour(), local.Minute(), 0, 0, time.UTC)
		// Move to the first year >= 1970 whose same weekday-of-month matches.
		nth := (local.Day()-1)/7 + 1
		if local.Day()+7 > daysIn(local.Month(), local.Year()) {
			nth = -1
		}
		wd := [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}[local.Weekday()]
		rule := fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=%d%s", int(local.Month()), nth, wd)
		wall = firstMatch(wall, nth, local.Weekday())
		tz.Children = append(tz.Children, tzSub(kind, name, wall, fromOff, toOff, rule))
	}
	return tz
}

func tzSub(kind, name string, start time.Time, fromOff, toOff int, rule string) *ical.Component {
	c := ical.NewComponent(kind)
	p := ical.NewProp(ical.PropDateTimeStart)
	p.Value = start.Format("20060102T150405")
	c.Props.Set(p)
	from := ical.NewProp(ical.PropTimezoneOffsetFrom)
	from.Value = formatOffset(fromOff)
	c.Props.Set(from)
	to := ical.NewProp(ical.PropTimezoneOffsetTo)
	to.Value = formatOffset(toOff)
	c.Props.Set(to)
	if name != "" {
		c.Props.SetText(ical.PropTimezoneName, name)
	}
	if rule != "" {
		r := ical.NewProp(ical.PropRecurrenceRule)
		r.Value = rule
		c.Props.Set(r)
	}
	return c
}

// firstMatch returns the date in 1970 (same month, same wall time) that is the
// nth (or last, n=-1) given weekday of that month.
func firstMatch(t time.Time, n int, wd time.Weekday) time.Time {
	y, m := t.Year(), t.Month()
	if n > 0 {
		d := time.Date(y, m, 1, t.Hour(), t.Minute(), 0, 0, time.UTC)
		for d.Weekday() != wd {
			d = d.AddDate(0, 0, 1)
		}
		return d.AddDate(0, 0, 7*(n-1))
	}
	d := time.Date(y, m, daysIn(m, y), t.Hour(), t.Minute(), 0, 0, time.UTC)
	for d.Weekday() != wd {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

func daysIn(m time.Month, y int) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func formatOffset(sec int) string {
	sign := '+'
	if sec < 0 {
		sign = '-'
		sec = -sec
	}
	return fmt.Sprintf("%c%02d%02d", sign, sec/3600, (sec%3600)/60)
}

// findTransitions returns the instants in [from, to) where the UTC offset of
// from's location changes, to the second.
func findTransitions(from, to time.Time) []time.Time {
	var out []time.Time
	_, prev := from.Zone()
	for t := from; t.Before(to); {
		next := t.Add(24 * time.Hour)
		if _, off := next.Zone(); off != prev {
			lo, hi := t, next
			for hi.Sub(lo) > time.Second {
				mid := lo.Add(hi.Sub(lo) / 2)
				if _, o := mid.Zone(); o == prev {
					lo = mid
				} else {
					hi = mid
				}
			}
			out = append(out, hi.Truncate(time.Second))
			prev = off
		}
		t = next
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// WriteTo is a convenience that encodes o to w.
func (o *Object) WriteTo(w io.Writer) (int64, error) {
	b, err := o.Encode()
	if err != nil {
		return 0, err
	}
	n, err := w.Write(b)
	return int64(n), err
}
