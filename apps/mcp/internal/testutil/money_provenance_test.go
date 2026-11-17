package testutil

// Tests for migration 071 (#4941): a bill's source document (same-family FK),
// the provenance columns on files, and the before/after values the change
// feed keeps for money and dates.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// rawDocument inserts a text document the way document_upload does for a
// paste: verbatim body, text_origin 'raw', fingerprint.
func rawDocument(t *testing.T, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, household uuid.UUID, text string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var blob uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO file_blobs (data, size) VALUES ($1, $2) RETURNING id`,
		[]byte(text), len(text)), &blob)
	sum := sha256.Sum256([]byte(text))
	var id uuid.UUID
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO files (household_id, blob_id, name, content_type, size, entity_type, entity_id,
		                    extracted_text, ocr_status, text_origin, text_sha256)
		 VALUES ($1, $2, 'paste.txt', 'text/plain', $3, 'household', $1, $4, 'done', 'raw', $5)
		 RETURNING id`,
		household, blob, len(text), text, sum[:]), &id)
	return id
}

func TestMoneyProvenance071_SourceDocumentLink(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)
	ctx := context.Background()
	doc := rawDocument(t, pool, f.household, "Well visit copay — $35.00")

	bill := insertID(t, pool,
		`INSERT INTO bills (household_id, name, amount, due_day, source_document_id)
		 VALUES ($1, 'Copay', 35, 10, $2) RETURNING id`, f.household, doc)

	var stored uuid.UUID
	var title string
	mustScan(t, pool.QueryRow(ctx,
		`SELECT b.source_document_id, COALESCE(f.title, f.name)
		   FROM bills b JOIN files f ON f.id = b.source_document_id WHERE b.id = $1`, bill),
		&stored, &title)
	if stored != doc || title != "paste.txt" {
		t.Errorf("link = %s %q, want %s", stored, title, doc)
	}

	// Deleting the document keeps the bill and clears the link (the change is
	// logged, so the unlink is traceable).
	mustExec(t, pool, `DELETE FROM files WHERE id = $1`, doc)
	var after *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT source_document_id FROM bills WHERE id = $1`, bill), &after)
	if after != nil {
		t.Errorf("source_document_id = %v after the document was deleted, want NULL", after)
	}

	// Another family's document cannot be linked.
	otherDoc := rawDocument(t, pool, insertID(t, pool,
		`INSERT INTO households (name) VALUES ('other family') RETURNING id`), "theirs")
	var pgErr error
	_, pgErr = pool.Exec(ctx,
		`INSERT INTO bills (household_id, name, amount, source_document_id) VALUES ($1, 'x', 1, $2)`,
		f.household, otherDoc)
	if pgErr == nil || !strings.Contains(pgErr.Error(), "bills_source_document_fkey") {
		t.Errorf("a cross-family link should violate the composite FK, got %v", pgErr)
	}
}

func TestMoneyProvenance071_AmountBeforeAndAfter(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)
	amber := newMember(t, pool, f.household, "Amber")

	var bill uuid.UUID
	asActor(t, pool, actor{user: amber.String()}, func(tx pgx.Tx) {
		bill = txInsertID(t, tx, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Copay', 5, 10) RETURNING id`, f.household)
	})
	// The R1 correction: $5 -> $35.
	asActor(t, pool, actor{user: amber.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE bills SET amount = 35 WHERE id = $1`, bill)
	})

	var changes []byte
	var actorID *uuid.UUID
	mustScan(t, pool.QueryRow(context.Background(),
		`SELECT changes, actor_user_id FROM activity_log
		  WHERE entity_type = 'bill' AND entity_id = $1 AND action = 'update'
		  ORDER BY id DESC LIMIT 1`, bill), &changes, &actorID)
	if changes == nil {
		t.Fatal("the update logged no values")
	}
	var decoded map[string]struct{ From, To *float64 }
	if err := json.Unmarshal(changes, &decoded); err != nil {
		t.Fatalf("changes is not JSON: %v", err)
	}
	a, ok := decoded["amount"]
	if !ok || a.From == nil || *a.From != 5 || a.To == nil || *a.To != 35 {
		t.Errorf("changes = %s, want amount 5 -> 35", changes)
	}
	if actorID == nil || *actorID != amber {
		t.Errorf("actor = %v, want Amber %s", actorID, amber)
	}

	// A column outside the allowlist never carries its value.
	asActor(t, pool, actor{user: amber.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE bills SET notes = 'card ending 4242' WHERE id = $1`, bill)
	})
	mustScan(t, pool.QueryRow(context.Background(),
		`SELECT changes FROM activity_log WHERE entity_type = 'bill' AND entity_id = $1 ORDER BY id DESC LIMIT 1`, bill),
		&changes)
	if changes != nil {
		t.Errorf("notes must not reach the log: %s", changes)
	}

	// Create and delete rows never carry values (the CHECK enforces it).
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO activity_log (household_id, entity_type, entity_id, action, changes)
		 VALUES ($1, 'bill', gen_random_uuid(), 'create', '{"amount": {"from": 0, "to": 1}}')`, f.household,
	); err == nil {
		t.Error("a create row with changes should be refused")
	}
}

// TestMoneyProvenance071_Reversible pins down/up: the columns, the function
// and the trigger survive a round trip, and text_origin defaults to derived.
func TestMoneyProvenance071_Reversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()

	var hasColumn bool
	mustScan(t, pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = 'bills' AND column_name = 'source_document_id')`), &hasColumn)
	if !hasColumn {
		t.Fatal("up did not add bills.source_document_id")
	}

	applyMigration(t, pool, "071_money_provenance.down.sql")
	for _, q := range []string{
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'bills' AND column_name = 'source_document_id')`,
	} {
		mustScan(t, pool.QueryRow(ctx, q), &hasColumn)
		if hasColumn {
			t.Error("down left bills.source_document_id")
		}
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_proc WHERE proname = 'activity_change_values'`); n != 0 {
		t.Errorf("down left activity_change_values (%d)", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'activity_log' AND column_name = 'changes'`); n != 0 {
		t.Errorf("down left activity_log.changes (%d)", n)
	}
	// 055's body is back: it must not name the dropped column.
	if n := countRows(t, pool, `SELECT count(*) FROM pg_proc WHERE proname = 'log_activity' AND prosrc LIKE '%v_changes%'`); n != 0 {
		t.Error("down left the 071 log_activity body")
	}
	// The trigger still logs an update, so down is not a broken state.
	f := seedV2Family(t, pool)
	bill := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Copay', 5, 10) RETURNING id`, f.household)
	mustExec(t, pool, `UPDATE bills SET amount = 35 WHERE id = $1`, bill)
	if n := countRows(t, pool, `SELECT count(*) FROM activity_log
		WHERE entity_type = 'bill' AND entity_id = $1 AND action = 'update'`, bill); n != 1 {
		t.Errorf("down left %d update rows, want 1", n)
	}

	applyMigration(t, pool, "071_money_provenance.up.sql")
	mustScan(t, pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = 'bills' AND column_name = 'source_document_id')`), &hasColumn)
	if !hasColumn {
		t.Error("re-up did not restore bills.source_document_id")
	}
	// A file written without an origin is derived text, never the source.
	f2 := seedV2Family(t, pool)
	var blob uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`), &blob)
	plain := insertID(t, pool,
		`INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id)
		 VALUES ($1, $2, 'p.txt', 1, 'household', $1) RETURNING id`, f2.household, blob)
	var origin string
	mustScan(t, pool.QueryRow(ctx, `SELECT text_origin FROM files WHERE id = $1`, plain), &origin)
	if origin != "extracted" {
		t.Errorf("default text_origin = %q, want extracted", origin)
	}
}
