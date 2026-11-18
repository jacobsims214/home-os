package calendar

// Range reads expand repeating events into occurrences (#4483).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// band is "Band" Tue/Thu 17:30-18:30 New York, 12 occurrences from Tue
// 2027-02-02 (Feb 2..25 and Mar 2..11). Dates are in 2027 so the conflict
// check, which starts at today, sees them.
var bandArgs = map[string]any{
	"title": "Band", "start": "2027-02-02T17:30:00-05:00", "end": "2027-02-02T18:30:00-05:00",
	"repeat": map[string]any{"freq": "weekly", "by_day": []string{"TU", "TH"}, "count": 12},
}

// occurrencesOf turns a detail=full agenda section back into Events, so the
// tests that check expansion can use occurrenceList unchanged.
func occurrencesOf(t *testing.T, entries []AgendaEvent) []Event {
	t.Helper()
	out := make([]Event, 0, len(entries))
	for _, e := range entries {
		ev := Event{ID: e.ID, Title: e.Title, Start: e.Start, End: e.End, AllDay: e.AllDay,
			LocalDate: e.LocalDate, Weekday: e.Weekday, RecurrenceID: e.RecurrenceID}
		if e.Description != nil {
			ev.Description = *e.Description
		}
		out = append(out, ev)
	}
	return out
}

// occurrenceList renders event_find/agenda items as "MM-DD HH:MM title".
func occurrenceList(t *testing.T, evs []Event, ny *time.Location) []string {
	t.Helper()
	var out []string
	for _, ev := range evs {
		s, err := time.Parse(time.RFC3339, ev.Start)
		if err != nil {
			t.Fatalf("start %q: %v", ev.Start, err)
		}
		out = append(out, s.In(ny).Format("01-02 15:04")+" "+ev.Title)
	}
	return out
}

