package testutil

// Tests for migration 044 (budgeting metadata). See KB
// decisions/domain-model-v2.md, "044 budgeting".

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// Down to 043, seed bills, loans and property/vehicle finance columns the way
// pre-044 code did, then up/down/up: existing bills become monthly, finance
// columns are copied once into subject loans/bills (skipping equivalents),
// down removes unedited copies and keeps edited ones.
func TestBudgeting044_CopyAndReversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	applyMigration(t, pool, "060_drop_pet_vet_columns.down.sql") // newest first: restores pets.vet_name / vet_phone
	applyMigration(t, pool, "058_same_family_fks.down.sql")      // newest first: 058's FKs need 043's vendors key
	applyMigration(t, pool, "057_secret_agent_share.down.sql")
	applyMigration(t, pool, "056_domain_v2_cleanup.down.sql") // newest first: 056 sits on top of 038-054
	applyMigration(t, pool, "055_activity_event_titles.down.sql")
	applyMigration(t, pool, "054_change_notifications.down.sql") // its trigger is on 050's activity_log
	applyMigration(t, pool, "050_authorship.down.sql")
	applyMigration(t, pool, "044_budgeting.down.sql")

	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)

	existingBill := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Electric', 180, 15) RETURNING id`, f.household)
	existingLoan := insertID(t, pool, `INSERT INTO loans (household_id, name, original_amount, remaining_balance, start_date, term_months) VALUES ($1, 'Old loan', 1000, 500, '2020-01-15', 24) RETURNING id`, f.household)

	// f.property: mortgage, tax, insurance and HOA; an equivalent tax bill
	// already exists (legacy columns only, as pre-#4449 code writes it).
	mustExec(t, pool, `UPDATE properties SET mortgage_amount = 300000, mortgage_rate = 4.125, mortgage_term_months = 360,
		mortgage_start_date = '2020-03-01', mortgage_lender = 'Wells Fargo', mortgage_account_number = 'ACC-1',
		property_tax_annual = 4200, property_tax_due_months = '4', insurance_annual = 1500, insurance_provider = 'State Farm',
		hoa_fee_monthly = 75 WHERE id = $1`, f.property)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, entity_type, entity_id) VALUES ($1, 'County tax', 'property', $2)`, f.household, f.property)
	// other.property: a mortgage, but a loan about it already exists.
	mustExec(t, pool, `UPDATE properties SET mortgage_amount = 100000 WHERE id = $1`, other.property)
	mustExec(t, pool, `INSERT INTO loans (household_id, name, property_id, original_amount, remaining_balance) VALUES ($1, 'Mortgage', $2, 100000, 90000)`, other.household, other.property)
	// f.vehicle: loan, registration and insurance.
	mustExec(t, pool, `UPDATE vehicles SET year = 2022, model = 'Outback', lender = 'Subaru Finance', loan_amount = 20000,
		monthly_payment = 400, loan_term_months = 60, registration_cost = 150, registration_renewal_month = 6,
		insurance_cost = 1200, insurance_provider = 'Geico' WHERE id = $1`, f.vehicle)
	// other.vehicle: loan_amount 0 and nothing else is not a loan.
	mustExec(t, pool, `UPDATE vehicles SET loan_amount = 0 WHERE id = $1`, other.vehicle)

	countCopies := func(table string) int {
		return countRows(t, pool, `SELECT count(*) FROM `+table+` WHERE notes LIKE 'Copied from the %details by migration 044.%'`)
	}

	for cycle := 0; cycle < 2; cycle++ {
		applyMigration(t, pool, "044_budgeting.up.sql")

		var freq string
		mustScan(t, pool.QueryRow(ctx, `SELECT frequency FROM bills WHERE id = $1`, existingBill), &freq)
		if freq != "monthly" {
			t.Errorf("cycle %d: existing bill frequency: want monthly, got %s", cycle, freq)
		}
		var termEnd *string
		mustScan(t, pool.QueryRow(ctx, `SELECT term_end::text FROM loans WHERE id = $1`, existingLoan), &termEnd)
		if termEnd == nil || *termEnd != "2022-01-15" {
			t.Errorf("cycle %d: existing loan term_end: want 2022-01-15, got %v", cycle, termEnd)
		}

		// Mortgage loan for f.property only.
		var lender, account string
		var rate float64
		var end string
		mustScan(t, pool.QueryRow(ctx, `SELECT lender, interest_rate::float8, term_end::text, account_number FROM loans
			WHERE property_id = $1 AND entity_type = 'property' AND entity_id = $1`, f.property), &lender, &rate, &end, &account)
		if lender != "Wells Fargo" || rate != 4.125 || end != "2050-03-01" || account != "ACC-1" {
			t.Errorf("cycle %d: mortgage loan: got %s %v %s %s", cycle, lender, rate, end, account)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM loans WHERE property_id = $1`, other.property); n != 1 {
			t.Errorf("cycle %d: other property already had a loan; want 1, got %d", cycle, n)
		}
		// Property bills: insurance (annual) and HOA (monthly); tax skipped.
		if n := countRows(t, pool, `SELECT count(*) FROM bills WHERE (property_id = $1 OR entity_id = $1) AND name ILIKE '%tax%'`, f.property); n != 1 {
			t.Errorf("cycle %d: tax bill had an equivalent; want 1, got %d", cycle, n)
		}
		mustScan(t, pool.QueryRow(ctx, `SELECT frequency FROM bills WHERE property_id = $1 AND category = 'Insurance'`, f.property), &freq)
		if freq != "annual" {
			t.Errorf("cycle %d: home insurance frequency: %s", cycle, freq)
		}
		mustScan(t, pool.QueryRow(ctx, `SELECT frequency FROM bills WHERE property_id = $1 AND name ILIKE '%HOA%'`, f.property), &freq)
		if freq != "monthly" {
			t.Errorf("cycle %d: HOA frequency: %s", cycle, freq)
		}
		// Vehicle: one loan, registration with a June due date, insurance.
		var payment float64
		mustScan(t, pool.QueryRow(ctx, `SELECT monthly_payment::float8 FROM loans WHERE vehicle_id = $1 AND entity_type = 'vehicle'`, f.vehicle), &payment)
		if payment != 400 {
			t.Errorf("cycle %d: vehicle loan payment: %v", cycle, payment)
		}
		var month int
		var dueDay *int
		mustScan(t, pool.QueryRow(ctx, `SELECT EXTRACT(MONTH FROM due_date)::int, due_day FROM bills WHERE vehicle_id = $1 AND name ILIKE '%registration%' AND frequency = 'annual'`, f.vehicle), &month, &dueDay)
		if month != 6 || dueDay != nil {
			t.Errorf("cycle %d: registration bill: month %d due_day %v", cycle, month, dueDay)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM loans WHERE vehicle_id = $1`, other.vehicle); n != 0 {
			t.Errorf("cycle %d: zero loan_amount must not create a loan, got %d", cycle, n)
		}
		// Exactly: loans = mortgage + vehicle; bills = insurance + HOA + registration + vehicle insurance.
		if n := countCopies("loans"); n != 2 {
			t.Errorf("cycle %d: want 2 copied loans, got %d", cycle, n)
		}
		if n := countCopies("bills"); n != 4 {
			t.Errorf("cycle %d: want 4 copied bills, got %d", cycle, n)
		}

		// Down removes unedited copies; the next cycle's up copies again,
		// and the counts above prove no duplicates.
		applyMigration(t, pool, "044_budgeting.down.sql")
		if n := countCopies("bills") + countCopies("loans"); n != 0 {
			t.Errorf("cycle %d: down should delete unedited copies, %d left", cycle, n)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'bills' AND column_name = 'frequency'`); n != 0 {
			t.Errorf("cycle %d: down left bills.frequency", cycle)
		}
	}

	// Edited copies survive down and count as equivalents on the next up.
	applyMigration(t, pool, "044_budgeting.up.sql")
	mustExec(t, pool, `UPDATE loans SET remaining_balance = 250000, updated_at = NOW() + interval '1 second' WHERE property_id = $1 AND notes LIKE 'Copied%'`, f.property)
	applyMigration(t, pool, "044_budgeting.down.sql")
	applyMigration(t, pool, "044_budgeting.up.sql")
	if n := countRows(t, pool, `SELECT count(*) FROM loans WHERE property_id = $1`, f.property); n != 1 {
		t.Errorf("edited mortgage copy: want 1 loan after down/up, got %d", n)
	}
}

