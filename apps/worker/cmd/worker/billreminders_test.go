package main

// The notification policy's four cases (#5500), as the ticket names them:
//
//  1. an autopay bill near its due date produces NO notification,
//  2. a manual bill near its due date produces exactly ONE, naming the amount
//     and the due date,
//  3. autopay off and no account says what is MISSING, not merely that it is
//     due,
//  4. running the worker twice still produces exactly one.
//
// Every case is checked twice: on the pure policy (billRemindersFor, no
// database) and end to end through insertDueNotifications, because the SQL is
// where a second copy of the rule would hide.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/billdue"
)

// policyCandidate is a bill as the policy sees it, with the fields that are
// awkward in a table literal filled in by the caller.
func policyCandidate(t *testing.T, hh uuid.UUID, name string, dueDay int, autopay, hasAccount bool, createdAt time.Time) billCandidate {
	t.Helper()
	day := dueDay
	return billCandidate{
		ID:          uuid.New(),
		HouseholdID: hh,
		Name:        name,
		Timezone:    "UTC",
		Frequency:   "monthly",
		DueDay:      &day,
		CreatedAt:   createdAt,
		Autopay:     autopay,
		HasAccount:  hasAccount,
	}
}

// cents is an amount for a candidate.
func cents(v int64) *int64 { return &v }

func TestBillReminderPolicy(t *testing.T) {
	// 21 Nov 2026. A monthly bill due on the 24th is 3 days out: inside the
	// worker's 3-day lead and inside needs_setup's 7-day one.
	today := time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC)
	hh := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	// A bill entered today and one that has been paid up to last month are both
	// "not late": the first because a due date before the row existed proves
	// nothing, the second because the money moved. A bill on the books since
	// January with nothing paid IS late, which is what the overdue case uses.
	established := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	paidLastMonth := func(c billCandidate) billCandidate {
		d := time.Date(2026, time.October, 24, 0, 0, 0, 0, time.UTC)
		c.PaidFor = &d
		return c
	}

	t.Run("1: autopay near its due date is silence", func(t *testing.T) {
		auto := policyCandidate(t, hh, "Mortgage", 24, true, true, today)
		auto.AmountCents = cents(234000)
		for _, c := range []billCandidate{
			auto,
			paidLastMonth(auto),
			policyCandidate(t, hh, "Mortgage today", 21, true, true, today),
			policyCandidate(t, hh, "Mortgage adding up", 28, true, true, today),
		} {
			got := billRemindersFor(today, []billCandidate{c}, billLeadDays)
			if len(got) != 0 {
				t.Errorf("%s (autopay, account set) produced %d reminders, want 0: %+v", c.Name, len(got), got)
			}
		}
	})

	t.Run("2: a manual bill near its due date is exactly one reminder naming amount and date", func(t *testing.T) {
		manual := policyCandidate(t, hh, "Electric", 24, false, true, today)
		manual.AmountCents = cents(18000)
		got := billRemindersFor(today, []billCandidate{manual}, billLeadDays)
		if len(got) != 1 {
			t.Fatalf("got %d reminders, want exactly 1: %+v", len(got), got)
		}
		if !strings.Contains(got[0].Body, "$180.00") {
			t.Errorf("body = %q, want the amount $180.00 in it", got[0].Body)
		}
		if !strings.Contains(got[0].Body, "Nov 24") {
			t.Errorf("body = %q, want the due date Nov 24 in it", got[0].Body)
		}
	})

	t.Run("3: autopay off and no account says what is missing", func(t *testing.T) {
		bare := policyCandidate(t, hh, "Electric", 24, false, false, today)
		bare.AmountCents = cents(18000)
		got := billRemindersFor(today, []billCandidate{bare}, billLeadDays)
		if len(got) != 1 {
			t.Fatalf("got %d reminders, want exactly 1: %+v", len(got), got)
		}
		body := got[0].Body
		for _, want := range []string{"autopay is off", "no account is set", "$180.00", "Nov 24"} {
			if !strings.Contains(body, want) {
				t.Errorf("body = %q, want %q in it", body, want)
			}
		}
		// The point of the case: it is not merely "it is due".
		if body == "Payment of $180.00 due on Nov 24" {
			t.Errorf("body = %q: a needs_setup reminder that says nothing is missing", body)
		}
		// And it is reminded EARLIER than an ordinary manual bill: 7 days out
		// is inside its lead but outside the 3-day one.
		far := policyCandidate(t, hh, "Electric", 28, false, false, today)
		far.AmountCents = cents(18000)
		if got := billRemindersFor(today, []billCandidate{far}, billLeadDays); len(got) != 1 {
			t.Errorf("a needs_setup bill 7 days out produced %d reminders, want 1 (it is reminded early)", len(got))
		}
		slow := policyCandidate(t, hh, "Electric", 28, false, true, today)
		slow.AmountCents = cents(18000)
		if got := billRemindersFor(today, []billCandidate{slow}, billLeadDays); len(got) != 0 {
			t.Errorf("a due_soon bill 7 days out produced %d reminders, want 0 (3-day lead)", len(got))
		}
	})

	t.Run("an overdue bill escalates, and names the date it MISSED", func(t *testing.T) {
		// Due on the 5th, never paid, on the books since January: 16 days late.
		// Its NEXT due date is 5 Dec, four weeks away, and naming that would be
		// the lie this case exists to prevent.
		late := policyCandidate(t, hh, "Water", 5, false, true, established)
		late.AmountCents = cents(4200)
		got := billRemindersFor(today, []billCandidate{late}, billLeadDays)
		if len(got) != 1 {
			t.Fatalf("got %d reminders, want exactly 1 (overdue escalates): %+v", len(got), got)
		}
		if got[0].Attention != billdue.AttentionOverdue {
			t.Errorf("attention = %q, want %q", got[0].Attention, billdue.AttentionOverdue)
		}
		if !strings.Contains(got[0].Body, "Nov 5") {
			t.Errorf("body = %q, want the MISSED date Nov 5, not the next due date", got[0].Body)
		}
		if strings.Contains(got[0].Body, "Dec 5") {
			t.Errorf("body = %q names the next due date; an overdue reminder must name the missed one", got[0].Body)
		}
	})

	t.Run("a bill added today is not late for a date before it existed", func(t *testing.T) {
		// The trap billdue.AttentionFor names: a bill entered now, due on the
		// 5th, was not "missed" on 5 Nov because the row did not exist.
		fresh := policyCandidate(t, hh, "Dentist", 5, false, true, today)
		got := billRemindersFor(today, []billCandidate{fresh}, billLeadDays)
		for _, r := range got {
			if r.Attention == billdue.AttentionOverdue {
				t.Fatalf("a bill created today reads overdue: %+v", r)
			}
		}
	})
}