func TestEventFindExpandsOccurrences(t *testing.T) {
	env := newRecurrenceEnv(t)
	find := bind(t, NewEventFindTool, env.pool)
	id := writtenEvent(t, env.mustCall(t, env.create, bandArgs)).Event.ID
	single := writtenEvent(t, env.mustCall(t, env.create, map[string]any{
		"title": "Dentist", "start": "2027-02-10T09:00:00-05:00", "end": "2027-02-10T10:00:00-05:00"})).Event.ID

	t.Run("a weekly event shows each occurrence in a month listing", func(t *testing.T) {
		evs := eventItems(t, env.mustCall(t, find, map[string]any{"from": "2027-02-01", "to": "2027-02-28"}))
		want := []string{"02-02 17:30 Band", "02-04 17:30 Band", "02-09 17:30 Band", "02-10 09:00 Dentist", "02-11 17:30 Band",
			"02-16 17:30 Band", "02-18 17:30 Band", "02-23 17:30 Band", "02-25 17:30 Band"}
		if got := occurrenceList(t, evs, env.ny); !eq(got, want) {
			t.Fatalf("February = %v\nwant %v", got, want)
		}
		for _, ev := range evs {
			if ev.ID == single {
				if ev.RecurrenceID != "" || ev.Recurring {
					t.Errorf("one-off came back as an occurrence: %+v", ev)
				}
				continue
			}
			if ev.ID != id || !ev.Recurring || ev.Repeat == nil || ev.RRule == "" {
				t.Errorf("occurrence lost its series: %+v", ev)
			}
			if ev.RecurrenceID != ev.Start || ev.LocalDate != ev.Start[:10] || (ev.Weekday != "Tuesday" && ev.Weekday != "Thursday") {
				t.Errorf("occurrence fields: recurrence_id %q start %q local_date %q weekday %q", ev.RecurrenceID, ev.Start, ev.LocalDate, ev.Weekday)
			}
			if !strings.Contains(strings.Join(eventHints(ev), " "), "occurrence_date="+ev.RecurrenceID) {
				t.Errorf("hints don't point at the occurrence: %v", eventHints(ev))
			}
		}
	})

	t.Run("skipped dates and moved occurrences", func(t *testing.T) {
		env.mustCall(t, env.del, map[string]any{"event_id": id, "scope": "this", "occurrence_date": "2027-02-09"})
		env.mustCall(t, env.update, map[string]any{"event_id": id, "scope": "this", "occurrence_date": "2027-02-11",
			"title": "Band (concert)", "start": "2027-02-11T19:00:00-05:00", "end": "2027-02-11T21:00:00-05:00"})
		// Moved out of March into February.
		env.mustCall(t, env.update, map[string]any{"event_id": id, "scope": "this", "occurrence_date": "2027-03-02",
			"start": "2027-02-27T10:00:00-05:00", "end": "2027-02-27T11:00:00-05:00"})

		evs := eventItems(t, env.mustCall(t, find, map[string]any{"from": "2027-02-01", "to": "2027-02-28", "query": "band"}))
		want := []string{"02-02 17:30 Band", "02-04 17:30 Band", "02-11 19:00 Band (concert)",
			"02-16 17:30 Band", "02-18 17:30 Band", "02-23 17:30 Band", "02-25 17:30 Band", "02-27 10:00 Band"}
		if got := occurrenceList(t, evs, env.ny); !eq(got, want) {
			t.Fatalf("February after edits = %v\nwant %v", got, want)
		}
		moved := evs[len(evs)-1]
		if moved.RecurrenceID != "2027-03-02T17:30:00-05:00" || moved.LocalDate != "2027-02-27" {
			t.Errorf("moved occurrence: recurrence_id %q local_date %q", moved.RecurrenceID, moved.LocalDate)
		}
		if evs[2].End != "2027-02-11T21:00:00-05:00" || evs[2].RecurrenceID != "2027-02-11T17:30:00-05:00" {
			t.Errorf("changed occurrence = %+v", evs[2])
		}
		march := occurrenceList(t, eventItems(t, env.mustCall(t, find, map[string]any{"from": "2027-03-01", "to": "2027-03-31"})), env.ny)
		if want := []string{"03-04 17:30 Band", "03-09 17:30 Band", "03-11 17:30 Band"}; !eq(march, want) {
			t.Errorf("March = %v, want %v", march, want)
		}
	})

	t.Run("one event by id is still the series", func(t *testing.T) {
		evs := eventItems(t, env.mustCall(t, find, map[string]any{"id": id}))
		if len(evs) != 1 || evs[0].RecurrenceID != "" || evs[0].Start != "2027-02-02T17:30:00-05:00" {
			t.Errorf("by id = %+v", evs)
		}
	})
}

func TestEventFindPagesOccurrences(t *testing.T) {
	env := newRecurrenceEnv(t)
	find := bind(t, NewEventFindTool, env.pool)
	// Never ends.
	env.mustCall(t, env.create, map[string]any{"title": "Walk the dog", "start": "2027-01-04T07:00:00-05:00", "end": "2027-01-04T07:30:00-05:00",
		"repeat": map[string]any{"freq": "daily"}})

	args := map[string]any{"from": "2027-03-01", "to": "2027-03-30", "limit": 12}
	var days []string
	for page := 1; ; page++ {
		text := env.mustCall(t, find, args)
		list := decodeList(t, text)
		for _, ev := range eventItems(t, text) {
			days = append(days, ev.LocalDate+" "+ev.Start[11:16])
		}
		if list.NextCursor == "" {
			if page != 3 || len(list.Items) != 6 {
				t.Errorf("last page %d has %d items", page, len(list.Items))
			}
			break
		}
		if page == 3 {
			t.Fatalf("more than 3 pages: %s", text)
		}
		args["cursor"] = list.NextCursor
	}
	if len(days) != 30 {
		t.Fatalf("got %d occurrences over 3 pages, want 30: %v", len(days), days)
	}
	for i, d := range days {
		// Every day once, in order, at 07:00 local across the DST change
		// on Mar 14.
		want := time.Date(2027, 3, 1+i, 0, 0, 0, 0, time.UTC).Format(dateLayout) + " 07:00"
		if d != want {
			t.Errorf("occurrence %d = %q, want %q", i, d, want)
		}
	}
}

