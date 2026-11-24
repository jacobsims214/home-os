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
	Name      string
	Amount    pgtype.Numeric
	DueDay    *int
	Category  *string
	VendorID  *uuid.UUID
	Rrule     *string
	Notes     *string
	CreatedAt time.Time
	UpdatedAt time.Time
	PaidDate  *time.Time
	// IsAutopay is bills.is_autopay (020, NOT NULL DEFAULT false since 075):
	// does this bill pay itself? Kept a pointer so a partial update can say
	// "leave it alone"; the column itself is never NULL.
	IsAutopay *bool
	// AutopayAccountID is bills.autopay_account_id (075): the payment_accounts
	// row that pays this bill when it pays itself. Only set on an autopay bill
	// (bills_autopay_account_check). PaymentAccount is that row as the API may
	// show it.
	AutopayAccountID *uuid.UUID
	// PaymentAccount is the linked account, masked: name, institution, type and
	// the LAST FOUR digits. Never a full account number -- payment_accounts has
	// no column for one (075).
	PaymentAccount *PaymentAccount
	// PayeeName is bills.payee_name (075): the payee when it is not a vendors
	// row. NULL when VendorID names the payee (one fact, one place).
	PayeeName *string
	// Payee is who the bill is paid to, resolved from the vendor when VendorID
	// is set and from PayeeName otherwise.
	Payee         *Payee
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
	// SourceDocumentID is the document the bill's amount came from
	// (migration 072, #4941): read it with the documents API to check the
	// amount against the text it was taken from. Read-only in the API.
	SourceDocumentID *uuid.UUID
}

// PaymentAccount is an account a household pays bills from, as the API
// serializes it: a display name, an institution, a type and the last four
// digits. payment_accounts (075) has no column for a full account number and
// none for a routing number; the banking login is a `secrets` row linked by
// payment_accounts.secret_id, which is deliberately NOT exposed here -- only
// the family may read secrets, and "a login is stored" is itself sensitive
// (decisions/2026-11-15-role-policy.md).
type PaymentAccount struct {
	ID          uuid.UUID
	Name        string
	Institution *string
	LastFour    *string
	AccountType string
}

// Payee is who a bill is paid to: a vendors row when bills.vendor_id is set,
// otherwise the free-text bills.payee_name. Vendor contacts are readable by
// housesitters (ResVendors read = rolesSitter), so this is not money -- it
// travels with any bill the caller may read.
type Payee struct {
	// VendorID is set only when the payee is a vendors row.
	VendorID *uuid.UUID
	Name     string
	Phone    *string
	Email    *string
	Website  *string
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
