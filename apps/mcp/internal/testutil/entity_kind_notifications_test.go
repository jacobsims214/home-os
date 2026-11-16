package testutil

// Tests for migration 070 (#4708): notifications.entity_type and
// calendar_objects.entity_type are entity_kind, with owner checks. See KB
// architecture/domain-model-v2-cleanup-056.md ("070").

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestEntityKind070_ColumnsAndOwnerChecks(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, f.household)

	for _, table := range []string{"notifications", "calendar_objects"} {
		var domain *string
		mustScan(t, pool.QueryRow(ctx, `SELECT domain_name FROM information_schema.columns WHERE table_name = $1 AND column_name = 'entity_type'`, table), &domain)
		if domain == nil || *domain != "entity_kind" {
			t.Errorf("%s.entity_type domain: want entity_kind, got %s", table, strp(domain))
		}
	}

	notify := func(kind *string, id *uuid.UUID) error {
		_, err := pool.Exec(ctx, `INSERT INTO notifications (household_id, type, title, entity_type, entity_id) VALUES ($1, 'x', 'n', $2, $3)`, f.household, kind, id)
		return err
	}
	kind := func(s string) *string { return &s }
	id := func(u uuid.UUID) *uuid.UUID { return &u }

	t.Run("notifications", func(t *testing.T) {
		// #4459: family A's notification about family B's property.
		wantSQLState(t, notify(kind("property"), id(other.property)), sqlstateFKViolation)
		wantSQLState(t, notify(kind("property"), id(uuid.New())), sqlstateFKViolation)
		wantSQLState(t, notify(kind("property"), nil), sqlstateFKViolation)
		wantSQLState(t, notify(kind("list_item"), id(uuid.New())), sqlstateCheckViolation)
		wantSQLState(t, notify(kind("calendar_alarm"), id(uuid.New())), sqlstateCheckViolation)
		if err := notify(nil, nil); err != nil {
			t.Errorf("no entity: %v", err)
		}
		if err := notify(kind("property"), id(f.property)); err != nil {
			t.Errorf("own property: %v", err)
		}
		if err := notify(kind("household"), id(f.household)); err != nil {
			t.Errorf("own household: %v", err)
		}
		if err := notify(kind("calendar"), id(cal)); err != nil {
			t.Errorf("own calendar: %v", err)
		}
		// Moving a notification to another family is refused too.
		n := insertID(t, pool, `INSERT INTO notifications (household_id, type, title, entity_type, entity_id) VALUES ($1, 'x', 'n', 'vehicle', $2) RETURNING id`, f.household, f.vehicle)
		_, err := pool.Exec(ctx, `UPDATE notifications SET household_id = $1 WHERE id = $2`, other.household, n)
		wantSQLState(t, err, sqlstateFKViolation)
		// Marking read does not re-check (the trigger is on the entity columns).
		mustExec(t, pool, `UPDATE notifications SET read_at = NOW() WHERE id = $1`, n)
	})

	t.Run("calendar_objects", func(t *testing.T) {
		ins := func(kind string, id uuid.UUID) error {
			_, err := pool.Exec(ctx, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, entity_type, entity_id) VALUES ($1, $2, '{}', 'e', $3, $4)`, cal, uuid.NewString(), kind, id)
			return err
		}
		wantSQLState(t, ins("property", other.property), sqlstateFKViolation)
		wantSQLState(t, ins("maintenance", uuid.New()), sqlstateCheckViolation)
		if err := ins("property", f.property); err != nil {
			t.Errorf("own property: %v", err)
		}
		task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name) VALUES ($1, 'Gutters') RETURNING id`, f.household)
		if err := ins("maintenance_task", task); err != nil {
			t.Errorf("own task: %v", err)
		}
	})
}

