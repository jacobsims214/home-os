package search

import (
	"sort"
	"testing"

	"home-os/mcp/internal/testutil"
)

// #4799: search_all's maintenance area for a vendor linked to a vendors row
// (memberships.vendor_id) holds only that vendor's jobs; an unlinked vendor
// still finds every maintenance task. Other areas a vendor may search
// (properties, rooms, assets) are not narrowed.
func TestSearchAllLinkedVendorOwnJobs(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, h := testutil.SeedHousehold(t, pool)

	plumber := mustExec(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Plumber') RETURNING id::text`, h.HouseholdID)
	electrician := mustExec(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Electrician') RETURNING id::text`, h.HouseholdID)
	own := mustExec(t, pool, `INSERT INTO maintenance_tasks (household_id, name, vendor_id) VALUES ($1, 'Wombat sink', $2) RETURNING id::text`, h.HouseholdID, plumber)
	theirs := mustExec(t, pool, `INSERT INTO maintenance_tasks (household_id, name, vendor_id) VALUES ($1, 'Wombat outlet', $2) RETURNING id::text`, h.HouseholdID, electrician)
	open := mustExec(t, pool, `INSERT INTO maintenance_tasks (household_id, name) VALUES ($1, 'Wombat gutters') RETURNING id::text`, h.HouseholdID)
	home := mustExec(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Wombat cottage') RETURNING id::text`, h.HouseholdID)

	set := func(role string, vendor any) {
		t.Helper()
		mustExec(t, pool, `UPDATE memberships SET role = $3, vendor_id = $4 WHERE household_id = $1 AND user_id = $2 RETURNING role::text`,
			h.HouseholdID, h.UserID, role, vendor)
	}
	all := []string{own, theirs, open}

	for _, c := range []struct {
		name   string
		role   string
		vendor any
		want   []string
	}{
		{"owner", "owner", nil, all},
		{"unlinked vendor", "vendor", nil, all},
		{"linked vendor", "vendor", plumber, []string{own}},
		{"family_member with a stray link", "family_member", plumber, all},
	} {
		t.Run(c.name, func(t *testing.T) {
			set(c.role, c.vendor)
			env := search(t, ctx, pool, map[string]any{"query": "wombat"})
			got := ids(group(env, "maintenance"))
			want := append([]string{}, c.want...)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("maintenance hits: got %v, want %v", got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("maintenance hits: got %v, want %v", got, want)
				}
			}
			if g := ids(group(env, "property")); len(g) != 1 || g[0] != home {
				t.Fatalf("property hits: got %v, want [%s]", g, home)
			}
		})
	}
}
