package billdue

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LoadDueSoonRows is what falls due in the next Days days for a household: each
// bill once at its next unpaid due date (BillDueFor, so a paid bill moves on
// to its next date and a once bill that passed unpaid is left out) and each
// active loan payment at its next monthly due date.
//
// It is the one implementation of "what's due soon", called by the MCP
// budget_summary due_soon section (apps/mcp/internal/budget) and by the API's
// GET /api/v1/budget/due-soon (#4819). Keeping the query and the rule
// together is what makes the two agree: a second query in the API would be a
// second chance to disagree about which bills count.
//
// today is the family's date as midnight UTC (a household-timezone date, as
// bill.HouseholdToday returns). Bill rows that scan with a NULL amount use a
// NULL AmountCents; they are counted and the total simply skips them, and
// WithoutDueDate counts records whose schedule cannot produce a date so a
// caller can say so rather than implying there is nothing else.
// Querier is what LoadDueSoon needs: a pool, a transaction or a test double.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// DueSoonItem is one bill or loan payment falling due in the window.
type DueSoonItem struct {
	// Source is "bill" or "loan".
	Source string
	// ID is the bill id or the loan id.
	ID uuid.UUID
	// Name is the bill or loan name.
	Name string
	// AmountCents is what is due on that date, nil when the row has no
	// amount. It is the bill's amount or the loan's monthly_payment.
	AmountCents *int64
	// DueDate is the due date (midnight UTC).
	DueDate time.Time
	// DaysUntil is the whole days from today to DueDate (0 today).
	DaysUntil int
	// Status is StatusDue when that occurrence still needs paying and
	// StatusPaid when the current occurrence is already covered and DueDate
	// is the one after it.
	Status string
	// Autopay is bills.is_autopay; always false for a loan.
	Autopay bool
	// SubjectKind and SubjectID are what the bill or loan is about:
	// "property", "vehicle", "asset", or "" for a family-level record.
	SubjectKind string
	SubjectID   *uuid.UUID
	// SubjectName is that property/vehicle/asset's display name, nil when the
	// record has no subject or it could not be resolved.
	SubjectName *string
}

// DueSoon is everything that falls due from today through today + Days.
type DueSoon struct {
	// Days is the window in days.
	Days int
	// From is today and To is today + Days, both midnight UTC dates.
	From, To time.Time
	// Items is every item in the window, earliest first, then by name.
	Items []DueSoonItem
	// BillCount and LoanCount count the items of each source.
	BillCount, LoanCount int
	// TotalCents is the sum of the items' amounts; an item without an amount
	// adds nothing and is counted in Uncountable.
	TotalCents int64
	// Uncountable is how many items have no amount.
	Uncountable int
	// WithoutDueDate is how many bills and active loans have no usable due
	// date, so they cannot appear in the window.
	WithoutDueDate int
}

