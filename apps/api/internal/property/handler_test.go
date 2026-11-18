package property

// #4806: a property is readable by every role (ResProperties read =
// rolesEveryone), and since #4704 its money fields are derived from the
// family's loans and bills (ResBills/ResLoans read = rolesReaders). Before
// this, GET /api/v1/properties[/{id}] handed a housesitter or a vendor the
// mortgage, the mortgage account number, the tax and insurance amounts and
// the HOA fee: a side door around the money policy
// (KB decisions/2026-11-15-role-policy.md).
//
// These tests run against a real Postgres (TEST_DATABASE_URL, every
// migration applied) and ask the same property as every role, once through
// the list and once through the single get.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
)

// moneyFields is every finance field of a property response. The table is
// spelled out here, independently of the Go structs, so a money field added
// later fails these tests until it is covered on purpose.
var moneyFields = []string{
	"purchase_price", "purchase_date", "current_value", "down_payment",
	"mortgage_amount", "mortgage_rate", "mortgage_term_months",
	"mortgage_start_date", "mortgage_lender", "mortgage_account_number",
	"property_tax_annual", "property_tax_due_months",
	"insurance_annual", "insurance_provider", "hoa_fee_monthly",
}

// identityFields are what a property response always carries, whatever the
// role: the record itself.
var identityFields = []string{"id", "household_id", "name", "address", "property_type", "notes", "created_at", "updated_at"}

type propertyFixture struct {
	pool      *pgxpool.Pool
	router    http.Handler
	household string
	property  string
}

