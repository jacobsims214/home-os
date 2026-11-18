package asset

import (
	"context"
	"strings"
	"testing"
)

// TestGuideHints (#4770): asset_find on one asset hints guide_create to a
// family role while the asset has no guide, guide_find to any guide reader
// once it has one, and nothing to a vendor. asset_create hints guide_create
// to family roles only.
func TestGuideHints(t *testing.T) {
	f := setup(t)
	setRole := func(role string) {
		t.Helper()
		if _, err := f.pool.Exec(context.Background(), `UPDATE memberships SET role = $3 WHERE household_id = $1 AND user_id = $2`,
			f.hh.HouseholdID, f.hh.UserID, role); err != nil {
			t.Fatal(err)
		}
	}
	hints := func(e wireEnvelope) (create, find bool) {
		all := strings.Join(e.Next, "\n")
		return strings.Contains(all, "guide_create about_type=asset"), strings.Contains(all, "guide_find about_type=asset")
	}

	created := call(t, f.ctx, f.create, "asset_create", map[string]any{"name": "Furnace", "category": "hvac"})
	furnace := created.asset(t).ID
	if c, _ := hints(created); !c || !strings.Contains(strings.Join(created.Next, " "), "guide_create about_type=asset about_id="+furnace+" title=… body=…") {
		t.Errorf("asset_create as owner: want guide_create hint, got %v", created.Next)
	}

	get := func() wireEnvelope { return call(t, f.ctx, f.find, "asset_find", map[string]any{"id": furnace}) }
	for _, tc := range []struct {
		role         string
		create, find bool
	}{
		{"owner", true, false},
		{"family_member", true, false},
		{"viewer", false, false},
		{"housesitter", false, false},
		{"vendor", false, false},
	} {
		setRole(tc.role)
		if c, fd := hints(get()); c != tc.create || fd != tc.find {
			t.Errorf("no guide, %s: guide_create=%v guide_find=%v, want %v %v", tc.role, c, fd, tc.create, tc.find)
		}
	}

	setRole("viewer")
	viewerCreate := call(t, f.ctx, f.create, "asset_create", map[string]any{"name": "Boiler", "category": "hvac"})
	if c, _ := hints(viewerCreate); c {
		t.Errorf("asset_create as viewer: want no guide_create hint, got %v", viewerCreate.Next)
	}

	if _, err := f.pool.Exec(context.Background(), `INSERT INTO guides (household_id, asset_id, title) VALUES ($1, $2, 'Replace the filter')`,
		f.hh.HouseholdID, furnace); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		role         string
		create, find bool
	}{
		{"owner", false, true},
		{"viewer", false, true},
		{"housesitter", false, true},
		{"vendor", false, false},
	} {
		setRole(tc.role)
		e := get()
		if c, fd := hints(e); c != tc.create || fd != tc.find {
			t.Errorf("with guide, %s: guide_create=%v guide_find=%v, want %v %v (%v)", tc.role, c, fd, tc.create, tc.find, e.Next)
		}
	}
	setRole("owner")
	if e := get(); !strings.Contains(strings.Join(e.Next, " "), "guide_find about_type=asset about_id="+furnace) {
		t.Errorf("guide_find hint should carry the real asset id: %v", e.Next)
	}
	// A list of several assets carries no guide hint (no per-row checks).
	if e := call(t, f.ctx, f.find, "asset_find", map[string]any{}); strings.Contains(strings.Join(e.Next, " "), "guide_") {
		t.Errorf("list of assets: want no guide hint, got %v", e.Next)
	}
}
