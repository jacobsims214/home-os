package bill

import "time"

// The bill due-date rule (#4468). Every bill tool, the daily briefing and the
// calendar use it through BillDue / BillDueFor, and the worker's bill_due job
// has a copy in apps/worker/cmd/worker/billdue.go (the worker is a separate
// module whose image builds with GOWORK=off). CHANGE BOTH when the rule
// changes: the tables in TestBillDueFrequencies / TestPaidForDueDateFrequencies
// here and TestNextUnpaidDueFrequencies / TestPaidForDueDateFrequencies there
// are the same.

// Bill frequencies (bills.frequency, migration 044).
const (
	FrequencyMonthly    = "monthly"
	FrequencyQuarterly  = "quarterly"
	FrequencySemiannual = "semiannual"
	FrequencyAnnual     = "annual"
	FrequencyOnce       = "once"
)

// frequencies lists the valid bills.frequency values, in the order tools
// advertise them.
var frequencies = []string{FrequencyMonthly, FrequencyQuarterly, FrequencySemiannual, FrequencyAnnual, FrequencyOnce}

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

// cadence is a usable schedule: due on day (clamped) of every step-th month
// counted from the anchor's month, or only on anchor when step is 0.
type cadence struct {
	step   int
	day    int
	anchor time.Time // midnight UTC; for monthly bills only its day matters
}

// cadence resolves s. ok is false when s cannot produce a due date: an
// unknown frequency, a monthly bill without a due_day in 1-31, or another
// bill without a due_date.
func (s Schedule) cadence() (c cadence, ok bool) {
	step, ok := frequencyMonths(s.Frequency)
	if !ok {
		return c, false
	}
	if step == 1 {
		if s.DueDay == nil || *s.DueDay < 1 || *s.DueDay > 31 {
			return c, false
		}
		return cadence{step: 1, day: *s.DueDay, anchor: time.Date(2000, time.January, *s.DueDay, 0, 0, 0, 0, time.UTC)}, true
	}
	if s.DueDate == nil {
		return c, false
	}
	a := midnight(*s.DueDate)
	return cadence{step: step, day: a.Day(), anchor: a}, true
}

// monthIndex numbers months consecutively (year*12 + month-1).
func monthIndex(t time.Time) int {
	return t.Year()*12 + int(t.Month()) - 1
}

// inMonth is the due date in month index idx: day clamped to its length.
func (c cadence) inMonth(idx int) time.Time {
	return dueDateInMonth(idx/12, time.Month(idx%12+1), c.day)
}

// mod is a non-negative a mod n.
func mod(a, n int) int {
	return ((a % n) + n) % n
}

// onOrAfter returns the first due date on or after d; ok is false when there
// is none (a once bill whose date has passed). due_date is the bill's next
// (or only) due date, so nothing falls due before the anchor.
func (c cadence) onOrAfter(d time.Time) (time.Time, bool) {
	d = midnight(d)
	if c.step == 0 || d.Before(c.anchor) {
		return c.anchor, !c.anchor.Before(d)
	}
	idx := monthIndex(d)
	idx += mod(monthIndex(c.anchor)-idx, c.step)
	due := c.inMonth(idx)
	if due.Before(d) {
		due = c.inMonth(idx + c.step)
	}
	return due, true
}

// onOrBefore returns the last due date on or before d; ok is false when there
// is none (d is before the anchor, the first due date).
func (c cadence) onOrBefore(d time.Time) (time.Time, bool) {
	d = midnight(d)
	if c.step == 0 || d.Before(c.anchor) {
		return c.anchor, !c.anchor.After(d)
	}
	idx := monthIndex(d)
	idx -= mod(idx-monthIndex(c.anchor), c.step)
	due := c.inMonth(idx)
	if due.After(d) {
		due = c.inMonth(idx - c.step)
	}
	return due, true
}

