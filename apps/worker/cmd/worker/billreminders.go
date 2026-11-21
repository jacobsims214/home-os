package main

// Bill reminders: the notification policy, which is "autopay means silence".
//
// # One rule, called and never copied
//
// Which bills earn a reminder is decided by billdue.AttentionFor (#5498), via
// billdue.ReminderFor, which also names the date the reminder should carry.
// billdue.AdviceFor then turns that state into an action. There is deliberately
// no `if autopay { ... } else { ... }` in this file and none in the SQL below:
// this codebase has twice shipped one rule in two places (the money redaction
// #4806/#4912, the due rule #5511) and a second copy of "is this bill on me?" is
// exactly how the worker would start contradicting the UI and the agent.
//
//	automatic    silence. It pays itself, and telling the family is noise.
//	needs_setup  remind early, and say what is MISSING (autopay off, no
//	             account) rather than merely that the bill is due.
//	due_soon     remind at the configured lead time.
//	overdue      remind now and escalate: nothing was going to pay it.
//
// # Idempotency
//
// A reminder is keyed on (bill, date, attention state) — billdue.AdviceFor's
// DedupeKey — and written into notifications.dedupe_key. Migration 081's
// idx_notifications_dedupe_family makes that unique per household for the
// family-wide rows this job writes (user_id NULL, which 056's per-member index
// cannot cover), so a second worker tick, a restart mid-run or a second replica
// inserts nothing. The date is part of the key, so next month's reminder is a
// new one; the state is too, so a bill that goes from due_soon to overdue earns
// the escalated reminder it now deserves.
//
// # The noise cap
//
// A family with twelve overdue bills gets ONE digest, not twelve pushes: above
// billdue.DigestThreshold reminders in a single run, they are collapsed into one
// bill_digest notification that still names every bill, its amount, its date and
// what is missing. Three is a nudge and a fourth push in the same tick is where
// a family stops reading them — and a first import of a dozen paper bills is
// exactly when that would happen. The digest is keyed on the set of reminders it
// stands in for, so a re-run is a no-op and a changed set sends a new one.
//
// # Delivery
//
// Nothing new: an in-app row in notifications, exactly like bill_due before it
// (same table, same read path, same role filter on the family's bill read), and
// the existing best-effort email in sendNotificationEmails. The single-reminder
// type stays 'bill_due' so the existing client icons and email template keep
// working; only the digest is a new type.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/billdue"
)

// billCandidate is one bill as the policy needs it: its schedule, its payments,
// when the row was created (the overdue rule needs it), and whether anything is
// set up to pay it.
type billCandidate struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	Name        string
	Timezone    string
	Frequency   string
	DueDay      *int
	DueDate     *time.Time
	PaidDate    *time.Time
	PaidFor     *time.Time
	CreatedAt   time.Time
	Autopay     bool
	AmountCents *int64
	HasAccount  bool
}

// billCandidatesSQL lists every bill of every household with a usable schedule,
// with what billdue.AttentionFor needs to answer "is this on me?".
//
// has_account is the SAME definition apps/api/internal/bill.hasPaymentSource and
// apps/mcp/internal/bill.billRow.hasPaymentSource use: a payment_accounts row
// linked by bills.autopay_account_id, or the legacy free-text bills.account_number
// on a pre-075 row (a manual bill cannot hold an autopay account, 075's
// bills_autopay_account_check). It is computed here and never serialized: the
// account number is plaintext and must not leave the database, and nothing about
// the account is written into a notification.
//
// is_autopay is COALESCEd because 020 added the column nullable and 075 made it
// NOT NULL DEFAULT false; a row from between the two can still be NULL, and NULL
// must mean "a person pays this" (false), never silence.
//
// Bounds: a once bill due more than a day before now's UTC date can never be in
// any household's window (every local date is within a day of UTC), so it is not
// fetched; and a bill already reminded for the date and state it is still in is
// skipped by NOT EXISTS on dedupe_key, which is what makes the common case (the
// same bill, every tick) cheap. A bill with no usable schedule is not fetched
// either — billdue.AttentionFor would call it needs_setup, which is true, but
// there is no date to name and the family has to fix the schedule first.
const billCandidatesSQL = `
	SELECT b.id, b.household_id, b.name, h.timezone, b.frequency, b.due_day, b.due_date,
	       b.paid_date, b.paid_for_due_date, b.created_at,
	       COALESCE(b.is_autopay, false),
	       ROUND(b.amount * 100)::bigint,
	       (b.autopay_account_id IS NOT NULL OR NULLIF(btrim(b.account_number), '') IS NOT NULL)
	FROM bills b
	JOIN households h ON h.id = b.household_id
	WHERE ((b.frequency = 'monthly' AND b.due_day BETWEEN 1 AND 31)
	    OR (b.frequency <> 'monthly' AND b.due_date IS NOT NULL))
	  AND NOT (b.frequency = 'once' AND b.due_date < $1::date - 1)
`

