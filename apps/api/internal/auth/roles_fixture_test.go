package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestRolePolicyMatchesFixture keeps the API's policy table and
// packages/db/testdata/household_role_policy.json in step (#4717). The MCP
// mirrors this table in apps/mcp/internal/auth/policy.go and asserts the same
// fixture, so the two apps cannot drift apart without one of the two tests
// failing. Edit roles.go, policy.go and the fixture together (and KB
// decisions/2026-11-15-role-policy.md).
func TestRolePolicyMatchesFixture(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "..", "..", "packages", "db", "testdata", "household_role_policy.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var f struct {
		Roles           []string                       `json:"roles"`
		Policy          map[string]map[string][]string `json:"policy"`
		EntityResources map[string]string              `json:"entity_resources"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(f.Policy) < 20 || len(f.EntityResources) < 20 || len(f.Roles) != 6 {
		t.Fatalf("%s looks truncated: %d resources, %d kinds, %d roles", path, len(f.Policy), len(f.EntityResources), len(f.Roles))
	}

	if !slices.Equal(f.Roles, AllRoles) {
		t.Errorf("AllRoles = %v, fixture roles = %v", AllRoles, f.Roles)
	}
	for res, acts := range f.Policy {
		mine, ok := policy[Resource(res)]
		if !ok {
			t.Errorf("resource %q is in the fixture but not in policy", res)
			continue
		}
		if len(mine) != len(acts) {
			t.Errorf("resource %q: table has %d actions, fixture has %d", res, len(mine), len(acts))
		}
		for act, roles := range acts {
			got, ok := mine[Action(act)]
			if !ok {
				t.Errorf("%s.%s is in the fixture but not in policy", res, act)
				continue
			}
			if !slices.Equal(got, roles) {
				t.Errorf("%s.%s = %v, fixture says %v", res, act, got, roles)
			}
		}
	}
	for res, acts := range policy {
		if _, ok := f.Policy[string(res)]; !ok {
			t.Errorf("policy has resource %q that the fixture does not", res)
			continue
		}
		for act := range acts {
			if _, ok := f.Policy[string(res)][string(act)]; !ok {
				t.Errorf("policy has %s.%s that the fixture does not", res, act)
			}
		}
	}
	if len(entityResources) != len(f.EntityResources) {
		t.Errorf("entityResources has %d kinds, fixture has %d", len(entityResources), len(f.EntityResources))
	}
	for kind, res := range f.EntityResources {
		got, ok := entityResources[kind]
		if !ok {
			t.Errorf("entity kind %q is in the fixture but not in entityResources", kind)
			continue
		}
		if string(got) != res {
			t.Errorf("entityResources[%q] = %q, fixture says %q", kind, got, res)
		}
	}
	for kind := range entityResources {
		if _, ok := f.EntityResources[kind]; !ok {
			t.Errorf("entityResources has kind %q that the fixture does not", kind)
		}
	}
}
