package billcal

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Beginner starts a transaction: a *pgxpool.Pool in practice.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// missingSQL lists the bills that should have a mirror event and don't: a
// usable schedule (the SQL form of Rule's ok), a Bills calendar in the
// household, and no event with entity_type and event_type 'bill' for the
// bill. Bills without a schedule, or in a household without a Bills
// calendar, are never listed, so a run with nothing to do writes nothing.
const missingSQL = `
SELECT b.household_id::text, b.id::text
  FROM bills b
 WHERE ((COALESCE(b.frequency, 'monthly') = 'monthly' AND b.due_day BETWEEN 1 AND 31)
     OR (b.frequency IN ('quarterly', 'semiannual', 'annual', 'once') AND b.due_date IS NOT NULL))
   AND EXISTS (SELECT 1 FROM calendars c WHERE c.household_id = b.household_id AND c.type = 'bills')
   AND NOT EXISTS (
       SELECT 1 FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id
        WHERE c.household_id = b.household_id
          AND co.entity_type = 'bill' AND co.entity_id = b.id AND co.event_type = 'bill')
 ORDER BY b.created_at, b.id`

// Backfill gives every bill that has no mirror event one (#4716): bills made
// before #4485, or written by a path that doesn't mirror (seeds, SQL). Each
// bill is synced in its own transaction under its row lock (SyncByID), so a
// run concurrent with an API/MCP write or another worker never creates a
// second event: whoever comes second finds the event and leaves it alone.
// Existing events are never touched, and events people made are never read.
// It returns how many events it created; a bill that fails is skipped and
// its error joined into the returned error.
func Backfill(ctx context.Context, db Beginner) (int, error) {
	rows, err := db.Query(ctx, missingSQL)
	if err != nil {
		return 0, fmt.Errorf("list bills without a calendar event: %w", err)
	}
	type ref struct{ household, bill string }
	var todo []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.household, &r.bill); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan bill without a calendar event: %w", err)
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("list bills without a calendar event: %w", err)
	}

	created := 0
	var errs []error
	for _, r := range todo {
		if err := ctx.Err(); err != nil {
			return created, err
		}
		ch, err := backfillOne(ctx, db, r.household, r.bill)
		if err != nil {
			// One bad bill must not hold back the others.
			errs = append(errs, err)
			continue
		}
		if ch.Action == ActionCreate {
			created++
		}
	}
	return created, errors.Join(errs...)
}

// backfillOne creates the bill's mirror event if it still has none.
func backfillOne(ctx context.Context, db Beginner, householdID, billID string) (Change, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return Change{}, fmt.Errorf("backfill bill event: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	b, ok, err := LoadBill(ctx, tx, householdID, billID)
	if err != nil || !ok {
		return Change{}, err
	}
	// Under the bill's lock: a writer that committed since the listing may
	// have created the event already. Only create; never rewrite one.
	if _, found, err := findEvent(ctx, tx, householdID, billID); err != nil || found {
		return Change{}, err
	}
	ch, err := Sync(ctx, tx, householdID, b)
	if err != nil {
		return Change{}, fmt.Errorf("backfill bill %s: %w", billID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Change{}, fmt.Errorf("backfill bill event: commit: %w", err)
	}
	return ch, nil
}
