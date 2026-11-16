package domainv2_test

// #4704: the property/vehicle finance fields (mortgage_*, property_tax_*,
// insurance_*, hoa_fee_monthly; lender, loan_*, monthly_payment,
// registration_*, insurance_*) are derived from the subject's loan and bills
// and written there. The API never reads or writes the legacy columns.

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/asset"
	"home-os/api/internal/bill"
	"home-os/api/internal/config"
	"home-os/api/internal/loan"
	"home-os/api/internal/property"
	"home-os/api/internal/vehicle"
)

func financeRouter(pool *pgxpool.Pool) http.Handler {
	r := chi.NewRouter()
	ph := property.NewHandler(property.NewRepo(pool))
	r.Get("/properties", ph.ListProperties)
	r.Post("/properties", ph.CreateProperty)
	r.Get("/properties/{id}", ph.GetProperty)
	r.Put("/properties/{id}", ph.UpdateProperty)
	vh := vehicle.NewHandler(vehicle.NewRepo(pool))
	r.Get("/vehicles", vh.List)
	r.Post("/vehicles", vh.Create)
	r.Get("/vehicles/{id}", vh.Get)
	r.Put("/vehicles/{id}", vh.Update)
	r.Post("/bills", bill.NewHandler(bill.NewRepo(pool)).Create)
	r.Post("/loans", loan.NewHandler(loan.NewRepo(pool)).Create)
	r.Post("/assets", asset.NewHandler(asset.NewRepo(pool), &config.Config{}).Create)
	return r
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func TestPropertyFinanceFieldsLiveInLoansAndBills(t *testing.T) {
	pool := testDB(t)
	r := financeRouter(pool)
	a := family(t, pool)
	ctx := context.Background()

	res := call(t, r, a, "POST", "/properties", map[string]any{
		"name": "Main", "purchase_price": "320000",
		"mortgage_amount": "$295,000", "mortgage_rate": "4.25", "mortgage_term_months": "360",
		"mortgage_start_date": "2018-07-01", "mortgage_lender": "Wells Fargo", "mortgage_account_number": "WF-1",
		"property_tax_annual": "3600", "property_tax_due_months": "Jan, Jul",
		"insurance_annual": "1800", "insurance_provider": "State Farm",
		"hoa_fee_monthly": "50",
	})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	id := res.id(t)
	d := res.data()
	assert.Equal(t, "295000.00", d["mortgage_amount"])
	assert.Equal(t, "4.250", d["mortgage_rate"])
	assert.Equal(t, "360", d["mortgage_term_months"])
	assert.Equal(t, "2018-07-01", d["mortgage_start_date"])
	assert.Equal(t, "Wells Fargo", d["mortgage_lender"])
	assert.Equal(t, "WF-1", d["mortgage_account_number"])
	assert.Equal(t, "3600.00", d["property_tax_annual"])
	assert.Equal(t, "Jan, Jul", d["property_tax_due_months"])
	assert.Equal(t, "1800.00", d["insurance_annual"])
	assert.Equal(t, "State Farm", d["insurance_provider"])
	assert.Equal(t, "50.00", d["hoa_fee_monthly"])

	// Typed-subject rows, nothing on the property row.
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM loans WHERE property_id = $1 AND household_id = $2
		AND name = 'Main mortgage' AND remaining_balance = 295000 AND term_end = '2048-07-01'`, id, a))
	assert.Equal(t, 3, count(t, pool, `SELECT count(*) FROM bills WHERE property_id = $1 AND household_id = $2`, id, a))
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM bills b JOIN vendors v ON v.id = b.vendor_id
		WHERE b.property_id = $1 AND v.household_id = $2 AND v.name = 'State Farm' AND 'insurance' = ANY (v.trades)`, id, a))
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM properties WHERE id = $1
		AND mortgage_amount IS NULL AND mortgage_lender IS NULL AND property_tax_annual IS NULL
		AND insurance_annual IS NULL AND insurance_provider IS NULL AND hoa_fee_monthly IS NULL`, id))

	// The UI sends back what it was shown (plus "" for blanks): nothing changes.
	const billState = `SELECT string_agg(concat_ws('|', name, amount, frequency, due_date, notes, vendor_id), ',' ORDER BY name)
		FROM bills WHERE property_id = $1`
	var before, after string
	require.NoError(t, pool.QueryRow(ctx, billState, id).Scan(&before))
	body := map[string]any{}
	for k, v := range d {
		body[k] = v
	}
	body["down_payment"] = ""
	res = call(t, r, a, "PUT", "/properties/"+id, body)
	require.Equal(t, http.StatusOK, res.Code, res.Body)
	assert.Equal(t, d["insurance_annual"], res.data()["insurance_annual"])
	require.NoError(t, pool.QueryRow(ctx, billState, id).Scan(&after))
	assert.Equal(t, before, after, "a round-trip changes no bill")
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM loans WHERE property_id = $1`, id))
	assert.Equal(t, 3, count(t, pool, `SELECT count(*) FROM bills WHERE property_id = $1`, id))
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM vendors WHERE household_id = $1`, a))

	// Partial update: a single due month on the annual tax bill becomes its
	// due_date; a new insurance amount on a monthly bill is converted.
	require.NoError(t, pool.QueryRow(ctx, `UPDATE bills SET frequency = 'monthly', amount = 150
		WHERE property_id = $1 AND category = 'Insurance' RETURNING 'x'`, id).Scan(new(string)))
	res = call(t, r, a, "PUT", "/properties/"+id, map[string]any{
		"property_tax_due_months": "4", "insurance_annual": "2400", "mortgage_rate": "3.5",
	})
	require.Equal(t, http.StatusOK, res.Code, res.Body)
	assert.Equal(t, "4", res.data()["property_tax_due_months"])
	assert.Equal(t, "2400.00", res.data()["insurance_annual"])
	assert.Equal(t, "3.500", res.data()["mortgage_rate"])
	assert.Equal(t, "295000.00", res.data()["mortgage_amount"], "absent fields keep their value")
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM bills WHERE property_id = $1 AND category = 'Insurance' AND amount = 200`, id))
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM bills WHERE property_id = $1 AND category = 'Taxes'
		AND EXTRACT(MONTH FROM due_date) = 4 AND (notes IS NULL OR notes NOT LIKE '%Due months%')`, id))

	// Bad values are 400.
	res = call(t, r, a, "PUT", "/properties/"+id, map[string]any{"mortgage_rate": "abc"})
	assert.Equal(t, http.StatusBadRequest, res.Code, res.Body)
	res = call(t, r, a, "POST", "/properties", map[string]any{"name": "X", "mortgage_term_months": "-1"})
	assert.Equal(t, http.StatusBadRequest, res.Code, res.Body)

	// Missing property: 404, nothing created.
	loans := count(t, pool, `SELECT count(*) FROM loans WHERE household_id = $1`, a)
	res = call(t, r, a, "PUT", "/properties/00000000-0000-0000-0000-000000000001", map[string]any{"mortgage_amount": "1000"})
	assert.Equal(t, http.StatusNotFound, res.Code)
	assert.Equal(t, loans, count(t, pool, `SELECT count(*) FROM loans WHERE household_id = $1`, a))
}

func TestPropertyFinanceDerivedFromExistingRows(t *testing.T) {
	pool := testDB(t)
	r := financeRouter(pool)
	a := family(t, pool)

	id := call(t, r, a, "POST", "/properties", map[string]any{"name": "Cabin"}).id(t)
	d := call(t, r, a, "GET", "/properties/"+id, nil).data()
	for _, k := range []string{"mortgage_amount", "property_tax_annual", "insurance_annual", "insurance_provider", "hoa_fee_monthly"} {
		assert.Nil(t, d[k], k)
	}

	// An asset bill gets the asset's property_id but is not the property's.
	assetID := call(t, r, a, "POST", "/assets", map[string]any{"name": "Boat", "property_id": id}).id(t)
	res := call(t, r, a, "POST", "/bills", map[string]any{"name": "Boat insurance", "amount": "99", "asset_id": assetID, "frequency": "annual"})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)

	// A quarterly tax bill and a HELOC + mortgage made through the bill/loan API.
	res = call(t, r, a, "POST", "/bills", map[string]any{"name": "County tax", "amount": "450", "frequency": "quarterly", "due_date": "2027-03-01", "property_id": id})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	res = call(t, r, a, "POST", "/loans", map[string]any{"name": "HELOC", "original_amount": "20000", "remaining_balance": "5000", "property_id": id})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	res = call(t, r, a, "POST", "/loans", map[string]any{"name": "Cabin Mortgage", "original_amount": "150000", "remaining_balance": "100000", "lender": "Chase", "property_id": id})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)

	list := call(t, r, a, "GET", "/properties", nil)
	items, _ := list.Body["data"].([]any)
	require.Len(t, items, 1)
	d = items[0].(map[string]any)
	assert.Equal(t, "1800.00", d["property_tax_annual"], "quarterly x4")
	assert.Equal(t, "3", d["property_tax_due_months"])
	assert.Nil(t, d["insurance_annual"], "asset bills are not the property's")
	assert.Equal(t, "150000.00", d["mortgage_amount"], "a mortgage-named loan wins")
	assert.Equal(t, "Chase", d["mortgage_lender"])

	// Another family sees nothing of it.
	b := family(t, pool)
	res = call(t, r, b, "GET", "/properties/"+id, nil)
	assert.Equal(t, http.StatusNotFound, res.Code)
}

func TestVehicleFinanceFieldsLiveInLoansAndBills(t *testing.T) {
	pool := testDB(t)
	r := financeRouter(pool)
	a := family(t, pool)

	res := call(t, r, a, "POST", "/vehicles", map[string]any{
		"year": 2022, "make": "Ford", "model": "F-150",
		"lender": "Ford Credit", "loan_amount": "12000", "loan_term_months": 60, "monthly_payment": "350",
		"registration_cost": "150", "registration_renewal_month": 5,
		"insurance_cost": "1200", "insurance_provider": "Geico",
	})
	require.Equal(t, http.StatusCreated, res.Code, res.Body)
	id := res.id(t)
	d := res.data()
	assert.Equal(t, "Ford Credit", d["lender"])
	assert.Equal(t, "12000.00", d["loan_amount"])
	assert.EqualValues(t, 60, d["loan_term_months"])
	assert.Equal(t, "350.00", d["monthly_payment"])
	assert.Equal(t, "150.00", d["registration_cost"])
	assert.EqualValues(t, 5, d["registration_renewal_month"])
	assert.Equal(t, "1200.00", d["insurance_cost"])
	assert.Equal(t, "Geico", d["insurance_provider"])

	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM loans WHERE vehicle_id = $1 AND name = '2022 Ford F-150 loan'`, id))
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM bills WHERE vehicle_id = $1 AND name = '2022 Ford F-150 registration'
		AND frequency = 'annual' AND EXTRACT(MONTH FROM due_date) = 5`, id))
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM vehicles WHERE id = $1 AND lender IS NULL AND loan_amount IS NULL
		AND monthly_payment IS NULL AND registration_cost IS NULL AND insurance_cost IS NULL AND insurance_provider IS NULL`, id))

	// The generic entity form sends null for blank fields: the loan and bills
	// are left alone (the vehicle's own columns are still replaced).
	res = call(t, r, a, "PUT", "/vehicles/"+id, map[string]any{
		"year": 2022, "make": "Ford", "model": "F-150", "color": "Black",
		"lender": nil, "loan_amount": nil, "loan_term_months": nil, "monthly_payment": "400",
		"registration_cost": nil, "registration_renewal_month": nil, "insurance_cost": nil, "insurance_provider": nil,
	})
	require.Equal(t, http.StatusOK, res.Code, res.Body)
	d = res.data()
	assert.Equal(t, "Black", d["color"])
	assert.Equal(t, "Ford Credit", d["lender"])
	assert.Equal(t, "400.00", d["monthly_payment"])
	assert.Equal(t, "1200.00", d["insurance_cost"])
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM loans WHERE vehicle_id = $1`, id))
	assert.Equal(t, 2, count(t, pool, `SELECT count(*) FROM bills WHERE vehicle_id = $1`, id))

	// A vehicle with nothing: all null.
	other := call(t, r, a, "POST", "/vehicles", map[string]any{"make": "Toyota"}).id(t)
	d = call(t, r, a, "GET", "/vehicles/"+other, nil).data()
	assert.Nil(t, d["loan_amount"])
	assert.Nil(t, d["registration_renewal_month"])
	assert.Equal(t, 0, count(t, pool, `SELECT count(*) FROM loans WHERE vehicle_id = $1`, other))

	res = call(t, r, a, "PUT", "/vehicles/"+id, map[string]any{"make": "Ford", "registration_renewal_month": 13})
	assert.Equal(t, http.StatusBadRequest, res.Code, res.Body)
	res = call(t, r, a, "PUT", "/vehicles/00000000-0000-0000-0000-000000000001", map[string]any{"make": "x", "loan_amount": "5"})
	assert.Equal(t, http.StatusNotFound, res.Code)
}
