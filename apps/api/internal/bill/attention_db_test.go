package bill

// #5498 against a real Postgres: a bill read carries autopay state, the payment
// account (masked), the payee, and the one attention state. These tests are the
// ticket's own proof:
//
//   - an autopay bill with an account reads "automatic", a manual bill with no
//     account reads "needs_setup" (the trap case), and both carry the payee;
//   - a housesitter's view has no account name or last-four, and does not
//     reveal that an account is linked (no payment_account key at all);
//   - a manual bill that is genuinely past due reads "overdue".
//
// Requires TEST_DATABASE_URL pointing at a database with all migrations
// applied; skipped otherwise. Run with -p 1. The attention rule itself is unit
// tested in packages/billdue/attention_test.go.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"home-os/api/internal/auth"
	"home-os/billdue"
)

// TestBillReadCarriesAutopayAccountPayeeAndAttention is the ticket's acceptance
// test: one autopay bill and one manual bill, their attention values, the payee
// block, and the masked account.
func TestBillReadCarriesAutopayAccountPayeeAndAttention(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	household, _ := newHousehold(t, pool, false)
	repo := NewRepo(pool)

	// Payment accounts: one checked account with its last four, and the
	// bill that pays itself from it.
	account := mustID(t, pool,
		`INSERT INTO payment_accounts (household_id, name, institution, account_type, last_four)
		 VALUES ($1, 'Joint checking', 'Riverside Credit Union', 'checking', '4242') RETURNING id`, household)
	// A vendor payee with contact details.
	vendor := mustID(t, pool,
		`INSERT INTO vendors (household_id, name, phone, email, website)
		 VALUES ($1, 'Riverside Power', '555-0142', 'billing@riverside.example', 'https://riverside.example/pay') RETURNING id`, household)

	// One autopay bill, due far enough out that it is not overdue.
	autopay := &Bill{
		HouseholdID: household, Name: "Electric", Frequency: FrequencyMonthly,
		DueDay: intPtr(28), IsAutopay: boolPtr(true), VendorID: &vendor,
	}
	autopay, err := repo.Create(ctx, autopay)
	if err != nil {
		t.Fatalf("create autopay bill: %v", err)
	}
	// Link the account the way the API's PUT does.
	autopay.AutopayAccountID = &account
	if autopay, err = repo.Update(ctx, autopay); err != nil {
		t.Fatalf("link account: %v", err)
	}
	autopay, err = repo.Get(ctx, autopay.ID, household)
	if err != nil || autopay == nil {
		t.Fatalf("get autopay bill: %v %v", autopay, err)
	}

	// One manual bill (no autopay) with a free-text payee and no account: the
	// trap case.
	manual := &Bill{
		HouseholdID: household, Name: "School trip", Frequency: FrequencyOnce,
		DueDate: timePtr(t, "2026-12-15"), IsAutopay: boolPtr(false), PayeeName: strPtr("Lincoln Middle School"),
	}
	if manual, err = repo.Create(ctx, manual); err != nil {
		t.Fatalf("create manual bill: %v", err)
	}
	manual, err = repo.Get(ctx, manual.ID, household)
	if err != nil || manual == nil {
		t.Fatalf("get manual bill: %v %v", manual, err)
	}

	// Today is the fixture's own date, so the assertion does not depend on
	// when the suite runs.
	today := time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC)

	autoResp := toResponse(autopay, auth.RoleOwner, today)
	manualResp := toResponse(manual, auth.RoleOwner, today)

	if !autoResp.AutopayEnabled {
		t.Fatalf("autopay bill: autopay_enabled = false, want true")
	}
	if autoResp.Attention != billdue.AttentionAutomatic {
		t.Fatalf("autopay bill with an account: attention = %q, want %q", autoResp.Attention, billdue.AttentionAutomatic)
	}
	if autoResp.PaymentAccount == nil {
		t.Fatalf("autopay bill: payment_account is absent; a family owner must see it")
	}
	if got := derefOr(autoResp.PaymentAccount.LastFour); got != "4242" {
		t.Fatalf("payment_account.last_four = %q, want 4242", got)
	}
	if got := derefOr(autoResp.PaymentAccount.Institution); got != "Riverside Credit Union" {
		t.Fatalf("payment_account.institution = %q, want Riverside Credit Union", got)
	}
	if autoResp.PaymentAccount.AccountType != "checking" {
		t.Fatalf("payment_account.account_type = %q, want checking", autoResp.PaymentAccount.AccountType)
	}
	// The payee came from the vendors row, with its contact details.
	if autoResp.Payee == nil || autoResp.Payee.VendorID == nil {
		t.Fatalf("autopay bill: payee = %+v, want the vendor with its id", autoResp.Payee)
	}
	if autoResp.Payee.Name != "Riverside Power" {
		t.Fatalf("payee.name = %q, want Riverside Power", autoResp.Payee.Name)
	}
	if len(autoResp.Payee.Phones) != 1 || autoResp.Payee.Phones[0] != "555-0142" {
		t.Fatalf("payee.phones = %v, want [555-0142]", autoResp.Payee.Phones)
	}
	if derefOr(autoResp.Payee.Email) != "billing@riverside.example" {
		t.Fatalf("payee.email = %q", derefOr(autoResp.Payee.Email))
	}
	if derefOr(autoResp.Payee.Website) != "https://riverside.example/pay" {
		t.Fatalf("payee.website = %q", derefOr(autoResp.Payee.Website))
	}

	if manualResp.AutopayEnabled {
		t.Fatalf("manual bill: autopay_enabled = true, want false")
	}
	if manualResp.Attention != billdue.AttentionNeedsSetup {
		t.Fatalf("manual bill with no account: attention = %q, want %q (the trap case)", manualResp.Attention, billdue.AttentionNeedsSetup)
	}
	if manualResp.PaymentAccount != nil {
		t.Fatalf("manual bill: payment_account = %+v, want none", manualResp.PaymentAccount)
	}
	// The payee is the free-text name, with no vendor id and no contact info
	// (there is nowhere to keep it for a one-off payee).
	if manualResp.Payee == nil || manualResp.Payee.Name != "Lincoln Middle School" {
		t.Fatalf("manual bill: payee = %+v, want Lincoln Middle School", manualResp.Payee)
	}
	if manualResp.Payee.VendorID != nil {
		t.Fatalf("manual bill: payee.vendor_id = %v, want absent for a free-text payee", *manualResp.Payee.VendorID)
	}

	// Paste-able: the two responses the ticket asks for.
	printJSON(t, "autopay bill", autoResp)
	printJSON(t, "manual bill", manualResp)
}

