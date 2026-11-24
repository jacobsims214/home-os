package billdue

import "time"

// ReminderFor is AttentionFor plus the date a reminder about the bill should
// NAME, so a caller never has to work that out for itself.
//
// The date differs by state, and getting it wrong is how a reminder turns into a
// lie: a due_soon or needs_setup reminder names the date the money is wanted
// (BillDueFor's Next), but an overdue reminder must name the date that was
// MISSED. A late monthly bill's next due date is four weeks away, so naming
// "Next" would send the family "Power is overdue, due Dec 28" about a bill due
// Nov 28 -- the one thing an overdue reminder must not say. The missed date is
// computed from the same cadence the rule uses (onOrBefore, not a second
// lateness calculation), which is why it lives here beside AttentionFor and not
// in the notification job.
//
// daysUntil is whole days from today to date: negative for a missed date, so a
// caller can sort by urgency. ok is false only when the bill has no date left to
// name (a once bill that is paid), in which case there is nothing to remind
// about whatever the state says.
func ReminderFor(today time.Time, s Schedule, paidDate, paidFor *time.Time, createdAt time.Time, autopay, hasAccount bool) (state string, date time.Time, daysUntil int, ok bool) {
	state = AttentionFor(today, s, paidDate, paidFor, createdAt, autopay, hasAccount)
	if state == AttentionOverdue {
		c, hasCadence := s.cadence()
		if hasCadence {
			// The last due date before today: the one no payment covers. A bill
			// due today is due, not late (the overdue test), so it is never the
			// named date of an overdue reminder.
			if missed, has := c.onOrBefore(midnight(today).AddDate(0, 0, -1)); has {
				return state, missed, DaysBetween(today, missed), true
			}
		}
	}
	d, hasDue := BillDueFor(today, s, paidDate, paidFor)
	if !hasDue || d.Next.IsZero() {
		return state, time.Time{}, 0, false
	}
	return state, midnight(d.Next), d.DaysUntil, true
}

// HouseholdToday is now's calendar date in the family's time zone, as midnight
// UTC so it compares with the DATE columns and with every other date here. An
// empty or unknown zone falls back to UTC, as the API, the MCP tools and the
// worker's own household date all do -- AT TIME ZONE in SQL would fail a whole
// query on one bad households.timezone where this never does.
func HouseholdToday(now time.Time, tz string) time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
