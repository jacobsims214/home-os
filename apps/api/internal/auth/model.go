// Package auth handles user authentication, authorization, and membership.
// It provides OIDC token verification, user management, and role-based access control.
package auth

import (
	"time"

	"github.com/google/uuid"
)

// Claims represents the enriched claims extracted from a verified OIDC token.
// UserID, HouseholdID, and Role are populated from the database after token
// verification via JIT provisioning. Email and Name come from the OIDC identity.
//
// Handlers access the injected Claims via ClaimsFromContext.
type Claims struct {
	UserID      string `json:"user_id"`
	HouseholdID string `json:"household_id"`
	Role        string `json:"role"`
	Email       string `json:"email"`
	Name        string `json:"name"`
}

// User represents a registered user from the users table.
type User struct {
	ID                 uuid.UUID
	Email              string
	Name               string
	PasswordHash       string
	CaldavPasswordHash *string
	AvatarURL          *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// IsInstanceAdmin is users.is_instance_admin (migration 037): someone
	// who administers the whole instance (the admin routes: families and
	// users). It is a property of the USER, a different axis from
	// Membership.Role: it grants no extra rights inside a household they
	// belong to, and no access at all to one they are not a member of.
	IsInstanceAdmin bool
}

// Membership represents a user's membership in a household from the memberships table.
// Its Role is the household role, a different axis from User.IsInstanceAdmin.
type Membership struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	UserID      uuid.UUID
	Role        string
	CreatedAt   time.Time
}

// Membership roles matching the membership_role PostgreSQL enum.
const (
	RoleOwner         = "owner"
	RoleFamilyManager = "family_manager"
	RoleFamilyMember  = "family_member"
	RoleViewer        = "viewer"
	RoleHousesitter   = "housesitter"
	RoleVendor        = "vendor"
)
