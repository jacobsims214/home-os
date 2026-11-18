// Package testutil provides test helpers for the MCP server: a real,
// migrated Postgres (via testcontainers-go) and an authenticated household
// context that tool handlers accept.
//
// One Postgres 16 container is started per test binary (per package) on first
// use. Every migration in packages/db/migrations/*.up.sql is applied once to a
// template database, and each NewPool call gets its own fresh database cloned
// from that template, so tests are isolated and fast. The container is removed
// by the testcontainers reaper when the test process exits. Container start
// is retried with backoff because parallel packages race on the shared
// reaper (see startPostgres), so plain `go test ./...` is safe.
//
// Requires a running Docker daemon.
package testutil

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
)

const (
	postgresImage = "postgres:16-alpine"
	templateDB    = "homeos_template"

	// MigrationsDirEnv overrides where migrations are read from.
	MigrationsDirEnv = "HOMEOS_MIGRATIONS_DIR"
)

var (
	setupOnce sync.Once
	setupErr  error
	// adminConnStr points at the container's default "postgres" database.
	adminConnStr string
	dbCounter    atomic.Int64
)

// NewPool returns a pool connected to a fresh database with every migration
// applied. The pool is closed and the database dropped when t finishes.
func NewPool(t testing.TB) *pgxpool.Pool {
	t.Helper()

	setupOnce.Do(func() { setupErr = setup() })
	if setupErr != nil {
		t.Fatalf("testutil: postgres setup: %v", setupErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	name := fmt.Sprintf("homeos_test_%d_%d", os.Getpid(), dbCounter.Add(1))
	if err := adminExec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateDB)); err != nil {
		t.Fatalf("testutil: create database %s: %v", name, err)
	}

	// server.NewPool, as in production: writes made with a context from
	// SeedHousehold (or server.ContextWithClaims) are attributed to that
	// member by the authorship triggers.
	pool, err := server.NewPool(ctx, withDatabase(adminConnStr, name))
	if err != nil {
		t.Fatalf("testutil: connect %s: %v", name, err)
	}

	t.Cleanup(func() {
		pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = adminExec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name))
	})

	return pool
}

// Household is the seeded household, user and owner membership.
type Household struct {
	HouseholdID uuid.UUID
	UserID      uuid.UUID
	Email       string
	Claims      *auth.Claims
}

// SeedHousehold inserts a household, a user and an owner membership, and
// returns them with a context carrying matching claims (the shape
// server.ClaimsFromContext reads), ready to pass to a tool handler.
func SeedHousehold(t testing.TB, pool *pgxpool.Pool) (context.Context, Household) {
	t.Helper()
	ctx := context.Background()

	suffix := uuid.NewString()[:8]
	h := Household{Email: fmt.Sprintf("owner-%s@test.homeos.local", suffix)}

	if err := pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ($1) RETURNING id`,
		"Test Household "+suffix,
	).Scan(&h.HouseholdID); err != nil {
		t.Fatalf("testutil: insert household: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		h.Email, "Test Owner", "not-a-real-hash",
	).Scan(&h.UserID); err != nil {
		t.Fatalf("testutil: insert user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'owner')`,
		h.HouseholdID, h.UserID,
	); err != nil {
		t.Fatalf("testutil: insert membership: %v", err)
	}

	h.Claims = &auth.Claims{
		Identity:    auth.Identity{Subject: h.UserID.String(), Email: h.Email, Name: "Test Owner"},
		UserID:      h.UserID.String(),
		HouseholdID: h.HouseholdID.String(),
		Role:        "owner",
	}
	return server.ContextWithClaims(ctx, h.Claims), h
}

