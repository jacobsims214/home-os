package note

// #4717: the role policy lets a housesitter add a note on a pet and nothing
// else (auth.CanAnnotateEntity). These tests run note_create as a housesitter.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// sitterCtx adds a housesitter to hh and returns their context.
func sitterCtx(t *testing.T, pool *pgxpool.Pool, hh testutil.Household) context.Context {
	t.Helper()
	email := "sitter-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, name, password_hash) VALUES ($1, 'Sitter', 'x') RETURNING id`, email).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'housesitter')`,
		hh.HouseholdID, uid); err != nil {
		t.Fatal(err)
	}
	return server.ContextWithClaims(context.Background(), &auth.Claims{
		Identity:    auth.Identity{Subject: uid.String(), Email: email},
		UserID:      uid.String(),
		HouseholdID: hh.HouseholdID.String(),
		Role:        auth.RoleHousesitter,
	})
}

// TestHousesitterNotesOnlyPets: a pet note succeeds; a note about the family,
// a property or a vehicle is refused with a corrective message naming the
// role, and nothing is stored.
func TestHousesitterNotesOnlyPets(t *testing.T) {
	pool := testutil.NewPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	petID := seedRow(t, pool, "pets", hh.HouseholdID)
	propertyID := seedRow(t, pool, "properties", hh.HouseholdID)
	vehicleID := seedRow(t, pool, "vehicles", hh.HouseholdID)

	ctx := sitterCtx(t, pool, hh)
	name, tool, h := NewNoteCreateTool(pool)
	create := newCaller[wireNote](t, name, tool, h)

	env := create.mustOK(t, ctx, map[string]any{
		"title": "Feeding", "content": "Bella eats at 7am", "about_type": "pet", "about_id": petID.String(),
	})
	if env.Data.ID == uuid.Nil {
		t.Fatalf("pet note was not created: %+v", env)
	}

	for _, tc := range []struct{ aboutType, aboutID, want string }{
		{"family", "", "the family"},
		{"property", propertyID.String(), "property"},
		{"vehicle", vehicleID.String(), "vehicle"},
	} {
		args := map[string]any{"title": "T", "content": "C", "about_type": tc.aboutType}
		if tc.aboutID != "" {
			args["about_id"] = tc.aboutID
		}
		isErr, env := create.call(t, ctx, args)
		if !isErr || env.Error == nil {
			t.Fatalf("housesitter note about %s was accepted: %+v", tc.aboutType, env)
		}
		if !strings.Contains(env.Error.Message, "housesitter") || !strings.Contains(env.Error.Message, tc.want) {
			t.Errorf("about %s: message %q does not name the role and %s", tc.aboutType, env.Error.Message, tc.want)
		}
		if env.Error.Fix == "" {
			t.Errorf("about %s: refusal has no fix", tc.aboutType)
		}
	}
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM notes WHERE household_id = $1 AND title = 'T'`, hh.HouseholdID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d refused notes were stored", n)
	}
}
