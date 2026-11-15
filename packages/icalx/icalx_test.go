package icalx

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return loc
}

// cal wraps VEVENT lines in a VCALENDAR with CRLF line endings.
func cal(lines ...string) []byte {
	all := append([]string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//test//EN"}, lines...)
	all = append(all, "END:VCALENDAR")
	return []byte(strings.Join(all, "\r\n") + "\r\n")
}

func vevent(props ...string) []string {
	out := []string{"BEGIN:VEVENT", "DTSTAMP:20260101T000000Z"}
	out = append(out, props...)
	return append(out, "END:VEVENT")
}

func fmtStarts(occs []Occurrence) []string {
	out := make([]string, len(occs))
	for i, o := range occs {
		out[i] = o.Start.Format("2006-01-02T15:04 -0700")
	}
	return out
}

func equalStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d\n got: %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("item %d: got %q, want %q\n got: %q\nwant: %q", i, got[i], want[i], got, want)
		}
	}
}

func TestExpand(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	london := mustLoc(t, "Europe/London")

	tests := []struct {
		name     string
		data     []byte
		from, to time.Time
		loc      *time.Location
		want     []string // occurrence starts in loc
		wantEnds []string // optional: occurrence ends in loc
	}{
		{
			name: "weekly Tue/Thu until, across US DST end (fall back)",
			data: cal(vevent(
				"UID:tt@test",
				"SUMMARY:Practice",
				"DTSTART;TZID=America/New_York:20261020T090000",
				"DTEND;TZID=America/New_York:20261020T100000",
				"RRULE:FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261112T140000Z",
			)...),
			from: time.Date(2026, 10, 1, 0, 0, 0, 0, ny),
			to:   time.Date(2026, 12, 1, 0, 0, 0, 0, ny),
			loc:  ny,
			want: []string{
				"2026-10-20T09:00 -0400", "2026-10-22T09:00 -0400",
				"2026-10-27T09:00 -0400", "2026-10-29T09:00 -0400",
				// DST ends Sun 2026-11-01: wall clock stays 09:00, offset -0500.
				"2026-11-03T09:00 -0500", "2026-11-05T09:00 -0500",
				"2026-11-10T09:00 -0500", "2026-11-12T09:00 -0500", // UNTIL is inclusive
			},
			wantEnds: []string{
				"2026-10-20T10:00 -0400", "2026-10-22T10:00 -0400",
				"2026-10-27T10:00 -0400", "2026-10-29T10:00 -0400",
				"2026-11-03T10:00 -0500", "2026-11-05T10:00 -0500",
				"2026-11-10T10:00 -0500", "2026-11-12T10:00 -0500",
			},
		},
		{
			name: "weekly Tue/Thu until, across US DST start (spring forward), viewed from London",
			data: cal(vevent(
				"UID:tt2@test",
				"DTSTART;TZID=America/New_York:20270302T183000",
				"DURATION:PT1H",
				"RRULE:FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20270316T235959Z",
			)...),
			from: time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC),
			to:   time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC),
			loc:  london,
			// NY moves to EDT on 2027-03-14, London stays GMT until 03-28,
			// so the London view shifts from 23:30 to 22:30.
			want: []string{
				"2027-03-02T23:30 +0000", "2027-03-04T23:30 +0000",
				"2027-03-09T23:30 +0000", "2027-03-11T23:30 +0000",
				"2027-03-16T22:30 +0000",
			},
		},
		{
			name: "daily COUNT across UK DST start",
			data: cal(vevent(
				"UID:count@test",
				"DTSTART;TZID=Europe/London:20270326T080000",
				"DTEND;TZID=Europe/London:20270326T083000",
				"RRULE:FREQ=DAILY;COUNT=4",
			)...),
			from: time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC),
			to:   time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC),
			loc:  london,
			want: []string{
				"2027-03-26T08:00 +0000", "2027-03-27T08:00 +0000",
				"2027-03-28T08:00 +0100", "2027-03-29T08:00 +0100",
			},
		},
		{
			name: "COUNT counts instances before the window",
			data: cal(vevent(
				"UID:count2@test",
				"DTSTART:20260101T120000Z",
				"RRULE:FREQ=WEEKLY;COUNT=3",
			)...),
			from: time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
			to:   time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			loc:  time.UTC,
			want: []string{"2026-01-15T12:00 +0000"},
		},
		{
			name: "EXDATE as TZID, UTC and comma list",
			data: cal(vevent(
				"UID:ex@test",
				"DTSTART;TZID=America/New_York:20261102T170000",
				"DTEND;TZID=America/New_York:20261102T180000",
				"RRULE:FREQ=DAILY;COUNT=7",
				"EXDATE;TZID=America/New_York:20261103T170000",
				"EXDATE:20261105T220000Z", // 17:00 EST written in UTC
				"EXDATE;TZID=America/New_York:20261106T170000,20261107T170000",
			)...),
			from: time.Date(2026, 11, 1, 0, 0, 0, 0, ny),
			to:   time.Date(2026, 11, 30, 0, 0, 0, 0, ny),
			loc:  ny,
			want: []string{
				"2026-11-02T17:00 -0500", "2026-11-04T17:00 -0500",
				"2026-11-08T17:00 -0500",
			},
		},
		{
			name: "overrides: moved, moved into window, cancelled",
			data: cal(
				append(append(append(append(vevent(
					"UID:ov@test",
					"SUMMARY:Standup",
					"DTSTART;TZID=America/New_York:20261116T090000",
					"DTEND;TZID=America/New_York:20261116T091500",
					"RRULE:FREQ=WEEKLY;BYDAY=MO,WE",
				),
					// Wed 18th moved to 11:00.
					vevent(
						"UID:ov@test",
						"RECURRENCE-ID;TZID=America/New_York:20261118T090000",
						"SUMMARY:Standup (late)",
						"DTSTART;TZID=America/New_York:20261118T110000",
						"DTEND;TZID=America/New_York:20261118T111500",
					)...),
					// Mon 23rd cancelled, RECURRENCE-ID written in UTC.
					vevent(
						"UID:ov@test",
						"RECURRENCE-ID:20261123T140000Z",
						"STATUS:CANCELLED",
						"DTSTART;TZID=America/New_York:20261123T090000",
						"DTEND;TZID=America/New_York:20261123T091500",
					)...),
					// Mon 30th (outside the window) moved into it, to Fri 27th.
					vevent(
						"UID:ov@test",
						"RECURRENCE-ID;TZID=America/New_York:20261130T090000",
						"DTSTART;TZID=America/New_York:20261127T090000",
						"DTEND;TZID=America/New_York:20261127T091500",
					)...),
					// Wed 25th moved out of the window, to Dec 2nd.
					vevent(
						"UID:ov@test",
						"RECURRENCE-ID;TZID=America/New_York:20261125T090000",
						"DTSTART;TZID=America/New_York:20261202T150000",
						"DTEND;TZID=America/New_York:20261202T151500",
					)...)...,
			),
			from: time.Date(2026, 11, 16, 0, 0, 0, 0, ny),
			to:   time.Date(2026, 11, 28, 0, 0, 0, 0, ny),
			loc:  ny,
			want: []string{
				"2026-11-16T09:00 -0500",
				"2026-11-18T11:00 -0500", // override
				"2026-11-27T09:00 -0500", // override moved in
			},
		},
		{
			name: "all-day weekly across DST keeps local midnights",
			data: cal(vevent(
				"UID:allday@test",
				"DTSTART;VALUE=DATE:20261025",
				"DTEND;VALUE=DATE:20261026",
				"RRULE:FREQ=WEEKLY;UNTIL=20261108",
			)...),
			from: time.Date(2026, 10, 1, 0, 0, 0, 0, ny),
			to:   time.Date(2026, 12, 1, 0, 0, 0, 0, ny),
			loc:  ny,
			want: []string{
				"2026-10-25T00:00 -0400",
				"2026-11-01T00:00 -0400", // the 25-hour DST day
				"2026-11-08T00:00 -0500",
			},
			wantEnds: []string{
				"2026-10-26T00:00 -0400",
				"2026-11-02T00:00 -0500",
				"2026-11-09T00:00 -0500",
			},
		},
		{
			name: "all-day with UTC UNTIL is read in the viewer's zone",
			data: cal(vevent(
				"UID:alldayutc@test",
				"DTSTART;VALUE=DATE:20261101",
				"RRULE:FREQ=DAILY;UNTIL=20261103T045959Z", // 2026-11-02 23:59 in New York
			)...),
			from: time.Date(2026, 10, 1, 0, 0, 0, 0, ny),
			to:   time.Date(2026, 12, 1, 0, 0, 0, 0, ny),
			loc:  ny,
			want: []string{"2026-11-01T00:00 -0400", "2026-11-02T00:00 -0500"},
		},
		{
			name: "all-day multi-day, window starts mid-event",
			data: cal(vevent(
				"UID:trip@test",
				"DTSTART;VALUE=DATE:20261224",
				"DTEND;VALUE=DATE:20261228",
			)...),
			from:     time.Date(2026, 12, 26, 12, 0, 0, 0, ny),
			to:       time.Date(2026, 12, 27, 0, 0, 0, 0, ny),
			loc:      ny,
			want:     []string{"2026-12-24T00:00 -0500"},
			wantEnds: []string{"2026-12-28T00:00 -0500"},
		},
		{
			name: "all-day EXDATE",
			data: cal(vevent(
				"UID:bins@test",
				"SUMMARY:Bins out",
				"DTSTART;VALUE=DATE:20261102",
				"RRULE:FREQ=WEEKLY;COUNT=4",
				"EXDATE;VALUE=DATE:20261109",
			)...),
			from: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
			to:   time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
			loc:  time.UTC,
			want: []string{
				"2026-11-02T00:00 +0000", "2026-11-16T00:00 +0000",
				"2026-11-23T00:00 +0000",
			},
		},
		{
			name: "floating time follows the viewer's zone",
			data: cal(vevent(
				"UID:float@test",
				"DTSTART:20261110T070000",
				"RRULE:FREQ=DAILY;COUNT=2",
			)...),
			from: time.Date(2026, 11, 1, 0, 0, 0, 0, london),
			to:   time.Date(2026, 12, 1, 0, 0, 0, 0, london),
			loc:  london,
			want: []string{"2026-11-10T07:00 +0000", "2026-11-11T07:00 +0000"},
		},
		{
			name: "non-recurring event outside window",
			data: cal(vevent(
				"UID:one@test",
				"DTSTART:20261110T070000Z",
				"DTEND:20261110T080000Z",
			)...),
			from: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC), // end is exclusive
			to:   time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC),
			loc:  time.UTC,
			want: nil,
		},
		{
			name: "RDATE adds an instance",
			data: cal(vevent(
				"UID:rdate@test",
				"DTSTART;TZID=Europe/London:20261201T100000",
				"RRULE:FREQ=MONTHLY;COUNT=2",
				"RDATE;TZID=Europe/London:20261215T100000",
			)...),
			from: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
			to:   time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC),
			loc:  london,
			want: []string{
				"2026-12-01T10:00 +0000", "2026-12-15T10:00 +0000",
				"2027-01-01T10:00 +0000",
			},
		},
		{
			name: "vendor-prefixed TZID resolves",
			data: cal(vevent(
				"UID:moz@test",
				"DTSTART;TZID=/mozilla.org/20050126_1/America/New_York:20261110T090000",
			)...),
			from: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
			to:   time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC),
			loc:  time.UTC,
			want: []string{"2026-11-10T14:00 +0000"},
		},
		{
			name: "Windows TZID resolves",
			data: cal(vevent(
				"UID:win@test",
				"DTSTART;TZID=Pacific Standard Time:20260710T090000",
			)...),
			from: time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
			to:   time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
			loc:  time.UTC,
			want: []string{"2026-07-10T16:00 +0000"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj, err := Parse(tc.data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			occs, err := obj.Expand(tc.from, tc.to, tc.loc)
			if err != nil {
				t.Fatalf("Expand: %v", err)
			}
			equalStrings(t, fmtStarts(occs), tc.want)
			if tc.wantEnds != nil {
				ends := make([]string, len(occs))
				for i, o := range occs {
					ends[i] = o.End.Format("2006-01-02T15:04 -0700")
				}
				equalStrings(t, ends, tc.wantEnds)
			}
		})
	}
}

