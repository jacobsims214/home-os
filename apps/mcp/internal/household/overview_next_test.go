package household

// Tests for the #4385 sections of family_overview: counts, next_days,
// unread_notifications and recent_changes, plus the latency budget.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/bill"
	"home-os/mcp/internal/notification"
	"home-os/mcp/internal/testutil"
)

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func insertReturningID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("insert %q: %v", sql, err)
	}
	return id
}

// insertEvent adds a timed event starting at start (one hour long).
func insertEvent(t *testing.T, pool *pgxpool.Pool, calendarID, title string, start time.Time) string {
	t.Helper()
	data, _ := json.Marshal(map[string]any{
		"title": title, "start": start.UTC().Format(time.RFC3339),
		"end": start.Add(time.Hour).UTC().Format(time.RFC3339), "all_day": false,
	})
	return insertReturningID(t, pool,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
		 VALUES ($1, $2, $3, 'etag-0', 'event') RETURNING id::text`,
		calendarID, uuid.NewString(), string(data))
}

func decodeOverview(t *testing.T, env envelope) overview {
	t.Helper()
	var ov overview
	if err := json.Unmarshal(env.Data, &ov); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return ov
}

// mentionsOnlyRegisteredTools fails when s names a snake_case word that
// looks like a tool (area_verb) but is not one of the registered tools.
func mentionsOnlyRegisteredTools(t *testing.T, s string) {
	t.Helper()
	registered := []string{
		"agenda_get", "bill_find", "bill_pay", "bill_update", "event_create", "event_find", "event_update", "event_delete",
		"family_update", "maintenance_complete", "maintenance_find", "maintenance_update", "notification_find", "person_find", "search_all",
	}
	toolLike := []string{"_find", "_get", "_create", "_update", "_delete", "_pay", "_complete", "_all"}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !(r == '_' || r >= 'a' && r <= 'z') }) {
		for _, suffix := range toolLike {
			if strings.HasSuffix(w, suffix) && strings.Count(w, "_") >= 1 && !slices.Contains(registered, w) &&
				!strings.HasPrefix(w, "due_") && !strings.HasPrefix(w, "unread_") {
				t.Errorf("%q names unregistered tool %q", s, w)
			}
		}
	}
}

func TestFamilyOverviewNextDays(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, other := testutil.SeedHousehold(t, pool)
	_, tool, h := NewFamilyOverviewTool(pool)
	rs := outputSchema(t, tool)

	// The families are UTC (the seed default), so "today" is the UTC date.
	// Avoid the minute around midnight so the test and the tool agree.
	now := time.Now().UTC()
	if now.Hour() == 23 && now.Minute() == 59 {
		time.Sleep(time.Minute)
		now = time.Now().UTC()
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	day := func(n int) string { return today.AddDate(0, 0, n).Format(time.DateOnly) }

	cal := insertCalendar(t, pool, hh.HouseholdID, "Family", "family")
	otherCal := insertCalendar(t, pool, other.HouseholdID, "Family", "family")

	// 12 events tomorrow (cap is 10), one 10 days out, one in another family.
	var eventIDs []string
	for i := range 12 {
		eventIDs = append(eventIDs, insertEvent(t, pool, cal, fmt.Sprintf("Event %02d", i), today.AddDate(0, 0, 1).Add(time.Duration(i)*time.Hour)))
	}
	insertEvent(t, pool, cal, "Far away", today.AddDate(0, 0, 10).Add(9*time.Hour))
	insertEvent(t, pool, otherCal, "Not ours", today.AddDate(0, 0, 1).Add(9*time.Hour))

	// Bills: due today unpaid (in), due today but paid for today (out),
	// one further out (in only for a long enough window), another family's.
	dueToday := insertReturningID(t, pool,
		`INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Electric', 120.5, $2) RETURNING id::text`,
		hh.HouseholdID, today.Day())
	mustExec(t, pool,
		`INSERT INTO bills (household_id, name, amount, due_day, paid_date, paid_for_due_date) VALUES ($1, 'Water', 40, $2, $3, $3)`,
		hh.HouseholdID, today.Day(), today)
	laterDay := today.AddDate(0, 0, 15).Day()
	laterDue := bill.BillDue(today, laterDay, nil, nil)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, due_day) VALUES ($1, 'Insurance', $2)`, hh.HouseholdID, laterDay)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Theirs', 1, $2)`, other.HouseholdID, today.Day())

	// Maintenance: one overdue, 11 due today (12 open in the window, cap 10),
	// one done today (out), one in 20 days (out), another family's (out).
	overdue := insertReturningID(t, pool,
		`INSERT INTO maintenance_tasks (household_id, name, due_date) VALUES ($1, 'Overdue filter', $2) RETURNING id::text`,
		hh.HouseholdID, today.AddDate(0, 0, -3))
	for i := range 11 {
		mustExec(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date) VALUES ($1, $2, $3)`,
			hh.HouseholdID, fmt.Sprintf("Task %02d", i), today)
	}
	mustExec(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, status) VALUES ($1, 'Done one', $2, 'done')`, hh.HouseholdID, today)
	mustExec(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date) VALUES ($1, 'Later', $2)`, hh.HouseholdID, today.AddDate(0, 0, 20))
	mustExec(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date) VALUES ($1, 'Theirs', $2)`, other.HouseholdID, today)

	// Notifications: two family-wide unread, one read, one another family's.
	for _, read := range []bool{false, false, true} {
		mustExec(t, pool,
			`INSERT INTO notifications (household_id, type, title, body, read_at) VALUES ($1, 'info', 'Hi', 'body', CASE WHEN $2 THEN NOW() END)`,
			hh.HouseholdID, read)
	}
	mustExec(t, pool, `INSERT INTO notifications (household_id, type, title, body) VALUES ($1, 'info', 'Theirs', 'body')`, other.HouseholdID)

	// Counts, and the newest change (activity_log is written by triggers).
	mustExec(t, pool, `INSERT INTO pets (household_id, name, species) VALUES ($1, 'Rex', 'dog')`, hh.HouseholdID)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Newest bill', 9)`, hh.HouseholdID)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Other newest', 9)`, other.HouseholdID)

	t.Run("all sections for the caller's family", func(t *testing.T) {
		env, res := callEnvelope(t, ctx, h, rs, map[string]any{})
		if !env.OK || res.IsError {
			t.Fatalf("want ok, got %+v", env)
		}
		ov := decodeOverview(t, env)
		if len(ov.Warnings) != 0 {
			t.Fatalf("warnings = %v", ov.Warnings)
		}
		if len(ov.People) == 0 || len(ov.Calendars) != 1 || ov.Family.Today != day(0) || len(ov.HowTo) < 5 || len(ov.HowTo) > 8 {
			t.Errorf("basic sections: family=%+v people=%d calendars=%d how_to=%d", ov.Family, len(ov.People), len(ov.Calendars), len(ov.HowTo))
		}

		c := ov.Counts
		if c.Bills != 4 || c.Pets != 1 || c.Lists != 0 || c.Loans != 0 || c.Documents != 0 {
			t.Errorf("counts = %+v, want 4 bills, 1 pet", c)
		}

		nd := ov.NextDays
		if nd.From != day(0) || nd.To != day(6) || nd.Days != 7 {
			t.Errorf("window = %s..%s (%d days), want %s..%s (7)", nd.From, nd.To, nd.Days, day(0), day(6))
		}

		if nd.Events.Total != 12 || len(nd.Events.Items) != maxPreview {
			t.Errorf("events total %d items %d, want 12 / %d", nd.Events.Total, len(nd.Events.Items), maxPreview)
		} else {
			e := nd.Events.Items[0]
			if e.ID != eventIDs[0] || e.Title != "Event 00" || e.LocalDate != day(1) ||
				e.Weekday != today.AddDate(0, 0, 1).Weekday().String() || e.Calendar != "Family" {
				t.Errorf("first event = %+v", e)
			}
			if !strings.Contains(nd.Events.More, "2 more") || !strings.Contains(nd.Events.More, "agenda_get from="+day(0)) {
				t.Errorf("events.more = %q", nd.Events.More)
			}
		}

		wantBills := []string{dueToday}
		if laterDue.DaysUntil <= 6 {
			wantBills = append(wantBills, "Insurance")
		}
		if nd.BillsDue.Total != len(wantBills) || len(nd.BillsDue.Items) == 0 || nd.BillsDue.Items[0].ID != dueToday {
			t.Fatalf("bills_due = %+v, want %v", nd.BillsDue, wantBills)
		}
		b := nd.BillsDue.Items[0]
		if b.Name != "Electric" || b.Amount == nil || *b.Amount != 120.5 || b.LocalDate != day(0) || b.DaysUntil != 0 ||
			b.Weekday != today.Weekday().String() || nd.BillsDue.More != "" {
			t.Errorf("bill = %+v more %q", b, nd.BillsDue.More)
		}

		m := nd.MaintenanceDue
		if m.Total != 12 || len(m.Items) != maxPreview {
			t.Fatalf("maintenance total %d items %d, want 12 / %d", m.Total, len(m.Items), maxPreview)
		}
		if m.Items[0].ID != overdue || !m.Items[0].Overdue || m.Items[0].LocalDate != day(-3) || m.Items[1].Overdue || m.Items[1].Status != "pending" {
			t.Errorf("maintenance items = %+v", m.Items[:2])
		}
		if !strings.Contains(m.More, "2 more: maintenance_find due_within_days=6") {
			t.Errorf("maintenance.more = %q", m.More)
		}

		want, err := notification.UnreadCount(context.Background(), pool, hh.HouseholdID.String(), hh.UserID.String())
		if err != nil {
			t.Fatal(err)
		}
		if ov.UnreadNotifications != 2 || ov.UnreadNotifications != want {
			t.Errorf("unread = %d, want 2 (UnreadCount says %d)", ov.UnreadNotifications, want)
		}

		if len(ov.RecentChanges) != maxRecentChanges || !strings.Contains(ov.RecentChanges[0].Summary, "Newest bill") ||
			ov.RecentChanges[0].EntityType != "bill" || ov.RecentChanges[0].Action != "create" {
			t.Errorf("recent_changes = %+v", ov.RecentChanges)
		}
		for _, rc := range ov.RecentChanges {
			if strings.Contains(rc.Summary, "Other newest") || strings.Contains(rc.Summary, "Theirs") {
				t.Errorf("another family's change leaked: %+v", rc)
			}
			if _, err := time.Parse(time.RFC3339, rc.At); err != nil {
				t.Errorf("at %q: %v", rc.At, err)
			}
		}

		if !strings.Contains(env.Summary, "12 events") || !strings.Contains(env.Summary, "2 unread") {
			t.Errorf("summary = %q", env.Summary)
		}
		all := append(append([]string{nd.Events.More, nd.BillsDue.More, m.More}, env.Next...), ov.HowTo...)
		for _, s := range all {
			mentionsOnlyRegisteredTools(t, s)
		}
		if !slices.ContainsFunc(env.Next, func(s string) bool { return strings.Contains(s, "bill_pay bill_id="+dueToday) }) {
			t.Errorf("next = %v, want a bill_pay hint", env.Next)
		}
	})

	t.Run("days sizes the window", func(t *testing.T) {
		env, _ := callEnvelope(t, ctx, h, rs, map[string]any{"days": 1})
		nd := decodeOverview(t, env).NextDays
		if nd.From != day(0) || nd.To != day(0) || nd.Events.Total != 0 || nd.BillsDue.Total != 1 || nd.MaintenanceDue.Total != 12 {
			t.Errorf("days=1: %+v", nd)
		}
		// Water is paid for today's due date, so it is next due a month on:
		// inside a 31-day window only when that month is short enough.
		wantBills := 2
		if bill.BillDue(today, today.Day(), &today, &today).DaysUntil <= 30 {
			wantBills = 3
		}
		env, _ = callEnvelope(t, ctx, h, rs, map[string]any{"days": 31})
		nd = decodeOverview(t, env).NextDays
		if nd.To != day(30) || nd.Events.Total != 13 || nd.BillsDue.Total != wantBills || nd.MaintenanceDue.Total != 13 {
			t.Errorf("days=31: to=%s events=%d bills=%d maintenance=%d", nd.To, nd.Events.Total, nd.BillsDue.Total, nd.MaintenanceDue.Total)
		}
	})

	t.Run("dates follow the family timezone", func(t *testing.T) {
		// Kiritimati (UTC+14) is always a day ahead of Pago Pago (UTC-11).
		for _, tz := range []string{"Pacific/Kiritimati", "Pacific/Pago_Pago"} {
			mustExec(t, pool, `UPDATE households SET timezone = $2 WHERE id = $1`, hh.HouseholdID, tz)
			loc, _ := time.LoadLocation(tz)
			env, _ := callEnvelope(t, ctx, h, rs, nil)
			ov := decodeOverview(t, env)
			if want := time.Now().In(loc).Format(time.DateOnly); ov.NextDays.From != want || ov.Family.Today != want {
				t.Errorf("%s: from %s today %s, want %s", tz, ov.NextDays.From, ov.Family.Today, want)
			}
			for _, e := range ov.NextDays.Events.Items {
				st, _ := time.Parse(time.RFC3339, e.Start)
				if e.LocalDate != st.In(loc).Format(time.DateOnly) {
					t.Errorf("%s: event %+v local_date off", tz, e)
				}
			}
		}
		mustExec(t, pool, `UPDATE households SET timezone = 'UTC' WHERE id = $1`, hh.HouseholdID)
	})
}

// TestFamilyOverviewLatency seeds a busy family and checks family_overview
// answers in under 100 ms. It asserts on the fastest of 15 calls after a
// warm-up: that is the tool's own cost, while the median also measures CPU
// contention when go test ./... runs every package (and its Postgres
// container) at once. Isolated runs show a median around 10 ms.
func TestFamilyOverviewLatency(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, tool, h := NewFamilyOverviewTool(pool)
	rs := outputSchema(t, tool)
	cal := insertCalendar(t, pool, hh.HouseholdID, "Family", "family")
	insertCalendar(t, pool, hh.HouseholdID, "Kids", "family")
	for range 3 {
		addMember(t, pool, hh, "family_member")
	}

	// A busy family: 500 events over the next ~2 months, 200 bills, 300
	// maintenance tasks, 300 notes, 200 notifications.
	mustExec(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
		SELECT $1, gen_random_uuid()::text,
		       json_build_object('title', 'Event ' || g,
		           'start', to_char((now() + g * interval '3 hours') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		           'end',   to_char((now() + g * interval '3 hours' + interval '1 hour') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		           'all_day', false)::text,
		       'etag-0', 'event'
		  FROM generate_series(1, 500) g`, cal)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, due_day)
		SELECT $1, 'Bill ' || g, g, 1 + g % 28 FROM generate_series(1, 200) g`, hh.HouseholdID)
	mustExec(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date)
		SELECT $1, 'Task ' || g, current_date + (g % 60 - 10) FROM generate_series(1, 300) g`, hh.HouseholdID)
	mustExec(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, title, body)
		SELECT $1, 'household', $1, 'Note ' || g, 'body' FROM generate_series(1, 300) g`, hh.HouseholdID)
	mustExec(t, pool, `INSERT INTO notifications (household_id, type, title, body)
		SELECT $1, 'info', 'N ' || g, 'body' FROM generate_series(1, 200) g`, hh.HouseholdID)
	mustExec(t, pool, `ANALYZE`)

	callEnvelope(t, ctx, h, rs, nil) // warm-up: connections, plans
	var took []time.Duration
	for range 15 {
		start := time.Now()
		env, _ := callEnvelope(t, ctx, h, rs, nil)
		took = append(took, time.Since(start))
		if !env.OK {
			t.Fatalf("not ok: %+v", env)
		}
		if ov := decodeOverview(t, env); len(ov.Warnings) != 0 || ov.Counts.Bills != 200 || ov.NextDays.Events.Total == 0 {
			t.Fatalf("unexpected overview: warnings=%v counts=%+v", ov.Warnings, ov.Counts)
		}
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	t.Logf("family_overview latency: min %v, median %v, max %v", took[0], took[len(took)/2], took[len(took)-1])
	if took[0] >= 100*time.Millisecond {
		t.Errorf("family_overview took at least %v on every call, want < 100ms", took[0])
	}
}
