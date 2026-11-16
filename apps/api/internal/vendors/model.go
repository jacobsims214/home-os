// Package vendor manages vendor records.
// Vendors are household-scoped entities representing service providers and
// contractors. A vendor belongs to the family and may serve any property
// (domain model v2 dropped vendors.property_id).
package vendors

import (
	"time"

	"github.com/google/uuid"
)

// Vendor represents a vendor from the vendors table.
type Vendor struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	Name        string
	Trades      []string // lower-case trades (plumber, vet, hvac, pest_control); never nil
	Specialty   *string
	Phone       *string
	Email       *string
	Website     *string
	Notes       *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
