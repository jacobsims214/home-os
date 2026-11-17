package testutil

// Tests for migration 071 (#4821): people.color, a palette name that is never
// free text, defaulted from the id so every person gets a stable colour
// without naming the column.

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPersonColor071 covers the column, the palette CHECK, the defaulting
// trigger, the memberships trigger's path through it, and the down/up cycle.
func TestPersonColor071(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)

	colorOf := func(id uuid.UUID) string {
		t.Helper()
		var c *string
		mustScan(t, pool.QueryRow(ctx, `SELECT color FROM people WHERE id = $1`, id), &c)
		if c == nil {
			return "<nil>"
		}
		return *c
	}

	t.Run("a person created without a colour gets one derived from the id", func(t *testing.T) {
		a := insertID(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Ada') RETURNING id`, f.household)
		b := insertID(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Bo') RETURNING id`, f.household)
		for _, id := range []uuid.UUID{a, b} {
			got := colorOf(id)
			if !isPaletteColour(got) {
				t.Errorf("person %s: color %q is not a palette name", id, got)
			}
			// Deterministic: the same id gives the same name on every read,
			// which is what makes the default stable per person.
			var again string
			mustScan(t, pool.QueryRow(ctx, `SELECT person_default_color($1)`, id), &again)
			if again != got {
				t.Errorf("person %s: stored %q but person_default_color says %q", id, got, again)
			}
		}
		// The derivation is the id's first byte modulo the palette size, so
		// every id lands on a name and ids that differ in that byte do not
		// all land on the same one.
		for _, c := range []struct {
			id   string
			want string
		}{
			{"00000000-0000-0000-0000-000000000000", "red"},
			{"01000000-0000-0000-0000-000000000000", "orange"},
			{"07000000-0000-0000-0000-000000000000", "violet"},
			{"ff000000-0000-0000-0000-000000000000", "violet"}, // 255 % 8 = 7
		} {
			var got string
			mustScan(t, pool.QueryRow(ctx, `SELECT person_default_color($1)`, c.id), &got)
			if got != c.want {
				t.Errorf("person_default_color(%s) = %q, want %q", c.id, got, c.want)
			}
		}
	})

	t.Run("an explicit colour is kept", func(t *testing.T) {
		id := insertID(t, pool, `INSERT INTO people (household_id, display_name, color) VALUES ($1, 'Cy', 'violet') RETURNING id`, f.household)
		if got := colorOf(id); got != "violet" {
			t.Errorf("color = %q, want violet", got)
		}
		mustExec(t, pool, `UPDATE people SET color = 'teal' WHERE id = $1`, id)
		if got := colorOf(id); got != "teal" {
			t.Errorf("after update: color = %q, want teal", got)
		}
	})

	t.Run("anything off the palette is refused by the database", func(t *testing.T) {
		for _, bad := range []string{
			"#ff0000", "RED", " red", "red ", "", "purple",
			"rgb(255,0,0)", "--accent", "url(x)", "red;",
		} {
			_, err := pool.Exec(ctx, `INSERT INTO people (household_id, display_name, color) VALUES ($1, 'Bad', $2)`, f.household, bad)
			wantSQLState(t, err, sqlstateCheckViolation)
		}
		// A long value is stopped by the column width rather than the CHECK;
		// either way nothing off the palette can be stored.
		for _, bad := range []string{
			"red; background: url(http://x/y.png)",
			"var(--mantine-color-red-6)",
			string(bytes.Repeat([]byte("x"), 200)),
		} {
			_, err := pool.Exec(ctx, `INSERT INTO people (household_id, display_name, color) VALUES ($1, 'Bad', $2)`, f.household, bad)
			wantSQLState(t, err, "22001") // string_data_right_truncation
		}
		// An UPDATE cannot sneak one in either.
		id := insertID(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Fix') RETURNING id`, f.household)
		_, err := pool.Exec(ctx, `UPDATE people SET color = 'purple' WHERE id = $1`, id)
		wantSQLState(t, err, sqlstateCheckViolation)
		// And a NULL is not storable either: the column is NOT NULL and the
		// trigger fills it, so a NULL insert is a defaulted row, not an error.
		nullID := insertID(t, pool, `INSERT INTO people (household_id, display_name, color) VALUES ($1, 'Null', NULL) RETURNING id`, f.household)
		if got := colorOf(nullID); !isPaletteColour(got) {
			t.Errorf("NULL color insert stored %q, want a palette name", got)
		}
	})

	t.Run("a member's person gets a colour through the 041 trigger", func(t *testing.T) {
		u := newUser(t, pool, "Dana")
		mustExec(t, pool, `INSERT INTO memberships (household_id, user_id) VALUES ($1, $2)`, f.household, u)
		var id uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT id FROM people WHERE household_id = $1 AND user_id = $2`, f.household, u), &id)
		if got := colorOf(id); !isPaletteColour(got) {
			t.Errorf("membership-created person: color %q, want a palette name", got)
		}
	})

	// Down drops the column, its CHECK and the trigger; up re-derives every
	// person's colour from their id, so an explicit choice is lost and the
	// rest land where they started.
	t.Run("reversible", func(t *testing.T) {
		chosen := insertID(t, pool, `INSERT INTO people (household_id, display_name, color) VALUES ($1, 'Eve', 'yellow') RETURNING id`, f.household)
		derived := personDefaultColor(t, pool, chosen)
		if derived == "yellow" {
			t.Skip("chosen colour equals the derived one; pick another palette name")
		}

		applyMigration(t, pool, "073_legacy_bill_events.down.sql") // newest first: 073 sits on top of everything below
		applyMigration(t, pool, "071_person_color.down.sql")
		applyMigration(t, pool, "071_person_color.down.sql") // safe twice
		if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'people' AND column_name = 'color'`); n != 0 {
			t.Fatalf("down left people.color")
		}
		if n := countRows(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname = 'people_default_color'`); n != 0 {
			t.Fatalf("down left the defaulting trigger")
		}

		applyMigration(t, pool, "071_person_color.up.sql")
		applyMigration(t, pool, "071_person_color.up.sql")       // safe twice
		applyMigration(t, pool, "073_legacy_bill_events.up.sql") // back to the head
		if got := colorOf(chosen); got != derived {
			t.Errorf("after down/up: color = %q, want the derived %q", got, derived)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM people WHERE color IS NULL`); n != 0 {
			t.Errorf("%d people with no colour after re-up", n)
		}
		_, err := pool.Exec(ctx, `UPDATE people SET color = 'purple' WHERE id = $1`, chosen)
		wantSQLState(t, err, sqlstateCheckViolation)
	})
}

// personDefaultColor returns the colour person_default_color derives for id.
func personDefaultColor(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var c string
	mustScan(t, pool.QueryRow(context.Background(), `SELECT person_default_color($1)`, id), &c)
	return c
}

// isPaletteColour reports whether c is one of migration 071's eight names.
func isPaletteColour(c string) bool {
	switch c {
	case "red", "orange", "yellow", "green", "teal", "cyan", "blue", "violet":
		return true
	}
	return false
}
