package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

// fixture is packages/db/testdata/household_role_policy.json: the shared
// contract both apps' tables must equal.
type fixture struct {
	Note            string                         `json:"note"`
	Roles           []string                       `json:"roles"`
	Policy          map[string]map[string][]string `json:"policy"`
	EntityResources map[string]string              `json:"entity_resources"`
}

// loadFixture reads the shared policy fixture.
func loadFixture(t *testing.T) fixture {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "packages", "db", "testdata", "household_role_policy.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(f.Policy) < 20 || len(f.EntityResources) < 20 || len(f.Roles) != 6 {
		t.Fatalf("%s looks truncated: %d resources, %d kinds, %d roles", path, len(f.Policy), len(f.EntityResources), len(f.Roles))
	}
	return f
}

// TestRolePolicyMatchesFixture is the parity test between this package's
// table and the API's (#4717): both are asserted against the same fixture, so
// neither can drift from the other without a test failing. A resource or
// action present on one side and missing on the other fails; so does a role
// list that differs in content or order.
func TestRolePolicyMatchesFixture(t *testing.T) {
	t.Parallel()
	f := loadFixture(t)

	if !slices.Equal(f.Roles, AllRoles) {
		t.Errorf("AllRoles = %v, fixture roles = %v", AllRoles, f.Roles)
	}

	// Every resource in the fixture is in the table, with the same actions.
	for res, acts := range f.Policy {
		mine, ok := Policy[Resource(res)]
		if !ok {
			t.Errorf("resource %q is in the fixture but not in Policy", res)
			continue
		}
		if len(mine) != len(acts) {
			t.Errorf("resource %q: table has %d actions, fixture has %d", res, len(mine), len(acts))
		}
		for act, roles := range acts {
			got, ok := mine[Action(act)]
			if !ok {
				t.Errorf("%s.%s is in the fixture but not in Policy", res, act)
				continue
			}
			if !slices.Equal(got, roles) {
				t.Errorf("%s.%s = %v, fixture says %v", res, act, got, roles)
			}
		}
	}
	// And nothing extra: a grant the fixture does not have would let a role
	// do something the API refuses.
	for res, acts := range Policy {
		if _, ok := f.Policy[string(res)]; !ok {
			t.Errorf("Policy has resource %q that the fixture does not", res)
			continue
		}
		for act := range acts {
			if _, ok := f.Policy[string(res)][string(act)]; !ok {
				t.Errorf("Policy has %s.%s that the fixture does not", res, act)
			}
		}
	}

	// Entity kinds map to the same resources.
	if len(EntityResources) != len(f.EntityResources) {
		t.Errorf("EntityResources has %d kinds, fixture has %d", len(EntityResources), len(f.EntityResources))
	}
	for kind, res := range f.EntityResources {
		got, ok := EntityResources[kind]
		if !ok {
			t.Errorf("entity kind %q is in the fixture but not in EntityResources", kind)
			continue
		}
		if string(got) != res {
			t.Errorf("EntityResources[%q] = %q, fixture says %q", kind, got, res)
		}
	}
	for kind := range EntityResources {
		if _, ok := f.EntityResources[kind]; !ok {
			t.Errorf("EntityResources has kind %q that the fixture does not", kind)
		}
	}
}

// TestCanMatchesTable re-derives Can from Policy for every role x resource x
// action, so the helper cannot disagree with the table it reads.
func TestCanMatchesTable(t *testing.T) {
	t.Parallel()
	actions := []Action{ActRead, ActWrite, ActReadBasic, ActManage, ActEditItems, ActAddItems,
		ActCheckItems, ActAnnotate, ActUpdateStatus, ActRateOthers, ActCreate, ActWriteScope,
		ActRevoke, ActMarkRead}
	for _, role := range append(append([]string{}, AllRoles...), "", "something_else") {
		for res, acts := range Policy {
			for _, act := range actions {
				want := false
				for _, r := range acts[act] {
					if r == role {
						want = true
					}
				}
				if got := Can(role, res, act); got != want {
					t.Errorf("Can(%q, %s, %s) = %v, table says %v", role, res, act, got, want)
				}
			}
		}
	}
	// Unknown resources and actions are denied for everyone.
	for _, role := range AllRoles {
		if Can(role, "not_a_resource", ActRead) || Can(role, ResBills, "not_an_action") {
			t.Errorf("unknown resource/action allowed for %s", role)
		}
	}
}

