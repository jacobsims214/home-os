package calendar

// The agenda's summary shaping (#4810), as a pure function: it is where the
// default answer's size and its honesty are decided, so it is tested without a
// database.

import (
	"testing"
	"time"
)

func agendaShapeLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	return loc
}

// day builds a timed event on 2026-11-<day> at hour:00 New York.
func day(loc *time.Location, d, hour int, title string) Event {
	start := time.Date(2026, 11, d, hour, 0, 0, 0, loc)
	return Event{
		ID: "id-" + title, Title: title, Start: start.Format(time.RFC3339),
		End: start.Add(time.Hour).Format(time.RFC3339), AllDay: false,
		LocalDate: start.Format(dateLayout), Weekday: start.Weekday().String(),
		CalendarID: "cal", CalendarName: "Family", EventType: "event",
	}
}

func allDay(loc *time.Location, from, to int, title string) Event {
	first := time.Date(2026, 11, from, 0, 0, 0, 0, loc)
	excl := time.Date(2026, 11, to+1, 0, 0, 0, 0, loc)
	return Event{
		ID: "id-" + title, Title: title, Start: first.Format(time.RFC3339),
		End: excl.Format(time.RFC3339), AllDay: true,
		LocalDate: first.Format(dateLayout), Weekday: first.Weekday().String(),
		CalendarID: "cal", CalendarName: "Family", EventType: "event",
	}
}

// series returns n daily occurrences of one series, from day d.
func series(loc *time.Location, d, n int, title string) []Event {
	out := make([]Event, 0, n)
	for i := 0; i < n; i++ {
		ev := day(loc, d+i, 17, title)
		ev.ID = "series-" + title
		ev.Recurring = true
		ev.RRule = "FREQ=DAILY"
		ev.Repeat = &Repeat{Freq: "daily"}
		out = append(out, ev)
	}
	return out
}

func TestShapeAgendaEventsCollapsesSeries(t *testing.T) {
	loc := agendaShapeLoc(t)
	events := append(series(loc, 16, 7, "Medication"), day(loc, 17, 9, "Dentist"))

	out, skipped := shapeAgendaEvents(events, agendaDetailSummary, defaultAgendaPerDay, loc)
	if skipped != 0 {
		t.Errorf("skipped %d, want 0: seven occurrences of one series and one one-off fit", skipped)
	}
	if len(out) != 2 {
		t.Fatalf("shaped %d entries, want 2 (one series, one one-off): %+v", len(out), out)
	}
	s := out[0]
	if s.Title != "Medication" || s.Occurrences != 7 || len(s.Dates) != 7 {
		t.Errorf("series entry = %+v, want 7 occurrences listed", s)
	}
	if s.Dates[0] != "2026-11-16" || s.Dates[6] != "2026-11-22" {
		t.Errorf("series dates = %v", s.Dates)
	}
	if s.Repeat == "" || s.LocalDate != "" || s.Weekday != "" {
		t.Errorf("series entry carries %q/%q, or no repeat phrase: %q", s.LocalDate, s.Weekday, s.Repeat)
	}
	if s.At != "17:00" || s.Start != "" || s.Description != nil {
		t.Errorf("summary series entry should carry the clock time and no full-detail fields: %+v", s)
	}
	one := out[1]
	if one.Title != "Dentist" || one.LocalDate != "2026-11-17" || one.Weekday != "Tuesday" || one.Dates != nil {
		t.Errorf("one-off entry = %+v", one)
	}
}

