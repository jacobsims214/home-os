// Package household manages households and memberships.
// It provides the domain model and database repository for the households
// and memberships tables.
package household

import (
	"time"

	"github.com/google/uuid"
)

// Household represents a household from the households table.
//
// It is serialized straight to the wire (GET /api/v1/households/me), so every
// field carries an explicit json tag. Untagged, Go names this object "ID",
// "Name", "Timezone", … and the UI's data.timezone / data.name were undefined
// on every family, silently (#5300). A struct sent to a client without tags
// has a wire contract decided by accident.
type Household struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Timezone  string    `json:"timezone"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Settings is the households.settings JSONB blob: internal storage with no
	// client consumer, and as []byte it would reach the wire as base64. It is
	// deliberately not serialized; give it a typed field like the others above
	// if a client ever needs it.
	Settings []byte `json:"-"`
}
