package calendar

// setLocalDay (#4550): the day a UI prints comes from the API, in the
// household's zone, and an all-day event keeps its own date.

import (
	"testing"
	"time"
)

func TestSetLocalDay(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	auckland, err := time.LoadLocation("Pacific/Auckland")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	must := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return v
	}
	tests := []struct {
		name    string
		start   time.Time
		allDay  bool
		loc     *time.Location
		wantDay string
		wantWd  string
	}{
		{
			// 19:30 New York on a Tuesday; 00:30Z on the Wednesday.
			name:  "a late evening event is dated by the household's day",
			start: must("2026-11-18T00:30:00Z"), loc: ny,
			wantDay: "2026-11-17", wantWd: "Tuesday",
		},
		{
			// The same instant in Auckland is already Wednesday afternoon.
			name:  "the zone decides, not the instant's UTC date",
			start: must("2026-11-18T00:30:00Z"), loc: auckland,
			wantDay: "2026-11-18", wantWd: "Wednesday",
		},
		{
			// An all-day event is stored at midnight UTC of its date. In New
			// York that instant is the previous day at 19:00, so converting it
			// would move the event a day earlier — the round-one bug.
			name:  "an all-day event keeps its own date west of UTC",
			start: must("2026-11-26T00:00:00Z"), allDay: true, loc: ny,
			wantDay: "2026-11-26", wantWd: "Thursday",
		},
		{
			name:  "an all-day event keeps its own date east of UTC",
			start: must("2026-11-26T00:00:00Z"), allDay: true, loc: auckland,
			wantDay: "2026-11-26", wantWd: "Thursday",
		},
		{
			name:  "no zone falls back to UTC rather than inventing a day",
			start: must("2026-11-17T12:00:00Z"), loc: nil,
			wantDay: "2026-11-17", wantWd: "Tuesday",
		},
		{
			name: "a zero start leaves both empty",
			loc:  ny, wantDay: "", wantWd: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := Event{Start: tt.start, AllDay: tt.allDay}
			setLocalDay(&ev, tt.loc)
			if ev.LocalDate != tt.wantDay || ev.Weekday != tt.wantWd {
				t.Errorf("setLocalDay = %q, %q; want %q, %q", ev.LocalDate, ev.Weekday, tt.wantDay, tt.wantWd)
			}
		})
	}
}

func TestSetLocalDaysFillsEveryEvent(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	start, _ := time.Parse(time.RFC3339, "2026-11-17T12:50:00Z")
	evs := []Event{{Start: start}, {Start: start.Add(24 * time.Hour)}, {AllDay: true, Start: start}}
	setLocalDays(evs, ny)
	for i, ev := range evs {
		if ev.LocalDate == "" || ev.Weekday == "" {
			t.Errorf("event %d left unset: %+v", i, ev)
		}
	}
}
