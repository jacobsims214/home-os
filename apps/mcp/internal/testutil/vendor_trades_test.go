package testutil

// Tests for migration 043 (vendor trades; pets' vet becomes a vendor).
// See KB decisions/domain-model-v2.md, "043 vendor trades".

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// Down to 041, seed pets with free-text vets and vendors with specialties,
// then up/down/up: vets become shared 'vet' vendors, specialties become
// trades, and the second up creates no duplicates.
func TestVendorTrades043_BackfillAndReversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	applyMigration(t, pool, "066_list_item_checked_by.down.sql")   // newest first: drops list_items.checked_by
	applyMigration(t, pool, "065_vendor_address.down.sql")         // newest first: moves vendors.address back into notes
	applyMigration(t, pool, "064_membership_vendor_link.down.sql") // newest first: 064's memberships FK needs 043's vendors key
	applyMigration(t, pool, "061_notify_creator_role.down.sql")    // newest first: restores 054's notify_entity_creator
	applyMigration(t, pool, "060_drop_pet_vet_columns.down.sql")   // newest first: restores pets.vet_name / vet_phone
	applyMigration(t, pool, "058_same_family_fks.down.sql")        // newest first: 058's FKs need 043's vendors key
	applyMigration(t, pool, "057_secret_agent_share.down.sql")
	applyMigration(t, pool, "056_domain_v2_cleanup.down.sql") // newest first: 056 sits on top of 038-054
	applyMigration(t, pool, "055_activity_event_titles.down.sql")
	applyMigration(t, pool, "054_change_notifications.down.sql") // its trigger is on 050's activity_log
	applyMigration(t, pool, "050_authorship.down.sql")
	applyMigration(t, pool, "043_vendor_trades.down.sql")

	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)
	hvac := insertID(t, pool, `INSERT INTO vendors (household_id, name, specialty) VALUES ($1, 'Cool Air', ' HVAC ') RETURNING id`, f.household)
	odd := insertID(t, pool, `INSERT INTO vendors (household_id, name, specialty) VALUES ($1, 'Bob', 'Roof repair & gutters') RETURNING id`, f.household)
	// An existing vendor with the vet's name but no phone: reused, gets 'vet'.
	existingVet := insertID(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Paws Clinic') RETURNING id`, f.household)
	pet := func(h uuid.UUID, name string, vetName, vetPhone any) uuid.UUID {
		return insertID(t, pool, `INSERT INTO pets (household_id, name, vet_name, vet_phone) VALUES ($1, $2, $3, $4) RETURNING id`, h, name, vetName, vetPhone)
	}
	max := pet(f.household, "Max", "Springfield Animal Hospital", "555-0100")
	luna := pet(f.household, "Luna", " springfield animal hospital ", "(555) 0100") // same vet
	rex := pet(f.household, "Rex", "Paws Clinic", "555-0300")
	// Same name, contradicting phone: a different vet.
	tom := pet(f.household, "Tom", "Springfield Animal Hospital", "555-9999")
	phoneOnly := pet(f.household, "Kiwi", nil, "555-0300") // matches Paws Clinic by phone after Rex
	none := pet(f.household, "Goldie", nil, "  ")
	otherPet := pet(other.household, "Fido", "Springfield Animal Hospital", "555-0100") // other family: own vendor

	vetOf := func(id uuid.UUID) *uuid.UUID {
		t.Helper()
		var v *uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT vet_vendor_id FROM pets WHERE id = $1`, id), &v)
		return v
	}

	for cycle := 0; cycle < 2; cycle++ {
		applyMigration(t, pool, "043_vendor_trades.up.sql")

		if vetOf(max) == nil || vetOf(luna) == nil || *vetOf(max) != *vetOf(luna) {
			t.Fatalf("cycle %d: Max and Luna should share one vet vendor", cycle)
		}
		if v := vetOf(rex); v == nil || *v != existingVet {
			t.Errorf("cycle %d: Rex should reuse the existing Paws Clinic vendor", cycle)
		}
		if v := vetOf(phoneOnly); v == nil || *v != existingVet {
			t.Errorf("cycle %d: phone-only vet should match Paws Clinic by phone", cycle)
		}
		if v := vetOf(tom); v == nil || *v == *vetOf(max) {
			t.Errorf("cycle %d: a contradicting phone should give a separate vendor", cycle)
		}
		if vetOf(none) != nil {
			t.Errorf("cycle %d: pet without a vet should stay unlinked", cycle)
		}
		if v := vetOf(otherPet); v == nil || *v == *vetOf(max) {
			t.Errorf("cycle %d: other family's pet must get its own vendor", cycle)
		}
		// Vendors: 3 seeded + Springfield (555-0100) + Springfield (555-9999) in f; 1 in other.
		if n := countRows(t, pool, `SELECT count(*) FROM vendors`); n != 6 {
			t.Errorf("cycle %d: want 6 vendors (no duplicates), got %d", cycle, n)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM vendors WHERE trades @> ARRAY['vet']`); n != 4 {
			t.Errorf("cycle %d: want 4 vendors with trade vet, got %d", cycle, n)
		}
		var phone string
		mustScan(t, pool.QueryRow(ctx, `SELECT phone FROM vendors WHERE id = $1`, existingVet), &phone)
		if phone != "555-0300" {
			t.Errorf("cycle %d: reused vendor should take the pet's vet phone, got %q", cycle, phone)
		}
		var trades []string
		mustScan(t, pool.QueryRow(ctx, `SELECT trades FROM vendors WHERE id = $1`, hvac), &trades)
		if len(trades) != 1 || trades[0] != "hvac" {
			t.Errorf("cycle %d: HVAC specialty should become trade hvac, got %v", cycle, trades)
		}
		mustScan(t, pool.QueryRow(ctx, `SELECT trades FROM vendors WHERE id = $1`, odd), &trades)
		if len(trades) != 0 {
			t.Errorf("cycle %d: unknown specialty should leave trades empty, got %v", cycle, trades)
		}

		if cycle == 0 {
			// A pet whose vet exists only as the link keeps it through down.
			mustExec(t, pool, `UPDATE pets SET vet_name = NULL, vet_phone = NULL WHERE id = $1`, tom)
			applyMigration(t, pool, "043_vendor_trades.down.sql")
			var name, ph *string
			mustScan(t, pool.QueryRow(ctx, `SELECT vet_name, vet_phone FROM pets WHERE id = $1`, tom), &name, &ph)
			if name == nil || *name != "Springfield Animal Hospital" || ph == nil || *ph != "555-9999" {
				t.Fatalf("down should copy the vendor back into vet_name/vet_phone, got %v %v", name, ph)
			}
		}
	}
}

