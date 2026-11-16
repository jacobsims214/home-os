package lists

// #4717: the role policy lets a housesitter add and check off list items, and
// not manage lists. These tests run the real tools as a housesitter.

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
)

// housesitterCtx adds a housesitter to tt's household and returns their
// context.
func housesitterCtx(t *testing.T, tt tools) context.Context {
	t.Helper()
	email := "sitter-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := tt.pool.QueryRow(context.Background(),
		`INSERT INTO users (email, name, password_hash) VALUES ($1, 'Sitter', 'x') RETURNING id`, email).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := tt.pool.Exec(context.Background(),
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'housesitter')`,
		tt.hh.HouseholdID, uid); err != nil {
		t.Fatal(err)
	}
	return server.ContextWithClaims(context.Background(), &auth.Claims{
		Identity:    auth.Identity{Subject: uid.String(), Email: email},
		UserID:      uid.String(),
		HouseholdID: tt.hh.HouseholdID.String(),
		Role:        auth.RoleHousesitter,
	})
}

// TestHousesitterAddsAndChecksOffItems: the narrow write works end to end.
func TestHousesitterAddsAndChecksOffItems(t *testing.T) {
	tt := setup(t)
	owner := tt.ctx
	sitter := housesitterCtx(t, tt)

	created := tt.create.ok(t, owner, map[string]any{"name": "Grocery", "kind": "grocery", "items": []any{"milk"}})
	id := created.Data.List.ID

	// The sitter adds an item to the family's list.
	added := tt.add.ok(t, sitter, map[string]any{"list_id": id, "items": []any{map[string]any{"text": "eggs"}}})
	if len(added.Data.Added) != 1 || added.Data.Added[0].Text != "eggs" {
		t.Fatalf("sitter added %+v", added.Data.Added)
	}
	// And checks it off.
	checked := tt.check.ok(t, sitter, map[string]any{"list_id": id, "texts": []string{"eggs"}})
	if got := itemTexts(checked.Data.Changed); len(got) != 1 || got[0] != "eggs" {
		t.Fatalf("sitter checked off %v", got)
	}
	if got := state(t, tt.pool, id)["eggs"]; !got {
		t.Fatal("eggs is not checked in the database")
	}
}

// TestHousesitterCannotManageLists: a sitter may not start a list, edit or
// remove items, or rename/archive one. The narrow grant does not widen.
func TestHousesitterCannotManageLists(t *testing.T) {
	tt := setup(t)
	sitter := housesitterCtx(t, tt)
	created := tt.create.ok(t, tt.ctx, map[string]any{"name": "Grocery", "kind": "grocery", "items": []any{"milk"}})
	id := created.Data.List.ID

	// A kind word with no active list of that kind: no list is created for a
	// sitter, and the call is a corrective error.
	tt.add.fail(t, sitter, map[string]any{"list_id": "packing", "items": []any{map[string]any{"text": "socks"}}},
		"list_id", "can't start a new list")
	var n int
	if err := tt.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM lists WHERE household_id = $1 AND kind = 'packing'`, tt.hh.HouseholdID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a sitter created %d packing list(s)", n)
	}

	// Editing an item, removing it and renaming the list are refused by the
	// server's role policy gate, not by these handlers: the gate is the
	// boundary (#4717), and cmd/mcp's TestNarrowWritesReachTheRightRoles and
	// TestToolPolicyMatrix pin it. What this package owns is the no-create
	// rule above, which the handler applies itself because it is a decision
	// about an argument, not about the role.
	_ = id
}