// TestCanSeeMoneyMatchesPolicy re-derives CanSeeMoney from Can (#4806), so a
// table edit and the helper cannot drift.
func TestCanSeeMoneyMatchesPolicy(t *testing.T) {
	t.Parallel()
	for _, role := range append(append([]string{}, AllRoles...), "", "something_else") {
		want := Can(role, ResBills, ActRead) && Can(role, ResLoans, ActRead)
		if got := CanSeeMoney(role); got != want {
			t.Errorf("CanSeeMoney(%q) = %v, want %v", role, got, want)
		}
	}
	for _, role := range []string{RoleHousesitter, RoleVendor, "", "something_else"} {
		if CanSeeMoney(role) {
			t.Errorf("CanSeeMoney(%q) must be false: money is not shared with that role", role)
		}
	}
	for _, role := range []string{RoleOwner, RoleFamilyManager, RoleFamilyMember, RoleViewer} {
		if !CanSeeMoney(role) {
			t.Errorf("CanSeeMoney(%q) must be true", role)
		}
	}
}

// TestPolicyRolesAreValid fails on a typo in a role list: a misspelled role
// would silently grant nothing (or, worse, look like a grant in review).
func TestPolicyRolesAreValid(t *testing.T) {
	t.Parallel()
	for res, acts := range Policy {
		if len(acts) == 0 {
			t.Errorf("resource %q has no actions", res)
		}
		for act, roles := range acts {
			for _, r := range roles {
				if !slices.Contains(AllRoles, r) {
					t.Errorf("%s.%s names unknown role %q", res, act, r)
				}
			}
			if !sort.SliceIsSorted(roles, func(i, j int) bool {
				return slices.Index(AllRoles, roles[i]) < slices.Index(AllRoles, roles[j])
			}) {
				t.Errorf("%s.%s = %v is not in AllRoles order", res, act, roles)
			}
		}
	}
}

// TestEntityHelpers pins the record-level helpers the MCP relies on: the
// kinds each outsider role may see, and who may annotate what.
func TestEntityHelpers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		role, kind    string
		see, annotate bool
	}{
		{RoleOwner, "secret", true, true},
		{RoleFamilyMember, "bill", true, true},
		{RoleViewer, "secret", false, false},
		{RoleViewer, "bill", true, false},
		{RoleViewer, "household", true, false},
		{RoleHousesitter, "pet", true, true},
		{RoleHousesitter, "property", true, false},
		{RoleHousesitter, "bill", false, false},
		{RoleHousesitter, "person", false, false},
		{RoleHousesitter, "vehicle", false, false},
		{RoleHousesitter, "file", false, false},
		{RoleHousesitter, "household", false, false},
		{RoleVendor, "maintenance_task", true, false},
		{RoleVendor, "asset", true, false},
		{RoleVendor, "pet", false, false},
		{RoleVendor, "bill", false, false},
		{RoleVendor, "calendar_event", false, false},
		// Unknown kinds fail closed outside the family roles.
		{RoleOwner, "something_new", true, true},
		{RoleViewer, "something_new", false, false},
		{RoleHousesitter, "something_new", false, false},
		{RoleVendor, "something_new", false, false},
	}
	for _, tc := range tests {
		if got := CanSeeEntity(tc.role, tc.kind); got != tc.see {
			t.Errorf("CanSeeEntity(%s, %s) = %v, want %v", tc.role, tc.kind, got, tc.see)
		}
		if got := CanAnnotateEntity(tc.role, tc.kind); got != tc.annotate {
			t.Errorf("CanAnnotateEntity(%s, %s) = %v, want %v", tc.role, tc.kind, got, tc.annotate)
		}
	}
	if hidden := HiddenEntityKinds(RoleViewer); len(hidden) != 1 || hidden[0] != "secret" {
		t.Errorf("viewer hidden kinds = %v, want [secret]", hidden)
	}
	if hidden := HiddenEntityKinds(RoleOwner); len(hidden) != 0 {
		t.Errorf("owner hidden kinds = %v, want none", hidden)
	}
}
