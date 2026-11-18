package db

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// setActorSQL writes all three actor settings at session level. The API never
// acts through an agent token (RequireAuth refuses them), so
// app.agent_token_id is always cleared.
//
// Session level (is_local = false) is deliberate: the hook runs while the
// connection is idle, outside any transaction, so a transaction-local setting
// would end with this very statement. Leaking is prevented instead by
// overwriting the settings on every acquisition.
const setActorSQL = `SELECT set_config('app.user_id', $1, false),
       set_config('app.household_id', $2, false),
       set_config('app.agent_token_id', '', false)`

// NewPool creates a pgx pool for dsn whose connections carry the actor of
// the acquiring context (see the package doc).
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	ConfigureActor(cfg)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	return pool, nil
}

// connActor is what a connection's settings were last set to.
type connActor struct{ userID, householdID string }

// ConfigureActor installs the actor hooks on cfg, chaining any AfterConnect,
// PrepareConn and BeforeClose hooks already set.
//
// To spare a round trip on most acquisitions, the hook remembers what each
// connection's settings hold and skips the statement when they already match.
// Nothing else in the API changes these settings at session level, which is
// what keeps that cache true.
func ConfigureActor(cfg *pgxpool.Config) {
	var state sync.Map // *pgx.Conn -> connActor

	prevPrepare := cfg.PrepareConn
	if prevPrepare == nil && cfg.BeforeAcquire != nil { //nolint:staticcheck // honour the deprecated hook
		before := cfg.BeforeAcquire //nolint:staticcheck
		prevPrepare = func(ctx context.Context, c *pgx.Conn) (bool, error) { return before(ctx, c), nil }
	}
	cfg.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		if prevPrepare != nil {
			if ok, err := prevPrepare(ctx, conn); !ok || err != nil {
				return ok, err
			}
		}
		uid, hid := settings(ctx)
		want := connActor{userID: uid, householdID: hid}
		if got, ok := state.Load(conn); ok && got.(connActor) == want {
			return true, nil
		}
		if _, err := conn.Exec(ctx, setActorSQL, uid, hid); err != nil {
			// The settings may or may not have changed: forget the
			// connection and have the pool destroy it.
			state.Delete(conn)
			return false, fmt.Errorf("set database actor: %w", err)
		}
		state.Store(conn, want)
		return true, nil
	}

	// A new session has none of the settings, which reads the same as
	// blank: record that so its first anonymous acquisition skips the
	// statement.
	prevConnect := cfg.AfterConnect
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if prevConnect != nil {
			if err := prevConnect(ctx, conn); err != nil {
				return err
			}
		}
		state.Store(conn, connActor{})
		return nil
	}

	prevClose := cfg.BeforeClose
	cfg.BeforeClose = func(conn *pgx.Conn) {
		state.Delete(conn)
		if prevClose != nil {
			prevClose(conn)
		}
	}
}
