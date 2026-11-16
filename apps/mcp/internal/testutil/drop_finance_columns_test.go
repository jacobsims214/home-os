package testutil

// Tests for migration 069 (#4707): the legacy property/vehicle finance
// columns are copied into the subject's loan/bills by the typed FK (the
// legacyfinance rules) and dropped; down adds them back from the rows.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"home-os/billcal"
)

func TestDropFinanceColumns069_CopyDropAndReversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	applyMigration(t, pool, "069_drop_finance_columns.down.sql")

	f := seedV2Family(t, pool)
	other := seedV2Family(t, pool)
	marker044 := "Copied from the property's details by migration 044. The remaining balance is the original amount; update it."

	// f.property ------------------------------------------------------------
	// Mortgage: an unedited 044 copy; the column was changed after 044.
	mortgage := insertID(t, pool, `INSERT INTO loans (household_id, name, property_id, lender, original_amount, remaining_balance, notes, created_at, updated_at)
		VALUES ($1, 'Main house mortgage', $2, 'Old Bank', 200000, 200000, $3, '2026-01-01', '2026-01-01') RETURNING id`, f.household, f.property, marker044)
	// A HELOC created earlier must not win over the mortgage.
	mustExec(t, pool, `INSERT INTO loans (household_id, name, property_id, original_amount, remaining_balance, created_at)
		VALUES ($1, 'HELOC', $2, 5000, 5000, '2025-01-01')`, f.household, f.property)
	// An asset's bill named like a tax bill is not the property's.
	asset := insertID(t, pool, `INSERT INTO assets (household_id, room_id, name) VALUES ($1, $2, 'Fridge') RETURNING id`, f.household, f.room)
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, asset_id, created_at) VALUES ($1, 'Fridge tax', 5, $2, '2025-01-01')`, f.household, asset)
	taxes := insertID(t, pool, `INSERT INTO budget_categories (household_id, name) VALUES ($1, 'taxes') RETURNING id`, f.household)
	// Insurance: a bill the family edited (no marker), 100 a month.
	insurance := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, category, property_id, created_at, updated_at)
		VALUES ($1, 'Home insurance', 100, 'Insurance', $2, '2026-01-01', '2026-02-01') RETURNING id`, f.household, f.property)
	// HOA: already equal to the column.
	hoa := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, property_id, created_at, updated_at)
		VALUES ($1, 'HOA', 75, $2, '2026-01-01', '2026-01-01') RETURNING id`, f.household, f.property)
	mustExec(t, pool, `UPDATE properties SET mortgage_amount = 250000, mortgage_lender = 'New Bank', mortgage_rate = 3.5,
		property_tax_annual = 3000, property_tax_due_months = '4',
		insurance_annual = 1500, insurance_provider = 'State Farm', hoa_fee_monthly = 75 WHERE id = $1`, f.property)

	// f.vehicle: no rows yet; values below 044's thresholds still count.
	geico := insertID(t, pool, `INSERT INTO vendors (household_id, name, trades) VALUES ($1, 'GEICO ', ARRAY['insurance']) RETURNING id`, f.household)
	mustExec(t, pool, `INSERT INTO vendors (household_id, name, created_at) VALUES ($1, 'Geico', '2020-01-01')`, f.household)
	mustExec(t, pool, `UPDATE vehicles SET year = 2022, model = 'Outback', lender = 'Ally', loan_amount = 0, monthly_payment = 300,
		loan_term_months = 60, registration_cost = 120, registration_renewal_month = 13, insurance_provider = 'Geico' WHERE id = $1`, f.vehicle)

	// other.vehicle: an unedited 044 registration copy with a mirror event;
	// the column changed after 044.
	vmarker := "Copied from the vehicle's details by migration 044."
	reg := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, category, frequency, due_date, vehicle_id, notes, created_at, updated_at)
		VALUES ($1, 'Subaru registration', 100, 'Vehicle', 'annual', '2027-06-01', $2, $3, '2026-01-01', '2026-01-01') RETURNING id`, other.household, other.vehicle, vmarker)
	for _, h := range []uuid.UUID{f.household, other.household} {
		mustExec(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Bills', 'bills', gen_random_uuid()::text)`, h)
	}
	if n, err := billcal.Backfill(ctx, pool); err != nil || n != 1 {
		t.Fatalf("backfill before 069: n=%d err=%v (want the registration event)", n, err)
	}
	var regUID string
	mustScan(t, pool.QueryRow(ctx, `SELECT uid FROM calendar_objects WHERE entity_type = 'bill' AND entity_id = $1`, reg), &regUID)
	mustExec(t, pool, `UPDATE vehicles SET registration_cost = 150, registration_renewal_month = 6 WHERE id = $1`, other.vehicle)

	var hoaUpdated string
	mustScan(t, pool.QueryRow(ctx, `SELECT updated_at::text FROM bills WHERE id = $1`, hoa), &hoaUpdated)

	applyMigration(t, pool, "069_drop_finance_columns.up.sql")

	if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns
		WHERE (table_name = 'properties' AND column_name IN ('mortgage_amount', 'property_tax_annual', 'insurance_provider', 'hoa_fee_monthly'))
		   OR (table_name = 'vehicles' AND column_name IN ('lender', 'loan_amount', 'registration_cost', 'insurance_cost'))`); n != 0 {
		t.Fatalf("up left %d finance columns", n)
	}

	// Mortgage: the unedited 044 copy takes the column values.
	var amount float64
	var lender string
	var rate *float64
	mustScan(t, pool.QueryRow(ctx, `SELECT original_amount::float8, lender, interest_rate::float8 FROM loans WHERE id = $1`, mortgage), &amount, &lender, &rate)
	if amount != 250000 || lender != "New Bank" || rate == nil || *rate != 3.5 {
		t.Errorf("mortgage: got %v %s %v", amount, lender, rate)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM loans WHERE property_id = $1`, f.property); n != 2 {
		t.Errorf("property loans: want mortgage + HELOC, got %d", n)
	}

	// Tax: a new annual bill due in April, filed under the existing category.
	var freq string
	var month int
	var cat *uuid.UUID
	var notes string
	mustScan(t, pool.QueryRow(ctx, `SELECT amount::float8, frequency, EXTRACT(MONTH FROM due_date)::int, budget_category_id, notes
		FROM bills WHERE property_id = $1 AND asset_id IS NULL AND name ILIKE '%tax%'`, f.property), &amount, &freq, &month, &cat, &notes)
	if amount != 3000 || freq != "annual" || month != 4 || cat == nil || *cat != taxes ||
		!strings.HasPrefix(notes, "Copied from the property's details by migration 069.") {
		t.Errorf("tax bill: %v %s %d %v %q", amount, freq, month, cat, notes)
	}

	// Insurance: the edited bill keeps its amount, the column amount goes to
	// its notes; the empty provider is filled with a new vendor.
	var vendor *string
	mustScan(t, pool.QueryRow(ctx, `SELECT b.amount::float8, b.notes, v.name FROM bills b LEFT JOIN vendors v ON v.id = b.vendor_id WHERE b.id = $1`, insurance), &amount, &notes, &vendor)
	if amount != 100 || vendor == nil || *vendor != "State Farm" ||
		!strings.Contains(notes, "Migration 069: the property's details also said insurance (annual) 1500.00.") {
		t.Errorf("insurance bill: %v %v %q", amount, vendor, notes)
	}

	// HOA: equal, untouched.
	var hoaAfter string
	mustScan(t, pool.QueryRow(ctx, `SELECT updated_at::text FROM bills WHERE id = $1`, hoa), &hoaAfter)
	if hoaAfter != hoaUpdated {
		t.Errorf("HOA bill was rewritten: %s -> %s", hoaUpdated, hoaAfter)
	}

	// Vehicle: a loan from a payment alone; registration with an invalid
	// month keeps it in notes; insurance from a provider alone reuses the
	// family's insurance vendor.
	var payment float64
	var term int
	mustScan(t, pool.QueryRow(ctx, `SELECT name, monthly_payment::float8, term_months, lender FROM loans WHERE vehicle_id = $1`, f.vehicle), &notes, &payment, &term, &lender)
	if notes != "2022 Subaru Outback loan" || payment != 300 || term != 60 || lender != "Ally" {
		t.Errorf("vehicle loan: %s %v %d %s", notes, payment, term, lender)
	}
	var due *string
	mustScan(t, pool.QueryRow(ctx, `SELECT amount::float8, due_date::text, notes FROM bills WHERE vehicle_id = $1 AND name ILIKE '%regist%'`, f.vehicle), &amount, &due, &notes)
	if amount != 120 || due != nil || !strings.Contains(notes, "\nDue months: 13") {
		t.Errorf("vehicle registration: %v %v %q", amount, due, notes)
	}
	var vendorID *uuid.UUID
	var nilAmount *float64
	mustScan(t, pool.QueryRow(ctx, `SELECT amount::float8, vendor_id FROM bills WHERE vehicle_id = $1 AND name ILIKE '%insur%'`, f.vehicle), &nilAmount, &vendorID)
	if nilAmount != nil || vendorID == nil || *vendorID != geico {
		t.Errorf("vehicle insurance: %v %v (want vendor %s)", nilAmount, vendorID, geico)
	}

	// other.vehicle: the 044 copy takes the new cost; its stale mirror event
	// is deleted (with a change row) and the backfill rebuilds it.
	mustScan(t, pool.QueryRow(ctx, `SELECT amount::float8, EXTRACT(MONTH FROM due_date)::int FROM bills WHERE id = $1`, reg), &amount, &month)
	if amount != 150 || month != 6 {
		t.Errorf("other registration: %v %d", amount, month)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM calendar_objects WHERE entity_type = 'bill' AND entity_id = $1`, reg); n != 0 {
		t.Errorf("stale registration event kept (%d)", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM calendar_changes WHERE event_uid = $1 AND action = 'delete'`, regUID); n != 1 {
		t.Errorf("want one delete change for the old event, got %d", n)
	}
	// Bills with a schedule: f's tax bill and other's registration.
	if n, err := billcal.Backfill(ctx, pool); err != nil || n != 2 {
		t.Errorf("backfill after 069: n=%d err=%v, want 2", n, err)
	}

	snapshot := func() string {
		t.Helper()
		var s string
		mustScan(t, pool.QueryRow(ctx, `SELECT
			(SELECT string_agg(id::text || amount::text || COALESCE(notes, '') || COALESCE(vendor_id::text, '') || COALESCE(due_date::text, ''), '|' ORDER BY id) FROM bills)
			|| (SELECT string_agg(id::text || original_amount::text || COALESCE(lender, '') || COALESCE(notes, '') || updated_at::text, '|' ORDER BY id) FROM loans)
			|| (SELECT count(*) FROM vendors)::text || (SELECT count(*) FROM calendar_objects)::text`), &s)
		return s
	}
	before := snapshot()

	// Down: columns back, filled from the rows the API shows.
	applyMigration(t, pool, "069_drop_finance_columns.down.sql")
	var mAmount, tax, ins, hoaFee *float64
	var mLender, dueMonths, provider *string
	mustScan(t, pool.QueryRow(ctx, `SELECT mortgage_amount::float8, mortgage_lender, property_tax_annual::float8, property_tax_due_months,
		insurance_annual::float8, insurance_provider, hoa_fee_monthly::float8 FROM properties WHERE id = $1`, f.property),
		&mAmount, &mLender, &tax, &dueMonths, &ins, &provider, &hoaFee)
	if !eqF(mAmount, 250000) || !eqS(mLender, "New Bank") || !eqF(tax, 3000) || !eqS(dueMonths, "4") ||
		!eqF(ins, 1200) || !eqS(provider, "State Farm") || !eqF(hoaFee, 75) {
		t.Errorf("down property: %v %v %v %v %v %v %v", mAmount, mLender, tax, dueMonths, ins, provider, hoaFee)
	}
	var vLoan, vPayment, vReg *float64
	var vMonth *int
	var vProvider *string
	mustScan(t, pool.QueryRow(ctx, `SELECT loan_amount::float8, monthly_payment::float8, registration_cost::float8,
		registration_renewal_month, insurance_provider FROM vehicles WHERE id = $1`, f.vehicle), &vLoan, &vPayment, &vReg, &vMonth, &vProvider)
	if vLoan != nil || !eqF(vPayment, 300) || !eqF(vReg, 120) || vMonth != nil || vProvider == nil || !strings.EqualFold(strings.TrimSpace(*vProvider), "geico") {
		t.Errorf("down vehicle: %v %v %v %v %v", vLoan, vPayment, vReg, vMonth, vProvider)
	}

	// Up again: everything equals its row; nothing is copied twice.
	applyMigration(t, pool, "069_drop_finance_columns.up.sql")
	if after := snapshot(); after != before {
		t.Errorf("down+up changed loans/bills/vendors/events:\n%s\n%s", before, after)
	}
}

func eqF(p *float64, want float64) bool { return p != nil && *p == want }
func eqS(p *string, want string) bool   { return p != nil && *p == want }
