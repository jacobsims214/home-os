package bill

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/testutil"
)

func insertRow(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id.String()
}

// TestBillSubjects covers domain model v2 typed subjects on bills: an asset
// bill derives its property, bill_find filters by property (including its
// assets) and vehicle, bill_update replaces the subject, the legacy
// entity_type/entity_id params map onto the typed columns, another family's
// subject is rejected, and the legacy columns are never written.
func TestBillSubjects(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, other := testutil.SeedHousehold(t, pool)

	house := insertRow(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'House') RETURNING id`, hh.HouseholdID)
	room := insertRow(t, pool, `INSERT INTO rooms (property_id, name) VALUES ($1, 'Basement') RETURNING id`, house)
	heater := insertRow(t, pool, `INSERT INTO assets (household_id, room_id, name) VALUES ($1, $2, 'Water heater') RETURNING id`, hh.HouseholdID, room)
	car := insertRow(t, pool, `INSERT INTO vehicles (household_id, year, make, model) VALUES ($1, 2020, 'Subaru', 'Outback') RETURNING id`, hh.HouseholdID)
	theirHouse := insertRow(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Theirs') RETURNING id`, other.HouseholdID)

	_, _, create := NewBillCreateTool(pool)
	_, _, update := NewBillUpdateTool(pool)
	mk := func(args map[string]any) billData {
		t.Helper()
		res, text := callTool(t, ctx, create, "bill_create", args)
		if res.IsError {
			t.Fatalf("bill_create %v: %s", args, text)
		}
		var b billData
		if err := unmarshalData(text, &b); err != nil {
			t.Fatalf("decode %q: %v", text, err)
		}
		return b
	}

	warranty := mk(map[string]any{"name": "Heater warranty", "amount": 12.0, "due_day": 3.0, "asset_id": heater})
	if strOrNil(warranty.AssetID) != heater || strOrNil(warranty.PropertyID) != house ||
		strOrNil(warranty.SubjectType) != "asset" || strOrNil(warranty.SubjectName) != "Water heater" {
		t.Errorf("asset bill = %+v, want asset with derived property", warranty)
	}
	tax := mk(map[string]any{"name": "Property tax", "amount": 300.0, "due_day": 1.0, "property_id": house})
	insurance := mk(map[string]any{"name": "Car insurance", "amount": 90.0, "due_day": 9.0, "entity_type": "vehicle", "entity_id": car})
	if strOrNil(insurance.VehicleID) != car || strOrNil(insurance.SubjectType) != "vehicle" || strOrNil(insurance.SubjectName) != "2020 Subaru Outback" {
		t.Errorf("legacy alias bill = %+v, want vehicle subject", insurance)
	}
	netflix := mk(map[string]any{"name": "Netflix", "amount": 15.0, "due_day": 20.0})
	if netflix.SubjectType != nil || netflix.PropertyID != nil {
		t.Errorf("family-level bill has a subject: %+v", netflix)
	}

	var legacy int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM bills WHERE household_id = $1 AND entity_id IS NOT NULL`, hh.HouseholdID).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy != 0 {
		t.Errorf("%d bills written with legacy entity_id", legacy)
	}

	ids := func(bills []billData) string {
		var out []string
		for _, b := range bills {
			out = append(out, b.Name)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"property includes its assets", map[string]any{"property_id": house}, "Property tax,Heater warranty"},
		{"vehicle", map[string]any{"vehicle_id": car}, "Car insurance"},
		{"legacy kind alone", map[string]any{"entity_type": "asset"}, "Heater warranty"},
		{"legacy pair", map[string]any{"entity_type": "vehicle", "entity_id": car}, "Car insurance"},
	} {
		t.Run("find "+tc.name, func(t *testing.T) {
			got, _ := findBills(t, ctx, pool, tc.args)
			names := ids(got)
			// bill_find sorts by due date; compare as sets.
			if len(got) != len(strings.Split(tc.want, ",")) {
				t.Fatalf("got %s, want %s", names, tc.want)
			}
			for _, w := range strings.Split(tc.want, ",") {
				if !strings.Contains(names, w) {
					t.Errorf("got %s, want %s", names, tc.want)
				}
			}
		})
	}

	// Moving to a vehicle clears property and asset.
	res, text := callTool(t, ctx, update, "bill_update", map[string]any{"bill_id": warranty.ID, "vehicle_id": car})
	if res.IsError {
		t.Fatalf("move to vehicle: %s", text)
	}
	got := decodeUpdated(t, text)
	if strOrNil(got.VehicleID) != car || got.PropertyID != nil || got.AssetID != nil || !strings.Contains(text, "vehicle_id") {
		t.Errorf("moved bill = %s", text)
	}
	// A blank property_id makes it family-level.
	res, text = callTool(t, ctx, update, "bill_update", map[string]any{"bill_id": tax.ID, "property_id": ""})
	if res.IsError {
		t.Fatalf("untie: %s", text)
	}
	if got := decodeUpdated(t, text); got.PropertyID != nil || got.SubjectType != nil || got.SubjectName != nil {
		t.Errorf("untied bill = %s", text)
	}

	// Another family's property looks like a missing one; nothing changes.
	res, text = callTool(t, ctx, update, "bill_update", map[string]any{"bill_id": netflix.ID, "property_id": theirHouse})
	if !res.IsError || !strings.Contains(text, "No property with id") {
		t.Errorf("cross-family property: %s", text)
	}
	res, text = callTool(t, ctx, create, "bill_create", map[string]any{"name": "X", "amount": 1.0, "due_day": 1.0, "property_id": theirHouse})
	if !res.IsError || !strings.Contains(text, "No property with id") {
		t.Errorf("cross-family property on create: %s", text)
	}
}
