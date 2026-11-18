package icalx

import (
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

// ParamPersonID is the ATTENDEE parameter Home OS uses to tie an attendee to
// a household person (people.id). Clients keep unknown X- parameters, so the
// link survives a round trip through a phone.
const ParamPersonID = "X-HOMEOS-PERSON-ID"

// Attendee is one ATTENDEE property of an event.
type Attendee struct {
	// Address is the cal-address URI, e.g. "mailto:nora@example.com" or
	// "urn:uuid:<person id>" for someone without an email address.
	Address string
	// CommonName is the CN parameter (the display name).
	CommonName string
	// PersonID is the X-HOMEOS-PERSON-ID parameter, empty for attendees a
	// client added.
	PersonID string

	prop *ical.Prop // the source property, kept so other parameters survive
}

// Attendees returns the event's ATTENDEE properties in source order.
func (e *Event) Attendees() []Attendee {
	props := e.extra[ical.PropAttendee]
	if len(props) == 0 {
		return nil
	}
	out := make([]Attendee, 0, len(props))
	for i := range props {
		p := props[i]
		out = append(out, Attendee{
			Address:    strings.TrimSpace(p.Value),
			CommonName: p.Params.Get(ical.ParamCommonName),
			PersonID:   p.Params.Get(ParamPersonID),
			prop:       &p,
		})
	}
	return out
}

// SetAttendees replaces the event's ATTENDEE properties. An Attendee that came
// from Attendees keeps its source parameters (PARTSTAT, ROLE, RSVP, ...) with
// CN and X-HOMEOS-PERSON-ID updated; a new one is written as an accepted
// individual. An empty list removes every attendee.
func (e *Event) SetAttendees(as []Attendee) {
	if e.extra == nil {
		e.extra = ical.Props{}
	}
	if len(as) == 0 {
		delete(e.extra, ical.PropAttendee)
		return
	}
	props := make([]ical.Prop, 0, len(as))
	for _, a := range as {
		var p ical.Prop
		if a.prop != nil {
			p = *a.prop
			p.Params = cloneParams(a.prop.Params)
		} else {
			p = *ical.NewProp(ical.PropAttendee)
			p.Params.Set(ical.ParamCalendarUserType, "INDIVIDUAL")
			p.Params.Set(ical.ParamParticipationStatus, "ACCEPTED")
		}
		p.Value = a.Address
		setParam(p.Params, ical.ParamCommonName, a.CommonName)
		setParam(p.Params, ParamPersonID, a.PersonID)
		props = append(props, p)
	}
	e.extra[ical.PropAttendee] = props
}

func setParam(ps ical.Params, name, value string) {
	if value == "" {
		ps.Del(name)
		return
	}
	ps.Set(name, value)
}

// Clone returns a deep copy of e, including the properties this package does
// not model (ATTENDEE, ORGANIZER, X-*), so a caller can derive a new series or
// an override from a master without the two sharing state.
func (e *Event) Clone() *Event {
	c := *e
	c.RDates = append([]time.Time(nil), e.RDates...)
	c.ExDates = append([]time.Time(nil), e.ExDates...)
	c.Alarms = make([]Alarm, len(e.Alarms))
	for i, a := range e.Alarms {
		a.extra = cloneProps(a.extra)
		c.Alarms[i] = a
	}
	if len(e.Alarms) == 0 {
		c.Alarms = nil
	}
	c.extra = cloneProps(e.extra)
	return &c
}

func cloneProps(ps ical.Props) ical.Props {
	if ps == nil {
		return nil
	}
	out := make(ical.Props, len(ps))
	for name, list := range ps {
		cp := make([]ical.Prop, len(list))
		for i, p := range list {
			p.Params = cloneParams(p.Params)
			cp[i] = p
		}
		out[name] = cp
	}
	return out
}