// item_changed (054/061) links only to registered kinds of the same family.
func TestEntityKind070_ChangeNoticeLinks(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	creator := newMember(t, pool, f.household, "Amber")
	dana := newMember(t, pool, f.household, "Dana")

	var list, item, plan, feedback, person, pref, link, pet, cal, ev uuid.UUID
	asActor(t, pool, actor{user: creator.String()}, func(tx pgx.Tx) {
		list = txInsertID(t, tx, `INSERT INTO lists (household_id, name) VALUES ($1, 'Groceries') RETURNING id`, f.household)
		item = txInsertID(t, tx, `INSERT INTO list_items (list_id, text) VALUES ($1, 'Milk') RETURNING id`, list)
		plan = txInsertID(t, tx, `INSERT INTO meal_plans (household_id, date, slot, title) VALUES ($1, '2026-11-20', 'dinner', 'Tacos') RETURNING id`, f.household)
		feedback = txInsertID(t, tx, `INSERT INTO meal_feedback (household_id, meal_plan_id, rating) VALUES ($1, $2, 4) RETURNING id`, f.household, plan)
		person = txInsertID(t, tx, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Kid') RETURNING id`, f.household)
		pref = txInsertID(t, tx, `INSERT INTO person_preferences (household_id, person_id, kind, value) VALUES ($1, $2, 'like', 'pizza') RETURNING id`, f.household, person)
		link = txInsertID(t, tx, `INSERT INTO document_links (household_id, entity_type, entity_id, link_type, url, title) VALUES ($1, 'vehicle', $2, 'url', 'https://example.com/manual', 'Manual') RETURNING id`, f.household, f.vehicle)
		pet = txInsertID(t, tx, `INSERT INTO pets (household_id, name) VALUES ($1, 'Rex') RETURNING id`, f.household)
		cal = txInsertID(t, tx, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, f.household)
		ev = txInsertID(t, tx, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, 'ev1', '{"title":"Dinner","start":"2026-11-20T18:00:00Z"}', 'e') RETURNING id`, cal)
	})

	asActor(t, pool, actor{user: dana.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE list_items SET checked = true WHERE id = $1`, item)
		txExec(t, tx, `UPDATE meal_feedback SET rating = 5 WHERE id = $1`, feedback)
		txExec(t, tx, `UPDATE person_preferences SET value = 'pasta' WHERE id = $1`, pref)
		txExec(t, tx, `UPDATE document_links SET title = 'Owner manual' WHERE id = $1`, link)
		txExec(t, tx, `UPDATE calendar_objects SET ical_data = '{"title":"Late dinner","start":"2026-11-20T19:00:00Z"}' WHERE id = $1`, ev)
	})
	// Deletes: a pet has no parent (the row is gone, so no link); an event
	// links to its calendar.
	asActor(t, pool, actor{user: dana.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `DELETE FROM pets WHERE id = $1`, pet)
		txExec(t, tx, `DELETE FROM calendar_objects WHERE id = $1`, ev)
	})

	type ref struct {
		kind string // "<nil>" for no link
		id   *uuid.UUID
	}
	want := map[string]ref{
		"list item":         {"list", &list},
		"meal feedback":     {"meal_plan", &plan},
		"person preference": {"person", &person},
		"document link":     {"document_link", &link},
		"pet":               {"<nil>", nil},
	}
	got := changeNotices(t, pool, creator)
	seen := map[string]bool{}
	for _, n := range got {
		for label, w := range want {
			if !strings.Contains(n.title, "updated "+label) && !strings.Contains(n.title, "deleted "+label) {
				continue
			}
			seen[label] = true
			if strp(n.entityType) != w.kind ||
				(w.id == nil) != (n.entityID == nil) || (w.id != nil && *w.id != *n.entityID) {
				t.Errorf("%q: want (%s, %v), got (%s, %v)", n.title, w.kind, w.id, strp(n.entityType), n.entityID)
			}
		}
	}
	for label := range want {
		if !seen[label] {
			t.Errorf("no notice for %s; got %+v", label, got)
		}
	}
	var evKinds []string
	rows, err := pool.Query(ctx, `SELECT COALESCE(entity_type::text, '<nil>') || ':' || COALESCE(entity_id::text, '') FROM notifications WHERE user_id = $1 AND title LIKE '%event%' ORDER BY created_at, title`, creator)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		evKinds = append(evKinds, s)
	}
	rows.Close()
	// The update's link to the event was cleared by the event's delete
	// (rehome_entity_refs('calendar_event')); the delete links to the calendar.
	if len(evKinds) != 2 || evKinds[0] != "<nil>:" || evKinds[1] != "calendar:"+cal.String() {
		t.Errorf("event notices: want [<nil>: calendar:%s], got %v", cal, evKinds)
	}
}

