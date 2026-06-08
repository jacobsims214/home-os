// Package asset manages household assets (HVAC, appliances, vehicles, etc.).
// It provides the domain model and database repository for the assets table.
package asset

import (
	"time"

	"github.com/google/uuid"
)

// Asset represents a physical asset belonging to a household.
type Asset struct {
	ID             uuid.UUID
	HouseholdID    uuid.UUID
	PropertyID     *uuid.UUID
	RoomID         *uuid.UUID
	Name           string
	Category       *string
	Manufacturer   *string
	Model          *string
	SerialNumber   *string
	PurchaseDate   *time.Time
	PurchasePrice  *float64
	WarrantyExpiry *time.Time
	Notes          *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
