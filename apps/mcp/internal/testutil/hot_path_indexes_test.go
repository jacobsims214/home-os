package testutil

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Migration 035 (#4387): calendar_objects.starts_at / ends_at, derived from
// ical_data's start/end by a trigger, plus household and trigram indexes.

// eventTimes reads one event's stored instants (nil when NULL).
func eventTimes(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) (start, end *time.Time) {
	t.Helper()
	mustScan(t, pool.QueryRow(context.Background(),
		`SELECT starts_at, ends_at FROM calendar_objects WHERE id = $1`, id), &start, &end)
	return start, end
}

func wantInstant(t *testing.T, what string, got *time.Time, want string) {
	t.Helper()
	if want == "" {
		if got != nil {
			t.Errorf("%s = %v, want NULL", what, got)
		}
		return
	}
	w, err := time.Parse(time.RFC3339, want)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.Equal(w) {
		t.Errorf("%s = %v, want %s", what, got, want)
	}
}

func TestHotPathIndexes035_EventTimesTrigger(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, f.household)

	// Session TimeZone must not matter: offset-less values read as UTC.
	func() {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		if _, err := conn.Exec(ctx, `SET timezone = 'America/New_York'`); err != nil {
			t.Fatal(err)
		}
		defer conn.Exec(ctx, `RESET timezone`) //nolint:errcheck
		var got time.Time
		mustScan(t, conn.QueryRow(ctx, `SELECT calendar_event_instant('{"start":"2026-11-18T15:00:00"}', 'start')`), &got)
		wantInstant(t, "offset-less start under America/New_York", &got, "2026-11-18T15:00:00Z")
	}()

	tests := []struct {
		name, ical, start, end string
	}{
		{"timed UTC", `{"title":"a","start":"2026-11-18T15:00:00Z","end":"2026-11-18T16:00:00Z"}`, "2026-11-18T15:00:00Z", "2026-11-18T16:00:00Z"},
		{"timed with offset", `{"start":"2026-11-18T10:00:00-05:00","end":"2026-11-18T11:30:00-05:00"}`, "2026-11-18T15:00:00Z", "2026-11-18T16:30:00Z"},
		{"fractional seconds", `{"start":"2026-11-18T15:00:00.5Z","end":"2026-11-18T16:00:00Z"}`, "2026-11-18T15:00:00.5Z", "2026-11-18T16:00:00Z"},
		{"all-day raw UTC midnights", `{"all_day":true,"start":"2026-11-18T00:00:00Z","end":"2026-11-19T00:00:00Z"}`, "2026-11-18T00:00:00Z", "2026-11-19T00:00:00Z"},
		{"offset-less is UTC", `{"start":"2026-11-18T15:00:00","end":"2026-11-18 16:00:00"}`, "2026-11-18T15:00:00Z", "2026-11-18T16:00:00Z"},
		{"missing end", `{"start":"2026-11-18T15:00:00Z"}`, "2026-11-18T15:00:00Z", ""},
		{"empty strings", `{"start":"","end":"  "}`, "", ""},
		{"not a timestamp", `{"start":"next tuesday","end":42}`, "", ""},
		{"not JSON (iCalendar text)", "BEGIN:VCALENDAR\r\nSUMMARY:x\r\nEND:VCALENDAR", "", ""},
		{"JSON array", `["2026-11-18T15:00:00Z"]`, "", ""},
		{"empty object", `{}`, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, $2, $3, 'e') RETURNING id`,
				cal, uuid.NewString(), tt.ical)
			s, e := eventTimes(t, pool, id)
			wantInstant(t, "starts_at", s, tt.start)
			wantInstant(t, "ends_at", e, tt.end)
		})
	}

	id := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, 'move-me', $2, 'e') RETURNING id`,
		cal, `{"start":"2026-11-18T15:00:00Z","end":"2026-11-18T16:00:00Z"}`)

	// Changing ical_data re-derives both columns (the CalDAV PUT and API path).
	mustExec(t, pool, `UPDATE calendar_objects SET ical_data = $2, etag = 'e2' WHERE id = $1`, id,
		`{"start":"2026-12-01T09:00:00Z","end":"2026-12-01T10:00:00Z"}`)
	s, e := eventTimes(t, pool, id)
	wantInstant(t, "after move: starts_at", s, "2026-12-01T09:00:00Z")
	wantInstant(t, "after move: ends_at", e, "2026-12-01T10:00:00Z")

	// The columns can't be set by hand: a direct write is replaced by the JSON's values.
	mustExec(t, pool, `UPDATE calendar_objects SET starts_at = '2000-01-01Z', ends_at = NULL WHERE id = $1`, id)
	s, e = eventTimes(t, pool, id)
	wantInstant(t, "after direct write: starts_at", s, "2026-12-01T09:00:00Z")
	wantInstant(t, "after direct write: ends_at", e, "2026-12-01T10:00:00Z")
	mustExec(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, starts_at, ends_at) VALUES ($1, 'forged', '{}', 'e', now(), now())`, cal)
	if n := countRows(t, pool, `SELECT count(*) FROM calendar_objects WHERE uid = 'forged' AND starts_at IS NULL AND ends_at IS NULL`); n != 1 {
		t.Error("insert kept hand-written starts_at/ends_at")
	}

	// A malformed update never fails the write; it clears the columns.
	mustExec(t, pool, `UPDATE calendar_objects SET ical_data = 'garbage' WHERE id = $1`, id)
	s, e = eventTimes(t, pool, id)
	wantInstant(t, "after garbage: starts_at", s, "")
	wantInstant(t, "after garbage: ends_at", e, "")

	// The range index exists and the planner can use it.
	for _, idx := range []string{"idx_calendar_objects_calendar_starts", "idx_calendars_household", "idx_bills_household",
		"idx_maintenance_tasks_household", "idx_maintenance_schedules_household", "idx_vendors_household",
		"idx_notifications_household", "idx_calendar_objects_ical_trgm"} {
		if n := countRows(t, pool, `SELECT count(*) FROM pg_indexes WHERE indexname = $1`, idx); n != 1 {
			t.Errorf("index %s missing", idx)
		}
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	rows, err := tx.Query(ctx, `EXPLAIN SELECT co.id FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id
		WHERE c.household_id = $1 AND co.starts_at < $2 AND (co.ends_at > $3 OR co.rrule IS NOT NULL)`,
		f.household, time.Now().Add(7*24*time.Hour), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	rows.Close()
	if !strings.Contains(plan.String(), "idx_calendar_objects_calendar_starts") {
		t.Errorf("range query does not use idx_calendar_objects_calendar_starts:\n%s", plan.String())
	}
}

// migrationsFrom lists the migration files numbered >= from, oldest first.
func migrationsFrom(t *testing.T, from int, kind string) []string {
	t.Helper()
	dir, err := migrationsDir()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*."+kind+".sql"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range paths {
		name := filepath.Base(p)
		var n int
		for _, c := range name[:3] {
			n = n*10 + int(c-'0')
		}
		if n >= from {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// TestHotPathIndexes035_FullChainReversible walks the whole chain back to
// 034 (052 ... 035 down, newest first), writes events the way pre-035 code
// did, then applies 035 ... 052 again and checks the backfill. It does the
// down/up walk twice, so up/down/up holds on a database with data in it.
func TestHotPathIndexes035_FullChainReversible(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, f.household)
	// An event written at 052, carried through every down and up.
	kept := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, rrule) VALUES ($1, 'kept', $2, 'e', 'FREQ=WEEKLY') RETURNING id`,
		cal, `{"title":"Swim","start":"2026-01-06T23:00:00Z","end":"2026-01-07T00:00:00Z"}`)

	downs := migrationsFrom(t, 35, "down")
	ups := migrationsFrom(t, 35, "up")
	if len(downs) != len(ups) || ups[0] != "035_mcp_hot_path_indexes.up.sql" {
		t.Fatalf("unexpected chain: ups %v downs %v", ups, downs)
	}

	for cycle := 0; cycle < 2; cycle++ {
		for i := len(downs) - 1; i >= 0; i-- {
			applyMigration(t, pool, downs[i])
		}
		if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'calendar_objects' AND column_name IN ('starts_at', 'ends_at', 'rrule')`); n != 0 {
			t.Fatalf("cycle %d: columns left at 034 (%d)", cycle, n)
		}
		for _, sql := range []string{
			`SELECT count(*) FROM pg_trigger WHERE tgname = 'calendar_objects_event_times'`,
			`SELECT count(*) FROM pg_proc WHERE proname IN ('calendar_event_instant', 'calendar_objects_set_event_times')`,
			`SELECT count(*) FROM pg_indexes WHERE indexname LIKE 'idx_calendar_objects_%' OR indexname IN ('idx_calendars_household', 'idx_bills_household')`,
			`SELECT count(*) FROM pg_extension WHERE extname = 'pg_trgm'`,
		} {
			if n := countRows(t, pool, sql); n != 0 {
				t.Fatalf("cycle %d: left at 034: %s = %d", cycle, sql, n)
			}
		}

		// Pre-035 writers: no starts_at/ends_at columns to fill.
		pre := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, $2, $3, 'e') RETURNING id`,
			cal, uuid.NewString(), `{"all_day":true,"start":"2026-11-18T00:00:00Z","end":"2026-11-19T00:00:00Z"}`)
		bad := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, $2, 'not json', 'e') RETURNING id`,
			cal, uuid.NewString())
		var etag string
		var updated time.Time
		mustScan(t, pool.QueryRow(context.Background(), `SELECT etag, updated_at FROM calendar_objects WHERE id = $1`, pre), &etag, &updated)

		for _, up := range ups {
			applyMigration(t, pool, up)
		}

		s, e := eventTimes(t, pool, pre)
		wantInstant(t, "backfilled all-day starts_at", s, "2026-11-18T00:00:00Z")
		wantInstant(t, "backfilled all-day ends_at", e, "2026-11-19T00:00:00Z")
		s, e = eventTimes(t, pool, bad)
		wantInstant(t, "backfilled malformed starts_at", s, "")
		wantInstant(t, "backfilled malformed ends_at", e, "")
		s, e = eventTimes(t, pool, kept)
		wantInstant(t, "kept starts_at", s, "2026-01-06T23:00:00Z")
		wantInstant(t, "kept ends_at", e, "2026-01-07T00:00:00Z")

		var etag2 string
		var updated2 time.Time
		mustScan(t, pool.QueryRow(context.Background(), `SELECT etag, updated_at FROM calendar_objects WHERE id = $1`, pre), &etag2, &updated2)
		if etag2 != etag || !updated2.Equal(updated) {
			t.Errorf("cycle %d: backfill touched etag/updated_at", cycle)
		}
		// The trigger is live after the full chain is back.
		mustExec(t, pool, `UPDATE calendar_objects SET ical_data = $2 WHERE id = $1`, bad, `{"start":"2026-11-20T10:00:00Z","end":"2026-11-20T11:00:00Z"}`)
		s, _ = eventTimes(t, pool, bad)
		wantInstant(t, "trigger after up", s, "2026-11-20T10:00:00Z")
	}
}
