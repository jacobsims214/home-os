package money

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// numeric scans a decimal string the way pgx scans a NUMERIC column, so the
// mantissa/exponent under test is the one Postgres really produces.
func numeric(t *testing.T, s string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("scan %q: %v", s, err)
	}
	return n
}

// TestDecimalString is the #5297 regression: bills.amount is NUMERIC(12,2), and
// pgx stores 2340.00 as Int=234000, Exp=-2. Rendering the mantissa alone sends
// "234000", which the UI reads as dollars: the $2,340 mortgage on Money and
// Today rendered as $234,000.
func TestDecimalString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// The four amounts from the ticket, as MCP bill_find reports them.
		{"mortgage", "2340.00", "2340.00"},
		{"electric", "180.00", "180.00"},
		{"internet", "75.00", "75.00"},
		{"copay", "35.00", "35.00"},
		{"half dollar", "82.50", "82.50"},
		{"cent", "0.05", "0.05"},
		{"zero", "0.00", "0.00"},
		{"sub dollar", "0.99", "0.99"},
		{"hundred", "100.00", "100.00"},
		{"large", "9999999999.99", "9999999999.99"},
		{"negative", "-12.34", "-12.34"},
		// A NUMERIC with no fractional part arrives as Int=245, Exp=1.
		{"no scale", "2450", "2450"},
		{"no scale small", "8", "8"},
		// Trailing zeros the column stored are kept: 2 decimal places.
		{"whole with scale", "1600.00", "1600.00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DecimalString(numeric(t, tc.in))
			if got == nil {
				t.Fatalf("DecimalString(%q) = nil, want %q", tc.in, tc.want)
			}
			if *got != tc.want {
				t.Fatalf("DecimalString(%q) = %q, want %q", tc.in, *got, tc.want)
			}
		})
	}
}

// TestDecimalStringIsNotTheMantissa pins the exact bug, not just the values: for
// a two-decimal amount the result must never equal Int.String(), which is what
// the handler used to send.
func TestDecimalStringIsNotTheMantissa(t *testing.T) {
	n := numeric(t, "2340.00")
	if n.Int.String() != "234000" {
		t.Fatalf("pgx representation changed: Int = %q, want the mantissa 234000", n.Int.String())
	}
	got := DecimalString(n)
	if got == nil || *got == n.Int.String() {
		t.Fatalf("DecimalString(2340.00) = %v, want the scaled decimal, not the mantissa %q", got, n.Int.String())
	}
	if *got != "2340.00" {
		t.Fatalf("DecimalString(2340.00) = %q, want 2340.00", *got)
	}
}

// TestDecimalStringMatchesPgxEncoder keeps this package honest against pgx's
// own text encoder, which writes the value the way Postgres would.
func TestDecimalStringMatchesPgxEncoder(t *testing.T) {
	for _, s := range []string{"2340.00", "180.00", "0.00", "82.50", "2450", "8", "0.05", "-12.34", "9999999999.99"} {
		n := numeric(t, s)
		v, err := n.Value()
		if err != nil {
			t.Fatalf("%q: Value(): %v", s, err)
		}
		want, _ := v.(string)
		got := DecimalString(n)
		if got == nil || *got != want {
			t.Fatalf("%q: DecimalString = %v, pgx encoder = %q", s, got, want)
		}
	}
}

// TestDecimalStringJSON pins the wire: a pointer field marshals as a JSON string
// of dollars (never a number, never cents), and NULL as null.
func TestDecimalStringJSON(t *testing.T) {
	type resp struct {
		Amount *string `json:"amount"`
	}
	b, err := json.Marshal(resp{Amount: DecimalString(numeric(t, "2340.00"))})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"amount":"2340.00"}` {
		t.Fatalf("marshalled %s, want {\"amount\":\"2340.00\"}", b)
	}

	b, err = json.Marshal(resp{Amount: DecimalString(pgtype.Numeric{})})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"amount":null}` {
		t.Fatalf("NULL marshalled %s, want {\"amount\":null}", b)
	}
}

func TestDecimalStringNullAndNonFinite(t *testing.T) {
	if got := DecimalString(pgtype.Numeric{}); got != nil {
		t.Fatalf("NULL = %v, want nil", *got)
	}
	for _, s := range []string{"NaN", "Infinity", "-Infinity"} {
		if got := DecimalString(numeric(t, s)); got != nil {
			t.Fatalf("%s = %v, want nil (money that is not a number is absent)", s, *got)
		}
	}
}

func TestFromString(t *testing.T) {
	for in, want := range map[string]string{"2340.00": "2340.00", "180": "180", "82.5": "82.5", "0.05": "0.05"} {
		got := FromString(in)
		if got == nil || *got != want {
			t.Fatalf("FromString(%q) = %v, want %q", in, got, want)
		}
	}
	if got := FromString("not money"); got != nil {
		t.Fatalf("FromString(invalid) = %v, want nil", *got)
	}
}

func TestCents(t *testing.T) {
	tests := map[string]int64{
		"2340.00": 234000,
		"180.00":  18000,
		"75.00":   7500,
		"35.00":   3500,
		"82.50":   8250,
		"0.00":    0,
		"0.05":    5,
		"-12.34":  -1234,
	}
	for want, c := range tests {
		if got := Cents(c); got != want {
			t.Fatalf("Cents(%d) = %q, want %q", c, got, want)
		}
	}
}
