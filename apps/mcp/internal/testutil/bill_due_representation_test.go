package testutil

// Tests for migration 082 (#5511, schema review F12 of #5507): "when is this
// bill due" had three representations on bills with nothing tying them
// together, so a monthly bill could hold a due_day that disagreed with its
// due_date.
//
// The invariant, and why it is the one asserted here: every reader of a
// bill's schedule (packages/billdue, packages/billcal, the worker's bill_due
// job, the MCP bill tools, the UI) branches on frequency first and then reads
// exactly one column — due_day for a monthly bill, due_date for every other
// frequency. So the state 082 must make impossible is a monthly bill carrying
// a due_date at all, and its test is the reviewer's probe.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestBillDueRepresentation082 is the reviewer's probe from #5507 run against
// the migrated schema: a monthly bill with BOTH due_day = 5 and due_date =
// '2026-12-28' must be refused, while the two legitimate forms (due_day only,
// due_date only) must still insert.
func TestBillDueRepresentation082(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	hh := f.household

	t.Run("monthly bill with a disagreeing due_date is refused", func(t *testing.T) {
		_, err := pool.Exec(ctx,
			`INSERT INTO bills (household_id, name, amount, frequency, due_day, due_date)
			 VALUES ($1, 'contradictory', 10, 'monthly', 5, '2026-12-28')`, hh)
		if err == nil {
			t.Fatal("monthly bill with due_day = 5 AND due_date = 2026-12-28 was accepted; 082 should have refused it")
		}
		wantSQLState(t, err, "23514") // check_violation
		if !strings.Contains(err.Error(), "bills_schedule_one_representation_check") {
			t.Errorf("refused by the wrong constraint: %v", err)
		}
	})

	t.Run("a legitimate monthly bill (due_day only) still inserts", func(t *testing.T) {
		mustExec(t, pool,
			`INSERT INTO bills (household_id, name, amount, frequency, due_day)
			 VALUES ($1, 'Electric', 142.50, 'monthly', 5)`, hh)
	})

	t.Run("a legitimate one-off (due_date only) still inserts", func(t *testing.T) {
		mustExec(t, pool,
			`INSERT INTO bills (household_id, name, amount, frequency, due_date)
			 VALUES ($1, 'Cabin tax', 1800, 'annual', '2026-12-28')`, hh)
		mustExec(t, pool,
			`INSERT INTO bills (household_id, name, amount, frequency, due_date)
			 VALUES ($1, 'Passport', 160, 'once', '2026-12-28')`, hh)
	})

	t.Run("a monthly bill with no schedule at all is still legal", func(t *testing.T) {
		// legacyfinance and API-created rows legitimately have neither column
		// set; every consumer reports "no due date" for them.
		mustExec(t, pool,
			`INSERT INTO bills (household_id, name, amount, frequency)
			 VALUES ($1, 'Unscheduled', 5, 'monthly')`, hh)
	})

	t.Run("the UPDATE path is constrained too", func(t *testing.T) {
		id := insertID(t, pool,
			`INSERT INTO bills (household_id, name, amount, frequency, due_day)
			 VALUES ($1, 'Internet', 89, 'monthly', 20) RETURNING id`, hh)
		_, err := pool.Exec(ctx, `UPDATE bills SET due_date = '2026-12-28' WHERE id = $1`, id)
		if err == nil {
			t.Fatal("adding a due_date to a monthly bill was accepted; 082 should have refused it")
		}
		wantSQLState(t, err, "23514")
	})

	t.Run("a due_day outside 1-31 is refused", func(t *testing.T) {
		for _, day := range []int{-1, 0, 32} {
			_, err := pool.Exec(ctx,
				`INSERT INTO bills (household_id, name, amount, frequency, due_day)
				 VALUES ($1, 'bad day', 5, 'monthly', $2)`, hh, day)
			if err == nil {
				t.Fatalf("due_day = %d was accepted; bills_due_day_range_check should have refused it", day)
			}
			wantSQLState(t, err, "23514")
		}
	})

	t.Run("the columns say which one wins", func(t *testing.T) {
		for col, want := range map[string]string{
			"due_day":  "AUTHORITATIVE for frequency = 'monthly'",
			"due_date": "AUTHORITATIVE for frequency <> 'monthly'",
			"rrule":    "NOT AUTHORITATIVE",
		} {
			var comment *string
			mustScan(t, pool.QueryRow(ctx,
				`SELECT col_description('bills'::regclass, attnum)
				   FROM pg_attribute
				  WHERE attrelid = 'bills'::regclass AND attname = $1`, col), &comment)
			if comment == nil {
				t.Errorf("bills.%s has no COMMENT; 082 documents which column wins", col)
				continue
			}
			if !strings.Contains(*comment, want) {
				t.Errorf("bills.%s comment does not contain %q: %q", col, want, *comment)
			}
		}
	})
}

