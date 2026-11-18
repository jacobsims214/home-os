package testutil

// Tests for migration 068 (#4772): guide_revisions.change_note, at most 500
// characters, NULL when no note was given.

import (
	"context"
	"strings"
	"testing"
)

func TestGuideRevisionChangeNote068(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	guide := insertID(t, pool, `INSERT INTO guides (household_id, title, body) VALUES ($1, 'Filter', '1. Off') RETURNING id`, f.household)

	mustExec(t, pool, `INSERT INTO guide_revisions (guide_id, body) VALUES ($1, '1. Off')`, guide)
	mustExec(t, pool, `INSERT INTO guide_revisions (guide_id, body, change_note) VALUES ($1, '1. Off', 'new filter size')`, guide)
	mustExec(t, pool, `INSERT INTO guide_revisions (guide_id, body, change_note) VALUES ($1, '1. Off', $2)`, guide, strings.Repeat("é", 500))
	_, err := pool.Exec(ctx, `INSERT INTO guide_revisions (guide_id, body, change_note) VALUES ($1, '1. Off', $2)`, guide, strings.Repeat("x", 501))
	wantSQLState(t, err, sqlstateCheckViolation)
	if n := countRows(t, pool, `SELECT count(*) FROM guide_revisions WHERE guide_id = $1 AND change_note IS NULL`, guide); n != 1 {
		t.Errorf("revisions without a note = %d, want 1", n)
	}

	// Reversible, and the down is safe to run twice.
	applyMigration(t, pool, "068_guide_revision_change_note.down.sql")
	applyMigration(t, pool, "068_guide_revision_change_note.down.sql")
	if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'guide_revisions' AND column_name = 'change_note'`); n != 0 {
		t.Errorf("change_note survived the down")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM guide_revisions WHERE guide_id = $1`, guide); n != 3 {
		t.Errorf("revisions after down = %d, want 3", n)
	}
	applyMigration(t, pool, "068_guide_revision_change_note.up.sql")
	applyMigration(t, pool, "068_guide_revision_change_note.up.sql")
	mustExec(t, pool, `INSERT INTO guide_revisions (guide_id, body, change_note) VALUES ($1, '1. Off', 'again')`, guide)
	_, err = pool.Exec(ctx, `INSERT INTO guide_revisions (guide_id, body, change_note) VALUES ($1, '1. Off', $2)`, guide, strings.Repeat("x", 501))
	wantSQLState(t, err, sqlstateCheckViolation)
}
