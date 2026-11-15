package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// newMigratedPool starts a throwaway Postgres 16 container (random host port),
// applies every packages/db/migrations/*.up.sql, and returns a pool to it.
// The container is terminated when t finishes. Requires Docker.
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
				billID := seedBill(t, pool, hh, "Power", tc.dueDay)

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

	t.Run("dedupe within 24h and maintenance window", func(t *testing.T) {
		ctx := context.Background()
		if _, err := pool.Exec(ctx, `TRUNCATE notifications, bills, maintenance_tasks CASCADE`); err != nil {
			t.Fatal(err)
		}
		hh := seedHousehold(t, pool)
		billID := seedBill(t, pool, hh, "Water", 1)
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

		// Once the earlier notification is older than 24h, a new one is allowed.
		if _, err := pool.Exec(ctx,
			`UPDATE notifications SET created_at = NOW() - INTERVAL '25 hours' WHERE entity_id = $1`, billID); err != nil {
			t.Fatal(err)
		}
		later, err := insertDueNotifications(ctx, pool, now)
		if err != nil {
			t.Fatalf("after 24h: %v", err)
		}
		if len(later) != 1 || later[0].Type != "bill_due" {
			t.Errorf("after 24h got %+v, want one new bill_due", later)
		}
	})
}
