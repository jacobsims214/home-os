package bill

// #5297: the bill amount on the wire is a decimal string of DOLLARS.
//
// bills.amount is NUMERIC(12,2) (migration 007) and is scanned into
// pgtype.Numeric, which keeps a value as Int * 10^Exp: 2340.00 is
// Int=234000, Exp=-2. The handler used to serialise `Amount.Int.String()` (the
// mantissa alone), so /bff/bills carried "234000" and the UI — which reads its
// amounts as dollars — rendered the $2,340 mortgage as $234,000 on both Money
// and Today.
//
// These tests drive toResponse, the ONE function every bill response goes
// through (List, Get, Create, Update), so a regression here cannot come back
// through a second path. No database needed: the domain model is built directly.
//
// Requires nothing: `go test ./internal/bill/ -run TestBillAmount`.

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func amountOf(t *testing.T, b *Bill) any {
	t.Helper()
	body, err := json.Marshal(toResponse(b))
	if err != nil {
		t.Fatalf("marshal bill response: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal bill response: %v", err)
	}
	return decoded["amount"]
}

// TestBillAmountIsDollars is the ticket's own numbers: the ground truth from
// MCP bill_find (2340 / 180 / 75 / 35 / 82.50 dollars) must come back from the
// API unchanged, not multiplied by 100.
func TestBillAmountIsDollars(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"mortgage", "2340.00", "2340.00"},
		{"electric", "180.00", "180.00"},
		{"internet", "75.00", "75.00"},
		{"riverside copay", "35.00", "35.00"},
		{"partial", "82.50", "82.50"},
		{"zero", "0.00", "0.00"},
		{"cent", "0.05", "0.05"},
		// A NUMERIC(12,2) always arrives with its scale, but a whole-dollar
		// value stored without one must not gain or lose digits either.
		{"unscaled", "2450", "2450"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var amount pgtype.Numeric
			if err := amount.Scan(tc.in); err != nil {
				t.Fatalf("scan %q: %v", tc.in, err)
			}
			got := amountOf(t, &Bill{Name: tc.name, Amount: amount})
			if got != tc.want {
				t.Fatalf("amount for %q = %v, want %q (100x means the exponent was dropped)", tc.name, got, tc.want)
			}
		})
	}
}

// TestBillAmountNullStaysNull pins #4806: a role without money, or a bill with
// no amount, gets a null — never "$0.00", and never a missing key the UI would
// have to guess at.
func TestBillAmountNullStaysNull(t *testing.T) {
	got := amountOf(t, &Bill{Name: "Water"})
	if got != nil {
		t.Fatalf("NULL amount = %v, want null", got)
	}
}

// TestBillAmountIsNotTheMantissa is the revert-check in test form: it asserts
// the specific wrong value the old code produced, so a future refactor back to
// `Amount.Int.String()` fails loudly with the reason.
func TestBillAmountIsNotTheMantissa(t *testing.T) {
	var amount pgtype.Numeric
	if err := amount.Scan("2340.00"); err != nil {
		t.Fatal(err)
	}
	if mantissa := amount.Int.String(); mantissa != "234000" {
		t.Fatalf("pgx representation changed: mantissa = %q, want 234000", mantissa)
	}
	if got := amountOf(t, &Bill{Name: "Mortgage", Amount: amount}); got == "234000" {
		t.Fatal(`amount = "234000": the numeric's exponent was dropped, so every bill is 100x too large (#5297)`)
	}
}

// TestBillAmountHonoursExponent is #5312's own test: a NUMERIC(12,2) with a
// NON-ZERO exponent, built from the mantissa and exponent pair pgx actually
// produces (never from a string, so the exponent cannot quietly be zero),
// must serialise as the decimal it represents.
//
// NUMERIC(12,2) always arrives as Int = the digits without the point and
// Exp = -2: 2340.00 → {234000, -2}. The old `Amount.Int.String()` returned only
// the mantissa, a silent power-of-ten error with no exception anywhere — this
// test names every shape of that error, including the ones an unscaled value
// would hide:
//
//	{234000, -2} → "2340.00"  (the old code sent "234000": 100x)
//	{245,    1}  → "2450"     (the old code sent "245":  10x too small)
//	{5,     -2}  → "0.05"     (the old code sent "5":    100x)
func TestBillAmountHonoursExponent(t *testing.T) {
	tests := []struct {
		name     string
		mantissa int64
		exp      int32
		want     string
	}{
		{"mortgage at scale 2", 234000, -2, "2340.00"},
		{"mortgage with positive exponent", 245, 1, "2450"},
		{"copay at scale 2", 8250, -2, "82.50"},
		{"cent at scale 2", 5, -2, "0.05"},
		{"negative at scale 2", -1234, -2, "-12.34"},
		{"zero at scale 2", 0, -2, "0.00"},
		{"positive exponent large", 999999999999, 2, "99999999999900"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := pgtype.Numeric{Int: big.NewInt(tc.mantissa), Exp: tc.exp, Valid: true}
			if n.Exp == 0 {
				t.Fatalf("test is vacuous: exponent is 0, which cannot catch a dropped exponent")
			}
			got := amountOf(t, &Bill{Name: tc.name, Amount: n})
			if got != tc.want {
				t.Fatalf("Int=%d Exp=%d serialised as %v, want %q (mantissa alone would be %q)",
					tc.mantissa, tc.exp, got, tc.want, n.Int.String())
			}
		})
	}
}
