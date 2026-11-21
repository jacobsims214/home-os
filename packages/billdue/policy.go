package billdue

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The notification policy: autopay means silence.
//
// This is the ONE place that decides whether a bill earns a reminder, and it is
// decided from AttentionFor's answer and nothing else. There is deliberately no
// `if autopay { ... } else { ... }` in the notification job (#5500): notifying
// on every bill is noise, notifying only on the bills that need a person is
// signal, and a second copy of "is this on me?" is how the two answers start
// disagreeing, as they twice have in this codebase. A caller that needs a new
// case adds a state to the rule or a row to this policy, never a comparison of
// its own.
//
//	automatic    autopay is on and an account is set. NO reminder: it pays
//	             itself, and telling the family would be noise.
//	needs_setup  nothing will pay this and nothing says where the money comes
//	             from. Remind EARLY, and say what is MISSING -- "Electric --
//	             autopay is off and no account is set; payment of $180.00 due
//	             Nov 28." A family told only "it is due" has to work the rest
//	             out for itself.
//	due_soon     a person pays it and an account is on record. Remind at the
//	             configured lead time.
//	overdue      the money is already late. Remind and escalate, whatever the
//	             date arithmetic says.

// DigestThreshold is how many bill reminders one family may get from one run
// before the job sends one digest instead. Three is a nudge; a fourth push in
// the same tick is where a family stops reading them, and a household that
// imported twelve bills at once would otherwise get twelve. The digest still
// names every bill, its amount, its date and what is missing, so nothing is
// hidden -- only the delivery is collapsed.
const DigestThreshold = 3

// NotificationTypeBill and NotificationTypeDigest are the notifications.type
// values the job writes. A single reminder keeps the type bill_due it has
// always had, so the existing client icons, email template and read filters
// keep working (#5500 requirement 3: no parallel delivery path).
const (
	NotificationTypeBill   = "bill_due"
	NotificationTypeDigest = "bill_digest"
)

// Advice is what the notification job should do about one bill.
type Advice struct {
	// Attention is the AttentionFor state this advice came from, carried so a
	// notification can be keyed on it: a bill that moves from due_soon to
	// overdue is new information and earns a new reminder.
	Attention string
	// Notify is false only for automatic: the bill pays itself.
	Notify bool
	// LeadDays is how many days before the named date a reminder may go out.
	// It is ignored when Escalate is set.
	LeadDays int
	// Escalate is true for overdue: send it now regardless of how far away the
	// next due date is, because the missed one is the point.
	Escalate bool
	// Missing says what a person has to set up, in the family's words, when
	// attention is needs_setup; empty otherwise. This is the difference
	// between a useful reminder and "Electric is due".
	Missing []string
}

// AdviceFor is the policy, keyed on attention. dueSoonLeadDays is the caller's
// configured lead time for an ordinary manual bill; a non-positive value falls
// back to AttentionLeadDays.
//
// An attention value that is not one of the four is a bug in the caller, and it
// fails towards telling a person rather than towards silence: the point of the
// policy is that a bill which will not pay itself must not be quiet.
func AdviceFor(attention string, dueSoonLeadDays int) Advice {
	if dueSoonLeadDays <= 0 {
		dueSoonLeadDays = AttentionLeadDays
	}
	switch attention {
	case AttentionAutomatic:
		return Advice{Attention: attention, Notify: false}
	case AttentionNeedsSetup:
		// Earlier than an ordinary manual bill: the family has to go and set
		// something up before the money can move, and that takes longer than
		// paying a bill that is already wired up.
		lead := dueSoonLeadDays
		if lead < AttentionLeadDays {
			lead = AttentionLeadDays
		}
		// Both halves are named, because which one is missing decides what the
		// person does: turn autopay on, link the account it would pay from, or
		// both. A bill with autopay on and no account is the same fix as one
		// with neither, and the sentence says so.
		return Advice{
			Attention: attention,
			Notify:    true,
			LeadDays:  lead,
			Missing:   []string{"autopay is off", "no account is set"},
		}
	case AttentionOverdue:
		return Advice{Attention: attention, Notify: true, Escalate: true}
	default:
		// AttentionDueSoon, and any value the rule does not know.
		return Advice{Attention: attention, Notify: true, LeadDays: dueSoonLeadDays}
	}
}

