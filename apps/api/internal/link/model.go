// Package link provides the unified entity-to-resource linking system.
// Any entity (asset, property, vehicle, pet, vendor, bill, maintenance_task)
// can have linked resources identified by a free-form link_type string. All
// links are returned in a single generic "other" bucket in the grouped
// response — the package does not maintain typed buckets for specific
// integration types. Legacy integration link rows whose link_type was once a
// first-class integration type may still exist in the database and are
// surfaced via the "other" bucket so they remain visible to callers.
package link

import (
	"time"

	"github.com/google/uuid"
)

// Link represents a row from the document_links table, extended to support
// multiple external resource types.
type Link struct {
	ID         uuid.UUID `json:"id"`
	EntityType string    `json:"entity_type"`
	EntityID   uuid.UUID `json:"entity_id"`
	LinkType   string    `json:"link_type"`
	LinkID     string    `json:"link_id"`
	Title      string    `json:"title"`
	URL        *string   `json:"url,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}