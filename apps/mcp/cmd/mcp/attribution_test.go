package main

// End-to-end write attribution (#4491): tools called through the registered
// server, with no tool aware of the actor, produce rows whose created_by and
// activity_log actor are the calling member, because testutil.NewPool (like
// production) is server.NewPool.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/config"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// callTool runs one tools/call through every tool handler middleware and
// returns the structuredContent data object, failing on an isError result.
func callTool(t *testing.T, ctx context.Context, srv *server.Server, name string, args map[string]any) map[string]any {
	t.Helper()
	raw := rpc(t, ctx, srv, string(mcp.MethodToolsCall), map[string]any{"name": name, "arguments": args})
	var resp struct {
		Result struct {
			IsError           bool `json:"isError"`
			StructuredContent struct {
				Data map[string]any `json:"data"`
			} `json:"structuredContent"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("%s: decode: %v\n%s", name, err, raw)
	}
	if resp.Error != nil || resp.Result.IsError {
		t.Fatalf("%s failed: %s", name, raw)
	}
	return resp.Result.StructuredContent.Data
}

// activityActor is the newest activity_log row for one entity.
type activityActor struct {
	user, token, client *string
	action              string
}

func lastActivity(t *testing.T, pool *pgxpool.Pool, entityType string, id uuid.UUID) activityActor {
	t.Helper()
	var a activityActor
	if err := pool.QueryRow(context.Background(),
		`SELECT actor_user_id::text, actor_agent_token_id::text, actor_client_id, action
		   FROM activity_log WHERE entity_type = $1 AND entity_id = $2 ORDER BY id DESC LIMIT 1`,
		entityType, id).Scan(&a.user, &a.token, &a.client, &a.action); err != nil {
		t.Fatalf("activity for %s %s: %v", entityType, id, err)
	}
	return a
}

func createdBy(t *testing.T, pool *pgxpool.Pool, table string, id uuid.UUID) (created, updated *string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT created_by::text, updated_by::text FROM `+table+` WHERE id = $1`, id).Scan(&created, &updated); err != nil {
		t.Fatalf("%s %s: %v", table, id, err)
	}
	return created, updated
}

func idOf(t *testing.T, data map[string]any, key string) uuid.UUID {
	t.Helper()
	s, _ := data[key].(string)
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("result has no %s: %v", key, data)
	}
	return id
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestToolWritesAreAttributed(t *testing.T) {
	pool := testutil.NewPool(t)
	ownerCtx, hh := testutil.SeedHousehold(t, pool)
	owner := hh.UserID.String()

	// An interactive Dex session from a registered MCP client.
	dex := *hh.Claims
	dex.ClientID = "mcp-3f6c1b9e"
	dexCtx := server.ContextWithClaims(context.Background(), &dex)

	// A second member acting through a write-scoped agent token.
	var adult, token uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, name, password_hash) VALUES ('adult-'||gen_random_uuid()||'@test.homeos.local', 'Adult', 'x') RETURNING id`).
		Scan(&adult); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'family_member')`, hh.HouseholdID, adult); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO agent_tokens (user_id, household_id, name, token_prefix, token_hash, scopes)
		 VALUES ($1, $2, 'Claude', 'hos_abcd', sha256(gen_random_uuid()::text::bytea), '{read,write}') RETURNING id`,
		adult, hh.HouseholdID).Scan(&token); err != nil {
		t.Fatal(err)
	}
	agentCtx := server.ContextWithClaims(context.Background(), &auth.Claims{
		UserID: adult.String(), HouseholdID: hh.HouseholdID.String(), Role: "family_member",
		AgentTokenID: token.String(), Scopes: []string{auth.ScopeRead, auth.ScopeWrite},
		// A stray client id on an agent-token caller must not be recorded:
		// the token is the client.
		Identity: auth.Identity{ClientID: "ignored"},
	})

	cfg := &config.Config{}
	srv := server.NewServer(cfg, pool, nil)
	registerAll(srv, cfg, pool)

	type want struct{ user, token, client string }
	check := func(t *testing.T, table, entity string, id uuid.UUID, w want) {
		t.Helper()
		created, updated := createdBy(t, pool, table, id)
		if deref(created) != w.user || deref(updated) != w.user {
			t.Errorf("%s created_by/updated_by = %s/%s, want %s", table, deref(created), deref(updated), w.user)
		}
		a := lastActivity(t, pool, entity, id)
		if a.action != "create" || deref(a.user) != w.user || deref(a.token) != w.token || deref(a.client) != w.client {
			t.Errorf("%s activity = %s user=%s token=%s client=%s, want create %+v",
				entity, a.action, deref(a.user), deref(a.token), deref(a.client), w)
		}
	}

	t.Run("note_create", func(t *testing.T) {
		d := callTool(t, ownerCtx, srv, "note_create", map[string]any{"title": "Wi-Fi", "content": "router in the closet"})
		check(t, "notes", "note", idOf(t, d, "id"), want{owner, "<nil>", "<nil>"})
	})

	t.Run("event_create via Dex client", func(t *testing.T) {
		cal := callTool(t, ownerCtx, srv, "calendar_create", map[string]any{"name": "Family"})
		var calID uuid.UUID
		if err := pool.QueryRow(context.Background(),
			`SELECT id FROM calendars WHERE household_id = $1 AND name = 'Family'`, hh.HouseholdID).Scan(&calID); err != nil {
			t.Fatalf("calendar_create made no Family calendar (%v): %v", cal, err)
		}
		check(t, "calendars", "calendar", calID, want{owner, "<nil>", "<nil>"})

		callTool(t, dexCtx, srv, "event_create", map[string]any{
			"calendar_id": calID.String(), "title": "Dentist", "start": "2026-11-19T17:30:00-05:00", "end": "2026-11-19T18:00:00-05:00",
		})
		var ev uuid.UUID
		if err := pool.QueryRow(context.Background(),
			`SELECT id FROM calendar_objects WHERE calendar_id = $1 ORDER BY created_at DESC LIMIT 1`, calID).Scan(&ev); err != nil {
			t.Fatal(err)
		}
		check(t, "calendar_objects", "calendar_event", ev, want{owner, "<nil>", "mcp-3f6c1b9e"})
	})

	var listID uuid.UUID
	t.Run("list_create", func(t *testing.T) {
		d := callTool(t, ownerCtx, srv, "list_create", map[string]any{"name": "Grocery", "kind": "grocery"})
		l, _ := d["list"].(map[string]any)
		listID = idOf(t, l, "id")
		check(t, "lists", "list", listID, want{owner, "<nil>", "<nil>"})
	})

	t.Run("list_add_items via agent token", func(t *testing.T) {
		if listID == uuid.Nil {
			t.Skip("list_create failed")
		}
		callTool(t, agentCtx, srv, "list_add_items", map[string]any{
			"list_id": listID.String(), "items": []any{map[string]any{"text": "milk"}},
		})
		var item uuid.UUID
		if err := pool.QueryRow(context.Background(),
			`SELECT id FROM list_items WHERE list_id = $1 AND text = 'milk'`, listID).Scan(&item); err != nil {
			t.Fatal(err)
		}
		check(t, "list_items", "list_item", item, want{adult.String(), token.String(), "<nil>"})
	})

	t.Run("background write is the system", func(t *testing.T) {
		// Same pool, no claims: nothing left over from the calls above.
		var id uuid.UUID
		if err := pool.QueryRow(context.Background(),
			`INSERT INTO notes (household_id, entity_type, entity_id, title, body)
			 VALUES ($1, 'household', $1, 'System note', 'x') RETURNING id`, hh.HouseholdID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		check(t, "notes", "note", id, want{"<nil>", "<nil>", "<nil>"})
	})
}
