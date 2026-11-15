package main

import (
	"time"
	// Embedded zoneinfo, so householdToday resolves households.timezone even
	// if the runtime image has no /usr/share/zoneinfo.
	_ "time/tzdata"
)

// This file is a copy of the bill due-date rule in apps/mcp/internal/bill/due.go
// (Schedule.cadence, onOrAfter/onOrBefore/after, billDue, coveredDueDate,
// paidForDueDate, dueDateInMonth) plus householdToday's timezone fallback.
// The worker is a separate Go module and its image builds with GOWORK=off
// from only the modules it needs, so importing apps/mcp (and its MCP
// dependencies) is not worth it for a few small functions. Keep the two in
// sync: the tables in billdue_test.go are the same as TestBillDue,
// TestBillDueFrequencies, TestPaidForDueDate and
// TestPaidForDueDateFrequencies in apps/mcp/internal/bill, and a change to
// either side should change both.

// lateGraceDays is how late a payment can be and still pay the due date it
// follows (apps/mcp/internal/bill.lateGraceDays).
const lateGraceDays = 7

// frequencyMonths is the number of months between due dates for a
// bills.frequency (044): 1 for monthly (and ""), 3, 6 or 12, and 0 for a
// once bill. ok is false for an unknown frequency.
func frequencyMonths(frequency string) (months int, ok bool) {
	switch frequency {
	case "monthly", "":
		return 1, true
	case "quarterly":
		return 3, true
	case "semiannual":
		return 6, true
	case "annual":
		return 12, true
	case "once":
		return 0, true
	}
	return 0, false
}

// schedule is when a bill falls due, as stored (044): a monthly bill is due
// on dueDay (1-31) of every month; any other bill uses dueDate as its anchor,
// repeating every 3, 6 or 12 months on that date's day of the month (clamped
// to short months), or never for a once bill (apps/mcp/internal/bill.Schedule).
type schedule struct {
	frequency string
	dueDay    *int
	dueDate   *time.Time
}

// monthlySchedule is the schedule of a monthly bill due on dueDay.
func monthlySchedule(dueDay int) schedule {
	return schedule{frequency: "monthly", dueDay: &dueDay}
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
func (s schedule) cadence() (c cadence, ok bool) {
	step, ok := frequencyMonths(s.frequency)
	if !ok {
		return c, false
	}
	if step == 1 {
		if s.dueDay == nil || *s.dueDay < 1 || *s.dueDay > 31 {
			return c, false
		}
		return cadence{step: 1, day: *s.dueDay, anchor: time.Date(2000, time.January, *s.dueDay, 0, 0, 0, 0, time.UTC)}, true
	}
	if s.dueDate == nil {
		return c, false
	}
	a := midnight(*s.dueDate)
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

// nextUnpaidDue returns a bill's next unpaid due date as of today (a
// midnight-UTC date, see householdToday): the first due date on or after
// today (due_day of this month or next for a monthly bill, the next anchored
// date every 3/6/12 months, or due_date itself for a once bill, even after
// it has passed, until it is paid). When the last payment covers that due
// date (or a later one), the bill moves on to the due date after the one
// paid. paidDate and paidFor are bills.paid_date and bills.paid_for_due_date.
// ok is false when s has no usable schedule or no due date remains (a paid
// once bill). Same as apps/mcp/internal/bill.BillDueFor(...).Next.
func nextUnpaidDue(today time.Time, s schedule, paidDate, paidFor *time.Time) (time.Time, bool) {
	c, ok := s.cadence()
	if !ok {
		return time.Time{}, false
	}
	next, ok := c.onOrAfter(today)
	if !ok {
		next = c.anchor // a once bill past its date: still due until paid
	}
	covered := coveredDueDate(c, paidDate, paidFor)
	if covered == nil {
		return next, true
	}
	for !covered.Before(next) {
		n, ok := c.after(next)
		if !ok {
			return time.Time{}, false
		}
		next = n
	}
	return next, true
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
// bill has one due date, so any payment pays it.
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

// householdToday returns the calendar date of now in the household's
// timezone, as midnight UTC so it compares with DATE columns. An empty or
// unknown timezone falls back to UTC, as the MCP tools do.
func householdToday(now time.Time, tz string) time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