// billReminder is one reminder the policy decided to send, before it is written.
type billReminder struct {
	HouseholdID uuid.UUID
	BillID      uuid.UUID
	Key         string
	Attention   string
	Title       string
	Body        string
	// Line is the same facts as one line of a digest (billdue.Advice.DigestLine),
	// so a digest and a single reminder never say different things about a bill.
	Line string
	// Date is the date the reminder names: the due date, or the missed one for
	// an overdue bill.
	Date time.Time
}

// billRemindersFor is the policy applied to the candidates: which bills earn a
// reminder as of now, what it says, and what it is keyed on. It is pure, so the
// four cases the ticket names are all testable without a database.
//
// The date used for the lead-time test is billdue.ReminderFor's: days until the
// named date, so a needs_setup bill is reminded at AttentionLeadDays and an
// ordinary manual bill at leadDays. An overdue bill fires whatever the distance
// to its next due date, because the missed date is the point.
func billRemindersFor(now time.Time, candidates []billCandidate, leadDays int) []billReminder {
	out := make([]billReminder, 0, len(candidates))
	for _, c := range candidates {
		today := billdue.HouseholdToday(now, c.Timezone)
		// created_at is an instant; the overdue rule compares CALENDAR DATES, and
		// the one that matters is the family's (03:00 UTC on the 10th is still the
		// evening of the 9th in Chicago, and a bill entered then did not exist for
		// a due date on the 9th). Converting through the household's zone here,
		// rather than reading the instant's own date, also keeps the answer
		// independent of the session time zone pgx happened to scan it in.
		createdOn := billdue.HouseholdToday(c.CreatedAt, c.Timezone)
		s := billdue.Schedule{Frequency: c.Frequency, DueDay: c.DueDay, DueDate: c.DueDate}
		state, date, daysUntil, ok := billdue.ReminderFor(today, s, c.PaidDate, c.PaidFor, createdOn, c.Autopay, c.HasAccount)
		if !ok {
			continue
		}
		advice := billdue.AdviceFor(state, leadDays)
		if !advice.Due(daysUntil) {
			continue
		}
		title, body := advice.Message(c.Name, c.AmountCents, date)
		out = append(out, billReminder{
			HouseholdID: c.HouseholdID,
			BillID:      c.ID,
			Key:         advice.DedupeKey(c.ID, date),
			Attention:   advice.Attention,
			Title:       title,
			Body:        body,
			Line:        advice.DigestLine(c.Name, c.AmountCents, date),
			Date:        date,
		})
	}
	return out
}

// insertBillReminders runs the policy over every household's bills and writes
// what it decides: one notification per bill, or one digest per family above
// billdue.DigestThreshold. Both are deduped on their key inside the INSERT, so
// two overlapping runs still write one of each.
func insertBillReminders(ctx context.Context, pool *pgxpool.Pool, now time.Time) ([]notif, error) {
	rows, err := pool.Query(ctx, billCandidatesSQL, now.UTC().Format(time.DateOnly))
	if err != nil {
		return nil, fmt.Errorf("bill reminders: %w", err)
	}
	candidates, err := pgx.CollectRows(rows, pgx.RowToStructByPos[billCandidate])
	if err != nil {
		return nil, fmt.Errorf("bill reminders: %w", err)
	}
	reminders := billRemindersFor(now, candidates, billLeadDays)
	if len(reminders) == 0 {
		return nil, nil
	}

	// Group by family: the noise cap is per family, because that is who reads
	// the notifications. One family with twelve bills is a digest; twelve
	// families with one each are twelve ordinary reminders.
	byHousehold := map[uuid.UUID][]billReminder{}
	order := make([]uuid.UUID, 0, len(reminders))
	for _, r := range reminders {
		if _, seen := byHousehold[r.HouseholdID]; !seen {
			order = append(order, r.HouseholdID)
		}
		byHousehold[r.HouseholdID] = append(byHousehold[r.HouseholdID], r)
	}

	var allNotifs []notif
	for _, hh := range order {
		group := byHousehold[hh]
		if len(group) <= billdue.DigestThreshold {
			got, err := insertSingleReminders(ctx, pool, group)
			if err != nil {
				return nil, err
			}
			allNotifs = append(allNotifs, got...)
			continue
		}
		got, err := insertBillDigest(ctx, pool, hh, group)
		if err != nil {
			return nil, err
		}
		allNotifs = append(allNotifs, got...)
	}
	return allNotifs, nil
}

