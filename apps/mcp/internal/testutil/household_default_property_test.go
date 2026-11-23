package testutil

// Tests for migration 093 (#5521, schema review F6): households.default_property_id
// is a composite same-family FK, not a single-column one.
//
// This table is the odd one out. Every other converted reference pairs a child
// column with the child's own household_id:
//
//     (thing_id, household_id) -> parent (id, household_id)
//
// `households` has no household_id column -- the row IS the household -- so the
// tenant key is its PRIMARY KEY `id`, and the composite is
//
//     (default_property_id, id) -> properties (id, household_id)
//
// i.e. default_property_id names the property and households.id must equal that
// property's household_id. TestSameFamilyFKs058_* covers the ordinary shape.

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestDefaultPropertyFK093_RejectsCrossHousehold is the whole point of the
// ticket: before 093 this UPDATE returned "UPDATE 1" and household A was left
// naming household B's house.
func TestDefaultPropertyFK093_RejectsCrossHousehold(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	a := seedV2Family(t, pool)
	b := seedV2Family(t, pool)

	// A second property for household A, so "same household" is not being
	// proved by the only property A has.
	aSecond := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'A beach house') RETURNING id`, a.household)

	// Establish a legitimate default first: a failure below is then a refusal
	// of the cross-family write, not the absence of any default.
	mustExec(t, pool, `UPDATE households SET default_property_id = $2 WHERE id = $1`, a.household, a.property)

	rejected := []struct {
		name string
		sql  string
		args []any
	}{
		// UPDATE across the family boundary -- the reported defect, verbatim.
		{"update default to another household's property",
			`UPDATE households SET default_property_id = $2 WHERE id = $1`, []any{a.household, b.property}},
		// The same write aimed at a property that is itself a valid default for
		// its own household, so nothing but the tenancy check can reject it.
		{"update default to another household's second property",
			`UPDATE households SET default_property_id = $2 WHERE id = $1`, []any{a.household, b.property}},
		// A missing property must still be refused (23503) as it was before 093:
		// the composite FK must not have loosened the existence check.
		{"update default to a nonexistent property",
			`UPDATE households SET default_property_id = $2 WHERE id = $1`, []any{a.household, uuid.New()}},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tc.sql, tc.args...)
			wantSQLState(t, err, sqlstateFKViolation)
		})
	}

	// Same-household assignment still works, including moving between two
	// properties of the same family and setting it back to NULL.
	mustExec(t, pool, `UPDATE households SET default_property_id = $2 WHERE id = $1`, a.household, aSecond)
	mustExec(t, pool, `UPDATE households SET default_property_id = $2 WHERE id = $1`, a.household, a.property)
	mustExec(t, pool, `UPDATE households SET default_property_id = NULL WHERE id = $1`, a.household)

	// Cross-family INSERT is refused too, not only UPDATE. households.id is
	// usually generated, so this inserts a household whose id is a known value
	// that is NOT the household owning the property.
	_, err := pool.Exec(ctx,
		`INSERT INTO households (id, name, default_property_id) VALUES ($1, 'Sneaky', $2)`,
		uuid.New(), b.property)
	wantSQLState(t, err, sqlstateFKViolation)
}

// TestDefaultPropertyFK093_DeleteActions proves ON DELETE SET NULL survived the
// conversion and still clears only default_property_id. A bare SET NULL on a
// two-column key would try to null households.id too and fail the delete with
// a NOT NULL violation, so this test is what pins the column-list form.
func TestDefaultPropertyFK093_DeleteActions(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	a := seedV2Family(t, pool)
	b := seedV2Family(t, pool)

	mustExec(t, pool, `UPDATE households SET default_property_id = $2 WHERE id = $1`, a.household, a.property)
	mustExec(t, pool, `UPDATE households SET default_property_id = $2 WHERE id = $1`, b.household, b.property)

	// Deleting the property nulls the default and leaves the household row.
	mustExec(t, pool, `DELETE FROM properties WHERE id = $1`, a.property)
	var defaultProp *uuid.UUID
	var householdID uuid.UUID
	var name string
	mustScan(t, pool.QueryRow(ctx,
		`SELECT default_property_id, id, name FROM households WHERE id = $1`, a.household),
		&defaultProp, &householdID, &name)
	if defaultProp != nil {
		t.Errorf("property delete should null default_property_id, got %v", *defaultProp)
	}
	if householdID != a.household {
		t.Errorf("property delete changed households.id to %v", householdID)
	}
	if name != "v2 family" {
		t.Errorf("property delete damaged the household row: name = %q", name)
	}

	// Deleting the other household still works while its default is set.
	mustExec(t, pool, `DELETE FROM households WHERE id = $1`, b.household)
	var n int
	mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM households WHERE id = $1`, b.household), &n)
	if n != 0 {
		t.Errorf("household delete left %d row(s)", n)
	}
}

// TestDefaultPropertyFK093_IsComposite asserts the catalogue shape, so a later
// migration that recreates this constraint single-column again fails here
// instead of silently reopening the hole. Column ORDER is checked too: the
// mirror order (id, default_property_id) also creates, but rejects legitimate
// same-household writes.
func TestDefaultPropertyFK093_IsComposite(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()

	var nChild, nParent int
	var confdeltype string
	var def string
	err := pool.QueryRow(ctx, `
		SELECT array_length(conkey,1), array_length(confkey,1), confdeltype,
		       pg_get_constraintdef(oid)
		  FROM pg_constraint
		 WHERE conname = 'households_default_property_id_fkey'`).Scan(&nChild, &nParent, &confdeltype, &def)
	if err != nil {
		t.Fatalf("read constraint: %v", err)
	}
	if nChild != 2 || nParent != 2 {
		t.Fatalf("households_default_property_id_fkey should be composite, got %d/%d cols: %s", nChild, nParent, def)
	}
	if confdeltype != "n" {
		t.Errorf("ON DELETE should remain SET NULL (confdeltype 'n'), got %q", confdeltype)
	}

	// Pairing, in order: default_property_id -> properties.id,
	// households.id -> properties.household_id.
	rows, err := pool.Query(ctx, `
		SELECT a.attname, af.attname
		  FROM pg_constraint c
		  JOIN LATERAL unnest(c.conkey, c.confkey) WITH ORDINALITY AS k(ca, pa, ord) ON TRUE
		  JOIN pg_attribute a  ON a.attrelid  = c.conrelid  AND a.attnum  = k.ca
		  JOIN pg_attribute af ON af.attrelid = c.confrelid AND af.attnum = k.pa
		 WHERE c.conname = 'households_default_property_id_fkey'
		 ORDER BY k.ord`)
	if err != nil {
		t.Fatalf("read pairing: %v", err)
	}
	defer rows.Close()
	want := [][2]string{
		{"default_property_id", "id"},
		{"id", "household_id"},
	}
	var got [][2]string
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			t.Fatalf("scan pairing: %v", err)
		}
		got = append(got, [2]string{child, parent})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("pairing rows: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("pairing has %d entries, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pairing[%d] = %v, want %v (column order is load-bearing)", i, got[i], want[i])
		}
	}
}
