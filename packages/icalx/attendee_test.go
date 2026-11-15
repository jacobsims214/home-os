package icalx

import (
	"strings"
	"testing"
	"time"
)

func TestAttendeesRoundTrip(t *testing.T) {
	const src = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:a1\r\n" +
		"DTSTAMP:20261101T000000Z\r\nDTSTART:20261117T223000Z\r\nDTEND:20261117T233000Z\r\nSUMMARY:Soccer\r\n" +
		"ORGANIZER:mailto:mom@example.com\r\n" +
		"ATTENDEE;CN=Coach;PARTSTAT=TENTATIVE;RSVP=TRUE:mailto:coach@example.com\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	obj, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	m := obj.Master
	got := m.Attendees()
	if len(got) != 1 || got[0].CommonName != "Coach" || got[0].Address != "mailto:coach@example.com" || got[0].PersonID != "" {
		t.Fatalf("attendees = %+v", got)
	}
	got = append(got, Attendee{Address: "urn:uuid:5e0c", CommonName: "Nora, Jr.", PersonID: "5e0c"})
	m.SetAttendees(got)

	// A clone is independent of the original.
	c := m.Clone()
	c.SetAttendees(nil)
	c.ExDates = append(c.ExDates, time.Now())
	if len(m.Attendees()) != 2 || len(m.ExDates) != 0 {
		t.Fatalf("clone shares state: %+v", m.Attendees())
	}
	if len(c.Attendees()) != 0 {
		t.Fatalf("clone attendees not cleared")
	}

	raw, err := obj.Encode()
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"ORGANIZER:mailto:mom@example.com", "RSVP=TRUE", "PARTSTAT=TENTATIVE", "X-HOMEOS-PERSON-ID=5e0c", `CN="Nora, Jr."`} {
		if !strings.Contains(strings.ReplaceAll(s, "\r\n ", ""), want) {
			t.Errorf("encoded raw lacks %q:\n%s", want, s)
		}
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	as := back.Master.Attendees()
	if len(as) != 2 || as[1].PersonID != "5e0c" || as[1].CommonName != "Nora, Jr." || as[1].Address != "urn:uuid:5e0c" {
		t.Fatalf("after round trip: %+v", as)
	}
	back.Master.SetAttendees(nil)
	raw, _ = back.Encode()
	if strings.Contains(string(raw), "ATTENDEE") {
		t.Errorf("SetAttendees(nil) kept attendees:\n%s", raw)
	}
}
