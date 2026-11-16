package vehicle

// #4808: vehicle_find is refused to a housesitter and a vendor by #4717's
// per-tool gate (ResVehicles read is rolesReaders), but the tool itself must
// still not depend on that: a role that may call it and may not see money (a
// future grant, a viewer demoted mid-session, an agent token whose role moved)
// gets no amount, value, balance or payment. The money fields are absent from
// the JSON, not null and not zero.
//
// The money fields are found by reflection over vehicleMoney, so a field added
// to it is covered here without editing this test.

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

// vehicleMoneyFields lists the JSON names of every money field of a vehicle.
func vehicleMoneyFields(t *testing.T) []string {
	t.Helper()
	typ := reflect.TypeOf(vehicleMoney{})
	out := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("vehicleMoney.%s has no json name", typ.Field(i).Name)
		}
		if !strings.Contains(typ.Field(i).Tag.Get("json"), "omitzero") {
			t.Errorf("vehicleMoney.%s must carry omitzero, or a hidden field serializes as null", typ.Field(i).Name)
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		t.Fatal("vehicleMoney has no fields")
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

// vehicleAsRole builds a member of hh with role and a context carrying it.
func vehicleAsRole(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) context.Context {
	t.Helper()
	ctx := context.Background()
	email := "veh-" + role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
		email, "Vehicle "+role).Scan(&uid); err != nil {
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

// TestVehicleFindHidesMoneyByRole is the per-role matrix for vehicle_find, in
// list form and with id (which also carries related.loans and related.bills).
func TestVehicleFindHidesMoneyByRole(t *testing.T) {
	pool := testutil.NewPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	_, _, find := NewVehicleFindTool(pool)

	odyssey := insertID(t, pool, `INSERT INTO vehicles (household_id, year, make, model, vin, license_plate, color,
		purchase_price, purchase_date, current_value)
		VALUES ($1, 2022, 'Honda', 'Odyssey', '5FNRL6H7XNB000001', 'ABC123', 'Blue', 38000, '2022-03-15', 31000)
		RETURNING id`, hh.HouseholdID)
	insertID(t, pool, `INSERT INTO loans (household_id, name, lender, original_amount, remaining_balance, monthly_payment, vehicle_id)
		VALUES ($1, 'Odyssey loan', 'Honda Finance', 30000, 21000, 550, $2) RETURNING id`, hh.HouseholdID, odyssey)
	insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day, vehicle_id)
		VALUES ($1, 'Car insurance', 100, 5, $2) RETURNING id`, hh.HouseholdID, odyssey)
	insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, vehicle_id)
		VALUES ($1, 'Oil change', '2026-12-04', $2) RETURNING id`, hh.HouseholdID, odyssey)

	money := vehicleMoneyFields(t)

	for _, role := range auth.AllRoles {
		t.Run(role, func(t *testing.T) {
			ctx := vehicleAsRole(t, pool, hh, role)
			withMoney := seesMoney(t, role)

			// List form: the vehicle's own money fields.
			env := call(t, ctx, find, map[string]any{})
			assertVehicleMoney(t, env.Items, role, withMoney, money, false)
			// id form: the same fields plus related.
			env = call(t, ctx, find, map[string]any{"id": odyssey})
			assertVehicleMoney(t, env.Items, role, withMoney, money, true)
			// The vehicle's identity is still there, or the tool is useless.
			if !strings.Contains(string(env.Items), `"license_plate":"ABC123"`) {
				t.Errorf("%s: license_plate missing: %s", role, env.Items)
			}

			hints := strings.Join(env.Next, " ")
			if !withMoney {
				for _, tool := range []string{"loan_create", "loan_find"} {
					if strings.Contains(hints, tool) {
						t.Errorf("%s: next hints at %s, which would refuse this role: %v", role, tool, env.Next)
					}
				}
			}
		})
	}
}

// assertVehicleMoney checks one vehicle_find items array: the money fields are
// present with their values for a role that may see money, and absent by name
// for one that may not — including related.loans and related.bills, whose
// records carry a remaining balance, a monthly payment and an amount.
func assertVehicleMoney(t *testing.T, items json.RawMessage, role string, withMoney bool, money []string, withRelated bool) {
	t.Helper()
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(items, &rows); err != nil {
		t.Fatalf("%s: decode items: %v", role, err)
	}
	if len(rows) != 1 {
		t.Fatalf("%s: %d items, want 1", role, len(rows))
	}
	row := rows[0]
	for _, name := range money {
		got, present := row[name]
		if !withMoney {
			if present {
				t.Errorf("%s must not see %s, but it is present as %s", role, name, got)
			}
			continue
		}
		if !present || string(got) == "null" {
			t.Errorf("%s should see %s, but it is absent or null", role, name)
		}
	}
	if !withRelated {
		return
	}
	rel, ok := row["related"]
	if !ok || string(rel) == "null" {
		t.Fatalf("%s: no related with id: %s", role, items)
	}
	var related map[string]json.RawMessage
	if err := json.Unmarshal(rel, &related); err != nil {
		t.Fatalf("%s: decode related: %v", role, err)
	}
	for _, name := range []string{"loans", "bills"} {
		got, present := related[name]
		if !withMoney {
			if present {
				t.Errorf("%s must not see related.%s, but it is present as %s", role, name, got)
			}
			continue
		}
		if !present || string(got) == "null" {
			t.Errorf("%s should see related.%s, but it is absent or null", role, name)
		}
	}
	// Upkeep carries no amount and stays for every role that may see upkeep.
	if _, ok := related["upkeep"]; !ok {
		t.Errorf("%s: related.upkeep missing: %s", role, related)
	}
}

// TestVehicleFindMoneyValues pins the values a money role gets, so "present"
// cannot pass with the wrong number.
func TestVehicleFindMoneyValues(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool) // the owner
	_, _, find := NewVehicleFindTool(pool)

	odyssey := insertID(t, pool, `INSERT INTO vehicles (household_id, year, make, model,
		purchase_price, purchase_date, current_value)
		VALUES ($1, 2022, 'Honda', 'Odyssey', 38000, '2022-03-15', 31000) RETURNING id`, hh.HouseholdID)
	insertID(t, pool, `INSERT INTO loans (household_id, name, lender, original_amount, remaining_balance, monthly_payment, vehicle_id)
		VALUES ($1, 'Odyssey loan', 'Honda Finance', 30000, 21000, 550, $2) RETURNING id`, hh.HouseholdID, odyssey)
	insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day, vehicle_id)
		VALUES ($1, 'Car insurance', 100, 5, $2) RETURNING id`, hh.HouseholdID, odyssey)

	v := call(t, ctx, find, map[string]any{"id": odyssey}).vehicles(t)
	if len(v) != 1 {
		t.Fatalf("want 1 vehicle, got %d", len(v))
	}
	got := v[0]
	if got.PurchaseDate == nil || *got.PurchaseDate != "2022-03-15" ||
		got.PurchasePrice == nil || *got.PurchasePrice != 38000 ||
		got.CurrentValue == nil || *got.CurrentValue != 31000 ||
		got.LoanCount == nil || *got.LoanCount != 1 || got.BillCount == nil || *got.BillCount != 1 {
		t.Errorf("money = %+v", got.vehicleMoney)
	}
	if got.Related == nil || len(got.Related.Loans) != 1 || got.Related.Loans[0].RemainingBalance != 21000 ||
		len(got.Related.Bills) != 1 || got.Related.Bills[0].Amount == nil || *got.Related.Bills[0].Amount != 100 {
		t.Errorf("related = %+v", got.Related)
	}
}
