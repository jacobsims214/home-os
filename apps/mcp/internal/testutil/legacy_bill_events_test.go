package testutil

// Tests for migration 073 (#4943): a legacy "bill due" calendar entry is
// retired in place when a bill with the same name and amount is the record for
// it, so the Bills calendar shows one projection per bill and no briefing can
// read a stale amount as bill state.
//
// See KB discoveries/war-game-r1-jacob.md G2 + G5.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// legacyBillEntry inserts a calendar entry on a Bills calendar of the family
// with the REAL round-1 shape (#5140): column event_type 'bill' and
// entity_type NULL ("Electric bill due — $180" on the live household is
// exactly this). It mirrors no record — entity_type is what says so — but it
// is typed like the bills module's mirror rows, which is what migration 073's
// `event_type IS DISTINCT FROM 'bill'` clause mistook for a mirror and
// therefore refused to supersede.
//
// The ical_data JSON keeps event_type empty, as the round-1 writer left it:
// the column is the typed fact, and this fixture exists to pin that difference.
func legacyBillEntry(t *testing.T, pool *pgxpool.Pool, household uuid.UUID, calendar uuid.UUID, title string) uuid.UUID {
	t.Helper()
	data := `{"title":"` + title + `","description":"","start":"2026-11-18T00:00:00Z","end":"2026-11-19T00:00:00Z","all_day":true,"event_type":""}`
	var id uuid.UUID
	mustScan(t, pool.QueryRow(context.Background(),
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
		 VALUES ($1, $2, $3, 'e', 'bill') RETURNING id`,
		calendar, uuid.NewString(), data), &id)
	return id
}

// billMirrorRow inserts a bill's own mirror, exactly as packages/billcal writes
// it: entity_type 'bill' with entity_id the bill, and event_type 'bill'. It is
// the row that must survive every reconcile — the test tells a mirror from a
// legacy entry by entity_type and nothing else (#5140).
func billMirrorRow(t *testing.T, pool *pgxpool.Pool, calendar, bill uuid.UUID, title string) uuid.UUID {
	t.Helper()
	data := `{"title":"` + title + `","description":"","start":"2026-11-18T00:00:00Z","end":"2026-11-19T00:00:00Z","all_day":true,"event_type":"bill"}`
	var id uuid.UUID
	mustScan(t, pool.QueryRow(context.Background(),
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
		 VALUES ($1, $2, $3, 'e', 'bill', 'bill', $4) RETURNING id`,
		calendar, uuid.NewString(), data, bill), &id)
	return id
}

// billsCalendar makes the family a Bills calendar and returns its id.
func billsCalendar(t *testing.T, ctx context.Context, pool *pgxpool.Pool, household uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Bills', 'bills', $2) RETURNING id`,
		household, uuid.NewString()), &id)
	return id
}

