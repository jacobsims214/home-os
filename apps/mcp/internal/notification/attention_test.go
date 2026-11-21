package notification

// The test that matters (#5501): an agent given ONLY the tool output answers
// "what needs my person's attention right now?" — and one autopay bill that is
// already handled correctly produces NOTHING.
//
// A bill_due reminder is not evidence that anything needs doing. A family whose
// electricity pays itself still gets a bill_due row every month, exactly like
// the water bill somebody has to pay. An agent that reads "there is a bill_due"
// as "a bill needs paying" nags its person about money that already left the
// account. So every notification about a bill carries needs_you (true/false,
// always present) and attention (billdue.AttentionFor's four states, the SAME
// rule the UI and bill_find use, #5498).
//
// This test reads only the JSON the tool returned — the same bytes a model
// would — and asserts the conclusion a model must reach. It never consults the
// database to decide what the answer is; the database is only where the
// fixtures were written.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/testutil"
)

// attentionHarness seeds one bill in each of billdue.AttentionFor's four
// states, each with a bill_due reminder waiting. The states are the branches a
// model must tell apart, so the fixtures cover all four:
//
//	automatic   autopay on, an account on record   -> NOTHING needs the person
//	needs_setup no autopay, no account on record   -> needs the person
//	due_soon    manual, an account on record       -> needs the person
//	overdue     manual, past its due date, unpaid  -> needs the person
type attentionHarness struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	hh     testutil.Household
	alerts map[string]string // bill name -> its notification id
}

// newAttentionHarness seeds the four bills and their reminders. The due day is
// the 20th of the month, two months of lead time are avoided by pinning the
// bills' created_at in the past so "overdue" is unambiguous.
func newAttentionHarness(t *testing.T) *attentionHarness {
	t.Helper()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	if _, err := pool.Exec(context.Background(),
		`UPDATE households SET timezone = 'UTC' WHERE id = $1`, hh.HouseholdID); err != nil {
		t.Fatal(err)
	}
	h := &attentionHarness{ctx: ctx, pool: pool, hh: hh, alerts: map[string]string{}}

	account := insertID(t, pool, `INSERT INTO payment_accounts (household_id, name, last_four)
		VALUES ($1, 'Joint checking', '4242') RETURNING id`, hh.HouseholdID)

	// The three bills that are NOT overdue are created today. billdue treats a
	// bill created after its last due date as "not late over it" (the import
	// case: the family may have paid before entering the row), so only the
	// overdue fixture below is created in the past.
	//
	// automatic: autopay on AND an account on record -> billdue says automatic.
	automatic := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day, is_autopay, autopay_account_id)
		VALUES ($1, 'Electric', 142.50, 20, true, $2) RETURNING id`, hh.HouseholdID, account)
	// needs_setup: no autopay, no account on record -> nothing will pay it.
	needsSetup := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day)
		VALUES ($1, 'School trip', 80.00, 20) RETURNING id`, hh.HouseholdID)
	// due_soon: manual, a legacy account_number on record -> a person pays it.
	dueSoon := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day, account_number)
		VALUES ($1, 'Water', 48.00, 20, '1234') RETURNING id`, hh.HouseholdID)
	// overdue: manual, no account, and its most recent due date came and went
	// while the row existed. A once bill whose date is in the past is the
	// cleanest overdue: there is only one due date and it has passed.
	overdue := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, frequency, due_date, created_at)
		VALUES ($1, 'Internet', 90.00, 'once', CURRENT_DATE - 10, NOW() - INTERVAL '90 days') RETURNING id`, hh.HouseholdID)

	for name, id := range map[string]string{
		"Electric": automatic, "School trip": needsSetup, "Water": dueSoon, "Internet": overdue,
	} {
		h.alerts[name] = notify(t, pool, hh.HouseholdID, "", "bill_due", name+" is due", "bill", id, 2*time.Hour)
	}
	return h
}

