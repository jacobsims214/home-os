// Package billdue is the one implementation of the household due-date rule:
// when a bill's next unpaid due date is, and which due date a payment covers.
// Everything that has to agree calls it —
//
//   - apps/mcp/internal/bill (bill_find, bill_create, bill_update, bill_pay,
//     the calendar agenda's billsDueBetween and family_overview's billsDue),
//   - apps/mcp/internal/budget (budget_summary's due_soon section),
//   - apps/api (GET /api/v1/budget/due-soon, #4819),
//   - and home-os/billcal writes the mirror event's RRULE, which must give the
//     same dates (TestBillRuleMatchesDueRule in apps/mcp pins the two
//     together).
//
// It was moved here from apps/mcp/internal/bill/due.go in #4819: the rule was
// already shared inside the MCP module, but apps/api cannot import an internal
// package of another module, so an API endpoint would have needed a second
// copy. There is deliberately no second copy — a copy is how the "two due
// rules that disagree" class of bug gets in.
//
// apps/worker/cmd/worker/billdue.go is still a separate copy. Its tables are
// the same as the ones here.
package billdue

import "time"

// Bill frequencies (bills.frequency, migration 044). "" is read as monthly
// (rows that predate 044).
const (
	FrequencyMonthly    = "monthly"
	FrequencyQuarterly  = "quarterly"
	FrequencySemiannual = "semiannual"
	FrequencyAnnual     = "annual"
	FrequencyOnce       = "once"
)

// Frequencies lists the valid bills.frequency values, in the order tools
// advertise them.
var Frequencies = []string{FrequencyMonthly, FrequencyQuarterly, FrequencySemiannual, FrequencyAnnual, FrequencyOnce}

// StatusDue means the bill's current due date has not been paid yet.
const StatusDue = "due"

// StatusPaid means the last payment already covers the current due date, so
// Next is the one after it (or zero for a once bill).
const StatusPaid = "paid"

// lateGraceDays is how late a payment can be and still pay the due date it
// follows. A payment more than this many days after a due date pays the next
// one instead.
const lateGraceDays = 7

// frequencyMonths is the number of months between due dates: 1 for monthly
// (and "" for rows that predate 044), 3, 6 or 12, and 0 for a once bill.
// ok is false for an unknown frequency.
func frequencyMonths(frequency string) (months int, ok bool) {
	switch frequency {
	case FrequencyMonthly, "":
		return 1, true
	case FrequencyQuarterly:
		return 3, true
	case FrequencySemiannual:
		return 6, true
	case FrequencyAnnual:
		return 12, true
	case FrequencyOnce:
		return 0, true
	}
	return 0, false
}

// Schedule is when a bill falls due, as stored (044): a monthly bill is due
// on DueDay (1-31) of every month; any other bill uses DueDate as its anchor,
// repeating every 3, 6 or 12 months on that date's day of the month (clamped
// to short months), or never for a once bill.
type Schedule struct {
	// Frequency is bills.frequency; "" means monthly.
	Frequency string
	// DueDay is bills.due_day (monthly bills only).
	DueDay *int
	// DueDate is bills.due_date (bills that are not monthly).
	DueDate *time.Time
}

// MonthlySchedule is the schedule of a monthly bill due on dueDay.
func MonthlySchedule(dueDay int) Schedule {
	return Schedule{Frequency: FrequencyMonthly, DueDay: &dueDay}
}

// Cadence is a usable schedule: due on Day (clamped) of every Step-th month
// counted from the Anchor's month, or only on Anchor when Step is 0. It is
// exported because the bill write paths have to ask a schedule questions
// BillDueFor does not answer: which due dates exist (bill_pay's for_due_date
// check) and how to describe the schedule.
type Cadence struct {
	// Step is the number of months between due dates, 0 for a once bill.
	Step int
	// Day is the day of the month the schedule falls on, pre-clamping.
	Day int
	// Anchor is the first due date at midnight UTC. For a monthly bill only
	// its day matters.
	Anchor time.Time
}

// cadence resolves s. ok is false when s cannot produce a due date: an
// unknown frequency, a monthly bill without a due_day in 1-31, or another
// bill without a due_date.
func (s Schedule) cadence() (c Cadence, ok bool) {
	step, ok := frequencyMonths(s.Frequency)
	if !ok {
		return c, false
	}
	if step == 1 {
		if s.DueDay == nil || *s.DueDay < 1 || *s.DueDay > 31 {
			return c, false
		}
		return Cadence{Step: 1, Day: *s.DueDay, Anchor: time.Date(2000, time.January, *s.DueDay, 0, 0, 0, 0, time.UTC)}, true
	}
	if s.DueDate == nil {
		return c, false
	}
	a := midnight(*s.DueDate)
	return Cadence{Step: step, Day: a.Day(), Anchor: a}, true
}

// CadenceOf resolves s for callers outside this package. ok is false when s
// has no usable schedule; see Schedule.cadence.
func CadenceOf(s Schedule) (Cadence, bool) { return s.cadence() }

