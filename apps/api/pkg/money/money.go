// Package money is the ONE place a money value becomes the string the API
// sends.
//
// THE UNIT CONTRACT (this is the whole point of the package): every amount on
// the wire is a **decimal string of dollars** — "2340.00" for a $2,340
// mortgage, "180.00" for a $180 electric bill. Not cents, not a float, not a
// NUMERIC mantissa. The UI divides by nothing: its formatters
// (apps/ui/lib/bills/model.ts formatMoney and its two siblings in
// lib/today/agenda.ts and lib/calendar/model.ts) parse the string as dollars
// and group it. Two units in one field is exactly how #5297 shipped — every
// bill rendered 100x too large on Money and Today.
//
// WHY THIS PACKAGE EXISTS (do not go back to Numeric.Int.String()):
//
// bills.amount is NUMERIC(12,2) (migration 007), and pgx scans that into
// pgtype.Numeric, which stores a value as Int * 10^Exp. For 2340.00 that is
// Int=234000, Exp=-2. `n.Int.String()` returns the mantissa alone — "234000" —
// so the exponent is silently dropped and the wire carries cents where the UI
// reads dollars. pgx's own encoder renders the same value correctly
// ("2340.00"); the bare Int field does not. Any money column that travels as
// pgtype.Numeric must leave through DecimalString.
//
// A NULL column is null (the UI renders "No amount", never "$0.00" — #4806).
// NaN and Infinity cannot be a bill amount, so they are null too: "NaN" in a
// money field would render a broken page instead of an honest absence.
package money

import (
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
)

// DecimalString renders a NUMERIC as the decimal string the wire contract
// promises ("2340.00"), or nil when the column is NULL or not a finite number.
//
// The digits are rendered from the mantissa and exponent as text, never via
// float64, so there is no rounding surprise; the scale Postgres stored is kept
// (a NUMERIC(12,2) keeps its two decimal places).
func DecimalString(n pgtype.Numeric) *string {
	if !n.Valid || n.NaN || n.InfinityModifier != pgtype.Finite {
		return nil
	}
	// A Valid, finite numeric with no mantissa is the zero value; pgx's own
	// encoder renders that as "0".
	if n.Int == nil {
		return strPtr("0")
	}

	digits := n.Int.String()
	sign := ""
	if len(digits) > 0 && digits[0] == '-' {
		sign, digits = "-", digits[1:]
	}

	var body string
	switch exp := int(n.Exp); {
	case exp == 0:
		body = digits
	case exp > 0:
		body = digits + zeros(exp)
	default:
		places := -exp
		if len(digits) <= places {
			body = "0." + zeros(places-len(digits)) + digits
		} else {
			cut := len(digits) - places
			body = digits[:cut] + "." + digits[cut:]
		}
	}
	return strPtr(sign + body)
}

// FromString normalises a decimal string (the form the API's request bodies
// carry) into the same wire form, so a value read from a NUMERIC column and one
// parsed from a request render identically. An unparseable string is nil.
func FromString(s string) *string {
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		return nil
	}
	return DecimalString(n)
}

// Cents renders a value already in integer cents as dollars on the same wire
// contract, for read models that load money as `ROUND(amount * 100)::bigint`
// (budget obligations, the billdue due-soon loader). This is the one rule that
// converts cents; package budget's formatCents delegates here.
func Cents(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return sign + strconv.FormatInt(c/100, 10) + "." + pad2(c%100)
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

func strPtr(s string) *string { return &s }

func zeros(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = '0'
	}
	return string(b)
}
