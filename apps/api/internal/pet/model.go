// Package pet manages pet records.
// Pets are household-scoped entities that track species, breed, vet information,
// and other pet-specific details.
package pet

import (
	"time"

	"github.com/google/uuid"
)

// Pet represents a pet from the pets table.
type Pet struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	Name        string
	Species     *string
	Breed       *string
	DateOfBirth *time.Time
	VetName     *string
	VetPhone    *string
	Notes       *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
