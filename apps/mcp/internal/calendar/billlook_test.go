package calendar

// #4943: the guard against a calendar entry being read as bill state.
//
// The war game's worst failure was a briefing that said a bill Jacob had paid
// that morning was "still unpaid", because the Bills calendar still held the
// round-1 entry "Electric bill due — $180" beside the bill's own record
// (discoveries/war-game-r1-jacob.md G2 + G5). These tests cover the two halves
// of the fix: the title rule migration 073's reconcile uses (and this package
// applies to what it could not supersede), and an agenda shaped from a paid
// bill plus a stale matching legacy entry, which must not claim the bill is
// due.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/testutil"
)

// TestBillNameMatchesMigration073 checks the Go title rule against the SQL
// twin migration 073 applies (calendar_bill_name, calendar_bill_amounts,
// calendar_title_reads_as_bill). If the two ever disagree, a bill the
// migration would supersede is left live, or worse, one it would leave is
// retired — so this runs the same titles through both.
func TestBillNameMatchesMigration073(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()

	titles := []string{
		"Electric bill due — $180",
		"Internet bill due — $75",
		"Mortgage due — $2,340",
		"Electric due",
		"Electric bill",
		"ELECTRIC — $180.00",
		"Mortgage payment — $2,340.00",
		"Water bill due",
		"Car payment",
		"Bill due — $80",
		"Library books due",
		"Dentist appointment",
		"Soccer practice",
		"Netflix subscription — $15.49",
		"Rent 1,850 USD",
		"",
		"   ",
		"$0.99",
	}
	for _, title := range titles {
		t.Run(title, func(t *testing.T) {
			var sqlName string
			var sqlReads bool
			var sqlAmounts []float64
			if err := pool.QueryRow(ctx,
				`SELECT calendar_bill_name($1), calendar_title_reads_as_bill($1), calendar_bill_amounts($1)`,
				title).Scan(&sqlName, &sqlReads, &sqlAmounts); err != nil {
				t.Fatalf("ask the SQL rule: %v", err)
			}
			if got := billName(title); got != sqlName {
				t.Errorf("billName(%q) = %q, migration 073 says %q", title, got, sqlName)
			}
			if got := titleReadsAsBill(title); got != sqlReads {
				t.Errorf("titleReadsAsBill(%q) = %v, migration 073 says %v", title, got, sqlReads)
			}
			got := billAmounts(title)
			if len(got) != len(sqlAmounts) {
				t.Fatalf("billAmounts(%q) = %v, migration 073 says %v", title, got, sqlAmounts)
			}
			for i := range got {
				if !sameMoney(got[i], sqlAmounts[i]) {
					t.Errorf("billAmounts(%q)[%d] = %v, migration 073 says %v", title, i, got[i], sqlAmounts[i])
				}
			}
		})
	}

	// The three war-game titles, explicitly: each names its bill.
	for title, want := range map[string]string{
		"Electric bill due — $180": "electric",
		"Internet bill due — $75":  "internet",
		"Mortgage due — $2,340":    "mortgage",
	} {
		if got := billName(title); got != want {
			t.Errorf("billName(%q) = %q, want %q", title, got, want)
		}
	}
}

