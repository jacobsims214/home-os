package bill

// Integration tests for the Bills-calendar mirror of API bill writes (#4716).
// The API and the MCP bill tools both call home-os/billcal, so a bill made or
// changed here gets the same event (RRULE, VALARM, ical_data/ical_raw/rrule
// from one master) as one made through bill_create/bill_update. These tests
// check the API wiring: create/update/delete, the no-op update, the missing
// Bills calendar, events people linked to a bill, and legacyfinance.
//
// Requires TEST_DATABASE_URL pointing at a database with all migrations
// applied; skipped otherwise. Run with -p 1.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/legacyfinance"
	"home-os/billcal"
	"home-os/icalx"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func mustID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id
}

func countOf(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func newHousehold(t *testing.T, pool *pgxpool.Pool, withBillsCalendar bool) (household, billsCal uuid.UUID) {
	t.Helper()
	household = mustID(t, pool, `INSERT INTO households (name) VALUES ('Bill mirror '||gen_random_uuid()) RETURNING id`)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, household)
	})
	if withBillsCalendar {
		mustID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, household)
		billsCal = mustID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Bills', 'bills', gen_random_uuid()::text) RETURNING id`, household)
	}
	return household, billsCal
}

type mirror struct {
	ID, CalendarID, UID, ETag, Data, Raw string
	RRule                                *string
}