// after returns the due date following due, one step later; ok is false for
// a once bill.
func (c cadence) after(due time.Time) (time.Time, bool) {
	if c.step == 0 {
		return time.Time{}, false
	}
	return c.inMonth(monthIndex(due) + c.step), true
}

// billDueInfo is where a bill stands on a given day.
type billDueInfo struct {
	// next is the next due date that still needs paying; zero when none
	// remains (a once bill that is paid).
	next time.Time
	// status is statusPaid when the current due date (the first one on or
	// after today) is already covered by the last payment, otherwise statusDue.
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
		next = c.anchor // a once bill past its date: still due until paid
	}
	info := billDueInfo{next: next, status: statusDue}
	covered := coveredDueDate(c, paidDate, paidFor)
	if covered == nil {
		return info, true
	}
	info.paidFor = covered
	if covered.Before(info.next) {
		return info, true
	}
	info.status = statusPaid
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
func coveredDueDate(c cadence, paidDate, paidFor *time.Time) *time.Time {
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
func paidForDueDate(paid time.Time, c cadence) time.Time {
	paid = midnight(paid)
	if last, ok := c.onOrBefore(paid); ok && paid.Sub(last) <= lateGraceDays*24*time.Hour {
		return last
	}
	if next, ok := c.onOrAfter(paid); ok {
		return next
	}
	return c.anchor
}

// nextDueDate returns the first date on or after today that falls on dueDay
// of its month. When a month is shorter than dueDay, the bill is due on that
// month's last day (due_day 31 is due on 30 April).
func nextDueDate(today time.Time, dueDay int) time.Time {
	c := cadence{step: 1, day: dueDay}
	next, _ := c.onOrAfter(today)
	return next
}

// dueDateInMonth returns dueDay of the given month, clamped to the month's
// last day. time.Date normalises a month of 13 into January of the next year.
func dueDateInMonth(year int, month time.Month, dueDay int) time.Time {
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if dueDay > lastDay {
		dueDay = lastDay
	}
	return time.Date(year, month, dueDay, 0, 0, 0, 0, time.UTC)
}

// midnight is t's calendar date as midnight UTC.
func midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Due is where a bill stands on a given day, as every bill tool (bill_find,
// bill_create, bill_update, bill_pay) reports it.
type Due struct {
	// Next is the next due date that still needs paying (midnight UTC). It is
	// zero when no due date remains: a once bill that is paid.
	Next time.Time
	// DaysUntil is the whole days from today to Next (0 when Next is zero).
	// Only an unpaid once bill past its date makes it negative.
	DaysUntil int
	// Status is "paid" when the last payment covers the current due date (so
	// Next is the one after it, or zero for a once bill), otherwise "due".
	Status string
	// PaidFor is the due date the last payment covers, nil when never paid.
	PaidFor *time.Time
}

// BillDueFor is the single rule for when a bill is due (billDue) as of today,
// which should come from HouseholdToday. paidDate and paidFor are
// bills.paid_date and bills.paid_for_due_date (either may be nil; paidFor
// wins when set). ok is false when s has no usable schedule (a monthly bill
// without a due_day, another bill without a due_date). Exported so other
// packages (e.g. the daily briefing) give the same answer as the bill tools.
func BillDueFor(today time.Time, s Schedule, paidDate, paidFor *time.Time) (Due, bool) {
	info, ok := billDue(today, s, paidDate, paidFor)
	if !ok {
		return Due{}, false
	}
	d := Due{Next: info.next, Status: info.status, PaidFor: info.paidFor}
	if !info.next.IsZero() {
		d.DaysUntil = daysBetween(today, info.next)
	}
	return d, true
}

// BillDue is BillDueFor for a monthly bill due on dueDay (1-31).
func BillDue(today time.Time, dueDay int, paidDate, paidFor *time.Time) Due {
	d, _ := BillDueFor(today, MonthlySchedule(dueDay), paidDate, paidFor)
	return d
}
