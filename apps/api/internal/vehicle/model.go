// Package vehicle manages vehicle records.
// Vehicles are household-scoped entities that track make, model, year, VIN,
// and other vehicle-specific details.
package vehicle

import (
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Vehicle represents a vehicle from the vehicles table.
type Vehicle struct {
	ID           uuid.UUID
	HouseholdID  uuid.UUID
	Year         *int
	Make         *string
	Model        *string
	VIN          *string
	LicensePlate *string
	Color        *string
	Notes        *string

	// Financial fields — all nullable, use *string to match the Property pattern.
	PurchasePrice *string
	PurchaseDate  *string
	CurrentValue  *string

	// Legacy finance fields (#4704). Not read from or written to the
	// vehicles row: derived from the vehicle's loan and bills (typed
	// vehicle_id, same family) by selectVehicle. KB
	// apis/property-vehicle-finance.md has the mapping.
	LoanAmount               *string // loan.original_amount
	InsuranceCost            *string // insurance bill amount, per year
	RegistrationCost         *string // registration bill amount, per year
	Lender                   *string // loan.lender
	LoanTermMonths           *int    // loan.term_months
	MonthlyPayment           *string // loan.monthly_payment
	RegistrationRenewalMonth *int    // month of the registration bill's due_date
	InsuranceProvider        *string // insurance bill vendor

	CreatedAt time.Time
	UpdatedAt time.Time
}

// MakeString returns a formatted string combining year, make, and model for search indexing.
func (v *Vehicle) MakeString() string {
	if v.Make == nil {
		return ""
	}
	result := *v.Make
	if v.Model != nil {
		result += " " + *v.Model
	}
	if v.Year != nil {
		result = strconv.Itoa(*v.Year) + " " + result
	}
	return result
}