func mirrorsOf(t *testing.T, pool *pgxpool.Pool, billID uuid.UUID) []mirror {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT id::text, calendar_id::text, uid, etag, ical_data, COALESCE(ical_raw, ''), rrule
		   FROM calendar_objects WHERE entity_type = 'bill' AND entity_id = $1 AND event_type = 'bill'`, billID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []mirror
	for rows.Next() {
		var m mirror
		if err := rows.Scan(&m.ID, &m.CalendarID, &m.UID, &m.ETag, &m.Data, &m.Raw, &m.RRule); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// oneMirror returns the bill's only mirror event and checks it is what
// billcal writes for the stored bill: the JSON and raw from one master
// (so CalDAV serves the raw, with RRULE and VALARM), the rrule column equal
// to the raw's RRULE.
func oneMirror(t *testing.T, pool *pgxpool.Pool, household, billID uuid.UUID, wantSeq int) mirror {
	t.Helper()
	ms := mirrorsOf(t, pool, billID)
	if len(ms) != 1 {
		t.Fatalf("bill %s has %d mirror events, want 1", billID, len(ms))
	}
	m := ms[0]
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	b, ok, err := billcal.LoadBill(ctx, tx, household.String(), billID.String())
	if err != nil || !ok {
		t.Fatalf("LoadBill: %v %v", ok, err)
	}
	master, ok := billcal.Master(b, m.UID, wantSeq)
	if !ok {
		t.Fatal("no master")
	}
	wantData, wantRaw, wantRule, err := billcal.Encode(master)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	_ = json.Unmarshal([]byte(m.Data), &got)
	_ = json.Unmarshal([]byte(wantData), &want)
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Errorf("ical_data = %s\nwant %s", gb, wb)
	}
	if noStamp(m.Raw) != noStamp(wantRaw) {
		t.Errorf("ical_raw =\n%s\nwant\n%s", m.Raw, wantRaw)
	}
	if deref(m.RRule) != deref(wantRule) {
		t.Errorf("rrule = %q, want %q", deref(m.RRule), deref(wantRule))
	}
	obj, err := icalx.Parse([]byte(m.Raw))
	if err != nil {
		t.Fatalf("parse raw: %v", err)
	}
	if obj.Master.RRule != deref(m.RRule) || !obj.Master.AllDay || len(obj.Master.Alarms) != 1 ||
		obj.Master.Alarms[0].Trigger != -63*time.Hour || len(obj.Master.Attendees()) != 0 {
		t.Errorf("raw master: rrule %q allday %v alarms %+v", obj.Master.RRule, obj.Master.AllDay, obj.Master.Alarms)
	}
	return m
}

// noStamp drops DTSTAMP (the time the raw was encoded).
func noStamp(raw string) string {
	var out []string
	for _, l := range strings.Split(raw, "\r\n") {
		if !strings.HasPrefix(l, "DTSTAMP:") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\r\n")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func numeric(t *testing.T, s string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBillCalendarMirror(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewRepo(pool)
	hh, billsCal := newHousehold(t, pool, true)
	changes := func(action string) int {
		return countOf(t, pool, `SELECT count(*) FROM calendar_changes WHERE calendar_id = $1 AND action = $2`, billsCal, action)
	}

	due, cat, notes := 31, "Utilities", "first"
	created, err := repo.Create(ctx, &Bill{HouseholdID: hh, Name: "Electric", Amount: numeric(t, "142.50"),
		DueDay: &due, Category: &cat, Notes: &notes, Frequency: FrequencyMonthly})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := oneMirror(t, pool, hh, created.ID, 0)
	if m.CalendarID != billsCal.String() || deref(m.RRule) != "FREQ=MONTHLY;BYMONTHDAY=-1" ||
		!strings.Contains(m.Raw, "SUMMARY:Electric due") || !strings.Contains(m.Raw, "TRIGGER:-P2DT15H") {
		t.Errorf("created event: calendar %s rrule %v raw\n%s", m.CalendarID, deref(m.RRule), m.Raw)
	}
	if changes("create") != 1 {
		t.Errorf("calendar_changes create = %d", changes("create"))
	}

	// Notes are not on the event: the row is left alone (same etag).
	created.Notes = ptr("second")
	updated, err := repo.Update(ctx, created)
	if err != nil || updated == nil {
		t.Fatalf("Update notes: %v", err)
	}
	if again := oneMirror(t, pool, hh, created.ID, 0); again.ETag != m.ETag || changes("update") != 0 {
		t.Errorf("notes-only update rewrote the event (etag %s -> %s, updates %d)", m.ETag, again.ETag, changes("update"))
	}

	// A schedule change rewrites all three views and bumps SEQUENCE.
	updated.Frequency, updated.DueDay = FrequencyQuarterly, nil
	d := time.Date(2027, time.January, 30, 0, 0, 0, 0, time.UTC)
	updated.DueDate = &d
	updated.Amount = numeric(t, "300")
	if _, err := repo.Update(ctx, updated); err != nil {
		t.Fatalf("Update schedule: %v", err)
	}
	m2 := oneMirror(t, pool, hh, created.ID, 1)
	if m2.ID != m.ID || m2.UID != m.UID || m2.ETag == m.ETag ||
		deref(m2.RRule) != "FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=28,29,30;BYSETPOS=-1" ||
		!strings.Contains(m2.Raw, "DTSTART;VALUE=DATE:20270130") || !strings.Contains(m2.Raw, "SEQUENCE:1") ||
		!strings.Contains(m2.Raw, "Amount: $300.00") {
		t.Errorf("schedule update: %+v\n%s", m2, m2.Raw)
	}
	if changes("update") != 1 {
		t.Errorf("calendar_changes update = %d", changes("update"))
	}

	// A person's own event linked to the bill is not a mirror: kept on
	// update and delete (the 038 trigger only unlinks it).
	userEvent := mustID(t, pool,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, entity_type, entity_id)
		 VALUES ($1, 'user-'||gen_random_uuid(), '{"title":"Call about electric"}', 'user-etag', 'bill', $2) RETURNING id`,
		billsCal, created.ID)

	// No usable schedule (annual without due_date): the event goes.
	updated.Frequency, updated.DueDate = FrequencyAnnual, nil
	if _, err := repo.Update(ctx, updated); err != nil {
		t.Fatalf("Update unusable: %v", err)
	}
	if ms := mirrorsOf(t, pool, created.ID); len(ms) != 0 {
		t.Errorf("unusable schedule kept %d events", len(ms))
	}
	// And comes back when it is usable again (a new event).
	updated.DueDate = &d
	if _, err := repo.Update(ctx, updated); err != nil {
		t.Fatalf("Update usable: %v", err)
	}
	m3 := oneMirror(t, pool, hh, created.ID, 0)
	if deref(m3.RRule) != "FREQ=YEARLY;BYMONTH=1;BYMONTHDAY=28,29,30;BYSETPOS=-1" {
		t.Errorf("annual rrule = %q", deref(m3.RRule))
	}

	if err := repo.Delete(ctx, created.ID, hh); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n := countOf(t, pool, `SELECT count(*) FROM calendar_objects WHERE id = $1`, m3.ID); n != 0 {
		t.Error("mirror event survived the bill's delete")
	}
	if changes("delete") != 2 {
		t.Errorf("calendar_changes delete = %d, want 2", changes("delete"))
	}
	var etag string
	var entity *string
	if err := pool.QueryRow(ctx, `SELECT etag, entity_type FROM calendar_objects WHERE id = $1`, userEvent).Scan(&etag, &entity); err != nil {
		t.Fatalf("user event gone: %v", err)
	}
	if etag != "user-etag" || entity != nil {
		t.Errorf("user event: etag %s entity %v (want untouched, unlinked by 038)", etag, deref(entity))
	}
}

