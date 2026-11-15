package auth

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolverTestDB connects to TEST_DATABASE_URL (all migrations applied,
// including 033_users_dex_sub). Skips when unset.
func resolverTestDB(t *testing.T) *pgxpool.Pool {
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

func insertUser(t *testing.T, pool *pgxpool.Pool, email string, dexSub *string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, 'Test', 'x', $2) RETURNING id::text`,
		email, dexSub).Scan(&id))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	return id
}

func insertMembership(t *testing.T, pool *pgxpool.Pool, userID, role string, createdAt time.Time) string {
	t.Helper()
	ctx := context.Background()
	var hh string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('HH') RETURNING id::text`).Scan(&hh))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hh) })
	_, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role, created_at) VALUES ($1, $2, $3, $4)`,
		hh, userID, role, createdAt)
	require.NoError(t, err)
	return hh
}

func dexSubOf(t *testing.T, pool *pgxpool.Pool, userID string) *string {
	t.Helper()
	var s *string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT dex_sub FROM users WHERE id = $1`, userID).Scan(&s))
	return s
}

func uniq(prefix string) string { return prefix + "-" + uuid.NewString()[:8] }

func TestPGMemberResolver_Resolve(t *testing.T) {
	pool := resolverTestDB(t)
	r := NewMemberResolver(pool)
	ctx := context.Background()
	now := time.Now()

	t.Run("by dex_sub", func(t *testing.T) {
		sub := uniq("CiQsub")
		uid := insertUser(t, pool, uniq("sub")+"@example.com", &sub)
		hh := insertMembership(t, pool, uid, "owner", now)

		m, err := r.Resolve(ctx, sub, "unrelated@example.com")
		require.NoError(t, err)
		assert.Equal(t, ResolvedMember{UserID: uid, HouseholdID: hh, Role: "owner"}, *m)
	})

	t.Run("email fallback backfills dex_sub, case-insensitive", func(t *testing.T) {
		email := uniq("mail") + "@example.com"
		uid := insertUser(t, pool, email, nil)
		hh := insertMembership(t, pool, uid, "family_member", now)
		sub := uniq("CiQnew")

		m, err := r.Resolve(ctx, sub, "  "+upper(email)+" ")
		require.NoError(t, err)
		assert.Equal(t, uid, m.UserID)
		assert.Equal(t, hh, m.HouseholdID)
		assert.Equal(t, "family_member", m.Role)

		got := dexSubOf(t, pool, uid)
		require.NotNil(t, got)
		assert.Equal(t, sub, *got)

		// Second call resolves via dex_sub even with a different email.
		m2, err := r.Resolve(ctx, sub, "")
		require.NoError(t, err)
		assert.Equal(t, uid, m2.UserID)
	})

	t.Run("email match does not overwrite an existing different dex_sub", func(t *testing.T) {
		email := uniq("keep") + "@example.com"
		old := uniq("CiQold")
		uid := insertUser(t, pool, email, &old)
		insertMembership(t, pool, uid, "viewer", now)

		m, err := r.Resolve(ctx, uniq("CiQother"), email)
		require.NoError(t, err)
		assert.Equal(t, uid, m.UserID)
		assert.Equal(t, old, *dexSubOf(t, pool, uid))
	})

	t.Run("oldest membership wins", func(t *testing.T) {
		sub := uniq("CiQmulti")
		uid := insertUser(t, pool, uniq("multi")+"@example.com", &sub)
		insertMembership(t, pool, uid, "viewer", now)
		oldest := insertMembership(t, pool, uid, "owner", now.Add(-48*time.Hour))
		insertMembership(t, pool, uid, "family_manager", now.Add(-time.Hour))

		for i := 0; i < 3; i++ {
			m, err := r.Resolve(ctx, sub, "")
			require.NoError(t, err)
			assert.Equal(t, oldest, m.HouseholdID)
			assert.Equal(t, "owner", m.Role)
		}
	})

	t.Run("user without membership", func(t *testing.T) {
		email := uniq("lonely") + "@example.com"
		uid := insertUser(t, pool, email, nil)
		_, err := r.Resolve(ctx, uniq("CiQlonely"), email)
		assert.ErrorIs(t, err, ErrNoMembership)
		// Backfill still happens: the identity is known even if not a member.
		assert.NotNil(t, dexSubOf(t, pool, uid))
	})

	t.Run("unknown identity", func(t *testing.T) {
		_, err := r.Resolve(ctx, uniq("CiQnobody"), uniq("nobody")+"@example.com")
		assert.ErrorIs(t, err, ErrNoMembership)
	})

	t.Run("unknown sub and empty email", func(t *testing.T) {
		_, err := r.Resolve(ctx, uniq("CiQnoemail"), "")
		assert.ErrorIs(t, err, ErrNoMembership)
	})
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}
