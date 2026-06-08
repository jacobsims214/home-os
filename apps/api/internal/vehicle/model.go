// Package vehicle manages vehicle records.
// Vehicles are household-scoped entities that track make, model, year, VIN,
// and other vehicle-specific details.
package vehicle

import (
	"time"

	"github.com/google/uuid"
)

// Vehicle represents a vehicle from the vehicles table.
type Vehicle struct {
	ID           uuid.UUID
	HouseholdID  uuid.UUID
	Year         *int
	Make         *string
	Model        *string
	VIN          *string
	LicensePlate *string
	Color        *string
	Notes        *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
