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
	// Frequency is monthly (default), quarterly, semiannual, annual or once
	// (migration 044). Only a monthly bill has a DueDay; the others are due
	// on DueDate.
	Frequency string
	// DueDate is the next or only due date of a bill that is not monthly.
	DueDate *time.Time
	// BudgetCategoryID is the family's budget category the bill counts
	// towards (same-family FK, migration 044).
	BudgetCategoryID *uuid.UUID
	// PaidForDueDate is the due date the last payment (PaidDate) covers
	// (migration 052, written by the MCP bill_pay). Read-only in the API.
	PaidForDueDate *time.Time
}

// Bill frequencies (bills_frequency_check, migration 044).
const (
	FrequencyMonthly    = "monthly"
	FrequencyQuarterly  = "quarterly"
	FrequencySemiannual = "semiannual"
	FrequencyAnnual     = "annual"
	FrequencyOnce       = "once"
)

// ValidFrequency reports whether f is a bills.frequency value.
func ValidFrequency(f string) bool {
	switch f {
	case FrequencyMonthly, FrequencyQuarterly, FrequencySemiannual, FrequencyAnnual, FrequencyOnce:
		return true
	}
	return false
}
