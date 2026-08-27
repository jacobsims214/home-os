// Package vendor manages vendor records.
// Vendors are household-scoped entities representing service providers and
// contractors. They can optionally be associated with a property.
package vendors

import (
	"time"

	"github.com/google/uuid"
)

// Vendor represents a vendor from the vendors table.
type Vendor struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	PropertyID  *uuid.UUID
	Name        string
	Specialty   *string
	Phone       *string
	Email       *string
	Website     *string
	Notes       *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
