// Package paymentaccount manages the accounts a household pays bills from
// (payment_accounts, migration 075 / #5497): a display name, an institution,
// an account type and the LAST FOUR DIGITS only.
//
// # There is no column for a full account number
//
// payment_accounts has no full-account-number column and no routing-number
// column, and last_four is CHAR(4) with a `^[0-9]{4}$` CHECK. That is the
// database's half of "the server never holds the account number"; this
// package is the other half. The inputs carry a LastFour string that is
// validated here BEFORE the database sees it, so a client that pastes a full
// number gets a 400 naming the rule rather than a raw constraint violation
// ("payment_accounts_last_four_check"), which tells the person nothing about
// what to do. Both halves matter: the CHECK holds for every writer (this API,
// MCP, psql, a migration); the validation makes the refusal legible.
//
// # The bank login is linked, never copied
//
// The banking credential lives in `secrets` as an AES-256-GCM blob the server
// cannot read. payment_accounts.secret_id is a UUID -- a link -- so writing it
// never requires a plaintext of any kind. That is why "attach a login" is a
// PUT of secret_id, and why this package can answer "a login is stored"
// without decrypting anything.
package paymentaccount

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// The four payment_account_type values (075), in enum order. Mirrored by the
// payment_account_type Postgres enum.
const (
	TypeChecking   = "checking"
	TypeSavings    = "savings"
	TypeCreditCard = "credit_card"
	TypeOther      = "other"
)

// Types is every valid account type, in enum order.
var Types = []string{TypeChecking, TypeSavings, TypeCreditCard, TypeOther}

// ValidType reports whether t is a payment_account_type value.
func ValidType(t string) bool {
	for _, v := range Types {
		if v == t {
			return true
		}
	}
	return false
}

// PaymentAccount is a payment_accounts row. LastFour is the masked tail of the
// account number. SecretID is the link to the bank login in `secrets`; its
// PRESENCE is what the API serializes (gated by the credential policy), never
// the uuid itself.
type PaymentAccount struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	Name        string
	Institution *string
	AccountType string
	LastFour    *string
	Notes       *string
	SecretID    *uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ValidateLastFour is the ONE rule for "is this a last-four value?", and the
// reason a person never hits the CHAR(4) CHECK blind.
//
// Accepted: "" (no digits on record yet) or EXACTLY four ASCII digits, with
// surrounding space trimmed the way a person copies it out of a statement.
// Everything else is refused with a message that says what arrived and what to
// do instead.
//
// The messages name the shape of what was pasted, not just "invalid": someone
// who pasted a 12-digit account number needs to know the app saw 12 digits (so
// it did not truncate them into a wrong account), and someone who pasted a
// routing number needs to be told it is not stored at all. Truncating to the
// last four -- the tempting "helpful" behaviour -- is exactly the bug that makes
// the app guess which four digits matter and silently link a bill to the wrong
// account.
//
// Returns the normalized value, whether it was empty, and an error message
// ("" when valid).
func ValidateLastFour(raw string) (value string, empty bool, msg string) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", true, ""
	}
	// Every rune must be an ASCII digit; "12a4" is not a last-four even though
	// it is four characters long.
	for _, r := range v {
		if r < '0' || r > '9' {
			return "", false, "Last four must be exactly four digits. " +
				"An account or routing number cannot be stored here — " +
				"Home OS keeps only the last four digits."
		}
	}
	if len(v) == 9 {
		return "", false, "That looks like a routing number. Home OS does not store " +
			"routing numbers — enter the last four digits of the account instead."
	}
	if len(v) != 4 {
		return "", false, "Last four must be exactly four digits (that is " +
			itoa(len(v)) + "). Home OS never stores a full account number — " +
			"keep only the last four digits."
	}
	return v, false, ""
}

// itoa is a tiny local decimal formatter: the message is built from a length,
// and this keeps the error text byte-identical under test without pulling
// strconv into a package this small.
func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// NormalizeInput trims the free-text fields and collapses "" to NULL, so "no
// institution" and "no notes" each have one representation in the database.
func NormalizeInput(name, institution, notes string) (n string, inst, nt *string) {
	n = strings.TrimSpace(name)
	if s := strings.TrimSpace(institution); s != "" {
		inst = &s
	}
	if s := strings.TrimSpace(notes); s != "" {
		nt = &s
	}
	return n, inst, nt
}