// LoadDueSoonRows reads it from the database. householdID is the household's
// id as a string (uuid.UUID.String()).
func LoadDueSoonRows(ctx context.Context, q Querier, householdID string, today time.Time, days int) (*DueSoon, error) {
	end := today.AddDate(0, 0, days)
	out := &DueSoon{Days: days, From: Midnight(today), To: Midnight(end)}

	rows, err := q.Query(ctx, billsDueSoonSQL, householdID)
	if err != nil {
		return nil, fmt.Errorf("due soon bills: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id                     uuid.UUID
			name, freq             string
			amount                 *int64
			dueDay                 *int
			dueDate, paid, paidFor *time.Time
			autopay                bool
			subjectKind            *string
			subjectID              *uuid.UUID
			subjectName            *string
		)
		if err := rows.Scan(&id, &name, &amount, &freq, &dueDay, &dueDate, &paid, &paidFor,
			&autopay, &subjectKind, &subjectID, &subjectName); err != nil {
			return nil, fmt.Errorf("scan bill: %w", err)
		}
		due, ok := BillDueFor(today, Schedule{Frequency: freq, DueDay: dueDay, DueDate: dueDate}, paid, paidFor)
		if !ok {
			out.WithoutDueDate++
			continue
		}
		if due.Next.IsZero() || due.DaysUntil < 0 || due.DaysUntil > days {
			continue
		}
		out.add(DueSoonItem{
			Source: "bill", ID: id, Name: name, AmountCents: amount,
			Status: due.Status, Autopay: autopay,
			SubjectKind: derefString(subjectKind), SubjectID: subjectID, SubjectName: subjectName,
		}, due.Next, today)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("due soon bills: %w", err)
	}

	lrows, err := q.Query(ctx, loansDueSoonSQL, householdID, today, end)
	if err != nil {
		return nil, fmt.Errorf("due soon loans: %w", err)
	}
	defer lrows.Close()
	for lrows.Next() {
		var (
			id             uuid.UUID
			name           string
			amount         *int64
			dueDay         *int
			start, termEnd *time.Time
			subjectKind    *string
			subjectID      *uuid.UUID
			subjectName    *string
		)
		if err := lrows.Scan(&id, &name, &amount, &dueDay, &start, &termEnd,
			&subjectKind, &subjectID, &subjectName); err != nil {
			return nil, fmt.Errorf("scan loan: %w", err)
		}
		if dueDay == nil || *dueDay < 1 || *dueDay > 31 {
			out.WithoutDueDate++
			continue
		}
		next := NextMonthlyDue(today, *dueDay)
		if start != nil && next.Before(*start) {
			next = NextMonthlyDue(*start, *dueDay)
		}
		if next.After(end) || (termEnd != nil && next.After(*termEnd)) {
			continue
		}
		out.add(DueSoonItem{
			Source: "loan", ID: id, Name: name, AmountCents: amount,
			Status:      StatusDue,
			SubjectKind: derefString(subjectKind), SubjectID: subjectID, SubjectName: subjectName,
		}, next, today)
	}
	if err := lrows.Err(); err != nil {
		return nil, fmt.Errorf("due soon loans: %w", err)
	}

	sort.SliceStable(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if !a.DueDate.Equal(b.DueDate) {
			return a.DueDate.Before(b.DueDate)
		}
		if !strings.EqualFold(a.Name, b.Name) {
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		return a.ID.String() < b.ID.String()
	})
	return out, nil
}

// add records it and counts it.
func (d *DueSoon) add(it DueSoonItem, next, today time.Time) {
	it.DueDate = Midnight(next)
	it.DaysUntil = DaysBetween(Midnight(today), it.DueDate)
	if it.Source == "loan" {
		d.LoanCount++
	} else {
		d.BillCount++
	}
	if it.AmountCents != nil {
		d.TotalCents += *it.AmountCents
	} else {
		d.Uncountable++
	}
	d.Items = append(d.Items, it)
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// subjectColumns resolves what a row is about into (kind, id, display name):
// an asset, else a vehicle (year make model), else a property. It reads the
// row's asset_id/vehicle_id/property_id through the alias a.
func subjectColumns(alias string) string {
	return fmt.Sprintf(`CASE WHEN %[1]s.asset_id IS NOT NULL THEN 'asset'
	                          WHEN %[1]s.vehicle_id IS NOT NULL THEN 'vehicle'
	                          WHEN %[1]s.property_id IS NOT NULL THEN 'property' END,
	 COALESCE(%[1]s.asset_id, %[1]s.vehicle_id, %[1]s.property_id),
	 COALESCE((SELECT a.name FROM assets a WHERE a.id = %[1]s.asset_id),
	          (SELECT NULLIF(concat_ws(' ', v.year, v.make, v.model), '') FROM vehicles v WHERE v.id = %[1]s.vehicle_id),
	          (SELECT p.name FROM properties p WHERE p.id = %[1]s.property_id))`, alias)
}

// billsDueSoonSQL selects the bills columns the rule needs, plus what the row
// is about. Every bill of the household is read: the window is applied to the
// computed next due date, not to a stored column, because a monthly bill has
// no stored next date.
var billsDueSoonSQL = `SELECT b.id, b.name, ROUND(b.amount * 100)::bigint,
	b.frequency, b.due_day, b.due_date, b.paid_date, b.paid_for_due_date,
	COALESCE(b.is_autopay, false), ` + subjectColumns("b") + `
	  FROM bills b WHERE b.household_id = $1`

// loansDueSoonSQL selects the active loans in the window: the same active rule
// as the obligations view (a balance left, started by the window's end, not
// paid off before it starts).
var loansDueSoonSQL = `SELECT l.id, l.name, ROUND(l.monthly_payment * 100)::bigint,
	l.due_day, l.start_date, l.term_end, ` + subjectColumns("l") + `
	  FROM loans l
	 WHERE l.household_id = $1 AND l.remaining_balance > 0
	   AND (l.start_date IS NULL OR l.start_date <= $3)
	   AND (l.term_end IS NULL OR l.term_end >= $2)`
