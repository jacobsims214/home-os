package calendar

// Recurrence, reminders and people on event writes (#4465).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/icalx"
	"home-os/mcp/internal/testutil"
)

func TestRepeatRule(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	timed := &icalx.Event{Start: time.Date(2026, 11, 17, 17, 30, 0, 0, ny), End: time.Date(2026, 11, 17, 18, 30, 0, 0, ny), TZID: "America/New_York"}
	allDay := &icalx.Event{Start: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), AllDay: true}
	tests := []struct {
		name    string
		rep     Repeat
		m       *icalx.Event
		want    string
		wantErr string
	}{
		{"weekly tue/thu until (end of the local day, in UTC)", Repeat{Freq: "weekly", ByDay: []string{"Tuesday", "thu"}, Until: "2026-12-15"}, timed,
			"FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261216T045959Z", ""},
		{"interval and count", Repeat{Freq: "Monthly", Interval: 2, Count: 6}, timed, "FREQ=MONTHLY;INTERVAL=2;COUNT=6", ""},
		{"monthly ordinal", Repeat{Freq: "monthly", ByDay: []string{"2tu", "-1FR"}}, timed, "FREQ=MONTHLY;BYDAY=2TU,-1FR", ""},
		{"all-day until is a DATE", Repeat{Freq: "weekly", ByDay: []string{"MO"}, Until: "2026-12-28"}, allDay, "FREQ=WEEKLY;BYDAY=MO;UNTIL=20261228", ""},
		{"interval 1 is omitted", Repeat{Freq: "daily", Interval: 1}, timed, "FREQ=DAILY", ""},
		{"until and count", Repeat{Freq: "daily", Count: 3, Until: "2026-12-01"}, timed, "", "not both"},
		{"bad freq", Repeat{Freq: "hourly"}, timed, "", "daily, weekly, monthly or yearly"},
		{"bad weekday", Repeat{Freq: "weekly", ByDay: []string{"funday"}}, timed, "", "not a weekday"},
		{"ordinal on weekly", Repeat{Freq: "weekly", ByDay: []string{"2TU"}}, timed, "", "monthly or yearly"},
		{"until before start", Repeat{Freq: "weekly", Until: "2026-11-01"}, timed, "", "before the first occurrence"},
		{"count too big", Repeat{Freq: "daily", Count: 5000}, timed, "", "between 1 and"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repeatRule(&tt.rep, tt.m, ny)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("rule = %q, %v; want %q", got, err, tt.want)
			}
			back := repeatFromRule(got, tt.m, ny)
			if back == nil || back.Freq != strings.ToLower(tt.rep.Freq) || back.Until != tt.rep.Until {
				t.Errorf("repeatFromRule(%q) = %+v", got, back)
			}
		})
	}
}

func TestRuleHelpers(t *testing.T) {
	if got := withRulePart("FREQ=WEEKLY;COUNT=10;BYDAY=TU", "COUNT", "4"); got != "FREQ=WEEKLY;COUNT=4;BYDAY=TU" {
		t.Errorf("withRulePart replace = %q", got)
	}
	if got := withRulePart("FREQ=WEEKLY;BYDAY=TU", "UNTIL", "20261201T045959Z"); got != "FREQ=WEEKLY;BYDAY=TU;UNTIL=20261201T045959Z" {
		t.Errorf("withRulePart add = %q", got)
	}
	if got := withRulePart("FREQ=WEEKLY;UNTIL=X;BYDAY=TU", "UNTIL", ""); got != "FREQ=WEEKLY;BYDAY=TU" {
		t.Errorf("withRulePart remove = %q", got)
	}
	for _, tt := range []struct {
		rule string
		n    int
		want string
	}{
		{"FREQ=WEEKLY;BYDAY=TU,TH", 1, "FREQ=WEEKLY;BYDAY=WE,FR"},
		{"FREQ=WEEKLY;BYDAY=SA", 1, "FREQ=WEEKLY;BYDAY=SU"},
		{"FREQ=WEEKLY;BYDAY=MO", -1, "FREQ=WEEKLY;BYDAY=SU"},
		{"FREQ=MONTHLY;BYDAY=2TU", 2, "FREQ=MONTHLY;BYDAY=2TH"},
		{"FREQ=DAILY", 3, "FREQ=DAILY"},
	} {
		if got := shiftByDay(tt.rule, tt.n); got != tt.want {
			t.Errorf("shiftByDay(%q, %d) = %q, want %q", tt.rule, tt.n, got, tt.want)
		}
	}
	if got := describeRepeat(&Repeat{Freq: "weekly", ByDay: []string{"TU", "TH"}, Until: "2026-12-15"}, ""); got != "every week on Tue, Thu until 2026-12-15" {
		t.Errorf("describeRepeat = %q", got)
	}
}

