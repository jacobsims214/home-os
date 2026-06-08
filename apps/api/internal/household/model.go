// Package household manages households and memberships.
// It provides the domain model and database repository for the households
// and memberships tables.
package household

import (
	"time"

	"github.com/google/uuid"
)

// Household represents a household from the households table.
type Household struct {
	ID        uuid.UUID
	Name      string
	Timezone  string
	Settings  []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}