func TestExpandOccurrenceFields(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	obj, err := Parse(cal(append(vevent(
		"UID:f@test",
		"SUMMARY:Master",
		"DTSTART;TZID=America/New_York:20261116T090000",
		"RRULE:FREQ=DAILY;COUNT=2",
	), vevent(
		"UID:f@test",
		"SUMMARY:Moved",
		"RECURRENCE-ID;TZID=America/New_York:20261117T090000",
		"DTSTART;TZID=America/New_York:20261117T100000",
	)...)...))
	if err != nil {
		t.Fatal(err)
	}
	occs, err := obj.Expand(time.Date(2026, 11, 16, 0, 0, 0, 0, ny), time.Date(2026, 11, 18, 0, 0, 0, 0, ny), ny)
	if err != nil {
		t.Fatal(err)
	}
	if len(occs) != 2 {
		t.Fatalf("got %d occurrences", len(occs))
	}
	if occs[0].Override || occs[0].Event.Summary != "Master" || !occs[0].RecurrenceID.Equal(time.Date(2026, 11, 16, 9, 0, 0, 0, ny)) {
		t.Errorf("first occurrence = %+v", occs[0])
	}
	if !occs[1].Override || occs[1].Event.Summary != "Moved" || !occs[1].RecurrenceID.Equal(time.Date(2026, 11, 17, 9, 0, 0, 0, ny)) {
		t.Errorf("second occurrence = %+v", occs[1])
	}
}

