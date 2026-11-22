package testutil

// Migration 084 (#5512): every family-scoped table a composite
// (col, household_id) FK can point at carries UNIQUE (id, household_id).
//
// The keys are redundant over the primary key — id alone is already unique — so
// they reject nothing on their own. Their whole purpose is to be the parent
// side of a composite foreign key, which PostgreSQL refuses to create unless a
// unique constraint over exactly those columns exists. 058 could convert only
// the 15 relationships whose parent already had the key; #5519 converts the
// remaining 15 on top of this migration.
//
// So this test asserts the thing the ticket is for, in the two ways it can
// regress: the keys are gone (a CREATE TABLE refactor dropped one), or a key is
// silently not over (id, household_id) any more (somebody "tidied" the column
// order or the columns). It also builds one real composite FK to prove the keys
// are usable, not just present.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// familyKeyTables are the 12 tables 084 gives a (id, household_id) key to.
//
// secrets is deliberately absent: 075 already gave it
// secrets_id_household_id_key, and 084 does not touch it. calendar_objects is
// absent for a different reason: it has no household_id column at all, so
// (id, household_id) is not a key it can have. Both are asserted below so a
// future reader does not "fix" the omission.
var familyKeyTables = []string{
	"assets",
	"bills",
	"calendars",
	"document_links",
	"guide_revisions",
	"loans",
	"maintenance_tasks",
	"memberships",
	"notes",
	"pets",
	"rooms",
	"vehicles",
}

// familyKeyName is the constraint 084 adds: <table>_id_household_id_key, the
// spelling 043/058/072/075 use.
func familyKeyName(table string) string { return table + "_id_household_id_key" }

// keyColumns returns the key columns of every unique index on table that is
// exactly (id, household_id) — no partial predicate, no expression, no extra
// column — as index name -> comma-joined columns. A unique index over
// (id, household_id, extra) would NOT count: a composite FK needs the pair to
// be unique on its own, and a wider index does not provide that.
func keyColumns(t *testing.T, pool *pgxpool.Pool, table string) map[string]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT ic.relname,
		       (SELECT string_agg(a.attname, ',' ORDER BY k.ord)
		          FROM unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
		          JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		         WHERE k.ord <= i.indnkeyatts)
		  FROM pg_index i
		  JOIN pg_class ic ON ic.oid = i.indexrelid
		 WHERE i.indrelid = $1::regclass
		   AND i.indisunique
		   AND i.indnkeyatts = 2
		   AND i.indpred IS NULL
		   AND i.indexprs IS NULL`, table)
	if err != nil {
		t.Fatalf("query keys on %s: %v", table, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, cols string
		if err := rows.Scan(&name, &cols); err != nil {
			t.Fatalf("scan keys on %s: %v", table, err)
		}
		out[name] = cols
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate keys on %s: %v", table, err)
	}
	return out
}

// TestFamilyUniqueKeys084_Present asserts every table 084 names has a unique
// index over exactly (id, household_id), under the expected name.
func TestFamilyUniqueKeys084_Present(t *testing.T) {
	pool := NewPool(t)

	for _, table := range familyKeyTables {
		table := table
		t.Run(table, func(t *testing.T) {
			keys := keyColumns(t, pool, table)
			cols, ok := keys[familyKeyName(table)]
			if !ok {
				t.Fatalf("%s has no %s; unique keys over 2 columns: %v",
					table, familyKeyName(table), keys)
			}
			if cols != "id,household_id" {
				t.Fatalf("%s: %s covers (%s), want (id,household_id)",
					table, familyKeyName(table), cols)
			}
		})
	}
}

// TestFamilyUniqueKeys084_CompositeFKIsPossible is the point of the migration:
// with the key in place, PostgreSQL accepts a composite same-family FK against
// the table. #5519 is the ticket that actually converts them, so this builds
// one and rolls it back rather than leaving the single-column FK replaced in a
// shared template-derivative database.
func TestFamilyUniqueKeys084_CompositeFKIsPossible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// assets is the parent four other tables point at by asset_id alone.
	if _, err := tx.Exec(ctx, `ALTER TABLE bills DROP CONSTRAINT bills_asset_id_fkey`); err != nil {
		t.Fatalf("drop bills_asset_id_fkey: %v", err)
	}
	_, err = tx.Exec(ctx, `
		ALTER TABLE bills ADD CONSTRAINT bills_asset_id_fkey
		    FOREIGN KEY (asset_id, household_id) REFERENCES assets (id, household_id)
		    ON DELETE SET NULL (asset_id)`)
	if err != nil {
		t.Fatalf("composite FK on assets(id, household_id) rejected — the 084 key is not usable: %v", err)
	}

	var def string
	if err := tx.QueryRow(ctx,
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'bills_asset_id_fkey'`).
		Scan(&def); err != nil {
		t.Fatalf("read constraint: %v", err)
	}
	// Both columns in the key, and the column-list SET NULL that keeps
	// household_id from being nulled by a cascade (043/058's form).
	for _, want := range []string{"FOREIGN KEY (asset_id, household_id)", "id, household_id)", "SET NULL (asset_id)"} {
		if !strings.Contains(def, want) {
			t.Fatalf("constraint is %q, want it to contain %q", def, want)
		}
	}
}

