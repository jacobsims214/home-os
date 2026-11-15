package testutil

// Tests for migration 041 (people and person preferences). See KB
// decisions/domain-model-v2.md, "Contracts for the Stage 2 migrations".

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const sqlstateUniqueViolation = "23505"

func applyMigration(t *testing.T, pool *pgxpool.Pool, name string) {
	t.Helper()
	dir, err := migrationsDir()
	if err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
}

func newUser(t *testing.T, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	return insertID(t, pool, `INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
		uuid.NewString()+"@test", name)
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	mustScan(t, pool.QueryRow(context.Background(), sql, args...), &n)
	return n
}

// Down to 039, seed memberships, up again: every membership gets exactly one
// person; a second up/down/up cycle works.
func TestPeople041_BackfillAndReversible(t *testing.T) {
	pool := NewPool(t)
	// 046 list_items, 047 meal_feedback and 048 files.person_id reference
	// people, 049 replaces entity_household() on top of 047, and 050 adds
	// authorship to all of them; undo them first (newest first).
	applyMigration(t, pool, "056_domain_v2_cleanup.down.sql") // newest first: 056 sits on top of 038-054
	applyMigration(t, pool, "054_change_notifications.down.sql")
	applyMigration(t, pool, "050_authorship.down.sql")
	applyMigration(t, pool, "049_guides.down.sql")
	applyMigration(t, pool, "048_document_metadata.down.sql")
	applyMigration(t, pool, "047_meals.down.sql")
	applyMigration(t, pool, "046_lists.down.sql")
	applyMigration(t, pool, "041_people.down.sql")

	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)
	alice := newUser(t, pool, "Alice")
	bob := newUser(t, pool, "  ") // blank name: falls back to email
	for _, m := range []struct{ h, u uuid.UUID }{{f.household, alice}, {f.household, bob}, {other.household, alice}} {
		mustExec(t, pool, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'family_member')`, m.h, m.u)
	}

	for cycle := 0; cycle < 2; cycle++ {
		applyMigration(t, pool, "041_people.up.sql")
		if n := countRows(t, pool, `SELECT count(*) FROM memberships m WHERE NOT EXISTS (
			SELECT 1 FROM people p WHERE p.household_id = m.household_id AND p.user_id = m.user_id)`); n != 0 {
			t.Fatalf("cycle %d: %d memberships without a person", cycle, n)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM people`); n != 3 {
			t.Fatalf("cycle %d: want 3 people (one per membership), got %d", cycle, n)
		}
		var name string
		mustScan(t, pool.QueryRow(context.Background(), `SELECT display_name FROM people WHERE user_id = $1`, bob), &name)
		if name == "" || name == "  " {
			t.Errorf("blank user name should fall back to email, got %q", name)
		}
		if cycle == 0 {
			applyMigration(t, pool, "041_people.down.sql")
		}
	}

	// A new membership gets a person through the trigger.
	carol := newUser(t, pool, "Carol")
	mustExec(t, pool, `INSERT INTO memberships (household_id, user_id) VALUES ($1, $2)`, f.household, carol)
	if n := countRows(t, pool, `SELECT count(*) FROM people WHERE household_id = $1 AND user_id = $2`, f.household, carol); n != 1 {
		t.Errorf("new membership: want 1 person, got %d", n)
	}
}

// Kids have no login; several of them may exist in one family; a login maps
// to at most one person per family.
func TestPeople041_PeopleWithoutLogin(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)

	kid := insertID(t, pool, `INSERT INTO people (household_id, display_name, relationship, birthdate, school, grade, allergies)
		VALUES ($1, 'Sam', 'child', '2016-04-02', 'Lincoln Elementary', '4th', '{peanuts}') RETURNING id`, f.household)
	insertID(t, pool, `INSERT INTO people (household_id, display_name, relationship) VALUES ($1, 'Jo', 'child') RETURNING id`, f.household)
	var allergies []string
	mustScan(t, pool.QueryRow(ctx, `SELECT allergies FROM people WHERE id = $1`, kid), &allergies)
	if len(allergies) != 1 || allergies[0] != "peanuts" {
		t.Errorf("allergies: got %v", allergies)
	}

	pref := insertID(t, pool, `INSERT INTO person_preferences (household_id, person_id, kind, category, value)
		VALUES ($1, $2, 'dislike', 'food', 'Mushrooms') RETURNING id`, f.household, kid)
	_, err := pool.Exec(ctx, `INSERT INTO person_preferences (household_id, person_id, kind, category, value)
		VALUES ($1, $2, 'dislike', 'Food', 'mushrooms')`, f.household, kid)
	wantSQLState(t, err, sqlstateUniqueViolation)

	// A preference cannot claim another family's person.
	other := seedV2Family(t, pool)
	_, err = pool.Exec(ctx, `INSERT INTO person_preferences (household_id, person_id, kind, value) VALUES ($1, $2, 'like', 'Lego')`, other.household, kid)
	wantSQLState(t, err, sqlstateFKViolation)

	// One person per (family, login).
	u := newUser(t, pool, "Dana")
	mustExec(t, pool, `INSERT INTO memberships (household_id, user_id) VALUES ($1, $2)`, f.household, u)
	_, err = pool.Exec(ctx, `INSERT INTO people (household_id, user_id, display_name) VALUES ($1, $2, 'Dana again')`, f.household, u)
	wantSQLState(t, err, sqlstateUniqueViolation)

	// Leaving the family unlinks the login but keeps the person.
	mustExec(t, pool, `DELETE FROM memberships WHERE household_id = $1 AND user_id = $2`, f.household, u)
	if n := countRows(t, pool, `SELECT count(*) FROM people WHERE household_id = $1 AND display_name = 'Dana' AND user_id IS NULL`, f.household); n != 1 {
		t.Errorf("after leaving: want Dana kept without a login, got %d rows", n)
	}

	// Deleting a login (which cascades its memberships) keeps the person too.
	e := newUser(t, pool, "Eve")
	mustExec(t, pool, `INSERT INTO memberships (household_id, user_id) VALUES ($1, $2)`, f.household, e)
	mustExec(t, pool, `DELETE FROM users WHERE id = $1`, e)
	if n := countRows(t, pool, `SELECT count(*) FROM people WHERE household_id = $1 AND display_name = 'Eve' AND user_id IS NULL`, f.household); n != 1 {
		t.Errorf("after user delete: want Eve kept without a login, got %d rows", n)
	}

	// Deleting the family deletes its people and their preferences.
	mustExec(t, pool, `DELETE FROM households WHERE id = $1`, f.household)
	if n := countRows(t, pool, `SELECT count(*) FROM people WHERE household_id = $1`, f.household); n != 0 {
		t.Errorf("family delete left %d people", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM person_preferences WHERE id = $1`, pref); n != 0 {
		t.Error("family delete left a preference")
	}
}

