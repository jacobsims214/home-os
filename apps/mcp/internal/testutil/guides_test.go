package testutil

// Tests for migration 049 (guides and guide revisions). See KB
// decisions/domain-model-v2.md, "049 guides".

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// Up/down/up with guides in place: down removes the tables, re-homes files
// about a guide and drops the trigger on assets; while 049 is down the
// narrower entity_kind rejects 'guide' and keeps 'recipe'; up again works.
func TestGuides049_Reversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	user := newUser(t, pool, "Editor")

	guide := insertID(t, pool, `INSERT INTO guides (household_id, title, body, property_id) VALUES ($1, 'Winterize', '# Steps', $2) RETURNING id`, f.household, f.property)
	mustExec(t, pool, `INSERT INTO guide_revisions (guide_id, body, edited_by) VALUES ($1, '# Steps', $2)`, guide, user)
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, 'filter.jpg', 1, 'guide', $3) RETURNING id`, f.household, blob, guide)

	applyMigration(t, pool, "056_domain_v2_cleanup.down.sql") // newest first: 056 sits on top of 038-054
	applyMigration(t, pool, "055_activity_event_titles.down.sql")
	applyMigration(t, pool, "054_change_notifications.down.sql") // its trigger is on 050's activity_log
	applyMigration(t, pool, "050_authorship.down.sql")
	applyMigration(t, pool, "049_guides.down.sql")
	if n := countRows(t, pool, `SELECT count(*) FROM pg_tables WHERE tablename IN ('guides', 'guide_revisions')`); n != 0 {
		t.Fatalf("down: %d guide tables left", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname = 'assets_guides_follow_property'`); n != 0 {
		t.Error("down: trigger on assets left behind")
	}
	var kind string
	mustScan(t, pool.QueryRow(ctx, `SELECT entity_type FROM files WHERE id = $1`, file), &kind)
	if kind != "household" {
		t.Errorf("down: file about a guide should be re-homed, got %s", kind)
	}
	_, err := pool.Exec(ctx, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'guide', $1, 'x')`, f.household)
	wantSQLState(t, err, sqlstateCheckViolation)
	// Earlier Stage 2 kinds survive the down.
	recipe := insertID(t, pool, `INSERT INTO recipes (household_id, title) VALUES ($1, 'Tacos') RETURNING id`, f.household)
	mustExec(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'recipe', $2, 'ok')`, f.household, recipe)
	// Moving an asset still works without 049's trigger.
	asset := insertID(t, pool, `INSERT INTO assets (household_id, property_id, name) VALUES ($1, $2, 'Furnace') RETURNING id`, f.household, f.property)
	mustExec(t, pool, `UPDATE assets SET property_id = NULL WHERE id = $1`, asset)

	applyMigration(t, pool, "049_guides.up.sql")
	guide = insertID(t, pool, `INSERT INTO guides (household_id, title) VALUES ($1, 'Again') RETURNING id`, f.household)
	mustExec(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'guide', $2, 'ok')`, f.household, guide)
	applyMigration(t, pool, "049_guides.down.sql")
	applyMigration(t, pool, "049_guides.up.sql")
	applyMigration(t, pool, "050_authorship.up.sql")
}

// Checks, subject rules, same-family rules, delete behaviour, revisions and
// the rehome trigger.
func TestGuides049_Rules(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)

	asset := insertID(t, pool, `INSERT INTO assets (household_id, room_id, name) VALUES ($1, $2, 'HVAC') RETURNING id`, f.household, f.room)
	theirAsset := insertID(t, pool, `INSERT INTO assets (household_id, property_id, name) VALUES ($1, $2, 'Theirs') RETURNING id`, other.household, other.property)

	t.Run("defaults and family-level guide", func(t *testing.T) {
		g := insertID(t, pool, `INSERT INTO guides (household_id, title) VALUES ($1, 'Reset the router') RETURNING id`, f.household)
		var body string
		var tags []string
		var prop, ast, veh *uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT body, tags, property_id, asset_id, vehicle_id FROM guides WHERE id = $1`, g), &body, &tags, &prop, &ast, &veh)
		if body != "" || len(tags) != 0 || prop != nil || ast != nil || veh != nil {
			t.Errorf("defaults: body=%q tags=%v subject=%v %v %v", body, tags, prop, ast, veh)
		}
	})

	t.Run("checks", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO guides (household_id, title) VALUES ($1, '  ')`, f.household)
		wantSQLState(t, err, sqlstateCheckViolation)
		_, err = pool.Exec(ctx, `INSERT INTO guides (household_id, title, vehicle_id, property_id) VALUES ($1, 'x', $2, $3)`, f.household, f.vehicle, f.property)
		wantSQLState(t, err, sqlstateCheckViolation)
		// An asset derives a property, which a vehicle excludes.
		_, err = pool.Exec(ctx, `INSERT INTO guides (household_id, title, vehicle_id, asset_id) VALUES ($1, 'x', $2, $3)`, f.household, f.vehicle, asset)
		wantSQLState(t, err, sqlstateCheckViolation)
	})

	t.Run("another family's subject is refused", func(t *testing.T) {
		for col, id := range map[string]uuid.UUID{"property_id": other.property, "asset_id": theirAsset, "vehicle_id": other.vehicle} {
			_, err := pool.Exec(ctx, `INSERT INTO guides (household_id, title, `+col+`) VALUES ($1, 'x', $2)`, f.household, id)
			wantSQLState(t, err, sqlstateFKViolation)
		}
	})

	t.Run("property follows the asset", func(t *testing.T) {
		// The caller's property_id is overridden by the asset's.
		lake := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Lake house') RETURNING id`, f.household)
		g := insertID(t, pool, `INSERT INTO guides (household_id, title, asset_id, property_id) VALUES ($1, 'Air filters', $2, $3) RETURNING id`, f.household, asset, lake)
		var prop uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT property_id FROM guides WHERE id = $1`, g), &prop)
		if prop != f.property {
			t.Fatalf("property_id: want asset's %s, got %s", f.property, prop)
		}
		// The asset moves; the guide follows.
		mustExec(t, pool, `UPDATE assets SET room_id = NULL, property_id = $2 WHERE id = $1`, asset, lake)
		mustScan(t, pool.QueryRow(ctx, `SELECT property_id FROM guides WHERE id = $1`, g), &prop)
		if prop != lake {
			t.Errorf("after move: want %s, got %s", lake, prop)
		}
		mustExec(t, pool, `UPDATE assets SET property_id = $2 WHERE id = $1`, asset, f.property)
	})

	t.Run("a guide outlives its subject", func(t *testing.T) {
		thing := insertID(t, pool, `INSERT INTO assets (household_id, property_id, name) VALUES ($1, $2, 'Water heater') RETURNING id`, f.household, f.property)
		g := insertID(t, pool, `INSERT INTO guides (household_id, title, asset_id) VALUES ($1, 'Flush it', $2) RETURNING id`, f.household, thing)
		mustExec(t, pool, `DELETE FROM assets WHERE id = $1`, thing)
		var prop, ast *uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT property_id, asset_id FROM guides WHERE id = $1`, g), &prop, &ast)
		if ast != nil || prop == nil || *prop != f.property {
			t.Errorf("asset deleted: want about the property, got asset=%v property=%v", ast, prop)
		}

		car := insertID(t, pool, `INSERT INTO vehicles (household_id, make) VALUES ($1, 'Volvo') RETURNING id`, f.household)
		g2 := insertID(t, pool, `INSERT INTO guides (household_id, title, vehicle_id) VALUES ($1, 'Jump start', $2) RETURNING id`, f.household, car)
		mustExec(t, pool, `DELETE FROM vehicles WHERE id = $1`, car)
		if n := countRows(t, pool, `SELECT count(*) FROM guides WHERE id = $1 AND vehicle_id IS NULL`, g2); n != 1 {
			t.Error("vehicle deleted: guide should stay, family-level")
		}

		// A property delete cascades its assets; the guide becomes family-level.
		cabin := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Cabin') RETURNING id`, f.household)
		stove := insertID(t, pool, `INSERT INTO assets (household_id, property_id, name) VALUES ($1, $2, 'Stove') RETURNING id`, f.household, cabin)
		g3 := insertID(t, pool, `INSERT INTO guides (household_id, title, asset_id) VALUES ($1, 'Light the stove', $2) RETURNING id`, f.household, stove)
		mustExec(t, pool, `DELETE FROM properties WHERE id = $1`, cabin)
		if n := countRows(t, pool, `SELECT count(*) FROM guides WHERE id = $1 AND property_id IS NULL AND asset_id IS NULL`, g3); n != 1 {
			t.Error("property deleted: guide should stay, family-level")
		}
	})

	t.Run("revisions", func(t *testing.T) {
		editor := newUser(t, pool, "Editor")
		g := insertID(t, pool, `INSERT INTO guides (household_id, title, body) VALUES ($1, 'Pool', 'v1') RETURNING id`, f.household)
		rev := insertID(t, pool, `INSERT INTO guide_revisions (guide_id, body, edited_by) VALUES ($1, 'v1', $2) RETURNING id`, g, editor)
		var hh uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT household_id FROM guide_revisions WHERE id = $1`, rev), &hh)
		if hh != f.household {
			t.Errorf("household_id not copied from the guide: %s", hh)
		}
		// A revision can't claim another family.
		_, err := pool.Exec(ctx, `INSERT INTO guide_revisions (guide_id, household_id, body) VALUES ($1, $2, 'x')`, g, other.household)
		wantSQLState(t, err, sqlstateFKViolation)
		// The editor's login goes; the history stays.
		mustExec(t, pool, `DELETE FROM users WHERE id = $1`, editor)
		if n := countRows(t, pool, `SELECT count(*) FROM guide_revisions WHERE id = $1 AND edited_by IS NULL`, rev); n != 1 {
			t.Error("edited_by should be SET NULL")
		}
		// updated_at moves on UPDATE.
		mustExec(t, pool, `UPDATE guides SET updated_at = '2000-01-01' WHERE id = $1`, g)
		if n := countRows(t, pool, `SELECT count(*) FROM guides WHERE id = $1 AND updated_at > '2001-01-01'`, g); n != 1 {
			t.Error("updated_at should be set by the trigger")
		}
		// Revisions go with their guide.
		mustExec(t, pool, `DELETE FROM guides WHERE id = $1`, g)
		if n := countRows(t, pool, `SELECT count(*) FROM guide_revisions WHERE guide_id = $1`, g); n != 0 {
			t.Error("revisions should cascade with the guide")
		}
	})

	t.Run("registry and rehome", func(t *testing.T) {
		g := insertID(t, pool, `INSERT INTO guides (household_id, title) VALUES ($1, 'Sprinklers') RETURNING id`, f.household)
		var hh uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT entity_household('guide', $1)`, g), &hh)
		if hh != f.household {
			t.Fatalf("entity_household: %s", hh)
		}
		// Another family can't attach a note to this guide.
		_, err := pool.Exec(ctx, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'guide', $2, 'x')`, other.household, g)
		wantSQLState(t, err, sqlstateFKViolation)

		note := insertID(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'guide', $2, 'tip') RETURNING id`, f.household, g)
		blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
		file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, 'map.png', 1, 'guide', $3) RETURNING id`, f.household, blob, g)
		list := insertID(t, pool, `INSERT INTO lists (household_id, name, entity_type, entity_id) VALUES ($1, 'Parts', 'guide', $2) RETURNING id`, f.household, g)
		mustExec(t, pool, `DELETE FROM guides WHERE id = $1`, g)
		if n := countRows(t, pool, `SELECT count(*) FROM notes WHERE id = $1`, note); n != 0 {
			t.Error("note about a deleted guide survived")
		}
		for table, id := range map[string]uuid.UUID{"files": file, "lists": list} {
			if n := countRows(t, pool, `SELECT count(*) FROM `+table+` WHERE id = $1 AND entity_type = 'household' AND entity_id = household_id`, id); n != 1 {
				t.Errorf("%s about a deleted guide should be re-homed to the family", table)
			}
		}
	})

	t.Run("indexes", func(t *testing.T) {
		for _, name := range []string{"idx_guides_household_title", "idx_guides_tags", "idx_guides_property", "idx_guides_asset", "idx_guides_vehicle", "idx_guide_revisions_guide_created"} {
			if n := countRows(t, pool, `SELECT count(*) FROM pg_indexes WHERE indexname = $1`, name); n != 1 {
				t.Errorf("missing index %s", name)
			}
		}
	})
}