func TestLegacyBillEvents073_Schema(t *testing.T) {
	pool := NewPool(t)
	for _, c := range [][2]string{
		{"calendar_objects", "superseded_at"},
		{"calendar_objects", "superseded_by"},
		{"calendar_objects", "superseded_reason"},
	} {
		if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`, c[0], c[1]); n != 1 {
			t.Errorf("%s.%s is missing", c[0], c[1])
		}
	}
	for _, f := range []string{
		"calendar_bill_name", "calendar_bill_amounts",
		"calendar_title_reads_as_bill", "calendar_absorb_bill_events",
	} {
		if n := countRows(t, pool, `SELECT count(*) FROM pg_proc WHERE proname = $1`, f); n != 1 {
			t.Errorf("function %s is missing", f)
		}
	}
	// The guard: a bill made by any writer absorbs its stale entry.
	if n := countRows(t, pool, `SELECT count(*) FROM pg_trigger
		WHERE tgname = 'bills_absorb_calendar_events' AND NOT tgisinternal`); n != 1 {
		t.Error("the bills INSERT guard trigger is missing")
	}
	// A superseded row must say why; a live one must say nothing.
	ctx := context.Background()
	f := seedV2Family(t, pool)
	cal := billsCalendar(t, ctx, pool, f.household)
	entry := legacyBillEntry(t, pool, f.household, cal, "Odd — $1")
	if _, err := pool.Exec(ctx,
		`UPDATE calendar_objects SET superseded_at = NOW() WHERE id = $1`, entry); err == nil {
		t.Error("a superseded entry with no reason was accepted")
	}
	if _, err := pool.Exec(ctx,
		`UPDATE calendar_objects SET superseded_at = NOW(), superseded_reason = 'nonsense' WHERE id = $1`, entry); err == nil {
		t.Error("an unknown superseded_reason was accepted")
	}
}

func TestLegacyBillEvents073_Reconcile(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	cal := billsCalendar(t, ctx, pool, f.household)

	// The bills module's records first, then the round-1 entries, then the
	// reconcile: that is the order migration 073 runs in (a family that
	// already has bills and already has stale entries), and it is the only
	// order in which an entry can still be live beside its bill.
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, due_day, paid_date, paid_for_due_date)
		VALUES ($1, 'Mortgage', 2340, 1, NULL, NULL)`, f.household)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Gas', 120, 25)`, f.household)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Internet', 75, 22)`, f.household)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Water', 60, 20)`, f.household)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Water', 90, 20)`, f.household)
	elecBill := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day, paid_date, paid_for_due_date)
		VALUES ($1, 'Electric', 180, 18, CURRENT_DATE, CURRENT_DATE) RETURNING id`, f.household)
	mortgageBill := insertID(t, pool, `SELECT id FROM bills WHERE household_id = $1 AND name = 'Mortgage'`, f.household)
	internetBill := insertID(t, pool, `SELECT id FROM bills WHERE household_id = $1 AND name = 'Internet'`, f.household)

	// The bills' own mirrors, as billcal writes them: entity_type 'bill' with
	// entity_id set. These are the rows that must survive every reconcile — the
	// live household has one per bill, and they are why the Bills calendar
	// showed each bill twice (#5140).
	elecMirror := billMirrorRow(t, pool, cal, elecBill, "Electric due")
	mortgageMirror := billMirrorRow(t, pool, cal, mortgageBill, "Mortgage due")
	internetMirror := billMirrorRow(t, pool, cal, internetBill, "Internet due")

	// The round-1 entries, exactly as the war game left them.
	electric := legacyBillEntry(t, pool, f.household, cal, "Electric bill due — $180")
	mortgage := legacyBillEntry(t, pool, f.household, cal, "Mortgage due — $2,340")
	// An amount that disagrees with the bill.
	gas := legacyBillEntry(t, pool, f.household, cal, "Gas bill due — $95")
	// No bill by that name.
	sewer := legacyBillEntry(t, pool, f.household, cal, "Sewer bill due — $40")
	// A bill word with no amount: the name still matches.
	internet := legacyBillEntry(t, pool, f.household, cal, "Internet bill")
	// Two bills of this name: ambiguous, no guess.
	water := legacyBillEntry(t, pool, f.household, cal, "Water bill due — $60")
	// Not a bill at all.
	library := legacyBillEntry(t, pool, f.household, cal, "Library books due")
	// A title with no name in it.
	nameless := legacyBillEntry(t, pool, f.household, cal, "Bill due — $80")
	// A person's own event linked to a bill is theirs, never absorbed.
	linked := insertID(t, pool,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
		 VALUES ($1, $2, '{"title":"Electric bill due — $180","description":"","start":"2026-11-18T00:00:00Z","end":"2026-11-19T00:00:00Z","all_day":true}', 'e', 'event', 'bill', $3) RETURNING id`,
		cal, uuid.NewString(), elecBill)

	// The one-time pass: one call per bill, exactly the migration's loop.
	for _, name := range []string{"Electric", "Mortgage", "Gas", "Internet", "Water"} {
		rows, err := pool.Query(ctx, `SELECT id FROM bills WHERE household_id = $1 AND name = $2 ORDER BY created_at, id`, f.household, name)
		if err != nil {
			t.Fatal(err)
		}
		ids := []uuid.UUID{}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			var n int
			mustScan(t, pool.QueryRow(ctx, `SELECT calendar_absorb_bill_events($1)`, id), &n)
		}
	}

	retired := func(id uuid.UUID) bool {
		t.Helper()
		return countRows(t, pool, `SELECT count(*) FROM calendar_objects WHERE id = $1 AND superseded_at IS NOT NULL`, id) == 1
	}
	for _, c := range []struct {
		name string
		id   uuid.UUID
		want bool
	}{
		{"Electric bill due — $180", electric, true},
		{"Mortgage due — $2,340", mortgage, true},
		{"Internet bill", internet, true},
		// The mirrors: entity_type='bill', so never touched, even though their
		// titles normalise to the same names as the legacy entries above and
		// their event_type is the same 'bill' (#5140).
		{"the Electric mirror (entity_type='bill')", elecMirror, false},
		{"the Mortgage mirror (entity_type='bill')", mortgageMirror, false},
		{"the Internet mirror (entity_type='bill')", internetMirror, false},
		{"Gas bill due — $95 (amount disagrees)", gas, false},
		{"Sewer bill due — $40 (no such bill)", sewer, false},
		{"Water bill due — $60 (two bills named Water)", water, false},
		{"Library books due (not a bill)", library, false},
		{"Bill due — $80 (no name)", nameless, false},
		{"an event a person linked to a bill", linked, false},
	} {
		if got := retired(c.id); got != c.want {
			t.Errorf("%s: superseded = %v, want %v", c.name, got, c.want)
		}
	}

	// What a retirement points at, and that it kept the family's text.
	var billName, title string
	mustScan(t, pool.QueryRow(ctx,
		`SELECT b.name, co.ical_data::jsonb->>'title'
		   FROM calendar_objects co JOIN bills b ON b.id = co.superseded_by WHERE co.id = $1`, electric),
		&billName, &title)
	if billName != "Electric" || title != "Electric bill due — $180" {
		t.Errorf("electric entry = bill %q, title %q", billName, title)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM calendar_objects WHERE id = $1 AND superseded_reason = 'matched_bill'`, electric); n != 1 {
		t.Error("the retirement reason is not matched_bill")
	}

	// CalDAV clients were told to drop the retired ones and only those.
	for _, c := range []struct {
		uid  string
		want bool
	}{
		{uidOf(t, pool, electric), true},
		{uidOf(t, pool, mortgage), true},
		{uidOf(t, pool, gas), false},
		{uidOf(t, pool, water), false},
	} {
		n := countRows(t, pool, `SELECT count(*) FROM calendar_changes WHERE event_uid = $1 AND action = 'delete'`, c.uid)
		if (n > 0) != c.want {
			t.Errorf("calendar_changes delete for %s: %d, want wanted=%v", c.uid, n, c.want)
		}
	}

	// Idempotent: the reconcile again changes nothing.
	for _, name := range []string{"Electric", "Mortgage", "Internet"} {
		var id uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT id FROM bills WHERE household_id = $1 AND name = $2`, f.household, name), &id)
		var n int
		mustScan(t, pool.QueryRow(ctx, `SELECT calendar_absorb_bill_events($1)`, id), &n)
		if n != 0 {
			t.Errorf("absorbing %s a second time retired %d entries", name, n)
		}
	}
}

