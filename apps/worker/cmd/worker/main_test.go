package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// serverEnv points the harness at an already-running PostgreSQL instead of
// starting a container (#5500). It is read as a conn string or URL
// ("host=/tmp/pg port=55432 user=postgres dbname=postgres" or
// "postgres://user:pass@host:5432/db?sslmode=disable"), and the migrations are
// applied to a template database on it, which every test then clones. It exists
// because the container path needs Docker, and a machine without a working
// daemon should not be a machine with no database tests: the MCP harness
// (apps/mcp/internal/testutil) honours the same variable for the same reason.
const serverEnv = "HOMEOS_TEST_POSTGRES"

// newMigratedPool returns a pool to a database with every
// packages/db/migrations/*.up.sql applied: a throwaway Postgres 16 container by
// default, or a database on the server in HOMEOS_TEST_POSTGRES when that is
// set. Either way the database is dropped/terminated when t finishes.
func newMigratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("needs a database; skipped with -short")
	}
	if server := os.Getenv(serverEnv); server != "" {
		return newServerPool(t, server)
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
		// No arguments: simple protocol, so a file may hold several statements.
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

// newServerPool is newMigratedPool's other half: a fresh database on the server
// in HOMEOS_TEST_POSTGRES, cloned from a migrated template built once per test
// binary. One template per binary, because `go test ./...` runs one binary per
// package in parallel and two of them building the same template would drop it
// under each other.
func newServerPool(t *testing.T, server string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	cfg, err := pgx.ParseConfig(server)
	if err != nil {
		t.Fatalf("%s: %v", serverEnv, err)
	}
	adminCfg := cfg.Copy()
	adminCfg.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		t.Fatalf("%s: connect %s: %v", serverEnv, server, err)
	}
	defer admin.Close(ctx)

	// The migrations GRANT to the application role, which a fresh server lacks.
	if _, err := admin.Exec(ctx, `DO $$ BEGIN
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app') THEN CREATE ROLE app; END IF;
	END $$`); err != nil {
		t.Fatalf("create app role: %v", err)
	}

	tpl := fmt.Sprintf("homeos_tpl_%d", os.Getpid())
	if _, err := admin.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", tpl)); err != nil {
		t.Fatalf("drop template: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+tpl); err != nil {
		t.Fatalf("create template: %v", err)
	}
	tplCfg := cfg.Copy()
	tplCfg.Database = tpl
	conn, err := pgx.ConnectConfig(ctx, tplCfg)
	if err != nil {
		t.Fatalf("connect template: %v", err)
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
	if err := conn.Close(ctx); err != nil {
		t.Fatalf("close template: %v", err)
	}

	// A fresh database per call, cloned from the template: tests in this package
	// share one process and truncate their own tables, but a caller that wants a
	// database nobody else has touched gets one.
	name := fmt.Sprintf("homeos_%d_%d", os.Getpid(), testDBCounter.Add(1))
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, tpl)); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	dbCfg, err := pgxpool.ParseConfig(server)
	if err != nil {
		t.Fatalf("%s: %v", serverEnv, err)
	}
	dbCfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, dbCfg)
	if err != nil {
		t.Fatalf("pool %s: %v", name, err)
	}
	t.Cleanup(func() {
		pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := pgx.ConnectConfig(ctx, adminCfg)
		if err != nil {
			return
		}
		defer conn.Close(ctx)
		_, _ = conn.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name))
	})
	return pool
}

var testDBCounter atomic.Int64

// migrationsDir walks up from this file to packages/db/migrations.
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

func seedHousehold(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO households (name) VALUES ('Test') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	return id
}

