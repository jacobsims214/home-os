package alarms

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// ics wraps VEVENT lines in a VCALENDAR with CRLF line endings.
func ics(lines ...string) string {
	all := append([]string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//test//EN"}, lines...)
	all = append(all, "END:VCALENDAR")
	return strings.Join(all, "\r\n") + "\r\n"
}

// event returns a VEVENT with a single VALARM of the given trigger.
func event(uid, dtstart, dtend, trigger string, extra ...string) []string {
	l := []string{"BEGIN:VEVENT", "UID:" + uid, "DTSTAMP:20261101T000000Z", "SUMMARY:Dentist", dtstart, dtend}
	l = append(l, extra...)
	l = append(l, "BEGIN:VALARM", "ACTION:DISPLAY", "DESCRIPTION:Reminder", "TRIGGER:"+trigger, "END:VALARM", "END:VEVENT")
	return l
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestFiringsFor(t *testing.T) {
	obj, hh := uuid.New(), uuid.New()
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	thirtyMin := ics(event("a", "DTSTART:20261120T150000Z", "DTEND:20261120T160000Z", "-PT30M")...)

	tests := []struct {
		name     string
		raw      string
		loc      *time.Location
		now      string
		lookback time.Duration
		wantAt   []string
	}{
		{"one second before the alarm", thirtyMin, time.UTC, "2026-11-20T14:29:59Z", DefaultLookback, nil},
		{"exactly 30 minutes before start", thirtyMin, time.UTC, "2026-11-20T14:30:00Z", DefaultLookback, []string{"2026-11-20T14:30:00Z"}},
		{"within lookback", thirtyMin, time.UTC, "2026-11-20T14:44:00Z", DefaultLookback, []string{"2026-11-20T14:30:00Z"}},
		{"older than lookback", thirtyMin, time.UTC, "2026-11-20T14:46:00Z", DefaultLookback, nil},
		{
			"floating event uses household zone",
			ics(event("f", "DTSTART:20261120T090000", "DTEND:20261120T100000", "-PT30M")...),
			chicago, "2026-11-20T14:30:00Z", DefaultLookback,
			[]string{"2026-11-20T14:30:00Z"}, // 08:30 CST
		},
		{
			"daily series fires for today's occurrence only",
			ics(event("d", "DTSTART;TZID=America/Chicago:20261116T090000", "DTEND;TZID=America/Chicago:20261116T093000", "-PT30M", "RRULE:FREQ=DAILY;COUNT=10")...),
			time.UTC, "2026-11-19T14:35:00Z", DefaultLookback,
			[]string{"2026-11-19T14:30:00Z"},
		},
		{
			"two alarms at the same instant collapse to one",
			ics("BEGIN:VEVENT", "UID:two", "DTSTAMP:20261101T000000Z", "SUMMARY:X",
				"DTSTART:20261120T150000Z", "DTEND:20261120T160000Z",
				"BEGIN:VALARM", "ACTION:DISPLAY", "DESCRIPTION:a", "TRIGGER:-PT30M", "END:VALARM",
				"BEGIN:VALARM", "ACTION:AUDIO", "TRIGGER:-PT30M", "END:VALARM",
				"END:VEVENT"),
			time.UTC, "2026-11-20T14:31:00Z", DefaultLookback,
			[]string{"2026-11-20T14:30:00Z"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs, err := FiringsFor(obj, hh, tt.raw, tt.loc, mustTime(t, tt.now), tt.lookback)
			if err != nil {
				t.Fatalf("FiringsFor: %v", err)
			}
			var got []string
			for _, f := range fs {
				got = append(got, f.At.UTC().Format(time.RFC3339))
			}
			if strings.Join(got, ",") != strings.Join(tt.wantAt, ",") {
				t.Fatalf("firings = %v, want %v", got, tt.wantAt)
			}
		})
	}
}

func TestFiringsForMessage(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	raw := ics(event("m", "DTSTART:20261120T150000Z", "DTEND:20261120T160000Z", "-PT30M", "LOCATION:Main St")...)
	fs, err := FiringsFor(uuid.New(), uuid.New(), raw, ny, mustTime(t, "2026-11-20T14:30:00Z"), time.Minute)
	if err != nil || len(fs) != 1 {
		t.Fatalf("got %d firings, err %v", len(fs), err)
	}
	if fs[0].Title != "Dentist" {
		t.Errorf("title = %q", fs[0].Title)
	}
	if want := "Starts Fri Nov 20, 10:00 AM EST at Main St"; fs[0].Body != want {
		t.Errorf("body = %q, want %q", fs[0].Body, want)
	}
}

func TestFiringsForParseError(t *testing.T) {
	if _, err := FiringsFor(uuid.New(), uuid.New(), "not ical", time.UTC, time.Now(), time.Minute); err == nil {
		t.Fatal("want parse error")
	}
}

// --- Postgres-backed tests ---

func newMigratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker; skipped with -short")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("homeos"),
		tcpostgres.WithUsername("homeos"),
		tcpostgres.WithPassword("homeos"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container (is Docker running?): %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = container.Terminate(ctx)
	})
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	// Migrations GRANT to the application role, which a fresh Postgres lacks.
	if _, err := conn.Exec(ctx, `CREATE ROLE app`); err != nil {
		t.Fatalf("create app role: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(migrationsDir(t), "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found (err=%v)", err)
	}
	sort.Strings(files)
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(f), err)
		}
	}
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func migrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	for dir := filepath.Dir(file); ; {
		candidate := filepath.Join(dir, "packages", "db", "migrations")
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("packages/db/migrations not found above %s", file)
		}
		dir = parent
	}
}

