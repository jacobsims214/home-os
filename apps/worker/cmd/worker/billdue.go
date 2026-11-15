package main

import (
	"time"
	// Embedded zoneinfo, so householdToday resolves households.timezone even
	// if the runtime image has no /usr/share/zoneinfo.
	_ "time/tzdata"
)

// This file is a copy of the bill due-date rule in apps/mcp/internal/bill
// (BillDue / billDue, paidForDueDate, nextDueDate, dueDateInMonth and
// householdToday's timezone fallback). The worker is a separate Go module
// and its image builds with GOWORK=off from only the modules it needs, so
// importing apps/mcp (and its MCP dependencies) is not worth it for four
// small functions. Keep the two in sync: the tables in billdue_test.go are
// the same as TestBillDue and TestPaidForDueDate in apps/mcp/internal/bill,
// and a change to either side should change both.

// lateGraceDays is how late a payment can be and still pay the due date it
// follows (apps/mcp/internal/bill.lateGraceDays).
const lateGraceDays = 7

// nextUnpaidDue returns a bill's next unpaid due date as of today (a
// midnight-UTC date, see householdToday): due_day of this month or, once it
// has passed, of next month, clamped to the month's length. When paid_date
// covers that due date (or a later one), the bill moves on to the due date
// after the one paid. Same as apps/mcp/internal/bill.BillDue(...).Next.
func nextUnpaidDue(today time.Time, dueDay int, paidDate *time.Time) time.Time {
	next := nextDueDate(today, dueDay)
	if paidDate == nil {
		return next
	}
	covered := paidForDueDate(*paidDate, dueDay)
	for !covered.Before(next) {
		next = dueDateInMonth(next.Year(), next.Month()+1, dueDay)
	}
	return next
}

// paidForDueDate returns the due date a payment made on paid pays: the most
// recent due date (on or before paid) when the payment is at most
// lateGraceDays late, otherwise the first due date on or after paid.
func paidForDueDate(paid time.Time, dueDay int) time.Time {
	y, m, d := paid.Date()
	paid = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	last := dueDateInMonth(y, m, dueDay)
	if last.After(paid) {
		last = dueDateInMonth(y, m-1, dueDay)
	}
	if paid.Sub(last) <= lateGraceDays*24*time.Hour {
		return last
	}
	return nextDueDate(paid, dueDay)
}

// nextDueDate returns the first date on or after today that falls on dueDay
// of its month, clamped to the month's last day.
func nextDueDate(today time.Time, dueDay int) time.Time {
	y, m, d := today.Date()
	today = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	candidate := dueDateInMonth(y, m, dueDay)
	if candidate.Before(today) {
		candidate = dueDateInMonth(y, m+1, dueDay)
	}
	return candidate
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
