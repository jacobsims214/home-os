// Package loan manages financial loan/liability records.
// Loans always belong to the family and may optionally be about one
// property, vehicle or asset (typed subject FKs, domain model v2); with no
// subject they are family-level.
package loan

import (
	"time"

	"github.com/google/uuid"

	"home-os/api/internal/subject"
)

// Loan represents a financial loan from the loans table.
// All nullable numeric/date columns use *string so that NULL in the DB maps
// to null in JSON and numeric values are carried as strings to avoid
// float64 precision loss during JSON serialization.
type Loan struct {
	ID               uuid.UUID
	HouseholdID      uuid.UUID
	Name             string
	// Subject columns are scanned by position: property_id, vehicle_id, asset_id.
	subject.Subject
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