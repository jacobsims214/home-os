package invite

import "time"

type Invitation struct {
	ID          string     `json:"id"`
	HouseholdID string     `json:"household_id"`
	Email       string     `json:"email"`
	Token       string     `json:"-"`
	Role        string     `json:"role"`
	InvitedBy   string     `json:"invited_by"`
	ExpiresAt   time.Time  `json:"expires_at"`
	AcceptedAt  *time.Time `json:"accepted_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}