func TestExpandTooMany(t *testing.T) {
	obj, err := Parse(cal(vevent("UID:m@test", "DTSTART:20260101T000000Z", "RRULE:FREQ=MINUTELY")...))
	if err != nil {
		t.Fatal(err)
	}
	_, err = obj.Expand(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), nil)
	if !errors.Is(err, ErrTooManyOccurrences) {
		t.Fatalf("err = %v, want ErrTooManyOccurrences", err)
	}
}

func TestValarmRoundTrip(t *testing.T) {
	src := cal(append(append(vevent(
		"UID:alarm@test",
		"SUMMARY:Dentist",
		"DTSTART;TZID=America/New_York:20261110T090000",
		"DTEND;TZID=America/New_York:20261110T100000",
		"RRULE:FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261231T235959Z",
		"EXDATE;TZID=America/New_York:20261112T090000",
		"ATTENDEE;CN=Jane:mailto:jane@example.com",
		"X-HOMEOS-COLOR:teal",
	)[:10],
		"BEGIN:VALARM",
		"ACTION:DISPLAY",
		"DESCRIPTION:Leave now",
		"TRIGGER:-PT15M",
		"REPEAT:2",
		"DURATION:PT5M",
		"END:VALARM",
		"BEGIN:VALARM",
		"ACTION:EMAIL",
		"DESCRIPTION:Tomorrow",
		"SUMMARY:Dentist tomorrow",
		"TRIGGER;RELATED=END:-P1D",
		"ATTENDEE:mailto:me@example.com",
		"END:VALARM",
		"BEGIN:VALARM",
		"ACTION:AUDIO",
		"TRIGGER;VALUE=DATE-TIME:20261109T120000Z",
		"END:VALARM",
	), "END:VEVENT")...)

	first, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wantAlarms := []Alarm{
		{Action: ActionDisplay, Description: "Leave now", Trigger: -15 * time.Minute, Related: RelatedStart, Repeat: 2, Interval: 5 * time.Minute},
		{Action: ActionEmail, Description: "Tomorrow", Summary: "Dentist tomorrow", Trigger: -24 * time.Hour, Related: RelatedEnd},
		{Action: ActionAudio, Related: RelatedStart, TriggerAt: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)},
	}
	checkAlarms := func(label string, got []Alarm) {
		t.Helper()
		if len(got) != len(wantAlarms) {
			t.Fatalf("%s: got %d alarms, want %d", label, len(got), len(wantAlarms))
		}
		for i, w := range wantAlarms {
			g := got[i]
			if g.Action != w.Action || g.Description != w.Description || g.Summary != w.Summary ||
				g.Trigger != w.Trigger || g.Related != w.Related || !g.TriggerAt.Equal(w.TriggerAt) ||
				g.Repeat != w.Repeat || g.Interval != w.Interval {
				t.Errorf("%s alarm %d:\n got %+v\nwant %+v", label, i, g, w)
			}
		}
	}
	checkAlarms("parsed", first.Master.Alarms)

	out, err := first.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"TRIGGER:-PT15M", "TRIGGER;RELATED=END:-P1D", "TRIGGER;VALUE=DATE-TIME:20261109T120000Z",
		"REPEAT:2", "DURATION:PT5M", "ATTENDEE:mailto:me@example.com",
		"ATTENDEE;CN=Jane:mailto:jane@example.com", "X-HOMEOS-COLOR:teal",
		"RRULE:FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261231T235959Z",
		"EXDATE;TZID=America/New_York:20261112T090000",
		"BEGIN:VTIMEZONE", "TZID:America/New_York",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("encoded output lacks %q:\n%s", want, text)
		}
	}

	second, err := Parse(out)
	if err != nil {
		t.Fatalf("re-Parse: %v\n%s", err, text)
	}
	checkAlarms("round-tripped", second.Master.Alarms)
	m1, m2 := first.Master, second.Master
	if m1.UID != m2.UID || m1.Summary != m2.Summary || m1.RRule != m2.RRule ||
		!m1.Start.Equal(m2.Start) || !m1.End.Equal(m2.End) || m1.TZID != m2.TZID ||
		len(m2.ExDates) != 1 || !m2.ExDates[0].Equal(m1.ExDates[0]) {
		t.Errorf("master changed in round trip:\n got %+v\nwant %+v", m2, m1)
	}
}

