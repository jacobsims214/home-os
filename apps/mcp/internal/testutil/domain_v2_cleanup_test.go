package testutil

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// Migration 056 (#4601): domain model v2 cleanup. It drops the legacy
// columns, adds the deferred constraints and moves calendar alarm dedupe keys
// to notifications.dedupe_key.

const sqlstateNotNullViolation = "23502"

func TestDomainV2Cleanup056_Schema(t *testing.T) {
	pool := NewPool(t)
	for _, c := range [][2]string{
		{"bills", "entity_type"}, {"bills", "entity_id"},
		{"loans", "entity_type"}, {"loans", "entity_id"},
		{"vendors", "property_id"}, {"document_links", "document_id"},
	} {
		if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`, c[0], c[1]); n != 0 {
			t.Errorf("%s.%s still exists", c[0], c[1])
		}
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_tables WHERE tablename = 'integrations'`); n != 0 {
		t.Error("integrations table still exists")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_type WHERE typname IN ('integration_type', 'integration_status')`); n != 0 {
		t.Error("integration enum types still exist")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_loans_entity'`); n != 0 {
		t.Error("idx_loans_entity still exists")
	}
}

func TestDomainV2Cleanup056_Constraints(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)

	t.Run("files and secrets without an entity are family-level", func(t *testing.T) {
		blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
		file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size) VALUES ($1, $2, 'loose.pdf', 1) RETURNING id`, f.household, blob)
		secret := insertID(t, pool, `INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type) VALUES ($1, 'x', 'y', 'wifi', 'password') RETURNING id`, f.household)
		for table, id := range map[string]uuid.UUID{"files": file, "secrets": secret} {
			var kind string
			var entity uuid.UUID
			mustScan(t, pool.QueryRow(ctx, `SELECT entity_type::text, entity_id FROM `+table+` WHERE id = $1`, id), &kind, &entity)
			if kind != "household" || entity != f.household {
				t.Errorf("%s: entity = %s %s, want household %s", table, kind, entity, f.household)
			}
		}
		// Clearing the entity on update re-homes to the family too.
		mustExec(t, pool, `UPDATE files SET entity_type = 'property', entity_id = $2 WHERE id = $1`, file, f.property)
		mustExec(t, pool, `UPDATE files SET entity_type = NULL, entity_id = NULL WHERE id = $1`, file)
		if n := countRows(t, pool, `SELECT count(*) FROM files WHERE id = $1 AND entity_type = 'household'`, file); n != 1 {
			t.Error("unlinking a file did not make it family-level")
		}
		// Half an entity is still refused (by the owner trigger, before the
		// NOT NULL check is reached).
		if _, err := pool.Exec(ctx, `UPDATE files SET entity_id = NULL WHERE id = $1`, file); err == nil {
			t.Error("a file with entity_type but no entity_id was accepted")
		}
	})

	t.Run("rooms need a family", func(t *testing.T) {
		if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'rooms' AND column_name = 'household_id' AND is_nullable = 'NO'`); n != 1 {
			t.Error("rooms.household_id is nullable")
		}
		// The 038 trigger still fills it from the property.
		room := insertID(t, pool, `INSERT INTO rooms (property_id, name) VALUES ($1, 'Den') RETURNING id`, f.property)
		if n := countRows(t, pool, `SELECT count(*) FROM rooms WHERE id = $1 AND household_id = $2`, room, f.household); n != 1 {
			t.Error("room did not get its household")
		}
	})

	t.Run("maintenance: a vehicle excludes property and asset", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO maintenance_tasks (household_id, name, vehicle_id, property_id) VALUES ($1, 'Oil', $2, $3)`,
			f.household, f.vehicle, f.property)
		wantSQLState(t, err, sqlstateCheckViolation)
		_, err = pool.Exec(ctx, `INSERT INTO maintenance_schedules (household_id, name, rrule, vehicle_id, property_id) VALUES ($1, 'Oil', 'FREQ=MONTHLY', $2, $3)`,
			f.household, f.vehicle, f.property)
		wantSQLState(t, err, sqlstateCheckViolation)
		mustExec(t, pool, `INSERT INTO maintenance_tasks (household_id, name, vehicle_id) VALUES ($1, 'Oil', $2)`, f.household, f.vehicle)
	})

	t.Run("links: file, secret or url only, always with a family", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO document_links (entity_type, entity_id, link_type, url) VALUES ('vehicle', $1, 'paperless', 'https://p/1')`, f.vehicle)
		wantSQLState(t, err, sqlstateCheckViolation)
		// No link_type default any more.
		_, err = pool.Exec(ctx, `INSERT INTO document_links (entity_type, entity_id, url) VALUES ('vehicle', $1, 'https://p/1')`, f.vehicle)
		wantSQLState(t, err, sqlstateNotNullViolation)
		// household_id is still filled from the entity by the owner trigger.
		link := insertID(t, pool, `INSERT INTO document_links (entity_type, entity_id, link_type, url) VALUES ('vehicle', $1, 'url', 'https://p/1') RETURNING id`, f.vehicle)
		if n := countRows(t, pool, `SELECT count(*) FROM document_links WHERE id = $1 AND household_id = $2`, link, f.household); n != 1 {
			t.Error("link did not get its household")
		}
	})
}

