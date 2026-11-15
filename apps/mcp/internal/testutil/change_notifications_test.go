package testutil

// Tests for migration 054 (creator notifications on activity_log). See KB
// architecture/change-notifications.md.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newMember is a user who belongs to the family (and so gets a person whose
// display_name is name).
func newMember(t *testing.T, pool *pgxpool.Pool, household uuid.UUID, name string) uuid.UUID {
	t.Helper()
	u := newUser(t, pool, name)
	mustExec(t, pool, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'family_member')`, household, u)
	return u
}

type changeNotice struct {
	title      string
	body       *string
	entityType *string
	entityID   *uuid.UUID
}

// changeNotices returns user's item_changed notifications, oldest first.
func changeNotices(t *testing.T, pool *pgxpool.Pool, user uuid.UUID) []changeNotice {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT title, body, entity_type, entity_id FROM notifications
		 WHERE user_id = $1 AND type = 'item_changed' AND channel = 'in_app'
		 ORDER BY created_at, title`, user)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []changeNotice
	for rows.Next() {
		var n changeNotice
		if err := rows.Scan(&n.title, &n.body, &n.entityType, &n.entityID); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func strp(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// The VTIMEZONE's DTSTART comes first; the date must come from the VEVENT.
const bookClubICal = "BEGIN:VCALENDAR\r\nBEGIN:VTIMEZONE\r\nTZID:America/Chicago\r\nBEGIN:STANDARD\r\nDTSTART:19701101T020000\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\nUID:bc1\r\nSUMMARY:Book club\r\nDTSTART;TZID=America/Chicago:20261127T190000\r\nDESCRIPTION:door code 1234\r\nEND:VEVENT\r\nEND:VCALENDAR"

// Another member deleting my event notifies me, with the event's name and
// date. ical_data is usually JSON (api, mcp, calendar app), sometimes iCal.
func TestChangeNotifications054_OtherMemberDeleteNotifiesCreator(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)
	mustExec(t, pool, `UPDATE households SET timezone = 'America/Chicago' WHERE id = $1`, f.household)
	bob := newMember(t, pool, f.household, "Bob")
	amber := newMember(t, pool, f.household, "Amber")
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, f.household)

	var events []uuid.UUID
	asActor(t, pool, actor{user: bob.String()}, func(tx pgx.Tx) {
		for _, ical := range []string{
			// 01:00Z on the 29th is 19:00 on the 28th in Chicago.
			`{"title":"Date night","description":"sitter code 1234","start":"2026-11-29T01:00:00Z","end":"2026-11-29T04:00:00Z","all_day":false}`,
			// All-day starts keep their date (00:00Z would be the 25th in Chicago).
			`{"title":"Thanksgiving","start":"2026-11-26T00:00:00Z","end":"2026-11-26T23:59:00Z","all_day":true}`,
			bookClubICal,
		} {
			events = append(events, txInsertID(t, tx, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, $2, $3, 'e1') RETURNING id`, cal, uuid.NewString(), ical))
		}
	})
	if got := changeNotices(t, pool, bob); len(got) != 0 {
		t.Fatalf("create notified: %+v", got)
	}

	asActor(t, pool, actor{user: amber.String()}, func(tx pgx.Tx) {
		for _, ev := range events {
			txExec(t, tx, `DELETE FROM calendar_objects WHERE id = $1`, ev)
		}
	})

	got := changeNotices(t, pool, bob)
	want := map[string]bool{
		`Amber deleted event "Date night" on Sat Nov 28`:   true,
		`Amber deleted event "Thanksgiving" on Thu Nov 26`: true,
		`Amber deleted event "Book club" on Fri Nov 27`:    true,
	}
	if len(got) != len(want) {
		t.Errorf("want %d notifications for Bob, got %d", len(want), len(got))
	}
	for _, n := range got {
		if !want[n.title] {
			t.Errorf("unexpected title %q", n.title)
		}
		if strp(n.body) != "You created this event." {
			t.Errorf("body = %q", strp(n.body))
		}
		// The event is gone, so it links to its calendar.
		if strp(n.entityType) != "calendar" || !eqID(n.entityID, cal) {
			t.Errorf("link = %s %v, want calendar %s", strp(n.entityType), n.entityID, cal)
		}
		if strings.Contains(n.title+strp(n.body), "1234") {
			t.Errorf("event description leaked: %q / %q", n.title, strp(n.body))
		}
	}
	// Personal: the actor gets nothing.
	if got := changeNotices(t, pool, amber); len(got) != 0 {
		t.Errorf("actor notified: %+v", got)
	}
}

// Own edits, agent edits for oneself and edits with no actor notify no one.
func TestChangeNotifications054_SelfAndSystemEditsNotifyNoOne(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)
	bob := newMember(t, pool, f.household, "Bob")
	bobAgent := newAgentToken(t, pool, f.household, bob)

	var bill, note uuid.UUID
	asActor(t, pool, actor{user: bob.String()}, func(tx pgx.Tx) {
		bill = txInsertID(t, tx, `INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Netflix', 15) RETURNING id`, f.household)
		note = txInsertID(t, tx, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'household', $1, 'Alarm', 'x') RETURNING id`, f.household)
	})
	asActor(t, pool, actor{user: bob.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE bills SET amount = 16 WHERE id = $1`, bill)
		txExec(t, tx, `UPDATE notes SET body = 'y' WHERE id = $1`, note)
	})
	asActor(t, pool, actor{user: bob.String(), agentToken: bobAgent.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE bills SET amount = 17 WHERE id = $1`, bill)
	})
	// System / unknown caller.
	mustExec(t, pool, `UPDATE bills SET amount = 18 WHERE id = $1`, bill)
	asActor(t, pool, actor{user: "not-a-uuid"}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE bills SET amount = 19 WHERE id = $1`, bill)
	})
	asActor(t, pool, actor{user: bob.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `DELETE FROM notes WHERE id = $1`, note)
		txExec(t, tx, `DELETE FROM bills WHERE id = $1`, bill)
	})

	if n := countRows(t, pool, `SELECT count(*) FROM notifications WHERE type = 'item_changed'`); n != 0 {
		t.Errorf("self/system edits wrote %d notifications", n)
	}
}

