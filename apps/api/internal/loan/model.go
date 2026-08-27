// Package loan manages financial loan/liability records.
// Loans are household-scoped and can be polymorphically associated with any
// entity (property, vehicle, asset) via entity_type + entity_id, or be
// household-level (no entity link).
package loan

import (
	"time"

	"github.com/google/uuid"
)

// Loan represents a financial loan from the loans table.
// All nullable numeric/date columns use *string so that NULL in the DB maps
// to null in JSON and numeric values are carried as strings to avoid
// float64 precision loss during JSON serialization.
type Loan struct {
	ID               uuid.UUID
	HouseholdID      uuid.UUID
	Name             string
	EntityType       *string
	EntityID         *uuid.UUID
	Lender           *string
	OriginalAmount   *string
	InterestRate     *string
	TermMonths       *int
	MonthlyPayment   *string
	RemainingBalance *string
	StartDate        *string
	Notes            *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}