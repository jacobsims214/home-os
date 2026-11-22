package auth

import (
	"slices"
	"testing"
)

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

// TestPolicy_PersonColor: a person's colour is a cosmetic, self-service
// setting, so it gets its own action on the people resource rather than
// riding on ActWrite (#4821). Two things are pinned here:
//
//   - the route-level grant is exactly the family roles (owner,
//     family_manager, family_member): a viewer, housesitter or vendor may not
//     recolour anyone, not even their own record;
//   - every role that may set a colour may also write a people record, so the
//     action can never become a way around the people write rules. The
//     record-level narrowing (self-or-manager) is the handler's decision, on
//     top of this grant, and is tested end to end in
//     internal/people/handler_test.go.
func TestPolicy_PersonColor(t *testing.T) {
	for _, role := range AllRoles {
		sets := role == RoleOwner || role == RoleFamilyManager || role == RoleFamilyMember
		if got := Can(role, ResPeople, ActSetColor); got != sets {
			t.Errorf("Can(%s, people, set_color) = %v, want %v", role, got, sets)
		}
		if got := Can(role, ResPeople, ActSetColor); got && !Can(role, ResPeople, ActWrite) {
			t.Errorf("%s may set a person's colour but may not write a people record", role)
		}
	}
	for _, bad := range []string{"", "admin", "OWNER", "instance_admin"} {
		if Can(bad, ResPeople, ActSetColor) {
			t.Errorf("unknown role %q may set a person's colour", bad)
		}
	}
	// The colour is visible to everyone who may see a person at all, and
	// setting it grants nothing else on the record: a family_member may
	// recolour, but still cannot manage members.
	for _, role := range AllRoles {
		if !Can(role, ResPeople, ActSetColor) {
			continue
		}
		if !Can(role, ResPeople, ActReadBasic) {
			t.Errorf("%s may set a colour but may not read people", role)
		}
	}
	if Can(RoleFamilyMember, ResMembers, ActWrite) {
		t.Error("family_member may set a colour and manage members: the colour grant must not widen")
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
	// A viewer is kept from credentials AND from the membership audit (#5518):
	// who granted, demoted or removed whom is management data, not family news.
	if hidden := HiddenEntityKinds(RoleViewer); len(hidden) != 2 ||
		!slices.Contains(hidden, "secret") || !slices.Contains(hidden, "membership") {
		t.Errorf("viewer hidden kinds = %v, want [secret membership]", hidden)
	}
	if hidden := HiddenEntityKinds(RoleOwner); len(hidden) != 0 {
		t.Errorf("owner hidden kinds = %v, want none", hidden)
	}
	// The membership audit must be hidden from everyone a demotion could be
	// about who is not a manager.
	for _, role := range []string{RoleFamilyMember, RoleViewer, RoleHousesitter, RoleVendor} {
		if CanSeeEntity(role, "membership") {
			t.Errorf("%s can read the membership audit; it is owner/family_manager only", role)
		}
		// members.read is unchanged (rolesSitter): a housesitter may still list
		// the family's members. Only the AUDIT of how they got their roles is
		// managers-only. Asserted so the two are not collapsed later.
		if !Can(role, ResMembers, ActRead) && role == RoleHousesitter {
			t.Errorf("%s can no longer read members; the audit decision must not have narrowed members", role)
		}
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
