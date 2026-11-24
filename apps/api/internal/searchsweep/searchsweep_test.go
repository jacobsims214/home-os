package searchsweep_test

// #5298: the API's sweep is what makes the index converge in production, so it
// gets its own test rather than riding on the reconciler's.
//
// What it pins down:
//   - the FIRST pass runs immediately (the backfill). A deployment that takes
//     this change has an index missing everything the MCP, CalDAV and the
//     worker wrote; making the family wait one interval would look exactly
//     like the bug being fixed.
//   - a bill that exists only in Postgres becomes searchable with no write
//     through the API, which is the whole failure the R2 war game hit.
//
// Needs TEST_DATABASE_URL and TYPESENSE_TEST_ADDR/TYPESENSE_TEST_KEY.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/search"
	"home-os/api/internal/search/searchtest"
	"home-os/api/internal/searchsweep"
)

func TestSweepBackfillsImmediatelyIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	client, _ := searchtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	require.NoError(t, client.InitCollection(ctx))

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var hh uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name, timezone) VALUES ('Sweep test', 'America/New_York') RETURNING id`).Scan(&hh))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hh)
	})

	// A bill written straight to Postgres, as the MCP's bill_create does: no
	// REST handler runs, so nothing indexes it.
	_, err = pool.Exec(ctx,
		`INSERT INTO bills (household_id, name, category, amount, due_day, frequency)
		 VALUES ($1, 'Daycare', 'Childcare', 980.00, 5, 'monthly')`, hh)
	require.NoError(t, err)

	// "Nothing matched" before the sweep: this is the bug, reproduced.
	before, err := client.Search(ctx, hh.String(), "daycare", search.SearchFilters{})
	require.NoError(t, err)
	require.Empty(t, before, "the record is not in the index before the sweep")

	// A long interval: if the first pass were not immediate, this test would
	// have to wait for it, and it would fail.
	sweepCtx, stopSweep := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = searchsweep.New(pool, client, time.Hour).Run(sweepCtx)
	}()

	// The sweep reconciles on start; wait (bounded) for the bill to appear.
	deadline := time.Now().Add(60 * time.Second)
	var after []search.SearchResult
	for time.Now().Before(deadline) {
		after, err = client.Search(ctx, hh.String(), "daycare", search.SearchFilters{})
		require.NoError(t, err)
		if len(after) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	stopSweep()
	<-done

	require.NotEmpty(t, after, "the sweep's first pass must backfill without waiting for an interval")
	assert.Equal(t, "bill", after[0].EntityType)
	assert.Equal(t, "Daycare", after[0].Title)
}