// The GIN index exists and serves trade lookups; trades reject NULL and
// blank elements; a pet's vet must be a vendor of the same family; deleting
// the vendor clears only the pet's link.
func TestVendorTrades043_Constraints(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	a := seedV2Family(t, pool)
	b := seedV2Family(t, pool)

	var method string
	mustScan(t, pool.QueryRow(ctx, `SELECT am.amname FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_am am ON am.oid = c.relam
		WHERE c.relname = 'idx_vendors_trades'`), &method)
	if method != "gin" {
		t.Fatalf("idx_vendors_trades: want gin, got %q", method)
	}

	plumber := insertID(t, pool, `INSERT INTO vendors (household_id, name, trades) VALUES ($1, 'Pipes', ARRAY['plumber','handyman']) RETURNING id`, a.household)
	var found uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT id FROM vendors WHERE household_id = $1 AND trades @> ARRAY['plumber']`, a.household), &found)
	if found != plumber {
		t.Fatalf("trade lookup returned %v, want %v", found, plumber)
	}

	for _, bad := range []string{`ARRAY['plumber', NULL]`, `ARRAY['']`} {
		_, err := pool.Exec(ctx, `INSERT INTO vendors (household_id, name, trades) VALUES ($1, 'Bad', `+bad+`)`, a.household)
		wantSQLState(t, err, sqlstateCheckViolation)
	}
	_, err := pool.Exec(ctx, `INSERT INTO vendors (household_id, name, trades) VALUES ($1, 'Bad', NULL)`, a.household)
	wantSQLState(t, err, "23502") // not_null_violation

	vet := insertID(t, pool, `INSERT INTO vendors (household_id, name, trades) VALUES ($1, 'Vet', ARRAY['vet']) RETURNING id`, a.household)
	otherVet := insertID(t, pool, `INSERT INTO vendors (household_id, name, trades) VALUES ($1, 'Vet', ARRAY['vet']) RETURNING id`, b.household)

	_, err = pool.Exec(ctx, `INSERT INTO pets (household_id, name, vet_vendor_id) VALUES ($1, 'Cross', $2)`, a.household, otherVet)
	wantSQLState(t, err, sqlstateFKViolation)

	rex := insertID(t, pool, `INSERT INTO pets (household_id, name, vet_vendor_id) VALUES ($1, 'Rex', $2) RETURNING id`, a.household, vet)
	mustExec(t, pool, `DELETE FROM vendors WHERE id = $1`, vet)
	var link *uuid.UUID
	var household uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT vet_vendor_id, household_id FROM pets WHERE id = $1`, rex), &link, &household)
	if link != nil || household != a.household {
		t.Fatalf("after vendor delete: want vet_vendor_id NULL and household kept, got %v %v", link, household)
	}
}
