// Package property manages properties and rooms.
// Properties represent physical locations (homes, rentals) that belong to a
// household. Rooms are organized under properties. Every query is scoped to
// the household_id extracted from JWT claims.
package property

import (
	"time"

	"github.com/google/uuid"
)

// Property represents a physical location from the properties table.
type Property struct {
	ID           uuid.UUID
	HouseholdID  uuid.UUID
	Name         string
	Address      *string
	PropertyType *string
	Notes        *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Room represents a room within a property from the rooms table.
type Room struct {
	ID         uuid.UUID
	PropertyID uuid.UUID
	Name       string
	Floor      *int
	Notes      *string
	CreatedAt  time.Time
}