// TestWhatNeedsMyPersonNow is the acceptance test the ticket names. It reads
// only the tool output and asserts the answer.
func TestWhatNeedsMyPersonNow(t *testing.T) {
	t.Parallel()
	h := newAttentionHarness(t)
	_, _, find := NewNotificationFindTool(h.pool)

	env := call[findResult](t, h.ctx, "notification_find", find, map[string]any{"unread_only": true, "limit": 100})
	rows := env.data(t).Notifications

	// The model's answer, built ONLY from the JSON: a row needs the person
	// when needs_you is true. No second source, no database.
	needs := map[string]bool{}
	for _, r := range rows {
		if r.AboutType == nil || *r.AboutType != "bill" {
			continue
		}
		needs[r.Title] = r.NeedsYou
	}
	if len(needs) != 4 {
		t.Fatalf("expected 4 bill notifications in the output, got %d: %v", len(needs), needs)
	}

	// THE case the ticket names: the autopay bill produces NOTHING for the
	// person. It must be present in the feed (the family was told) and must
	// not be in the answer.
	if needs["Electric is due"] {
		t.Error("autopay bill \"Electric is due\" was reported as needing the person; a handled bill must produce nothing")
	}
	// The three that are genuinely on the person must be in the answer.
	for _, title := range []string{"School trip is due", "Water is due", "Internet is due"} {
		if !needs[title] {
			t.Errorf("%q did not need the person; a bill nobody is paying must be in the answer", title)
		}
	}

	// The same conclusion, spelled the way an agent reads it: the count of
	// rows that need the person, and the summary's own words.
	var attention []string
	for _, r := range rows {
		if r.AboutType != nil && *r.AboutType == "bill" && r.Attention != nil {
			attention = append(attention, r.Title+"="+*r.Attention)
		}
	}
	// The four attention states the model has to distinguish, straight out of
	// the output. If these were absent or all "automatic", a model could not
	// tell a handled bill from an unhandled one.
	want := map[string]string{
		"Electric is due":    "automatic",
		"School trip is due": "needs_setup",
		"Water is due":       "due_soon",
		"Internet is due":    "overdue",
	}
	for _, a := range attention {
		var title, state string
		for i := 0; i < len(a); i++ {
			if a[i] == '=' {
				title, state = a[:i], a[i+1:]
				break
			}
		}
		if want[title] != state {
			t.Errorf("%q attention = %q, want %q (from %v)", title, state, want[title], attention)
		}
	}
	if len(attention) != 4 {
		t.Errorf("expected 4 attention states in the output, got %v", attention)
	}
}

// TestWhatNeedsMyPersonNowCount: the envelope's own summary and needs_you count
// agree with the rows, so an agent that reads only the summary (a small model
// often does) still gets the right answer.
func TestWhatNeedsMyPersonNowCount(t *testing.T) {
	t.Parallel()
	h := newAttentionHarness(t)
	_, _, find := NewNotificationFindTool(h.pool)

	env := call[findResult](t, h.ctx, "notification_find", find, map[string]any{"unread_only": true, "limit": 100})
	d := env.data(t)

	// Three of the four bills are on the person; the autopay one is not.
	if d.NeedsYou != 3 {
		t.Errorf("needs_you = %d, want 3 (four bills, one pays itself)", d.NeedsYou)
	}
	if d.Returned != 4 {
		t.Errorf("returned = %d, want 4 (the handled bill is still in the feed)", d.Returned)
	}
	// The summary has to say so in words, not only in a number: a model that
	// reads only the summary must still learn that one of the four is handled.
	if !strings.Contains(env.Summary, "1 already handled") {
		t.Errorf("summary = %q, want it to say one of the four is already handled", env.Summary)
	}
}