// TestBillDueRepresentation082_BackfillAndReverse rolls 082 back, builds the
// rows the pre-082 schema accepted, and applies 082 for real — twice, with a
// down in between. It asserts the two things a migration that rewrites data
// owes: a bill keeps the due date it had, and rollback restores the shape it
// had (without stacking notes when up is applied again).
func TestBillDueRepresentation082_BackfillAndReverse(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	hh := f.household

	// Pre-082 state: no 082 constraints, so the reviewer's row is accepted.
	applyMigration(t, pool, "082_bill_due_representation.down.sql")

	contradictory := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, frequency, due_day, due_date)
		VALUES ($1, 'contradictory', 10, 'monthly', 5, '2026-12-28') RETURNING id`, hh)
	legit := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, frequency, due_day)
		VALUES ($1, 'Electric', 142.50, 'monthly', 15) RETURNING id`, hh)
	oneoff := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, frequency, due_date)
		VALUES ($1, 'Cabin tax', 1800, 'annual', '2026-12-15') RETURNING id`, hh)
	unscheduled := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, frequency)
		VALUES ($1, 'Unscheduled', 5, 'monthly') RETURNING id`, hh)

	// resolved is what billdue.Schedule.cadence computes for the row: due_day
	// for a monthly bill, else due_date, else "" (no schedule). It is exactly
	// what every consumer of "when is this due" saw before 082.
	type shape struct {
		dueDay, dueDate *string
		resolved        string
	}
	read := func(id uuid.UUID) shape {
		var s shape
		var freq string
		var dueDay *int
		var dueDate *string
		mustScan(t, pool.QueryRow(ctx,
			`SELECT frequency, due_day, due_date::text FROM bills WHERE id = $1`, id),
			&freq, &dueDay, &dueDate)
		if dueDay != nil {
			d := itoa(*dueDay)
			s.dueDay = &d
		}
		s.dueDate = dueDate
		switch {
		case freq == "monthly" && dueDay != nil:
			s.resolved = "day " + itoa(*dueDay)
		case dueDate != nil:
			s.resolved = *dueDate
		}
		return s
	}

	ids := map[string]uuid.UUID{
		"contradictory": contradictory, "legit": legit, "oneoff": oneoff, "unscheduled": unscheduled,
	}
	before := map[string]shape{}
	for name, id := range ids {
		before[name] = read(id)
	}

	applyMigration(t, pool, "082_bill_due_representation.up.sql")

	// Criterion 4: every bill keeps the due date it had.
	for name, id := range ids {
		if got := read(id); got.resolved != before[name].resolved {
			t.Errorf("%s: resolved due date changed by 082: %q -> %q",
				name, before[name].resolved, got.resolved)
		}
	}
	// The monthly day is the schedule; the unread date is gone.
	if got := read(contradictory); got.dueDay == nil || *got.dueDay != "5" {
		t.Errorf("contradictory: due_day = %v, want 5", got.dueDay)
	}
	if got := read(contradictory); got.dueDate != nil {
		t.Errorf("contradictory: due_date = %v, want NULL (nothing ever read it)", *got.dueDate)
	}
	// The one-off's authoritative column is untouched.
	if got := read(oneoff); got.dueDate == nil || *got.dueDate != "2026-12-15" {
		t.Errorf("oneoff: due_date = %v, want 2026-12-15", got.dueDate)
	}
	// A bill with no schedule stays with no schedule.
	if got := read(unscheduled); got.dueDay != nil || got.dueDate != nil {
		t.Errorf("unscheduled: due_day = %v due_date = %v, want both NULL", got.dueDay, got.dueDate)
	}
	// The discarded value is kept in notes, not silently dropped.
	var notes *string
	mustScan(t, pool.QueryRow(ctx, `SELECT notes FROM bills WHERE id = $1`, contradictory), &notes)
	if notes == nil || !strings.Contains(*notes, "2026-12-28") {
		t.Errorf("contradictory: notes = %v, want the removed due_date recorded", notes)
	}

	// Criterion 5: down restores the pre-082 shape exactly.
	applyMigration(t, pool, "082_bill_due_representation.down.sql")
	for name, id := range ids {
		if got := read(id); got.resolved != before[name].resolved {
			t.Errorf("after down, %s: resolved due date %q, want the pre-082 %q",
				name, got.resolved, before[name].resolved)
		}
		if got := read(id); !sameOptional(got.dueDay, before[name].dueDay) || !sameOptional(got.dueDate, before[name].dueDate) {
			t.Errorf("after down, %s: columns %v/%v, want the pre-082 %v/%v",
				name, got.dueDay, got.dueDate, before[name].dueDay, before[name].dueDate)
		}
	}
	// The down took its own notes lines back out, so re-running up does not
	// stack a second copy on top of the first.
	mustScan(t, pool.QueryRow(ctx, `SELECT notes FROM bills WHERE id = $1`, contradictory), &notes)
	if notes != nil && strings.Contains(*notes, "Migration 082") {
		t.Errorf("after down, notes still hold the 082 line: %q", *notes)
	}

	// And up applies cleanly again on the restored shape.
	applyMigration(t, pool, "082_bill_due_representation.up.sql")
	if got := read(contradictory); got.dueDate != nil {
		t.Errorf("after re-up, contradictory: due_date = %v, want NULL", *got.dueDate)
	}
	var n int
	mustScan(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM bills, regexp_matches(COALESCE(notes,''), 'Migration 082:', 'g')
		  WHERE id = $1`, contradictory), &n)
	if n != 1 {
		t.Errorf("after down+up, contradictory has %d x 082 note lines, want exactly 1", n)
	}
}

func sameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