func TestBillCalendarMirror_NoBillsCalendar(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewRepo(pool)
	hh, _ := newHousehold(t, pool, false)
	due := 5
	b, err := repo.Create(ctx, &Bill{HouseholdID: hh, Name: "Water", DueDay: &due, Frequency: FrequencyMonthly})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if n := countOf(t, pool, `SELECT count(*) FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id WHERE c.household_id = $1`, hh); n != 0 {
		t.Errorf("events without a Bills calendar: %d", n)
	}
	if err := repo.Delete(ctx, b.ID, hh); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Deleting a bill that doesn't exist still says not found and rolls the
	// (empty) event delete back.
	if err := repo.Delete(ctx, b.ID, hh); err == nil {
		t.Error("second Delete succeeded")
	}
}

// Bills legacyfinance writes for the property/vehicle forms (#4704) are
// mirrored too.
func TestBillCalendarMirror_LegacyFinance(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	hh, _ := newHousehold(t, pool, true)
	prop := mustID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Lake House') RETURNING id`, hh)
	s := legacyfinance.Subject{Col: "property_id", ID: prop, HouseholdID: hh, Name: "Lake House"}

	apply := func(f legacyfinance.BillFields) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if err := legacyfinance.ApplyBill(ctx, tx, s, legacyfinance.PropertyTax, f); err != nil {
			t.Fatalf("ApplyBill: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	apply(legacyfinance.BillFields{Amount: ptr("2400"), DueMonths: ptr("4")})
	billID := mustID(t, pool, `SELECT id FROM bills WHERE property_id = $1`, prop)
	m := oneMirror(t, pool, hh, billID, 0)
	if deref(m.RRule) != "FREQ=YEARLY" || !strings.Contains(m.Raw, "SUMMARY:Lake House property tax due") ||
		!strings.Contains(m.Raw, "Amount: $2400.00") {
		t.Errorf("legacy create: rrule %q raw\n%s", deref(m.RRule), m.Raw)
	}

	apply(legacyfinance.BillFields{Amount: ptr("2600")})
	if m2 := oneMirror(t, pool, hh, billID, 1); !strings.Contains(m2.Raw, "Amount: $2600.00") || m2.UID != m.UID {
		t.Errorf("legacy update raw\n%s", m2.Raw)
	}

	// A legacy bill with no due month has no schedule, so no event.
	veh := mustID(t, pool, `INSERT INTO vehicles (household_id, make, model) VALUES ($1, 'Subaru', 'Outback') RETURNING id`, hh)
	vs := legacyfinance.Subject{Col: "vehicle_id", ID: veh, HouseholdID: hh, Name: "Outback"}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyfinance.ApplyBill(ctx, tx, vs, legacyfinance.VehicleRegistration, legacyfinance.BillFields{Amount: ptr("90")}); err != nil {
		t.Fatalf("ApplyBill vehicle: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	regID := mustID(t, pool, `SELECT id FROM bills WHERE vehicle_id = $1`, veh)
	if ms := mirrorsOf(t, pool, regID); len(ms) != 0 {
		t.Errorf("unscheduled legacy bill got %d events", len(ms))
	}
}

func ptr(s string) *string { return &s }