// TestTitleReadsAsBill pins the narrowness of the rule: an appointment that
// merely says "due" is not a bill, and a bill word is enough even without an
// amount.
func TestTitleReadsAsBill(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{"Electric bill due — $180", true},
		{"Library books due", false},
		{"Library books due — $4 fine", true}, // states money: worth the guard
		{"Dentist appointment", false},
		{"Soccer practice", false},
		{"Water payment", true},
		{"Autopay rent", true},
		{"Invoice 42", true},
		{"Mortgage due", false}, // "due" alone is not a bill
		{"", false},
	} {
		if got := titleReadsAsBill(tc.title); got != tc.want {
			t.Errorf("titleReadsAsBill(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

// TestBillLikeReason pins what a model is told: never an amount to repeat, and
// the bill to look up instead when there is one.
func TestBillLikeReason(t *testing.T) {
	elec := AgendaBill{BillID: "bill-elec", Name: "Electric", Amount: 180}
	water1 := AgendaBill{BillID: "bill-w1", Name: "Water", Amount: 60}
	water2 := AgendaBill{BillID: "bill-w2", Name: "Water", Amount: 90}
	byName := map[string][]AgendaBill{
		"electric": {elec},
		"water":    {water1, water2},
	}

	// The same amount: the bill is the record, the entry is not its state.
	id, reason := billLikeReason("Electric bill due — $180", byName)
	if id == nil || *id != "bill-elec" {
		t.Errorf("matching entry: bill_id = %v, want bill-elec", id)
	}
	if !strings.Contains(reason, "never report it as due or unpaid") {
		t.Errorf("matching entry reason does not say not to trust it: %q", reason)
	}

	// A disagreeing amount: neither is declared right, and bill_find is named.
	id, reason = billLikeReason("Electric bill due — $210", byName)
	if id == nil || *id != "bill-elec" {
		t.Errorf("disagreeing entry: bill_id = %v, want bill-elec", id)
	}
	if !strings.Contains(reason, "$210.00") || !strings.Contains(reason, "$180.00") {
		t.Errorf("disagreeing entry reason does not name both amounts: %q", reason)
	}

	// Two bills of the name: no guess, and no amount to repeat.
	id, reason = billLikeReason("Water bill due — $60", byName)
	if id != nil {
		t.Errorf("ambiguous entry got bill_id %v, want none", id)
	}
	if !strings.Contains(reason, "More than one bill") {
		t.Errorf("ambiguous entry reason = %q", reason)
	}

	// No bill at all: the entry is the only record, and the user decides.
	id, reason = billLikeReason("Sewer bill due — $40", byName)
	if id != nil {
		t.Errorf("unlinked entry got bill_id %v, want none", id)
	}
	if !strings.Contains(reason, "no bill by this name") || !strings.Contains(reason, "bill_create") {
		t.Errorf("unlinked entry reason = %q", reason)
	}

	// No name to match.
	id, _ = billLikeReason("Bill due — $80", byName)
	if id != nil {
		t.Errorf("unnamed entry got bill_id %v, want none", id)
	}
}

// billLookEnv seeds the war-game household: a Bills calendar with a stale
// round-1 entry, the bills module's records, and a Family calendar with an
// ordinary event so the events section is not empty.
type billLookEnv struct {
	pool               *pgxpool.Pool
	ctx                context.Context
	hh                 testutil.Household
	billsCal, famCal   string
	electric, internet string
}

// seedBillLookEnv writes the household the failure happened in. electricPaid
// pays the Electric bill for its own due date, the way bill_pay does.
//
// legacyAfterBills decides the order the two sources of truth are written in:
//
//   - false (the round-1 history): the calendar entries came first and the
//     bills module's records came later. Migration 073's trigger absorbs each
//     entry as its bill is inserted, so afterwards only one record per bill is
//     live — the fix working.
//   - true (the state a briefing must still survive): the bills exist and the
//     stale entry arrives afterwards, which is what a phone syncing an old
//     entry back, a raw SQL import or a pre-073 row does. Nothing absorbed it,
//     so the entry is live and the guard has to report it rather than let a
//     briefing read it as bill state.
func seedBillLookEnv(t *testing.T, electricPaid, legacyAfterBills bool) billLookEnv {
	t.Helper()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	env := billLookEnv{pool: pool, ctx: ctx, hh: hh}

	// The household is UTC here (SeedHousehold leaves timezone unset, and
	// agenda_get falls back to UTC), so today's dates are UTC dates.
	now := time.Now().UTC()
	today := now.Format(time.DateOnly)

	for _, c := range []struct {
		name, typ string
		dst       *string
	}{
		{"Bills", "bills", &env.billsCal},
		{"Family", "family", &env.famCal},
	} {
		if err := pool.QueryRow(ctx,
			`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			hh.HouseholdID, c.name, c.typ, uuid.NewString()).Scan(c.dst); err != nil {
			t.Fatal(err)
		}
	}

	// The round-1 entries: what R1 left behind, with the amount in the title.
	legacy := func(title, uid string, day time.Time) string {
		t.Helper()
		data := fmt.Sprintf(`{"title":%q,"description":"","start":"%sT00:00:00Z","end":"%sT00:00:00Z","all_day":true,"event_type":"event"}`,
			title, day.Format(time.DateOnly), day.AddDate(0, 0, 1).Format(time.DateOnly))
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
			 VALUES ($1, $2, $3, 'e', 'event') RETURNING id::text`,
			env.billsCal, uid, data).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	writeLegacy := func() {
		env.electric = legacy("Electric bill due — $180", "legacy-electric", now)
		env.internet = legacy("Internet bill due — $75", "legacy-internet", now.AddDate(0, 0, 1))
	}
	writeBills := func() {
		// The bills module's records, exactly as the war game had them.
		var paidDate, paidFor any
		if electricPaid {
			paidDate, paidFor = today, today
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO bills (household_id, name, amount, due_day, paid_date, paid_for_due_date)
			 VALUES ($1, 'Electric', 180, $2, $3::date, $4::date)`,
			hh.HouseholdID, now.Day(), paidDate, paidFor); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Internet', 75, $2)`,
			hh.HouseholdID, now.AddDate(0, 0, 1).Day()); err != nil {
			t.Fatal(err)
		}
	}

	if legacyAfterBills {
		writeBills()
		writeLegacy()
	} else {
		writeLegacy()
		writeBills()
	}

	// An ordinary appointment on the Family calendar: it must survive as an
	// event, so the guard is not just emptying the calendar.
	appt := fmt.Sprintf(`{"title":"Soccer practice","description":"","start":"%sT18:00:00Z","end":"%sT19:00:00Z","all_day":false}`,
		today, today)
	if _, err := pool.Exec(ctx,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
		 VALUES ($1, $2, $3, 'e', 'event')`,
		env.famCal, uuid.NewString(), appt); err != nil {
		t.Fatal(err)
	}
	return env
}

// TestAgendaPaidBillWithStaleLegacyEvent is the failure from the war game:
// a bill paid today, and a legacy calendar entry still saying it is due. The
// agenda must not claim the bill is due from the entry, and must say what the
// entry is.
func TestAgendaPaidBillWithStaleLegacyEvent(t *testing.T) {
	env := seedBillLookEnv(t, true, true) // a stale entry still live beside the paid bill
	_, h := NewAgendaGetTool(env.pool)
	text := callAgenda(t, env, h)

	// 1. The bills section does not claim Electric is due: the bill is paid
	// for today's due date, so bill.BillDueFor does not put it in the range.
	res := decodeAgenda(t, text)
	for _, b := range res.Bills {
		if strings.EqualFold(b.Name, "Electric") {
			t.Errorf("agenda says Electric is due after it was paid today: %+v", b)
		}
	}
	// 2. The stale entry is not an event either.
	for _, e := range res.Events {
		if strings.Contains(e.Title, "Electric") {
			t.Errorf("the stale legacy entry is listed as an event: %q", e.Title)
		}
	}
	// 3. It IS reported as a bill-like entry, with the reason.
	found := false
	for _, e := range res.BillLikeEntries {
		if strings.Contains(e.Title, "Electric") {
			found = true
			if !strings.Contains(e.Reason, "never report it as due or unpaid") {
				t.Errorf("bill-like entry reason = %q", e.Reason)
			}
			if e.BillID == nil {
				t.Errorf("bill-like entry names no bill: %+v", e)
			}
		}
	}
	if !found {
		t.Errorf("the stale entry is not reported at all:\n%s", text)
	}
	// 4. The summary — the line a small model reads first — names no bill the
	// module says is paid, and the bills section holds the one bill that IS
	// due (Internet), from its record.
	var envelope struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if strings.Contains(envelope.Summary, "Electric") {
		t.Errorf("summary claims the paid bill: %q", envelope.Summary)
	}
	if len(res.Bills) != 1 || res.Bills[0].Name != "Internet" {
		t.Errorf("bills section = %+v, want just Internet from the module", res.Bills)
	}
}

// TestAgendaUnpaidBillDoesNotUseLegacyAmount: the other half. A bill that IS
// due is reported from the bill record (its amount and its id), and the legacy
// entry still does not stand in for it.
func TestAgendaUnpaidBillDoesNotUseLegacyAmount(t *testing.T) {
	env := seedBillLookEnv(t, false, true)
	_, h := NewAgendaGetTool(env.pool)
	text := callAgenda(t, env, h)
	res := decodeAgenda(t, text)

	// Electric is due today, and the agenda says so from the bill.
	var electric *AgendaBill
	for i, b := range res.Bills {
		if strings.EqualFold(b.Name, "Electric") {
			electric = &res.Bills[i]
		}
	}
	if electric == nil {
		t.Fatalf("Electric is due today and the agenda does not list it:\n%s", text)
	}
	if electric.Amount != 180 {
		t.Errorf("agenda bill amount = %v, want the bill's 180", electric.Amount)
	}
	if electric.BillID == "" {
		t.Error("the agenda bill carries no bill_id, so it cannot be the module's record")
	}
	// The legacy entry is still not an event, and is still reported as an
	// entry rather than as a second, duplicate bill.
	for _, e := range res.Events {
		if strings.Contains(e.Title, "Electric") {
			t.Errorf("the legacy entry is listed as an event: %q", e.Title)
		}
	}
	due := 0
	for _, b := range res.Bills {
		if strings.EqualFold(b.Name, "Electric") {
			due++
		}
	}
	if due != 1 {
		t.Errorf("Electric appears %d times in the bills section, want once (from the module)", due)
	}
}

// TestAgendaAfterMigration073Supersedes: once 073 has retired the matching
// entry, the agenda shows no bill-like entry for it at all — one record per
// bill.
func TestAgendaAfterMigration073Supersedes(t *testing.T) {
	env := seedBillLookEnv(t, true, true)
	ctx := context.Background()
	// The reconcile 073 runs: one call per bill, the same SQL the trigger and
	// the migration use.
	var n int
	for _, name := range []string{"Electric", "Internet"} {
		var id uuid.UUID
		if err := env.pool.QueryRow(ctx, `SELECT id FROM bills WHERE household_id = $1 AND name = $2`,
			env.hh.HouseholdID, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if err := env.pool.QueryRow(ctx, `SELECT calendar_absorb_bill_events($1)`, id).Scan(&n); err != nil {
			t.Fatalf("absorb %s: %v", name, err)
		}
		if n != 1 {
			t.Errorf("absorb %s retired %d entries, want 1", name, n)
		}
	}
	// Both entries are retired in place, not deleted: the rows and their text
	// are still there.
	for _, c := range []struct {
		name, id string
	}{
		{"Electric", env.electric},
		{"Internet", env.internet},
	} {
		var supersededAt *time.Time
		var title string
		if err := env.pool.QueryRow(ctx,
			`SELECT superseded_at, ical_data::jsonb->>'title' FROM calendar_objects WHERE id = $1`,
			c.id).Scan(&supersededAt, &title); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if supersededAt == nil {
			t.Errorf("%s entry was not retired", c.name)
		}
		if !strings.Contains(title, c.name) {
			t.Errorf("%s entry lost its text: %q", c.name, title)
		}
	}

	_, h := NewAgendaGetTool(env.pool)
	res := decodeAgenda(t, callAgenda(t, env, h))
	if len(res.BillLikeEntries) != 0 {
		t.Errorf("agenda still reports %d bill-like entries after the reconcile: %+v", len(res.BillLikeEntries), res.BillLikeEntries)
	}
	for _, b := range res.Bills {
		if strings.EqualFold(b.Name, "Electric") {
			t.Errorf("a paid bill is still due in the agenda: %+v", b)
		}
	}
	// The ordinary appointment is untouched by all of this.
	found := false
	for _, e := range res.Events {
		if e.Title == "Soccer practice" {
			found = true
		}
	}
	if !found {
		t.Error("the reconcile took an ordinary event with it")
	}
}

// TestAgendaNewBillAbsorbsItsStaleEntry is the guard end to end: a bill
// inserted by any writer (here: plain SQL, which stands for the API,
// legacyfinance or a seed) retires the stale entry on insert, so the two
// sources of truth cannot come back.
func TestAgendaNewBillAbsorbsItsStaleEntry(t *testing.T) {
	// The round-1 order: the entry exists before the bill does, so the bills
	// INSERT trigger is what absorbs it — the reconcile is not run here.
	env := seedBillLookEnv(t, true, false)
	ctx := context.Background()

	data := `{"title":"Sewer bill due — $40","description":"keep my note","start":"2026-11-28T00:00:00Z","end":"2026-11-29T00:00:00Z","all_day":true}`
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type) VALUES ($1, 'legacy-sewer', $2, 'e', 'event')`,
		env.billsCal, data); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err := env.pool.QueryRow(ctx,
		`INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Sewer', 40, 28) RETURNING id`,
		env.hh.HouseholdID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// A second bill of the same name: now the name is ambiguous, so an entry
	// naming Sewer written after it must survive untouched.
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Sewer', 55, 28)`,
		env.hh.HouseholdID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
		 VALUES ($1, 'legacy-sewer-2', $2, 'e', 'event')`,
		env.billsCal, strings.Replace(data, "keep my note", "second", 1)); err != nil {
		t.Fatal(err)
	}

	var superseded []string
	rows, err := env.pool.Query(ctx,
		`SELECT uid FROM calendar_objects WHERE superseded_at IS NOT NULL ORDER BY uid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			t.Fatal(err)
		}
		superseded = append(superseded, uid)
	}
	// 'legacy-sewer' was absorbed when the FIRST Sewer bill was inserted; the
	// second bill made the name ambiguous, so 'legacy-sewer-2' survives.
	if !containsStr(superseded, "legacy-sewer") {
		t.Errorf("a new bill did not absorb its stale entry: retired %v", superseded)
	}
	if containsStr(superseded, "legacy-sewer-2") {
		t.Errorf("an entry was retired against an ambiguous name: %v", superseded)
	}
	// The retired entry keeps its body, so the family's note survives.
	var body string
	if err := env.pool.QueryRow(ctx,
		`SELECT ical_data::jsonb->>'description' FROM calendar_objects WHERE uid = 'legacy-sewer'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "keep my note" {
		t.Errorf("the retired entry lost its note: %q", body)
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// callAgenda runs agenda_get over today and the next two days as the family
// owner, with every area, and returns the result's text.
func callAgenda(t *testing.T, env billLookEnv, h handlerFunc) string {
	t.Helper()
	today := time.Now().UTC()
	text, isErr := callTool(t, h, env.ctx, map[string]any{
		"from": today.Format(time.DateOnly),
		"to":   today.AddDate(0, 0, 2).Format(time.DateOnly),
	})
	if isErr {
		t.Fatalf("agenda_get: %s", text)
	}
	return text
}

// decodeAgenda decodes an agenda_get result's data.
func decodeAgenda(t *testing.T, text string) agendaResult {
	t.Helper()
	var res agendaResult
	if err := json.Unmarshal(data(t, text), &res); err != nil {
		t.Fatalf("decode agenda: %v\n%s", err, text)
	}
	return res
}

// TestBillLikeRowsSQLMatchesReconcile is #5140's core regression: the rows the
// guard reads (billLikeRowsSQL, this package) and the rows the reconcile
// supersedes (calendar_absorb_bill_events, migration 074) must be the SAME set.
//
// The live failure was exactly a disagreement here: 073's reconcile required
// `event_type IS DISTINCT FROM 'bill'`, so a legacy entry typed event_type
// 'bill' with entity_type NULL was reported by the guard and never retired —
// the Bills calendar kept one of them beside the module's mirror forever. The
// fixture writes the real round-1 shape, and the bills' own mirrors, and
// asserts both sides see the legacy rows and neither touches a mirror.
func TestBillLikeRowsSQLMatchesReconcile(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)

	var cal string
	if err := pool.QueryRow(ctx,
		`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Bills', 'bills', $2) RETURNING id::text`,
		hh.HouseholdID, uuid.NewString()).Scan(&cal); err != nil {
		t.Fatal(err)
	}

	// insert writes one calendar object with the columns the predicate reads:
	// event_type is the column, entity_type/entity_id are the mirror marks.
	insert := func(uid, title, eventType string, entity *string) string {
		t.Helper()
		data := fmt.Sprintf(`{"title":%q,"description":"","start":"2026-11-18T00:00:00Z","end":"2026-11-19T00:00:00Z","all_day":true}`, title)
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
			 VALUES ($1, $2, $3, 'e', $4, CASE WHEN $5::text IS NULL THEN NULL ELSE 'bill' END, $5::uuid) RETURNING id::text`,
			cal, uid, data, eventType, entity).Scan(&id); err != nil {
			t.Fatalf("insert %s: %v", uid, err)
		}
		return id
	}

	// The bill, and its mirror as billcal writes it (event_type 'bill',
	// entity_type 'bill').
	var billID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Electric', 180, 18) RETURNING id::text`,
		hh.HouseholdID).Scan(&billID); err != nil {
		t.Fatal(err)
	}
	mirror := insert("mirror-electric", "Electric due", "bill", &billID)
	// The live round-1 shape: typed 'bill' but mirroring nothing. This is the
	// row 073 could not see.
	legacyBillTyped := insert("legacy-electric", "Electric bill due — $180", "bill", nil)
	// An ordinary-looking legacy entry, the shape #5140's fixture used to
	// write. It must be superseded too — the old predicate accepted it, and
	// changing the predicate must not lose what already worked.
	legacyEventTyped := insert("legacy-electric-2", "Electric bill — $180", "event", nil)
	// Unrelated events on a Bills calendar are still not bill-like by name, but
	// the Bills calendar makes them candidates; the name rule keeps them out.
	unrelated := insert("unrelated", "Dentist", "event", nil)

	// What the guard reads.
	rows, err := pool.Query(ctx, billLikeRowsSQL, hh.HouseholdID)
	if err != nil {
		t.Fatalf("billLikeRowsSQL: %v", err)
	}
	guard := map[string]bool{}
	for rows.Next() {
		var id, calName, calType, data string
		if err := rows.Scan(&id, &calName, &calType, &data); err != nil {
			t.Fatal(err)
		}
		guard[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// The guard's row set: every live entity_type-NULL entry, mirror excluded.
	for _, c := range []struct {
		name string
		id   string
		want bool
	}{
		{"the round-1 entry typed event_type='bill'", legacyBillTyped, true},
		{"the round-1 entry typed event_type='event'", legacyEventTyped, true},
		{"an unrelated event on the Bills calendar", unrelated, true},
		{"the bill's mirror (entity_type='bill')", mirror, false},
	} {
		if guard[c.id] != c.want {
			t.Errorf("%s: guard reads it = %v, want %v", c.name, guard[c.id], c.want)
		}
	}

	// What the reconcile supersedes: one call per bill, as the migration and
	// the INSERT trigger do.
	var n int
	if err := pool.QueryRow(ctx, `SELECT calendar_absorb_bill_events($1)`, billID).Scan(&n); err != nil {
		t.Fatalf("absorb: %v", err)
	}
	if n != 2 {
		t.Errorf("the reconcile superseded %d entries, want 2 (both legacy Electric entries)", n)
	}
	superseded := func(id string) bool {
		t.Helper()
		var ok bool
		if err := pool.QueryRow(ctx,
			`SELECT superseded_at IS NOT NULL FROM calendar_objects WHERE id = $1`, id).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	for _, c := range []struct {
		name string
		id   string
		want bool
	}{
		{"the round-1 entry typed event_type='bill' (#5140's failure)", legacyBillTyped, true},
		{"the round-1 entry typed event_type='event'", legacyEventTyped, true},
		{"the bill's mirror (entity_type='bill')", mirror, false},
		{"an unrelated event on the Bills calendar", unrelated, false},
	} {
		if got := superseded(c.id); got != c.want {
			t.Errorf("%s: superseded = %v, want %v", c.name, got, c.want)
		}
	}

	// And the two sides now agree on the rows that are still to be reported:
	// what the guard read before the reconcile, minus what it superseded, is
	// exactly what it reads after it.
	rows, err = pool.Query(ctx, billLikeRowsSQL, hh.HouseholdID)
	if err != nil {
		t.Fatalf("billLikeRowsSQL after: %v", err)
	}
	after := map[string]bool{}
	for rows.Next() {
		var id, calName, calType, data string
		if err := rows.Scan(&id, &calName, &calType, &data); err != nil {
			t.Fatal(err)
		}
		after[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for id := range guard {
		wantLive := id != legacyBillTyped && id != legacyEventTyped
		if after[id] != wantLive {
			t.Errorf("after the reconcile, guard reads %s = %v, want %v", id, after[id], wantLive)
		}
	}
}