// insertSingleRemindersSQL writes one family-wide reminder per bill and skips a
// bill that already has this exact (bill, date, attention) reminder. The key is
// checked in the WHERE and enforced by idx_notifications_dedupe_family, so
// neither a re-run nor a second replica can duplicate it.
//
// user_id is left NULL on purpose: a bill reminder is for the family, and the
// read paths treat NULL as "every member whose role may see a bill"
// (role_can_see_entity('bill') is owner/family_manager/family_member/viewer, the
// same roles that may read the bills route), so a housesitter or a vendor is not
// told about the family's money — the same rule alarms.insertSQL applies to
// events.
const insertSingleRemindersSQL = `
	INSERT INTO notifications (household_id, type, title, body, entity_type, entity_id, dedupe_key)
	SELECT r.household_id, $2::varchar, r.title, r.body, 'bill'::entity_kind, r.bill_id, r.dedupe_key
	FROM unnest($1::uuid[], $3::uuid[], $4::text[], $5::text[], $6::text[])
	     AS r(household_id, bill_id, dedupe_key, title, body)
	WHERE NOT EXISTS (
		SELECT 1 FROM notifications n
		WHERE n.household_id = r.household_id AND n.dedupe_key = r.dedupe_key
		  AND n.user_id IS NULL
	)
	ON CONFLICT DO NOTHING
	RETURNING id, household_id, type, title, body
`

func insertSingleReminders(ctx context.Context, pool *pgxpool.Pool, group []billReminder) ([]notif, error) {
	var households, bills, keys, titles, bodies []string
	for _, r := range group {
		households = append(households, r.HouseholdID.String())
		bills = append(bills, r.BillID.String())
		keys = append(keys, r.Key)
		titles = append(titles, r.Title)
		bodies = append(bodies, r.Body)
	}
	rows, err := pool.Query(ctx, insertSingleRemindersSQL,
		households, billdue.NotificationTypeBill, bills, keys, titles, bodies)
	if err != nil {
		return nil, fmt.Errorf("bill reminders: %w", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[notif])
	if err != nil {
		return nil, fmt.Errorf("bill reminders: %w", err)
	}
	return got, nil
}

// insertBillDigestSQL writes the one notification that stands in for a family's
// too-many reminders. It carries no entity: a digest is about several bills, and
// pointing it at one of them would open the wrong record. Its dedupe key is the
// set it replaces (billdue.DigestKey), so it is written once and a changed set
// writes a new one.
const insertBillDigestSQL = `
	INSERT INTO notifications (household_id, type, title, body, dedupe_key)
	SELECT $1::uuid, $2::varchar, $3::varchar, $4::text, $5::text
	WHERE NOT EXISTS (
		SELECT 1 FROM notifications n
		WHERE n.household_id = $1::uuid AND n.dedupe_key = $5::text
		  AND n.user_id IS NULL
	)
	ON CONFLICT DO NOTHING
	RETURNING id, household_id, type, title, body
`

func insertBillDigest(ctx context.Context, pool *pgxpool.Pool, hh uuid.UUID, group []billReminder) ([]notif, error) {
	lines := make([]string, 0, len(group))
	keys := make([]string, 0, len(group))
	for _, r := range group {
		// The digest line is the reminder's own body, which already names the
		// amount, the date and (for needs_setup) what is missing. It reuses the
		// body rather than rebuilding it, so a digest and a single reminder can
		// never say different things about the same bill.
		lines = append(lines, r.Line)
		keys = append(keys, r.Key)
	}
	title, body := billdue.DigestMessage(lines)
	rows, err := pool.Query(ctx, insertBillDigestSQL,
		hh, billdue.NotificationTypeDigest, title, body, billdue.DigestKey(hh, keys))
	if err != nil {
		return nil, fmt.Errorf("bill digest: %w", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[notif])
	if err != nil {
		return nil, fmt.Errorf("bill digest: %w", err)
	}
	return got, nil
}
