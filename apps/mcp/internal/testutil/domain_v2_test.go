package testutil

// Tests for migrations 038 (domain model v2: ownership rules) and 039 (data
// backfill). See KB decisions/domain-model-v2.md.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	sqlstateFKViolation    = "23503"
	sqlstateCheckViolation = "23514"
)

// v2Fixture is one family with a property, a room in it, and a vehicle.
type v2Fixture struct {
	household, property, room, vehicle uuid.UUID
}

func seedV2Family(t *testing.T, pool *pgxpool.Pool) v2Fixture {
	t.Helper()
	ctx := context.Background()
	var f v2Fixture
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO households (name) VALUES ('v2 family') RETURNING id`), &f.household)
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO properties (household_id, name) VALUES ($1, 'Main house') RETURNING id`, f.household), &f.property)
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO rooms (property_id, name) VALUES ($1, 'Kitchen') RETURNING id`, f.property), &f.room)
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO vehicles (household_id, make) VALUES ($1, 'Subaru') RETURNING id`, f.household), &f.vehicle)
	return f
}

func mustScan(t *testing.T, row interface{ Scan(...any) error }, dest ...any) {
	t.Helper()
	if err := row.Scan(dest...); err != nil {
		t.Fatalf("scan: %v", err)
	}
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func insertID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("insert %q: %v", sql, err)
	}
	return id
}

// wantSQLState fails unless err is a Postgres error with the given code.
func wantSQLState(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want SQLSTATE %s, got %v", code, err)
	}
	if pgErr.Code != code {
		t.Fatalf("want SQLSTATE %s, got %s: %s", code, pgErr.Code, pgErr.Message)
	}
}

