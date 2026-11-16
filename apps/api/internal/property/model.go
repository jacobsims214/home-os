// Package property manages properties and rooms.
// Properties represent physical locations (homes, rentals) that belong to a
// household. Rooms are organized under properties. Every query is scoped to
// the household_id extracted from JWT claims.
package property

import (
	"time"

	"github.com/google/uuid"
)

// Property represents a physical location from the properties table.
// All nullable numeric/date columns use *string so that NULL in the DB maps
// to null in JSON and numeric values are carried as strings to avoid
// float64 precision loss during JSON serialization.
type Property struct {
	ID           uuid.UUID `json:"id"`
	HouseholdID  uuid.UUID `json:"household_id"`
	Name         string    `json:"name"`
	Address      *string   `json:"address"`
	PropertyType *string   `json:"property_type"`
	Notes        *string   `json:"notes"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	// Financial fields. All nullable — use *string.
	PurchasePrice *string `json:"purchase_price"`
	PurchaseDate  *string `json:"purchase_date"`
	CurrentValue  *string `json:"current_value"`
	DownPayment   *string `json:"down_payment"`

	// Legacy finance fields (#4704). They are NOT columns of properties any
	// more as far as the API is concerned: each one is derived from the
	// property's loan or bills (typed property_id, same family) by
	// selectProperty, and written there by legacyfinance.Apply*. See
	// KB apis/property-vehicle-finance.md for the mapping.
	MortgageAmount        *string `json:"mortgage_amount"`         // loan.original_amount
	MortgageRate          *string `json:"mortgage_rate"`           // loan.interest_rate
	MortgageTermMonths    *string `json:"mortgage_term_months"`    // loan.term_months
	MortgageStartDate     *string `json:"mortgage_start_date"`     // loan.start_date
	MortgageLender        *string `json:"mortgage_lender"`         // loan.lender
	MortgageAccountNumber *string `json:"mortgage_account_number"` // loan.account_number
	PropertyTaxAnnual     *string `json:"property_tax_annual"`     // tax bill amount, per year
	PropertyTaxDueMonths  *string `json:"property_tax_due_months"` // tax bill due_date month / notes
	InsuranceAnnual       *string `json:"insurance_annual"`        // insurance bill amount, per year
	InsuranceProvider     *string `json:"insurance_provider"`      // insurance bill vendor
	HOAFeeMonthly         *string `json:"hoa_fee_monthly"`         // HOA bill amount, per month
}

// Room represents a room within a property from the rooms table.
type Room struct {
	ID         uuid.UUID
	PropertyID uuid.UUID
	Name       string
	Floor      *int
	Notes      *string
	CreatedAt  time.Time
}