// TestNotificationToolsFollowTheMoneyPolicy is the role-scoping requirement:
// the three tools are scoped exactly like the UI. A housesitter or vendor has
// no money read (auth.CanSeeMoney false), so a bill_due notification is not
// theirs to see at all — not in find, not by id in get, and not markable. The
// rule is the existing one (role_can_see_entity('bill'), the SQL copy of
// auth.CanSeeEntity): no second policy is invented here.
func TestNotificationToolsFollowTheMoneyPolicy(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	_, _, find := NewNotificationFindTool(pool)
	_, _, get := NewNotificationGetTool(pool)
	_, _, mark := NewNotificationMarkReadTool(pool)
	amberCtx, amber := addMember(t, pool, hh, "Amber")

	bill := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day)
		VALUES ($1, 'Electric', 142.50, 20) RETURNING id`, hh.HouseholdID)
	billN := notify(t, pool, hh.HouseholdID, "", "bill_due", "Electric is due", "bill", bill, 2*time.Hour)
	// A non-money notice the same role may see: a calendar alarm.
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid)
		VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, hh.HouseholdID)
	event := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag)
		VALUES ($1, 'evt-1', 'BEGIN:VCALENDAR', 'e') RETURNING id`, cal)
	alarmN := notify(t, pool, hh.HouseholdID, "", "calendar_alarm", "Dentist in 30 minutes", "calendar_event", event, time.Hour)

	// Owner: both notices, and the bill's attention state is visible.
	d := call[findResult](t, amberCtx, "notification_find", find, map[string]any{"limit": 100}).data(t)
	if d.Total != 2 {
		t.Fatalf("owner total = %d, want 2", d.Total)
	}
	for _, r := range d.Notifications {
		if r.ID == billN && r.Attention == nil {
			t.Error("owner sees the bill_due but got no attention state")
		}
	}

	// Demote to housesitter: the money policy drops the bill everywhere.
	if _, err := pool.Exec(context.Background(),
		`UPDATE memberships SET role = 'housesitter' WHERE household_id = $1 AND user_id = $2`,
		hh.HouseholdID, amber); err != nil {
		t.Fatal(err)
	}

	// find: only the alarm, never the bill.
	d = call[findResult](t, amberCtx, "notification_find", find, map[string]any{"limit": 100}).data(t)
	if d.Total != 1 || d.Returned != 1 || d.Notifications[0].ID != alarmN {
		t.Fatalf("housesitter find = %+v, want only the calendar alarm", d)
	}

	// get: the bill's id is not found (same answer as a row that does not exist).
	if env := call[notificationData](t, amberCtx, "notification_get", get, map[string]any{"notification_id": billN}); env.OK {
		t.Fatalf("housesitter got the bill_due by id: %q", env.Summary)
	}
	// ...but the alarm is theirs.
	if env := call[notificationData](t, amberCtx, "notification_get", get, map[string]any{"notification_id": alarmN}); !env.OK {
		t.Fatalf("housesitter could not get the calendar alarm: %q", env.Summary)
	}

	// mark_read: the bill id is refused, nothing marked.
	env := call[markResult](t, amberCtx, "notification_mark_read", mark, map[string]any{"notification_ids": []string{billN}})
	wantError(t, env, "notification_ids")
	var readAt *time.Time
	if err := pool.QueryRow(context.Background(), `SELECT read_at FROM notifications WHERE id = $1`, billN).Scan(&readAt); err != nil {
		t.Fatal(err)
	}
	if readAt != nil {
		t.Error("housesitter's refused mark read still marked the bill notification read")
	}
	// The alarm is markable.
	if d := call[markResult](t, amberCtx, "notification_mark_read", mark, map[string]any{"notification_ids": []string{alarmN}}).data(t); d.Marked != 1 {
		t.Fatalf("housesitter marked %d calendar alarms, want 1", d.Marked)
	}
}