func TestBillReminders_Postgres(t *testing.T) {
	pool := newMigratedPool(t)

	// clear empties the tables these tests share (they are one database).
	clear := func(t *testing.T) *pgxpool.Pool {
		t.Helper()
		if _, err := pool.Exec(context.Background(),
			`TRUNCATE notifications, bills, maintenance_tasks, payment_accounts CASCADE`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return pool
	}

	// 21 Nov 2026, and a bill entered that day: the ordinary case of recording a
	// bill the family has been paying, which is NOT late (billdue.AttentionFor
	// checks createdAt). The overdue case below says so explicitly instead.
	now := day("2026-11-21")
	established := "2026-11-21T00:00:00Z"
	overdueSince := "2026-01-01T00:00:00Z"

	// seedPolicyBill inserts a bill with every field the policy reads.
	seedPolicyBill := func(t *testing.T, hh, name string, amount float64, dueDay int, autopay, withAccount bool) string {
		t.Helper()
		var account *string
		if withAccount {
			// A payment account is only on record for an autopay bill (075's
			// bills_autopay_account_check refuses one on a manual bill), so the
			// account for a manual bill is the legacy free-text column -- which
			// is how billdue.AttentionFor reaches due_soon at all.
			if autopay {
				var accID string
				if err := pool.QueryRow(context.Background(),
					`INSERT INTO payment_accounts (household_id, name) VALUES ($1, 'Joint checking') RETURNING id::text`,
					hh).Scan(&accID); err != nil {
					t.Fatalf("insert payment account: %v", err)
				}
				var id string
				if err := pool.QueryRow(context.Background(),
					`INSERT INTO bills (household_id, name, amount, due_day, is_autopay, autopay_account_id, created_at)
					 VALUES ($1, $2, $3, $4, true, $5, $6) RETURNING id::text`,
					hh, name, amount, dueDay, accID, established).Scan(&id); err != nil {
					t.Fatalf("insert autopay bill: %v", err)
				}
				return id
			}
			legacy := "legacy-4471"
			account = &legacy
		}
		var id string
		if err := pool.QueryRow(context.Background(),
			`INSERT INTO bills (household_id, name, amount, due_day, is_autopay, account_number, created_at)
			 VALUES ($1, $2, $3, $4, COALESCE($5::boolean, false), $6, $7) RETURNING id::text`,
			hh, name, amount, dueDay, autopay, account, established).Scan(&id); err != nil {
			t.Fatalf("insert bill: %v", err)
		}
		return id
	}

	t.Run("1: an autopay bill near its due date produces no notification", func(t *testing.T) {
		pool := clear(t)
		hh := seedHousehold(t, pool)
		billID := seedPolicyBill(t, hh, "Mortgage", 2340.00, 24, true, true)

		got, err := insertDueNotifications(context.Background(), pool, now)
		if err != nil {
			t.Fatalf("insertDueNotifications: %v", err)
		}
		for _, n := range got {
			if n.Type == "bill_due" || n.Type == "bill_digest" {
				t.Errorf("an autopay bill produced %+v, want no bill notification at all", n)
			}
		}
		if rows := notificationCount(t, pool); rows != 0 {
			t.Errorf("%d notifications written for a bill that pays itself, want 0", rows)
		}
		if n := notifiedEntities(t, pool, "bill_due")[billID]; n != 0 {
			t.Errorf("bill_due rows for the autopay bill = %d, want 0", n)
		}
	})

	t.Run("2: a manual bill near its due date is exactly one, naming amount and date", func(t *testing.T) {
		pool := clear(t)
		hh := seedHousehold(t, pool)
		billID := seedPolicyBill(t, hh, "Electric", 180.00, 24, false, true)

		got, err := insertDueNotifications(context.Background(), pool, now)
		if err != nil {
			t.Fatalf("insertDueNotifications: %v", err)
		}
		var bills []notif
		for _, n := range got {
			if n.Type == "bill_due" {
				bills = append(bills, n)
			}
		}
		if len(bills) != 1 {
			t.Fatalf("got %d bill notifications, want exactly 1: %+v", len(bills), got)
		}
		if got := bills[0].Body; !strings.Contains(got, "$180.00") || !strings.Contains(got, "Nov 24") {
			t.Errorf("body = %q, want the amount $180.00 and the due date Nov 24", got)
		}
		if n := notifiedEntities(t, pool, "bill_due")[billID]; n != 1 {
			t.Errorf("bill_due rows for %s = %d, want 1", billID, n)
		}
	})

	t.Run("3: autopay off and no account says what is missing", func(t *testing.T) {
		pool := clear(t)
		hh := seedHousehold(t, pool)
		seedPolicyBill(t, hh, "Electric", 180.00, 24, false, false)

		got, err := insertDueNotifications(context.Background(), pool, now)
		if err != nil {
			t.Fatalf("insertDueNotifications: %v", err)
		}
		var body string
		for _, n := range got {
			if n.Type == "bill_due" {
				body = n.Body
			}
		}
		if body == "" {
			t.Fatalf("got %+v, want one bill_due", got)
		}
		for _, want := range []string{"autopay is off", "no account is set", "$180.00", "Nov 24"} {
			if !strings.Contains(body, want) {
				t.Errorf("body = %q, want %q in it", body, want)
			}
		}
	})

	t.Run("4: running the worker twice still produces exactly one notification", func(t *testing.T) {
		pool := clear(t)
		hh := seedHousehold(t, pool)
		billID := seedPolicyBill(t, hh, "Electric", 180.00, 24, false, false)

		ctx := context.Background()
		// generateNotifications is the loop's real entry point (it also emails;
		// a nil SMTP client skips that).
		if err := generateNotifications(ctx, pool, nil, now); err != nil {
			t.Fatalf("first run: %v", err)
		}
		first := notificationCount(t, pool)
		if first != 1 {
			t.Fatalf("first run wrote %d notifications, want 1", first)
		}

		if err := generateNotifications(ctx, pool, nil, now); err != nil {
			t.Fatalf("second run: %v", err)
		}
		if got := notificationCount(t, pool); got != 1 {
			t.Errorf("after the second run %d notifications, want still 1", got)
		}

		// The same tick, twice more, through the inner entry point and an hour
		// later in wall-clock terms: the key is (bill, date, attention), so
		// time passing is not a reason to remind a family again.
		for _, at := range []time.Time{now, now.Add(time.Hour), now.AddDate(0, 0, 1)} {
			if _, err := insertDueNotifications(ctx, pool, at); err != nil {
				t.Fatalf("re-run at %s: %v", at, err)
			}
		}
		if got := notificationCount(t, pool); got != 1 {
			t.Errorf("after three re-runs %d notifications, want still 1", got)
		}
		if n := notifiedEntities(t, pool, "bill_due")[billID]; n != 1 {
			t.Errorf("bill_due rows for %s = %d, want 1", billID, n)
		}
	})

	t.Run("the noise cap: twelve overdue bills are one digest, not twelve pushes", func(t *testing.T) {
		pool := clear(t)
		hh := seedHousehold(t, pool)
		for i := 1; i <= 12; i++ {
			// Due on the 1st, never paid, on the books since January: all twelve
			// are overdue on 21 Nov.
			id := seedPolicyBill(t, hh, "Bill "+string(rune('A'+i-1)), float64(10*i), 1, false, true)
			if _, err := pool.Exec(context.Background(), `UPDATE bills SET created_at = $2 WHERE id = $1`, id, overdueSince); err != nil {
				t.Fatalf("set created_at: %v", err)
			}
		}

		got, err := insertDueNotifications(context.Background(), pool, now)
		if err != nil {
			t.Fatalf("insertDueNotifications: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d notifications, want exactly one digest: %+v", len(got), got)
		}
		if got[0].Type != billdue.NotificationTypeDigest {
			t.Errorf("type = %q, want %q", got[0].Type, billdue.NotificationTypeDigest)
		}
		if !strings.HasPrefix(got[0].Title, "12 bills") {
			t.Errorf("title = %q, want it to say how many bills", got[0].Title)
		}
		for _, want := range []string{"Bill A", "Bill L", "$120.00", "overdue"} {
			if !strings.Contains(got[0].Body, want) {
				t.Errorf("digest body is missing %q:\n%s", want, got[0].Body)
			}
		}
		if rows := notificationCount(t, pool); rows != 1 {
			t.Errorf("%d notification rows, want 1", rows)
		}

		// And the digest is deduped like everything else.
		if _, err := insertDueNotifications(context.Background(), pool, now.Add(time.Minute)); err != nil {
			t.Fatalf("re-run: %v", err)
		}
		if rows := notificationCount(t, pool); rows != 1 {
			t.Errorf("after a re-run %d notification rows, want 1", rows)
		}
	})

	t.Run("two families: three reminders each, four in one family is a digest", func(t *testing.T) {
		pool := clear(t)
		small := seedHousehold(t, pool)
		big := seedHousehold(t, pool)
		for i := 1; i <= billdue.DigestThreshold; i++ {
			seedPolicyBill(t, small, "Small "+string(rune('A'+i-1)), 10, 24, false, true)
		}
		for i := 1; i <= billdue.DigestThreshold+1; i++ {
			seedPolicyBill(t, big, "Big "+string(rune('A'+i-1)), 10, 24, false, true)
		}

		got, err := insertDueNotifications(context.Background(), pool, now)
		if err != nil {
			t.Fatalf("insertDueNotifications: %v", err)
		}
		var single, digest int
		for _, n := range got {
			switch n.Type {
			case billdue.NotificationTypeBill:
				single++
				if n.HouseholdID != small {
					t.Errorf("a single reminder went to the wrong family: %+v", n)
				}
			case billdue.NotificationTypeDigest:
				digest++
				if n.HouseholdID != big {
					t.Errorf("a digest went to the wrong family: %+v", n)
				}
			}
		}
		if single != billdue.DigestThreshold || digest != 1 {
			t.Errorf("got %d single and %d digests, want %d and 1", single, digest, billdue.DigestThreshold)
		}
	})
}

// notificationCount is how many notifications exist, for the "exactly one"
// assertions that are about the table rather than the returned rows.
func notificationCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications`).Scan(&n); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	return n
}
