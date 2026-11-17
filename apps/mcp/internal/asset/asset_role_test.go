package asset

// #4808: asset_find answers every role (ResAssets read is rolesEveryone), so a
// housesitter or a vendor may list the family's things — but not what each one
// cost or is worth. This is the asset half #4806 left open (its KB doc names
// it); the money fields are absent from the JSON, not null and not zero.
//
// The money fields are found by reflection over assetMoney, so a field added to
// it is covered here without editing this test.

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// assetMoneyFields lists the JSON names of every money field of an asset.
func assetMoneyFields(t *testing.T) []string {
	t.Helper()
	typ := reflect.TypeOf(assetMoney{})
	out := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("assetMoney.%s has no json name", typ.Field(i).Name)
		}
		if !strings.Contains(typ.Field(i).Tag.Get("json"), "omitzero") {
			t.Errorf("assetMoney.%s must carry omitzero, or a hidden field serializes as null", typ.Field(i).Name)
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		t.Fatal("assetMoney has no fields")
	}
	return out
}

// moneyByRole is this test's own expectation, written out per role, so the
// assertion does not take its answer from the function under test: mutating
// auth.CanSeeMoney moves both the behaviour and a derived expectation, and a
// self-consistent test then passes while money leaks. The ticket names
// housesitter and vendor as the roles that must lose money and owner/viewer as
// unchanged (viewer is family for bills.read and loans.read, see
// decisions/2026-11-16-money-redaction.md); the loop still covers every role in
// auth.AllRoles and fails on one this table does not name.
var moneyByRole = map[string]bool{
	"owner":          true,
	"family_manager": true,
	"family_member":  true,
	"viewer":         true,
	"housesitter":    false,
	"vendor":         false,
}

// seesMoney is moneyByRole as the one question the tool asks, and it fails the
// test when the policy table and this test have drifted apart.
func seesMoney(t *testing.T, role string) bool {
	t.Helper()
	want, ok := moneyByRole[role]
	if !ok {
		t.Fatalf("role %q is not in moneyByRole: add it and say whether it may see money", role)
	}
	if got := auth.CanSeeMoney(role); got != want {
		t.Errorf("auth.CanSeeMoney(%q) = %v, want %v: the policy table moved and this test's "+
			"expectation did not (or the policy is wrong)", role, got, want)
	}
	return want
}

// assetAsRole builds a member of hh with role and a context carrying it.
func assetAsRole(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) context.Context {
	t.Helper()
	ctx := context.Background()
	email := "asset-" + role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
		email, "Asset "+role).Scan(&uid); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
		hh.HouseholdID, uid, role); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	return server.ContextWithClaims(ctx, &auth.Claims{
		Identity:    auth.Identity{Subject: uid.String(), Email: email},
		UserID:      uid.String(),
		HouseholdID: hh.HouseholdID.String(),
		Role:        role,
	})
}

// TestAssetFindHidesMoneyByRole is the per-role matrix for asset_find, in list
// form and with id.
func TestAssetFindHidesMoneyByRole(t *testing.T) {
	pool := testutil.NewPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	_, _, find := NewAssetFindTool(pool)

	fridge := insertID(t, pool, `INSERT INTO assets (household_id, name, category, purchase_date, purchase_price, current_value, warranty_expiry)
		VALUES ($1, 'Fridge', 'appliance', '2024-03-01', 2400, 1800, '2027-03-01') RETURNING id`, hh.HouseholdID)

	money := assetMoneyFields(t)

	for _, role := range auth.AllRoles {
		t.Run(role, func(t *testing.T) {
			ctx := assetAsRole(t, pool, hh, role)
			withMoney := seesMoney(t, role)
			for form, args := range map[string]map[string]any{"list": {}, "id": {"id": fridge}} {
				env := call(t, ctx, find, "asset_find", args)
				assertAssetMoney(t, env.Items, role, withMoney, money, form)
				// The identity fields stay, or the tool is useless to them.
				if !strings.Contains(string(env.Items), `"name":"Fridge"`) ||
					!strings.Contains(string(env.Items), `"warranty_expiry":"2027-03-01"`) {
					t.Errorf("%s (%s): identity/warranty missing: %s", role, form, env.Items)
				}
			}
		})
	}
}

// assertAssetMoney checks one asset_find items array: the money fields are
// present with their values for a role that may see money, and absent by name
// for one that may not.
func assertAssetMoney(t *testing.T, items json.RawMessage, role string, withMoney bool, money []string, form string) {
	t.Helper()
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(items, &rows); err != nil {
		t.Fatalf("%s (%s): decode items: %v", role, form, err)
	}
	if len(rows) != 1 {
		t.Fatalf("%s (%s): %d items, want 1", role, form, len(rows))
	}
	row := rows[0]
	for _, name := range money {
		got, present := row[name]
		if !withMoney {
			if present {
				t.Errorf("%s (%s) must not see %s, but it is present as %s", role, form, name, got)
			}
			continue
		}
		if !present || string(got) == "null" {
			t.Errorf("%s (%s) should see %s, but it is absent or null", role, form, name)
		}
	}
}

// TestAssetFindMoneyValues pins the values a money role gets, so "present"
// cannot pass with the wrong number.
func TestAssetFindMoneyValues(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool) // the owner
	_, _, find := NewAssetFindTool(pool)

	fridge := insertID(t, pool, `INSERT INTO assets (household_id, name, category, purchase_date, purchase_price, current_value)
		VALUES ($1, 'Fridge', 'appliance', '2024-03-01', 2400, 1800) RETURNING id`, hh.HouseholdID)

	a := call(t, ctx, find, "asset_find", map[string]any{"id": fridge}).assets(t)
	if len(a) != 1 {
		t.Fatalf("want 1 asset, got %d", len(a))
	}
	got := a[0]
	if got.PurchaseDate == nil || *got.PurchaseDate != "2024-03-01" ||
		got.PurchasePrice == nil || *got.PurchasePrice != 2400 ||
		got.CurrentValue == nil || *got.CurrentValue != 1800 {
		t.Errorf("money = %+v", got.assetMoney)
	}
}