// TestBillAttentionIsOverdueWhenTrulyLate covers the fourth state end to end:
// a bill that has been on the books for months with nothing paid against it is
// late, and its account does not make it "automatic".
func TestBillAttentionIsOverdueWhenTrulyLate(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	household, _ := newHousehold(t, pool, false)
	repo := NewRepo(pool)

	// Created in the spring, due on the 5th, never paid, autopay on with an
	// account: the row is configured to pay itself and did not.
	account := mustID(t, pool,
		`INSERT INTO payment_accounts (household_id, name, account_type, last_four)
		 VALUES ($1, 'Joint checking', 'checking', '4242') RETURNING id`, household)
	b := &Bill{HouseholdID: household, Name: "Water", Frequency: FrequencyMonthly,
		DueDay: intPtr(5), IsAutopay: boolPtr(true), AutopayAccountID: &account}
	b, err := repo.Create(ctx, b)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Back-date it: the bill existed before its last due date.
	if _, err := pool.Exec(ctx, `UPDATE bills SET created_at = '2026-04-01T00:00:00Z' WHERE id = $1`, b.ID); err != nil {
		t.Fatal(err)
	}
	b, err = repo.Get(ctx, b.ID, household)
	if err != nil || b == nil {
		t.Fatalf("get: %v %v", b, err)
	}

	resp := toResponse(b, auth.RoleOwner, time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC))
	if resp.Attention != billdue.AttentionOverdue {
		t.Fatalf("attention = %q for a bill three weeks past its last due date, want %q", resp.Attention, billdue.AttentionOverdue)
	}
	printJSON(t, "overdue bill", resp)
}

