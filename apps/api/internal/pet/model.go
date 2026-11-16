// Package pet manages pet records.
// Pets are household-scoped entities that track species, breed, the family's
// vet (a vendor with trade "vet"), and other pet-specific details.
package pet

import (
	"time"

	"github.com/google/uuid"
)

// Pet represents a pet from the pets table. The vet is a same-family vendor
// (pets.vet_vendor_id, composite FK from migration 043). VetName and VetPhone
// are read-only: they come from that vendor's row, not from the pets table.
type Pet struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	Name        string
	Species     *string
	Breed       *string
	DateOfBirth *time.Time
	VetVendorID *uuid.UUID
	VetName     *string
	VetPhone    *string
	Notes       *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
