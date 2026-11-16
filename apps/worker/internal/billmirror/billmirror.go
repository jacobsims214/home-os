// Package billmirror runs the Bills-calendar backfill (#4716): every bill
// with a usable schedule, in a household with a Bills calendar, gets its
// mirror event (home-os/billcal) if it has none. It runs at worker startup
// and then every interval, so bills made before #4485 get their event once,
// and so do bills written by paths that don't mirror (demo seeds, SQL) or
// households that add a Bills calendar later. It only creates missing
// events: existing mirror events are kept by the API and MCP writers, and
// events people made are never read or changed.
package billmirror

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/billcal"
)

// DefaultInterval is how often the backfill runs after the startup run.
const DefaultInterval = time.Hour

// Job is the periodic backfill.
type Job struct {
	pool     *pgxpool.Pool
	interval time.Duration
}

// New returns a Job on pool; interval <= 0 means DefaultInterval.
func New(pool *pgxpool.Pool, interval time.Duration) *Job {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Job{pool: pool, interval: interval}
}

// Run backfills now and then every interval until ctx is cancelled, then
// returns ctx.Err(). A failed run is logged and retried on the next tick.
func (j *Job) Run(ctx context.Context) error {
	slog.Info("bill calendar backfill started", "interval", j.interval)
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		if n, err := billcal.Backfill(ctx, j.pool); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Error("bill calendar backfill failed", "error", err)
		} else if n > 0 {
			slog.Info("bill calendar events created", "count", n)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
