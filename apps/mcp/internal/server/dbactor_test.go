package server_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

type actorSettings struct{ user, household, token, client string }

func readSettings(t *testing.T, ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) actorSettings {
	t.Helper()
	var s actorSettings
	if err := q.QueryRow(ctx, `SELECT coalesce(current_setting('app.user_id', true), ''),
	                                  coalesce(current_setting('app.household_id', true), ''),
	                                  coalesce(current_setting('app.agent_token_id', true), ''),
	                                  coalesce(current_setting('app.client_id', true), '')`).
		Scan(&s.user, &s.household, &s.token, &s.client); err != nil {
		t.Fatalf("read settings: %v", err)
	}
	return s
}

// TestPoolActorSettings drives a one-connection pool through a sequence of
// callers, so every acquisition reuses the same session: each must see
// exactly its own identity, and nothing from the caller before it.
func TestPoolActorSettings(t *testing.T) {
	base := testutil.NewPool(t) // server.NewPool: hooks installed
	cfg := base.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const (
		uid = "0b3c8a52-7f0e-4d43-9a8e-1f5d3f1a2b01"
		hid = "0b3c8a52-7f0e-4d43-9a8e-1f5d3f1a2b02"
		tid = "0b3c8a52-7f0e-4d43-9a8e-1f5d3f1a2b03"
	)
	with := func(c *auth.Claims) context.Context {
		return server.ContextWithClaims(context.Background(), c)
	}
	steps := []struct {
		name string
		ctx  context.Context
		want actorSettings
	}{
		{"no claims", context.Background(), actorSettings{}},
		{"dex session records its OAuth client", with(&auth.Claims{
			Identity: auth.Identity{ClientID: "mcp-abc"}, UserID: uid, HouseholdID: hid,
		}), actorSettings{uid, hid, "", "mcp-abc"}},
		{"agent token: token id, never a client id", with(&auth.Claims{
			Identity: auth.Identity{ClientID: "stray"}, UserID: uid, HouseholdID: hid, AgentTokenID: tid,
		}), actorSettings{uid, hid, tid, ""}},
		{"same caller again (cached)", with(&auth.Claims{
			UserID: uid, HouseholdID: hid, AgentTokenID: tid,
		}), actorSettings{uid, hid, tid, ""}},
		{"background work after a member", context.Background(), actorSettings{}},
		{"local-dev identity is anonymous", with(&auth.Claims{
			Identity: auth.Identity{ClientID: "x"}, UserID: "local-dev",
		}), actorSettings{}},
		{"bad household id is dropped", with(&auth.Claims{UserID: uid, HouseholdID: "nope"}),
			actorSettings{uid, "", "", ""}},
		{"detached context drops the actor", with(nil), actorSettings{}},
	}
	for _, st := range steps {
		if got := readSettings(t, st.ctx, pool); got != st.want {
			t.Errorf("%s: settings = %+v, want %+v", st.name, got, st.want)
		}
	}

	// Inside a transaction begun by a member, every statement carries the
	// member; the next caller still starts clean.
	mctx := with(&auth.Claims{UserID: uid, HouseholdID: hid})
	tx, err := pool.Begin(mctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := readSettings(t, mctx, tx); got.user != uid {
		t.Errorf("in tx: user = %q, want %q", got.user, uid)
	}
	if err := tx.Commit(mctx); err != nil {
		t.Fatal(err)
	}
	if got := readSettings(t, context.Background(), pool); got != (actorSettings{}) {
		t.Errorf("after tx: settings = %+v, want blank", got)
	}
}
