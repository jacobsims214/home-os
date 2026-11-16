package testutil

// Tests for migration 047 (recipes, meal plans, meal feedback). See KB
// decisions/domain-model-v2.md, "047 meals".

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Up/down/up with meals in place: down removes the tables and re-homes files
// about a recipe; while 047 is down the narrower entity_kind rejects
// 'recipe' and keeps 'list'; up again works.
func TestMeals047_Reversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)

	recipe := insertID(t, pool, `INSERT INTO recipes (household_id, title, ingredients) VALUES ($1, 'Tacos', '[{"name":"tortillas","quantity":8}]') RETURNING id`, f.household)
	meal := insertID(t, pool, `INSERT INTO meal_plans (household_id, date, slot, recipe_id) VALUES ($1, '2026-11-10', 'dinner', $2) RETURNING id`, f.household, recipe)
	mustExec(t, pool, `INSERT INTO meal_feedback (meal_plan_id, rating) VALUES ($1, 5)`, meal)
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, 'card.jpg', 1, 'recipe', $3) RETURNING id`, f.household, blob, recipe)
	list := insertID(t, pool, `INSERT INTO lists (household_id, name, entity_type, entity_id) VALUES ($1, 'Taco night', 'meal_plan', $2) RETURNING id`, f.household, meal)

	// 049 replaces entity_household() on top of 047, and 050 adds authorship
	// on top of both; undo them first.
	applyMigration(t, pool, "056_domain_v2_cleanup.down.sql") // newest first: 056 sits on top of 038-054
	applyMigration(t, pool, "055_activity_event_titles.down.sql")
	applyMigration(t, pool, "054_change_notifications.down.sql") // its trigger is on 050's activity_log
	applyMigration(t, pool, "050_authorship.down.sql")
	applyMigration(t, pool, "049_guides.down.sql")
	applyMigration(t, pool, "047_meals.down.sql")
	if n := countRows(t, pool, `SELECT count(*) FROM pg_tables WHERE tablename IN ('recipes', 'meal_plans', 'meal_feedback')`); n != 0 {
		t.Fatalf("down: %d meal tables left", n)
	}
	var kind string
	mustScan(t, pool.QueryRow(ctx, `SELECT entity_type FROM files WHERE id = $1`, file), &kind)
	if kind != "household" {
		t.Errorf("down: file about a recipe should be re-homed, got %s", kind)
	}
	mustScan(t, pool.QueryRow(ctx, `SELECT entity_type FROM lists WHERE id = $1`, list), &kind)
	if kind != "household" {
		t.Errorf("down: list about a meal should be re-homed, got %s", kind)
	}
	_, err := pool.Exec(ctx, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'recipe', $1, 'x')`, f.household)
	wantSQLState(t, err, sqlstateCheckViolation)
	// Earlier Stage 2 kinds survive the down.
	mustExec(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'list', $2, 'ok')`, f.household, list)
	// person_preferences (041) is untouched.
	if n := countRows(t, pool, `SELECT count(*) FROM pg_tables WHERE tablename = 'person_preferences'`); n != 1 {
		t.Error("down: person_preferences should remain")
	}

	applyMigration(t, pool, "047_meals.up.sql")
	recipe = insertID(t, pool, `INSERT INTO recipes (household_id, title) VALUES ($1, 'Again') RETURNING id`, f.household)
	mustExec(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'recipe', $2, 'ok')`, f.household, recipe)
	applyMigration(t, pool, "047_meals.down.sql")
	applyMigration(t, pool, "047_meals.up.sql")
	applyMigration(t, pool, "049_guides.up.sql")
	applyMigration(t, pool, "050_authorship.up.sql")
}

