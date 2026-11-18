package testutil

// Tests for migration 055 (calendar event titles in activity_log summaries,
// #4694): log_activity() names an event from ical_data's JSON "title", the
// VEVENT SUMMARY, or ical_raw; 055 up backfills names into existing rows.

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func summaries(rows []activityRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.summary
	}
	return out
}

func TestActivityEventTitles055_CreateEditDelete(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)
	amber := newMember(t, pool, f.household, "Amber")
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, f.household)

	var ev uuid.UUID
	asActor(t, pool, actor{user: amber.String()}, func(tx pgx.Tx) {
		ev = txInsertID(t, tx, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, $2, $3, 'e1') RETURNING id`,
			cal, uuid.NewString(),
			`{"title":"Date  night","description":"sitter code 1234","location":"12 Elm St","start":"2026-11-29T01:00:00Z","end":"2026-11-29T04:00:00Z","all_day":false}`)
	})
	asActor(t, pool, actor{user: amber.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE calendar_objects SET ical_data = replace(ical_data, 'Date  night', 'Movie night'), etag = 'e2' WHERE id = $1`, ev)
	})
	asActor(t, pool, actor{user: amber.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `DELETE FROM calendar_objects WHERE id = $1`, ev)
	})

	got := summaries(activityFor(t, pool, ev))
	if len(got) != 3 {
		t.Fatalf("want 3 activity rows, got %q", got)
	}
	if got[0] != `created calendar event "Date night"` {
		t.Errorf("create summary = %q", got[0])
	}
	if !strings.HasPrefix(got[1], `updated calendar event "Movie night" (`) || !strings.Contains(got[1], "ical_data") {
		t.Errorf("update summary = %q", got[1])
	}
	if got[2] != `deleted calendar event "Movie night"` {
		t.Errorf("delete summary = %q", got[2])
	}
	for _, s := range got {
		if strings.Contains(s, "1234") || strings.Contains(s, "Elm") {
			t.Errorf("description or location leaked: %q", s)
		}
	}
}

func TestActivityEventTitles055_Sources(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, f.household)

	cases := []struct {
		name, icalData string
		icalRaw        *string
		want           string
	}{
		{"json", `{"title":"Soccer","start":"2026-11-29"}`, nil, `created calendar event "Soccer"`},
		{"ical VEVENT, not VTIMEZONE", "BEGIN:VCALENDAR\r\nBEGIN:VTIMEZONE\r\nSUMMARY:zone\r\nEND:VTIMEZONE\r\nBEGIN:VEVENT\r\nSUMMARY;LANGUAGE=en:Book club\r\nEND:VEVENT\r\nEND:VCALENDAR", nil, `created calendar event "Book club"`},
		{"json without title falls back to ical_raw", `{"start":"2026-11-29"}`, ptr("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nSUMMARY:Piano\r\nEND:VEVENT\r\nEND:VCALENDAR"), `created calendar event "Piano"`},
		{"bad json never fails the write", `{not json`, nil, `created calendar event`},
		{"blank title", `{"title":"   "}`, nil, `created calendar event`},
		{"long title cut to 80", `{"title":"` + strings.Repeat("x", 100) + `"}`, nil, `created calendar event "` + strings.Repeat("x", 80) + `"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, ical_raw, etag) VALUES ($1, $2, $3, $4, 'e1') RETURNING id`,
				cal, uuid.NewString(), c.icalData, c.icalRaw)
			got := summaries(activityFor(t, pool, ev))
			if len(got) != 1 || got[0] != c.want {
				t.Errorf("summary = %q, want %q", got, c.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }

// 055 up names rows 050 logged without a name, where the event still exists;
// down/up/down/up neither fails nor doubles a name.
func TestActivityEventTitles055_BackfillReversible(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, f.household)

	applyMigration(t, pool, "055_activity_event_titles.down.sql")
	if n := countRows(t, pool, `SELECT count(*) FROM pg_proc WHERE proname = 'calendar_event_label'`); n != 0 {
		t.Fatalf("down left calendar_event_label")
	}
	// With 050's log_activity, JSON events get no name.
	kept := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, 'k', '{"title":"Dentist & co \\ 2"}', 'e1') RETURNING id`, cal)
	mustExec(t, pool, `UPDATE calendar_objects SET ical_data = '{"title":"Dentist & co \\ 2","start":"2026-12-01"}' WHERE id = $1`, kept)
	gone := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, 'g', '{"title":"Gone"}', 'e1') RETURNING id`, cal)
	mustExec(t, pool, `DELETE FROM calendar_objects WHERE id = $1`, gone)
	if got := summaries(activityFor(t, pool, kept)); len(got) != 2 || got[0] != "created calendar event" {
		t.Fatalf("050 summaries = %q", got)
	}

	for cycle := 0; cycle < 2; cycle++ {
		applyMigration(t, pool, "055_activity_event_titles.up.sql")
		got := summaries(activityFor(t, pool, kept))
		if len(got) != 2 || got[0] != `created calendar event "Dentist & co \ 2"` ||
			!strings.HasPrefix(got[1], `updated calendar event "Dentist & co \ 2" (`) {
			t.Errorf("cycle %d: backfilled = %q", cycle, got)
		}
		if got := summaries(activityFor(t, pool, gone)); len(got) != 2 || got[0] != "created calendar event" || got[1] != "deleted calendar event" {
			t.Errorf("cycle %d: deleted event's rows changed: %q", cycle, got)
		}
		applyMigration(t, pool, "055_activity_event_titles.down.sql")
	}
	applyMigration(t, pool, "055_activity_event_titles.up.sql")
	ev := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, 'n', '{"title":"After"}', 'e1') RETURNING id`, cal)
	if got := summaries(activityFor(t, pool, ev)); len(got) != 1 || got[0] != `created calendar event "After"` {
		t.Errorf("after re-up: %q", got)
	}
}