// TestNotificationGet covers the one row an agent decides to act on: its whole
// body, the call that opens what it is about, and the same needs_you and
// attention as find — plus not-found for another member's or another family's
// row, and for a malformed id.
func TestNotificationGet(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, _, get := NewNotificationGetTool(pool)
	amberCtx, amber := addMember(t, pool, hh, "Amber")

	bill := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day)
		VALUES ($1, 'Electric', 142.50, 20) RETURNING id`, hh.HouseholdID)
	billN := notify(t, pool, hh.HouseholdID, "", "bill_due", "Electric is due", "bill", bill, 2*time.Hour)
	mine := notify(t, pool, hh.HouseholdID, hh.UserID.String(), "calendar_alarm", "Dentist", "", nil, time.Hour)
	amberOnly := notify(t, pool, hh.HouseholdID, amber, "calendar_alarm", "Not yours", "", nil, time.Hour)
	_, other := testutil.SeedHousehold(t, pool)
	foreign := notify(t, pool, other.HouseholdID, "", "bill_due", "Other family", "", nil, time.Hour)

	// The family-wide bill: whole body, its open call, and its bill state.
	env := call[notificationData](t, ctx, "notification_get", get, map[string]any{"notification_id": billN})
	d := env.data(t)
	if d.ID != billN || d.Title != "Electric is due" {
		t.Errorf("get returned %+v", d)
	}
	if d.Body == nil || *d.Body != "body of Electric is due" {
		t.Errorf("body = %v, want the whole body", d.Body)
	}
	if d.AboutType == nil || *d.AboutType != "bill" || d.AboutID == nil || *d.AboutID != bill {
		t.Errorf("about = %v/%v, want bill/%s", d.AboutType, d.AboutID, bill)
	}
	if d.Open == nil || *d.Open != "bill_find id="+bill {
		t.Errorf("open = %v, want bill_find id=%s", d.Open, bill)
	}
	if d.Attention == nil || *d.Attention != "needs_setup" {
		t.Errorf("attention = %v, want needs_setup (no autopay, no account)", d.Attention)
	}
	if !d.NeedsYou {
		t.Error("needs_you = false, want true: nothing has handled this bill")
	}
	if !strings.Contains(env.Summary, "needs a person") {
		t.Errorf("summary = %q, want it to say the bill needs a person", env.Summary)
	}
	if len(env.Next) == 0 || !strings.Contains(strings.Join(env.Next, " "), "bill_find id="+bill) {
		t.Errorf("next = %v, want the open call", env.Next)
	}

	// The caller's own personal row.
	if d := call[notificationData](t, ctx, "notification_get", get, map[string]any{"notification_id": mine}).data(t); d.ID != mine {
		t.Errorf("get on own personal row returned %+v", d)
	}

	// Another member's personal row, another family's row, and an id that is
	// not a UUID: all refused, and the same answer for "not yours" and "not
	// there" so ids cannot be probed.
	for _, id := range []string{amberOnly, foreign} {
		if env := call[notificationData](t, ctx, "notification_get", get, map[string]any{"notification_id": id}); env.OK {
			t.Errorf("get returned %q for %s, want not found", env.Summary, id)
		}
	}
	wantError(t, call[notificationData](t, ctx, "notification_get", get, map[string]any{"notification_id": "not-a-uuid"}), "notification_id")
	wantError(t, call[notificationData](t, ctx, "notification_get", get, map[string]any{}), "notification_id")
	// The "id" alias a small model reaches for.
	if d := call[notificationData](t, ctx, "notification_get", get, map[string]any{"id": billN}).data(t); d.ID != billN {
		t.Errorf("id alias returned %+v", d)
	}
	// Amber's own token cannot read the owner's personal row.
	if env := call[notificationData](t, amberCtx, "notification_get", get, map[string]any{"notification_id": mine}); env.OK {
		t.Error("amber read the owner's personal notification")
	}
}

// TestNotificationFindSinceAndType covers the two filters the watching use case
// needs: "what is new since I last looked" (since) and "only bill reminders"
// (type). "kind" is accepted as an alias for type.
func TestNotificationFindSinceAndType(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, _, find := NewNotificationFindTool(pool)

	old := notify(t, pool, hh.HouseholdID, "", "bill_due", "Old bill", "", nil, 72*time.Hour)
	fresh := notify(t, pool, hh.HouseholdID, "", "bill_due", "Fresh bill", "", nil, time.Hour)
	alarm := notify(t, pool, hh.HouseholdID, "", "calendar_alarm", "Dentist", "", nil, time.Hour)

	// since as an ISO 8601 instant: only the two from the last hour.
	d := call[findResult](t, ctx, "notification_find", find, map[string]any{
		"since": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
	}).data(t)
	if d.Total != 2 || d.Returned != 2 {
		t.Fatalf("since: total=%d returned=%d, want 2/2", d.Total, d.Returned)
	}
	for _, n := range d.Notifications {
		if n.ID == old {
			t.Fatal("since returned the 72-hour-old notification")
		}
	}

	// since as a bare date: midnight in the family timezone, so yesterday's
	// row is excluded and today's are not.
	d = call[findResult](t, ctx, "notification_find", find, map[string]any{
		"since": time.Now().UTC().Format(time.DateOnly),
	}).data(t)
	if d.Total != 2 {
		t.Fatalf("since date: total=%d, want 2", d.Total)
	}

	// type: only the bill reminders.
	d = call[findResult](t, ctx, "notification_find", find, map[string]any{"type": "bill_due"}).data(t)
	if d.Total != 2 {
		t.Fatalf("type: total=%d, want 2", d.Total)
	}
	for _, n := range d.Notifications {
		if n.ID == alarm {
			t.Fatal("type=bill_due returned a calendar alarm")
		}
		if n.ID != old && n.ID != fresh {
			t.Fatalf("unexpected row %s", n.ID)
		}
	}

	// "kind" is the catalog's word for the same filter.
	if d := call[findResult](t, ctx, "notification_find", find, map[string]any{"kind": "calendar_alarm"}).data(t); d.Total != 1 || d.Notifications[0].ID != alarm {
		t.Fatalf("kind alias: %+v", d)
	}

	// A naive datetime is refused with the offset hint, not silently read as UTC.
	wantError(t, call[findResult](t, ctx, "notification_find", find, map[string]any{"since": "2026-11-20T00:00:00"}), "since")
}

// TestWatchLoop is the watching use case end to end, exactly as an agent
// described in the tool description would do it: poll for unread, tell the
// person what needs them, mark those read, poll again and see nothing new.
// The autopay bill is in the feed but never in the "needs you" answer, and
// marking it read does not change the answer for the bills that do.
func TestWatchLoop(t *testing.T) {
	t.Parallel()
	h := newAttentionHarness(t)
	_, _, find := NewNotificationFindTool(h.pool)
	_, _, mark := NewNotificationMarkReadTool(h.pool)

	// 1. Poll. needs_you in the envelope is the count to trust.
	env := call[findResult](t, h.ctx, "notification_find", find, map[string]any{"unread_only": true, "limit": 100})
	page := env.data(t)
	if page.NeedsYou != 3 || page.Returned != 4 {
		t.Fatalf("poll: needs_you=%d returned=%d, want 3/4", page.NeedsYou, page.Returned)
	}

	// 2. Answer the question from the rows only: which titles need the person.
	var need []string
	var handled []string
	for _, n := range page.Notifications {
		if n.NeedsYou {
			need = append(need, n.Title)
		} else {
			handled = append(handled, n.Title)
		}
	}
	if len(need) != 3 {
		t.Fatalf("needs the person: %v, want 3", need)
	}
	if len(handled) != 1 || !strings.Contains(handled[0], "Electric") {
		t.Fatalf("handled: %v, want exactly the autopay bill", handled)
	}

	// 3. Act: mark the three that need the person read, by id (explicit ids).
	var ids []string
	for _, n := range page.Notifications {
		if n.NeedsYou {
			ids = append(ids, n.ID)
		}
	}
	markEnv := call[markResult](t, h.ctx, "notification_mark_read", mark, map[string]any{"notification_ids": ids})
	if d := markEnv.data(t); d.Marked != 3 {
		t.Fatalf("marked %d, want 3", d.Marked)
	}

	// 4. Poll again: the three are gone; the handled bill is still unread
	// (nothing acted on it, so nobody marked it) and still needs nothing.
	after := call[findResult](t, h.ctx, "notification_find", find, map[string]any{"unread_only": true, "limit": 100}).data(t)
	if after.Returned != 1 || after.NeedsYou != 0 {
		t.Fatalf("after: returned=%d needs_you=%d, want 1/0", after.Returned, after.NeedsYou)
	}
	if !strings.Contains(after.Notifications[0].Title, "Electric") {
		t.Fatalf("after: %q, want the autopay bill", after.Notifications[0].Title)
	}
}