func TestBuildNewEvent(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	master := &Event{
		UID:     "new@test",
		Summary: "Swim, lessons; level 2",
		Start:   time.Date(2026, 11, 3, 16, 0, 0, 0, ny),
		End:     time.Date(2026, 11, 3, 17, 0, 0, 0, ny),
		TZID:    "America/New_York",
		RRule:   "FREQ=WEEKLY;BYDAY=TU;COUNT=3",
		ExDates: []time.Time{time.Date(2026, 11, 10, 16, 0, 0, 0, ny)},
		Alarms:  []Alarm{{Trigger: -30 * time.Minute}},
	}
	override := &Event{
		UID:          "new@test",
		Summary:      "Swim (pool B)",
		Start:        time.Date(2026, 11, 17, 18, 0, 0, 0, ny),
		End:          time.Date(2026, 11, 17, 19, 0, 0, 0, ny),
		TZID:         "America/New_York",
		RecurrenceID: time.Date(2026, 11, 17, 16, 0, 0, 0, ny),
	}
	data, err := Build([]*Event{master, override})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"PRODID:" + ProdID,
		"BEGIN:STANDARD", "BEGIN:DAYLIGHT",
		"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU", "RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU",
		"TZOFFSETFROM:-0400", "TZOFFSETTO:-0500",
		"RECURRENCE-ID;TZID=America/New_York:20261117T160000",
		"ACTION:DISPLAY", "TRIGGER:-PT30M",
		`SUMMARY:Swim\, lessons\; level 2`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("built output lacks %q:\n%s", want, text)
		}
	}

	obj, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse(Build): %v", err)
	}
	if obj.Master.Summary != master.Summary {
		t.Errorf("summary = %q", obj.Master.Summary)
	}
	occs, err := obj.Expand(time.Date(2026, 11, 1, 0, 0, 0, 0, ny), time.Date(2026, 12, 1, 0, 0, 0, 0, ny), ny)
	if err != nil {
		t.Fatal(err)
	}
	equalStrings(t, fmtStarts(occs), []string{"2026-11-03T16:00 -0500", "2026-11-17T18:00 -0500"})
}

