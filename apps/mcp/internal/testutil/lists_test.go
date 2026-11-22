package testutil

// Tests for migration 046 (shared lists). See KB decisions/domain-model-v2.md,
// "046 lists".

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Up/down/up with lists in place: down removes the tables and re-homes files
// about a list; up again works and the narrower entity_kind rejects 'list'
// while 046 is down.
func TestLists046_Reversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)

	list := insertID(t, pool, `INSERT INTO lists (household_id, name, kind) VALUES ($1, 'Groceries', 'grocery') RETURNING id`, f.household)
	mustExec(t, pool, `INSERT INTO list_items (list_id, text) VALUES ($1, 'Milk'), ($1, 'Eggs')`, list)
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, 'coupon.pdf', 1, 'list', $3) RETURNING id`, f.household, blob, list)

	// 047 and 049 replace entity_household() on top of 046, and 050 adds
	// authorship on top of all of them; undo them first.
	applyMigration(t, pool, "074_legacy_bill_events_reconcile.down.sql") // newest first: 074 sits on top of 073
	applyMigration(t, pool, "073_legacy_bill_events.down.sql")           // newest first: 073 sits on top of everything below
	applyMigration(t, pool, "071_person_color.down.sql")                 // newest first: drops people.color and its palette CHECK
	applyMigration(t, pool, "070_entity_kind_notifications.down.sql")    // newest first: notifications/calendar_objects entity_type back to VARCHAR
	applyMigration(t, pool, "069_drop_finance_columns.down.sql")         // newest first: restores the property/vehicle finance columns
	applyMigration(t, pool, "068_guide_revision_change_note.down.sql")   // newest first: drops guide_revisions.change_note
	applyMigration(t, pool, "067_invitation_vendor_link.down.sql")       // newest first: 067's invitations FK needs 043's vendors key
	applyMigration(t, pool, "066_list_item_checked_by.down.sql")         // newest first: drops list_items.checked_by
	applyMigration(t, pool, "065_vendor_address.down.sql")               // newest first: moves vendors.address back into notes
	applyMigration(t, pool, "064_membership_vendor_link.down.sql")       // newest first: 064's memberships FK needs 043's vendors key
	applyMigration(t, pool, "061_notify_creator_role.down.sql")          // newest first: restores 054's notify_entity_creator
	applyMigration(t, pool, "060_drop_pet_vet_columns.down.sql")         // newest first: restores pets.vet_name / vet_phone
	applyMigration(t, pool, "091_family_composite_fks.down.sql")         // newest first: since 091 its composite FKs ride on 058/084's keys
	applyMigration(t, pool, "058_same_family_fks.down.sql")              // newest first: 058's FKs need 043's vendors key
	applyMigration(t, pool, "057_secret_agent_share.down.sql")
	applyMigration(t, pool, "056_domain_v2_cleanup.down.sql") // newest first: 056 sits on top of 038-054
	applyMigration(t, pool, "055_activity_event_titles.down.sql")
	applyMigration(t, pool, "054_change_notifications.down.sql") // its trigger is on 050's activity_log
	applyMigration(t, pool, "050_authorship.down.sql")
	applyMigration(t, pool, "049_guides.down.sql")
	applyMigration(t, pool, "047_meals.down.sql")
	applyMigration(t, pool, "046_lists.down.sql")
	if n := countRows(t, pool, `SELECT count(*) FROM pg_tables WHERE tablename IN ('lists', 'list_items')`); n != 0 {
		t.Fatalf("down: %d list tables left", n)
	}
	var kind string
	mustScan(t, pool.QueryRow(ctx, `SELECT entity_type FROM files WHERE id = $1`, file), &kind)
	if kind != "household" {
		t.Errorf("down: file about a list should be re-homed, got %s", kind)
	}
	_, err := pool.Exec(ctx, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'list', $1, 'x')`, f.household)
	wantSQLState(t, err, sqlstateCheckViolation)
	// 'person' and 'budget' survive the down.
	person := insertID(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Sam') RETURNING id`, f.household)
	mustExec(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'person', $2, 'ok')`, f.household, person)

	applyMigration(t, pool, "046_lists.up.sql")
	list = insertID(t, pool, `INSERT INTO lists (household_id, name) VALUES ($1, 'Again') RETURNING id`, f.household)
	mustExec(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'list', $2, 'ok')`, f.household, list)
	applyMigration(t, pool, "046_lists.down.sql")
	applyMigration(t, pool, "046_lists.up.sql")
	applyMigration(t, pool, "047_meals.up.sql")
	applyMigration(t, pool, "049_guides.up.sql")
	applyMigration(t, pool, "050_authorship.up.sql")
}

