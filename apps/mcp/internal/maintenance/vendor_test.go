package maintenance

import (
	"sort"
	"testing"

	"home-os/mcp/internal/testutil"
)

// #4799: a vendor login linked to a vendors row (memberships.vendor_id) finds
// only that vendor's upkeep tasks; an unlinked vendor and family roles find
// every task. Role and link are re-read from memberships on every call.
func TestMaintenanceFindLinkedVendorSeesOwnJobs(t *testing.T) {
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool, "America/Chicago")
	var userID string
	if err := pool.QueryRow(f.ctx, `SELECT user_id::text FROM memberships WHERE household_id = $1`, f.id).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	plumber := insertID(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Plumber') RETURNING id`, f.id)
	electrician := insertID(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Electrician') RETURNING id`, f.id)
	sink := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, vendor_id) VALUES ($1, 'Fix sink', $2) RETURNING id`, f.id, plumber)
	heater := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, vendor_id, status) VALUES ($1, 'Flush heater', $2, 'done') RETURNING id`, f.id, plumber)
	outlet := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, vendor_id) VALUES ($1, 'Replace outlet', $2) RETURNING id`, f.id, electrician)
	gutters := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name) VALUES ($1, 'Clean gutters') RETURNING id`, f.id)

	set := func(role string, vendor any) {
		t.Helper()
		exec(t, pool, `UPDATE memberships SET role = $3, vendor_id = $4 WHERE household_id = $1 AND user_id = $2`, f.id, userID, role, vendor)
	}
	found := func(args map[string]any) []string {
		t.Helper()
		env := mustOK(t, f.ctx, pool, "maintenance_find", args)
		out := []string{}
		for _, it := range env.Items {
			out = append(out, it.ID)
		}
		sort.Strings(out)
		return out
	}
	sorted := func(s ...string) []string { sort.Strings(s); return s }
	all := sorted(sink, heater, outlet, gutters)

	for _, c := range []struct {
		name   string
		role   string
		vendor any
		want   []string
	}{
		{"owner", "owner", nil, all},
		{"unlinked vendor", "vendor", nil, all},
		{"plumber", "vendor", plumber, sorted(sink, heater)},
		{"electrician", "vendor", electrician, []string{outlet}},
		// The column is ignored for other roles (it is cleared by the API).
		{"family_member with a stray link", "family_member", plumber, all},
	} {
		t.Run(c.name, func(t *testing.T) {
			set(c.role, c.vendor)
			if got := found(map[string]any{"status": "all"}); !equal(got, c.want) {
				t.Fatalf("status=all: got %v, want %v", got, c.want)
			}
			for _, id := range all {
				env := call(t, f.ctx, pool, "maintenance_find", map[string]any{"id": id})
				visible := contains(c.want, id)
				if env.OK != visible {
					t.Fatalf("get %s: ok=%v, want %v (%s)", id, env.OK, visible, env.Summary)
				}
			}
		})
	}

	// Filters combine with the scope: pending only, query words.
	set("vendor", plumber)
	if got := found(map[string]any{}); !equal(got, []string{sink}) {
		t.Fatalf("pending: got %v", got)
	}
	if got := found(map[string]any{"query": "outlet", "status": "all"}); len(got) != 0 {
		t.Fatalf("query for another vendor's job: got %v", got)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