// Checks, same-family rules, title copy, delete behaviour and the indexes.
func TestMeals047_Rules(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)

	// Recipes: defaults and checks.
	recipe := insertID(t, pool, `INSERT INTO recipes (household_id, title, servings, prep_minutes, cook_minutes, ingredients, tags)
		VALUES ($1, 'Chili', 6, 15, 60, '[{"name":"beans","quantity":"2","unit":"can","section":"canned"},{"name":"salt"}]', '{weeknight,spicy}') RETURNING id`, f.household)
	plain := insertID(t, pool, `INSERT INTO recipes (household_id, title) VALUES ($1, 'Toast') RETURNING id`, f.household)
	var ingredients string
	mustScan(t, pool.QueryRow(ctx, `SELECT ingredients::text FROM recipes WHERE id = $1`, plain), &ingredients)
	if ingredients != "[]" {
		t.Errorf("ingredients default: got %s", ingredients)
	}
	for _, bad := range []string{
		`{"name":"x"}`,     // not an array
		`["salt"]`,         // element not an object
		`[{"quantity":1}]`, // no name
		`[{"name":"  "}]`,  // blank name
		`[{"name":"x","quantity":true}]`,
		`[{"name":"x","unit":1}]`,
	} {
		_, err := pool.Exec(ctx, `INSERT INTO recipes (household_id, title, ingredients) VALUES ($1, 'x', $2::jsonb)`, f.household, bad)
		wantSQLState(t, err, sqlstateCheckViolation)
	}
	_, err := pool.Exec(ctx, `INSERT INTO recipes (household_id, title, servings) VALUES ($1, 'x', 0)`, f.household)
	wantSQLState(t, err, sqlstateCheckViolation)
	_, err = pool.Exec(ctx, `INSERT INTO recipes (household_id, title) VALUES ($1, ' ')`, f.household)
	wantSQLState(t, err, sqlstateCheckViolation)
	if n := countRows(t, pool, `SELECT count(*) FROM recipes WHERE household_id = $1 AND tags @> '{weeknight}'`, f.household); n != 1 {
		t.Errorf("tags: want 1 weeknight recipe, got %d", n)
	}

	// Meal plans: title copied from the recipe; slot check; one per slot.
	dinner := insertID(t, pool, `INSERT INTO meal_plans (household_id, date, slot, recipe_id) VALUES ($1, '2026-11-01', 'dinner', $2) RETURNING id`, f.household, recipe)
	var title string
	mustScan(t, pool.QueryRow(ctx, `SELECT title FROM meal_plans WHERE id = $1`, dinner), &title)
	if title != "Chili" {
		t.Errorf("title from recipe: got %q", title)
	}
	mustExec(t, pool, `INSERT INTO meal_plans (household_id, date, slot, title) VALUES ($1, '2026-11-01', 'lunch', 'Leftovers')`, f.household)
	mustExec(t, pool, `INSERT INTO meal_plans (household_id, date, slot, title) VALUES ($1, '2026-11-01', 'dinner', 'Theirs')`, other.household)
	_, err = pool.Exec(ctx, `INSERT INTO meal_plans (household_id, date, slot, title) VALUES ($1, '2026-11-01', 'dinner', 'Pizza')`, f.household)
	wantSQLState(t, err, "23505")
	_, err = pool.Exec(ctx, `INSERT INTO meal_plans (household_id, date, slot, title) VALUES ($1, '2026-11-02', 'brunch', 'x')`, f.household)
	wantSQLState(t, err, sqlstateCheckViolation)
	_, err = pool.Exec(ctx, `INSERT INTO meal_plans (household_id, date, slot) VALUES ($1, '2026-11-02', 'dinner')`, f.household)
	wantSQLState(t, err, "23502") // no recipe and no title
	// Another family's recipe is rejected (composite FK).
	_, err = pool.Exec(ctx, `INSERT INTO meal_plans (household_id, date, slot, recipe_id) VALUES ($1, '2026-11-02', 'dinner', $2)`, other.household, recipe)
	wantSQLState(t, err, sqlstateFKViolation)

	// Feedback: household from the meal; rating range; content; one per person.
	kid := insertID(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Sam') RETURNING id`, f.household)
	otherKid := insertID(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Jo') RETURNING id`, other.household)
	fb := insertID(t, pool, `INSERT INTO meal_feedback (meal_plan_id, person_id, rating, comment) VALUES ($1, $2, 4, 'more cheese') RETURNING id`, dinner, kid)
	var hh uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT household_id FROM meal_feedback WHERE id = $1`, fb), &hh)
	if hh != f.household {
		t.Errorf("feedback household: got %s", hh)
	}
	_, err = pool.Exec(ctx, `INSERT INTO meal_feedback (meal_plan_id, person_id, rating) VALUES ($1, $2, 3)`, dinner, kid)
	wantSQLState(t, err, "23505")
	_, err = pool.Exec(ctx, `INSERT INTO meal_feedback (meal_plan_id, rating) VALUES ($1, 6)`, dinner)
	wantSQLState(t, err, sqlstateCheckViolation)
	_, err = pool.Exec(ctx, `INSERT INTO meal_feedback (meal_plan_id, comment) VALUES ($1, '  ')`, dinner)
	wantSQLState(t, err, sqlstateCheckViolation)
	_, err = pool.Exec(ctx, `INSERT INTO meal_feedback (meal_plan_id, person_id, rating) VALUES ($1, $2, 3)`, dinner, otherKid)
	wantSQLState(t, err, sqlstateFKViolation)
	_, err = pool.Exec(ctx, `INSERT INTO meal_feedback (meal_plan_id, household_id, rating) VALUES ($1, $2, 3)`, dinner, other.household)
	wantSQLState(t, err, sqlstateFKViolation)

	// updated_at moves on UPDATE.
	var createdAt, updatedAt time.Time
	mustScan(t, pool.QueryRow(ctx, `UPDATE meal_feedback SET rating = 5 WHERE id = $1 RETURNING created_at, updated_at`, fb), &createdAt, &updatedAt)
	if !updatedAt.After(createdAt) {
		t.Errorf("feedback updated_at did not move: %v %v", createdAt, updatedAt)
	}
	mustScan(t, pool.QueryRow(ctx, `UPDATE recipes SET servings = 8 WHERE id = $1 RETURNING created_at, updated_at`, recipe), &createdAt, &updatedAt)
	if !updatedAt.After(createdAt) {
		t.Errorf("recipe updated_at did not move: %v %v", createdAt, updatedAt)
	}

	// Deleting a person keeps their rating, unattributed (SET NULL).
	mustExec(t, pool, `DELETE FROM people WHERE id = $1`, kid)
	var person *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT person_id FROM meal_feedback WHERE id = $1`, fb), &person)
	if person != nil {
		t.Errorf("person delete: person_id should be NULL, got %v", person)
	}

	// Kind 'recipe': notes about it go with it; the meal keeps its title and
	// its feedback (history survives).
	note := insertID(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'recipe', $2, 'from grandma') RETURNING id`, f.household, recipe)
	_, err = pool.Exec(ctx, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'recipe', $2, 'x')`, other.household, recipe)
	wantSQLState(t, err, sqlstateFKViolation)
	mustExec(t, pool, `DELETE FROM recipes WHERE id = $1`, recipe)
	var recipeID *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT recipe_id, title FROM meal_plans WHERE id = $1`, dinner), &recipeID, &title)
	if recipeID != nil || title != "Chili" {
		t.Errorf("recipe delete: meal should keep title with no recipe, got %v %q", recipeID, title)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM notes WHERE id = $1`, note); n != 0 {
		t.Error("note about a deleted recipe survived")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM meal_feedback WHERE meal_plan_id = $1`, dinner); n != 1 {
		t.Errorf("recipe delete: feedback lost, got %d", n)
	}

	// Kind 'meal_plan': a file about it is re-homed; feedback cascades.
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, 'dinner.jpg', 1, 'meal_plan', $3) RETURNING id`, f.household, blob, dinner)
	mustExec(t, pool, `DELETE FROM meal_plans WHERE id = $1`, dinner)
	var kind string
	mustScan(t, pool.QueryRow(ctx, `SELECT entity_type FROM files WHERE id = $1`, file), &kind)
	if kind != "household" {
		t.Errorf("meal delete: file should be re-homed, got %s", kind)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM meal_feedback WHERE meal_plan_id = $1`, dinner); n != 0 {
		t.Errorf("meal delete: %d feedback rows survived", n)
	}

	// History query uses the (household_id, date, slot) index.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, `EXPLAIN SELECT * FROM meal_plans WHERE household_id = $1 AND date BETWEEN '2026-10-01' AND '2026-11-30' ORDER BY date`, f.household)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	rows.Close()
	conn.Release()
	if !strings.Contains(plan.String(), "meal_plans_household_date_slot_key") {
		t.Errorf("history query plan does not use the index:\n%s", plan.String())
	}

	// Deleting the family removes its meals.
	mustExec(t, pool, `DELETE FROM households WHERE id = $1`, f.household)
	for _, table := range []string{"recipes", "meal_plans", "meal_feedback"} {
		if n := countRows(t, pool, `SELECT count(*) FROM `+table+` WHERE household_id = $1`, f.household); n != 0 {
			t.Errorf("family delete left %d %s", n, table)
		}
	}
}