// TestHousesitterSeesNoPaymentAccount is the role rule: a housesitter's bill
// response carries no account name, no last four and no payment_account key at
// all (so even "an account is linked" is hidden), while the payee -- a contact,
// not money -- still travels.
func TestHousesitterSeesNoPaymentAccount(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	household, _ := newHousehold(t, pool, false)
	repo := NewRepo(pool)

	account := mustID(t, pool,
		`INSERT INTO payment_accounts (household_id, name, institution, account_type, last_four)
		 VALUES ($1, 'Joint checking', 'Riverside Credit Union', 'checking', '4242') RETURNING id`, household)
	vendor := mustID(t, pool, `INSERT INTO vendors (household_id, name, phone) VALUES ($1, 'Riverside Power', '555-0142') RETURNING id`, household)
	b := &Bill{HouseholdID: household, Name: "Electric", Frequency: FrequencyMonthly,
		DueDay: intPtr(28), IsAutopay: boolPtr(true), AutopayAccountID: &account, VendorID: &vendor}
	b, err := repo.Create(ctx, b)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	b, err = repo.Get(ctx, b.ID, household)
	if err != nil || b == nil {
		t.Fatalf("get: %v %v", b, err)
	}

	// Sanity: the family owner does see it, so the assertion below is about
	// the role and not about a fixture that never had an account.
	owner := toResponse(b, auth.RoleOwner, time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC))
	if owner.PaymentAccount == nil {
		t.Fatal("fixture is wrong: the owner does not see the account either")
	}
	body, err := json.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, detail := range []string{"Joint checking", "Riverside Credit Union", "4242"} {
		if !containsString(string(body), detail) {
			t.Fatalf("the owner's response does not contain %q, so the sitter test would prove nothing: %s", detail, body)
		}
	}

	for _, role := range []string{auth.RoleHousesitter, auth.RoleVendor} {
		sitter := toResponse(b, role, time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC))
		raw, err := json.Marshal(sitter)
		if err != nil {
			t.Fatal(err)
		}
		if sitter.PaymentAccount != nil {
			t.Fatalf("%s: payment_account = %+v, want absent", role, sitter.PaymentAccount)
		}
		for _, secret := range []string{"Joint checking", "Riverside Credit Union", "4242", "payment_account"} {
			if containsString(string(raw), secret) {
				t.Fatalf("%s: response contains %q: %s", role, secret, raw)
			}
		}
		// The payee is not money, and a house sitter may see contacts.
		if sitter.Payee == nil || sitter.Payee.Name != "Riverside Power" {
			t.Fatalf("%s: payee = %+v, want the vendor (contacts are not money)", role, sitter.Payee)
		}
		// The state is still honest without the account: autopay is on but
		// nothing this role can see will pay it, so it says needs_setup rather
		// than automatic -- it never claims "nothing to do" on a guess, and it
		// never leaks that an account exists.
		if sitter.Attention != billdue.AttentionNeedsSetup {
			t.Fatalf("%s: attention = %q, want %q (a hidden account cannot make a bill look automatic)", role, sitter.Attention, billdue.AttentionNeedsSetup)
		}
		printJSON(t, role+" sees", sitter)
	}
}

// TestPaymentAccountOnANonAutopayBillIsRefused pins the database's own rule
// (bills_autopay_account_check, 075) through the repo: a manual bill cannot
// hold an autopay account, so "manual with an account" only ever comes from the
// legacy free-text column.
func TestPaymentAccountOnANonAutopayBillIsRefused(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	household, _ := newHousehold(t, pool, false)
	repo := NewRepo(pool)
	account := mustID(t, pool,
		`INSERT INTO payment_accounts (household_id, name, account_type) VALUES ($1, 'Joint checking', 'checking') RETURNING id`, household)
	b := &Bill{HouseholdID: household, Name: "Water", Frequency: FrequencyMonthly,
		DueDay: intPtr(5), IsAutopay: boolPtr(false), AutopayAccountID: &account}
	if _, err := repo.Create(ctx, b); err == nil {
		t.Fatal("a manual bill accepted an autopay_account_id; bills_autopay_account_check is not holding")
	}
}

// --- small test helpers (this file's own; the other test files have theirs) ---

func boolPtr(b bool) *bool { return &b }

func strPtr(s string) *string { return &s }

func timePtr(t *testing.T, s string) *time.Time {
	t.Helper()
	d, err := time.Parse(dateLayout, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return &d
}

// derefOr is s, or "" when nil.
func derefOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// containsString reports whether s contains sub.
func containsString(s, sub string) bool { return strings.Contains(s, sub) }

// printJSON prints a response as indented JSON, so the ticket's evidence is the
// actual wire shape.
func printJSON(t *testing.T, label string, v any) {
	t.Helper()
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("%s: marshal: %v", label, err)
	}
	t.Logf("%s:\n%s", label, body)
}