func seedBill(t *testing.T, pool *pgxpool.Pool, hh, name string, dueDay int) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, $2, 42.50, $3) RETURNING id::text`,
		hh, name, dueDay).Scan(&id); err != nil {
		t.Fatalf("insert bill: %v", err)
	}
	return id
}

// seedBillWithAccount is a MANUAL bill that has an account on record: the
// bills.account_number column a pre-075 row carries, which
// billdue.AttentionFor reads as "a person pays this, and the family knows from
// where" (due_soon). Without it a manual bill is needs_setup and is reminded
// earlier (#5500), which is a different test.
//
// createdAt is when the row appeared, which the overdue rule reads: a due date
// that passed before the row existed is unknowable, not late. A test that
// simulates a future "now" must say so, or the bill is (correctly) overdue for
// every month between the real date and the simulated one.
func seedBillWithAccount(t *testing.T, pool *pgxpool.Pool, hh, name string, dueDay int, createdAt time.Time) string {
	t.Helper()
	id := seedBill(t, pool, hh, name, dueDay)
	if _, err := pool.Exec(context.Background(),
		`UPDATE bills SET account_number = 'legacy-4471', created_at = $2 WHERE id = $1`, id, createdAt); err != nil {
		t.Fatalf("set account_number: %v", err)
	}
	return id
}

func seedTask(t *testing.T, pool *pgxpool.Pool, hh, name, status, due string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO maintenance_tasks (household_id, name, status, due_date)
		 VALUES ($1, $2, $3::task_status, $4::date) RETURNING id::text`,
		hh, name, status, due).Scan(&id); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	return id
}

// seedBillCreatedOn is seedBill for a bill that has been on the books since
// createdAt: the overdue rule (billdue.AttentionFor) treats a due date that
// passed before the row existed as unknowable rather than late, so a test that
// wants "not overdue" has to say when the row appeared. A bill seeded with the
// table default (NOW()) is created today, which also reads as not overdue.
func seedBillCreatedOn(t *testing.T, pool *pgxpool.Pool, hh, name string, dueDay int, createdAt time.Time) string {
	t.Helper()
	id := seedBill(t, pool, hh, name, dueDay)
	if _, err := pool.Exec(context.Background(),
		`UPDATE bills SET created_at = $2 WHERE id = $1`, id, createdAt); err != nil {
		t.Fatalf("set created_at: %v", err)
	}
	return id
}

func day(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d.Add(15 * time.Hour) // mid-afternoon; only the date matters
}