// Updates: names only, deduped per record for 10 minutes; a delete always
// notifies; agents are named; non-members are not notified.
func TestChangeNotifications054_UpdatesDedupeAndText(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	mustExec(t, pool, `UPDATE households SET timezone = 'America/Chicago' WHERE id = $1`, f.household)
	bob := newMember(t, pool, f.household, "Bob")
	amber := newMember(t, pool, f.household, "Amber")
	amberAgent := newAgentToken(t, pool, f.household, amber)
	mustExec(t, pool, `UPDATE agent_tokens SET name = 'Kitchen assistant' WHERE id = $1`, amberAgent)
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, f.household)

	var bill, note, secret, list, item, ev uuid.UUID
	asActor(t, pool, actor{user: bob.String()}, func(tx pgx.Tx) {
		bill = txInsertID(t, tx, `INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Netflix', 15) RETURNING id`, f.household)
		note = txInsertID(t, tx, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'vehicle', $2, 'Tires', 'rotate at 5k') RETURNING id`, f.household, f.vehicle)
		secret = txInsertID(t, tx, `INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type) VALUES ($1, 'OLDBYTES', 'iv', 'Wi-Fi', 'password') RETURNING id`, f.household)
		list = txInsertID(t, tx, `INSERT INTO lists (household_id, name) VALUES ($1, 'Groceries') RETURNING id`, f.household)
		item = txInsertID(t, tx, `INSERT INTO list_items (list_id, text) VALUES ($1, 'Milk') RETURNING id`, list)
		txExec(t, tx, `INSERT INTO list_items (list_id, text) VALUES ($1, 'Eggs')`, list)
		// 02:00Z on the 29th is the evening of the 28th in Chicago.
		ev = txInsertID(t, tx, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, 'u2', E'BEGIN:VEVENT\r\nSUMMARY:Movie\r\nDTSTART:20261129T020000Z\r\nEND:VEVENT', 'e1') RETURNING id`, cal)
	})

	asActor(t, pool, actor{user: amber.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE bills SET amount = 16, notes = 'family plan' WHERE id = $1`, bill)
	})
	asActor(t, pool, actor{user: amber.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE bills SET amount = 17 WHERE id = $1`, bill) // deduped
		txExec(t, tx, `UPDATE notes SET body = 'rotate at 6k' WHERE id = $1`, note)
		txExec(t, tx, `UPDATE secrets SET encrypted_data = 'NEWBYTES' WHERE id = $1`, secret)
		txExec(t, tx, `UPDATE list_items SET checked = true WHERE list_id = $1`, list) // two items, one notification
		txExec(t, tx, `UPDATE calendar_objects SET ical_data = replace(ical_data, 'Movie', 'Movie night'), etag = 'e2' WHERE id = $1`, ev)
	})
	asActor(t, pool, actor{user: amber.String(), agentToken: amberAgent.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `DELETE FROM list_items WHERE id = $1`, item) // same list within 10 minutes: deduped
		txExec(t, tx, `DELETE FROM bills WHERE id = $1`, bill)      // deletes always notify
	})

	got := changeNotices(t, pool, bob)
	byTitle := map[string]changeNotice{}
	for _, n := range got {
		byTitle[n.title] = n
	}
	want := map[string]struct {
		body, linkType string
		link           uuid.UUID
	}{
		`Amber updated bill "Netflix"`:                         {"You created this bill. Changed: amount, notes.", "", uuid.Nil}, // link cleared by rehome on delete
		`Amber updated note "Tires"`:                           {"You created this note. Changed: body.", "note", note},
		`Amber updated secret "Wi-Fi"`:                         {"You created this secret. Changed: encrypted data.", "secret", secret},
		`Amber updated list item "Milk"`:                       {"You created this list item. Changed: checked, checked at.", "list", list},
		`Amber updated event "Movie night" on Sat Nov 28`:      {"You created this event. Changed: ical data.", "calendar_event", ev},
		`Amber (via Kitchen assistant) deleted bill "Netflix"`: {"You created this bill.", "", uuid.Nil},
	}
	if len(got) != len(want) {
		t.Errorf("want %d notifications, got %d:", len(want), len(got))
		for _, n := range got {
			t.Logf("  %q / %q", n.title, strp(n.body))
		}
	}
	for title, w := range want {
		n, ok := byTitle[title]
		if !ok {
			t.Errorf("missing %q", title)
			continue
		}
		if strp(n.body) != w.body {
			t.Errorf("%q body = %q, want %q", title, strp(n.body), w.body)
		}
		if w.linkType != "" && (strp(n.entityType) != w.linkType || !eqID(n.entityID, w.link)) {
			t.Errorf("%q link = %s %v, want %s %s", title, strp(n.entityType), n.entityID, w.linkType, w.link)
		}
	}
	for _, n := range got {
		all := n.title + strp(n.body)
		for _, leak := range []string{"rotate", "NEWBYTES", "OLDBYTES", "family plan"} {
			if strings.Contains(all, leak) {
				t.Errorf("value %q leaked into %q / %q", leak, n.title, strp(n.body))
			}
		}
	}

	// The dedupe window is 10 minutes: an older notice no longer suppresses.
	mustExec(t, pool, `UPDATE notifications SET created_at = now() - interval '11 minutes' WHERE user_id = $1 AND title = 'Amber updated note "Tires"'`, bob)
	asActor(t, pool, actor{user: amber.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE notes SET title = 'Tyres' WHERE id = $1`, note)
	})
	var n int
	mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND entity_id = $2`, bob, note), &n)
	if n != 2 {
		t.Errorf("after the window: want 2 note notifications, got %d", n)
	}

	// A creator who has left the family is not notified.
	mustExec(t, pool, `DELETE FROM memberships WHERE household_id = $1 AND user_id = $2`, f.household, bob)
	before := len(changeNotices(t, pool, bob))
	asActor(t, pool, actor{user: amber.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `DELETE FROM secrets WHERE id = $1`, secret)
	})
	if after := len(changeNotices(t, pool, bob)); after != before {
		t.Errorf("former member notified: %d -> %d", before, after)
	}
}

