package notification

import "time"

// Notification represents a user notification in the system.
type Notification struct {
	ID           string     `json:"id"`
	HouseholdID  string     `json:"household_id"`
	Type         string     `json:"type"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	EntityType   *string    `json:"entity_type,omitempty"`
	EntityID     *string    `json:"entity_id,omitempty"`
	ReadAt       *time.Time `json:"read_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}