func TestDomainV2Cleanup056_AlarmDedupeAndEventLink(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	user := newUser(t, pool, "Alex")
	mustExec(t, pool, `INSERT INTO memberships (household_id, user_id) VALUES ($1, $2)`, f.household, user)
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, f.household)
	ev := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, 'dentist', '{}', 'e') RETURNING id`, cal)

	insertAlarm := `INSERT INTO notifications (household_id, user_id, type, title, entity_type, entity_id, dedupe_key)
		VALUES ($1, $2, 'calendar_alarm', 'Dentist', 'calendar_event', $3, 'k1')
		ON CONFLICT (user_id, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING`
	tag, err := pool.Exec(ctx, insertAlarm, f.household, user, ev)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("first alarm: %v (%d rows)", err, tag.RowsAffected())
	}
	tag, err = pool.Exec(ctx, insertAlarm, f.household, user, ev)
	if err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("duplicate alarm: %v (%d rows), want skipped", err, tag.RowsAffected())
	}
	// Family-wide rows (user_id NULL) and rows without a key are not limited.
	for i := 0; i < 2; i++ {
		mustExec(t, pool, `INSERT INTO notifications (household_id, type, title, dedupe_key) VALUES ($1, 'info', 'x', 'k1')`, f.household)
		mustExec(t, pool, `INSERT INTO notifications (household_id, user_id, type, title) VALUES ($1, $2, 'info', 'x')`, f.household, user)
	}
	_, err = pool.Exec(ctx, `INSERT INTO notifications (household_id, type, title, dedupe_key) VALUES ($1, 'info', 'x', ' ')`, f.household)
	wantSQLState(t, err, sqlstateCheckViolation)

	// Deleting the event clears the link and keeps the reminder.
	mustExec(t, pool, `DELETE FROM calendar_objects WHERE id = $1`, ev)
	if n := countRows(t, pool, `SELECT count(*) FROM notifications
		WHERE type = 'calendar_alarm' AND entity_type IS NULL AND entity_id IS NULL AND dedupe_key = 'k1'`); n != 1 {
		t.Errorf("after event delete: %d cleared alarm rows, want 1", n)
	}
	// Deleting the family still cascades cleanly through the trigger.
	mustExec(t, pool, `DELETE FROM households WHERE id = $1`, f.household)
	if n := countRows(t, pool, `SELECT count(*) FROM notifications WHERE household_id = $1`, f.household); n != 0 {
		t.Errorf("%d notifications left after household delete", n)
	}
}

// TestDomainV2Cleanup056_BackfillAndReversible goes down to 054's schema,
// writes rows the way pre-v2 code did, and checks that 056 up carries them
// onto the typed columns before dropping the legacy ones, that down refills
// the legacy columns, and that up again lands in the same state.
func TestDomainV2Cleanup056_BackfillAndReversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	user := newUser(t, pool, "Alex")
	mustExec(t, pool, `INSERT INTO memberships (household_id, user_id) VALUES ($1, $2)`, f.household, user)
	pet := insertID(t, pool, `INSERT INTO pets (household_id, name) VALUES ($1, 'Rex') RETURNING id`, f.household)
	asset := insertID(t, pool, `INSERT INTO assets (household_id, room_id, name) VALUES ($1, $2, 'Fridge') RETURNING id`, f.household, f.room)

	applyMigration(t, pool, "065_vendor_address.down.sql")         // newest first: moves vendors.address back into notes
	applyMigration(t, pool, "064_membership_vendor_link.down.sql") // newest first: 064's memberships FK needs 043's vendors key
	applyMigration(t, pool, "061_notify_creator_role.down.sql")    // newest first: restores 054's notify_entity_creator
	applyMigration(t, pool, "060_drop_pet_vet_columns.down.sql")   // newest first: restores pets.vet_name / vet_phone
	applyMigration(t, pool, "058_same_family_fks.down.sql")        // newest first: 058's FKs need 043's vendors key
	applyMigration(t, pool, "057_secret_agent_share.down.sql")
	applyMigration(t, pool, "056_domain_v2_cleanup.down.sql")

	billVehicle := insertID(t, pool, `INSERT INTO bills (household_id, name, entity_type, entity_id) VALUES ($1, 'Car wash', 'vehicle', $2) RETURNING id`, f.household, f.vehicle)
	billAsset := insertID(t, pool, `INSERT INTO bills (household_id, name, entity_type, entity_id) VALUES ($1, 'Fridge plan', 'asset', $2) RETURNING id`, f.household, asset)
	billPet := insertID(t, pool, `INSERT INTO bills (household_id, name, entity_type, entity_id) VALUES ($1, 'Pet insurance', 'pet', $2) RETURNING id`, f.household, pet)
	billNone := insertID(t, pool, `INSERT INTO bills (household_id, name) VALUES ($1, 'Netflix') RETURNING id`, f.household)
	loanProperty := insertID(t, pool, `INSERT INTO loans (household_id, name, entity_type, entity_id, original_amount, remaining_balance) VALUES ($1, 'Mortgage', 'property', $2, 1, 1) RETURNING id`, f.household, f.property)
	loanNone := insertID(t, pool, `INSERT INTO loans (household_id, name, original_amount, remaining_balance) VALUES ($1, 'Personal', 1, 1) RETURNING id`, f.household)
	vendor := insertID(t, pool, `INSERT INTO vendors (household_id, property_id, name) VALUES ($1, $2, 'Plumber') RETURNING id`, f.household, f.property)
	task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, vehicle_id, property_id) VALUES ($1, 'Oil', $2, $3) RETURNING id`, f.household, f.vehicle, f.property)
	alarmKey := uuid.New()
	alarm := insertID(t, pool, `INSERT INTO notifications (household_id, user_id, type, title, entity_type, entity_id) VALUES ($1, $2, 'calendar_alarm', 'Dentist', 'calendar_alarm', $3) RETURNING id`, f.household, user, alarmKey)
	linkKept := insertID(t, pool, `INSERT INTO document_links (entity_type, entity_id, link_type, link_id, url) VALUES ('vehicle', $1, 'paperless', '12', 'https://paperless/12') RETURNING id`, f.vehicle)
	linkDropped := insertID(t, pool, `INSERT INTO document_links (entity_type, entity_id, link_type, link_id) VALUES ('vehicle', $1, 'minio', 'key') RETURNING id`, f.vehicle)
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	// The owner trigger allowed a NULL entity before 056; the 039 file
	// backfill already ran, so this row is what pre-v2 code writes now.
	file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size) VALUES ($1, $2, 'loose.pdf', 1) RETURNING id`, f.household, blob)
	mustExec(t, pool, `INSERT INTO integrations (household_id, type) VALUES ($1, 'homeassistant')`, f.household)

	applyMigration(t, pool, "056_domain_v2_cleanup.up.sql")
	// Back to the head, so the down below is newest-first again (057 down
	// is not idempotent).
	applyMigration(t, pool, "057_secret_agent_share.up.sql")
	applyMigration(t, pool, "058_same_family_fks.up.sql")
	applyMigration(t, pool, "060_drop_pet_vet_columns.up.sql")
	applyMigration(t, pool, "064_membership_vendor_link.up.sql")
	applyMigration(t, pool, "065_vendor_address.up.sql")

	type subject struct{ property, vehicle, asset *uuid.UUID }
	subjectOf := func(table string, id uuid.UUID) subject {
		t.Helper()
		var s subject
		mustScan(t, pool.QueryRow(ctx, `SELECT property_id, vehicle_id, asset_id FROM `+table+` WHERE id = $1`, id), &s.property, &s.vehicle, &s.asset)
		return s
	}
	is := func(p *uuid.UUID, want uuid.UUID) bool { return p != nil && *p == want }

	if s := subjectOf("bills", billVehicle); !is(s.vehicle, f.vehicle) || s.property != nil {
		t.Errorf("vehicle bill: %+v", s)
	}
	if s := subjectOf("bills", billAsset); !is(s.asset, asset) || !is(s.property, f.property) {
		t.Errorf("asset bill: want the asset and its property, got %+v", s)
	}
	for name, id := range map[string]uuid.UUID{"pet": billPet, "none": billNone} {
		if s := subjectOf("bills", id); s.property != nil || s.vehicle != nil || s.asset != nil {
			t.Errorf("%s bill: want family level, got %+v", name, s)
		}
	}
	if n := countRows(t, pool, `SELECT count(*) FROM notes WHERE entity_type = 'bill' AND entity_id = $1 AND title = 'Earlier link (migration 039)'`, billPet); n != 1 {
		t.Errorf("pet bill link note: %d, want 1", n)
	}
	if s := subjectOf("loans", loanProperty); !is(s.property, f.property) {
		t.Errorf("property loan: %+v", s)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM notes WHERE entity_type = 'vendor' AND entity_id = $1 AND title = 'Serves property' AND body = 'Main house'`, vendor); n != 1 {
		t.Errorf("vendor property note: %d, want 1", n)
	}
	if s := subjectOf("maintenance_tasks", task); !is(s.vehicle, f.vehicle) || s.property != nil {
		t.Errorf("maintenance task: want vehicle only, got %+v", s)
	}
	var key *string
	var entityType *string
	mustScan(t, pool.QueryRow(ctx, `SELECT dedupe_key, entity_type FROM notifications WHERE id = $1`, alarm), &key, &entityType)
	if key == nil || *key != alarmKey.String() || entityType != nil {
		t.Errorf("alarm: dedupe_key %v entity_type %v, want key %s and no entity", key, entityType, alarmKey)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM document_links WHERE id = $1 AND link_type = 'url' AND household_id = $2`, linkKept, f.household); n != 1 {
		t.Error("paperless link with a URL was not kept as a url link")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM document_links WHERE id = $1`, linkDropped); n != 0 {
		t.Error("minio link without a URL was kept")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM files WHERE id = $1 AND entity_type = 'household' AND entity_id = $2`, file, f.household); n != 1 {
		t.Error("file without an entity was not made family-level")
	}

	countAll := func() int {
		t.Helper()
		return countRows(t, pool, `SELECT (SELECT count(*) FROM bills) + (SELECT count(*) FROM loans) + (SELECT count(*) FROM notes)
			+ (SELECT count(*) FROM vendors) + (SELECT count(*) FROM notifications) + (SELECT count(*) FROM document_links)
			+ (SELECT count(*) FROM files) + (SELECT count(*) FROM maintenance_tasks)`)
	}
	before := countAll()

	// Down: the legacy columns come back, filled from the typed FKs.
	applyMigration(t, pool, "065_vendor_address.down.sql")         // newest first: moves vendors.address back into notes
	applyMigration(t, pool, "064_membership_vendor_link.down.sql") // newest first: 064's memberships FK needs 043's vendors key
	applyMigration(t, pool, "061_notify_creator_role.down.sql")    // newest first: restores 054's notify_entity_creator
	applyMigration(t, pool, "060_drop_pet_vet_columns.down.sql")   // newest first: restores pets.vet_name / vet_phone
	applyMigration(t, pool, "058_same_family_fks.down.sql")        // newest first: 058's FKs need 043's vendors key
	applyMigration(t, pool, "057_secret_agent_share.down.sql")
	applyMigration(t, pool, "056_domain_v2_cleanup.down.sql")
	legacy := func(table string, id uuid.UUID) (kind *string, entity *uuid.UUID) {
		t.Helper()
		mustScan(t, pool.QueryRow(ctx, `SELECT entity_type, entity_id FROM `+table+` WHERE id = $1`, id), &kind, &entity)
		return kind, entity
	}
	for _, c := range []struct {
		table, wantKind string
		id              uuid.UUID
		wantEntity      *uuid.UUID
	}{
		{"bills", "vehicle", billVehicle, &f.vehicle},
		{"bills", "asset", billAsset, &asset},
		{"bills", "property", billNone, nil}, // 020's default
		{"loans", "property", loanProperty, &f.property},
	} {
		kind, entity := legacy(c.table, c.id)
		if kind == nil || *kind != c.wantKind || (c.wantEntity == nil) != (entity == nil) || (entity != nil && *entity != *c.wantEntity) {
			t.Errorf("down %s %s: got %v %v, want %s %v", c.table, c.id, kind, entity, c.wantKind, c.wantEntity)
		}
	}
	if kind, entity := legacy("loans", loanNone); kind != nil || entity != nil {
		t.Errorf("down: loan without subject got %v %v", kind, entity)
	}
	var vendorProperty *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT property_id FROM vendors WHERE id = $1`, vendor), &vendorProperty)
	if !is(vendorProperty, f.property) {
		t.Errorf("down: vendor property = %v, want %s", vendorProperty, f.property)
	}
	if kind, entity := legacy("notifications", alarm); kind == nil || *kind != "calendar_alarm" || entity == nil || *entity != alarmKey {
		t.Errorf("down: alarm entity = %v %v, want calendar_alarm %s", kind, entity, alarmKey)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM integrations`); n != 0 {
		t.Errorf("down: integrations has %d rows, want an empty table", n)
	}

	// Up again: same rows, nothing duplicated.
	applyMigration(t, pool, "056_domain_v2_cleanup.up.sql")
	if after := countAll(); after != before {
		t.Errorf("down+up changed the row count: %d -> %d", before, after)
	}
	mustScan(t, pool.QueryRow(ctx, `SELECT dedupe_key FROM notifications WHERE id = $1`, alarm), &key)
	if key == nil || *key != alarmKey.String() {
		t.Errorf("up again: alarm dedupe_key = %v", key)
	}
}