// monthIndex numbers months consecutively (year*12 + month-1).
func monthIndex(t time.Time) int {
	return t.Year()*12 + int(t.Month()) - 1
}

// inMonth is the due date in month index idx: day clamped to its length.
func (c Cadence) inMonth(idx int) time.Time {
	return dueDateInMonth(idx/12, time.Month(idx%12+1), c.Day)
}

// mod is a non-negative a mod n.
func mod(a, n int) int {
	return ((a % n) + n) % n
}

// onOrAfter returns the first due date on or after d; ok is false when there
// is none (a once bill whose date has passed). due_date is the bill's next
// (or only) due date, so nothing falls due before the anchor.
func (c Cadence) onOrAfter(d time.Time) (time.Time, bool) {
	d = midnight(d)
	if c.Step == 0 || d.Before(c.Anchor) {
		return c.Anchor, !c.Anchor.Before(d)
	}
	idx := monthIndex(d)
	idx += mod(monthIndex(c.Anchor)-idx, c.Step)
	due := c.inMonth(idx)
	if due.Before(d) {
		due = c.inMonth(idx + c.Step)
	}
	return due, true
}

// onOrBefore returns the last due date on or before d; ok is false when there
// is none (d is before the anchor, the first due date).
func (c Cadence) onOrBefore(d time.Time) (time.Time, bool) {
	d = midnight(d)
	if c.Step == 0 || d.Before(c.Anchor) {
		return c.Anchor, !c.Anchor.After(d)
	}
	idx := monthIndex(d)
	idx -= mod(idx-monthIndex(c.Anchor), c.Step)
	due := c.inMonth(idx)
	if due.After(d) {
		due = c.inMonth(idx - c.Step)
	}
	return due, true
}

// OnOrAfter is onOrAfter for other packages: the first due date on or after
// d, ok false when there is none.
func (c Cadence) OnOrAfter(d time.Time) (time.Time, bool) { return c.onOrAfter(d) }

// OnOrBefore is onOrBefore for other packages: the last due date on or
// before d, ok false when d is before the anchor.
func (c Cadence) OnOrBefore(d time.Time) (time.Time, bool) { return c.onOrBefore(d) }

// After is after for other packages: the due date following due, ok false for
// a once bill.
func (c Cadence) After(due time.Time) (time.Time, bool) { return c.after(due) }

// after returns the due date following due, one step later; ok is false for
// a once bill.
func (c Cadence) after(due time.Time) (time.Time, bool) {
	if c.Step == 0 {
		return time.Time{}, false
	}
	return c.inMonth(monthIndex(due) + c.Step), true
}

// billDueInfo is where a bill stands on a given day.
type billDueInfo struct {
	// next is the next due date that still needs paying; zero when none
	// remains (a once bill that is paid).
	next time.Time
	// status is StatusPaid when the current due date (the first one on or
	// after today) is already covered by the last payment, otherwise
	// StatusDue.
	status string
	// paidFor is the due date the last payment covers, nil when never paid.
	paidFor *time.Time
}

// billDue computes a bill's next unpaid due date as of today (household-local
// midnight UTC, see householdToday). ok is false when s has no usable
// schedule (see Schedule.cadence).
//
// The current due date is the first one on or after today: due_day of this
// month or next for a monthly bill, the next anchored date every 3/6/12
// months for quarterly/semiannual/annual, and due_date itself for a once
// bill (even after it has passed, until it is paid). If the last payment
// covers that due date or a later one (coveredDueDate: bills.paid_for_due_date
// when stored, else inferred from paid_date), the bill is paid and moves on
// to the due date after the one paid; a paid once bill has none left.
func billDue(today time.Time, s Schedule, paidDate, paidFor *time.Time) (billDueInfo, bool) {
	c, ok := s.cadence()
	if !ok {
		return billDueInfo{}, false
	}
	next, ok := c.onOrAfter(today)
	if !ok {
		next = c.Anchor // a once bill past its date: still due until paid
	}
	info := billDueInfo{next: next, status: StatusDue}
	covered := coveredDueDate(c, paidDate, paidFor)
	if covered == nil {
		return info, true
	}
	info.paidFor = covered
	if covered.Before(info.next) {
		return info, true
	}
	info.status = StatusPaid
	for !covered.Before(info.next) {
		n, ok := c.after(info.next)
		if !ok {
			info.next = time.Time{}
			break
		}
		info.next = n
	}
	return info, true
}

// coveredDueDate is the due date a bill's last payment covers, nil when it
// was never paid: bills.paid_for_due_date when stored (bill_pay, #4655),
// otherwise inferred from paid_date by paidForDueDate (rows written before
// 052, or after the schedule changed).
func coveredDueDate(c Cadence, paidDate, paidFor *time.Time) *time.Time {
	if paidFor != nil {
		d := midnight(*paidFor)
		return &d
	}
	if paidDate == nil {
		return nil
	}
	d := paidForDueDate(*paidDate, c)
	return &d
}

