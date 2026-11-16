package testutil

// Tests for migration 060 (#4705): 043's vet backfill runs again, then
// pets.vet_name / vet_phone are dropped.

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// Down to 059, seed pets with vets typed by old code (no vet_vendor_id),
// then up/down/up: every typed vet becomes or links a same-family 'vet'
// vendor, the columns are gone, and the second up creates no duplicates.
func TestDropPetVetColumns060_BackfillAndReversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()

	cols := func() int {
		t.Helper()
		return countRows(t, pool, `SELECT count(*) FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'pets' AND column_name IN ('vet_name', 'vet_phone')`)
	}
	if n := cols(); n != 0 {
		t.Fatalf("at head: %d legacy vet columns, want 0", n)
	}

	applyMigration(t, pool, "060_drop_pet_vet_columns.down.sql")
	if n := cols(); n != 2 {
		t.Fatalf("after down: %d legacy vet columns, want 2", n)
	}

	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)
	// An existing vet vendor, already linked to one pet.
	linkedVet := insertID(t, pool, `INSERT INTO vendors (household_id, name, phone, trades) VALUES ($1, 'Springfield Animal Hospital', '555-0100', ARRAY['vet']) RETURNING id`, f.household)
	linked := insertID(t, pool, `INSERT INTO pets (household_id, name, vet_vendor_id) VALUES ($1, 'Max', $2) RETURNING id`, f.household, linkedVet)
	// A plain vendor that a typed name matches: reused, gains trade 'vet'.
	plain := insertID(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Paws Clinic') RETURNING id`, f.household)

	pet := func(h uuid.UUID, name string, vetName, vetPhone any) uuid.UUID {
		return insertID(t, pool, `INSERT INTO pets (household_id, name, vet_name, vet_phone) VALUES ($1, $2, $3, $4) RETURNING id`, h, name, vetName, vetPhone)
	}
	luna := pet(f.household, "Luna", " springfield animal hospital ", "(555) 0100") // same vet as Max
	rex := pet(f.household, "Rex", "Paws Clinic", "555-0300")
	kiwi := pet(f.household, "Kiwi", nil, "555.0300") // phone only: Paws Clinic after Rex
	newVet := pet(f.household, "Tom", "Brand New Vet", "555-7777")
	blank := pet(f.household, "Goldie", "  ", nil)
	fido := pet(other.household, "Fido", "Springfield Animal Hospital", "555-0100") // other family: own vendor

	vetOf := func(id uuid.UUID) *uuid.UUID {
		t.Helper()
		var v *uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT vet_vendor_id FROM pets WHERE id = $1`, id), &v)
		return v
	}
	is := func(p *uuid.UUID, want uuid.UUID) bool { return p != nil && *p == want }
	vendors := func(h uuid.UUID) int {
		t.Helper()
		return countRows(t, pool, `SELECT count(*) FROM vendors WHERE household_id = $1`, h)
	}

	var famVendors, otherVendors int
	for cycle := 0; cycle < 2; cycle++ {
		applyMigration(t, pool, "060_drop_pet_vet_columns.up.sql")

		if n := cols(); n != 0 {
			t.Fatalf("cycle %d: %d legacy vet columns after up, want 0", cycle, n)
		}
		if !is(vetOf(linked), linkedVet) || !is(vetOf(luna), linkedVet) {
			t.Errorf("cycle %d: Max and Luna should share the Springfield vendor", cycle)
		}
		if !is(vetOf(rex), plain) || !is(vetOf(kiwi), plain) {
			t.Errorf("cycle %d: Rex and Kiwi should link the existing Paws Clinic vendor", cycle)
		}
		nv := vetOf(newVet)
		if nv == nil || *nv == linkedVet || *nv == plain {
			t.Errorf("cycle %d: Tom should get a new vendor, got %v", cycle, nv)
		} else {
			var name, phone string
			var trades []string
			var hh uuid.UUID
			mustScan(t, pool.QueryRow(ctx, `SELECT household_id, name, phone, trades FROM vendors WHERE id = $1`, *nv), &hh, &name, &phone, &trades)
			if hh != f.household || name != "Brand New Vet" || phone != "555-7777" || len(trades) != 1 || trades[0] != "vet" {
				t.Errorf("cycle %d: new vendor = %s %q %q %v", cycle, hh, name, phone, trades)
			}
		}
		if v := vetOf(blank); v != nil {
			t.Errorf("cycle %d: a blank vet should stay unlinked, got %v", cycle, v)
		}
		fv := vetOf(fido)
		if fv == nil || *fv == linkedVet {
			t.Errorf("cycle %d: Fido must get its own family's vendor, got %v", cycle, fv)
		} else if n := countRows(t, pool, `SELECT count(*) FROM vendors WHERE id = $1 AND household_id = $2`, *fv, other.household); n != 1 {
			t.Errorf("cycle %d: Fido's vet is not in Fido's family", cycle)
		}
		var trades []string
		mustScan(t, pool.QueryRow(ctx, `SELECT trades FROM vendors WHERE id = $1`, plain), &trades)
		if len(trades) != 1 || trades[0] != "vet" {
			t.Errorf("cycle %d: Paws Clinic trades = %v, want [vet]", cycle, trades)
		}

		if cycle == 0 {
			famVendors, otherVendors = vendors(f.household), vendors(other.household)
			applyMigration(t, pool, "060_drop_pet_vet_columns.down.sql")
			// Down refills the text from the linked vendor; nothing is lost
			// for a linked pet.
			var name, phone *string
			mustScan(t, pool.QueryRow(ctx, `SELECT vet_name, vet_phone FROM pets WHERE id = $1`, linked), &name, &phone)
			if name == nil || *name != "Springfield Animal Hospital" || phone == nil || *phone != "555-0100" {
				t.Errorf("down: Max's vet text = %v %v", name, phone)
			}
			mustScan(t, pool.QueryRow(ctx, `SELECT vet_name, vet_phone FROM pets WHERE id = $1`, kiwi), &name, &phone)
			if name == nil || *name != "Paws Clinic" || phone == nil || *phone != "555-0300" {
				t.Errorf("down: Kiwi's vet text = %v %v", name, phone)
			}
		} else if vendors(f.household) != famVendors || vendors(other.household) != otherVendors {
			t.Errorf("second up changed vendor counts: %d/%d, want %d/%d",
				vendors(f.household), vendors(other.household), famVendors, otherVendors)
		}
	}
}