// Defaults, same-family rules, rehome on delete and the item triggers.
func TestLists046_Rules(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)

	// A list without a subject is family-level; kind defaults to 'other'.
	list := insertID(t, pool, `INSERT INTO lists (household_id, name) VALUES ($1, 'Chores') RETURNING id`, f.household)
	var kind, entityType string
	var entityID uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT kind, entity_type, entity_id FROM lists WHERE id = $1`, list), &kind, &entityType, &entityID)
	if kind != "other" || entityType != "household" || entityID != f.household {
		t.Errorf("defaults: got kind=%s entity=(%s, %s)", kind, entityType, entityID)
	}

	_, err := pool.Exec(ctx, `INSERT INTO lists (household_id, name, kind) VALUES ($1, 'x', 'wishlist')`, f.household)
	wantSQLState(t, err, sqlstateCheckViolation)
	_, err = pool.Exec(ctx, `INSERT INTO lists (household_id, name) VALUES ($1, '  ')`, f.household)
	wantSQLState(t, err, sqlstateCheckViolation)

	// About one entity: same family only.
	packing := insertID(t, pool, `INSERT INTO lists (household_id, name, kind, entity_type, entity_id) VALUES ($1, 'Road trip', 'packing', 'vehicle', $2) RETURNING id`, f.household, f.vehicle)
	_, err = pool.Exec(ctx, `INSERT INTO lists (household_id, name, entity_type, entity_id) VALUES ($1, 'x', 'vehicle', $2)`, other.household, f.vehicle)
	wantSQLState(t, err, sqlstateFKViolation)
	_, err = pool.Exec(ctx, `INSERT INTO lists (household_id, name, entity_type, entity_id) VALUES ($1, 'x', 'household', $2)`, other.household, f.household)
	wantSQLState(t, err, sqlstateFKViolation)

	// Deleting the subject makes the list family-level, items intact.
	mustExec(t, pool, `INSERT INTO list_items (list_id, text) VALUES ($1, 'Sunscreen')`, packing)
	mustExec(t, pool, `DELETE FROM vehicles WHERE id = $1`, f.vehicle)
	mustScan(t, pool.QueryRow(ctx, `SELECT entity_type, entity_id FROM lists WHERE id = $1`, packing), &entityType, &entityID)
	if entityType != "household" || entityID != f.household {
		t.Errorf("rehome: want ('household', %s), got (%s, %s)", f.household, entityType, entityID)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM list_items WHERE list_id = $1`, packing); n != 1 {
		t.Errorf("rehome: items lost, got %d", n)
	}

	// Items: household_id from the list, position appended in order.
	var a, b, c uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO list_items (list_id, text) VALUES ($1, 'Mop') RETURNING id`, list), &a)
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO list_items (list_id, text) VALUES ($1, 'Vacuum') RETURNING id`, list), &b)
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO list_items (list_id, text, position) VALUES ($1, 'Dust', 0.5) RETURNING id`, list), &c)
	var hh uuid.UUID
	var pa, pb float64
	mustScan(t, pool.QueryRow(ctx, `SELECT household_id, position FROM list_items WHERE id = $1`, a), &hh, &pa)
	mustScan(t, pool.QueryRow(ctx, `SELECT position FROM list_items WHERE id = $1`, b), &pb)
	if hh != f.household || pa != 0 || pb != 1 {
		t.Errorf("items: household=%s positions %v %v", hh, pa, pb)
	}
	var order []uuid.UUID
	rows, err := pool.Query(ctx, `SELECT id FROM list_items WHERE list_id = $1 ORDER BY position, created_at, id`, list)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		order = append(order, id)
	}
	rows.Close()
	if len(order) != 3 || order[0] != a || order[1] != c || order[2] != b {
		t.Errorf("order: got %v, want [%s %s %s]", order, a, c, b)
	}

	// Cross-family items are rejected (composite FK).
	_, err = pool.Exec(ctx, `INSERT INTO list_items (list_id, household_id, text) VALUES ($1, $2, 'x')`, list, other.household)
	wantSQLState(t, err, sqlstateFKViolation)
	_, err = pool.Exec(ctx, `INSERT INTO list_items (list_id, text, quantity) VALUES ($1, 'x', 0)`, list)
	wantSQLState(t, err, sqlstateCheckViolation)

	// checked_at follows checked; updated_at moves on UPDATE.
	var createdAt, updatedAt time.Time
	var checkedAt *time.Time
	mustScan(t, pool.QueryRow(ctx, `UPDATE list_items SET checked = true WHERE id = $1 RETURNING checked_at, created_at, updated_at`, a), &checkedAt, &createdAt, &updatedAt)
	if checkedAt == nil || !updatedAt.After(createdAt) {
		t.Errorf("check: checked_at=%v created=%v updated=%v", checkedAt, createdAt, updatedAt)
	}
	mustScan(t, pool.QueryRow(ctx, `UPDATE list_items SET checked = false WHERE id = $1 RETURNING checked_at`, a), &checkedAt)
	if checkedAt != nil {
		t.Errorf("uncheck: checked_at should be NULL, got %v", checkedAt)
	}
	_, err = pool.Exec(ctx, `INSERT INTO list_items (list_id, text, checked, checked_at) VALUES ($1, 'x', false, NOW())`, list)
	if err != nil {
		t.Errorf("unchecked item with checked_at should be normalised, got %v", err)
	}

	// Assignee: same family only; deleting the person keeps the item.
	kid := insertID(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Sam') RETURNING id`, f.household)
	otherKid := insertID(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Jo') RETURNING id`, other.household)
	mustExec(t, pool, `UPDATE list_items SET assignee_person_id = $2 WHERE id = $1`, b, kid)
	_, err = pool.Exec(ctx, `UPDATE list_items SET assignee_person_id = $2 WHERE id = $1`, c, otherKid)
	wantSQLState(t, err, sqlstateFKViolation)
	mustExec(t, pool, `DELETE FROM people WHERE id = $1`, kid)
	var assignee *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT assignee_person_id FROM list_items WHERE id = $1`, b), &assignee)
	if assignee != nil {
		t.Errorf("person delete: assignee should be NULL, got %v", assignee)
	}

	// Kind 'list': notes about it, deleted with it; items cascade.
	note := insertID(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'list', $2, 'Costco run') RETURNING id`, f.household, list)
	_, err = pool.Exec(ctx, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'list', $2, 'x')`, other.household, list)
	wantSQLState(t, err, sqlstateFKViolation)
	mustExec(t, pool, `DELETE FROM lists WHERE id = $1`, list)
	if n := countRows(t, pool, `SELECT count(*) FROM notes WHERE id = $1`, note); n != 0 {
		t.Error("note about a deleted list survived")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM list_items WHERE list_id = $1`, list); n != 0 {
		t.Errorf("items of a deleted list survived: %d", n)
	}

	// Deleting the family removes its lists.
	mustExec(t, pool, `DELETE FROM households WHERE id = $1`, f.household)
	if n := countRows(t, pool, `SELECT count(*) FROM lists WHERE household_id = $1`, f.household); n != 0 {
		t.Errorf("family delete left %d lists", n)
	}
}

// Concurrent check-offs of different items on one list both land: items are
// rows, so neither write overwrites the other.
func TestLists046_ConcurrentCheckOffs(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	list := insertID(t, pool, `INSERT INTO lists (household_id, name, kind) VALUES ($1, 'Groceries', 'grocery') RETURNING id`, f.household)
	const n = 20
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = insertID(t, pool, `INSERT INTO list_items (list_id, text) VALUES ($1, 'item') RETURNING id`, list)
	}

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for _, id := range ids {
		wg.Add(1)
		go func(id uuid.UUID) {
			defer wg.Done()
			_, err := pool.Exec(ctx, `UPDATE list_items SET checked = true WHERE id = $1`, id)
			errs <- err
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := countRows(t, pool, `SELECT count(*) FROM list_items WHERE list_id = $1 AND checked`, list); got != n {
		t.Errorf("want %d checked items, got %d", n, got)
	}
}