// up/down/up: down stops notifications and keeps the ones already written.
func TestChangeNotifications054_Reversible(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)
	bob := newMember(t, pool, f.household, "Bob")
	amber := newMember(t, pool, f.household, "Amber")

	mkPet := func(name string) uuid.UUID {
		var id uuid.UUID
		asActor(t, pool, actor{user: bob.String()}, func(tx pgx.Tx) {
			id = txInsertID(t, tx, `INSERT INTO pets (household_id, name) VALUES ($1, $2) RETURNING id`, f.household, name)
		})
		return id
	}
	delPet := func(id uuid.UUID) {
		asActor(t, pool, actor{user: amber.String()}, func(tx pgx.Tx) {
			txExec(t, tx, `DELETE FROM pets WHERE id = $1`, id)
		})
	}

	delPet(mkPet("Rex"))
	if got := len(changeNotices(t, pool, bob)); got != 1 {
		t.Fatalf("before down: want 1, got %d", got)
	}
	for cycle := 0; cycle < 2; cycle++ {
		applyMigration(t, pool, "054_change_notifications.down.sql")
		if n := countRows(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname IN ('activity_log_notify_creator', 'calendar_objects_notify_when')`); n != 0 {
			t.Fatalf("cycle %d: %d triggers left", cycle, n)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM pg_proc WHERE proname IN ('notify_entity_creator', 'notify_event_when')`); n != 0 {
			t.Fatalf("cycle %d: %d functions left", cycle, n)
		}
		before := len(changeNotices(t, pool, bob))
		delPet(mkPet("Down"))
		if after := len(changeNotices(t, pool, bob)); after != before {
			t.Fatalf("cycle %d: notified while down", cycle)
		}

		applyMigration(t, pool, "054_change_notifications.up.sql")
		delPet(mkPet("Up"))
		if after := len(changeNotices(t, pool, bob)); after != before+1 {
			t.Fatalf("cycle %d: up did not notify (%d -> %d)", cycle, before, after)
		}
	}
	// 054 down also runs after 050 down (the test down chains do 054 first,
	// but it must tolerate either order).
	applyMigration(t, pool, "054_change_notifications.down.sql")
	applyMigration(t, pool, "050_authorship.down.sql")
	applyMigration(t, pool, "054_change_notifications.down.sql")
}