func TestShapeAgendaEventsCapsOneOffsPerDay(t *testing.T) {
	loc := agendaShapeLoc(t)
	// Six one-off events on one day, against a cap of 3.
	var events []Event
	for i, title := range []string{"A", "B", "C", "D", "E", "F"} {
		events = append(events, day(loc, 18, 8+i, title))
	}
	// A daily series on the same day: series are one entry each, so the cap
	// must not touch them, and the day's count must not either.
	events = append(events, series(loc, 18, 1, "Trash")...)

	out, skipped := shapeAgendaEvents(events, agendaDetailSummary, 3, loc)
	if skipped != 3 {
		t.Errorf("skipped %d, want the 3 one-off events over the cap", skipped)
	}
	ones, seriesEntries := 0, 0
	for _, e := range out {
		if e.Dates != nil {
			seriesEntries++
			continue
		}
		ones++
	}
	if ones != 3 || seriesEntries != 1 {
		t.Errorf("shaped %d one-offs and %d series, want 3 and 1: %+v", ones, seriesEntries, out)
	}
	// Every entry that survives is still complete: a capped day never makes a
	// series look like it skips a day.
	for _, e := range out {
		if e.Dates != nil && e.Occurrences != len(e.Dates) {
			t.Errorf("%q: occurrences %d, dates %d", e.Title, e.Occurrences, len(e.Dates))
		}
	}
}

func TestShapeAgendaEventsFullKeepsEveryOccurrence(t *testing.T) {
	loc := agendaShapeLoc(t)
	events := append(series(loc, 16, 7, "Medication"), day(loc, 17, 9, "Dentist"))

	out, skipped := shapeAgendaEvents(events, agendaDetailFull, 1, loc)
	if skipped != 0 || len(out) != 8 {
		t.Fatalf("detail=full shaped %d entries (skipped %d), want all 8 occurrences", len(out), skipped)
	}
	for _, e := range out {
		if e.Start == "" || e.End == "" || e.CalendarID != "cal" || e.CalendarName != "Family" ||
			e.EventType != "event" || e.Description == nil {
			t.Errorf("detail=full entry lost fields: %+v", e)
		}
		if e.Dates != nil {
			t.Errorf("detail=full entry carries the summary's dates: %+v", e)
		}
	}
	// Recurring occurrences carry recurrence_id, which is what an edit needs;
	// a one-off has none. fullAgendaEvent passes Event.RecurrenceID through.
	if out[0].RecurrenceID != events[0].RecurrenceID {
		t.Errorf("recurrence_id = %q, want %q", out[0].RecurrenceID, events[0].RecurrenceID)
	}
}

func TestSummaryAgendaEventTimes(t *testing.T) {
	loc := agendaShapeLoc(t)
	tests := []struct {
		name              string
		ev                Event
		wantAt, wantUntil string
		wantLastDate      string
	}{
		{
			name: "a one-hour timed event keeps only its start",
			ev:   day(loc, 18, 10, "Dentist"), wantAt: "10:00",
		},
		{
			name: "a three-hour event says when it ends",
			ev: func() Event {
				s := time.Date(2026, 11, 18, 9, 0, 0, 0, loc)
				return Event{ID: "x", Title: "Work", Start: s.Format(time.RFC3339),
					End:       s.Add(3 * time.Hour).Format(time.RFC3339),
					LocalDate: s.Format(dateLayout), Weekday: s.Weekday().String()}
			}(),
			wantAt: "09:00", wantUntil: "12:00",
		},
		{
			name: "an event past midnight says when it ends",
			ev: func() Event {
				s := time.Date(2026, 11, 18, 23, 0, 0, 0, loc)
				return Event{ID: "y", Title: "Late call", Start: s.Format(time.RFC3339),
					End:       s.Add(2 * time.Hour).Format(time.RFC3339),
					LocalDate: s.Format(dateLayout), Weekday: s.Weekday().String()}
			}(),
			wantAt: "23:00", wantUntil: "01:00",
		},
		{
			name: "an all-day event has a day, no clock time",
			ev:   allDay(loc, 18, 18, "Holiday"), wantLastDate: "",
		},
		{
			name: "a three-day all-day event names its last day",
			ev:   allDay(loc, 18, 20, "Field trip"), wantLastDate: "2026-11-20",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := summaryAgendaEvent(tt.ev, loc)
			if e.At != tt.wantAt || e.Until != tt.wantUntil || e.LastDate != tt.wantLastDate {
				t.Errorf("at=%q until=%q last_date=%q, want %q/%q/%q",
					e.At, e.Until, e.LastDate, tt.wantAt, tt.wantUntil, tt.wantLastDate)
			}
			if tt.ev.AllDay && !e.AllDay {
				t.Error("all-day flag lost")
			}
		})
	}
}
