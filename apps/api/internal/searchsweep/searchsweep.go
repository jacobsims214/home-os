// Package searchsweep runs searchrecon on a timer inside the API process.
//
// WHY (#5298): a household_search document used to exist only if the record
// happened to be written through one of the REST handlers that calls
// search.Client.IndexDocument. Agents write through the MCP, phones write
// through CalDAV, and the worker writes files — none of those index — so
// search answered "nothing matched" for records the family could see on
// screen. Handler-level indexing cannot fix that: every writer would have to
// be taught, and the next writer would forget.
//
// This sweep makes the index a pure function of Postgres: every tick it
// re-derives every household's documents from the source of truth and deletes
// the ones whose record is gone. A missed write then costs at most one
// interval of staleness instead of being lost forever, and a fresh deployment
// (or a restored database) converges with no manual step.
package searchsweep

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/search"
	"home-os/api/internal/searchrecon"
)

// DefaultInterval is the production cadence. Five minutes keeps a document
// created by an agent findable within one coffee break while costing a
// household-sized upsert batch per tick; a family's index is hundreds of
// documents, not millions.
const DefaultInterval = 5 * time.Minute

// runTimeout bounds one pass so a hung Typesense cannot stack sweeps.
const runTimeout = 4 * time.Minute

// Job reconciles the search index with Postgres on a timer.
type Job struct {
	pool     *pgxpool.Pool
	client   *search.Client
	interval time.Duration
}

// New builds the sweep. A nil client or a non-positive interval yields a job
// whose Run returns at once, so "search not configured" needs no branch at the
// call site.
func New(pool *pgxpool.Pool, client *search.Client, interval time.Duration) *Job {
	return &Job{pool: pool, client: client, interval: interval}
}

// Run reconciles once immediately, then on every tick, until ctx is cancelled.
//
// The first pass is not delayed by an interval on purpose: it is also the
// backfill. A deployment that has just taken this change has an index that is
// missing everything the other writers created, and making the family wait
// five minutes for their own records would look exactly like the bug.
func (j *Job) Run(ctx context.Context) error {
	if j.pool == nil || j.client == nil || j.interval <= 0 {
		slog.Info("search sweep disabled")
		return nil
	}

	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	j.pass(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			j.pass(ctx)
		}
	}
}

// pass runs one reconcile. Every failure is logged and swallowed: a sweep is
// a repair, never a reason to stop the API, and the next tick retries.
func (j *Job) pass(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	start := time.Now()
	stats, err := searchrecon.Reconcile(ctx, j.pool, j.client, "")
	if err != nil {
		slog.Warn("search sweep: reconcile failed, the index keeps its previous contents",
			"error", err, "took", time.Since(start))
		return
	}
	slog.Info("search sweep: index reconciled with postgres",
		"households", stats.Households,
		"indexed", stats.Indexed,
		"pruned", stats.Pruned,
		"took", time.Since(start))
}
