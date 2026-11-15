package seed_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/config"
	"home-os/api/internal/seed"
)

// testDB connects to TEST_DATABASE_URL (a database with every migration in
// packages/db/migrations applied; a fresh one needs CREATE ROLE app first).
func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func isInstanceAdmin(t *testing.T, pool *pgxpool.Pool, email string) bool {
	t.Helper()
	var v bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT is_instance_admin FROM users WHERE email = $1`, email).Scan(&v))
	return v
}

func calendarTypes(t *testing.T, pool *pgxpool.Pool, email string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT c.type::text FROM calendars c
		 JOIN memberships m ON m.household_id = c.household_id AND m.role = 'owner'
		 JOIN users u ON u.id = m.user_id
		 WHERE u.email = $1 ORDER BY c.type::text`, email)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestBootstrapAdmin_NewAdminIsInstanceAdmin(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	email := "boot-" + uuid.NewString() + "@example.test"
	cfg := &config.Config{AdminEmail: email, AdminPassword: "pw-123456", AdminName: "Boot"}

	require.NoError(t, seed.BootstrapAdmin(ctx, pool, cfg, nil))
	assert.True(t, isInstanceAdmin(t, pool, email))
	assert.Equal(t, []string{"bills", "family", "maintenance"}, calendarTypes(t, pool, email))

	// Restart: still admin, no duplicate calendars.
	require.NoError(t, seed.BootstrapAdmin(ctx, pool, cfg, nil))
	assert.True(t, isInstanceAdmin(t, pool, email))
	assert.Equal(t, []string{"bills", "family", "maintenance"}, calendarTypes(t, pool, email))
}

func TestBootstrapAdmin_ExistingAdminGetsFlagged(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	email := "pre-" + uuid.NewString() + "@example.test"

	// An admin created before migration 037: plain user, owner of a household
	// with no calendars.
	var userID, hhID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, 'Pre', 'x') RETURNING id`,
		email).Scan(&userID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Pre Home') RETURNING id`).Scan(&hhID))
	_, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'owner')`, hhID, userID)
	require.NoError(t, err)
	require.False(t, isInstanceAdmin(t, pool, email))

	cfg := &config.Config{AdminEmail: email, AdminPassword: "pw-123456", AdminName: "Pre"}
	require.NoError(t, seed.BootstrapAdmin(ctx, pool, cfg, nil))
	assert.True(t, isInstanceAdmin(t, pool, email))
	assert.Equal(t, []string{"bills", "family", "maintenance"}, calendarTypes(t, pool, email))
}

func TestEnsureDefaultCalendars_InTx(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	var hhID string
	require.NoError(t, tx.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Tx Home') RETURNING id`).Scan(&hhID))
	require.NoError(t, seed.EnsureDefaultCalendars(ctx, tx, hhID))
	require.NoError(t, seed.EnsureDefaultCalendars(ctx, tx, hhID)) // idempotent

	var n int
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT count(*) FROM calendars WHERE household_id = $1`, hhID).Scan(&n))
	assert.Equal(t, 3, n)
}
