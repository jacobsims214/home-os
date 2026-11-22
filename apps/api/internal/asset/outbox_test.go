package asset

// #5513: an asset write must enqueue its outbox event whoever makes it.
//
// Until migration 088 the enqueue was handler code in this package's
// Create/Update. The MCP tool layer writes the assets row itself and
// enqueued nothing, so an asset created by a family agent left no outbox
// event at all. The enqueue is now the assets_outbox trigger, the one shared
// place, and the hand-written INSERTs are gone.
//
// These tests drive Repo (what the HTTP handlers call). The MCP path is
// proved in apps/mcp/internal/asset/outbox_test.go against the same trigger
// (a Go module cannot import another module's internal package).

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outboxDB connects to TEST_DATABASE_URL (all migrations applied). Skips when
// unset, as the other API integration tests do.
func outboxDB(t *testing.T) *pgxpool.Pool {
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

// assetEvents returns the events about one asset as "<type>|<name>|<category>"
// (category "null" when the payload carries JSON null), oldest first.
func assetEvents(t *testing.T, pool *pgxpool.Pool, assetID uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT type || '|' || (payload->>'name') || '|' || COALESCE(payload->>'category', 'null')
		   FROM outbox_events
		  WHERE payload->>'asset_id' = $1
		  ORDER BY created_at, id`, assetID.String())
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

// TestRepoWritesEnqueueOneOutboxEvent drives the REST repository and asserts
// exactly one asset.created and one asset.updated, carrying the asset's
// values.
func TestRepoWritesEnqueueOneOutboxEvent(t *testing.T) {
	pool := outboxDB(t)
	ctx := context.Background()
	repo := NewRepo(pool)

	var hid uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Asset outbox') RETURNING id`).Scan(&hid))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hid)
	})

	category := "appliance"
	created, err := repo.Create(ctx, &Asset{HouseholdID: hid, Name: "Dishwasher", Category: &category})
	require.NoError(t, err)
	require.NotNil(t, created)

	assert.Equal(t, []string{"asset.created|Dishwasher|appliance"}, assetEvents(t, pool, created.ID),
		"Repo.Create must enqueue exactly one asset.created")

	updated, err := repo.Update(ctx, created.ID, hid, &Asset{Name: "Kitchen dishwasher"})
	require.NoError(t, err)
	require.NotNil(t, updated)

	assert.Equal(t, []string{
		"asset.created|Dishwasher|appliance",
		"asset.updated|Kitchen dishwasher|appliance",
	}, assetEvents(t, pool, created.ID),
		"Repo.Update must enqueue exactly one asset.updated and must not lose the create event")

	// The payload's household_id is the asset's own, so a consumer polling one
	// family never picks up another's asset.
	var payloadHousehold string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT payload->>'household_id' FROM outbox_events WHERE payload->>'asset_id' = $1 LIMIT 1`,
		created.ID.String()).Scan(&payloadHousehold))
	assert.Equal(t, hid.String(), payloadHousehold)

	// A rejected write enqueues nothing: the trigger is in the same
	// transaction as the row, so a rolled-back insert leaves no event behind.
	var before int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE household_id = $1`, hid).Scan(&before))

	_, err = pool.Exec(ctx, `INSERT INTO assets (household_id, name) VALUES ($1, NULL)`, hid)
	require.Error(t, err, "a NULL name must still be rejected")
	var after int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE household_id = $1`, hid).Scan(&after))
	assert.Equal(t, before, after, "a failed asset insert must not leave an outbox event")
}

// TestAssetDeleteEnqueuesNothing: no asset.deleted has ever existed, and 088
// does not invent one. What a delete DOES emit is the bill.updated for every
// bill whose asset_id went NULL -- proven in the bill package, since that is
// where the bill lives.
func TestAssetDeleteEnqueuesNothing(t *testing.T) {
	pool := outboxDB(t)
	ctx := context.Background()
	repo := NewRepo(pool)

	var hid uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Asset delete outbox') RETURNING id`).Scan(&hid))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hid)
	})

	category := "appliance"
	created, err := repo.Create(ctx, &Asset{HouseholdID: hid, Name: "Toaster", Category: &category})
	require.NoError(t, err)
	require.NotNil(t, created)

	require.NoError(t, repo.Delete(ctx, created.ID, hid))

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox_events WHERE type = 'asset.deleted' AND payload->>'asset_id' = $1`,
		created.ID.String()).Scan(&n))
	assert.Equal(t, 0, n, "deleting an asset must not emit asset.deleted (that event has never existed)")
}