func TestBuildAllDayAndUTC(t *testing.T) {
	evs := []*Event{
		{UID: "a@test", Start: time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 12, 26, 0, 0, 0, 0, time.UTC), AllDay: true},
	}
	data, err := Build(evs)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "DTSTART;VALUE=DATE:20261225") || !strings.Contains(text, "DTEND;VALUE=DATE:20261226") {
		t.Errorf("all-day dates not written as DATE:\n%s", text)
	}
	if strings.Contains(text, "VTIMEZONE") {
		t.Errorf("all-day event should not need a VTIMEZONE:\n%s", text)
	}
	obj, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !obj.Master.AllDay || obj.Master.Duration() != 24*time.Hour {
		t.Errorf("parsed all-day = %+v", obj.Master)
	}
}

func TestAlarms(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	obj, err := Parse(cal(append(append(vevent(
		"UID:al@test",
		"DTSTART;TZID=America/New_York:20261029T090000",
		"DTEND;TZID=America/New_York:20261029T100000",
		"RRULE:FREQ=WEEKLY;BYDAY=TH;COUNT=3",
	)[:6],
		"BEGIN:VALARM", "ACTION:DISPLAY", "DESCRIPTION:x", "TRIGGER:-PT15M", "END:VALARM",
		"BEGIN:VALARM", "ACTION:DISPLAY", "DESCRIPTION:y", "TRIGGER;RELATED=END:PT0S", "END:VALARM",
		"BEGIN:VALARM", "ACTION:DISPLAY", "DESCRIPTION:z", "TRIGGER:-P1D", "REPEAT:1", "DURATION:PT1H", "END:VALARM",
	), "END:VEVENT")...))
	if err != nil {
		t.Fatal(err)
	}
	// Window: Wed 4 Nov 12:00 to Fri 6 Nov — just the second instance (Thu 5th,
	// after the DST change) and its day-before alarms.
	fs, err := obj.Alarms(time.Date(2026, 11, 4, 0, 0, 0, 0, ny), time.Date(2026, 11, 6, 0, 0, 0, 0, ny), ny)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(fs))
	for i, f := range fs {
		got[i] = f.At.Format("2006-01-02T15:04 -0700") + " " + f.Alarm.Description
	}
	equalStrings(t, got, []string{
		"2026-11-04T09:00 -0500 z",
		"2026-11-04T10:00 -0500 z",
		"2026-11-05T08:45 -0500 x",
		"2026-11-05T10:00 -0500 y",
	})
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"no event", cal(), ErrNoEvent},
		{"two UIDs", cal(append(vevent("UID:a", "DTSTART:20260101T000000Z"), vevent("UID:b", "DTSTART:20260101T000000Z")...)...), ErrMultipleUIDs},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.data); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := Parse(cal(vevent("UID:x", "SUMMARY:no start")...)); err == nil {
		t.Error("missing DTSTART should fail")
	}
	if _, err := Parse([]byte("not ical")); err == nil {
		t.Error("garbage should fail")
	}
	objs, err := ParseAll(cal(append(vevent("UID:a", "DTSTART:20260101T000000Z"), vevent("UID:b", "DTSTART:20260101T000000Z")...)...))
	if err != nil || len(objs) != 2 || objs[0].UID != "a" || objs[1].UID != "b" {
		t.Errorf("ParseAll = %v, %v", objs, err)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "PT0S"},
		{-15 * time.Minute, "-PT15M"},
		{24 * time.Hour, "P1D"},
		{7 * 24 * time.Hour, "P1W"},
		{-(26*time.Hour + 30*time.Minute + 5*time.Second), "-P1DT2H30M5S"},
	}
	for _, tc := range tests {
		if got := formatDuration(tc.in); got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
		if back, err := parseDuration(tc.want); err != nil || back != tc.in {
			t.Errorf("parseDuration(%q) = %v, %v", tc.want, back, err)
		}
	}
}
