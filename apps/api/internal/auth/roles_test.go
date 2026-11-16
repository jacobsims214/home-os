package auth

import "testing"

func TestRoleRules(t *testing.T) {
	roles := []string{RoleOwner, RoleFamilyManager, RoleFamilyMember, RoleViewer, RoleHousesitter, RoleVendor}
	for _, actor := range roles {
		manager := actor == RoleOwner || actor == RoleFamilyManager
		if got := CanManageMembers(actor); got != manager {
			t.Errorf("CanManageMembers(%q) = %v, want %v", actor, got, manager)
		}
		for _, target := range roles {
			wantGrant := actor == RoleOwner || (actor == RoleFamilyManager && target != RoleOwner)
			if got := CanGrantRole(actor, target); got != wantGrant {
				t.Errorf("CanGrantRole(%q, %q) = %v, want %v", actor, target, got, wantGrant)
			}
			if got := CanModifyMember(actor, target); got != wantGrant {
				t.Errorf("CanModifyMember(%q, %q) = %v, want %v", actor, target, got, wantGrant)
			}
		}
	}
	for _, bad := range []string{"", "admin", "OWNER"} {
		if CanGrantRole(RoleOwner, bad) || ValidRole(bad) {
			t.Errorf("role %q must be invalid", bad)
		}
		if CanManageMembers(bad) {
			t.Errorf("CanManageMembers(%q) must be false", bad)
		}
	}
}

// TestPolicy_HouseholdWrite: everyone reads the household record, but only
// owner and family_manager may change it (#4820). Renaming the family and
// changing its timezone decides what every date means for every member and
// agent, so it is a management act, not an ordinary family write: a
// family_member, viewer, housesitter and vendor are refused, and so is an
// unknown role.
func TestPolicy_HouseholdWrite(t *testing.T) {
	for _, role := range AllRoles {
		manages := role == RoleOwner || role == RoleFamilyManager
		if got := Can(role, ResHousehold, ActWrite); got != manages {
			t.Errorf("Can(%s, household, write) = %v, want %v", role, got, manages)
		}
		if !Can(role, ResHousehold, ActRead) {
			t.Errorf("Can(%s, household, read) = false: every member reads its own family", role)
		}
	}
	for _, bad := range []string{"", "admin", "OWNER", "instance_admin"} {
		if Can(bad, ResHousehold, ActWrite) {
			t.Errorf("unknown role %q may write the household", bad)
		}
	}
	// The grant is a management one: every role that may change the household
	// may also manage members, which a family_member (who may write every
	// other family record) may not.
	for _, role := range AllRoles {
		if Can(role, ResHousehold, ActWrite) && !CanManageMembers(role) {
			t.Errorf("%s may write the household but not manage members", role)
		}
	}
}

// TestPolicy_Entities checks the record-level helpers of the household role
// policy (#4689).
func TestPolicy_Entities(t *testing.T) {
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
		{RoleHousesitter, "household", false, false},
		{RoleHousesitter, "file", false, false},
		{RoleVendor, "maintenance_task", true, false},
		{RoleVendor, "asset", true, false},
		{RoleVendor, "pet", false, false},
		{RoleVendor, "calendar_event", false, false},
		// Unknown kinds fail closed for every role outside the family.
		{RoleOwner, "something_new", true, true},
		{RoleViewer, "something_new", false, false},
		{RoleHousesitter, "something_new", false, false},
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
	for _, role := range []string{"", "admin"} {
		if Can(role, ResHousehold, ActRead) {
			t.Errorf("unknown role %q allowed", role)
		}
	}
	// Every resource the table names has at least read or a narrower grant.
	for res, acts := range policy {
		if len(acts) == 0 {
			t.Errorf("resource %s has no grants", res)
		}
	}
}
