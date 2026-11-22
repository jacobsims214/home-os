package asset

// #5513: an asset write must enqueue its outbox event whoever makes it.
//
// Until migration 088 the enqueue was handler code in the API's asset repo
// (apps/api/internal/asset/repo.go). The MCP tool layer writes the assets row
// itself and enqueued NOTHING, so an asset created by a family agent left no
// outbox event at all -- work the family asked for that silently did not
// happen downstream. The enqueue is now the assets_outbox trigger, so these
// tests drive the real tool handlers and assert one row per write.
//
// The REST path is proved in apps/api/internal/asset/outbox_test.go against
// the same trigger (a Go module cannot import another module's internal
// package); both fail if the migration is missing.

import (
	"context"
	"testing"
)

// assetOutboxRows returns the outbox events about one asset as
// "<type>|<name>|<category>", oldest first.
func assetOutboxRows(t *testing.T, f fixture, assetID string) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT type || '|' || (payload->>'name') || '|' || COALESCE(payload->>'category', 'null')
		   FROM outbox_events
		  WHERE payload->>'asset_id' = $1
		  ORDER BY created_at, id`, assetID)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan outbox: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	return out
}

// TestAssetCreateEnqueuesOneEvent proves asset_create (the MCP write path)
// leaves exactly one asset.created with the same four keys the API wrote.
// "Exactly one" is the other half of the bug: a trigger left beside a copy in
// the handler would double every agent write.
func TestAssetCreateEnqueuesOneEvent(t *testing.T) {
	f := setup(t)
	prop := f.property(t, f.hh.HouseholdID, "Home")

	env := call(t, f.ctx, f.create, "asset_create", map[string]any{
		"name":        "Dishwasher",
		"category":    "appliance",
		"property_id": prop,
	})
	if !env.OK {
		t.Fatalf("asset_create failed: %s", env.Summary)
	}
	created := env.asset(t)

	got := assetOutboxRows(t, f, created.ID)
	if len(got) != 1 {
		t.Fatalf("outbox events = %v, want exactly one asset.created", got)
	}
	if want := "asset.created|Dishwasher|appliance"; got[0] != want {
		t.Errorf("event = %q, want %q", got[0], want)
	}

	// The payload's household_id is the asset's own, so a consumer polling one
	// family never picks up another's asset.
	var payloadHousehold string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT payload->>'household_id' FROM outbox_events WHERE payload->>'asset_id' = $1`,
		created.ID).Scan(&payloadHousehold); err != nil {
		t.Fatalf("read payload household: %v", err)
	}
	if payloadHousehold != f.hh.HouseholdID.String() {
		t.Errorf("payload household_id = %q, want %q", payloadHousehold, f.hh.HouseholdID)
	}
}

// TestAssetUpdateEnqueuesOneEvent proves asset_update emits one asset.updated
// and no second asset.created.
func TestAssetUpdateEnqueuesOneEvent(t *testing.T) {
	f := setup(t)
	prop := f.property(t, f.hh.HouseholdID, "Home")

	env := call(t, f.ctx, f.create, "asset_create", map[string]any{
		"name":        "Washer",
		"category":    "appliance",
		"property_id": prop,
	})
	if !env.OK {
		t.Fatalf("asset_create failed: %s", env.Summary)
	}
	created := env.asset(t)

	env = call(t, f.ctx, f.upd, "asset_update", map[string]any{
		"asset_id": created.ID,
		"name":     "Utility washer",
	})
	if !env.OK {
		t.Fatalf("asset_update failed: %s", env.Summary)
	}

	got := assetOutboxRows(t, f, created.ID)
	want := []string{"asset.created|Washer|appliance", "asset.updated|Utility washer|appliance"}
	if len(got) != len(want) {
		t.Fatalf("outbox events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestAssetDeleteEnqueuesNoEvent pins what 088 does NOT do: no asset.deleted
// has ever existed and none is invented here. The asset row still goes.
func TestAssetDeleteEnqueuesNoEvent(t *testing.T) {
	f := setup(t)
	prop := f.property(t, f.hh.HouseholdID, "Home")

	env := call(t, f.ctx, f.create, "asset_create", map[string]any{
		"name":        "Toaster",
		"category":    "appliance",
		"property_id": prop,
	})
	if !env.OK {
		t.Fatalf("asset_create failed: %s", env.Summary)
	}
	created := env.asset(t)

	env = call(t, f.ctx, f.del, "asset_delete", map[string]any{"asset_id": created.ID})
	if !env.OK {
		t.Fatalf("asset_delete failed: %s", env.Summary)
	}

	var live int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM assets WHERE id = $1`, created.ID).Scan(&live); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if live != 0 {
		t.Errorf("asset still in table after delete")
	}

	var deleted int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM outbox_events WHERE type = 'asset.deleted' AND payload->>'asset_id' = $1`,
		created.ID).Scan(&deleted); err != nil {
		t.Fatalf("count asset.deleted: %v", err)
	}
	if deleted != 0 {
		t.Errorf("asset.deleted events = %d, want 0 (that event has never existed)", deleted)
	}
}