// uidOf is a calendar entry's uid, which is what calendar_changes records.
func uidOf(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var uid string
	mustScan(t, pool.QueryRow(context.Background(), `SELECT uid FROM calendar_objects WHERE id = $1`, id), &uid)
	return uid
}

// TestLegacyBillEvents073_GuardOnInsert is the durable half: a bill made by any
// writer (here plain SQL, which stands for the MCP, the API, legacyfinance or a
// seed) retires the stale entry it is the record for, so two sources of truth
// cannot come back.
func TestLegacyBillEvents073_GuardOnInsert(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	cal := billsCalendar(t, ctx, pool, f.household)

	entry := legacyBillEntry(t, pool, f.household, cal, "Electric bill due — $180")
	if n := countRows(t, pool, `SELECT count(*) FROM calendar_objects WHERE id = $1 AND superseded_at IS NOT NULL`, entry); n != 0 {
		t.Fatal("the entry was superseded before any bill existed")
	}
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Electric', 180, 18)`, f.household)
	if n := countRows(t, pool, `SELECT count(*) FROM calendar_objects WHERE id = $1 AND superseded_at IS NOT NULL AND superseded_reason = 'matched_bill'`, entry); n != 1 {
		t.Error("a bill inserted after the entry did not absorb it")
	}

	// An amount that disagrees is not absorbed, even by the guard: the entry's
	// amount is the only record of what somebody once read off a statement.
	gas := legacyBillEntry(t, pool, f.household, cal, "Gas bill due — $95")
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Gas', 120, 25)`, f.household)
	if n := countRows(t, pool, `SELECT count(*) FROM calendar_objects WHERE id = $1 AND superseded_at IS NOT NULL`, gas); n != 0 {
		t.Error("the guard absorbed an entry whose amount disagrees with the bill")
	}

	// A second bill of the same name makes the name ambiguous, so a later
	// entry naming it survives.
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Gas', 99, 25)`, f.household)
	gas2 := legacyBillEntry(t, pool, f.household, cal, "Gas bill due — $99")
	if n := countRows(t, pool, `SELECT count(*) FROM calendar_objects WHERE id = $1 AND superseded_at IS NOT NULL`, gas2); n != 0 {
		t.Error("an ambiguous name was matched anyway")
	}
}

