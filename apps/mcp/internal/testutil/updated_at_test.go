package testutil

// Tests for migration 077 (#5508): one set_updated_at() BEFORE UPDATE trigger
// on every table that has an updated_at column, replacing the three
// per-table copies from 046/047/049.
//
// The defect it fixes: updated_at moved only when the writer remembered to
// put `updated_at = NOW()` in its own statement, so the same edit bumped the
// column through one code path and not another. 050's log_activity()
// deliberately leaves updated_at out of changed_fields BECAUSE it assumed a
// trigger moved it -- so the log said "updated bill (amount)" while the row
// itself claimed nothing had happened.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// updatedAtTablesAt074 is the catalogue at 074: the 25 tables with the column.
// It is a floor, not an equality. Migrations 075+ are in flight on other
// branches and two of them add a table with an updated_at column
// (payment_accounts in #5497, kb_docs in #5502), so the test asserts that
// every one of these is covered and that the catalogue-derived set is covered
// -- never that the set still has exactly 25 members.
var updatedAtTablesAt074 = []string{
	"assets", "bills", "budget_categories", "calendar_objects", "calendars",
	"files", "guides", "households", "list_items", "lists", "loans",
	"maintenance_schedules", "maintenance_tasks", "meal_feedback",
	"meal_plans", "notes", "people", "person_preferences", "pets",
	"properties", "recipes", "secrets", "users", "vehicles", "vendors",
}

// noUpdatedAtTables are the 14 tables without the column. 077 must not add
// one -- activity_log, the credential tables and the link tables are
// append-only by design, rooms simply never got the column.
var noUpdatedAtTables = []string{
	"activity_log", "agent_tokens", "calendar_changes", "document_links",
	"file_blobs", "file_search_deletions", "guide_revisions", "invitations",
	"memberships", "notifications", "outbox_events", "password_reset_tokens",
	"rooms", "secret_keys",
}