// paidForDueDate returns the due date a payment made on paid pays: the most
// recent due date (on or before paid) when the payment is at most
// lateGraceDays late, otherwise the first due date on or after paid. A once
// bill has one due date, so any payment pays it. It is the coverage bill_pay
// stores when the caller gives no for_due_date, and the fallback when
// bills.paid_for_due_date is NULL (see coveredDueDate).
func paidForDueDate(paid time.Time, c Cadence) time.Time {
	paid = midnight(paid)
	if last, ok := c.onOrBefore(paid); ok && paid.Sub(last) <= lateGraceDays*24*time.Hour {
		return last
	}
	if next, ok := c.onOrAfter(paid); ok {
		return next
	}
	return c.Anchor
}

// CoveredDueDate is coveredDueDate for other packages: the due date a bill's
// last payment covers, nil when it was never paid.
func CoveredDueDate(c Cadence, paidDate, paidFor *time.Time) *time.Time {
	return coveredDueDate(c, paidDate, paidFor)
}

// PaidForDueDate is paidForDueDate for other packages: the due date a payment
// made on paid pays under c.
func PaidForDueDate(paid time.Time, c Cadence) time.Time { return paidForDueDate(paid, c) }

// FrequencyMonths is frequencyMonths for other packages: the months between
// due dates for a bills.frequency, ok false for an unknown one.
func FrequencyMonths(frequency string) (int, bool) { return frequencyMonths(frequency) }

// NextDueDate returns the first date on or after today that falls on dueDay
// of its month. When a month is shorter than dueDay, the bill is due on that
// month's last day (due_day 31 is due on 30 April).
func NextDueDate(today time.Time, dueDay int) time.Time {
	c := Cadence{Step: 1, Day: dueDay}
	next, _ := c.onOrAfter(today)
	return next
}

// NextMonthlyDue is the first date on or after today that falls on dueDay of
// its month, clamped to the month's last day (31 is Nov 30): the loan rule,
// which is the bill rule for a monthly schedule, so bills and loans agree on
// what "due on the 31st" means.
func NextMonthlyDue(today time.Time, dueDay int) time.Time { return NextDueDate(today, dueDay) }

// dueDateInMonth returns dueDay of the given month, clamped to the month's
// last day. time.Date normalises a month of 13 into January of the next year.
func dueDateInMonth(year int, month time.Month, dueDay int) time.Time {
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if dueDay > lastDay {
		dueDay = lastDay
	}
	return time.Date(year, month, dueDay, 0, 0, 0, 0, time.UTC)
}

// DueDateInMonth is dueDateInMonth for other packages: dueDay of year/month,
// clamped to the month's last day, at midnight UTC.
func DueDateInMonth(year int, month time.Month, dueDay int) time.Time {
	return dueDateInMonth(year, month, dueDay)
}

// midnight is t's calendar date as midnight UTC.
func midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Midnight is midnight for other packages: t's calendar date at midnight UTC,
// so it compares with dates scanned from DATE columns.
func Midnight(t time.Time) time.Time { return midnight(t) }

// Due is where a bill stands on a given day, as every bill tool (bill_find,
// bill_create, bill_update, bill_pay) and the API report it.
type Due struct {
	// Next is the next due date that still needs paying (midnight UTC). It is
	// zero when no due date remains: a once bill that is paid.
	Next time.Time
	// DaysUntil is the whole days from today to Next (0 when Next is zero).
	// Only an unpaid once bill past its date makes it negative.
	DaysUntil int
	// Status is StatusPaid when the last payment covers the current due date
	// (so Next is the one after it, or zero for a once bill), otherwise
	// StatusDue.
	Status string
	// PaidFor is the due date the last payment covers, nil when never paid.
	PaidFor *time.Time
}

// BillDueFor is the single rule for when a bill is due (billDue) as of today,
// which should come from HouseholdToday. paidDate and paidFor are
// bills.paid_date and bills.paid_for_due_date (either may be nil; paidFor
// wins when set). ok is false when s has no usable schedule (a monthly bill
// without a due_day, another bill without a due_date).
func BillDueFor(today time.Time, s Schedule, paidDate, paidFor *time.Time) (Due, bool) {
	info, ok := billDue(today, s, paidDate, paidFor)
	if !ok {
		return Due{}, false
	}
	d := Due{Next: info.next, Status: info.status, PaidFor: info.paidFor}
	if !info.next.IsZero() {
		d.DaysUntil = DaysBetween(today, info.next)
	}
	return d, true
}

// BillDue is BillDueFor for a monthly bill due on dueDay (1-31).
func BillDue(today time.Time, dueDay int, paidDate, paidFor *time.Time) Due {
	d, _ := BillDueFor(today, MonthlySchedule(dueDay), paidDate, paidFor)
	return d
}

// DaysBetween returns the whole days from a to b (both midnight UTC dates).
func DaysBetween(a, b time.Time) int {
	return int(b.Sub(a).Hours() / 24)
}
