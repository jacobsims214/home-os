package property

// #4808: property_find answers every role (ResProperties read is
// rolesEveryone), so a housesitter or a vendor may list the family's homes —
// but not what they cost, what they are worth, or how many bills and loans
// hang off them. The money fields are absent from the JSON, not null and not
// zero, so a model can never read "purchase_price: null" as a value.
//
// The money fields are found by reflection over propertyMoney and
// propertyMoneyCounts, so a field added to either struct is covered here
// without editing this test: that is the point of one embedded struct per
// group (#4806's pattern).

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

// moneyFields lists the JSON names of every money field of a property, from
// the two embedded structs.
func moneyFields(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, typ := range []reflect.Type{reflect.TypeOf(propertyMoney{}), reflect.TypeOf(propertyMoneyCounts{})} {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				t.Fatalf("%s.%s has no json name", typ.Name(), typ.Field(i).Name)
			}
			if !strings.Contains(typ.Field(i).Tag.Get("json"), "omitzero") {
				t.Errorf("%s.%s must carry omitzero, or a hidden field serializes as null", typ.Name(), typ.Field(i).Name)
			}
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		t.Fatal("no money fields found: the reflection helper is looking at the wrong structs")
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

// propertyAsRole builds a member of hh with role and a context carrying it.
func propertyAsRole(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) context.Context {
	t.Helper()
	ctx := context.Background()
	email := "prop-" + role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
		email, "Property "+role).Scan(&uid); err != nil {
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

// TestPropertyFindHidesMoneyByRole is the per-role matrix for property_find:
// a role that may see money gets every money field with its value; every other
// role gets none of them, by name, in list and id form.
func TestPropertyFindHidesMoneyByRole(t *testing.T) {
	pool := testutil.NewPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	_, _, find := NewPropertyFindTool(pool)

	// A property with every money field set, plus a bill and a loan so the
	// money counts are non-zero (a zero count would not prove the field is
	// absent rather than 0).
	home := insertID(t, pool, `INSERT INTO properties (household_id, name, address, property_type, purchase_date, purchase_price, current_value)
		VALUES ($1, 'Home', '12 Elm St', 'house', '2019-06-01', 380000, 450000) RETURNING id`, hh.HouseholdID)
	insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day, property_id)
		VALUES ($1, 'Electric', 142.50, 5, $2) RETURNING id`, hh.HouseholdID, home)
	insertID(t, pool, `INSERT INTO loans (household_id, name, original_amount, remaining_balance, property_id)
		VALUES ($1, 'Mortgage', 300000, 250000, $2) RETURNING id`, hh.HouseholdID, home)

	money := moneyFields(t)

	// Every role, against this test's own expectation (moneyByRole): an
	// expectation derived from auth.CanSeeMoney would move with the function
	// under test and pass while money leaked.
	for _, role := range auth.AllRoles {
		t.Run(role, func(t *testing.T) {
			ctx := propertyAsRole(t, pool, hh, role)
			withMoney := seesMoney(t, role)
			// List form.
			env := call(t, ctx, find, map[string]any{})
			assertMoney(t, env.Items, role, withMoney, money)
			// id form (the search_all open path).
			env = call(t, ctx, find, map[string]any{"id": home})
			assertMoney(t, env.Items, role, withMoney, money)
			// The next hints must not send a role without money to loan_find
			// or bill_create, which would refuse it.
			hints := strings.Join(env.Next, " ")
			if withMoney {
				if !strings.Contains(hints, "loan_find property_id="+home) {
					t.Errorf("%s: next = %v, want the loan_find hint", role, env.Next)
				}
				return
			}
			for _, tool := range []string{"loan_find", "bill_create", "bill_find", "budget_summary"} {
				if strings.Contains(hints, tool) {
					t.Errorf("%s: next hints at %s, which would refuse this role: %v", role, tool, env.Next)
				}
			}
			// The identity fields stay, or the tool would be useless to them.
			if !strings.Contains(string(env.Items), `"address":"12 Elm St"`) {
				t.Errorf("%s: address missing: %s", role, env.Items)
			}
		})
	}
}

// assertMoney checks one property_find items array: withMoney says whether the
// money fields must be present with their values, or absent entirely.
func assertMoney(t *testing.T, items json.RawMessage, role string, withMoney bool, money []string) {
	t.Helper()
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(items, &rows); err != nil {
		t.Fatalf("%s: decode items: %v", role, err)
	}
	if len(rows) != 1 {
		t.Fatalf("%s: %d items, want 1", role, len(rows))
	}
	row := rows[0]
	counts, ok := row["counts"]
	if !ok {
		t.Fatalf("%s: no counts: %s", role, items)
	}
	var countFields map[string]json.RawMessage
	if err := json.Unmarshal(counts, &countFields); err != nil {
		t.Fatalf("%s: decode counts: %v", role, err)
	}
	for _, name := range money {
		// A money count lives inside counts; the rest at the top level.
		where, present := row[name]
		if _, inCounts := countFields[name]; inCounts {
			where, present = countFields[name], true
		}
		if !withMoney {
			if present {
				t.Errorf("%s must not see %s, but it is present as %s", role, name, where)
			}
			continue
		}
		if !present {
			t.Errorf("%s should see %s, but it is absent", role, name)
			continue
		}
		if string(where) == "null" {
			t.Errorf("%s: %s is null, want its value", role, name)
		}
	}
	// A money role keeps the counts' identity fields whatever the role.
	for _, name := range []string{"rooms", "assets", "open_maintenance"} {
		if _, ok := countFields[name]; !ok {
			t.Errorf("%s: counts.%s missing: %s", role, name, counts)
		}
	}
}

// TestPropertyFindMoneyValues pins the values a money role gets, so "present"
// cannot pass with the wrong number (the query's CASE WHEN could return the
// wrong column and reflection alone would not notice).
func TestPropertyFindMoneyValues(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool) // the owner
	_, _, find := NewPropertyFindTool(pool)

	home := insertID(t, pool, `INSERT INTO properties (household_id, name, purchase_date, purchase_price, current_value)
		VALUES ($1, 'Home', '2019-06-01', 380000, 450000) RETURNING id`, hh.HouseholdID)
	insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day, property_id)
		VALUES ($1, 'Electric', 142.50, 5, $2) RETURNING id`, hh.HouseholdID, home)
	insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day, property_id)
		VALUES ($1, 'Water', 60, 9, $2) RETURNING id`, hh.HouseholdID, home)
	insertID(t, pool, `INSERT INTO loans (household_id, name, original_amount, remaining_balance, property_id)
		VALUES ($1, 'Mortgage', 300000, 250000, $2) RETURNING id`, hh.HouseholdID, home)

	p := call(t, ctx, find, map[string]any{"id": home}).properties(t)
	if len(p) != 1 {
		t.Fatalf("want 1 property, got %d", len(p))
	}
	got := p[0]
	if got.PurchaseDate == nil || *got.PurchaseDate != "2019-06-01" ||
		got.PurchasePrice == nil || *got.PurchasePrice != 380000 ||
		got.CurrentValue == nil || *got.CurrentValue != 450000 {
		t.Errorf("money values = %+v", got.propertyMoney)
	}
	if got.Counts.Bills == nil || *got.Counts.Bills != 2 || got.Counts.Loans == nil || *got.Counts.Loans != 1 {
		t.Errorf("money counts = %+v, want 2 bills and 1 loan", got.Counts.propertyMoneyCounts)
	}
	// The summary a model reads must not state the value to a role that may
	// not see it either; here (owner) it does.
	if !strings.Contains(call(t, ctx, find, map[string]any{"id": home}).Summary, "$450,000.00") {
		t.Errorf("owner summary should state the value")
	}
}