// TestFamilyUniqueKeys084_Scope pins the two tables that look like they belong
// in the list but do not, so neither is "fixed" later by adding a duplicate
// index or a column.
func TestFamilyUniqueKeys084_Scope(t *testing.T) {
	pool := NewPool(t)

	// secrets already had the key before 084 — from 075, not from this one.
	if !strings.Contains(keyColumns(t, pool, "secrets")["secrets_id_household_id_key"], "id,household_id") {
		t.Fatal("secrets lost secrets_id_household_id_key (075); 084 does not re-add it")
	}
	// Adding it again would be a second index over the same columns.
	for name, cols := range keyColumns(t, pool, "secrets") {
		if cols == "id,household_id" && name != "secrets_id_household_id_key" {
			t.Fatalf("secrets has a second key over (id,household_id): %s", name)
		}
	}

	// calendar_objects has no household_id, so it cannot and must not have the
	// key. Its family scope comes from calendars.calendar_id.
	if n := countRows(t, pool, `
		SELECT count(*) FROM information_schema.columns
		 WHERE table_name = 'calendar_objects' AND column_name = 'household_id'`); n != 0 {
		t.Fatal("calendar_objects gained a household_id column: revisit 084's scope note")
	}

	// The two pre-existing keys that look similar stay: they serve different
	// references from the one 084 adds.
	for _, k := range []struct{ table, name, cols string }{
		{"rooms", "rooms_id_property_id_key", "id,property_id"},
		{"memberships", "memberships_household_id_user_id_key", "household_id,user_id"},
	} {
		if got := keyColumns(t, pool, k.table)[k.name]; got != k.cols {
			t.Fatalf("%s: %s covers (%s), want (%s) — 084 must not replace it", k.table, k.name, got, k.cols)
		}
	}
}

// TestFamilyUniqueKeys084_Reversible walks 084 down and up twice. The down must
// remove exactly the 12 keys and nothing else; the up must put them back. Data
// is untouched either way — the keys are redundant over the primary key — so
// the seeded rows are asserted to be still there.
//
// Since 090 (#5519) the keys have DEPENDANTS: 18 composite FKs reference
// (id, household_id) on these tables, so 084's down cannot drop them on its
// own. That is not a defect — golang-migrate would run 090's down first, going
// back from head to 083 — so this walk takes 090 down before 084 and puts 090
// back up after, exactly the order a real down-chain uses. Walking 084 down
// under a 090 schema is not a state that can exist.
func TestFamilyUniqueKeys084_Reversible(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)

	wantKey := func(t *testing.T, present bool) {
		t.Helper()
		for _, table := range familyKeyTables {
			_, ok := keyColumns(t, pool, table)[familyKeyName(table)]
			if ok != present {
				t.Fatalf("%s: %s present=%v, want %v", table, familyKeyName(table), ok, present)
			}
		}
	}

	// seedV2Family gives one household with a property, a room and a vehicle.
	// rooms is the only table in the 12 it fills, so add a row to one more to
	// show a populated table survives the cycle.
	mustExec(t, pool, `INSERT INTO assets (household_id, name) VALUES ($1, 'Furnace')`, f.household)

	for cycle := 0; cycle < 2; cycle++ {
		// 090's composite FKs reference these keys: take them down first, as
		// the real down-chain (090 -> 084) does.
		applyMigration(t, pool, "090_family_composite_fks.down.sql")
		applyMigration(t, pool, "084_family_unique_keys.down.sql")
		wantKey(t, false)

		// Nothing else moved: 075's key on secrets is still there, and the
		// similar-looking keys on rooms/memberships were not touched.
		if _, ok := keyColumns(t, pool, "secrets")["secrets_id_household_id_key"]; !ok {
			t.Fatalf("cycle %d: down dropped secrets_id_household_id_key (075's, not 084's)", cycle)
		}
		for _, k := range []struct{ table, name string }{
			{"rooms", "rooms_id_property_id_key"},
			{"memberships", "memberships_household_id_user_id_key"},
		} {
			if _, ok := keyColumns(t, pool, k.table)[k.name]; !ok {
				t.Fatalf("cycle %d: down dropped %s", cycle, k.name)
			}
		}

		applyMigration(t, pool, "084_family_unique_keys.up.sql")
		wantKey(t, true)
		applyMigration(t, pool, "090_family_composite_fks.up.sql")
	}

	// The seeded rows are the same rows: a unique key cannot have deleted them.
	if n := countRows(t, pool, `SELECT count(*) FROM assets WHERE household_id = $1`, f.household); n != 1 {
		t.Fatalf("assets rows after two cycles = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM rooms WHERE household_id = $1`, f.household); n != 1 {
		t.Fatalf("rooms rows after two cycles = %d, want 1", n)
	}
}