func TestDomainV2_OwnerTriggerRejectsCrossFamilyRefs(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	a := seedV2Family(t, pool)
	b := seedV2Family(t, pool)
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)

	rejected := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"note about another family's property",
			`INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'property', $2, 'x')`,
			[]any{a.household, b.property}, sqlstateFKViolation},
		{"note about a missing row",
			`INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'vehicle', $2, 'x')`,
			[]any{a.household, uuid.New()}, sqlstateFKViolation},
		{"note about another family",
			`INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'household', $2, 'x')`,
			[]any{a.household, b.household}, sqlstateFKViolation},
		{"note with an unknown kind",
			`INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'spaceship', $2, 'x')`,
			[]any{a.household, a.property}, sqlstateCheckViolation},
		{"file about another family's room",
			`INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, 'f', 1, 'room', $3)`,
			[]any{a.household, blob, b.room}, sqlstateFKViolation},
		{"secret about another family's vehicle",
			`INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type, entity_type, entity_id) VALUES ($1, 'x', 'y', 's', 'password', 'vehicle', $2)`,
			[]any{a.household, b.vehicle}, sqlstateFKViolation},
		{"link about another family's vehicle",
			`INSERT INTO document_links (household_id, entity_type, entity_id, link_type, url) VALUES ($1, 'vehicle', $2, 'url', 'https://x')`,
			[]any{a.household, b.vehicle}, sqlstateFKViolation},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tc.sql, tc.args...)
			wantSQLState(t, err, tc.code)
		})
	}

	t.Run("own rows and family level are accepted", func(t *testing.T) {
		mustExec(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'property', $2, 'ok')`, a.household, a.property)
		mustExec(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'household', $1, 'ok')`, a.household)
		mustExec(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, 'f', 1, 'room', $3)`, a.household, blob, a.room)
		// Until 040, a file may still have no entity (pre-v2 code writes NULL).
		blob2 := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('y', 1) RETURNING id`)
		mustExec(t, pool, `INSERT INTO files (household_id, blob_id, name, size) VALUES ($1, $2, 'loose', 1)`, a.household, blob2)
	})

	t.Run("moving a note to another family's row is rejected", func(t *testing.T) {
		id := insertID(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'vehicle', $2, 'x') RETURNING id`, a.household, a.vehicle)
		_, err := pool.Exec(ctx, `UPDATE notes SET entity_id = $1 WHERE id = $2`, b.vehicle, id)
		wantSQLState(t, err, sqlstateFKViolation)
	})

	t.Run("link household is filled from its entity", func(t *testing.T) {
		var hh uuid.UUID
		mustScan(t, pool.QueryRow(ctx,
			`INSERT INTO document_links (entity_type, entity_id, link_type, url) VALUES ('vehicle', $1, 'url', 'https://x') RETURNING household_id`,
			a.vehicle), &hh)
		if hh != a.household {
			t.Fatalf("household_id: want %s, got %s", a.household, hh)
		}
	})

	t.Run("file link must name a file of the same family", func(t *testing.T) {
		bBlob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('z', 1) RETURNING id`)
		bFile := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, 'b', 1, 'household', $1) RETURNING id`, b.household, bBlob)
		_, err := pool.Exec(ctx,
			`INSERT INTO document_links (entity_type, entity_id, link_type, link_id) VALUES ('vehicle', $1, 'file', $2)`,
			a.vehicle, bFile.String())
		wantSQLState(t, err, sqlstateFKViolation)
		_, err = pool.Exec(ctx,
			`INSERT INTO document_links (entity_type, entity_id, link_type, link_id) VALUES ('vehicle', $1, 'file', 'not-a-uuid')`,
			a.vehicle)
		wantSQLState(t, err, sqlstateFKViolation)
	})
}

func TestDomainV2_DeleteRehomesReferences(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)

	asset := insertID(t, pool, `INSERT INTO assets (household_id, room_id, name) VALUES ($1, $2, 'Fridge') RETURNING id`, f.household, f.room)
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('receipt', 7) RETURNING id`)
	file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, 'receipt.pdf', 7, 'asset', $3) RETURNING id`, f.household, blob, asset)
	secret := insertID(t, pool, `INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type, entity_type, entity_id) VALUES ($1, 'x', 'y', 'alarm code', 'password', 'property', $2) RETURNING id`, f.household, f.property)
	note := insertID(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'asset', $2, 'hums') RETURNING id`, f.household, asset)
	link := insertID(t, pool, `INSERT INTO document_links (entity_type, entity_id, link_type, url) VALUES ('asset', $1, 'url', 'https://manual') RETURNING id`, asset)
	assetBill := insertID(t, pool, `INSERT INTO bills (household_id, name, asset_id) VALUES ($1, 'Fridge warranty', $2) RETURNING id`, f.household, asset)
	assetTask := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, asset_id) VALUES ($1, 'Clean coils', $2) RETURNING id`, f.household, asset)

	// The property owns its assets: deleting it cascades.
	mustExec(t, pool, `DELETE FROM properties WHERE id = $1`, f.property)

	exists := func(table string, id uuid.UUID) bool {
		t.Helper()
		var ok bool
		mustScan(t, pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, id), &ok)
		return ok
	}
	if exists("assets", asset) {
		t.Error("asset survived its property")
	}
	if exists("maintenance_tasks", assetTask) {
		t.Error("maintenance task survived its asset (want CASCADE)")
	}
	if exists("notes", note) {
		t.Error("note about a deleted asset survived")
	}
	if exists("document_links", link) {
		t.Error("link about a deleted asset survived")
	}
	var billAsset, billProperty *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT asset_id, property_id FROM bills WHERE id = $1`, assetBill), &billAsset, &billProperty)
	if billAsset != nil || billProperty != nil {
		t.Errorf("bill subject: want family level, got asset %v property %v", billAsset, billProperty)
	}
	for table, id := range map[string]uuid.UUID{"files": file, "secrets": secret} {
		var kind string
		var entity uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT entity_type, entity_id FROM `+table+` WHERE id = $1`, id), &kind, &entity)
		if kind != "household" || entity != f.household {
			t.Errorf("%s: want ('household', %s), got (%s, %s)", table, f.household, kind, entity)
		}
	}

	t.Run("maintenance task delete takes its calendar event; other events only lose the link", func(t *testing.T) {
		task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name) VALUES ($1, 'Gutters') RETURNING id`, f.household)
		pet := insertID(t, pool, `INSERT INTO pets (household_id, name) VALUES ($1, 'Rex') RETURNING id`, f.household)
		cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Maintenance', 'maintenance', gen_random_uuid()::text) RETURNING id`, f.household)
		taskEv := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, entity_type, entity_id) VALUES ($1, 'u1', '{}', 'e', 'maintenance', $2) RETURNING id`, cal, task)
		petEv := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, entity_type, entity_id) VALUES ($1, 'u2', '{}', 'e', 'pet', $2) RETURNING id`, cal, pet)
		mustExec(t, pool, `DELETE FROM maintenance_tasks WHERE id = $1`, task)
		if exists("calendar_objects", taskEv) {
			t.Error("maintenance event survived its task")
		}
		mustExec(t, pool, `DELETE FROM pets WHERE id = $1`, pet)
		var entity *uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT entity_id FROM calendar_objects WHERE id = $1`, petEv), &entity)
		if entity != nil {
			t.Errorf("pet event entity_id: want NULL, got %s", entity)
		}
	})

	t.Run("deleting a maintenance event still deletes its task (027)", func(t *testing.T) {
		task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name) VALUES ($1, 'Filters') RETURNING id`, f.household)
		cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Maint 2', 'maintenance', gen_random_uuid()::text) RETURNING id`, f.household)
		for i, kind := range []string{"maintenance_task", "maintenance"} {
			if i == 1 {
				task = insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name) VALUES ($1, 'Legacy') RETURNING id`, f.household)
			}
			ev := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, entity_type, entity_id) VALUES ($1, $2, '{}', 'e', $3, $4) RETURNING id`, cal, uuid.NewString(), kind, task)
			mustExec(t, pool, `DELETE FROM calendar_objects WHERE id = $1`, ev)
			if exists("maintenance_tasks", task) {
				t.Errorf("%s event delete left its task", kind)
			}
		}
	})

	t.Run("vehicle delete clears notifications, cascades maintenance, nulls bills", func(t *testing.T) {
		notif := insertID(t, pool, `INSERT INTO notifications (household_id, type, title, entity_type, entity_id) VALUES ($1, 'x', 'Registration due', 'vehicle', $2) RETURNING id`, f.household, f.vehicle)
		task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, vehicle_id) VALUES ($1, 'Oil', $2) RETURNING id`, f.household, f.vehicle)
		bill := insertID(t, pool, `INSERT INTO bills (household_id, name, vehicle_id) VALUES ($1, 'Insurance', $2) RETURNING id`, f.household, f.vehicle)
		mustExec(t, pool, `DELETE FROM vehicles WHERE id = $1`, f.vehicle)
		var kind *string
		mustScan(t, pool.QueryRow(ctx, `SELECT entity_type FROM notifications WHERE id = $1`, notif), &kind)
		if kind != nil {
			t.Errorf("notification entity_type: want NULL, got %s", *kind)
		}
		if exists("maintenance_tasks", task) {
			t.Error("vehicle maintenance survived the vehicle")
		}
		var vehicle *uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT vehicle_id FROM bills WHERE id = $1`, bill), &vehicle)
		if vehicle != nil {
			t.Error("bill kept a deleted vehicle")
		}
	})

	t.Run("file delete removes its blob", func(t *testing.T) {
		mustExec(t, pool, `DELETE FROM files WHERE id = $1`, file)
		if exists("file_blobs", blob) {
			t.Error("blob survived its file")
		}
	})

	t.Run("family delete still cascades", func(t *testing.T) {
		g := seedV2Family(t, pool)
		a := insertID(t, pool, `INSERT INTO assets (household_id, property_id, name) VALUES ($1, $2, 'TV') RETURNING id`, g.household, g.property)
		b := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('tv', 2) RETURNING id`)
		mustExec(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, 'tv.pdf', 2, 'asset', $3)`, g.household, b, a)
		mustExec(t, pool, `DELETE FROM households WHERE id = $1`, g.household)
		var n int
		mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE household_id = $1`, g.household), &n)
		if n != 0 {
			t.Errorf("files left after family delete: %d", n)
		}
	})
}

func TestDomainV2_SubjectRules(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)
	asset := insertID(t, pool, `INSERT INTO assets (household_id, property_id, name) VALUES ($1, $2, 'Boiler') RETURNING id`, f.household, f.property)

	for _, table := range []string{"bills", "loans"} {
		cols, vals := "household_id, name", "$1, 'x'"
		if table == "loans" {
			cols, vals = "household_id, name, original_amount, remaining_balance", "$1, 'x', 1, 1"
		}
		t.Run(table+": a vehicle excludes property and asset", func(t *testing.T) {
			_, err := pool.Exec(ctx, `INSERT INTO `+table+` (`+cols+`, vehicle_id, property_id) VALUES (`+vals+`, $2, $3)`, f.household, f.vehicle, f.property)
			wantSQLState(t, err, sqlstateCheckViolation)
		})
		t.Run(table+": another family's vehicle is rejected", func(t *testing.T) {
			_, err := pool.Exec(ctx, `INSERT INTO `+table+` (`+cols+`, vehicle_id) VALUES (`+vals+`, $2)`, f.household, other.vehicle)
			wantSQLState(t, err, sqlstateFKViolation)
		})
		t.Run(table+": property is derived from the asset", func(t *testing.T) {
			var property *uuid.UUID
			mustScan(t, pool.QueryRow(ctx, `INSERT INTO `+table+` (`+cols+`, asset_id) VALUES (`+vals+`, $2) RETURNING property_id`, f.household, asset), &property)
			if property == nil || *property != f.property {
				t.Fatalf("property_id: want %s, got %v", f.property, property)
			}
		})
		t.Run(table+": no subject is family level", func(t *testing.T) {
			mustExec(t, pool, `INSERT INTO `+table+` (`+cols+`) VALUES (`+vals+`)`, f.household)
		})
	}

	t.Run("maintenance: another family's asset is rejected", func(t *testing.T) {
		otherAsset := insertID(t, pool, `INSERT INTO assets (household_id, name) VALUES ($1, 'Theirs') RETURNING id`, other.household)
		_, err := pool.Exec(ctx, `INSERT INTO maintenance_tasks (household_id, name, asset_id) VALUES ($1, 'x', $2)`, f.household, otherAsset)
		wantSQLState(t, err, sqlstateFKViolation)
	})

	t.Run("records follow an asset that moves property", func(t *testing.T) {
		lake := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Lake house') RETURNING id`, f.household)
		task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, asset_id) VALUES ($1, 'Flush', $2) RETURNING id`, f.household, asset)
		mustExec(t, pool, `UPDATE assets SET property_id = $1 WHERE id = $2`, lake, asset)
		var n int
		mustScan(t, pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM bills WHERE asset_id = $1 AND property_id IS DISTINCT FROM $2)
			     + (SELECT count(*) FROM loans WHERE asset_id = $1 AND property_id IS DISTINCT FROM $2)
			     + (SELECT count(*) FROM maintenance_tasks WHERE id = $3 AND property_id IS DISTINCT FROM $2)`,
			asset, lake, task), &n)
		if n != 0 {
			t.Fatalf("%d records did not follow the asset to its new property", n)
		}
	})

	t.Run("deleting a schedule keeps its tasks", func(t *testing.T) {
		sched := insertID(t, pool, `INSERT INTO maintenance_schedules (household_id, name, rrule) VALUES ($1, 'Yearly', 'FREQ=YEARLY') RETURNING id`, f.household)
		task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, schedule_id) VALUES ($1, 'This year', $2) RETURNING id`, f.household, sched)
		mustExec(t, pool, `DELETE FROM maintenance_schedules WHERE id = $1`, sched)
		var schedID *uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT schedule_id FROM maintenance_tasks WHERE id = $1`, task), &schedID)
		if schedID != nil {
			t.Error("task kept a deleted schedule")
		}
	})
}