// setupProperty seeds a family with one property carrying a mortgage loan and
// tax, insurance and HOA bills, the way the demo seed and the UI create them
// (#4704): the money lives only in loans and bills.
func setupProperty(t *testing.T) *propertyFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var hid string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Property money roles') RETURNING id::text`).Scan(&hid))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hid)
	})

	var pid string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO properties
		(household_id, name, address, property_type, notes, purchase_price, purchase_date, current_value, down_payment)
		VALUES ($1, 'Main Residence', '12 Elm Street', 'house', 'Burgundy front door',
		        320000, '2018-06-15', 425000, 64000)
		RETURNING id::text`, hid).Scan(&pid))

	// The mortgage, as property_vehicle_finance_test.go seeds it.
	_, err = pool.Exec(ctx, `INSERT INTO loans
		(household_id, property_id, name, lender, account_number, original_amount, remaining_balance,
		 interest_rate, term_months, monthly_payment, start_date)
		VALUES ($1, $2, 'Main Residence mortgage', 'Wells Fargo', 'WF-12345678',
		        295000, 262000, 4.25, 360, 1450.75, '2018-07-01')`, hid, pid)
	require.NoError(t, err)

	mustInsert := func(name, category, frequency, dueDate string, amount string) {
		_, err := pool.Exec(ctx, `INSERT INTO bills
			(household_id, property_id, name, category, frequency, due_date, amount)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, hid, pid, name, category, frequency, dueDate, amount)
		require.NoError(t, err)
	}
	mustInsert("Main Residence property tax", "Taxes", "annual", "2026-12-15", "3600")
	mustInsert("Main Residence insurance", "Insurance", "annual", "2027-03-01", "1800")
	mustInsert("Main Residence HOA fee", "HOA", "monthly", "2026-12-01", "50")

	// insurance_provider is the insurance bill's vendor (ProviderSQL).
	var vid string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO vendors (household_id, name, trades)
		VALUES ($1, 'State Farm', ARRAY['insurance']) RETURNING id::text`, hid).Scan(&vid))
	_, err = pool.Exec(ctx, `UPDATE bills SET vendor_id = $1 WHERE household_id = $2 AND category = 'Insurance'`, vid, hid)
	require.NoError(t, err)

	f := &propertyFixture{pool: pool, household: hid, property: pid}
	f.router = propertyRouter(pool)
	return f
}

// propertyRouter mounts the property routes with claims taken from the
// X-Test-Role header, the way RequireAuth would have put them in context. The
// route policy itself is covered by cmd/api/routes_test.go; these tests are
// about what the response carries for a role.
func propertyRouter(pool *pgxpool.Pool) http.Handler {
	h := NewHandler(NewRepo(pool))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := &auth.Claims{
				HouseholdID: req.Header.Get("X-Test-Household"),
				Role:        req.Header.Get("X-Test-Role"),
			}
			next.ServeHTTP(w, req.WithContext(middleware.WithClaims(req.Context(), c)))
		})
	})
	r.Get("/api/v1/properties", h.ListProperties)
	r.Get("/api/v1/properties/{id}", h.GetProperty)
	r.Get("/api/v1/properties/{id}/rooms", h.ListRooms)
	return r
}

// get asks for path as role and returns the status and the decoded body.
func (f *propertyFixture) get(t *testing.T, role, path string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, &bytes.Buffer{})
	req.Header.Set("X-Test-Household", f.household)
	req.Header.Set("X-Test-Role", role)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	var body map[string]any
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	}
	return rec.Code, body
}

// TestPropertyMoneyIsHiddenFromRolesWithoutMoney is the #4806 test: owner,
// family_manager, family_member and viewer see the derived amounts;
// housesitter, vendor and an unknown role get 200 with every money field
// null and the identity fields intact.
func TestPropertyMoneyIsHiddenFromRolesWithoutMoney(t *testing.T) {
	f := setupProperty(t)

	rolesWithMoney := []string{auth.RoleOwner, auth.RoleFamilyManager, auth.RoleFamilyMember, auth.RoleViewer}
	rolesWithoutMoney := []string{auth.RoleHousesitter, auth.RoleVendor, "something_else"}

	// What the family sees, field by field: the mortgage comes from the loan,
	// the tax and insurance amounts are per year, the HOA fee per month.
	wantMoney := map[string]string{
		"purchase_price":          "320000.00",
		"purchase_date":           "2018-06-15",
		"current_value":           "425000.00",
		"down_payment":            "64000.00",
		"mortgage_amount":         "295000.00",
		"mortgage_rate":           "4.250",
		"mortgage_term_months":    "360",
		"mortgage_start_date":     "2018-07-01",
		"mortgage_lender":         "Wells Fargo",
		"mortgage_account_number": "WF-12345678",
		"property_tax_annual":     "3600.00",
		"property_tax_due_months": "12",
		"insurance_annual":        "1800.00",
		"insurance_provider":      "State Farm",
		"hoa_fee_monthly":         "50.00",
	}
	for _, field := range moneyFields {
		require.Contains(t, wantMoney, field, "moneyFields and wantMoney must list the same fields")
	}

	// Both ways in: the list and the single get.
	paths := map[string]func(t *testing.T, role string) (int, map[string]any){
		"GET /properties": func(t *testing.T, role string) (int, map[string]any) {
			code, body := f.get(t, role, "/api/v1/properties")
			if code != http.StatusOK {
				return code, nil
			}
			rows, ok := body["data"].([]any)
			require.True(t, ok, "list response has no data array: %v", body)
			require.Len(t, rows, 1)
			row, ok := rows[0].(map[string]any)
			require.True(t, ok)
			return code, row
		},
		"GET /properties/{id}": func(t *testing.T, role string) (int, map[string]any) {
			code, body := f.get(t, role, "/api/v1/properties/"+f.property)
			if code != http.StatusOK {
				return code, nil
			}
			row, ok := body["data"].(map[string]any)
			require.True(t, ok, "get response has no data object: %v", body)
			return code, row
		},
	}

	for pathName, fetch := range paths {
		for _, role := range rolesWithMoney {
			t.Run(pathName+"/"+role+" sees money", func(t *testing.T) {
				code, row := fetch(t, role)
				require.Equal(t, http.StatusOK, code)
				for _, field := range moneyFields {
					assert.Equal(t, wantMoney[field], row[field], "%s must be visible to %s", field, role)
				}
			})
		}

		for _, role := range rolesWithoutMoney {
			t.Run(pathName+"/"+role+" sees no money", func(t *testing.T) {
				code, row := fetch(t, role)
				require.Equal(t, http.StatusOK, code, "the role may still read the property")
				for _, field := range moneyFields {
					value, present := row[field]
					assert.True(t, present, "%s must be present as null, not dropped: the response shape does not depend on the role", field)
					assert.Nil(t, value, "%s must be null for %s", field, role)
				}
				// What the housesitter needs is still there.
				assert.Equal(t, f.property, row["id"])
				assert.Equal(t, "Main Residence", row["name"])
				assert.Equal(t, "12 Elm Street", row["address"])
				assert.Equal(t, "house", row["property_type"])
				assert.Equal(t, "Burgundy front door", row["notes"])
				for _, field := range identityFields {
					assert.Contains(t, row, field, "identity field %s must stay", field)
				}
			})
		}
	}
}

// TestSelectPropertySQLSkipsMoneyQuery pins the other half of the fix: for a
// role without money read the loans and bills are not in the query at all, so
// a housesitter's property read cannot be slowed, broken or made to leak a
// value by a loan or bill row. Both forms return the same number of columns
// of the same type in the same order (checked by Postgres itself), so the
// struct scan and the JSON shape do not depend on the role.
func TestSelectPropertySQLSkipsMoneyQuery(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	type column struct {
		name string
		oid  uint32
	}
	columns := func(withMoney bool) []column {
		rows, err := pool.Query(ctx, selectPropertySQL(withMoney))
		require.NoError(t, err, "the %v select must be valid SQL", withMoney)
		defer rows.Close()
		out := make([]column, 0, 24)
		for _, f := range rows.FieldDescriptions() {
			out = append(out, column{name: string(f.Name), oid: f.DataTypeOID})
		}
		for rows.Next() { // drain: values are not compared here
		}
		require.NoError(t, rows.Err())
		return out
	}

	withMoney, withoutMoney := columns(true), columns(false)

	// Same shape: the scan into Property is positional, so this is the
	// invariant that lets one struct serve both.
	require.Len(t, withMoney, len(moneyFields)+len(identityFields),
		"the select must return the Property struct's columns")
	require.Len(t, withoutMoney, len(withMoney), "the two forms must return the same columns")
	for i := range withMoney {
		assert.Equal(t, withMoney[i].oid, withoutMoney[i].oid, "column %d has a different type without money", i)
	}

	// The identity columns are real columns, so both forms name them alike;
	// the money columns carry values only in the money form (the NULL form
	// selects NULL::text and has no column names to preserve).
	firstNames := func(cols []column, n int) []string {
		out := make([]string, n)
		for i := 0; i < n; i++ {
			out[i] = cols[i].name
		}
		return out
	}
	assert.Equal(t, identityFields, firstNames(withoutMoney, len(identityFields)))
	assert.Equal(t, identityFields, firstNames(withMoney, len(identityFields)))

	noMoneySQL := selectPropertySQL(false)
	for _, name := range []string{"bills", "loans", "vendors", "LATERAL"} {
		assert.NotContains(t, noMoneySQL, name, "the no-money select must not read %s", name)
	}
	assert.Contains(t, noMoneySQL, "FROM properties p")

	moneySQL := selectPropertySQL(true)
	for _, name := range []string{"bills b", "loans l"} {
		assert.Contains(t, moneySQL, name, "the money select derives its fields from %s", name)
	}
}

// TestPropertyRepoNullsMoneyWithoutMoneyRead is the SQL half of the handler
// test above: the repo, told the role may not see money, returns the row's
// own fields and NULL in every money column even though the family has a
// mortgage loan and three bills.
func TestPropertyRepoNullsMoneyWithoutMoneyRead(t *testing.T) {
	f := setupProperty(t)
	ctx := context.Background()
	hid, err := uuid.Parse(f.household)
	require.NoError(t, err)
	pid, err := uuid.Parse(f.property)
	require.NoError(t, err)
	repo := NewRepo(f.pool)

	// The family's read sees the derived values.
	seen, err := repo.GetProperty(ctx, pid, hid, true)
	require.NoError(t, err)
	require.NotNil(t, seen)
	assert.Equal(t, "295000.00", deref(seen.MortgageAmount))
	assert.Equal(t, "WF-12345678", deref(seen.MortgageAccountNumber))
	assert.Equal(t, "3600.00", deref(seen.PropertyTaxAnnual))

	// A housesitter's read of the same row: identity only.
	hidden, err := repo.GetProperty(ctx, pid, hid, false)
	require.NoError(t, err)
	require.NotNil(t, hidden)
	assert.Equal(t, "Main Residence", hidden.Name)
	assert.Equal(t, "12 Elm Street", deref(hidden.Address))
	for name, field := range map[string]*string{
		"purchase_price": hidden.PurchasePrice, "purchase_date": hidden.PurchaseDate,
		"current_value": hidden.CurrentValue, "down_payment": hidden.DownPayment,
		"mortgage_amount": hidden.MortgageAmount, "mortgage_rate": hidden.MortgageRate,
		"mortgage_term_months": hidden.MortgageTermMonths, "mortgage_start_date": hidden.MortgageStartDate,
		"mortgage_lender": hidden.MortgageLender, "mortgage_account_number": hidden.MortgageAccountNumber,
		"property_tax_annual": hidden.PropertyTaxAnnual, "property_tax_due_months": hidden.PropertyTaxDueMonths,
		"insurance_annual": hidden.InsuranceAnnual, "insurance_provider": hidden.InsuranceProvider,
		"hoa_fee_monthly": hidden.HOAFeeMonthly,
	} {
		assert.Nil(t, field, "%s must be NULL without money read", name)
	}

	// And in the list, for the same role.
	list, err := repo.ListProperties(ctx, hid, false)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Nil(t, list[0].MortgageAmount)
	assert.Nil(t, list[0].HOAFeeMonthly)
	assert.Equal(t, "Main Residence", list[0].Name)
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// TestPropertyRoomsDoNotCarryMoney: the room routes only check that the
// property exists, so their response carries no money field at all.
func TestPropertyRoomsDoNotCarryMoney(t *testing.T) {
	f := setupProperty(t)

	for _, role := range []string{auth.RoleOwner, auth.RoleHousesitter, auth.RoleVendor} {
		code, body := f.get(t, role, "/api/v1/properties/"+f.property+"/rooms")
		assert.Equal(t, http.StatusOK, code, "%s must get the room list of an existing property", role)
		assert.Contains(t, body, "data")
		for _, field := range moneyFields {
			assert.NotContains(t, body, field, "a room list carries no money field")
		}
	}
}

// TestCanSeeMoneyMatchesPolicy ties the helper the property handlers use to
// the policy table itself, so a later edit to either cannot drift silently.
func TestCanSeeMoneyMatchesPolicy(t *testing.T) {
	for _, role := range auth.AllRoles {
		want := auth.Can(role, auth.ResBills, auth.ActRead) && auth.Can(role, auth.ResLoans, auth.ActRead)
		assert.Equal(t, want, auth.CanSeeMoney(role), "role %s", role)
	}
	for _, role := range []string{auth.RoleOwner, auth.RoleFamilyManager, auth.RoleFamilyMember, auth.RoleViewer} {
		assert.True(t, auth.CanSeeMoney(role), "%s reads bills and loans", role)
	}
	for _, role := range []string{auth.RoleHousesitter, auth.RoleVendor, "", "admin"} {
		assert.False(t, auth.CanSeeMoney(role), "%s must not see money", role)
	}
}
