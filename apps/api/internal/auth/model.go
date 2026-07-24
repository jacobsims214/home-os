// Package auth handles user authentication, authorization, and membership.
// It provides the domain models and database repository for the users and
// memberships tables.
package auth

import (
	"time"

	"github.com/google/uuid"
)

// User represents a registered user from the users table.
type User struct {
	ID               uuid.UUID
	Email            string
	Name             string
	PasswordHash     string
	CaldavPasswordHash *string
	AvatarURL        *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Membership represents a user's membership in a household from the memberships table.
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
