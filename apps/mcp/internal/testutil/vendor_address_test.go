package testutil

// Tests for migration 065 (#4766): vendors.address, filled from the MCP
// contact tools' old "Address: ..." first notes line, and the vendor_id
// indexes on bills and maintenance_tasks.

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestVendorAddress065_BackfillIdempotentAndReversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	applyMigration(t, pool, "065_vendor_address.down.sql")

	f := seedV2Family(t, pool)
	vendor := func(name string, notes any) uuid.UUID {
		return insertID(t, pool, `INSERT INTO vendors (household_id, name, notes) VALUES ($1, $2, $3) RETURNING id`, f.household, name, notes)
	}
	withRest := vendor("Joe", "Address: 12 Main St\nAsk for Joe\nline 2")
	only := vendor("Ann", "Address: 9 Elm St")
	notFirst := vendor("Bob", "Call first\nAddress: not the first line")
	noNotes := vendor("Cat", nil)
	blank := vendor("Dan", "Address:   \nrest")
	crlf := vendor("Eve", "Address: 1 A St\r\nrest")
	twice := vendor("Fay", "Address: 1 A St\nAddress: 2 B St")

	type row struct{ address, notes *string }
	get := func(id uuid.UUID) row {
		t.Helper()
		var r row
		mustScan(t, pool.QueryRow(ctx, `SELECT address, notes FROM vendors WHERE id = $1`, id), &r.address, &r.notes)
		return r
	}
	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	want := func(when string, id uuid.UUID, address, notes string) {
		t.Helper()
		if r := get(id); str(r.address) != address || str(r.notes) != notes {
			t.Errorf("%s: address, notes = %q, %q; want %q, %q", when, str(r.address), str(r.notes), address, notes)
		}
	}
	wantUp := func(when string) {
		t.Helper()
		want(when, withRest, "12 Main St", "Ask for Joe\nline 2")
		want(when, only, "9 Elm St", "<nil>")
		want(when, notFirst, "<nil>", "Call first\nAddress: not the first line")
		want(when, noNotes, "<nil>", "<nil>")
		want(when, blank, "<nil>", "rest")
		want(when, crlf, "1 A St", "rest")
		// Only the first line moves, and only once: the second up leaves the
		// remaining "Address:" line in notes because address is already set.
		want(when, twice, "1 A St", "Address: 2 B St")
	}

	applyMigration(t, pool, "065_vendor_address.up.sql")
	wantUp("first up")
	applyMigration(t, pool, "065_vendor_address.up.sql")
	wantUp("second up (idempotent)")

	for _, idx := range []string{"idx_bills_vendor_id", "idx_maintenance_tasks_vendor_id"} {
		if n := countRows(t, pool, `SELECT count(*) FROM pg_indexes WHERE indexname = '`+idx+`'`); n != 1 {
			t.Errorf("index %s: %d rows in pg_indexes, want 1", idx, n)
		}
	}

	// An address an API client sent with a newline stays on the first line.
	multi := insertID(t, pool, `INSERT INTO vendors (household_id, name, address, notes) VALUES ($1, 'Gus', E'5 Oak Rd\nApt 2', 'hi') RETURNING id`, f.household)

	applyMigration(t, pool, "065_vendor_address.down.sql")
	var hasColumn bool
	mustScan(t, pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'vendors' AND column_name = 'address')`), &hasColumn)
	if hasColumn {
		t.Fatal("down left vendors.address")
	}
	notesOf := func(id uuid.UUID) string {
		t.Helper()
		var n *string
		mustScan(t, pool.QueryRow(ctx, `SELECT notes FROM vendors WHERE id = $1`, id), &n)
		return str(n)
	}
	for id, w := range map[uuid.UUID]string{
		withRest: "Address: 12 Main St\nAsk for Joe\nline 2",
		only:     "Address: 9 Elm St",
		notFirst: "Call first\nAddress: not the first line",
		noNotes:  "<nil>",
		blank:    "rest",
		crlf:     "Address: 1 A St\nrest",
		twice:    "Address: 1 A St\nAddress: 2 B St",
		multi:    "Address: 5 Oak Rd Apt 2\nhi",
	} {
		if got := notesOf(id); got != w {
			t.Errorf("down: notes = %q, want %q", got, w)
		}
	}
	applyMigration(t, pool, "065_vendor_address.down.sql") // safe to run twice

	applyMigration(t, pool, "065_vendor_address.up.sql")
	want("re-up", withRest, "12 Main St", "Ask for Joe\nline 2")
	want("re-up", multi, "5 Oak Rd Apt 2", "hi")
}