// Rules added by 044: frequencies, due_day only for monthly bills, budget
// categories are family-scoped and registered as entity kind 'budget'.
func TestBudgeting044_Rules(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)

	_, err := pool.Exec(ctx, `INSERT INTO bills (household_id, name, frequency) VALUES ($1, 'x', 'weekly')`, f.household)
	wantSQLState(t, err, sqlstateCheckViolation)
	_, err = pool.Exec(ctx, `INSERT INTO bills (household_id, name, frequency, due_day) VALUES ($1, 'x', 'annual', 5)`, f.household)
	wantSQLState(t, err, sqlstateCheckViolation)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, frequency, due_date) VALUES ($1, 'Car tax', 'annual', '2027-06-01')`, f.household)
	_, err = pool.Exec(ctx, `INSERT INTO loans (household_id, name, original_amount, remaining_balance, due_day) VALUES ($1, 'x', 1, 1, 32)`, f.household)
	wantSQLState(t, err, sqlstateCheckViolation)

	housing := insertID(t, pool, `INSERT INTO budget_categories (household_id, name, monthly_amount) VALUES ($1, 'Housing', 3000) RETURNING id`, f.household)
	_, err = pool.Exec(ctx, `INSERT INTO budget_categories (household_id, name) VALUES ($1, ' housing ')`, f.household)
	wantSQLState(t, err, "23505")
	mustExec(t, pool, `INSERT INTO budget_categories (household_id, name) VALUES ($1, 'Housing')`, other.household)

	// Same-family composite FK.
	_, err = pool.Exec(ctx, `INSERT INTO bills (household_id, name, budget_category_id) VALUES ($1, 'x', $2)`, other.household, housing)
	wantSQLState(t, err, sqlstateFKViolation)
	bill := insertID(t, pool, `INSERT INTO bills (household_id, name, budget_category_id) VALUES ($1, 'Mortgage', $2) RETURNING id`, f.household, housing)
	loan := insertID(t, pool, `INSERT INTO loans (household_id, name, original_amount, remaining_balance, budget_category_id) VALUES ($1, 'M', 1, 1, $2) RETURNING id`, f.household, housing)

	// Kind 'budget': notes can be about a category, not another family's.
	var h uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT entity_household('budget', $1)`, housing), &h)
	if h != f.household {
		t.Fatalf("entity_household('budget'): got %s", h)
	}
	mustExec(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'budget', $2, 'tight month')`, f.household, housing)
	_, err = pool.Exec(ctx, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'budget', $2, 'x')`, other.household, housing)
	wantSQLState(t, err, sqlstateFKViolation)

	// Deleting the category clears the bill/loan link and its notes.
	mustExec(t, pool, `DELETE FROM budget_categories WHERE id = $1`, housing)
	if n := countRows(t, pool, `SELECT count(*) FROM bills WHERE id = $1 AND budget_category_id IS NULL`, bill); n != 1 {
		t.Error("bill should survive with budget_category_id NULL")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM loans WHERE id = $1 AND budget_category_id IS NULL`, loan); n != 1 {
		t.Error("loan should survive with budget_category_id NULL")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM notes WHERE entity_type = 'budget'`); n != 0 {
		t.Errorf("notes about the category should be deleted, %d left", n)
	}
}
