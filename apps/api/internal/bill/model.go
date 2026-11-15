// Package bill manages recurring bills (mortgage, utilities, subscriptions).
// It provides the domain model and database repository for the bills table.
package bill

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"home-os/api/internal/subject"
)

// Bill represents a recurring bill from the bills table.
type Bill struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	// Subject is the optional property, vehicle or asset the bill is about.
	subject.Subject
	Name          string
	Amount        pgtype.Numeric
	DueDay        *int
	Category      *string
	VendorID      *uuid.UUID
	Rrule         *string
	Notes         *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	PaidDate      *time.Time
	IsAutopay     *bool
	AccountNumber *string
	PaymentURL    *string
}
