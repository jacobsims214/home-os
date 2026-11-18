package testutil

// Tests for migration 066 (#4800): list_items.checked_by, filled by trigger
// from app.user_id when the actor is a member of the item's family, with a
// same-family FK to memberships.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestListItemCheckedBy066(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	amber := newMember(t, pool, f.household, "Amber")
	sitter := newUser(t, pool, "Sam")
	mustExec(t, pool, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'housesitter')`, f.household, sitter)
	other := seedV2Family(t, pool)
	outsider := newMember(t, pool, other.household, "Olga")

	list := insertID(t, pool, `INSERT INTO lists (household_id, name, kind) VALUES ($1, 'Groceries', 'grocery') RETURNING id`, f.household)
	item := func(text string) uuid.UUID {
		return insertID(t, pool, `INSERT INTO list_items (list_id, text) VALUES ($1, $2) RETURNING id`, list, text)
	}
	milk, eggs, jam := item("milk"), item("eggs"), item("jam")
	checkedBy := func(id uuid.UUID) *uuid.UUID {
		t.Helper()
		var by *uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT checked_by FROM list_items WHERE id = $1`, id), &by)
		return by
	}
	set := func(a actor, id uuid.UUID, checked bool) {
		t.Helper()
		asActor(t, pool, a, func(tx pgx.Tx) {
			txExec(t, tx, `UPDATE list_items SET checked = $2 WHERE id = $1`, id, checked)
		})
	}

	set(actor{user: amber.String()}, milk, true)
	set(actor{user: sitter.String()}, eggs, true) // housesitters are members too
	if by := checkedBy(milk); !eqID(by, amber) {
		t.Errorf("milk checked_by = %v, want Amber", by)
	}
	if by := checkedBy(eggs); !eqID(by, sitter) {
		t.Errorf("eggs checked_by = %v, want Sam", by)
	}

	// Stays checked: the first checker is kept, also across other edits.
	set(actor{user: sitter.String()}, milk, true)
	asActor(t, pool, actor{user: sitter.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE list_items SET text = 'oat milk' WHERE id = $1`, milk)
	})
	if by := checkedBy(milk); !eqID(by, amber) {
		t.Errorf("milk checked_by after re-check/edit = %v, want Amber", by)
	}

	// Uncheck clears it.
	set(actor{user: amber.String()}, milk, false)
	if by := checkedBy(milk); by != nil {
		t.Errorf("unchecked milk checked_by = %v", by)
	}

	// No actor, a non-member actor or garbage: checked, but anonymous.
	set(actor{}, jam, true)
	if by := checkedBy(jam); by != nil {
		t.Errorf("no actor: checked_by = %v", by)
	}
	set(actor{}, jam, false)
	set(actor{user: outsider.String()}, jam, true)
	if by := checkedBy(jam); by != nil {
		t.Errorf("outsider: checked_by = %v", by)
	}
	set(actor{}, jam, false)
	set(actor{user: "not-a-uuid"}, jam, true)
	if by := checkedBy(jam); by != nil {
		t.Errorf("garbage actor: checked_by = %v", by)
	}

	// An INSERT that is already checked records the actor.
	asActor(t, pool, actor{user: amber.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `INSERT INTO list_items (list_id, text, checked) VALUES ($1, 'bread', true)`, list)
	})
	var breadBy *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT checked_by FROM list_items WHERE list_id = $1 AND text = 'bread'`, list), &breadBy)
	if !eqID(breadBy, amber) {
		t.Errorf("checked insert: checked_by = %v", breadBy)
	}

	// Same-family FK: another family's user is refused (23503).
	_, err := pool.Exec(ctx, `UPDATE list_items SET checked_by = $1 WHERE id = $2`, outsider, eggs)
	wantSQLState(t, err, "23503")

	// Removing the membership clears only checked_by.
	mustExec(t, pool, `DELETE FROM memberships WHERE household_id = $1 AND user_id = $2`, f.household, sitter)
	var checked bool
	var by *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT checked, checked_by FROM list_items WHERE id = $1`, eggs), &checked, &by)
	if !checked || by != nil {
		t.Errorf("after membership removal: checked=%v checked_by=%v", checked, by)
	}

	// Reversible, and the down is safe to run twice.
	applyMigration(t, pool, "066_list_item_checked_by.down.sql")
	applyMigration(t, pool, "066_list_item_checked_by.down.sql")
	if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'list_items' AND column_name = 'checked_by'`); n != 0 {
		t.Errorf("checked_by survived the down")
	}
	set(actor{user: amber.String()}, milk, true) // no trigger left to fail
	applyMigration(t, pool, "066_list_item_checked_by.up.sql")
	set(actor{user: amber.String()}, milk, false)
	set(actor{user: amber.String()}, milk, true)
	if by := checkedBy(milk); !eqID(by, amber) {
		t.Errorf("after down/up: checked_by = %v", by)
	}
}