type fixture struct {
	pool     *pgxpool.Pool
	hh       string
	calendar string
	members  []string
}

func newFixture(t *testing.T, pool *pgxpool.Pool, tz string, members int) fixture {
	t.Helper()
	ctx := context.Background()
	f := fixture{pool: pool}
	if err := pool.QueryRow(ctx, `INSERT INTO households (name, timezone) VALUES ('Sims', $1) RETURNING id::text`, tz).Scan(&f.hh); err != nil {
		t.Fatalf("household: %v", err)
	}
	for i := 0; i < members; i++ {
		var uid string
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (email, name, password_hash) VALUES ($1, 'm', 'x') RETURNING id::text`,
			uuid.NewString()+"@example.com").Scan(&uid); err != nil {
			t.Fatalf("user: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO memberships (household_id, user_id) VALUES ($1, $2)`, f.hh, uid); err != nil {
			t.Fatalf("membership: %v", err)
		}
		f.members = append(f.members, uid)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', $2) RETURNING id::text`,
		f.hh, uuid.NewString()).Scan(&f.calendar); err != nil {
		t.Fatalf("calendar: %v", err)
	}
	return f
}

func (f fixture) addObject(t *testing.T, uid string, raw *string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, ical_raw) VALUES ($1, $2, '{}', 'e', $3) RETURNING id::text`,
		f.calendar, uid, raw).Scan(&id); err != nil {
		t.Fatalf("calendar object: %v", err)
	}
	return id
}

type notifRow struct {
	UserID   string
	Title    string
	EntityID string
}

func (f fixture) notifications(t *testing.T) []notifRow {
	t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT user_id::text, title, entity_id::text FROM notifications
		 WHERE household_id = $1 AND type = 'calendar_alarm' ORDER BY created_at, user_id`, f.hh)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[notifRow])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func process(t *testing.T, j *Job, now time.Time) int {
	t.Helper()
	n, err := j.Process(context.Background(), now)
	if err != nil {
		t.Fatalf("Process(%s): %v", now.Format(time.RFC3339), err)
	}
	return n
}

func TestProcess(t *testing.T) {
	pool := newMigratedPool(t)

	t.Run("30-minute alarm notifies every member once", func(t *testing.T) {
		f := newFixture(t, pool, "America/Chicago", 2)
		raw := ics(event("dentist", "DTSTART;TZID=America/Chicago:20261120T150000", "DTEND;TZID=America/Chicago:20261120T160000", "-PT30M")...)
		f.addObject(t, "dentist", &raw)
		start := mustTime(t, "2026-11-20T21:00:00Z") // 15:00 CST

		j := New(pool)
		if n := process(t, j, start.Add(-31*time.Minute)); n != 0 {
			t.Fatalf("before alarm: inserted %d, want 0", n)
		}
		if n := process(t, j, start.Add(-30*time.Minute)); n != 2 {
			t.Fatalf("at alarm: inserted %d, want 2 (one per member)", n)
		}
		if n := process(t, j, start.Add(-29*time.Minute)); n != 0 {
			t.Fatalf("next tick: inserted %d, want 0 (dedupe)", n)
		}
		// A restarted worker has no in-memory state; dedupe must hold.
		if n := process(t, New(pool), start.Add(-20*time.Minute)); n != 0 {
			t.Fatalf("after restart: inserted %d, want 0", n)
		}

		got := f.notifications(t)
		if len(got) != 2 {
			t.Fatalf("notifications = %d, want 2", len(got))
		}
		users := map[string]bool{got[0].UserID: true, got[1].UserID: true}
		for _, m := range f.members {
			if !users[m] {
				t.Errorf("member %s got no notification", m)
			}
		}
		if got[0].Title != "Dentist" || got[0].EntityID != got[1].EntityID {
			t.Errorf("unexpected rows: %+v", got)
		}
	})

	t.Run("each occurrence of a series is its own notification", func(t *testing.T) {
		f := newFixture(t, pool, "UTC", 1)
		raw := ics(event("standup", "DTSTART:20261116T150000Z", "DTEND:20261116T153000Z", "-PT30M", "RRULE:FREQ=DAILY;COUNT=5")...)
		f.addObject(t, "standup", &raw)

		j := New(pool)
		for _, day := range []string{"2026-11-17", "2026-11-18"} {
			now := mustTime(t, day+"T14:31:00Z")
			if n := process(t, j, now); n != 1 {
				t.Fatalf("%s: inserted %d, want 1", day, n)
			}
			if n := process(t, j, now.Add(time.Minute)); n != 0 {
				t.Fatalf("%s repeat: inserted %d, want 0", day, n)
			}
		}
		got := f.notifications(t)
		if len(got) != 2 || got[0].EntityID == got[1].EntityID {
			t.Fatalf("want 2 distinct occurrence notifications, got %+v", got)
		}
	})

	t.Run("rows without ical_raw and broken ical are skipped", func(t *testing.T) {
		f := newFixture(t, pool, "UTC", 1)
		f.addObject(t, "legacy", nil)
		bad := "BEGIN:VCALENDAR\r\nBEGIN:VALARM\r\nnonsense\r\n"
		f.addObject(t, "bad", &bad)
		good := ics(event("good", "DTSTART:20261201T120000Z", "DTEND:20261201T130000Z", "-PT30M")...)
		f.addObject(t, "good", &good)

		if n := process(t, New(pool), mustTime(t, "2026-12-01T11:30:00Z")); n != 1 {
			t.Fatalf("inserted %d, want 1 (only the good event)", n)
		}
	})
}
