package billmirror

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"home-os/billcal"
	"home-os/icalx"
)

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

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func mustID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id
}

type mirrorRow struct {
	id, calendarID, etag, data, raw string
	rrule                           *string
}

// mirrors returns the bill's mirror events (entity_type and event_type 'bill').
func mirrors(t *testing.T, pool *pgxpool.Pool, billID string) []mirrorRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT id::text, calendar_id::text, etag, ical_data, COALESCE(ical_raw, ''), rrule
		   FROM calendar_objects WHERE entity_type = 'bill' AND entity_id = $1 AND event_type = 'bill'`, billID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []mirrorRow
	for rows.Next() {
		var r mirrorRow
		if err := rows.Scan(&r.id, &r.calendarID, &r.etag, &r.data, &r.raw, &r.rrule); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func TestBackfill(t *testing.T) {
	pool := newMigratedPool(t)
	ctx := context.Background()

	hh := mustID(t, pool, `INSERT INTO households (name) VALUES ('Sims') RETURNING id::text`)
	billsCal := mustID(t, pool,
		`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Bills', 'bills', $2) RETURNING id::text`, hh, uuid.NewString())
	family := mustID(t, pool,
		`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', $2) RETURNING id::text`, hh, uuid.NewString())
	// A household with no Bills calendar gets nothing.
	other := mustID(t, pool, `INSERT INTO households (name) VALUES ('Other') RETURNING id::text`)

	newBill := func(household, name, freq string, dueDay *int, dueDate *string) string {
		return mustID(t, pool,
			`INSERT INTO bills (household_id, name, amount, category, frequency, due_day, due_date, is_autopay, payment_url)
			 VALUES ($1, $2, 120.5, 'Utilities', $3, $4, $5::date, true, 'https://pay.example') RETURNING id::text`,
			household, name, freq, dueDay, dueDate)
	}
	day := func(n int) *int { return &n }
	date := func(s string) *string { return &s }

	want := map[string]string{ // bill id -> RRULE ("" for once)
		newBill(hh, "Power", "monthly", day(31), nil):                "FREQ=MONTHLY;BYMONTHDAY=-1",
		newBill(hh, "Water", "quarterly", nil, date("2027-01-30")):   "FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=28,29,30;BYSETPOS=-1",
		newBill(hh, "Trash", "semiannual", nil, date("2027-03-05")):  "FREQ=MONTHLY;INTERVAL=6;BYMONTHDAY=5",
		newBill(hh, "Insurance", "annual", nil, date("2028-02-29")):  "FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=28,29;BYSETPOS=-1",
		newBill(hh, "Registration", "once", nil, date("2027-04-01")): "",
	}
	noSchedule := newBill(hh, "Someday", "annual", nil, nil)
	noCalendar := newBill(other, "Elsewhere", "monthly", day(5), nil)

	// A bill that already has its mirror event (e.g. made by the MCP)
	// keeps it untouched.
	mirrored := newBill(hh, "Phone", "monthly", day(12), nil)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := billcal.SyncByID(ctx, tx, hh, mirrored); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before := mirrors(t, pool, mirrored)
	if len(before) != 1 {
		t.Fatalf("pre-mirrored bill has %d events", len(before))
	}

	// An event a person made and linked to a bill is not a mirror: it is
	// never touched, and the bill still gets its own mirror event.
	linked := newBill(hh, "Gym", "monthly", day(3), nil)
	userEvent := mustID(t, pool,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, entity_type, entity_id)
		 VALUES ($1, 'user-gym', '{"title":"Cancel gym","start":"2027-01-03T10:00:00Z","end":"2027-01-03T11:00:00Z"}', 'user-etag', 'bill', $2)
		 RETURNING id::text`, family, linked)
	want[linked] = "FREQ=MONTHLY;BYMONTHDAY=3"

	n, err := billcal.Backfill(ctx, pool)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if n != len(want) {
		t.Errorf("first run created %d events, want %d", n, len(want))
	}

	for billID, rule := range want {
		ev := mirrors(t, pool, billID)
		if len(ev) != 1 {
			t.Fatalf("bill %s has %d mirror events, want 1", billID, len(ev))
		}
		e := ev[0]
		if e.calendarID != billsCal {
			t.Errorf("bill %s event on calendar %s, want Bills %s", billID, e.calendarID, billsCal)
		}
		if got := deref(e.rrule); got != rule {
			t.Errorf("bill %s rrule = %q, want %q", billID, got, rule)
		}
		// Exactly what the shared writer (and so the MCP and API) would
		// store for this bill: JSON, raw and rrule from one master.
		b, ok, err := loadBill(ctx, pool, hh, billID)
		if err != nil || !ok {
			t.Fatalf("load bill: %v %v", ok, err)
		}
		var uid string
		if err := pool.QueryRow(ctx, `SELECT uid FROM calendar_objects WHERE id = $1`, e.id).Scan(&uid); err != nil {
			t.Fatal(err)
		}
		m, _ := billcal.Master(b, uid, 0)
		wantData, wantRaw, _, err := billcal.Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		if !sameJSON(t, e.data, wantData) || noStamp(e.raw) != noStamp(wantRaw) {
			t.Errorf("bill %s stored event differs from billcal.Encode:\n data %s\n want %s", billID, e.data, wantData)
		}
		obj, err := icalx.Parse([]byte(e.raw))
		if err != nil {
			t.Fatalf("parse raw: %v", err)
		}
		if obj.Master.RRule != rule || len(obj.Master.Alarms) != 1 || !obj.Master.AllDay {
			t.Errorf("bill %s raw master = rrule %q alarms %d allday %v", billID, obj.Master.RRule, len(obj.Master.Alarms), obj.Master.AllDay)
		}
	}
	for _, id := range []string{noSchedule, noCalendar} {
		if ev := mirrors(t, pool, id); len(ev) != 0 {
			t.Errorf("bill %s got %d events, want none", id, len(ev))
		}
	}
	after := mirrors(t, pool, mirrored)
	if len(after) != 1 || after[0].id != before[0].id || after[0].etag != before[0].etag ||
		after[0].data != before[0].data || after[0].raw != before[0].raw || deref(after[0].rrule) != deref(before[0].rrule) {
		t.Errorf("pre-mirrored event changed:\n%+v\n%+v", before, after)
	}
	var etag, data string
	var entity *string
	if err := pool.QueryRow(ctx, `SELECT etag, ical_data, entity_type FROM calendar_objects WHERE id = $1`, userEvent).
		Scan(&etag, &data, &entity); err != nil {
		t.Fatal(err)
	}
	if etag != "user-etag" || deref(entity) != "bill" || !sameJSON(t, data, `{"title":"Cancel gym","start":"2027-01-03T10:00:00Z","end":"2027-01-03T11:00:00Z"}`) {
		t.Errorf("user event touched: etag %s entity %v data %s", etag, entity, data)
	}
	if got := count(t, pool, `SELECT count(*) FROM calendar_changes WHERE calendar_id = $1 AND action = 'create'`, billsCal); got != len(want)+1 {
		t.Errorf("calendar_changes creates = %d, want %d", got, len(want)+1)
	}

	t.Run("second run is a no-op", func(t *testing.T) {
		changes := count(t, pool, `SELECT count(*) FROM calendar_changes`)
		var ctag string
		if err := pool.QueryRow(ctx, `SELECT ctag FROM calendars WHERE id = $1`, billsCal).Scan(&ctag); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			n, err := billcal.Backfill(ctx, pool)
			if err != nil || n != 0 {
				t.Fatalf("run %d: created %d, err %v", i+2, n, err)
			}
		}
		if got := count(t, pool, `SELECT count(*) FROM calendar_changes`); got != changes {
			t.Errorf("calendar_changes grew %d -> %d", changes, got)
		}
		var ctag2 string
		if err := pool.QueryRow(ctx, `SELECT ctag FROM calendars WHERE id = $1`, billsCal).Scan(&ctag2); err != nil {
			t.Fatal(err)
		}
		if ctag2 != ctag {
			t.Error("ctag bumped by a no-op run")
		}
		if got := count(t, pool, `SELECT count(*) FROM calendar_objects WHERE event_type = 'bill'`); got != len(want)+1 {
			t.Errorf("mirror events = %d, want %d", got, len(want)+1)
		}
	})

	t.Run("concurrent runs create one event", func(t *testing.T) {
		bill := newBill(hh, "Internet", "monthly", day(20), nil)
		var wg sync.WaitGroup
		created := make([]int, 4)
		errs := make([]error, 4)
		for i := range created {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				created[i], errs[i] = billcal.Backfill(ctx, pool)
			}(i)
		}
		wg.Wait()
		total := 0
		for i := range created {
			if errs[i] != nil {
				t.Fatalf("run %d: %v", i, errs[i])
			}
			total += created[i]
		}
		if ev := mirrors(t, pool, bill); len(ev) != 1 || total != 1 {
			t.Errorf("concurrent backfill: %d events, %d created; want 1, 1", len(ev), total)
		}
	})

	t.Run("a Bills calendar added later is backfilled", func(t *testing.T) {
		mustExec(t, pool,
			`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Bills', 'bills', $2)`, other, uuid.NewString())
		n, err := billcal.Backfill(ctx, pool)
		if err != nil || n != 1 {
			t.Fatalf("created %d, err %v; want 1", n, err)
		}
		if ev := mirrors(t, pool, noCalendar); len(ev) != 1 {
			t.Errorf("bill in the other household has %d events", len(ev))
		}
	})

	t.Run("Run backfills at startup and stops on cancel", func(t *testing.T) {
		bill := newBill(hh, "Streaming", "monthly", day(9), nil)
		ctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- New(pool, time.Hour).Run(ctx) }()
		deadline := time.Now().Add(30 * time.Second)
		for len(mirrors(t, pool, bill)) == 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not stop")
		}
		if ev := mirrors(t, pool, bill); len(ev) != 1 {
			t.Errorf("startup run: %d events", len(ev))
		}
	})
}

func loadBill(ctx context.Context, pool *pgxpool.Pool, hh, id string) (billcal.Bill, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return billcal.Bill{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	return billcal.LoadBill(ctx, tx, hh, id)
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

func sameJSON(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Fatalf("json %q: %v", a, err)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Fatalf("json %q: %v", b, err)
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}