func TestDomainV2_RoomDerivesAssetProperty(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)

	var roomHousehold uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT household_id FROM rooms WHERE id = $1`, f.room), &roomHousehold)
	if roomHousehold != f.household {
		t.Fatalf("rooms.household_id: want %s, got %s", f.household, roomHousehold)
	}

	// Only a room is given: the property comes from it.
	var property *uuid.UUID
	var asset uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO assets (household_id, room_id, name) VALUES ($1, $2, 'Oven') RETURNING id, property_id`, f.household, f.room), &asset, &property)
	if property == nil || *property != f.property {
		t.Fatalf("property_id from room: want %s, got %v", f.property, property)
	}

	// A disagreeing property is overridden by the room's.
	lake := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Lake house') RETURNING id`, f.household)
	mustScan(t, pool.QueryRow(ctx, `UPDATE assets SET property_id = $1 WHERE id = $2 RETURNING property_id`, lake, asset), &property)
	if *property != f.property {
		t.Fatalf("room should win over a disagreeing property, got %s", *property)
	}

	// Moving to a room in another property moves the asset.
	dock := insertID(t, pool, `INSERT INTO rooms (property_id, name) VALUES ($1, 'Boathouse') RETURNING id`, lake)
	mustScan(t, pool.QueryRow(ctx, `UPDATE assets SET room_id = $1 WHERE id = $2 RETURNING property_id`, dock, asset), &property)
	if *property != lake {
		t.Fatalf("property after room move: want %s, got %s", lake, *property)
	}

	// Another family's room is rejected, for assets and for rooms.household_id.
	_, err := pool.Exec(ctx, `UPDATE assets SET room_id = $1 WHERE id = $2`, other.room, asset)
	wantSQLState(t, err, sqlstateFKViolation)
	_, err = pool.Exec(ctx, `INSERT INTO rooms (property_id, household_id, name) VALUES ($1, $2, 'x')`, other.property, f.household)
	wantSQLState(t, err, sqlstateFKViolation)

	// Deleting the room leaves the asset in the property.
	mustExec(t, pool, `DELETE FROM rooms WHERE id = $1`, dock)
	var room *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT room_id, property_id FROM assets WHERE id = $1`, asset), &room, &property)
	if room != nil || property == nil || *property != lake {
		t.Fatalf("after room delete: want room NULL, property %s; got room %v, property %v", lake, room, property)
	}

	// No room and no property: family level.
	mustExec(t, pool, `INSERT INTO assets (household_id, name) VALUES ($1, 'Laptop')`, f.household)
}

// TestDomainV2_Backfill039 rolls a fresh database back to 037 (039 down, 038
// down), writes rows the way pre-v2 code did, then applies 038 and 039 again
// and checks the moved data. It also proves down/up and that 039 is
// idempotent.
func TestDomainV2_Backfill039(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	apply := func(name string) {
		t.Helper()
		dir, err := migrationsDir()
		if err != nil {
			t.Fatal(err)
		}
		sql, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	// Undo later migrations first, newest first: 041, 044, 046, 047 and 049
	// hang triggers on 038's functions, which 038 down drops without CASCADE,
	// and 048's files.person_id references 041's people. 050 adds authorship
	// columns and triggers to tables of all of them. 051 only touches
	// secret_keys and 052 one bills column; they are undone first to keep the
	// chain strictly newest-first.
	apply("054_change_notifications.down.sql")
	apply("052_bills_paid_for_due_date.down.sql")
	apply("051_secret_key_verifier.down.sql")
	apply("050_authorship.down.sql")
	apply("049_guides.down.sql")
	apply("048_document_metadata.down.sql")
	apply("047_meals.down.sql")
	apply("046_lists.down.sql")
	apply("044_budgeting.down.sql")
	apply("043_vendor_trades.down.sql")
	apply("041_people.down.sql")
	apply("039_domain_model_v2_data.down.sql")
	apply("038_domain_model_v2_add.down.sql")

	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)
	var user uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ('v2@test', 'U', 'x') RETURNING id`), &user)
	asset := insertID(t, pool, `INSERT INTO assets (household_id, room_id, name) VALUES ($1, $2, 'Fridge') RETURNING id`, f.household, f.room)
	// An asset whose property disagrees with its room (pre-v2 allowed it).
	lake := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Lake house') RETURNING id`, f.household)
	stray := insertID(t, pool, `INSERT INTO assets (household_id, room_id, property_id, name) VALUES ($1, $2, $3, 'Stray') RETURNING id`, f.household, f.room, lake)
	pet := insertID(t, pool, `INSERT INTO pets (household_id, name) VALUES ($1, 'Rex') RETURNING id`, f.household)

	bill := func(kind string, id any) uuid.UUID {
		return insertID(t, pool, `INSERT INTO bills (household_id, name, entity_type, entity_id) VALUES ($1, $2, $3, $4) RETURNING id`, f.household, kind+" bill", kind, id)
	}
	billProperty := bill("property", f.property)
	billVehicle := bill("vehicle", f.vehicle)
	billAsset := bill("asset", asset)
	billPet := bill("pet", pet)
	billCross := bill("property", other.property)
	billDefault := insertID(t, pool, `INSERT INTO bills (household_id, name) VALUES ($1, 'Netflix') RETURNING id`, f.household)
	loanVehicle := insertID(t, pool, `INSERT INTO loans (household_id, name, entity_type, entity_id, original_amount, remaining_balance) VALUES ($1, 'Car loan', 'vehicle', $2, 1, 1) RETURNING id`, f.household, f.vehicle)
	loanProperty := insertID(t, pool, `INSERT INTO loans (household_id, name, entity_type, entity_id, original_amount, remaining_balance) VALUES ($1, 'Mortgage', 'property', $2, 1, 1) RETURNING id`, f.household, f.property)
	vendor := insertID(t, pool, `INSERT INTO vendors (household_id, property_id, name) VALUES ($1, $2, 'Plumber') RETURNING id`, f.household, f.property)
	task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, vehicle_id, property_id) VALUES ($1, 'Oil', $2, $3) RETURNING id`, f.household, f.vehicle, f.property)
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Maintenance', 'maintenance', gen_random_uuid()::text) RETURNING id`, f.household)
	ev := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, entity_type, entity_id) VALUES ($1, 'u', '{}', 'e', 'maintenance', $2) RETURNING id`, cal, task)
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size) VALUES ($1, $2, 'loose.pdf', 1) RETURNING id`, f.household, blob)
	secret := insertID(t, pool, `INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type) VALUES ($1, 'x', 'y', 'wifi', 'password') RETURNING id`, f.household)
	crossNote := insertID(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'property', $2, 'wrong family') RETURNING id`, f.household, other.property)
	linkKept := insertID(t, pool, `INSERT INTO document_links (entity_type, entity_id, link_type, link_id, url) VALUES ('vehicle', $1, 'paperless', '12', 'https://paperless/12') RETURNING id`, f.vehicle)
	linkDropped := insertID(t, pool, `INSERT INTO document_links (entity_type, entity_id, link_type, link_id) VALUES ('vehicle', $1, 'minio', 'key') RETURNING id`, f.vehicle)
	linkOrphan := insertID(t, pool, `INSERT INTO document_links (entity_type, entity_id, link_type, url) VALUES ('asset', $1, 'url', 'https://x') RETURNING id`, uuid.New())
	invite := insertID(t, pool, `INSERT INTO invitations (household_id, email, token, role, invited_by, expires_at) VALUES ($1, 'i@test', 'tok', 'grandma', $2, now() + interval '1 day') RETURNING id`, f.household, user)

	apply("038_domain_model_v2_add.up.sql")
	apply("039_domain_model_v2_data.up.sql")

	countRows := func() int {
		t.Helper()
		var n int
		mustScan(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM bills) + (SELECT count(*) FROM loans) + (SELECT count(*) FROM notes)
			+ (SELECT count(*) FROM files) + (SELECT count(*) FROM secrets) + (SELECT count(*) FROM document_links)
			+ (SELECT count(*) FROM assets) + (SELECT count(*) FROM rooms) + (SELECT count(*) FROM vendors)
			+ (SELECT count(*) FROM maintenance_tasks) + (SELECT count(*) FROM calendar_objects)`), &n)
		return n
	}
	before := countRows()
	apply("039_domain_model_v2_data.up.sql") // idempotent
	if after := countRows(); after != before {
		t.Fatalf("re-running 039 changed the row count: %d -> %d", before, after)
	}

	type subject struct{ property, vehicle, asset *uuid.UUID }
	subjectOf := func(table string, id uuid.UUID) subject {
		t.Helper()
		var s subject
		mustScan(t, pool.QueryRow(ctx, `SELECT property_id, vehicle_id, asset_id FROM `+table+` WHERE id = $1`, id), &s.property, &s.vehicle, &s.asset)
		return s
	}
	is := func(p *uuid.UUID, want uuid.UUID) bool { return p != nil && *p == want }

	t.Run("bills and loans get typed subjects; legacy columns kept", func(t *testing.T) {
		if s := subjectOf("bills", billProperty); !is(s.property, f.property) || s.vehicle != nil {
			t.Errorf("property bill: %+v", s)
		}
		if s := subjectOf("bills", billVehicle); !is(s.vehicle, f.vehicle) || s.property != nil {
			t.Errorf("vehicle bill: %+v", s)
		}
		if s := subjectOf("bills", billAsset); !is(s.asset, asset) || !is(s.property, f.property) {
			t.Errorf("asset bill: want asset and its property, got %+v", s)
		}
		for name, id := range map[string]uuid.UUID{"pet": billPet, "cross-family": billCross, "default": billDefault} {
			if s := subjectOf("bills", id); s.property != nil || s.vehicle != nil || s.asset != nil {
				t.Errorf("%s bill: want family level, got %+v", name, s)
			}
		}
		if s := subjectOf("loans", loanVehicle); !is(s.vehicle, f.vehicle) {
			t.Errorf("vehicle loan: %+v", s)
		}
		if s := subjectOf("loans", loanProperty); !is(s.property, f.property) {
			t.Errorf("property loan: %+v", s)
		}
		var legacy string
		mustScan(t, pool.QueryRow(ctx, `SELECT entity_type FROM bills WHERE id = $1`, billVehicle), &legacy)
		if legacy != "vehicle" {
			t.Errorf("legacy bills.entity_type: want vehicle, got %s", legacy)
		}
	})

	t.Run("lost links are kept as notes", func(t *testing.T) {
		var n int
		mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM notes WHERE entity_type = 'bill' AND entity_id = ANY($1) AND title = 'Earlier link (migration 039)'`,
			[]uuid.UUID{billPet, billCross}), &n)
		if n != 2 {
			t.Errorf("want 2 earlier-link notes (pet, cross-family), got %d", n)
		}
		mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM notes WHERE entity_type = 'bill' AND entity_id = ANY($1)`,
			[]uuid.UUID{billProperty, billVehicle, billAsset, billDefault}), &n)
		if n != 0 {
			t.Errorf("migrated bills should get no note, got %d", n)
		}
	})

	t.Run("vendor property becomes a note", func(t *testing.T) {
		var body string
		mustScan(t, pool.QueryRow(ctx, `SELECT body FROM notes WHERE entity_type = 'vendor' AND entity_id = $1 AND title = 'Serves property'`, vendor), &body)
		if body != "Main house" {
			t.Errorf("vendor note body: want %q, got %q", "Main house", body)
		}
		var still *uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT property_id FROM vendors WHERE id = $1`, vendor), &still)
		if !is(still, f.property) {
			t.Error("vendors.property_id must stay until 040")
		}
	})

	t.Run("calendar maintenance becomes maintenance_task", func(t *testing.T) {
		var kind string
		mustScan(t, pool.QueryRow(ctx, `SELECT entity_type FROM calendar_objects WHERE id = $1`, ev), &kind)
		if kind != "maintenance_task" {
			t.Errorf("entity_type: want maintenance_task, got %s", kind)
		}
	})

	t.Run("files, secrets and wrong-family notes point at the family", func(t *testing.T) {
		for table, id := range map[string]uuid.UUID{"files": file, "secrets": secret, "notes": crossNote} {
			var kind string
			var entity uuid.UUID
			mustScan(t, pool.QueryRow(ctx, `SELECT entity_type, entity_id FROM `+table+` WHERE id = $1`, id), &kind, &entity)
			if kind != "household" || entity != f.household {
				t.Errorf("%s: want ('household', %s), got (%s, %s)", table, f.household, kind, entity)
			}
		}
	})

	t.Run("rooms, assets, maintenance, links, invitations", func(t *testing.T) {
		var n int
		mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM rooms WHERE household_id IS NULL`), &n)
		if n != 0 {
			t.Errorf("%d rooms without household_id", n)
		}
		var property *uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT property_id FROM assets WHERE id = $1`, stray), &property)
		if !is(property, f.property) {
			t.Errorf("stray asset: want its room's property %s, got %v", f.property, property)
		}
		if s := subjectOf("maintenance_tasks", task); !is(s.vehicle, f.vehicle) || s.property != nil {
			t.Errorf("vehicle task: want vehicle only, got %+v", s)
		}
		var linkType string
		var hh uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT link_type, household_id FROM document_links WHERE id = $1`, linkKept), &linkType, &hh)
		if linkType != "url" || hh != f.household {
			t.Errorf("paperless link: want url in %s, got %s in %s", f.household, linkType, hh)
		}
		mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_links WHERE id = ANY($1)`, []uuid.UUID{linkDropped, linkOrphan}), &n)
		if n != 0 {
			t.Errorf("minio link without url and orphan link should be deleted, %d left", n)
		}
		var role string
		mustScan(t, pool.QueryRow(ctx, `SELECT role::text FROM invitations WHERE id = $1`, invite), &role)
		if role != "family_member" {
			t.Errorf("invalid invitation role: want family_member, got %s", role)
		}
	})
}
