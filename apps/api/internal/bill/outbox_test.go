package bill

// #5513: the REST write path must enqueue exactly one outbox event per bill
// change, and a bills UPDATE that never went through a handler must enqueue
// one too.
//
// Until migration 088 Create, Update and Delete INSERTed into outbox_events
// themselves. That is the side-effect-in-handler-code pattern #5314 found:
// it fires only when the handler runs, so assets_propagate_property() -- a
// trigger that UPDATEs bills.property_id when an asset moves -- changed
// bills with no event at all. The enqueue is now the bills_outbox trigger
// (the one shared place) and those INSERTs are gone.
//
// These tests hit Repo (what the HTTP handlers call) and raw SQL (what
// nothing calls). "Exactly one" is what catches the other half of the bug:
// leaving the INSERTs in place beside the trigger would double every REST
// write. The MCP path is proved in apps/mcp/internal/bill/outbox_test.go for
// the same trigger (a Go module cannot import another module's internal
// package).

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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

// billEvents returns the events about one bill as "<type>|<name>", oldest
// first.
func billEvents(t *testing.T, pool *pgxpool.Pool, billID uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT type || '|' || (payload->>'name')
		   FROM outbox_events
		  WHERE payload->>'id' = $1
		  ORDER BY created_at, id`, billID.String())
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

// TestRepoWritesEnqueueOneOutboxEvent drives the REST repository (not the
// trigger by hand) and asserts what a consumer sees: one bill.created, one
// bill.updated and one bill.deleted, in that order.
func TestRepoWritesEnqueueOneOutboxEvent(t *testing.T) {
	pool := outboxDB(t)
	ctx := context.Background()
	repo := NewRepo(pool)

	var hid uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Bill outbox') RETURNING id`).Scan(&hid))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hid)
	})

	var amount pgtype.Numeric
	require.NoError(t, amount.Scan("120.50"))
	created, err := repo.Create(ctx, &Bill{HouseholdID: hid, Name: "Electric", Amount: amount})
	require.NoError(t, err)
	require.NotNil(t, created)

	assert.Equal(t, []string{"bill.created|Electric"}, billEvents(t, pool, created.ID),
		"Repo.Create must enqueue exactly one bill.created (a second copy beside the trigger would show here)")

	require.NoError(t, amount.Scan("150.00"))
	created.Amount = amount
	updated, err := repo.Update(ctx, created)
	require.NoError(t, err)
	require.NotNil(t, updated)

	assert.Equal(t, []string{
		"bill.created|Electric",
		"bill.updated|Electric",
	}, billEvents(t, pool, created.ID),
		"Repo.Update must enqueue exactly one bill.updated, and must not lose the create event")

	require.NoError(t, repo.Delete(ctx, created.ID, hid))

	assert.Equal(t, []string{
		"bill.created|Electric",
		"bill.updated|Electric",
		"bill.deleted|Electric",
	}, billEvents(t, pool, created.ID),
		"Repo.Delete must enqueue exactly one bill.deleted carrying the name of the row it removed")
}

// TestBillsUpdateFromBareSQLEnqueues proves the event no longer depends on a
// handler existing: an UPDATE written straight against the table (nothing in
// apps/ issues this one) emits bill.updated. That is the "any code path"
// criterion, and it is what makes assets_propagate_property() visible.
func TestBillsUpdateFromBareSQLEnqueues(t *testing.T) {
	pool := outboxDB(t)
	ctx := context.Background()

	var hid uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Bare SQL outbox') RETURNING id`).Scan(&hid))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hid)
	})

	var billID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Insurance', 80.00) RETURNING id`,
		hid).Scan(&billID))

	// No Repo, no handler, no transaction of ours.
	_, err := pool.Exec(ctx, `UPDATE bills SET amount = 95.00 WHERE id = $1`, billID)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"bill.created|Insurance",
		"bill.updated|Insurance",
	}, billEvents(t, pool, billID),
		"a bare UPDATE must enqueue exactly one bill.updated")
}

// TestAssetPropertyMoveEnqueuesForBills is the finding that made this a bug
// rather than a gap: assets_propagate_property() (038) UPDATEs
// bills.property_id when an asset moves property. Before 088 that UPDATE went
// through no handler, so the bills changed and said nothing.
func TestAssetPropertyMoveEnqueuesForBills(t *testing.T) {
	pool := outboxDB(t)
	ctx := context.Background()

	var hid uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Propagate outbox') RETURNING id`).Scan(&hid))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hid)
	})

	var home, cabin uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO properties (household_id, name) VALUES ($1, 'Home') RETURNING id`, hid).Scan(&home))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO properties (household_id, name) VALUES ($1, 'Cabin') RETURNING id`, hid).Scan(&cabin))

	var assetID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO assets (household_id, property_id, name) VALUES ($1, $2, 'Boiler') RETURNING id`,
		hid, home).Scan(&assetID))

	var billID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO bills (household_id, property_id, asset_id, name, amount)
		 VALUES ($1, $2, $3, 'Boiler cover', 12.00) RETURNING id`,
		hid, home, assetID).Scan(&billID))

	before := len(billEvents(t, pool, billID))

	// Move the asset. Only the trigger chain reacts: no Go code runs.
	_, err := pool.Exec(ctx, `UPDATE assets SET property_id = $1 WHERE id = $2`, cabin, assetID)
	require.NoError(t, err)

	var moved uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT property_id FROM bills WHERE id = $1`, billID).Scan(&moved))
	require.Equal(t, cabin, moved, "assets_propagate_property must have moved the bill")

	events := billEvents(t, pool, billID)
	require.Len(t, events, before+1, "the moved bill must enqueue exactly one more event: %v", events)
	assert.Equal(t, "bill.updated|Boiler cover", events[len(events)-1],
		"the event for a propagated property change is bill.updated")
}

// TestBillsOutboxIsOneEventPerChange counts rows for a single logical change,
// so a trigger added beside the old INSERTs cannot pass.
func TestBillsOutboxIsOneEventPerChange(t *testing.T) {
	pool := outboxDB(t)
	ctx := context.Background()

	var hid uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Count outbox') RETURNING id`).Scan(&hid))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hid)
	})
	var billID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Gas', 60.00) RETURNING id`,
		hid).Scan(&billID))

	_, err := pool.Exec(ctx, `UPDATE bills SET notes = 'one change' WHERE id = $1`, billID)
	require.NoError(t, err)

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox_events
		  WHERE type = 'bill.updated' AND payload->>'id' = $1`, billID.String()).Scan(&n))
	assert.Equal(t, 1, n, "one bill update must emit exactly one bill.updated")
}
