package billdue

import "time"

// Attention is the ONE rule for "is this bill on me?"
//
// Three things have to answer that question and none of them may answer it for
// itself:
//
//   - the API's bill responses (apps/api/internal/bill, #5498),
//   - the MCP bill tools, bill_find above all (#5498): an agent must be able
//     to answer "which bills will not pay themselves?" without guessing,
//   - the notification policy (#5500): a bill that pays itself is silence, a
//     bill that does not is a reminder.
//
// A copy of the rule in any of those three is how this codebase twice shipped
// two disagreeing versions of one rule (the money redaction and the due-date
// rule), so it lives here — in the package that already holds the household
// due-date rule, which both apps/api and apps/mcp import — and it is expressed
// in terms of that rule (BillDueFor, not a second lateness calculation).
//
// The four states, in priority order:
//
//	overdue      past due and unpaid: the most recent due date came and went
//	             and no payment covers it. Whatever the configuration says, the
//	             money is late, so this outranks every other state.
//	automatic    autopay is on AND an account is on record. Nothing to do:
//	             the state the notifier stays quiet for.
//	needs_setup  nothing automatic will pay this and nothing is on record to
//	             pay it from (autopay off with no account, or autopay on with
//	             no account — a row that claims to be automatic and would never
//	             pay). The trap case: the bill is silent and nobody is told.
//	due_soon     a person pays it, an account is on record, and it is not yet
//	             late. Note this is the only remaining state, so it does not
//	             depend on how far out the date is: a manual bill with an
//	             account is "on you" whether it is due in two days or three
//	             weeks. The lead time that decides WHEN to remind is the
//	             notification policy's own setting (#5500, AttentionLeadDays is
//	             its default); a bill is not mislabelled "needs setup" for
//	             being far away.
const (
	// AttentionAutomatic: autopay on, account on record. Nothing to do.
	AttentionAutomatic = "automatic"
	// AttentionNeedsSetup: nothing will pay this as configured, so it is on
	// the family until an account (and autopay) is set up.
	AttentionNeedsSetup = "needs_setup"
	// AttentionDueSoon: manual, account on record, not yet late.
	AttentionDueSoon = "due_soon"
	// AttentionOverdue: past due and unpaid.
	AttentionOverdue = "overdue"
)

// AttentionLeadDays is the default lead, in days, before a manually paid bill
// the family is reminded: long enough to pay a bill that arrives by post,
// short enough that "due soon" means soon. It belongs to the notification
// policy (#5500), which may configure it; the attention state does not move
// with it, so a change of lead time cannot relabel a bill.
const AttentionLeadDays = 7

// AttentionFor is the rule: given a bill's row as of today (the household's
// date as midnight UTC, as every due date here uses), which of the four states
// it is in. Every API response, every MCP bill row and the notification policy
// call this; none of them derives the state itself.
//
// autopay is bills.is_autopay (NOT NULL DEFAULT false since 075) and
// hasAccount is "a payment account is on record for this bill": a
// payment_accounts row linked by bills.autopay_account_id, or the legacy
// free-text bills.account_number on a pre-075 row. It never carries a number.
// s is the row's schedule, paidDate/paidFor are bills.paid_date and
// bills.paid_for_due_date, and createdAt is bills.created_at (which decides
// whether a passed due date could have been paid -- see overdue).
func AttentionFor(today time.Time, s Schedule, paidDate, paidFor *time.Time, createdAt time.Time, autopay, hasAccount bool) string {
	if overdue(today, s, paidDate, paidFor, createdAt) {
		return AttentionOverdue
	}
	if autopay && hasAccount {
		return AttentionAutomatic
	}
	if !hasAccount {
		// Nothing automatic, and nothing on record to pay it from. This is
		// the same fix whether autopay is off (turn it on and link an
		// account) or on (link the account it would pay from).
		return AttentionNeedsSetup
	}
	// A person pays it, and the family knows where the money comes from.
	// (Under 075 a manual bill cannot hold an autopay account —
	// bills_autopay_account_check — so this is a pre-075 row with
	// bills.account_number set.)
	return AttentionDueSoon
}

// overdue reports whether the bill's most recent due date has passed without
// being paid. It is deliberately built on the same cadence and coverage the
// due rule uses (cadence, coveredDueDate), so "past due" cannot disagree with
// "next due date": a bill is overdue exactly when the last due date before
// today is not covered by the last payment. A bill due today is due, not late.
//
// createdAt is part of the test, and it is what keeps an old bill that the
// family has only just entered from being declared late the day it is added: a
// bill created after that due date proves nothing about whether it was paid
// (the family may have paid it before the record existed, which is the normal
// case when importing paper bills). A bill that existed when the date passed
// and has no payment covering it is late -- no guessing involved.
func overdue(today time.Time, s Schedule, paidDate, paidFor *time.Time, createdAt time.Time) bool {
	c, ok := s.cadence()
	if !ok {
		// No usable schedule: no date can be late. The bill can still be
		// "needs setup", which AttentionFor decides.
		return false
	}
	// The last due date BEFORE today: a bill due today is due, not late.
	last, hasLast := c.onOrBefore(midnight(today).AddDate(0, 0, -1))
	if !hasLast {
		return false
	}
	if midnight(createdAt).After(last) {
		// This bill did not exist yet on that date.
		return false
	}
	covered := coveredDueDate(c, paidDate, paidFor)
	return covered == nil || covered.Before(last)
}

// AttentionValues lists the four states, in priority order, for the schemas
// and docs that advertise the field.
var AttentionValues = []string{AttentionOverdue, AttentionAutomatic, AttentionDueSoon, AttentionNeedsSetup}
