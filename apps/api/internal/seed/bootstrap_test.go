package seed_test

import (
	"context"
	"os"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// starterTemplates returns name -> item count of a household's template lists.
func starterTemplates(t *testing.T, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, hhID string) map[string]int {
	t.Helper()
	rows, err := q.Query(context.Background(),
		`SELECT l.name, count(i.id) FROM lists l
		 LEFT JOIN list_items i ON i.list_id = l.id
		 WHERE l.household_id = $1 AND l.is_template
		 GROUP BY l.id, l.name`, hhID)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		require.NoError(t, rows.Scan(&name, &n))
		out[name] += n
	}
	require.NoError(t, rows.Err())
	return out
}

func newHousehold(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO households (name) VALUES ('Starter Home') RETURNING id`).Scan(&id))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, id)
	})
	return id
}

func ensureStarter(t *testing.T, pool *pgxpool.Pool, hhID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	require.NoError(t, seed.EnsureStarterLists(ctx, tx, hhID))
	require.NoError(t, tx.Commit(ctx))
}

var wantStarterNames = []string{
	"Baby & toddler travel", "Beach trip", "Grocery staples",
	"Ski trip", "Weekend trip packing", "Weekly chores",
}

func names(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestEnsureStarterLists_SixTemplatesIdempotent(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	hhID := newHousehold(t, pool)

	ensureStarter(t, pool, hhID)
	first := starterTemplates(t, pool, hhID)
	assert.Equal(t, wantStarterNames, names(first))
	for name, n := range first {
		assert.Greater(t, n, 5, name)
	}

	// Items are unchecked, ordered 0..n-1 and every one has a section.
	var bad int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM list_items WHERE household_id = $1
		   AND (checked OR section IS NULL OR position <> floor(position))`, hhID).Scan(&bad))
	assert.Zero(t, bad)

	// Re-run: nothing duplicated.
	ensureStarter(t, pool, hhID)
	assert.Equal(t, first, starterTemplates(t, pool, hhID))

	// Lost settings marker: the deterministic ids still prevent duplicates.
	_, err := pool.Exec(ctx, `UPDATE households SET settings = '{}' WHERE id = $1`, hhID)
	require.NoError(t, err)
	ensureStarter(t, pool, hhID)
	assert.Equal(t, first, starterTemplates(t, pool, hhID))
}

func TestEnsureStarterLists_DeletedOrRenamedNotRecreated(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	hhID := newHousehold(t, pool)
	ensureStarter(t, pool, hhID)

	_, err := pool.Exec(ctx, `DELETE FROM lists WHERE household_id = $1 AND name = 'Ski trip'`, hhID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`UPDATE lists SET name = 'Our groceries' WHERE household_id = $1 AND name = 'Grocery staples'`, hhID)
	require.NoError(t, err)

	ensureStarter(t, pool, hhID)
	require.NoError(t, seed.BackfillStarterLists(ctx, pool))
	got := names(starterTemplates(t, pool, hhID))
	assert.NotContains(t, got, "Ski trip")
	assert.NotContains(t, got, "Grocery staples")
	assert.Contains(t, got, "Our groceries")
	assert.Len(t, got, 5)
}

func TestBackfillStarterLists_ExistingHouseholds(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	a := newHousehold(t, pool)
	b := newHousehold(t, pool)

	// b already has one template seeded (an older starter set).
	_, err := pool.Exec(ctx,
		`UPDATE households SET settings = '{"seeded_list_templates":["grocery-staples"]}' WHERE id = $1`, b)
	require.NoError(t, err)

	require.NoError(t, seed.BackfillStarterLists(ctx, pool))
	assert.Equal(t, wantStarterNames, names(starterTemplates(t, pool, a)))
	assert.Len(t, starterTemplates(t, pool, b), 5) // grocery staples was "already given"

	// Second run (restart) writes nothing.
	before := starterTemplates(t, pool, a)
	require.NoError(t, seed.BackfillStarterLists(ctx, pool))
	assert.Equal(t, before, starterTemplates(t, pool, a))
}

func TestBootstrapAdmin_SeedsStarterLists(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	email := "lists-" + uuid.NewString() + "@example.test"
	cfg := &config.Config{AdminEmail: email, AdminPassword: "pw-123456", AdminName: "Lists"}

	require.NoError(t, seed.BootstrapAdmin(ctx, pool, cfg, nil))
	require.NoError(t, seed.BootstrapAdmin(ctx, pool, cfg, nil))
	var hhID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT m.household_id FROM memberships m JOIN users u ON u.id = m.user_id
		 WHERE u.email = $1`, email).Scan(&hhID))
	assert.Equal(t, wantStarterNames, names(starterTemplates(t, pool, hhID)))
}

func TestStarterListID_Stable(t *testing.T) {
	hh := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	assert.Equal(t, seed.StarterListID(hh, "beach-trip"), seed.StarterListID(hh, "beach-trip"))
	assert.NotEqual(t, seed.StarterListID(hh, "beach-trip"), seed.StarterListID(hh, "ski-trip"))
	assert.Len(t, seed.StarterListKeys(), 6)
}
