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
