package asset

// #5320: an asset write must enqueue its outbox event whoever makes it.
//
// Until migration 075 the enqueue was handler code in the API's asset repo
// (apps/api/internal/asset/repo.go), so the MCP tool layer -- the way family
// agents write -- left no outbox event at all. The enqueue is now the
// assets_enqueue_outbox trigger, so these tests drive the real tool handlers
// and assert one row per write: exactly one is what proves the fix is in one
// shared place and was not also copied into the MCP handlers.
//
// The REST path is proved in the API's own module
// (apps/api/internal/asset/outbox_test.go), because a Go module cannot import
// another's internal packages; both tests target the same trigger and, unlike
// a unit test with a fake, they fail if the migration is missing.

import (
	"context"
	"testing"
)

// outboxRow is one outbox_events row as the tests read it.
type outboxRow struct {
	householdID string
	type_       string
	assetID     string
	name        string
	category    *string
}

// outboxFor returns every outbox event about an asset, oldest first.
func outboxFor(t *testing.T, ctx context.Context, f fixture, assetID string) []outboxRow {
	t.Helper()
	rows, err := f.pool.Query(ctx,
		`SELECT household_id::text, type, payload->>'asset_id', payload->>'name', payload->>'category'
		   FROM outbox_events
		  WHERE payload->>'asset_id' = $1
		  ORDER BY created_at, id`, assetID)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.householdID, &r.type_, &r.assetID, &r.name, &r.category); err != nil {
			t.Fatalf("scan outbox: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	return out
}

// TestAssetCreateEnqueuesOutbox proves asset_create (the MCP write path)
// leaves exactly one asset.created event carrying the same four keys the API
// repo used to build: asset_id, name, category, household_id.
func TestAssetCreateEnqueuesOutbox(t *testing.T) {
	f := setup(t)
	d := call(t, f.ctx, f.create, "asset_create", map[string]any{
		"name": "Dishwasher", "category": "appliance", "manufacturer": "Bosch",
	}).asset(t)

	got := outboxFor(t, context.Background(), f, d.ID)
	if len(got) != 1 {
		t.Fatalf("%d outbox events for the MCP-created asset, want exactly 1: %+v", len(got), got)
	}
	e := got[0]
	if e.type_ != "asset.created" {
		t.Errorf("type = %q, want asset.created", e.type_)
	}
	if e.householdID != f.hh.HouseholdID.String() || e.assetID != d.ID {
		t.Errorf("household/asset = %s/%s, want %s/%s", e.householdID, e.assetID, f.hh.HouseholdID, d.ID)
	}
	if e.name != "Dishwasher" || e.category == nil || *e.category != "appliance" {
		t.Errorf("name/category = %q/%v", e.name, e.category)
	}

	// An asset with no category keeps the key and carries null, the shape the
	// API repo produced (the Go nil pointer marshals to null). asset_create
	// requires a category, so this row is inserted the way the API's own
	// writes reach the table -- the trigger, not the caller, decides.
	ring := insertID(t, f.pool, `INSERT INTO assets (household_id, name) VALUES ($1, 'Wedding ring') RETURNING id`, f.hh.HouseholdID)
	rows := outboxFor(t, context.Background(), f, ring)
	if len(rows) != 1 {
		t.Fatalf("%d outbox events for a category-less asset, want 1", len(rows))
	}
	var nullCategory bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT payload ? 'category' AND payload->'category' = 'null'::jsonb
		   FROM outbox_events WHERE payload->>'asset_id' = $1`, ring).Scan(&nullCategory); err != nil {
		t.Fatalf("read category: %v", err)
	}
	if !nullCategory {
		t.Error("payload category is not null for an asset with no category")
	}

	// The duplicate guard returns the existing row instead of writing, so it
	// must not enqueue a second event.
	call(t, f.ctx, f.create, "asset_create", map[string]any{"name": "dishwasher", "category": "Appliance"})
	if n := len(outboxFor(t, context.Background(), f, d.ID)); n != 1 {
		t.Errorf("%d outbox events after the duplicate-guard call, want still 1", n)
	}
}

// TestAssetUpdateEnqueuesOutbox proves asset_update (the MCP write path)
// leaves exactly one asset.updated event per call, with the asset's values
// after the change.
func TestAssetUpdateEnqueuesOutbox(t *testing.T) {
	f := setup(t)
	d := call(t, f.ctx, f.create, "asset_create", map[string]any{"name": "Grill", "category": "outdoor"}).asset(t)
	before := len(outboxFor(t, context.Background(), f, d.ID))

	call(t, f.ctx, f.upd, "asset_update", map[string]any{"asset_id": d.ID, "current_value": 300, "name": "Gas grill"})

	got := outboxFor(t, context.Background(), f, d.ID)
	if len(got) != before+1 {
		t.Fatalf("%d outbox events after one update, want %d: %+v", len(got), before+1, got)
	}
	e := got[len(got)-1]
	if e.type_ != "asset.updated" || e.name != "Gas grill" || e.assetID != d.ID {
		t.Errorf("update event = %+v, want asset.updated for Gas grill", e)
	}
}

// TestAssetOutboxIsPerHousehold proves an event is filed under the writing
// household, so a consumer polling one family never sees another's assets.
func TestAssetOutboxIsPerHousehold(t *testing.T) {
	f := setup(t)
	other := f.otherHousehold(t)
	id := insertID(t, f.pool, `INSERT INTO assets (household_id, name, category) VALUES ($1, 'Piano', 'music') RETURNING id`, other)

	got := outboxFor(t, context.Background(), f, id)
	if len(got) != 1 {
		t.Fatalf("%d outbox events, want 1", len(got))
	}
	if got[0].householdID != other.String() {
		t.Errorf("household = %s, want the asset's own %s", got[0].householdID, other)
	}
	var leaked int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM outbox_events WHERE household_id <> $1 AND payload->>'asset_id' = $2`,
		got[0].householdID, id).Scan(&leaked); err != nil {
		t.Fatalf("count: %v", err)
	}
	if leaked != 0 {
		t.Errorf("%d events filed under another household", leaked)
	}
}