// Deleting a calendar, an event or a document link clears what pointed at
// it, and a household still deletes cleanly.
func TestEntityKind070_Rehome(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, f.household)
	ev := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, 'ev1', '{}', 'e') RETURNING id`, cal)
	link := insertID(t, pool, `INSERT INTO document_links (household_id, entity_type, entity_id, link_type, url) VALUES ($1, 'vehicle', $2, 'url', 'https://example.com') RETURNING id`, f.household, f.vehicle)
	nEv := insertID(t, pool, `INSERT INTO notifications (household_id, type, title, entity_type, entity_id) VALUES ($1, 'x', 'ev', 'calendar_event', $2) RETURNING id`, f.household, ev)
	nCal := insertID(t, pool, `INSERT INTO notifications (household_id, type, title, entity_type, entity_id) VALUES ($1, 'x', 'cal', 'calendar', $2) RETURNING id`, f.household, cal)
	nLink := insertID(t, pool, `INSERT INTO notifications (household_id, type, title, entity_type, entity_id) VALUES ($1, 'x', 'link', 'document_link', $2) RETURNING id`, f.household, link)
	evNote := insertID(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'calendar_event', $2, 'Bring wine', 'x') RETURNING id`, f.household, ev)

	cleared := func(id uuid.UUID) bool {
		return countRows(t, pool, `SELECT count(*) FROM notifications WHERE id = $1 AND entity_type IS NULL AND entity_id IS NULL`, id) == 1
	}
	mustExec(t, pool, `DELETE FROM calendar_objects WHERE id = $1`, ev)
	if !cleared(nEv) {
		t.Error("event delete left the notification's reference")
	}
	if countRows(t, pool, `SELECT count(*) FROM notes WHERE id = $1`, evNote) != 0 {
		t.Error("event delete left the note on it")
	}
	mustExec(t, pool, `DELETE FROM document_links WHERE id = $1`, link)
	if !cleared(nLink) {
		t.Error("document link delete left the notification's reference")
	}
	mustExec(t, pool, `DELETE FROM calendars WHERE id = $1`, cal)
	if !cleared(nCal) {
		t.Error("calendar delete left the notification's reference")
	}

	// A whole family, with events and notifications about them.
	cal2 := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, f.household)
	ev2 := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, entity_type, entity_id) VALUES ($1, 'ev2', '{}', 'e', 'vehicle', $2) RETURNING id`, cal2, f.vehicle)
	mustExec(t, pool, `INSERT INTO notifications (household_id, type, title, entity_type, entity_id) VALUES ($1, 'x', 'ev2', 'calendar_event', $2)`, f.household, ev2)
	if _, err := pool.Exec(ctx, `DELETE FROM households WHERE id = $1`, f.household); err != nil {
		t.Fatalf("household delete: %v", err)
	}
}

// Down then up: rows written in the old shapes while 070 is down are
// repaired by up.
func TestEntityKind070_Reversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, f.household)
	ev := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, 'ev1', '{}', 'e') RETURNING id`, cal)
	// A note on an event goes on down (kind 'calendar_event' leaves the
	// registry); a file on one becomes family-level.
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	note := insertID(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'calendar_event', $2, 'n', 'x') RETURNING id`, f.household, ev)
	file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, 'a.pdf', 1, 'calendar', $3) RETURNING id`, f.household, blob, cal)

	applyMigration(t, pool, "070_entity_kind_notifications.down.sql")

	if countRows(t, pool, `SELECT count(*) FROM notes WHERE id = $1`, note) != 0 {
		t.Error("down kept a note on an event")
	}
	if countRows(t, pool, `SELECT count(*) FROM files WHERE id = $1 AND entity_type = 'household' AND entity_id = $2`, file, f.household) != 1 {
		t.Error("down did not re-home a file on a calendar")
	}
	var domain *string
	mustScan(t, pool.QueryRow(ctx, `SELECT domain_name FROM information_schema.columns WHERE table_name = 'notifications' AND column_name = 'entity_type'`), &domain)
	if domain != nil {
		t.Errorf("down: notifications.entity_type still %s", *domain)
	}

	task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name) VALUES ($1, 'Gutters') RETURNING id`, f.household)
	list := insertID(t, pool, `INSERT INTO lists (household_id, name) VALUES ($1, 'Groceries') RETURNING id`, f.household)
	item := insertID(t, pool, `INSERT INTO list_items (list_id, text) VALUES ($1, 'Milk') RETURNING id`, list)
	maintEv := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, entity_type, entity_id) VALUES ($1, 'm', '{}', 'e', 'maintenance', $2) RETURNING id`, cal, task)
	crossEv := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, entity_type, entity_id) VALUES ($1, 'x', '{}', 'e', 'property', $2) RETURNING id`, cal, other.property)
	nItem := insertID(t, pool, `INSERT INTO notifications (household_id, type, title, entity_type, entity_id) VALUES ($1, 'x', 'i', 'list_item', $2) RETURNING id`, f.household, item)
	nCross := insertID(t, pool, `INSERT INTO notifications (household_id, type, title, entity_type, entity_id) VALUES ($1, 'x', 'c', 'property', $2) RETURNING id`, f.household, other.property)
	nMaint := insertID(t, pool, `INSERT INTO notifications (household_id, type, title, entity_type, entity_id) VALUES ($1, 'x', 'm', 'maintenance', $2) RETURNING id`, f.household, task)
	nHalf := insertID(t, pool, `INSERT INTO notifications (household_id, type, title, entity_type) VALUES ($1, 'x', 'h', 'bill') RETURNING id`, f.household)

	applyMigration(t, pool, "070_entity_kind_notifications.up.sql")

	ref := func(table string, id uuid.UUID) string {
		var s string
		mustScan(t, pool.QueryRow(ctx, `SELECT COALESCE(entity_type::text, '<nil>') || ':' || COALESCE(entity_id::text, '') FROM `+table+` WHERE id = $1`, id), &s)
		return s
	}
	for _, c := range []struct {
		table string
		id    uuid.UUID
		want  string
	}{
		{"calendar_objects", maintEv, "maintenance_task:" + task.String()},
		{"calendar_objects", crossEv, "<nil>:"},
		{"notifications", nItem, "list:" + list.String()},
		{"notifications", nCross, "<nil>:"},
		{"notifications", nMaint, "maintenance_task:" + task.String()},
		{"notifications", nHalf, "<nil>:"},
	} {
		if got := ref(c.table, c.id); got != c.want {
			t.Errorf("%s %s after up: want %s, got %s", c.table, c.id, c.want, got)
		}
	}
	// And the maintenance pair still deletes together.
	mustExec(t, pool, `DELETE FROM maintenance_tasks WHERE id = $1`, task)
	if countRows(t, pool, `SELECT count(*) FROM calendar_objects WHERE id = $1`, maintEv) != 0 {
		t.Error("task delete left its event")
	}
}