// notifiedEntities returns entity_id -> count of notifications of type typ.
func notifiedEntities(t *testing.T, pool *pgxpool.Pool, typ string) map[string]int {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT entity_id::text, count(*) FROM notifications WHERE type = $1 GROUP BY entity_id`, typ)
	if err != nil {
		t.Fatalf("query notifications: %v", err)
	}
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func TestGenerateNotifications_Postgres(t *testing.T) {
	pool := newMigratedPool(t)

	t.Run("bills: month boundary and clamping", func(t *testing.T) {
		tests := []struct {
			name     string
			today    string
			dueDay   int
			want     bool
			wantBody string
		}{
			{"wraps into next month", "2026-11-29", 1, true, "Payment of $42.50 due on Dec 1"},
			{"wraps into next year", "2026-12-30", 2, true, "Payment of $42.50 due on Jan 2"},
			{"same month, today", "2026-11-10", 10, true, "Payment of $42.50 due on Nov 10"},
			{"same month, in 3 days", "2026-11-10", 13, true, "Payment of $42.50 due on Nov 13"},
			{"4 days out", "2026-11-10", 14, false, ""},
			{"yesterday", "2026-11-10", 9, false, ""},
			{"day 31 clamps to Feb 28", "2027-02-26", 31, true, "Payment of $42.50 due on Feb 28"},
			{"day 30 clamps to Feb 28 from Feb 25", "2027-02-25", 30, true, "Payment of $42.50 due on Feb 28"},
			{"day 31 in Nov is Nov 30", "2026-11-28", 31, true, "Payment of $42.50 due on Nov 30"},
			{"far from month end", "2026-11-29", 15, false, ""},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				if _, err := pool.Exec(ctx, `TRUNCATE notifications, bills, maintenance_tasks CASCADE`); err != nil {
					t.Fatal(err)
				}
				hh := seedHousehold(t, pool)
				billID := seedBillWithAccount(t, pool, hh, "Power", tc.dueDay, day(tc.today))

				got, err := insertDueNotifications(ctx, pool, day(tc.today))
				if err != nil {
					t.Fatalf("insertDueNotifications: %v", err)
				}
				if tc.want != (len(got) == 1) || len(got) > 1 {
					t.Fatalf("got %d notifications, want created=%v: %+v", len(got), tc.want, got)
				}
				if !tc.want {
					return
				}
				n := got[0]
				if n.Type != "bill_due" || n.HouseholdID != hh || n.Title != "Power due soon" || n.Body != tc.wantBody {
					t.Errorf("unexpected notification %+v (want body %q)", n, tc.wantBody)
				}
				if c := notifiedEntities(t, pool, "bill_due")[billID]; c != 1 {
					t.Errorf("entity_id %s has %d bill_due rows, want 1", billID, c)
				}
			})
		}
	})

	// paid_date follows the MCP rule (coveredDueDate): a stored
	// paid_for_due_date (bill_pay's for_due_date, #4655) wins; otherwise a
	// payment covers the most recent due date when at most 7 days late, and
	// the next one after that.
	t.Run("bills: paid_date covering the due date", func(t *testing.T) {
		// Today Nov 18, due_day 20: due in 2 days (Nov 20).
		tests := []struct {
			name     string
			paidDate string // "" = never paid
			paidFor  string // stored paid_for_due_date; "" = NULL
			want     bool
		}{
			{"paid 3 days ago covers this due date", "2026-11-15", "", false},
			{"paid on the due date ahead of time", "2026-11-20", "", false},
			{"never paid", "", "", true},
			{"paid on last month's due date only", "2026-10-20", "", true},
			{"paid early for last month only", "2026-10-18", "", true},
			{"paid 5 days late for last month (grace)", "2026-10-25", "", true},
			{"paid 7 days late for last month (grace edge)", "2026-10-27", "", true},
			{"paid 8 days late: pays this month", "2026-10-28", "", false},
			{"paid 8 days late for last month (stored): still notifies", "2026-10-28", "2026-10-20", true},
			{"paid Oct 28 for this due date (stored)", "2026-10-28", "2026-11-20", false},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				if _, err := pool.Exec(ctx, `TRUNCATE notifications, bills, maintenance_tasks CASCADE`); err != nil {
					t.Fatal(err)
				}
				hh := seedHousehold(t, pool)
				billID := seedBillWithAccount(t, pool, hh, "Electric", 20, day("2026-11-18"))
				if tc.paidDate != "" {
					if _, err := pool.Exec(ctx, `UPDATE bills SET paid_date = $2::date, paid_for_due_date = NULLIF($3, '')::date WHERE id = $1`,
						billID, tc.paidDate, tc.paidFor); err != nil {
						t.Fatal(err)
					}
				}

				got, err := insertDueNotifications(ctx, pool, day("2026-11-18"))
				if err != nil {
					t.Fatalf("insertDueNotifications: %v", err)
				}
				if tc.want != (len(got) == 1) || len(got) > 1 {
					t.Fatalf("got %d notifications, want created=%v: %+v", len(got), tc.want, got)
				}
				if tc.want && got[0].Body != "Payment of $42.50 due on Nov 20" {
					t.Errorf("body = %q", got[0].Body)
				}
			})
		}
	})

	// Non-monthly bills (044, #4468): quarterly/semiannual/annual repeat on
	// due_date's day every 3/6/12 months, once is due on due_date only.
	t.Run("bills: frequencies", func(t *testing.T) {
		// Today Nov 18; the window is [Nov 18, Nov 21].
		tests := []struct {
			name      string
			frequency string
			dueDate   string // "" = NULL
			paidDate  string // "" = never paid
			want      string // notification body; "" = none
		}{
			{"annual from last year", "annual", "2025-11-20", "", "Payment of $42.50 due on Nov 20"},
			{"annual not until next year", "annual", "2025-12-20", "", ""},
			{"quarterly from August", "quarterly", "2026-08-21", "", "Payment of $42.50 due on Nov 21"},
			{"quarterly next month", "quarterly", "2026-09-20", "", ""},
			{"semiannual from May", "semiannual", "2026-05-19", "", "Payment of $42.50 due on Nov 19"},
			{"annual paid early", "annual", "2025-11-20", "2026-11-17", ""},
			{"once tomorrow", "once", "2026-11-19", "", "Payment of $42.50 due on Nov 19"},
			{"once tomorrow, paid", "once", "2026-11-19", "2026-11-10", ""},
			{"once already past", "once", "2026-11-15", "", ""},
			{"annual without due_date", "annual", "", "", ""},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				if _, err := pool.Exec(ctx, `TRUNCATE notifications, bills, maintenance_tasks CASCADE`); err != nil {
					t.Fatal(err)
				}
				hh := seedHousehold(t, pool)
				if _, err := pool.Exec(ctx,
					`INSERT INTO bills (household_id, name, amount, frequency, due_date, paid_date, account_number, created_at)
					 VALUES ($1, 'Insurance', 42.50, $2, NULLIF($3, '')::date, NULLIF($4, '')::date, 'legacy-4471', $5)`,
					hh, tc.frequency, tc.dueDate, tc.paidDate, day("2026-11-18")); err != nil {
					t.Fatalf("insert bill: %v", err)
				}
				got, err := insertDueNotifications(ctx, pool, day("2026-11-18"))
				if err != nil {
					t.Fatalf("insertDueNotifications: %v", err)
				}
				if (tc.want != "") != (len(got) == 1) || len(got) > 1 {
					t.Fatalf("got %d notifications, want %q: %+v", len(got), tc.want, got)
				}
				if tc.want != "" && got[0].Body != tc.want {
					t.Errorf("body = %q, want %q", got[0].Body, tc.want)
				}
			})
		}
	})

	t.Run("bills: today in the household timezone", func(t *testing.T) {
		// 03:00 UTC Nov 10 is 21:00 Nov 9 in Chicago.
		now := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
		tests := []struct {
			name   string
			tz     string
			dueDay int
			want   bool
		}{
			{"UTC: Nov 13 is in 3 days", "UTC", 13, true},
			{"Chicago: Nov 13 is in 4 days", "America/Chicago", 13, false},
			{"UTC: Nov 9 was yesterday", "UTC", 9, false},
			{"Chicago: Nov 9 is today", "America/Chicago", 9, true},
			{"unknown timezone falls back to UTC", "Not/AZone", 13, true},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				if _, err := pool.Exec(ctx, `TRUNCATE notifications, bills, maintenance_tasks CASCADE`); err != nil {
					t.Fatal(err)
				}
				hh := seedHousehold(t, pool)
				if _, err := pool.Exec(ctx, `UPDATE households SET timezone = $2 WHERE id = $1`, hh, tc.tz); err != nil {
					t.Fatal(err)
				}
				seedBillWithAccount(t, pool, hh, "Gas", tc.dueDay, now)

				got, err := insertDueNotifications(ctx, pool, now)
				if err != nil {
					t.Fatalf("insertDueNotifications: %v", err)
				}
				if tc.want != (len(got) == 1) || len(got) > 1 {
					t.Fatalf("got %d notifications, want created=%v: %+v", len(got), tc.want, got)
				}
			})
		}
	})

	t.Run("maintenance: today in the household timezone", func(t *testing.T) {
		// 03:00 UTC Nov 10 is 21:00 Nov 9 in Chicago: the 7-day window is
		// Nov 9..16 there and Nov 10..17 in UTC.
		now := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
		tests := []struct {
			name string
			tz   string
			due  string
			want bool
		}{
			{"Chicago: Nov 16 is the window's last day", "America/Chicago", "2026-11-16", true},
			{"Chicago: Nov 17 is 8 days out", "America/Chicago", "2026-11-17", false},
			{"Chicago: Nov 9 is today", "America/Chicago", "2026-11-09", true},
			{"UTC: Nov 17 is the window's last day", "UTC", "2026-11-17", true},
			{"UTC: Nov 9 was yesterday", "UTC", "2026-11-09", false},
			{"UTC: Nov 18 is 8 days out", "UTC", "2026-11-18", false},
			{"Auckland (16:00 Nov 10): Nov 17 is the last day", "Pacific/Auckland", "2026-11-17", true},
			{"unknown timezone falls back to UTC: Nov 17 in", "Not/AZone", "2026-11-17", true},
			{"unknown timezone falls back to UTC: Nov 9 out", "Not/AZone", "2026-11-09", false},
			{"empty timezone falls back to UTC", "", "2026-11-17", true},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				if _, err := pool.Exec(ctx, `TRUNCATE notifications, bills, maintenance_tasks CASCADE`); err != nil {
					t.Fatal(err)
				}
				hh := seedHousehold(t, pool)
				if _, err := pool.Exec(ctx, `UPDATE households SET timezone = $2 WHERE id = $1`, hh, tc.tz); err != nil {
					t.Fatal(err)
				}
				taskID := seedTask(t, pool, hh, "Furnace", "pending", tc.due)

				got, err := insertDueNotifications(ctx, pool, now)
				if err != nil {
					t.Fatalf("insertDueNotifications: %v", err)
				}
				if tc.want != (len(got) == 1) || len(got) > 1 {
					t.Fatalf("got %d notifications, want created=%v: %+v", len(got), tc.want, got)
				}
				if !tc.want {
					return
				}
				if n := got[0]; n.Type != "maintenance_due" || n.HouseholdID != hh || n.Title != "Furnace due soon" {
					t.Errorf("unexpected notification %+v", n)
				}
				if c := notifiedEntities(t, pool, "maintenance_due")[taskID]; c != 1 {
					t.Errorf("entity_id %s has %d maintenance_due rows, want 1", taskID, c)
				}
			})
		}
	})

	t.Run("maintenance: households in different timezones in one run", func(t *testing.T) {
		ctx := context.Background()
		if _, err := pool.Exec(ctx, `TRUNCATE notifications, bills, maintenance_tasks CASCADE`); err != nil {
			t.Fatal(err)
		}
		chicago := seedHousehold(t, pool)
		utc := seedHousehold(t, pool)
		bad := seedHousehold(t, pool)
		for hh, tz := range map[string]string{chicago: "America/Chicago", utc: "UTC", bad: "Mars/Olympus"} {
			if _, err := pool.Exec(ctx, `UPDATE households SET timezone = $2 WHERE id = $1`, hh, tz); err != nil {
				t.Fatal(err)
			}
		}
		chi16 := seedTask(t, pool, chicago, "Chicago 16", "pending", "2026-11-16")
		seedTask(t, pool, chicago, "Chicago 17", "pending", "2026-11-17")
		utc17 := seedTask(t, pool, utc, "UTC 17", "pending", "2026-11-17")
		bad17 := seedTask(t, pool, bad, "Bad 17", "pending", "2026-11-17")

		now := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
		if err := generateNotifications(ctx, pool, nil, now); err != nil {
			t.Fatalf("generateNotifications: %v", err)
		}
		maint := notifiedEntities(t, pool, "maintenance_due")
		if len(maint) != 3 || maint[chi16] != 1 || maint[utc17] != 1 || maint[bad17] != 1 {
			t.Errorf("maintenance_due = %v, want one each for %s, %s, %s", maint, chi16, utc17, bad17)
		}
	})

	t.Run("dedupe by key and the maintenance window", func(t *testing.T) {
		ctx := context.Background()
		if _, err := pool.Exec(ctx, `TRUNCATE notifications, bills, maintenance_tasks CASCADE`); err != nil {
			t.Fatal(err)
		}
		hh := seedHousehold(t, pool)
		billID := seedBillWithAccount(t, pool, hh, "Water", 1, day("2026-11-29"))
		dueToday := seedTask(t, pool, hh, "Filters", "pending", "2026-11-29")
		dueIn7 := seedTask(t, pool, hh, "Gutters", "pending", "2026-12-06")
		seedTask(t, pool, hh, "Too late", "pending", "2026-12-07")
		seedTask(t, pool, hh, "Done", "done", "2026-11-30")
		seedTask(t, pool, hh, "Overdue", "pending", "2026-11-28")

		now := day("2026-11-29")
		// generateNotifications with a nil SMTP client is the loop's real entry point.
		if err := generateNotifications(ctx, pool, nil, now); err != nil {
			t.Fatalf("first run: %v", err)
		}
		if err := generateNotifications(ctx, pool, nil, now); err != nil {
			t.Fatalf("second run: %v", err)
		}
		again, err := insertDueNotifications(ctx, pool, now)
		if err != nil {
			t.Fatalf("third run: %v", err)
		}
		if len(again) != 0 {
			t.Errorf("repeat runs created %d notifications, want 0: %+v", len(again), again)
		}

		bills := notifiedEntities(t, pool, "bill_due")
		if len(bills) != 1 || bills[billID] != 1 {
			t.Errorf("bill_due = %v, want exactly one for %s", bills, billID)
		}
		maint := notifiedEntities(t, pool, "maintenance_due")
		if len(maint) != 2 || maint[dueToday] != 1 || maint[dueIn7] != 1 {
			t.Errorf("maintenance_due = %v, want one each for %s (today) and %s (+7d)", maint, dueToday, dueIn7)
		}

		// A reminder is keyed on (bill, date, attention), NOT on a rolling
		// window: the same bill in the same state for the same date is never
		// re-sent, however long ago the first one was (#5500). Time passing
		// alone must not produce a second push.
		if _, err := pool.Exec(ctx,
			`UPDATE notifications SET created_at = NOW() - INTERVAL '25 hours' WHERE entity_id = $1`, billID); err != nil {
			t.Fatal(err)
		}
		later, err := insertDueNotifications(ctx, pool, now.AddDate(0, 0, 1))
		if err != nil {
			t.Fatalf("a day later: %v", err)
		}
		for _, n := range later {
			if n.Type == "bill_due" || n.Type == "bill_digest" {
				t.Errorf("a day later produced %+v, want nothing for the same bill, date and state", n)
			}
		}

		// What does earn a new reminder is the state changing: this bill is
		// still unpaid when its date arrives, so it is now overdue and the
		// family is told again, in those words.
		overdueRun, err := insertDueNotifications(ctx, pool, day("2026-12-02"))
		if err != nil {
			t.Fatalf("after the due date: %v", err)
		}
		var overdue *notif
		for i := range overdueRun {
			if overdueRun[i].Type == "bill_due" {
				overdue = &overdueRun[i]
			}
		}
		if overdue == nil {
			t.Fatalf("after the due date got %+v, want one bill_due saying it is overdue", overdueRun)
		}
		if !strings.Contains(overdue.Title, "overdue") {
			t.Errorf("title = %q, want the overdue state in it", overdue.Title)
		}
		if n := notifiedEntities(t, pool, "bill_due")[billID]; n != 2 {
			t.Errorf("bill_due rows for %s = %d, want 2 (due_soon, then overdue), not one per run", billID, n)
		}
	})
}

// TestHouseholdMembers_SkipsDeactivated is the regression test for #4873 (code
// review #4499, Significant #3) on the email path: sendNotificationEmails
// joined users but never filtered users.active, so a deactivated member of a
// bill or maintenance notification's household still received the email.
func TestHouseholdMembers_SkipsDeactivated(t *testing.T) {
	pool := newMigratedPool(t)
	ctx := context.Background()
	hh := seedHousehold(t, pool)

	seedUser := func(email string, active bool) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (email, name, password_hash, active) VALUES ($1, $2, 'x', $3) RETURNING id::text`,
			email, email, active).Scan(&id); err != nil {
			t.Fatalf("insert user %s: %v", email, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO memberships (household_id, user_id) VALUES ($1, $2)`, hh, id); err != nil {
			t.Fatalf("membership %s: %v", email, err)
		}
		return id
	}
	seedUser("active1@example.com", true)
	inactive := seedUser("gone@example.com", false)
	seedUser("active2@example.com", true)

	// A second household whose members must not leak into the first.
	other := seedHousehold(t, pool)
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ('elsewhere@example.com', 'e', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id) SELECT $1, id FROM users WHERE email = 'elsewhere@example.com'`, other); err != nil {
		t.Fatal(err)
	}

	got, err := householdMembers(ctx, pool, hh)
	if err != nil {
		t.Fatalf("householdMembers: %v", err)
	}
	emails := make([]string, 0, len(got))
	for _, m := range got {
		emails = append(emails, m.Email)
	}
	sort.Strings(emails)
	if want := []string{"active1@example.com", "active2@example.com"}; strings.Join(emails, ",") != strings.Join(want, ",") {
		t.Fatalf("recipients = %v, want %v (deactivated %s excluded)", emails, want, inactive)
	}
}
