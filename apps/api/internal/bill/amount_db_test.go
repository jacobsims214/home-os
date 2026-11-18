package bill

// #5297 end-to-end, against a real Postgres: a bill stored in a NUMERIC(12,2)
// column must reach the wire as the same number of dollars it holds.
//
// handler_amount_test.go proves the serialisation; this proves the scan that
// feeds it. The repo reads bills.amount straight into pgtype.Numeric over the
// binary wire, which is where the mantissa/exponent split is created
// (2340.00 → Int=234000, Exp=-2), so a fix that only worked for
// Numeric.Scan("2340.00") would not be enough.
//
// Requires TEST_DATABASE_URL pointing at a database with all migrations
// applied; skipped otherwise. Run with -p 1.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// TestBillAmountFromDatabaseIsDollars is the ticket's own reproduction: the
// stored $2,340 mortgage and $180 electric bill must come back from the API as
// 2340.00 and 180.00 — not 234000 and 18000.
func TestBillAmountFromDatabaseIsDollars(t *testing.T) {
	pool := testPool(t)
	household, _ := newHousehold(t, pool, false)
	repo := NewRepo(pool)
	ctx := context.Background()

	// Stored the way the seed and the MCP bill_create store them: a decimal
	// string into a NUMERIC(12,2).
	want := map[string]string{
		"Mortgage":        "2340.00",
		"Electric":        "180.00",
		"Internet":        "75.00",
		"Riverside copay": "35.00",
		"Water":           "82.50",
	}
	ids := make(map[string]uuid.UUID, len(want))
	for name, amount := range want {
		b := &Bill{HouseholdID: household, Name: name, Frequency: FrequencyMonthly, DueDay: intPtr(15)}
		if err := b.Amount.Scan(amount); err != nil {
			t.Fatalf("%s: scan %q: %v", name, amount, err)
		}
		created, err := repo.Create(ctx, b)
		if err != nil {
			t.Fatalf("%s: create: %v", name, err)
		}
		ids[name] = created.ID
	}

	bills, err := repo.List(ctx, household)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(bills) != len(want) {
		t.Fatalf("listed %d bills, want %d", len(bills), len(want))
	}

	for _, b := range bills {
		wantAmount, ok := want[b.Name]
		if !ok {
			t.Fatalf("unexpected bill %q", b.Name)
		}
		// The scan split the value into a mantissa and an exponent: the
		// regression lived in rendering only the mantissa.
		if b.Amount.Int == nil || b.Amount.Exp != -2 {
			t.Fatalf("%s: scanned as Int=%v Exp=%d, want a -2 exponent (NUMERIC(12,2))", b.Name, b.Amount.Int, b.Amount.Exp)
		}
		body, err := json.Marshal(toResponse(b))
		if err != nil {
			t.Fatalf("%s: marshal: %v", b.Name, err)
		}
		var decoded struct {
			Amount *string `json:"amount"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("%s: unmarshal: %v", b.Name, err)
		}
		got := "<null>"
		if decoded.Amount != nil {
			got = *decoded.Amount
		}
		if decoded.Amount == nil || got != wantAmount {
			t.Fatalf("%s: amount = %q, want %q (100x means the exponent was dropped)", b.Name, got, wantAmount)
		}
	}

	// A bill with no amount stays null: money absent is never $0.00 (#4806).
	none := &Bill{HouseholdID: household, Name: "No amount bill", Frequency: FrequencyMonthly, DueDay: intPtr(1)}
	created, err := repo.Create(ctx, none)
	if err != nil {
		t.Fatalf("create no-amount bill: %v", err)
	}
	got, err := repo.Get(ctx, created.ID, household)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	body, _ := json.Marshal(toResponse(got))
	var decoded struct {
		Amount *string `json:"amount"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Amount != nil {
		t.Fatalf("no-amount bill = %q, want null", *decoded.Amount)
	}
}

func intPtr(n int) *int { return &n }
