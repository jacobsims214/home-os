package server

// Write attribution (#4491).
//
// Migration 050 (authorship) fills created_by/updated_by and activity_log's
// actor from the settings app.user_id, app.household_id and
// app.agent_token_id; migration 054 notifies a record's creator when another
// member changes it; migration 059 records the OAuth client in
// activity_log.actor_client_id from app.client_id. All of them read those
// settings inside Postgres, so a write that arrives without them is
// anonymous.
//
// Rather than touch every tool, the pool sets them itself: NewPool installs a
// PrepareConn hook that runs on every connection acquisition (pool.Exec,
// Query, QueryRow, Begin, BeginTx and Acquire all acquire) and writes the
// caller identity carried by the acquiring context, the *auth.Claims that
// AuthMiddleware put there (ClaimsFromContext). A context without claims
// (startup, background work, the auth lookup itself) writes blanks, which
// the triggers read as NULL ("the system"). Every acquisition overwrites all
// four settings, so a pooled connection never carries one caller's identity
// into another caller's writes.
//
// This mirrors apps/api/internal/db (#4492). The settings are session level
// on purpose: the hook runs while the connection is idle, outside any
// transaction, so a transaction-local setting would end with the hook's own
// statement. Never change these settings at session level anywhere else in
// apps/mcp, or the per-connection cache below goes stale; set_config(...,
// true) inside a transaction is fine.

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
)

// setActorSQL writes the four actor settings at session level.
const setActorSQL = `SELECT set_config('app.user_id', $1, false),
       set_config('app.household_id', $2, false),
       set_config('app.agent_token_id', $3, false),
       set_config('app.client_id', $4, false)`

// maxClientIDLen bounds app.client_id (activity_log.actor_client_id is
// VARCHAR(200)). Client ids are short (home-os-mcp, mcp-<uuid>).
const maxClientIDLen = 200

// dbActor is what one connection's settings hold. The zero value is
// "anonymous" (all blank).
type dbActor struct{ userID, householdID, agentTokenID, clientID string }

// actorFromClaims converts claims to the settings values. Ids that are not
// UUIDs (the auth-disabled "local-dev" identity) are dropped, so the write is
// recorded as anonymous rather than failing; with no valid user id nothing
// is attributed at all.
func actorFromClaims(c *auth.Claims) dbActor {
	if c == nil {
		return dbActor{}
	}
	uid, err := uuid.Parse(c.UserID)
	if err != nil || uid == uuid.Nil {
		return dbActor{}
	}
	a := dbActor{userID: uid.String()}
	if hid, err := uuid.Parse(c.HouseholdID); err == nil && hid != uuid.Nil {
		a.householdID = hid.String()
	}
	if tid, err := uuid.Parse(c.AgentTokenID); err == nil && tid != uuid.Nil {
		a.agentTokenID = tid.String()
	} else if c.AgentTokenID == "" {
		// The OAuth client only names interactive (Dex) sessions; an
		// agent token is identified by its id instead.
		a.clientID = c.ClientID
		if len(a.clientID) > maxClientIDLen {
			a.clientID = a.clientID[:maxClientIDLen]
		}
	}
	return a
}

// NewPool creates a pgx pool for dsn whose connections carry the identity of
// the acquiring context's caller (see ConfigureActor).
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

// ConfigureActor installs the actor hooks on cfg, chaining any AfterConnect,
// PrepareConn (or deprecated BeforeAcquire) and BeforeClose hooks already
// set.
//
// To spare a round trip on most acquisitions, the hook remembers what each
// connection's settings hold and skips the statement when they already
// match.
func ConfigureActor(cfg *pgxpool.Config) {
	var state sync.Map // *pgx.Conn -> dbActor

	prevPrepare := cfg.PrepareConn
	if prevPrepare == nil && cfg.BeforeAcquire != nil { //nolint:staticcheck // honour the deprecated hook
		before := cfg.BeforeAcquire //nolint:staticcheck
		prevPrepare = func(ctx context.Context, c *pgx.Conn) (bool, error) { return before(ctx, c), nil }
		cfg.BeforeAcquire = nil //nolint:staticcheck // replaced by the chained PrepareConn
	}
	cfg.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		if prevPrepare != nil {
			if ok, err := prevPrepare(ctx, conn); !ok || err != nil {
				return ok, err
			}
		}
		want := actorFromClaims(ClaimsFromContext(ctx))
		if got, ok := state.Load(conn); ok && got.(dbActor) == want {
			return true, nil
		}
		if _, err := conn.Exec(ctx, setActorSQL, want.userID, want.householdID, want.agentTokenID, want.clientID); err != nil {
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
		state.Store(conn, dbActor{})
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
