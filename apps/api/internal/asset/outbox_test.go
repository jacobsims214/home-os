package asset

// #5320: the REST write path (Repo.Create / Repo.Update, which the HTTP
// handlers call) must enqueue exactly one outbox event per write.
//
// Until migration 075 these two methods INSERTed into outbox_events
// themselves; the MCP tool layer did not, so agent-created assets never
// reached the outbox. The enqueue is now a trigger on assets (the one shared
// place), and the hand-written INSERTs are gone. "Exactly one" is what
// catches the other half of that bug: leaving the INSERTs in place beside the
// trigger would double every REST write.
//
// The MCP path is proved in apps/mcp/internal/asset/outbox_test.go, for the
// same trigger (a Go module cannot import another module's internal package).

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

// outboxEvents returns the events about one asset as "<type>|<name>|<category>"
// (category "null" when the payload carries JSON null), oldest first.
func outboxEvents(t *testing.T, pool *pgxpool.Pool, assetID uuid.UUID) []string {
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

// TestRepoWritesEnqueueOutbox drives the REST repository (not a trigger by
// hand) and asserts what a consumer sees: one asset.created, one
// asset.updated, carrying the asset's values.
func TestRepoWritesEnqueueOutbox(t *testing.T) {
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

	assert.Equal(t, []string{"asset.created|Dishwasher|appliance"}, outboxEvents(t, pool, created.ID),
		"Repo.Create must enqueue exactly one asset.created (a second copy beside the trigger would show here)")

	name := "Kitchen dishwasher"
	updated, err := repo.Update(ctx, created.ID, hid, &Asset{Name: name})
	require.NoError(t, err)
	require.NotNil(t, updated)

	assert.Equal(t, []string{
		"asset.created|Dishwasher|appliance",
		"asset.updated|Kitchen dishwasher|appliance",
	}, outboxEvents(t, pool, created.ID),
		"Repo.Update must enqueue exactly one asset.updated, and must not lose the create event")

	// The payload's household_id is the asset's own, so a consumer polling one
	// family never picks up another's asset.
	var payloadHousehold string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT payload->>'household_id' FROM outbox_events WHERE payload->>'asset_id' = $1 LIMIT 1`,
		created.ID.String()).Scan(&payloadHousehold))
	assert.Equal(t, hid.String(), payloadHousehold)

	// A rejected write enqueues nothing: the trigger is in the same
	// transaction as the row, so a rolled-back insert leaves no event behind.
	_, err = pool.Exec(ctx, `INSERT INTO assets (household_id, name) VALUES ($1, $2)`,
		hid, "rollback me")
	require.NoError(t, err) // control: this one does write
	var before int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE household_id = $1`, hid).Scan(&before))

	_, err = pool.Exec(ctx, `INSERT INTO assets (household_id, name) VALUES ($1, NULL)`, hid)
	require.Error(t, err, "a NULL name must still be rejected")
	var after int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE household_id = $1`, hid).Scan(&after))
	assert.Equal(t, before, after, "a failed asset insert must not leave an outbox event")
}
