package calendar

// Bills mirrored onto the Bills calendar (#4485): bill_create/update/delete
// keep one all-day repeating event with a VALARM per bill, stored as
// ical_data + ical_raw + rrule that agree, and agenda_get / family_overview
// list bills only in their bills sections.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/billcal"
	"home-os/mcp/internal/bill"
)

// bindBill adapts a bill tool constructor to handlerFunc.
func bindBill(ctor func(*pgxpool.Pool) (string, mcp.Tool, handlerFunc), pool *pgxpool.Pool) handlerFunc {
	_, _, h := ctor(pool)
	return h
}

// billEventRow is a bill's mirror event as stored.
type billEventRow struct {
	objRow
	calendarID, eventType string
}

func billEvents(t *testing.T, pool *pgxpool.Pool, billID string) []billEventRow {
	t.Helper()
	rs, err := pool.Query(context.Background(),
		`SELECT id::text, uid, ical_data, ical_raw, rrule, calendar_id::text, event_type
		   FROM calendar_objects WHERE entity_type = 'bill' AND entity_id = $1`, billID)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []billEventRow
	for rs.Next() {
		var r billEventRow
		if err := rs.Scan(&r.id, &r.uid, &r.data, &r.raw, &r.rrule, &r.calendarID, &r.eventType); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// changes lists a calendar's calendar_changes actions for uid, oldest first.
func changes(t *testing.T, pool *pgxpool.Pool, calendarID, uid string) []string {
	t.Helper()
	rs, err := pool.Query(context.Background(),
		`SELECT action FROM calendar_changes WHERE calendar_id = $1 AND event_uid = $2 ORDER BY id`, calendarID, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var a string
		if err := rs.Scan(&a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func ctag(t *testing.T, pool *pgxpool.Pool, calendarID string) string {
	t.Helper()
	var c string
	if err := pool.QueryRow(context.Background(), `SELECT ctag FROM calendars WHERE id = $1`, calendarID).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

func billID(t *testing.T, text string) string {
	t.Helper()
	var b struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data(t, text), &b); err != nil || b.ID == "" {
		t.Fatalf("decode bill %s: %v", text, err)
	}
	return b.ID
}

func TestBillsMirroredOnBillsCalendar(t *testing.T) {
	env := newRecurrenceEnv(t) // Family calendar first, New York household
	var bills string
	if err := env.pool.QueryRow(context.Background(),
		`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Bills', 'bills', $2) RETURNING id::text`,
		env.household, uuid.NewString()).Scan(&bills); err != nil {
		t.Fatal(err)
	}
	create := bindBill(bill.NewBillCreateTool, env.pool)
	update := bindBill(bill.NewBillUpdateTool, env.pool)
	del := bindBill(bill.NewBillDeleteTool, env.pool)

	before := ctag(t, env.pool, bills)
	electric := billID(t, env.mustCall(t, create, map[string]any{"name": "Electric", "amount": 142.5, "due_day": 31, "category": "Utilities"}))

	var ev billEventRow
	t.Run("a new monthly bill is a repeating all-day event on Bills with a reminder", func(t *testing.T) {
		evs := billEvents(t, env.pool, electric)
		if len(evs) != 1 {
			t.Fatalf("events = %d, want 1", len(evs))
		}
		ev = evs[0]
		if ev.calendarID != bills || ev.eventType != bill.CalendarEventType {
			t.Errorf("calendar %s (want Bills %s), event_type %q", ev.calendarID, bills, ev.eventType)
		}
		obj := consistent(t, ev.objRow) // raw current for the JSON, rrule column = raw RRULE
		m := obj.Master
		if !m.AllDay || m.RRule != "FREQ=MONTHLY;BYMONTHDAY=-1" || m.Summary != "Electric due" {
			t.Errorf("master = all_day %v rrule %q summary %q", m.AllDay, m.RRule, m.Summary)
		}
		if len(m.Alarms) != 1 || m.Alarms[0].Trigger != -63*time.Hour {
			t.Errorf("alarms = %+v", m.Alarms)
		}
		if len(m.Attendees()) != 0 || strings.Contains(strings.ToLower(*ev.raw), "mailto") {
			t.Errorf("bill event has attendees:\n%s", *ev.raw)
		}
		if got := changes(t, env.pool, bills, ev.uid); strings.Join(got, ",") != "create" {
			t.Errorf("calendar_changes = %v", got)
		}
		if ctag(t, env.pool, bills) == before {
			t.Error("Bills ctag not bumped")
		}
	})

	t.Run("the API's and the backfill's writer see the MCP event as current", func(t *testing.T) {
		// The API bill endpoints, legacyfinance and the worker backfill
		// load the bill from the table (billcal.SyncByID) instead of the
		// tool's row: the event they would write is the one bill_create
		// wrote, so syncing again changes nothing (#4716).
		ctx := context.Background()
		tx, err := env.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		ch, err := billcal.SyncByID(ctx, tx, env.household.String(), electric)
		if err != nil {
			t.Fatal(err)
		}
		if ch.Action != billcal.ActionNone || ch.EventID != ev.id {
			t.Errorf("SyncByID after bill_create = %+v, want no change to %s", ch, ev.id)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if n, err := billcal.Backfill(ctx, env.pool); err != nil || n != 0 {
			t.Errorf("Backfill after bill_create created %d (err %v), want 0", n, err)
		}
	})

	t.Run("event_find shows it on the due dates; agenda and conflicts leave it to the bills section", func(t *testing.T) {
		find := bind(t, NewEventFindTool, env.pool)
		evs := eventItems(t, env.mustCall(t, find, map[string]any{"from": "2027-01-01", "to": "2027-03-31", "calendar_id": bills}))
		var dates []string
		for _, e := range evs {
			if e.ID == ev.id {
				dates = append(dates, e.LocalDate)
				if !e.AllDay || !e.Recurring || len(e.ReminderMinutes) != 1 || e.ReminderMinutes[0] != 63*60 {
					t.Errorf("occurrence = %+v", e)
				}
			}
		}
		if strings.Join(dates, ",") != "2027-01-31,2027-02-28,2027-03-31" {
			t.Errorf("due dates = %v", dates)
		}

		agenda := bind(t, NewAgendaGetTool, env.pool)
		var res agendaResult
		if err := json.Unmarshal(data(t, env.mustCall(t, agenda, map[string]any{"from": "2027-02-27", "to": "2027-03-01"})), &res); err != nil {
			t.Fatal(err)
		}
		for _, e := range res.Events {
			if e.ID == ev.id {
				t.Errorf("agenda events list the bill event: %+v", e)
			}
		}
		if len(res.Bills) != 1 || res.Bills[0].BillID != electric || res.Bills[0].DueDate != "2027-02-28" {
			t.Errorf("agenda bills = %+v", res.Bills)
		}

		conflicts, err := findConflicts(env.ctx, env.pool, env.household.String(), env.ny, nil,
			time.Date(2027, 2, 28, 10, 0, 0, 0, env.ny), time.Date(2027, 2, 28, 11, 0, 0, 0, env.ny), "")
		if err != nil {
			t.Fatal(err)
		}
		if len(conflicts) != 0 {
			t.Errorf("conflicts = %+v", conflicts)
		}
	})

	t.Run("bill_update rewrites the event; an unrelated change leaves it alone", func(t *testing.T) {
		env.mustCall(t, update, map[string]any{"bill_id": electric, "name": "Power", "amount": 150.0})
		r := billEvents(t, env.pool, electric)[0]
		obj := consistent(t, r.objRow)
		if r.id != ev.id || obj.Master.Summary != "Power due" || obj.Master.Sequence != 1 ||
			!strings.Contains(obj.Master.Description, "$150.00") {
			t.Errorf("updated master = %+v", obj.Master)
		}
		env.mustCall(t, update, map[string]any{"bill_id": electric, "notes": "acct 12"})
		if got := changes(t, env.pool, bills, ev.uid); strings.Join(got, ",") != "create,update" {
			t.Errorf("calendar_changes = %v", got)
		}

		env.mustCall(t, update, map[string]any{"bill_id": electric, "frequency": "annual", "due_date": "2027-02-28"})
		r = billEvents(t, env.pool, electric)[0]
		obj = consistent(t, r.objRow)
		if obj.Master.RRule != "FREQ=YEARLY" || obj.Master.Start.Format(time.DateOnly) != "2027-02-28" {
			t.Errorf("annual master = %q %s", obj.Master.RRule, obj.Master.Start)
		}
	})

	t.Run("a once bill is a single event; quarterly bills show in the agenda", func(t *testing.T) {
		once := billID(t, env.mustCall(t, create, map[string]any{"name": "Registration", "amount": 80.0, "frequency": "once", "due_date": "2027-05-01"}))
		r := billEvents(t, env.pool, once)
		if len(r) != 1 || r[0].rrule != nil {
			t.Fatalf("once events = %+v", r)
		}
		consistent(t, r[0].objRow)

		q := billID(t, env.mustCall(t, create, map[string]any{"name": "Water", "amount": 90.0, "frequency": "quarterly", "due_date": "2027-01-31"}))
		agenda := bind(t, NewAgendaGetTool, env.pool)
		var res agendaResult
		if err := json.Unmarshal(data(t, env.mustCall(t, agenda, map[string]any{"from": "2027-04-29", "to": "2027-05-01", "areas": []string{"bills"}})), &res); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, b := range res.Bills {
			got = append(got, b.Name+" "+b.DueDate)
		}
		if strings.Join(got, ",") != "Water 2027-04-30,Registration 2027-05-01" {
			t.Errorf("agenda bills = %v (water %s)", got, q)
		}
	})

	t.Run("deleting the bill removes the event", func(t *testing.T) {
		env.mustCall(t, del, map[string]any{"bill_id": electric})
		if n := len(billEvents(t, env.pool, electric)); n != 0 {
			t.Fatalf("events after delete = %d", n)
		}
		var left int
		if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM calendar_objects WHERE id = $1`, ev.id).Scan(&left); err != nil || left != 0 {
			t.Errorf("event row still there (%d, %v)", left, err)
		}
		if got := changes(t, env.pool, bills, ev.uid); strings.Join(got, ",") != "create,update,update,delete" {
			t.Errorf("calendar_changes = %v", got)
		}
	})

	t.Run("a family without a Bills calendar gets no event", func(t *testing.T) {
		if _, err := env.pool.Exec(context.Background(), `DELETE FROM calendars WHERE id = $1`, bills); err != nil {
			t.Fatal(err)
		}
		id := billID(t, env.mustCall(t, create, map[string]any{"name": "Gym", "amount": 30.0, "due_day": 5}))
		if n := len(billEvents(t, env.pool, id)); n != 0 {
			t.Errorf("events = %d, want 0", n)
		}
	})
}
