// Package note manages polymorphic notes about one entity (any entity_kind:
// property, vehicle, pet, asset, vendor, bill, maintenance_task, ...) or about
// the family itself, ('household', household_id). Notes are household-scoped
// and support free-form text with an optional title and author reference.
package note

import (
	"time"

	"github.com/google/uuid"
)

// Note represents a free-form note attached to any entity via entity_type + entity_id.
type Note struct {
	ID           uuid.UUID
	HouseholdID  uuid.UUID
	EntityType   string
	EntityID     uuid.UUID
	Title        *string
	Body         string
	AuthorID     *uuid.UUID
	AuthorName   *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}