// tablesWithUpdatedAt returns the catalogue's answer, so the assertions below
// compare the schema against itself rather than against a list that could go
// stale in the same edit.
func tablesWithUpdatedAt(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT table_name FROM information_schema.columns
		 WHERE table_schema = 'public' AND column_name = 'updated_at'
		 ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestUpdatedAt077_EveryTableIsCovered is criterion 1: all 25 tables that had
// the column at 074 -- and any table a later migration adds -- have a BEFORE
// UPDATE trigger on the one shared function, and the three bespoke functions
// are gone.
func TestUpdatedAt077_EveryTableIsCovered(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()

	// Every table the catalogue reports is covered -- this is the assertion
	// that stays true as later migrations add tables.
	got := tablesWithUpdatedAt(t, pool)
	if len(got) < len(updatedAtTablesAt074) {
		t.Fatalf("tables with updated_at = %d, want at least %d: %v", len(got), len(updatedAtTablesAt074), got)
	}
	have := make(map[string]bool, len(got))
	for _, s := range got {
		have[s] = true
	}
	for _, name := range updatedAtTablesAt074 {
		if !have[name] {
			t.Errorf("%s has no updated_at column any more (the 25-table floor moved)", name)
		}
	}

	// Every one of them is covered by exactly one set_updated_at trigger,
	// BEFORE UPDATE, FOR EACH ROW -- including a table added after 077.
	for _, table := range got {
		var n int
		mustScan(t, pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_trigger t
			  JOIN pg_class c ON c.oid = t.tgrelid
			  JOIN pg_proc p  ON p.oid = t.tgfoid
			 WHERE c.relname = $1 AND NOT t.tgisinternal
			   AND p.proname = 'set_updated_at'
			   AND (t.tgtype & 2) <> 0   -- BEFORE
			   AND (t.tgtype & 16) <> 0  -- UPDATE
			   AND (t.tgtype & 1) <> 0`, table), &n)
		if n != 1 {
			t.Errorf("%s: %d set_updated_at BEFORE UPDATE triggers, want 1", table, n)
		}
	}

	// The three per-table copies of the body are gone, function and trigger.
	for _, fn := range []string{
		"lists_touch_updated_at", "recipes_touch_updated_at", "guides_touch_updated_at",
	} {
		if n := countRows(t, pool, `SELECT count(*) FROM pg_proc WHERE proname = $1`, fn); n != 0 {
			t.Errorf("function %s still exists", fn)
		}
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname LIKE '%\_touch\_updated\_at'`); n != 0 {
		t.Errorf("%d *_touch_updated_at triggers left, want 0", n)
	}

	// Nothing outside the updated_at set got the trigger.
	var extra []string
	rows, err := pool.Query(ctx, `
		SELECT c.relname FROM pg_trigger t
		  JOIN pg_class c ON c.oid = t.tgrelid
		  JOIN pg_proc p  ON p.oid = t.tgfoid
		 WHERE p.proname = 'set_updated_at' AND NOT t.tgisinternal`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		extra = append(extra, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(extra) != len(got) {
		t.Errorf("set_updated_at triggers = %d, want one per table with updated_at (%d): %v",
			len(extra), len(got), extra)
	}
}

// TestUpdatedAt077_AppendOnlyTablesHaveNoColumn is criterion 5: the 14 tables
// that have no updated_at still have none.
func TestUpdatedAt077_AppendOnlyTablesHaveNoColumn(t *testing.T) {
	pool := NewPool(t)

	got := tablesWithUpdatedAt(t, pool)
	have := make(map[string]bool, len(got))
	for _, s := range got {
		have[s] = true
	}
	for _, table := range noUpdatedAtTables {
		if have[table] {
			t.Errorf("%s has an updated_at column; 077 must not add one", table)
		}
	}
}

// TestUpdatedAt077_UpdateBumpsTheColumn is criterion 2. bills is the table the
// schema review proved the defect on: it had no trigger, so the statement
// `UPDATE bills SET amount = amount + 1` left updated_at where it was.
func TestUpdatedAt077_UpdateBumpsTheColumn(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)

	bill := insertID(t, pool,
		`INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Netflix', 15.99) RETURNING id`,
		f.household)

	var before, after time.Time
	mustScan(t, pool.QueryRow(ctx, `SELECT updated_at FROM bills WHERE id = $1`, bill), &before)

	// No updated_at in the statement: the trigger is the only thing that can
	// move it.
	mustExec(t, pool, `UPDATE bills SET amount = amount + 1 WHERE id = $1`, bill)

	mustScan(t, pool.QueryRow(ctx, `SELECT updated_at FROM bills WHERE id = $1`, bill), &after)
	if !after.After(before) {
		t.Errorf("bills.updated_at = %v after UPDATE ... SET amount = amount + 1, want later than %v", after, before)
	}
}

// TestUpdatedAt077_NoOpUpdateWritesNoActivity is criterion 3 -- the
// regression risk. The trigger bumps updated_at unconditionally; if
// log_activity() counted that as a change, every no-op UPDATE would write a
// spurious row to the family feed.
func TestUpdatedAt077_NoOpUpdateWritesNoActivity(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	alex := newUser(t, pool, "Alex")

	var bill uuid.UUID
	asActor(t, pool, actor{user: alex.String()}, func(tx pgx.Tx) {
		bill = txInsertID(t, tx, `INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Netflix', 15.99) RETURNING id`, f.household)
	})
	before := activityFor(t, pool, bill)
	if len(before) != 1 || before[0].action != "create" {
		t.Fatalf("setup: want one create row, got %+v", before)
	}

	// A statement that changes nothing at all.
	asActor(t, pool, actor{user: alex.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE bills SET amount = amount WHERE id = $1`, bill)
	})

	after := activityFor(t, pool, bill)
	if len(after) != len(before) {
		t.Errorf("no-op UPDATE wrote %d activity rows, want 0: %+v", len(after)-len(before), after[len(before):])
	}

	// And the column still moved, so the two behaviours are independent.
	var moved bool
	mustScan(t, pool.QueryRow(ctx, `SELECT updated_at > created_at FROM bills WHERE id = $1`, bill), &moved)
	if !moved {
		t.Error("a no-op UPDATE should still bump updated_at (it is a write); it did not")
	}
}

// TestUpdatedAt077_RealEditLogsTheSameRow is criterion 4: the same statement
// writes the same activity row as it did at 074. Comparing summary and
// changed_fields is the point -- updated_at must not creep into changed_fields.
func TestUpdatedAt077_RealEditLogsTheSameRow(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)
	alex := newUser(t, pool, "Alex")

	var bill uuid.UUID
	asActor(t, pool, actor{user: alex.String()}, func(tx pgx.Tx) {
		bill = txInsertID(t, tx, `INSERT INTO bills (household_id, name, amount, notes) VALUES ($1, 'Netflix', 15.99, 'first') RETURNING id`, f.household)
	})

	asActor(t, pool, actor{user: alex.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE bills SET amount = 21.50 WHERE id = $1`, bill)
	})

	rows := activityFor(t, pool, bill)
	if len(rows) != 2 {
		t.Fatalf("want create + one update, got %d: %+v", len(rows), rows)
	}
	upd := rows[1]
	if upd.action != "update" {
		t.Errorf("action = %q, want update", upd.action)
	}
	if want := `updated bill "Netflix" (amount)`; upd.summary != want {
		t.Errorf("summary = %q, want %q", upd.summary, want)
	}
	if len(upd.changed) != 1 || upd.changed[0] != "amount" {
		t.Errorf("changed_fields = %v, want [amount] (updated_at must stay excluded)", upd.changed)
	}
	if !eqID(upd.actorUser, alex) {
		t.Errorf("actor = %v, want %s", upd.actorUser, alex)
	}
}

// TestUpdatedAt077_Reversible is criterion 6: down restores exactly what 074
// had -- the three per-table functions and triggers, no set_updated_at -- and
// up puts the single implementation back.
func TestUpdatedAt077_Reversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	bill := insertID(t, pool,
		`INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Netflix', 15.99) RETURNING id`, f.household)

	bumpedByUpdate := func() bool {
		t.Helper()
		var before, after time.Time
		mustScan(t, pool.QueryRow(ctx, `SELECT updated_at FROM bills WHERE id = $1`, bill), &before)
		mustExec(t, pool, `UPDATE bills SET amount = amount + 1 WHERE id = $1`, bill)
		mustScan(t, pool.QueryRow(ctx, `SELECT updated_at FROM bills WHERE id = $1`, bill), &after)
		return after.After(before)
	}

	if !bumpedByUpdate() {
		t.Fatal("at 077: bills.updated_at did not move")
	}

	applyMigration(t, pool, "077_set_updated_at.down.sql")

	if n := countRows(t, pool, `SELECT count(*) FROM pg_proc WHERE proname = 'set_updated_at'`); n != 0 {
		t.Error("down left set_updated_at()")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname LIKE '%\_set\_updated\_at'`); n != 0 {
		t.Errorf("down left %d *_set_updated_at triggers", n)
	}
	for _, fn := range []string{
		"lists_touch_updated_at", "recipes_touch_updated_at", "guides_touch_updated_at",
	} {
		if n := countRows(t, pool, `SELECT count(*) FROM pg_proc WHERE proname = $1`, fn); n != 1 {
			t.Errorf("down did not restore function %s", fn)
		}
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname LIKE '%\_touch\_updated\_at'`); n != 3 {
		t.Errorf("down restored %d *_touch_updated_at triggers, want 3", n)
	}
	// At 074 bills had no trigger, so its column stops moving again.
	if bumpedByUpdate() {
		t.Error("after 077 down, bills.updated_at still moved; the trigger is not really gone")
	}

	applyMigration(t, pool, "077_set_updated_at.up.sql")

	if n, want := countRows(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname LIKE '%\_set\_updated\_at'`),
		len(tablesWithUpdatedAt(t, pool)); n != want {
		t.Errorf("up again: %d set_updated_at triggers, want one per updated_at table (%d)", n, want)
	}
	if !bumpedByUpdate() {
		t.Error("up again: bills.updated_at did not move")
	}
}

// TestUpdatedAt077_TriggerFiresLast is the ordering assertion. set_authorship()
// decides "only bookkeeping columns changed" by comparing the whole row, so if
// set_updated_at() ran first it would make every no-op look like an edit and
// rewrite updated_by -- blaming whoever ran a statement that changed nothing.
// Proved both ways on a live cluster before this test existed: with the name
// below, updated_by survives a no-op; renamed to fire earlier, it is set to
// NULL.
func TestUpdatedAt077_TriggerFiresLast(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	alex := newUser(t, pool, "Alex")

	var bill uuid.UUID
	asActor(t, pool, actor{user: alex.String()}, func(tx pgx.Tx) {
		bill = txInsertID(t, tx, `INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Netflix', 15.99) RETURNING id`, f.household)
	})
	var owner *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT updated_by FROM bills WHERE id = $1`, bill), &owner)
	if !eqID(owner, alex) {
		t.Fatalf("setup: updated_by = %v, want %s", owner, alex)
	}

	// No actor on this write at all. If set_updated_at() fired before
	// bills_authorship, the authorship trigger would see a "changed" row and
	// rewrite updated_by to NULL.
	asActor(t, pool, actor{}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE bills SET amount = amount WHERE id = $1`, bill)
	})

	var after *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT updated_by FROM bills WHERE id = $1`, bill), &after)
	if !eqID(after, alex) {
		t.Errorf("updated_by = %v after a no-op UPDATE with no actor, want it unchanged at %s: "+
			"set_updated_at() must fire after bills_authorship (name order)", after, alex)
	}
}

// TestUpdatedAt077_UpIsIdempotentOnTheCatalogueGuard asserts that re-running
// up on an already-077 schema does not double the triggers: DROP-then-CREATE
// in the loop means it replaces, never stacks.
func TestUpdatedAt077_RerunDoesNotStackTriggers(t *testing.T) {
	pool := NewPool(t)

	applyMigration(t, pool, "077_set_updated_at.up.sql")

	if n, want := countRows(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname LIKE '%\_set\_updated\_at'`),
		len(tablesWithUpdatedAt(t, pool)); n != want {
		t.Errorf("re-running 077 up: %d set_updated_at triggers, want one per updated_at table (%d)", n, want)
	}
	// One trigger per table, never two.
	var worst int
	mustScan(t, pool.QueryRow(context.Background(), `
		SELECT coalesce(max(n), 0) FROM (
		  SELECT count(*) AS n FROM pg_trigger
		   WHERE tgname LIKE '%\_set\_updated\_at' GROUP BY tgrelid) s`), &worst)
	if worst != 1 {
		t.Errorf("a table has %d set_updated_at triggers, want 1", worst)
	}
}
