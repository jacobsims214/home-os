package bill

// #5498 on the MCP side: bill_find carries the same autopay state, the masked
// payment account, the payee and — the point of the ticket — the SAME attention
// value the REST response computes for the same row.
//
// The proof that the rule lives in one place is TestBillFindAttentionMatchesAPI:
// it builds the REST response with the API's own toResponse-equivalent inputs
// (billdue.AttentionFor over the same row) and asserts the tool's JSON carries
// exactly that string. If bill_find ever re-derives "is this on me?", the two
// answers drift and this test fails.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"home-os/billdue"
	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/testutil"
)

// decodeFind runs bill_find with args and returns the decoded bill rows.
func decodeFind(t *testing.T, ctx context.Context, handler toolHandler, args map[string]any) []map[string]any {
	t.Helper()
	_, text := callTool(t, ctx, handler, "bill_find", args)
	var env struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("decode bill_find: %v\n%s", err, text)
	}
	return env.Items
}

// TestBillFindCarriesAutopayAccountPayeeAndAttention is bill_find's half of the
// ticket's evidence: an autopay bill reads "automatic" with its masked account
// and payee, a manual bill with no account reads "needs_setup".
func TestBillFindCarriesAutopayAccountPayeeAndAttention(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, _, find := NewBillFindTool(pool)

	var accountID, vendorID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO payment_accounts (household_id, name, institution, account_type, last_four)
		 VALUES ($1, 'Joint checking', 'Riverside Credit Union', 'checking', '4242') RETURNING id`,
		hh.HouseholdID).Scan(&accountID); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO vendors (household_id, name, phone, email, website)
		 VALUES ($1, 'Riverside Power', '555-0142', 'billing@riverside.example', 'https://riverside.example/pay') RETURNING id`,
		hh.HouseholdID).Scan(&vendorID); err != nil {
		t.Fatalf("insert vendor: %v", err)
	}
	// An autopay bill due on the 28th, paid for this month, so it cannot be
	// overdue whatever day the suite runs.
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO bills (household_id, name, amount, due_day, is_autopay, vendor_id, autopay_account_id, frequency, created_at)
		 VALUES ($1, 'Electric', 180.00, 28, true, $2, $3, 'monthly', NOW())`,
		hh.HouseholdID, vendorID, accountID); err != nil {
		t.Fatalf("insert autopay bill: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO bills (household_id, name, amount, due_date, is_autopay, payee_name, frequency, created_at)
		 VALUES ($1, 'School trip', 75.00, '2027-12-15', false, 'Lincoln Middle School', 'once', NOW())`,
		hh.HouseholdID); err != nil {
		t.Fatalf("insert manual bill: %v", err)
	}

	bills := decodeFind(t, ctx, find, map[string]any{})
	if len(bills) != 2 {
		t.Fatalf("bill_find returned %d bills, want 2: %v", len(bills), bills)
	}
	byName := map[string]map[string]any{}
	for _, b := range bills {
		byName[b["name"].(string)] = b
	}

	auto := byName["Electric"]
	if auto == nil {
		t.Fatalf("no Electric in bill_find: %v", bills)
	}
	if auto["autopay"] != true {
		t.Fatalf("Electric: autopay = %v, want true", auto["autopay"])
	}
	if auto["attention"] != billdue.AttentionAutomatic {
		t.Fatalf("Electric: attention = %v, want %q", auto["attention"], billdue.AttentionAutomatic)
	}
	acct, _ := auto["payment_account"].(map[string]any)
	if acct == nil {
		t.Fatalf("Electric: no payment_account: %v", auto)
	}
	if acct["last_four"] != "4242" {
		t.Fatalf("payment_account.last_four = %v, want 4242", acct["last_four"])
	}
	if acct["name"] != "Joint checking" {
		t.Fatalf("payment_account.name = %v", acct["name"])
	}
	payee, _ := auto["payee"].(map[string]any)
	if payee == nil || payee["name"] != "Riverside Power" {
		t.Fatalf("Electric: payee = %v, want Riverside Power", payee)
	}
	if payee["phone"] != "555-0142" {
		t.Fatalf("payee.phone = %v, want 555-0142", payee["phone"])
	}
	if payee["website"] != "https://riverside.example/pay" {
		t.Fatalf("payee.website = %v", payee["website"])
	}

	manual := byName["School trip"]
	if manual == nil {
		t.Fatalf("no School trip in bill_find: %v", bills)
	}
	if manual["autopay"] != false {
		t.Fatalf("School trip: autopay = %v, want false", manual["autopay"])
	}
	if manual["attention"] != billdue.AttentionNeedsSetup {
		t.Fatalf("School trip: attention = %v, want %q (no autopay, no account: the trap case)",
			manual["attention"], billdue.AttentionNeedsSetup)
	}
	if manual["payment_account"] != nil {
		t.Fatalf("School trip: payment_account = %v, want absent", manual["payment_account"])
	}
	if p, _ := manual["payee"].(map[string]any); p == nil || p["name"] != "Lincoln Middle School" {
		t.Fatalf("School trip: payee = %v, want the free-text payee", manual["payee"])
	}

	// Paste-able evidence.
	pretty, _ := json.MarshalIndent(bills, "", "  ")
	t.Logf("bill_find:\n%s", pretty)
}