func TestEventFindCapsNeverEndingSeries(t *testing.T) {
	env := newRecurrenceEnv(t)
	find := bind(t, NewEventFindTool, env.pool)
	// A phone-made hourly rule that never ends: 720 occurrences in 30 days.
	id := seedEvent(t, env.pool, env.calendarID, "hourly@example.com",
		`{"title":"Meds","start":"2027-01-01T12:00:00Z","end":"2027-01-01T12:05:00Z"}`)
	setRecurrence(t, env.pool, id, ptr("FREQ=HOURLY"), nil)

	start := time.Now()
	text := env.mustCall(t, find, map[string]any{"from": "2027-03-01", "to": "2027-03-30", "limit": 100})
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("event_find took %v", d)
	}
	list := decodeList(t, text)
	if list.Total != maxSeriesOccurrences || list.NextCursor == "" || len(list.Items) != 100 {
		t.Errorf("total %d next %q items %d; want %d with a cursor", list.Total, list.NextCursor, len(list.Items), maxSeriesOccurrences)
	}
	if !strings.Contains(list.Summary, `"Meds" repeats more than 400 times`) {
		t.Errorf("summary doesn't say the series was cut: %q", list.Summary)
	}
	evs := eventItems(t, text)
	if evs[0].Start != "2027-03-01T00:00:00-05:00" || evs[1].Start != "2027-03-01T01:00:00-05:00" {
		t.Errorf("first occurrences %q, %q", evs[0].Start, evs[1].Start)
	}
}

func TestAgendaExpandsDailySeries(t *testing.T) {
	env := newRecurrenceEnv(t)
	agenda := bind(t, NewAgendaGetTool, env.pool)
	env.mustCall(t, env.create, map[string]any{"title": "Homework", "start": "2027-03-01T17:30:00-05:00", "end": "2027-03-01T18:30:00-05:00",
		"repeat": map[string]any{"freq": "daily", "until": "2027-03-20"}, "except_dates": []string{"2027-03-15"}})
	env.mustCall(t, env.create, map[string]any{"title": "Trash", "start": "2027-03-02", "end": "2027-03-02", "all_day": true,
		"repeat": map[string]any{"freq": "weekly"}})

	// detail=full gives one entry per occurrence, the shape event_find returns.
	text := env.mustCall(t, agenda, map[string]any{"from": "2027-03-13", "to": "2027-03-23", "areas": []string{"events"}, "detail": "full"})
	var res agendaResult
	if err := json.Unmarshal(data(t, text), &res); err != nil {
		t.Fatal(err)
	}
	// Mar 14 is the DST change: still 17:30. Mar 15 is skipped; the series
	// ends on the 20th. Trash is weekly on Tuesdays, all day.
	want := []string{"03-13 17:30 Homework", "03-14 17:30 Homework", "03-16 00:00 Trash", "03-16 17:30 Homework", "03-17 17:30 Homework",
		"03-18 17:30 Homework", "03-19 17:30 Homework", "03-20 17:30 Homework", "03-23 00:00 Trash"}
	if got := occurrenceList(t, occurrencesOf(t, res.Events), env.ny); !eq(got, want) {
		t.Errorf("agenda = %v\nwant %v", got, want)
	}
	for _, ev := range res.Events {
		if ev.Title != "Trash" {
			continue
		}
		s, _ := time.Parse(time.RFC3339, ev.Start)
		if !ev.AllDay || ev.RecurrenceID != ev.LocalDate || ev.End != s.AddDate(0, 0, 1).Format(time.RFC3339) {
			t.Errorf("all-day occurrence = %+v", ev)
		}
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %v", res.Warnings)
	}

	// The default, summary: one entry per series, its occurrence dates
	// complete (Mar 15 skipped, the series ending on the 20th), and the same
	// days as the full answer.
	text = env.mustCall(t, agenda, map[string]any{"from": "2027-03-13", "to": "2027-03-23", "areas": []string{"events"}})
	var sum agendaResult
	if err := json.Unmarshal(data(t, text), &sum); err != nil {
		t.Fatal(err)
	}
	if len(sum.Events) != 2 {
		t.Fatalf("summary has %d entries, want one per series: %s", len(sum.Events), text)
	}
	byTitle := map[string]AgendaEvent{}
	for _, e := range sum.Events {
		byTitle[e.Title] = e
	}
	hw := byTitle["Homework"]
	if hw.Repeat == "" || hw.Occurrences != 7 || strings.Join(hw.Dates, ",") !=
		"2027-03-13,2027-03-14,2027-03-16,2027-03-17,2027-03-18,2027-03-19,2027-03-20" {
		t.Errorf("Homework summary = %+v", hw)
	}
	if hw.At != "17:30" {
		t.Errorf("Homework at = %q, want 17:30 (a series keeps its first time)", hw.At)
	}
	trash := byTitle["Trash"]
	if !trash.AllDay || strings.Join(trash.Dates, ",") != "2027-03-16,2027-03-23" {
		t.Errorf("Trash summary = %+v", trash)
	}
}