// 'person' is a registered entity kind: same-family refs work, cross-family
// refs are rejected, and deleting a person re-homes their files and secrets.
func TestPeople041_EntityKindPerson(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)
	kid := insertID(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Sam') RETURNING id`, f.household)

	_, err := pool.Exec(ctx, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'person', $2, 'not ours')`, other.household, kid)
	wantSQLState(t, err, sqlstateFKViolation)
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	_, err = pool.Exec(ctx, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, 'x.pdf', 1, 'person', $3)`, other.household, blob, kid)
	wantSQLState(t, err, sqlstateFKViolation)

	file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, 'report-card.pdf', 1, 'person', $3) RETURNING id`, f.household, blob, kid)
	secret := insertID(t, pool, `INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type, entity_type, entity_id) VALUES ($1, 'x', 'y', 'school portal', 'password', 'person', $2) RETURNING id`, f.household, kid)
	note := insertID(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'person', $2, 'loves dinosaurs') RETURNING id`, f.household, kid)
	link := insertID(t, pool, `INSERT INTO document_links (entity_type, entity_id, link_type, url) VALUES ('person', $1, 'url', 'https://school') RETURNING id`, kid)

	mustExec(t, pool, `DELETE FROM people WHERE id = $1`, kid)

	for table, id := range map[string]uuid.UUID{"files": file, "secrets": secret} {
		var kind string
		var entity uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT entity_type, entity_id FROM `+table+` WHERE id = $1`, id), &kind, &entity)
		if kind != "household" || entity != f.household {
			t.Errorf("%s: want ('household', %s), got (%s, %s)", table, f.household, kind, entity)
		}
	}
	for table, id := range map[string]uuid.UUID{"notes": note, "document_links": link} {
		if n := countRows(t, pool, `SELECT count(*) FROM `+table+` WHERE id = $1`, id); n != 0 {
			t.Errorf("%s about a deleted person survived", table)
		}
	}

	// Down re-homes refs to remaining people and narrows entity_kind again.
	kid2 := insertID(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Jo') RETURNING id`, f.household)
	file2 := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, 'y.pdf', 1, 'person', $3) RETURNING id`, f.household, blob, kid2)
	applyMigration(t, pool, "056_domain_v2_cleanup.down.sql") // newest first: 056 sits on top of 038-054
	applyMigration(t, pool, "054_change_notifications.down.sql")
	applyMigration(t, pool, "050_authorship.down.sql")        // authorship columns on people and the tables below
	applyMigration(t, pool, "049_guides.down.sql")            // entity_household() is replaced on top of 047
	applyMigration(t, pool, "048_document_metadata.down.sql") // files.person_id references people
	applyMigration(t, pool, "047_meals.down.sql")             // meal_feedback references people
	applyMigration(t, pool, "046_lists.down.sql")             // list_items reference people
	applyMigration(t, pool, "041_people.down.sql")
	var kind string
	mustScan(t, pool.QueryRow(ctx, `SELECT entity_type FROM files WHERE id = $1`, file2), &kind)
	if kind != "household" {
		t.Errorf("down: file about a person should be re-homed, got %s", kind)
	}
	_, err = pool.Exec(ctx, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'person', $2, 'x')`, f.household, kid2)
	wantSQLState(t, err, sqlstateCheckViolation)
	applyMigration(t, pool, "041_people.up.sql")
}