// TestBillFindAttentionMatchesAPI is the ticket's proof that the rule lives in
// ONE place: for the same rows, bill_find's attention equals the value
// billdue.AttentionFor produces from those rows — the exact call the REST
// handler makes in toResponse. It covers all four states in one household.
func TestBillFindAttentionMatchesAPI(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, _, find := NewBillFindTool(pool)
	db := context.Background()

	var accountID uuid.UUID
	if err := pool.QueryRow(db,
		`INSERT INTO payment_accounts (household_id, name, account_type, last_four)
		 VALUES ($1, 'Joint checking', 'checking', '4242') RETURNING id`,
		hh.HouseholdID).Scan(&accountID); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	// One bill per state. Each is created well before its last due date so a
	// passed date can count as late (see billdue.AttentionFor).
	// established marks the one row that has been on the books since January,
	// so a due date before today can count as late; the others were entered
	// today, which is the ordinary case for a bill the family already pays.
	seed := []struct {
		name        string
		dueDay      int
		auto        bool
		acct        *uuid.UUID
		legacyAcct  *string // bills.account_number (pre-075): the only way a MANUAL bill has an account
		paid        string  // YYYY-MM-DD of the last payment, "" for none
		established bool
	}{
		{name: "Automatic", dueDay: 28, auto: true, acct: &accountID},
		{name: "Trap", dueDay: 28, auto: false},
		{name: "Manual with account", dueDay: 28, auto: false, legacyAcct: strPtr("pre-075-account")},
		{name: "Late", dueDay: 5, established: true},
	}
	for _, s := range seed {
		var paid any
		if s.paid != "" {
			paid = s.paid
		}
		if _, err := pool.Exec(db,
			`INSERT INTO bills (household_id, name, amount, due_day, is_autopay, autopay_account_id,
			                    account_number, frequency, paid_date, created_at)
			 VALUES ($1, $2, 100.00, $3, $4, $5, $6, 'monthly', $7::date,
			         CASE WHEN $8 THEN '2026-01-01' ELSE NOW() END)`,
			hh.HouseholdID, s.name, s.dueDay, s.auto, s.acct, s.legacyAcct, paid, s.established); err != nil {
			t.Fatalf("insert %s: %v", s.name, err)
		}
	}

	// The rows as the API would read them, and the date bill_find uses (the
	// household's today, which testutil households leave at UTC).
	today, err := householdToday(ctx, pool, hh.HouseholdID.String())
	if err != nil {
		t.Fatalf("household today: %v", err)
	}

	rows, err := pool.Query(db, `SELECT id, name, frequency, due_day, due_date, paid_date,
		paid_for_due_date, is_autopay, autopay_account_id, account_number, created_at FROM bills WHERE household_id = $1`,
		hh.HouseholdID)
	if err != nil {
		t.Fatalf("load rows: %v", err)
	}
	defer rows.Close()
	want := map[string]string{}
	for rows.Next() {
		var (
			id                         uuid.UUID
			name                       string
			frequency                  *string
			dueDay                     *int
			dueDate, paidDate, paidFor *time.Time
			autopay                    bool
			acctID                     *uuid.UUID
			accountNumber              *string
			createdAt                  time.Time
		)
		if err := rows.Scan(&id, &name, &frequency, &dueDay, &dueDate, &paidDate, &paidFor,
			&autopay, &acctID, &accountNumber, &createdAt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		freq := ""
		if frequency != nil {
			freq = *frequency
		}
		s := billdue.Schedule{Frequency: freq, DueDay: dueDay, DueDate: dueDate}
		// hasAccount exactly as the API computes it for a money role.
		hasAccount := acctID != nil || (accountNumber != nil && *accountNumber != "")
		want[name] = billdue.AttentionFor(today, s, paidDate, paidFor, createdAt, autopay, hasAccount)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(want) != len(seed) {
		t.Fatalf("computed %d expectations for %d bills", len(want), len(seed))
	}

	bills := decodeFind(t, ctx, find, map[string]any{})
	if len(bills) != len(seed) {
		t.Fatalf("bill_find returned %d bills, want %d", len(bills), len(seed))
	}
	for _, b := range bills {
		name, _ := b["name"].(string)
		got, _ := b["attention"].(string)
		if got != want[name] {
			t.Fatalf("%s: bill_find attention = %q, the shared rule says %q — the two have drifted", name, got, want[name])
		}
	}

	// Every state is actually represented, so this is not three rows that
	// happen to agree on one value.
	seen := map[string]bool{}
	for _, v := range want {
		seen[v] = true
	}
	for _, state := range billdue.AttentionValues {
		if !seen[state] {
			t.Fatalf("the fixture does not produce %q (got %v); the comparison would miss it", state, want)
		}
	}
	pretty, _ := json.MarshalIndent(bills, "", "  ")
	t.Logf("bill_find attention vs shared rule %v:\n%s", want, pretty)
}

// TestBillFindHidesPaymentAccountFromNonMoneyRole is the role rule on the MCP
// side: a housesitter gets no payment_account and no last four, while still
// being told (via attention) that the bill is on them.
func TestBillFindHidesPaymentAccountFromNonMoneyRole(t *testing.T) {
	pool := testutil.NewPool(t)
	ownerCtx, hh := testutil.SeedHousehold(t, pool)
	_, _, find := NewBillFindTool(pool)
	db := context.Background()

	var accountID uuid.UUID
	if err := pool.QueryRow(db,
		`INSERT INTO payment_accounts (household_id, name, institution, account_type, last_four)
		 VALUES ($1, 'Joint checking', 'Riverside Credit Union', 'checking', '4242') RETURNING id`,
		hh.HouseholdID).Scan(&accountID); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := pool.Exec(db,
		`INSERT INTO bills (household_id, name, amount, due_day, is_autopay, autopay_account_id, frequency, created_at)
		 VALUES ($1, 'Electric', 180.00, 28, true, $2, 'monthly', NOW())`,
		hh.HouseholdID, accountID); err != nil {
		t.Fatalf("insert bill: %v", err)
	}

	// The owner sees it (otherwise the sitter assertion proves nothing).
	ownerBills := decodeFind(t, ownerCtx, find, map[string]any{})
	if len(ownerBills) != 1 {
		t.Fatalf("owner: %d bills", len(ownerBills))
	}
	raw, _ := json.Marshal(ownerBills)
	for _, detail := range []string{"Joint checking", "Riverside Credit Union", "4242"} {
		if !containsText(string(raw), detail) {
			t.Fatalf("the owner's row does not contain %q, so the sitter test would prove nothing: %s", detail, raw)
		}
	}

	// A housesitter's context: same household, role housesitter. bill_find is
	// roleReaders (money), so the tool itself refuses -- which is the first
	// lock. The row-level policy is the second, and it is what this asserts:
	// the row builder (data) never emits an account to that role.
	sitterClaims := *hh.Claims
	sitterClaims.Role = auth.RoleHousesitter
	if auth.CanSeeMoney(auth.RoleHousesitter) {
		t.Fatal("auth.CanSeeMoney says a housesitter may see money; the fixture assumes otherwise")
	}
	r, err := scanBill(pool.QueryRow(db, `SELECT `+billColumns+` FROM bills WHERE household_id = $1`, hh.HouseholdID))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	today, err := householdToday(context.Background(), pool, hh.HouseholdID.String())
	if err != nil {
		t.Fatalf("today: %v", err)
	}
	row := r.data(today, auth.CanSeeMoney(auth.RoleHousesitter))
	body, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"Joint checking", "Riverside Credit Union", "4242", "payment_account"} {
		if containsText(string(body), secret) {
			t.Fatalf("housesitter row contains %q: %s", secret, body)
		}
	}
	if row.Attention != billdue.AttentionNeedsSetup {
		t.Fatalf("housesitter attention = %q, want %q: autopay is on but this role cannot see the account, so it is not 'automatic'",
			row.Attention, billdue.AttentionNeedsSetup)
	}
	t.Logf("housesitter row:\n%s", body)
}

// strPtr is &s.
func strPtr(s string) *string { return &s }

// containsText is strings.Contains, spelled out so this file needs no import
// for one call.
func containsText(s, sub string) bool { return strings.Contains(s, sub) }