// setup starts the container and builds the migrated template database.
func setup() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	dir, err := migrationsDir()
	if err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil {
		return fmt.Errorf("glob migrations: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no *.up.sql migrations in %s", dir)
	}
	sort.Strings(files) // zero-padded numeric prefixes sort lexically

	container, err := startPostgres(ctx)
	if err != nil {
		return fmt.Errorf("start postgres container (is Docker running?): %w", err)
	}

	templateConn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return fmt.Errorf("connection string: %w", err)
	}
	adminConnStr = withDatabase(templateConn, "postgres")

	conn, err := pgx.Connect(ctx, templateConn)
	if err != nil {
		return fmt.Errorf("connect template db: %w", err)
	}
	// Migrations GRANT to the application role (e.g. 032_loans), which the
	// real database has; create it so they apply cleanly.
	if _, err := conn.Exec(ctx, `DO $$ BEGIN
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app') THEN CREATE ROLE app; END IF;
	END $$`); err != nil {
		_ = conn.Close(ctx)
		return fmt.Errorf("create app role: %w", err)
	}
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			_ = conn.Close(ctx)
			return fmt.Errorf("read %s: %w", filepath.Base(f), err)
		}
		// No arguments: pgx uses the simple protocol, so a file may hold
		// several statements, run as one implicit transaction.
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			_ = conn.Close(ctx)
			return fmt.Errorf("apply %s: %w", filepath.Base(f), err)
		}
	}
	// The template must have no open connections when it is cloned.
	if err := conn.Close(ctx); err != nil {
		return fmt.Errorf("close template db: %w", err)
	}
	return nil
}

// startAttempts bounds how often startPostgres tries before giving up.
const startAttempts = 5

// startPostgres starts the Postgres container, retrying with backoff.
//
// `go test ./...` runs one test binary per package in parallel, and they all
// share one testcontainers session (the session ID comes from the parent
// `go test` process), so they share one Ryuk reaper container. The first
// binary creates it; a binary that looks it up before Docker has moved it
// from "created" to "running" fails with
//
//	wait for reaper <id>: unexpected container status "created"
//
// which testcontainers-go (v0.44.0) treats as permanent. A moment later the
// reaper is running and a retry reuses it, so retrying here is enough.
func startPostgres(ctx context.Context) (*tcpostgres.PostgresContainer, error) {
	delay := 500 * time.Millisecond
	var errs []error
	for attempt := 1; ; attempt++ {
		container, err := tcpostgres.Run(ctx, postgresImage,
			tcpostgres.WithDatabase(templateDB),
			tcpostgres.WithUsername("homeos"),
			tcpostgres.WithPassword("homeos"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(2*time.Minute),
			),
		)
		if err == nil {
			return container, nil
		}
		// Run can return a half-started container alongside the error.
		errs = append(errs, fmt.Errorf("attempt %d: %w", attempt, errors.Join(err, testcontainers.TerminateContainer(container))))
		if attempt == startAttempts {
			return nil, errors.Join(errs...)
		}
		// Jitter so parallel test binaries do not retry in lockstep.
		sleep := delay/2 + rand.N(delay)
		log.Printf("testutil: postgres start attempt %d/%d failed, retrying in %s: %v", attempt, startAttempts, sleep, err)
		select {
		case <-ctx.Done():
			return nil, errors.Join(append(errs, ctx.Err())...)
		case <-time.After(sleep):
		}
		delay *= 2
	}
}

// migrationsDir finds packages/db/migrations by walking up from this source
// file, unless HOMEOS_MIGRATIONS_DIR is set.
func migrationsDir() (string, error) {
	if dir := os.Getenv(MigrationsDirEnv); dir != "" {
		return dir, nil
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate testutil source; set %s", MigrationsDirEnv)
	}
	for dir := filepath.Dir(file); ; {
		candidate := filepath.Join(dir, "packages", "db", "migrations")
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("packages/db/migrations not found above %s; set %s", file, MigrationsDirEnv)
		}
		dir = parent
	}
}

func adminExec(ctx context.Context, sql string) error {
	conn, err := pgx.Connect(ctx, adminConnStr)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql)
	return err
}

// withDatabase swaps the database name in a postgres:// URL.
func withDatabase(connStr, db string) string {
	u, err := url.Parse(connStr)
	if err != nil {
		return connStr // container URLs are well-formed; connect will report it
	}
	u.Path = "/" + db
	return u.String()
}
