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
