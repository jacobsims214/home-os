// Package vehicle manages vehicle records.
// Vehicles are household-scoped entities that track make, model, year, VIN,
// and other vehicle-specific details.
package vehicle

import (
	"strconv"
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

// MakeString returns a formatted string combining year, make, and model for search indexing.
func (v *Vehicle) MakeString() string {
	if v.Make == nil {
		return ""
	}
	result := *v.Make
	if v.Model != nil {
		result += " " + *v.Model
	}
	if v.Year != nil {
		result = strconv.Itoa(*v.Year) + " " + result
	}
	return result
}