// Due reports whether the advice fires for a bill whose named date is on date,
// daysUntil days from today.
func (a Advice) Due(daysUntil int) bool {
	if !a.Notify {
		return false
	}
	if a.Escalate {
		return true
	}
	return daysUntil <= a.LeadDays
}

// DedupeKey identifies one reminder: the bill, the date it is about and the
// attention state. Re-running the job (it ticks every few seconds) is a no-op;
// a bill that moves to a new state or a new due date earns a new reminder. It
// is stored in notifications.dedupe_key, unique per family for the family-wide
// rows this job writes (migration 081), so a second replica or a restart cannot
// duplicate it either.
func (a Advice) DedupeKey(billID uuid.UUID, date time.Time) string {
	return fmt.Sprintf("bill_due:%s:%s:%s", billID, Midnight(date).Format(time.DateOnly), a.Attention)
}

// Message is the notification's title and body for one bill. date is the date
// being named: the due date, or the missed one for an overdue bill.
// amountCents is nil when the bill has no amount, and the text says "a
// payment" rather than printing a zero the family never owed.
func (a Advice) Message(name string, amountCents *int64, date time.Time) (title, body string) {
	when := Midnight(date).Format("Jan 2")
	amount := "A payment"
	if amountCents != nil {
		amount = "Payment of $" + Dollars(*amountCents)
	}
	switch a.Attention {
	case AttentionOverdue:
		title = Truncate(name+" is overdue", 500)
		body = fmt.Sprintf("%s was due on %s and nothing has paid it.", amount, when)
		if len(a.Missing) > 0 {
			body += " Also, " + strings.Join(a.Missing, " and ") + "."
		}
		return title, body
	case AttentionNeedsSetup:
		// What is missing comes first: someone reading the notification on a
		// lock screen should be able to act on it without opening the app.
		title = Truncate(name+" needs setting up", 500)
		body = fmt.Sprintf("%s — %s; %s due %s.",
			name, strings.Join(a.Missing, " and "), lowerFirst(amount), when)
		return title, body
	default:
		title = Truncate(name+" due soon", 500)
		return title, amount + " due on " + when
	}
}

// DigestMessage is the one notification a family gets instead of many: every
// bill, what it costs, when it is due and (for needs_setup) what is missing.
func DigestMessage(lines []string) (title, body string) {
	if len(lines) == 0 {
		return "", ""
	}
	return Truncate(fmt.Sprintf("%d bills need you", len(lines)), 500), strings.Join(lines, "\n")
}

// DigestKey identifies one digest: its family and the exact set of reminders it
// stands in for, so a re-run is a no-op and a set that changes sends a new
// digest instead of being suppressed by the old one.
func DigestKey(householdID uuid.UUID, keys []string) string {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	return fmt.Sprintf("bill_digest:%s:%s", householdID, strings.Join(sorted, "|"))
}

// Dollars formats cents as a plain amount with two decimals: 18000 -> "180.00".
func Dollars(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

// Truncate shortens s to at most n bytes (notifications.title is VARCHAR(500)).
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// DigestLine is one bill's line in a digest: the same facts a single reminder
// carries, with the name in front because a digest lists several bills.
func (a Advice) DigestLine(name string, amountCents *int64, date time.Time) string {
	when := Midnight(date).Format("Jan 2")
	amount := "a payment"
	if amountCents != nil {
		amount = "payment of $" + Dollars(*amountCents)
	}
	switch a.Attention {
	case AttentionOverdue:
		return Truncate(fmt.Sprintf("%s — overdue: %s was due %s and nothing has paid it.", name, amount, when), 500)
	case AttentionNeedsSetup:
		return Truncate(fmt.Sprintf("%s — %s; %s due %s.",
			name, strings.Join(a.Missing, " and "), amount, when), 500)
	default:
		return Truncate(fmt.Sprintf("%s — %s due %s.", name, amount, when), 500)
	}
}
