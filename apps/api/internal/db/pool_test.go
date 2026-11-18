package db_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/db"
)

func TestWithActorIDs(t *testing.T) {
	uid, hid := uuid.New(), uuid.New()
	cases := []struct {
		name    string
		user    string
		hh      string
		wantOK  bool
		wantHID uuid.UUID
	}{
		{name: "user and household", user: uid.String(), hh: hid.String(), wantOK: true, wantHID: hid},
		{name: "user only", user: uid.String(), wantOK: true},
		{name: "bad household dropped", user: uid.String(), hh: "x", wantOK: true},
		{name: "local-dev identity is anonymous", user: "local-dev", hh: hid.String()},
		{name: "nil uuid is anonymous", user: uuid.Nil.String()},
		{name: "empty", user: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, ok := db.ActorFromContext(db.WithActorIDs(context.Background(), tc.user, tc.hh))
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, uid, a.UserID)
				assert.Equal(t, tc.wantHID, a.HouseholdID)
			}
		})
	}
	ctx := db.WithoutActor(db.WithActorIDs(context.Background(), uid.String(), ""))
	_, ok := db.ActorFromContext(ctx)
	assert.False(t, ok, "WithoutActor clears the actor")
}

// testPool returns a db.NewPool pool limited to one connection, so every
// acquisition reuses the same session and a leaked setting would show.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.MaxConns = 1
	db.ConfigureActor(cfg)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func currentSettings(t *testing.T, ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) interface{ Scan(...any) error }
}) (string, string, string) {
	t.Helper()
	var u, h, a string
	require.NoError(t, q.QueryRow(ctx, `SELECT coalesce(current_setting('app.user_id', true), ''),
		coalesce(current_setting('app.household_id', true), ''),
		coalesce(current_setting('app.agent_token_id', true), '')`).Scan(&u, &h, &a))
	return u, h, a
}

type poolQ struct{ p *pgxpool.Pool }

func (q poolQ) QueryRow(ctx context.Context, sql string, args ...any) interface{ Scan(...any) error } {
	return q.p.QueryRow(ctx, sql, args...)
}

func TestPoolSetsActorPerAcquisition(t *testing.T) {
	pool := testPool(t)
	q := poolQ{pool}
	bg := context.Background()
	alice := db.Actor{UserID: uuid.New(), HouseholdID: uuid.New()}
	bob := db.Actor{UserID: uuid.New()}

	u, h, a := currentSettings(t, bg, q)
	assert.Equal(t, [3]string{"", "", ""}, [3]string{u, h, a}, "fresh connection is anonymous")

	u, h, _ = currentSettings(t, db.WithActor(bg, alice), q)
	assert.Equal(t, alice.UserID.String(), u)
	assert.Equal(t, alice.HouseholdID.String(), h)

	// Same (only) connection, next caller without an actor: nothing leaks.
	u, h, a = currentSettings(t, bg, q)
	assert.Equal(t, [3]string{"", "", ""}, [3]string{u, h, a}, "background acquisition after a request is anonymous")

	u, h, _ = currentSettings(t, db.WithActor(bg, bob), q)
	assert.Equal(t, bob.UserID.String(), u)
	assert.Empty(t, h, "no household set")

	// An agent token set at session level outside the hook is cleared the
	// next time the actor changes.
	_, err := pool.Exec(bg, `SELECT set_config('app.agent_token_id', $1, false)`, uuid.NewString())
	require.NoError(t, err)
	_, _, a = currentSettings(t, db.WithActor(bg, alice), q)
	assert.Empty(t, a)

	// Transactions see the actor of the context that began them.
	tx, err := pool.Begin(db.WithActor(bg, alice))
	require.NoError(t, err)
	var inTx string
	require.NoError(t, tx.QueryRow(bg, `SELECT current_setting('app.user_id', true)`).Scan(&inTx))
	assert.Equal(t, alice.UserID.String(), inTx)
	require.NoError(t, tx.Rollback(bg))
}