func TestFreeTimeAndConflictsUseOccurrences(t *testing.T) {
	env := newRecurrenceEnv(t)
	free := bind(t, NewCalendarFindFreeTimeTool, env.pool)
	band := writtenEvent(t, env.mustCall(t, env.create, bandArgs)).Event.ID

	t.Run("free time skips each occurrence", func(t *testing.T) {
		text := env.mustCall(t, free, map[string]any{"from": "2027-02-16", "to": "2027-02-17", "duration_minutes": 30, "start_hour": 17, "end_hour": 19})
		var got []string
		var slots []AvailableSlot
		if err := json.Unmarshal(items(t, text), &slots); err != nil {
			t.Fatal(err)
		}
		for _, s := range slots {
			got = append(got, s.Start[5:16]+"-"+s.End[11:16])
		}
		want := []string{"02-16T17:00-17:30", "02-16T18:30-19:00", "02-17T17:00-19:00"}
		if !eq(got, want) {
			t.Errorf("slots = %v, want %v", got, want)
		}
	})

	t.Run("a one-off conflicts with a later occurrence", func(t *testing.T) {
		res := writtenEvent(t, env.mustCall(t, env.create, map[string]any{"title": "Parent meeting",
			"start": "2027-02-23T18:00:00-05:00", "end": "2027-02-23T19:00:00-05:00"}))
		if len(res.Conflicts) != 1 || res.Conflicts[0].ID != band || res.Conflicts[0].Start != "2027-02-23T17:30:00-05:00" || res.Conflicts[0].LocalDate != "2027-02-23" {
			t.Errorf("conflicts = %+v", res.Conflicts)
		}
	})

	t.Run("a new series conflicts with another series' later occurrences, once", func(t *testing.T) {
		// Thursdays only from Feb 11: never overlaps Band's first occurrence
		// (Tue Feb 2), which is all the old check compared against.
		res := writtenEvent(t, env.mustCall(t, env.create, map[string]any{"title": "Piano",
			"start": "2027-02-11T18:00:00-05:00", "end": "2027-02-11T18:45:00-05:00",
			"repeat": map[string]any{"freq": "weekly", "by_day": []string{"TH"}, "count": 4}}))
		var bandHits []ConflictEvent
		for _, c := range res.Conflicts {
			if c.ID == band {
				bandHits = append(bandHits, c)
			}
		}
		if len(bandHits) != 1 || bandHits[0].Start != "2027-02-11T17:30:00-05:00" {
			t.Errorf("conflicts = %+v", res.Conflicts)
		}
	})

	t.Run("a skipped occurrence is not a conflict", func(t *testing.T) {
		env.mustCall(t, env.del, map[string]any{"event_id": band, "scope": "this", "occurrence_date": "2027-03-09"})
		res := writtenEvent(t, env.mustCall(t, env.create, map[string]any{"title": "Dinner out",
			"start": "2027-03-09T17:00:00-05:00", "end": "2027-03-09T19:00:00-05:00"}))
		if len(res.Conflicts) != 0 {
			t.Errorf("conflicts = %+v", res.Conflicts)
		}
	})
}
