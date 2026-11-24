package notification

// The bill a notification is about, and whether that bill still needs a person.
//
// This is the whole point of the ticket. A bill_due reminder on its own says
// "the household was told about a bill"; it does not say whether the bill is
// handled. A family whose autopay is on gets reminders for bills that pay
// themselves, and an agent that reads "bill_due" as "needs attention" nags its
// person about nothing. So every notification about a bill carries the SAME
// attention state bill_find and the REST API carry (#5498):
//
//	automatic    it pays itself. needs_you = false. Nothing to do.
//	needs_setup  nothing will pay it. needs_you = true.
//	due_soon     a person pays it. needs_you = true.
//	overdue      it is late. needs_you = true.
//
// The state is billdue.AttentionFor's answer, computed from the same columns
// and the same arguments as apps/mcp/internal/bill and apps/api/internal/bill.
// Nothing here re-derives "is this on me?"; if this file and bill_find ever
// disagreed, the bug is in one of the two call sites, not in a third rule.
//
// Money visibility is auth.CanSeeMoney(role), the same predicate the bill tools
// use. A role without money never reaches the bill query: visibleSQL already
// drops a notification whose entity is a bill (role_can_see_entity(bill) is
// false), so a housesitter's find never returns a bill_due row at all. The gate
// is here anyway, so a future caller cannot leak the state through a row that
// slipped past the filter.

import (
	"context"
	"time"

	"github.com/google/uuid"

	"home-os/billdue"
	"home-os/mcp/internal/auth"
)

// billRow is the part of a bills row the attention rule reads. It is a
// deliberate subset of internal/bill's billRow: only the columns
// billdue.AttentionFor takes, so this package cannot drift into a second bill
// read model.
type billRow struct {
	ID        uuid.UUID
	Frequency string
	DueDay    *int
	DueDate   *time.Time
	PaidDate  *time.Time
	PaidFor   *time.Time
	CreatedAt time.Time
	AutoPay   bool
	// HasAccount is "a payment account is on record": autopay_account_id set,
	// or the legacy account_number. It is computed under the caller's money
	// visibility, so a hidden account cannot turn into "automatic".
	HasAccount bool
}

// billsSQL loads the bills behind a page of notifications in one query. The
// notification's entity must be a bill and the id must match. A row the caller
// cannot see (another family's, or a bill the role cannot see) simply comes
// back absent.
var billsSQL = `
	SELECT b.id, COALESCE(b.frequency::text, ''), b.due_day, b.due_date,
	       b.paid_date, b.paid_for_due_date, b.created_at, COALESCE(b.is_autopay, false),
	       (b.autopay_account_id IS NOT NULL OR NULLIF(btrim(COALESCE(b.account_number, '')), '') IS NOT NULL)
	FROM bills b
	WHERE b.household_id = $1 AND b.id = ANY($2)`

// loadBills reads the bills behind the given notification entity ids. The
// result is keyed by bill id; a bill that does not exist or is not visible is
// absent. When the role may not see money, no bill is loaded at all.
func loadBills(ctx context.Context, q querier, householdID string, ids []uuid.UUID, seesMoney bool) (map[uuid.UUID]*billRow, error) {
	out := make(map[uuid.UUID]*billRow, len(ids))
	if !seesMoney || len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, billsSQL, householdID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r billRow
		if err := rows.Scan(&r.ID, &r.Frequency, &r.DueDay, &r.DueDate, &r.PaidDate,
			&r.PaidFor, &r.CreatedAt, &r.AutoPay, &r.HasAccount); err != nil {
			return nil, err
		}
		out[r.ID] = &r
	}
	return out, rows.Err()
}

// schedule is the row's due schedule, the shape billdue.AttentionFor takes.
func (r *billRow) schedule() billdue.Schedule {
	return billdue.Schedule{Frequency: r.Frequency, DueDay: r.DueDay, DueDate: r.DueDate}
}

// attention is billdue.AttentionFor's answer for this bill as of today. It is
// the same call bill_find and the API make; there is no second rule.
func (r *billRow) attention(today time.Time) string {
	return billdue.AttentionFor(today, r.schedule(), r.PaidDate, r.PaidFor,
		r.CreatedAt, r.AutoPay, r.HasAccount)
}

// needsYou reports whether the bill still needs a person. A bill that pays
// itself is the only one that does not.
func (r *billRow) needsYou(today time.Time) bool {
	return r.attention(today) != billdue.AttentionAutomatic
}

// entityIDs pulls the bill ids out of a page of notification rows, so one query
// loads every bill the page needs.
func entityIDs(rows []row) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(rows))
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		if r.entityType == nil || *r.entityType != "bill" || r.entityID == nil {
			continue
		}
		if seen[*r.entityID] {
			continue
		}
		seen[*r.entityID] = true
		ids = append(ids, *r.entityID)
	}
	return ids
}

// attachBills loads the bills for a page and hangs each one on its row. A
// notification about a bill whose row is gone keeps bill == nil: the output
// leaves needs_you null rather than guessing. Role is the caller's current
// role, so the money gate is applied as the bill tools apply it.
func attachBills(ctx context.Context, q querier, householdID string, rows []row, role string) error {
	ids := entityIDs(rows)
	if len(ids) == 0 {
		return nil
	}
	bills, err := loadBills(ctx, q, householdID, ids, auth.CanSeeMoney(role))
	if err != nil {
		return err
	}
	for i := range rows {
		if rows[i].entityType == nil || *rows[i].entityType != "bill" || rows[i].entityID == nil {
			continue
		}
		rows[i].bill = bills[*rows[i].entityID]
	}
	return nil
}