// TestLegacyBillEvents073_Reversible is the ticket's up/down/up: down puts
// every retired entry back (nothing was deleted, so this is the whole undo) and
// tells CalDAV clients to re-create them; up again retires them again.
func TestLegacyBillEvents073_Reversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	cal := billsCalendar(t, ctx, pool, f.household)

	before := insertID(t, pool,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, $2, '{"title":"Dentist"}', 'e') RETURNING id`,
		cal, uuid.NewString())
	entry := legacyBillEntry(t, pool, f.household, cal, "Electric bill due — $180")
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Electric', 180, 18)`, f.household)
	if n := countRows(t, pool, `SELECT count(*) FROM calendar_objects WHERE id = $1 AND superseded_at IS NOT NULL`, entry); n != 1 {
		t.Fatal("setup: the entry was not retired")
	}
	uid := uidOf(t, pool, entry)

	applyMigration(t, pool, "074_legacy_bill_events_reconcile.down.sql") // newest first: 074 sits on top of 073
	applyMigration(t, pool, "073_legacy_bill_events.down.sql")
	if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'calendar_objects' AND column_name LIKE 'superseded%'`); n != 0 {
		t.Fatalf("down left %d supersede columns", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_proc WHERE proname IN
		('calendar_bill_name','calendar_bill_amounts','calendar_title_reads_as_bill','calendar_absorb_bill_events','bills_absorb_calendar_events')`); n != 0 {
		t.Fatalf("down left %d functions", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname = 'bills_absorb_calendar_events'`); n != 0 {
		t.Fatalf("down left the guard trigger")
	}
	// Nothing was deleted, so both entries are still there.
	if n := countRows(t, pool, `SELECT count(*) FROM calendar_objects WHERE id IN ($1, $2)`, before, entry); n != 2 {
		t.Fatalf("down lost a calendar entry: %d of 2 left", n)
	}
	// And CalDAV clients are told to put the retired one back.
	if n := countRows(t, pool, `SELECT count(*) FROM calendar_changes WHERE event_uid = $1 AND action = 'create'`, uid); n != 1 {
		t.Errorf("down wrote %d create changes for the retired entry, want 1", n)
	}

	// Up again: the entry is retired again by the reconcile, and the schema and
	// the guard are back.
	applyMigration(t, pool, "073_legacy_bill_events.up.sql")
	applyMigration(t, pool, "074_legacy_bill_events_reconcile.up.sql") // back to the head
	if n := countRows(t, pool, `SELECT count(*) FROM calendar_objects WHERE id = $1 AND superseded_at IS NOT NULL`, entry); n != 1 {
		t.Error("up again did not retire the entry")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname = 'bills_absorb_calendar_events'`); n != 1 {
		t.Error("up again did not restore the guard trigger")
	}
	// The ordinary event is still live, and always was.
	if n := countRows(t, pool, `SELECT count(*) FROM calendar_objects WHERE id = $1 AND superseded_at IS NULL`, before); n != 1 {
		t.Error("an ordinary event was retired")
	}
	// The bill survives the whole cycle.
	if n := countRows(t, pool, `SELECT count(*) FROM bills WHERE household_id = $1 AND name = 'Electric'`, f.household); n != 1 {
		t.Errorf("down/up lost the bill: %d", n)
	}
}

// TestLegacyBillEvents073_FamiliesDoNotMix: two families can each
// have an "Electric" bill and a stale entry; each family's entry is matched to
// its own bill only.
func TestLegacyBillEvents073_FamiliesDoNotMix(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	a := seedV2Family(t, pool)
	b := seedV2Family(t, pool)
	calA := billsCalendar(t, ctx, pool, a.household)
	calB := billsCalendar(t, ctx, pool, b.household)

	entryA := legacyBillEntry(t, pool, a.household, calA, "Electric bill due — $180")
	entryB := legacyBillEntry(t, pool, b.household, calB, "Electric bill due — $999")
	billA := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Electric', 180, 18) RETURNING id`, a.household)
	billB := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Electric', 999, 18) RETURNING id`, b.household)

	for _, c := range []struct {
		name   string
		entry  uuid.UUID
		bill   uuid.UUID
		family string
	}{
		{"family A", entryA, billA, "A"},
		{"family B", entryB, billB, "B"},
	} {
		var gotBill uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT superseded_by FROM calendar_objects WHERE id = $1`, c.entry), &gotBill)
		if gotBill != c.bill {
			t.Errorf("%s: entry superseded_by %s, want its own bill %s", c.name, gotBill, c.bill)
		}
	}
}
