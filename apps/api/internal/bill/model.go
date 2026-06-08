// Package bill manages recurring bills (mortgage, utilities, subscriptions).
// It provides the domain model and database repository for the bills table.
package bill

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// Bill represents a recurring bill from the bills table.
// Columns match the order defined in migration 007_bills.up.sql.
type Bill struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	PropertyID  *uuid.UUID
	Name        string
	Amount      pgtype.Numeric
	DueDay      *int
	Category    *string
	VendorID    *uuid.UUID
	Rrule       *string
	Notes       *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
