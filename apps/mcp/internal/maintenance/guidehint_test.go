package maintenance

import (
	"strings"
	"testing"

	"home-os/mcp/internal/testutil"
)

// TestGuideHint (#4770): maintenance_find for one task hints guide_find
// about the task's subject only when a guide about it exists, and only to
// roles that read guides (not a vendor).
func TestGuideHint(t *testing.T) {
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool, "America/New_York")
	house := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Main House') RETURNING id`, f.id)
	furnace := insertID(t, pool, `INSERT INTO assets (household_id, property_id, name) VALUES ($1, $2, 'Furnace') RETURNING id`, f.id, house)
	car := insertID(t, pool, `INSERT INTO vehicles (household_id, make) VALUES ($1, 'Honda') RETURNING id`, f.id)
	filter := mustOK(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "Replace furnace filter", "due_date": f.day(7), "asset_id": furnace}).Data.ID
	oil := mustOK(t, f.ctx, pool, "maintenance_create", map[string]any{"name": "Oil change", "due_date": f.day(9), "vehicle_id": car}).Data.ID
	var userID string
	if err := pool.QueryRow(f.ctx, `SELECT user_id::text FROM memberships WHERE household_id = $1`, f.id).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	setRole := func(role string) {
		t.Helper()
		exec(t, pool, `UPDATE memberships SET role = $3 WHERE household_id = $1 AND user_id = $2`, f.id, userID, role)
	}
	hint := func(args map[string]any) string {
		t.Helper()
		for _, n := range mustOK(t, f.ctx, pool, "maintenance_find", args).Next {
			if strings.Contains(n, "guide_find") {
				return n
			}
		}
		return ""
	}

	// No guide yet: no hint.
	if h := hint(map[string]any{"id": filter}); h != "" {
		t.Errorf("no guide: want no hint, got %q", h)
	}
	// A guide about the house is not about the furnace.
	exec(t, pool, `INSERT INTO guides (household_id, property_id, title) VALUES ($1, $2, 'Winterize')`, f.id, house)
	if h := hint(map[string]any{"id": filter}); h != "" {
		t.Errorf("guide about the house only: want no hint, got %q", h)
	}

	exec(t, pool, `INSERT INTO guides (household_id, asset_id, title) VALUES ($1, $2, 'Replace the filter')`, f.id, furnace)
	want := "Steps for this job: guide_find about_type=asset about_id=" + furnace + "."
	for _, tc := range []struct {
		role string
		want string
	}{
		{"owner", want},
		{"viewer", want},
		{"housesitter", want},
		{"vendor", ""},
	} {
		setRole(tc.role)
		if h := hint(map[string]any{"id": filter}); h != tc.want {
			t.Errorf("%s: hint = %q, want %q", tc.role, h, tc.want)
		}
	}
	setRole("owner")

	// maintenance_find that lists exactly one task gets the hint; several don't.
	if h := hint(map[string]any{"asset_id": furnace}); h != want {
		t.Errorf("find one task: hint = %q, want %q", h, want)
	}
	if h := hint(map[string]any{}); h != "" {
		t.Errorf("find several tasks: want no hint, got %q", h)
	}

	// A car task points at guides about the car, once one exists.
	if h := hint(map[string]any{"id": oil}); h != "" {
		t.Errorf("car without guide: want no hint, got %q", h)
	}
	exec(t, pool, `INSERT INTO guides (household_id, vehicle_id, title) VALUES ($1, $2, 'Oil change steps')`, f.id, car)
	if h := hint(map[string]any{"id": oil}); h != "Steps for this job: guide_find about_type=vehicle about_id="+car+"." {
		t.Errorf("car with guide: hint = %q", h)
	}
}
