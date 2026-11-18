package bill

import (
	"time"

	"home-os/billdue"
)

// The bill due-date rule (#4468) now lives in home-os/billdue, because the
// API needs the same rule (#4819) and apps/api cannot import an internal
// package of the MCP module. This file is the MCP-side name for it: type
// aliases and wrappers, no logic. Every bill tool, the daily briefing and the
// calendar reach the one implementation through these names, so a change to
// the rule changes every caller at once.
//
// apps/worker/cmd/worker/billdue.go is still a copy (its image builds with
// GOWORK=off); its tables must stay the same as TestBillDueFrequencies and
// TestPaidForDueDateFrequencies here.

// Bill frequencies (bills.frequency, migration 044).
const (
	FrequencyMonthly    = billdue.FrequencyMonthly
	FrequencyQuarterly  = billdue.FrequencyQuarterly
	FrequencySemiannual = billdue.FrequencySemiannual
	FrequencyAnnual     = billdue.FrequencyAnnual
	FrequencyOnce       = billdue.FrequencyOnce
)

// frequencies lists the valid bills.frequency values, in the order tools
// advertise them.
var frequencies = billdue.Frequencies

// Schedule is when a bill falls due (bills.frequency, due_day, due_date).
type Schedule = billdue.Schedule

// Due is where a bill stands on a given day.
type Due = billdue.Due

// Status values in Due.Status.
const (
	statusDue  = billdue.StatusDue
	statusPaid = billdue.StatusPaid
)

// MonthlySchedule is the schedule of a monthly bill due on dueDay.
func MonthlySchedule(dueDay int) Schedule { return billdue.MonthlySchedule(dueDay) }

// BillDueFor is the single rule for when a bill is due as of today. See
// billdue.BillDueFor.
func BillDueFor(today time.Time, s Schedule, paidDate, paidFor *time.Time) (Due, bool) {
	return billdue.BillDueFor(today, s, paidDate, paidFor)
}

// BillDue is BillDueFor for a monthly bill due on dueDay (1-31).
func BillDue(today time.Time, dueDay int, paidDate, paidFor *time.Time) Due {
	return billdue.BillDue(today, dueDay, paidDate, paidFor)
}

// daysBetween returns the whole days from a to b (both midnight UTC dates).
func daysBetween(a, b time.Time) int { return billdue.DaysBetween(a, b) }

// nextDueDate returns the first date on or after today that falls on dueDay
// of its month, clamped to the month's last day.
func nextDueDate(today time.Time, dueDay int) time.Time { return billdue.NextDueDate(today, dueDay) }

// dueDateInMonth returns dueDay of the given month, clamped to its last day.
func dueDateInMonth(year int, month time.Month, dueDay int) time.Time {
	return billdue.DueDateInMonth(year, month, dueDay)
}

// midnight is t's calendar date as midnight UTC.
func midnight(t time.Time) time.Time { return billdue.Midnight(t) }

// frequencyMonths is the number of months between due dates for a
// bills.frequency; ok is false for an unknown one.
var frequencyMonths = billdue.FrequencyMonths

// paidForDueDate returns the due date a payment made on paid pays. Exported
// from the shared package for the payment tests.
var paidForDueDate = billdue.PaidForDueDate

// coveredDueDate is the due date a bill's last payment covers, nil when it
// was never paid.
var coveredDueDate = billdue.CoveredDueDate

// cadence resolves a schedule for the write and pay paths that need more than
// BillDueFor exposes (bill_pay's for_due_date check, the schedule text).
func cadence(s Schedule) (billdue.Cadence, bool) { return billdue.CadenceOf(s) }

// frequencyName is s.Frequency, with "" read as monthly.
func frequencyName(s Schedule) string {
	if s.Frequency == "" {
		return FrequencyMonthly
	}
	return s.Frequency
}