// ─────────────────────────────────────────────
// Integration
// ─────────────────────────────────────────────

type recurrenceEnv struct {
	pool       *pgxpool.Pool
	ctx        context.Context
	household  uuid.UUID
	calendarID string
	nora       string
	ny         *time.Location
	create     handlerFunc
	update     handlerFunc
	del        handlerFunc
}

func newRecurrenceEnv(t *testing.T) recurrenceEnv {
	t.Helper()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	env := recurrenceEnv{pool: pool, ctx: ctx, household: hh.HouseholdID, calendarID: seedCalendar(t, pool, hh.HouseholdID),
		ny:     mustLoc(t, "America/New_York"),
		create: bind(t, NewEventCreateTool, pool), update: bind(t, NewEventUpdateTool, pool), del: bind(t, NewEventDeleteTool, pool)}
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO people (household_id, display_name) VALUES ($1, 'Nora Smith') RETURNING id::text`, hh.HouseholdID,
	).Scan(&env.nora); err != nil {
		t.Fatal(err)
	}
	return env
}

// call runs a tool with arguments as a JSON client sends them.
func (e recurrenceEnv) call(t *testing.T, h handlerFunc, args map[string]any) (string, bool) {
	t.Helper()
	b, _ := json.Marshal(args)
	var decoded map[string]any
	_ = json.Unmarshal(b, &decoded)
	return callTool(t, h, e.ctx, decoded)
}

func (e recurrenceEnv) mustCall(t *testing.T, h handlerFunc, args map[string]any) string {
	t.Helper()
	text, isErr := e.call(t, h, args)
	if isErr {
		t.Fatalf("unexpected error for %v: %s", args, text)
	}
	return text
}

type objRow struct {
	id, uid, data string
	raw, rrule    *string
}

func (e recurrenceEnv) rows(t *testing.T, title string) []objRow {
	t.Helper()
	rs, err := e.pool.Query(context.Background(),
		`SELECT id::text, uid, ical_data, ical_raw, rrule FROM calendar_objects
		  WHERE calendar_id = $1 AND ical_data::jsonb->>'title' LIKE $2 ORDER BY starts_at`, e.calendarID, title)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []objRow
	for rs.Next() {
		var r objRow
		if err := rs.Scan(&r.id, &r.uid, &r.data, &r.raw, &r.rrule); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (e recurrenceEnv) row(t *testing.T, id string) objRow {
	t.Helper()
	var r objRow
	if err := e.pool.QueryRow(context.Background(),
		`SELECT id::text, uid, ical_data, ical_raw, rrule FROM calendar_objects WHERE id = $1`, id,
	).Scan(&r.id, &r.uid, &r.data, &r.raw, &r.rrule); err != nil {
		t.Fatalf("read row %s: %v", id, err)
	}
	return r
}

// consistent checks the row the way CalDAV serves it: ical_raw parses,
// its master matches the JSON (so phones get the raw, not a regenerated
// single event) and the rrule column is the master's RRULE.
func consistent(t *testing.T, r objRow) *icalx.Object {
	t.Helper()
	if r.raw == nil {
		t.Fatalf("row %s has no ical_raw", r.id)
	}
	var ej eventJSON
	if err := json.Unmarshal([]byte(r.data), &ej); err != nil {
		t.Fatal(err)
	}
	s := loadSeries(ej, r.uid, r.rrule, r.raw)
	if !s.fromRaw {
		t.Fatalf("ical_raw is stale for the JSON %s:\n%s", r.data, *r.raw)
	}
	want := ""
	if r.rrule != nil {
		want = *r.rrule
	}
	if s.obj.Master.RRule != want {
		t.Errorf("rrule column %q, raw RRULE %q", want, s.obj.Master.RRule)
	}
	if s.obj.UID != r.uid {
		t.Errorf("raw UID %q, row uid %q", s.obj.UID, r.uid)
	}
	return s.obj
}

// starts lists the occurrence starts (New York wall clock) in Oct–Dec 2026.
func starts(t *testing.T, obj *icalx.Object, ny *time.Location) []string {
	t.Helper()
	occs, err := obj.Expand(time.Date(2026, 10, 1, 0, 0, 0, 0, ny), time.Date(2027, 1, 1, 0, 0, 0, 0, ny), ny)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, o := range occs {
		s := o.Start.In(ny).Format("01-02 15:04")
		if o.Override && o.Event.Summary != obj.Master.Summary {
			s += " " + o.Event.Summary
		}
		out = append(out, s)
	}
	return out
}

func eq(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

var soccerArgs = map[string]any{
	"title": "Soccer", "start": "2026-11-17T17:30:00-05:00", "end": "2026-11-17T18:30:00-05:00",
	"repeat": map[string]any{"freq": "weekly", "by_day": []string{"TU", "TH"}, "until": "2026-12-15"},
}

// "Soccer every Tue/Thu 5:30-6:30 until Dec 15" is one event with an RRULE,
// a VALARM and an ATTENDEE, stored consistently.
func TestEventCreateRecurring(t *testing.T) {
	env := newRecurrenceEnv(t)
	args := map[string]any{"reminder_minutes": []int{30}, "person_ids": []string{"Nora"}}
	for k, v := range soccerArgs {
		args[k] = v
	}
	res := writtenEvent(t, env.mustCall(t, env.create, args))
	ev := res.Event
	if !ev.Recurring || ev.Repeat == nil || ev.Repeat.Freq != "weekly" || !eq(ev.Repeat.ByDay, []string{"TU", "TH"}) || ev.Repeat.Until != "2026-12-15" {
		t.Errorf("event repeat = %+v recurring=%v", ev.Repeat, ev.Recurring)
	}
	if !eq(intsToStrings(ev.ReminderMinutes), []string{"30"}) || len(ev.People) != 1 || ev.People[0].ID != env.nora || ev.People[0].Name != "Nora Smith" {
		t.Errorf("reminders=%v people=%+v", ev.ReminderMinutes, ev.People)
	}
	if ev.Start != "2026-11-17T17:30:00-05:00" || ev.End != "2026-11-17T18:30:00-05:00" {
		t.Errorf("first occurrence %s–%s", ev.Start, ev.End)
	}

	rows := env.rows(t, "Soccer")
	if len(rows) != 1 {
		t.Fatalf("want one event row, got %d", len(rows))
	}
	r := rows[0]
	if r.rrule == nil || *r.rrule != "FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261216T045959Z" {
		t.Errorf("rrule = %v", r.rrule)
	}
	raw := strings.ReplaceAll(*r.raw, "\r\n ", "")
	for _, want := range []string{"RRULE:FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261216T045959Z", "DTSTART;TZID=America/New_York:20261117T173000",
		"BEGIN:VTIMEZONE", "BEGIN:VALARM", "TRIGGER:-PT30M", "ACTION:DISPLAY", "X-HOMEOS-PERSON-ID=" + env.nora, "CN=Nora Smith",
		"urn:uuid:" + env.nora, "STATUS:CONFIRMED"} {
		if !strings.Contains(raw, want) {
			t.Errorf("ical_raw lacks %q:\n%s", want, *r.raw)
		}
	}
	if !strings.Contains(r.data, `"people":[{"id":"`+env.nora+`","name":"Nora Smith"}]`) || !strings.Contains(r.data, `"start":"2026-11-17T22:30:00Z"`) {
		t.Errorf("ical_data = %s", r.data)
	}
	obj := consistent(t, r)
	want := []string{"11-17 17:30", "11-19 17:30", "11-24 17:30", "11-26 17:30", "12-01 17:30", "12-03 17:30", "12-08 17:30", "12-10 17:30", "12-15 17:30"}
	if got := starts(t, obj, env.ny); !eq(got, want) {
		t.Errorf("occurrences = %v\nwant %v", got, want)
	}
	var changes int
	_ = env.pool.QueryRow(context.Background(), `SELECT count(*) FROM calendar_changes WHERE event_uid = $1 AND action = 'create'`, r.uid).Scan(&changes)
	if changes != 1 {
		t.Errorf("create changes = %d", changes)
	}

	t.Run("start off the repeat days moves to the first one, across DST", func(t *testing.T) {
		text := env.mustCall(t, env.create, map[string]any{"title": "Piano", "start": "2026-10-26T16:00:00-04:00", "end": "2026-10-26T16:45:00-04:00",
			"repeat": map[string]any{"freq": "weekly", "by_day": "WE", "count": 3}, "except_dates": []string{"2026-11-04"}})
		res := writtenEvent(t, text)
		if !strings.Contains(res.Note, "2 days later") || res.Event.LocalDate != "2026-10-28" {
			t.Errorf("note %q local_date %s", res.Note, res.Event.LocalDate)
		}
		obj := consistent(t, env.rows(t, "Piano")[0])
		if got := starts(t, obj, env.ny); !eq(got, []string{"10-28 16:00", "11-11 16:00"}) { // COUNT=3 minus the EXDATE; 16:00 on both sides of Nov 1
			t.Errorf("piano = %v", got)
		}
		if !eq(res.Event.ExceptDates, []string{"2026-11-04"}) {
			t.Errorf("except_dates = %v", res.Event.ExceptDates)
		}
	})

	t.Run("all-day series", func(t *testing.T) {
		env.mustCall(t, env.create, map[string]any{"title": "Trash day", "all_day": true, "start": "2026-11-16", "end": "2026-11-16",
			"repeat": map[string]any{"freq": "weekly", "until": "2026-12-07"}})
		r := env.rows(t, "Trash day")[0]
		if !strings.Contains(*r.raw, "DTSTART;VALUE=DATE:20261116") || *r.rrule != "FREQ=WEEKLY;UNTIL=20261207" {
			t.Errorf("all-day raw:\n%s", *r.raw)
		}
		if got := starts(t, consistent(t, r), env.ny); len(got) != 4 || got[0] != "11-16 00:00" {
			t.Errorf("trash = %v", got)
		}
	})

	t.Run("bad input", func(t *testing.T) {
		for name, tc := range map[string]struct {
			args  map[string]any
			field string
		}{
			"unsupported rule":         {map[string]any{"repeat": "FREQ=SECONDLY"}, "repeat"},
			"never-matching rule":      {map[string]any{"repeat": "FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30"}, "repeat"},
			"except date not in rule":  {map[string]any{"except_dates": []string{"2026-11-18"}}, "except_dates"},
			"unknown person":           {map[string]any{"person_ids": []string{"Zed"}}, "person_ids"},
			"reminder too far":         {map[string]any{"reminder_minutes": []int{99999}}, "reminder_minutes"},
			"except_dates w/o repeat":  {map[string]any{"repeat": nil, "except_dates": []string{"2026-11-17"}}, "except_dates"},
			"until and count together": {map[string]any{"repeat": map[string]any{"freq": "daily", "count": 2, "until": "2026-12-01"}}, "repeat"},
		} {
			a := map[string]any{}
			for k, v := range soccerArgs {
				a[k] = v
			}
			a["title"] = "Bad " + name
			for k, v := range tc.args {
				a[k] = v
			}
			text, isErr := env.call(t, env.create, a)
			if !isErr || !strings.Contains(text, `"field":"`+tc.field+`"`) {
				t.Errorf("%s: want error on %s, got %s", name, tc.field, text)
			}
		}
		if n := len(env.rows(t, "Bad %")); n != 0 {
			t.Errorf("%d rows written by rejected creates", n)
		}
	})
}

func intsToStrings(in []int) []string {
	var out []string
	for _, n := range in {
		b, _ := json.Marshal(n)
		out = append(out, string(b))
	}
	return out
}

// Editing one occurrence vs this-and-following vs all behaves per scope.
func TestEventUpdateScopes(t *testing.T) {
	env := newRecurrenceEnv(t)
	created := writtenEvent(t, env.mustCall(t, env.create, soccerArgs)).Event
	id := created.ID

	t.Run("no scope is a corrective error", func(t *testing.T) {
		before := env.row(t, id)
		text, isErr := env.call(t, env.update, map[string]any{"event_id": id, "title": "x"})
		if !isErr || !strings.Contains(text, `"field":"scope"`) || !strings.Contains(text, "occurrence_date") {
			t.Fatalf("got %s", text)
		}
		if after := env.row(t, id); after.data != before.data || *after.raw != *before.raw {
			t.Error("row changed")
		}
		text, isErr = env.call(t, env.update, map[string]any{"event_id": id, "scope": "this", "title": "x"})
		if !isErr || !strings.Contains(text, `"field":"occurrence_date"`) {
			t.Errorf("scope this without occurrence_date: %s", text)
		}
		text, isErr = env.call(t, env.update, map[string]any{"event_id": id, "occurrence_date": "2026-11-18", "title": "x"})
		if !isErr || !strings.Contains(text, "no occurrence on Wed 2026-11-18") || !strings.Contains(text, "2026-11-19") {
			t.Errorf("not an occurrence: %s", text)
		}
		text, isErr = env.call(t, env.update, map[string]any{"event_id": id, "occurrence_date": "2026-11-19", "repeat": map[string]any{"freq": "daily"}})
		if !isErr || !strings.Contains(text, "whole series") {
			t.Errorf("repeat with scope this: %s", text)
		}
	})

	t.Run("this: one occurrence becomes an override", func(t *testing.T) {
		text := env.mustCall(t, env.update, map[string]any{"event_id": id, "scope": "this", "occurrence_date": "2026-11-26",
			"title": "Soccer (morning)", "start": "2026-11-26T10:00:00-05:00", "reminder_minutes": []int{15}})
		res := writtenEvent(t, text)
		if res.Scope != "this" || res.Event.RecurrenceID != "2026-11-26T17:30:00-05:00" || res.Event.Start != "2026-11-26T10:00:00-05:00" ||
			res.Event.End != "2026-11-26T11:00:00-05:00" || res.Event.Title != "Soccer (morning)" || !eq(intsToStrings(res.Event.ReminderMinutes), []string{"15"}) {
			t.Errorf("occurrence result = %+v", res.Event)
		}
		r := env.row(t, id)
		if !strings.Contains(*r.raw, "RECURRENCE-ID;TZID=America/New_York:20261126T173000") {
			t.Errorf("no override in raw:\n%s", *r.raw)
		}
		if !strings.Contains(r.data, `"title":"Soccer"`) {
			t.Errorf("master JSON changed: %s", r.data)
		}
		want := []string{"11-17 17:30", "11-19 17:30", "11-24 17:30", "11-26 10:00 Soccer (morning)", "12-01 17:30", "12-03 17:30", "12-08 17:30", "12-10 17:30", "12-15 17:30"}
		if got := starts(t, consistent(t, r), env.ny); !eq(got, want) {
			t.Errorf("after this-edit = %v", got)
		}
		// Editing the same occurrence again changes the override, not a new one.
		env.mustCall(t, env.update, map[string]any{"event_id": id, "occurrence_date": "2026-11-26", "location": "Gym"})
		if r := env.row(t, id); strings.Count(*r.raw, "RECURRENCE-ID") != 1 || !strings.Contains(*r.raw, "LOCATION:Gym") {
			t.Errorf("second this-edit:\n%s", *r.raw)
		}
	})

	t.Run("all: the series moves, the override keeps its own changes", func(t *testing.T) {
		res := writtenEvent(t, env.mustCall(t, env.update, map[string]any{"event_id": id, "scope": "all", "occurrence_date": "2026-11-19",
			"title": "Soccer practice", "start": "2026-11-19T18:00:00-05:00", "person_ids": []string{env.nora}}))
		if res.Scope != "all" || res.Event.Title != "Soccer practice" || res.Event.Start != "2026-11-17T18:00:00-05:00" || len(res.Event.People) != 1 {
			t.Errorf("all result = %+v", res.Event)
		}
		r := env.row(t, id)
		want := []string{"11-17 18:00", "11-19 18:00", "11-24 18:00", "11-26 10:00 Soccer (morning)", "12-01 18:00", "12-03 18:00", "12-08 18:00", "12-10 18:00", "12-15 18:00"}
		if got := starts(t, consistent(t, r), env.ny); !eq(got, want) {
			t.Errorf("after all-edit = %v\nwant %v", got, want)
		}
		if !strings.Contains(*r.raw, "RECURRENCE-ID;TZID=America/New_York:20261126T180000") || strings.Count(*r.raw, "X-HOMEOS-PERSON-ID") != 2 {
			t.Errorf("override not moved along / people not on both:\n%s", *r.raw)
		}
	})

	t.Run("following: the series is split", func(t *testing.T) {
		res := writtenEvent(t, env.mustCall(t, env.update, map[string]any{"event_id": id, "scope": "following", "occurrence_date": "2026-12-03",
			"location": "Field 9"}))
		if res.Scope != "following" || res.OriginalEventID != id || res.Event.ID == id || res.Event.Location != "Field 9" || res.Event.LocalDate != "2026-12-03" {
			t.Fatalf("following result = %+v", res)
		}
		old, nw := env.row(t, id), env.row(t, res.Event.ID)
		if nw.uid == old.uid || *old.rrule != "FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261203T225959Z" || *nw.rrule != "FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261216T045959Z" {
			t.Errorf("rules: old %q new %q", *old.rrule, *nw.rrule)
		}
		if got := starts(t, consistent(t, old), env.ny); !eq(got, []string{"11-17 18:00", "11-19 18:00", "11-24 18:00", "11-26 10:00 Soccer (morning)", "12-01 18:00"}) {
			t.Errorf("old part = %v", got)
		}
		newObj := consistent(t, nw)
		if got := starts(t, newObj, env.ny); !eq(got, []string{"12-03 18:00", "12-08 18:00", "12-10 18:00", "12-15 18:00"}) {
			t.Errorf("new part = %v", got)
		}
		if newObj.Master.Location != "Field 9" || len(peopleFromAttendees(newObj.Master)) != 1 {
			t.Errorf("new master = %+v", newObj.Master)
		}
		var actions []string
		rows, _ := env.pool.Query(context.Background(), `SELECT action FROM calendar_changes WHERE event_uid = $1`, nw.uid)
		for rows.Next() {
			var a string
			_ = rows.Scan(&a)
			actions = append(actions, a)
		}
		rows.Close()
		if !eq(actions, []string{"create"}) {
			t.Errorf("new uid changes = %v", actions)
		}
	})

	t.Run("following with COUNT splits the count", func(t *testing.T) {
		c := writtenEvent(t, env.mustCall(t, env.create, map[string]any{"title": "Swim", "start": "2026-11-16T07:00:00-05:00", "end": "2026-11-16T08:00:00-05:00",
			"repeat": map[string]any{"freq": "daily", "count": 6}})).Event
		res := writtenEvent(t, env.mustCall(t, env.update, map[string]any{"event_id": c.ID, "scope": "following", "occurrence_date": "2026-11-18",
			"start": "2026-11-18T06:30:00-05:00"}))
		if r := env.row(t, c.ID); *r.rrule != "FREQ=DAILY;COUNT=2" {
			t.Errorf("old rule %q", *r.rrule)
		}
		nw := env.row(t, res.Event.ID)
		if got := starts(t, consistent(t, nw), env.ny); !eq(got, []string{"11-18 06:30", "11-19 06:30", "11-20 06:30", "11-21 06:30"}) {
			t.Errorf("new swim = %v (rule %s)", got, *nw.rrule)
		}
	})

	t.Run("repeat-only change implies all; freq none makes a one-off", func(t *testing.T) {
		c := writtenEvent(t, env.mustCall(t, env.create, map[string]any{"title": "Chess", "start": "2026-11-16T15:00:00-05:00", "end": "2026-11-16T16:00:00-05:00",
			"repeat": map[string]any{"freq": "weekly"}})).Event
		res := writtenEvent(t, env.mustCall(t, env.update, map[string]any{"event_id": c.ID, "repeat": map[string]any{"freq": "weekly", "interval": 2, "count": 3}}))
		if res.Scope != "all" || res.Event.Repeat == nil || res.Event.Repeat.Interval != 2 {
			t.Errorf("repeat change = %+v", res)
		}
		if got := starts(t, consistent(t, env.row(t, c.ID)), env.ny); !eq(got, []string{"11-16 15:00", "11-30 15:00", "12-14 15:00"}) {
			t.Errorf("every 2 weeks = %v", got)
		}
		res = writtenEvent(t, env.mustCall(t, env.update, map[string]any{"event_id": c.ID, "scope": "all", "repeat": map[string]any{"freq": "none"}}))
		r := env.row(t, c.ID)
		if res.Event.Recurring || r.rrule != nil || strings.Contains(*r.raw, "RRULE:FREQ=WEEKLY") {
			t.Errorf("still repeating: %+v\n%s", res.Event, *r.raw)
		}
		consistent(t, r)
	})

	t.Run("a one-off becomes a series; reminders [] removes alarms", func(t *testing.T) {
		c := writtenEvent(t, env.mustCall(t, env.create, map[string]any{"title": "Vet", "start": "2026-11-20T09:00:00-05:00", "end": "2026-11-20T09:30:00-05:00",
			"reminder_minutes": []int{60}})).Event
		r := env.row(t, c.ID)
		if r.rrule != nil || !strings.Contains(*r.raw, "TRIGGER:-PT1H") || !strings.Contains(*r.raw, "DTSTART:20261120T140000Z") {
			t.Errorf("one-off raw:\n%s", *r.raw)
		}
		env.mustCall(t, env.update, map[string]any{"event_id": c.ID, "repeat": map[string]any{"freq": "monthly", "count": 2}, "reminder_minutes": []int{}})
		r = env.row(t, c.ID)
		if strings.Contains(*r.raw, "VALARM") {
			t.Errorf("alarm not removed:\n%s", *r.raw)
		}
		if got := starts(t, consistent(t, r), env.ny); !eq(got, []string{"11-20 09:00", "12-20 09:00"}) {
			t.Errorf("monthly vet = %v", got)
		}
	})

	t.Run("all-day occurrence moved by a day", func(t *testing.T) {
		c := writtenEvent(t, env.mustCall(t, env.create, map[string]any{"title": "Recycling", "all_day": true, "start": "2026-11-16", "end": "2026-11-16",
			"repeat": map[string]any{"freq": "weekly", "count": 4}})).Event
		res := writtenEvent(t, env.mustCall(t, env.update, map[string]any{"event_id": c.ID, "occurrence_date": "2026-11-23", "start": "2026-11-24"}))
		if res.Event.RecurrenceID != "2026-11-23" || res.Event.LocalDate != "2026-11-24" {
			t.Errorf("all-day occurrence = %+v", res.Event)
		}
		r := env.row(t, c.ID)
		if !strings.Contains(*r.raw, "RECURRENCE-ID;VALUE=DATE:20261123") {
			t.Errorf("raw:\n%s", *r.raw)
		}
		if got := starts(t, consistent(t, r), env.ny); !eq(got, []string{"11-16 00:00", "11-24 00:00", "11-30 00:00", "12-07 00:00"}) {
			t.Errorf("recycling = %v", got)
		}
	})
}

// Phone-written raw (VTIMEZONE, ATTENDEE, X- properties, an override) is
// kept when the series is edited through the MCP.
func TestEventUpdateKeepsPhoneRaw(t *testing.T) {
	env := newRecurrenceEnv(t)
	const raw = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Apple Inc.//iPhone OS 18//EN\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:America/New_York\r\nBEGIN:STANDARD\r\nDTSTART:20071104T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU\r\nTZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\nTZNAME:EST\r\nEND:STANDARD\r\n" +
		"BEGIN:DAYLIGHT\r\nDTSTART:20070311T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\nTZNAME:EDT\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:phone-1@example.com\r\nDTSTAMP:20261101T000000Z\r\nDTSTART;TZID=America/New_York:20261116T190000\r\nDTEND;TZID=America/New_York:20261116T200000\r\n" +
		"RRULE:FREQ=WEEKLY;BYDAY=MO\r\nSUMMARY:Book club\r\nATTENDEE;CN=Pat;PARTSTAT=NEEDS-ACTION:mailto:pat@example.com\r\nX-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:phone-1@example.com\r\nDTSTAMP:20261101T000000Z\r\nRECURRENCE-ID;TZID=America/New_York:20261123T190000\r\nDTSTART;TZID=America/New_York:20261124T190000\r\nDTEND;TZID=America/New_York:20261124T200000\r\nSUMMARY:Book club\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	const data = `{"title":"Book club","description":"","start":"2026-11-17T00:00:00Z","end":"2026-11-17T01:00:00Z","all_day":false,"location":"","sequence":2}`
	id := seedEvent(t, env.pool, env.calendarID, "phone-1@example.com", data)
	setRecurrence(t, env.pool, id, ptr("FREQ=WEEKLY;BYDAY=MO"), ptr(raw))

	env.mustCall(t, env.update, map[string]any{"event_id": id, "scope": "all", "location": "Library", "person_ids": []string{"Nora Smith"}})
	r := env.row(t, id)
	flat := strings.ReplaceAll(*r.raw, "\r\n ", "")
	for _, want := range []string{"TZNAME:EDT", "mailto:pat@example.com", "X-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC", "RECURRENCE-ID;TZID=America/New_York:20261123T190000",
		"LOCATION:Library", "X-HOMEOS-PERSON-ID=" + env.nora, "UID:phone-1@example.com", "SEQUENCE:1"} {
		if !strings.Contains(flat, want) {
			t.Errorf("raw lacks %q:\n%s", want, *r.raw)
		}
	}
	if !strings.Contains(r.data, `"sequence":3`) {
		t.Errorf("JSON sequence not bumped: %s", r.data)
	}
	if got := starts(t, consistent(t, r), env.ny); len(got) < 3 || got[1] != "11-24 19:00" {
		t.Errorf("book club = %v", got)
	}
}

func TestEventDeleteScopes(t *testing.T) {
	env := newRecurrenceEnv(t)
	id := writtenEvent(t, env.mustCall(t, env.create, soccerArgs)).Event.ID

	t.Run("no scope deletes nothing", func(t *testing.T) {
		text, isErr := env.call(t, env.del, map[string]any{"event_id": id})
		if !isErr || !strings.Contains(text, "Nothing was deleted") {
			t.Fatalf("got %s", text)
		}
		env.row(t, id)
	})
	t.Run("this adds an EXDATE", func(t *testing.T) {
		env.mustCall(t, env.update, map[string]any{"event_id": id, "occurrence_date": "2026-11-24", "title": "moved"})
		text := env.mustCall(t, env.del, map[string]any{"event_id": id, "occurrence_date": "2026-11-24"})
		var res eventDeleteResult
		_ = json.Unmarshal(data(t, text), &res)
		if res.Scope != "this" || res.OccurrenceDate != "2026-11-24" || res.SeriesDeleted || !eq(res.Event.ExceptDates, []string{"2026-11-24"}) {
			t.Errorf("delete this = %+v", res)
		}
		r := env.row(t, id)
		if !strings.Contains(*r.raw, "EXDATE;TZID=America/New_York:20261124T173000") || strings.Contains(*r.raw, "RECURRENCE-ID") {
			t.Errorf("raw:\n%s", *r.raw)
		}
		if got := starts(t, consistent(t, r), env.ny); len(got) != 8 {
			t.Errorf("occurrences = %v", got)
		}
		if text, isErr := env.call(t, env.del, map[string]any{"event_id": id, "occurrence_date": "2026-11-24"}); !isErr || !strings.Contains(text, "already removed") {
			t.Errorf("second delete: %s", text)
		}
	})
	t.Run("following truncates", func(t *testing.T) {
		env.mustCall(t, env.del, map[string]any{"event_id": id, "scope": "following", "occurrence_date": "2026-12-08"})
		r := env.row(t, id)
		if got := starts(t, consistent(t, r), env.ny); !eq(got, []string{"11-17 17:30", "11-19 17:30", "11-26 17:30", "12-01 17:30", "12-03 17:30"}) {
			t.Errorf("after following = %v (%s)", got, *r.rrule)
		}
	})
	t.Run("following from the first occurrence deletes the row", func(t *testing.T) {
		env.mustCall(t, env.del, map[string]any{"event_id": id, "scope": "following", "occurrence_date": "2026-11-17"})
		var n int
		_ = env.pool.QueryRow(context.Background(), `SELECT count(*) FROM calendar_objects WHERE id = $1`, id).Scan(&n)
		if n != 0 {
			t.Error("row still there")
		}
	})
}
