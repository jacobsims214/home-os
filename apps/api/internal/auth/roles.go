package auth

// Household role rules. They decide who may change a household's membership
// (invite, change roles, remove members) and which roles they may hand out.
//
//   - Only owner and family_manager manage members. family_member, viewer,
//     housesitter and vendor may not invite, list invitations, change roles
//     or remove anyone.
//   - An owner may grant any role.
//   - A family_manager may grant any role except owner, and may not change or
//     remove an owner.
//
// Callers must pass the actor's role in the household the request is for
// (the X-Household-ID-resolved one), read from memberships.

// validRoles mirrors the membership_role enum (migration 001).
var validRoles = map[string]bool{
	RoleOwner: true, RoleFamilyManager: true, RoleFamilyMember: true,
	RoleViewer: true, RoleHousesitter: true, RoleVendor: true,
}

// ValidRole reports whether role is a membership_role enum value.
func ValidRole(role string) bool { return validRoles[role] }

// CanManageMembers reports whether a member with role may invite people,
// list invitations, change roles and remove members.
func CanManageMembers(role string) bool {
	return role == RoleOwner || role == RoleFamilyManager
}

// CanGrantRole reports whether a member with actorRole may give someone
// target (by invitation or a role change). target must also be valid.
func CanGrantRole(actorRole, target string) bool {
	if !ValidRole(target) || !CanManageMembers(actorRole) {
		return false
	}
	if target == RoleOwner {
		return actorRole == RoleOwner
	}
	return true
}

// CanModifyMember reports whether a member with actorRole may change the role
// of, or remove, a member whose current role is targetCurrent.
func CanModifyMember(actorRole, targetCurrent string) bool {
	if !CanManageMembers(actorRole) {
		return false
	}
	if targetCurrent == RoleOwner {
		return actorRole == RoleOwner
	}
	return true
}
