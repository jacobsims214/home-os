// Package link provides the "also related to" links (document_links table).
// Any registered entity kind (the entity_kind domain from migration 038,
// including 'household' for family-level links) can carry links. A link is
// one of LinkTypes: a same-family file or secret (link_id is its UUID), or an
// external url. All links are returned in a single "other" bucket in the
// grouped response.
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
	// HouseholdID is the owning family. It is the last column scanned.
	HouseholdID uuid.UUID `json:"-"`
}

// LinkTypes are the accepted link_type values (domain model v2).
var LinkTypes = map[string]bool{"file": true, "secret": true, "url": true}
