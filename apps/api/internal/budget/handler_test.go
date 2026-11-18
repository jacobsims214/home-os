package budget_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/bill"
	"home-os/api/internal/budget"
	"home-os/api/internal/loan"
	"home-os/api/internal/middleware"
)

const testIssuer = "https://dex.test/dex"

type fixture struct {
	pool *pgxpool.Pool
	h    http.Handler
	key  *rsa.PrivateKey
}

// setup connects to TEST_DATABASE_URL (all migrations applied; skips when
// unset) and builds a router like cmd/api/main.go: the budget, bill and loan
// routes behind the real RequireAuth with the Postgres member resolver, so
// X-Household-ID is resolved exactly as in production.
func setup(t *testing.T, tweak ...func(*budget.Handler) *budget.Handler) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{key.Public()}}
	v := auth.NewVerifierWithKeySet(testIssuer, ks, []string{"home-os-ui"}).
		WithResolver(auth.NewMemberResolver(pool))

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(v))
		h := budget.NewHandler(budget.NewRepo(pool))
		for _, fn := range tweak {
			h = fn(h)
		}
		h.RegisterRoutes(r)
		bh := bill.NewHandler(bill.NewRepo(pool))
		r.Post("/api/v1/bills", bh.Create)
		r.Get("/api/v1/bills/{id}", bh.Get)
		r.Put("/api/v1/bills/{id}", bh.Update)
		r.Delete("/api/v1/bills/{id}", bh.Delete)
		lh := loan.NewHandler(loan.NewRepo(pool))
		r.Post("/api/v1/loans", lh.Create)
		r.Get("/api/v1/loans/{id}", lh.Get)
		r.Put("/api/v1/loans/{id}", lh.Update)
	})
	return &fixture{pool: pool, h: r, key: key}
}

type member struct{ token, userID, householdID string }

func (f *fixture) household(t *testing.T) string {
	t.Helper()
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`INSERT INTO households (name, timezone) VALUES ('Budget HH', 'America/Chicago') RETURNING id::text`).Scan(&id))
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, id) })
	return id
}

func (f *fixture) member(t *testing.T, hhID, role string) member {
	t.Helper()
	ctx := context.Background()
	email := "budget-" + uuid.NewString()[:8] + "@example.com"
	sub := "CiQ" + uuid.NewString() + "EgVsb2NhbA"
	var userID string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, $2, 'x', $3) RETURNING id::text`,
		email, "Member "+role, sub).Scan(&userID))
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	f.join(t, hhID, userID, role)

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": testIssuer, "aud": "home-os-ui", "sub": sub, "email": email,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	token, err := tok.SignedString(f.key)
	require.NoError(t, err)
	return member{token: token, userID: userID, householdID: hhID}
}

func (f *fixture) join(t *testing.T, hhID, userID, role string) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hhID, userID, role)
	require.NoError(t, err)
}

type req struct {
	method, path string
	as           member
	body         any
	household    string // X-Household-ID, optional
}

func (f *fixture) do(t *testing.T, rq req) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if rq.body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(rq.body))
	}
	r := httptest.NewRequest(rq.method, rq.path, &buf)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+rq.as.token)
	if rq.household != "" {
		r.Header.Set("X-Household-ID", rq.household)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out struct {
		Data T `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out.Data
}

type categoryJSON struct {
	ID            string  `json:"id"`
	HouseholdID   string  `json:"household_id"`
	Name          string  `json:"name"`
	MonthlyAmount *string `json:"monthly_amount"`
	Notes         *string `json:"notes"`
}

type billJSON struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Amount           *string `json:"amount"`
	Frequency        string  `json:"frequency"`
	DueDay           *int    `json:"due_day"`
	DueDate          *string `json:"due_date"`
	BudgetCategoryID *string `json:"budget_category_id"`
	PaidDate         *string `json:"paid_date"`
	PaidForDueDate   *string `json:"paid_for_due_date"`
}

type loanJSON struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	MonthlyPayment   *string `json:"monthly_payment"`
	InterestRate     *string `json:"interest_rate"`
	Lender           *string `json:"lender"`
	DueDay           *int    `json:"due_day"`
	TermEnd          *string `json:"term_end"`
	AccountNumber    *string `json:"account_number"`
	BudgetCategoryID *string `json:"budget_category_id"`
}

func ptr[T any](v T) *T { return &v }

func (f *fixture) category(t *testing.T, as member, body map[string]any) categoryJSON {
	t.Helper()
	rec := f.do(t, req{method: http.MethodPost, path: "/api/v1/budget/categories", as: as, body: body})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	return decode[categoryJSON](t, rec)
}

func TestCategoryCRUD(t *testing.T) {
	f := setup(t)
	hh := f.household(t)
	owner := f.member(t, hh, auth.RoleOwner)

	c := f.category(t, owner, map[string]any{"name": "  Housing ", "monthly_amount": "2000", "notes": "rent + utilities"})
	assert.Equal(t, "Housing", c.Name)
	assert.Equal(t, hh, c.HouseholdID)
	assert.Equal(t, ptr("2000.00"), c.MonthlyAmount)

	rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/budget/categories/" + c.ID, as: owner})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, c, decode[categoryJSON](t, rec))

	// Same name, other case and spacing: one per family.
	rec = f.do(t, req{method: http.MethodPost, path: "/api/v1/budget/categories", as: owner, body: map[string]any{"name": "housing"}})
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	for _, body := range []map[string]any{
		{"name": "  "},
		{"name": "Food", "monthly_amount": "-5"},
		{"name": "Food", "monthly_amount": "12.345"},
	} {
		rec = f.do(t, req{method: http.MethodPost, path: "/api/v1/budget/categories", as: owner, body: body})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%v: %s", body, rec.Body.String())
	}

	f.category(t, owner, map[string]any{"name": "Food"})
	rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/budget/categories", as: owner})
	require.Equal(t, http.StatusOK, rec.Code)
	list := decode[[]categoryJSON](t, rec)
	require.Len(t, list, 2)
	assert.Equal(t, "Food", list[0].Name)
	assert.Nil(t, list[0].MonthlyAmount)

	// Partial update: notes only keeps the amount; null clears it.
	rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/budget/categories/" + c.ID, as: owner, body: map[string]any{"notes": "rent"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decode[categoryJSON](t, rec)
	assert.Equal(t, ptr("rent"), got.Notes)
	assert.Equal(t, ptr("2000.00"), got.MonthlyAmount)

	rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/budget/categories/" + c.ID, as: owner, body: map[string]any{"monthly_amount": nil, "name": "Home"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got = decode[categoryJSON](t, rec)
	assert.Nil(t, got.MonthlyAmount)
	assert.Equal(t, "Home", got.Name)

	rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/budget/categories/" + c.ID, as: owner, body: map[string]any{"name": "FOOD"}})
	assert.Equal(t, http.StatusConflict, rec.Code)

	rec = f.do(t, req{method: http.MethodDelete, path: "/api/v1/budget/categories/" + c.ID, as: owner})
	assert.Equal(t, http.StatusNoContent, rec.Code)
	rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/budget/categories/" + c.ID, as: owner})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = f.do(t, req{method: http.MethodDelete, path: "/api/v1/budget/categories/" + c.ID, as: owner})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/budget/categories/not-a-uuid", as: owner})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCategoryPermissions(t *testing.T) {
	f := setup(t)
	hh := f.household(t)
	owner := f.member(t, hh, auth.RoleOwner)
	c := f.category(t, owner, map[string]any{"name": "Kids"})

	cases := []struct {
		role        string
		read, write int
	}{
		{auth.RoleFamilyManager, http.StatusOK, http.StatusCreated},
		{auth.RoleFamilyMember, http.StatusOK, http.StatusCreated},
		{auth.RoleViewer, http.StatusOK, http.StatusForbidden},
		{auth.RoleHousesitter, http.StatusForbidden, http.StatusForbidden},
		{auth.RoleVendor, http.StatusForbidden, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			m := f.member(t, hh, tc.role)
			rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/budget/categories/" + c.ID, as: m})
			assert.Equal(t, tc.read, rec.Code, rec.Body.String())
			rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/budget/obligations", as: m})
			assert.Equal(t, tc.read, rec.Code, rec.Body.String())
			rec = f.do(t, req{method: http.MethodPost, path: "/api/v1/budget/categories", as: m, body: map[string]any{"name": "By " + tc.role}})
			assert.Equal(t, tc.write, rec.Code, rec.Body.String())
			if tc.write != http.StatusCreated {
				rec = f.do(t, req{method: http.MethodDelete, path: "/api/v1/budget/categories/" + c.ID, as: m})
				assert.Equal(t, http.StatusForbidden, rec.Code)
			}
		})
	}
}

func TestCrossFamily(t *testing.T) {
	f := setup(t)
	hhA, hhB := f.household(t), f.household(t)
	a := f.member(t, hhA, auth.RoleOwner)
	b := f.member(t, hhB, auth.RoleOwner)
	catB := f.category(t, b, map[string]any{"name": "B's"})

	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec := f.do(t, req{method: m, path: "/api/v1/budget/categories/" + catB.ID, as: a, body: map[string]any{"name": "x"}})
		assert.Equal(t, http.StatusNotFound, rec.Code, "%s: %s", m, rec.Body.String())
	}
	// Another family's category can't be put on a bill or loan.
	rec := f.do(t, req{method: http.MethodPost, path: "/api/v1/bills", as: a, body: map[string]any{"name": "x", "budget_category_id": catB.ID}})
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	rec = f.do(t, req{method: http.MethodPost, path: "/api/v1/loans", as: a, body: map[string]any{
		"name": "x", "original_amount": "10", "remaining_balance": "10", "budget_category_id": catB.ID}})
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	// A member of both families picks one with X-Household-ID.
	f.join(t, hhB, a.userID, auth.RoleViewer)
	rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/budget/categories", as: a, household: hhB})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	list := decode[[]categoryJSON](t, rec)
	require.Len(t, list, 1)
	assert.Equal(t, catB.ID, list[0].ID)
	rec = f.do(t, req{method: http.MethodPost, path: "/api/v1/budget/categories", as: a, household: hhB, body: map[string]any{"name": "nope"}})
	assert.Equal(t, http.StatusForbidden, rec.Code, "viewer in B")
	rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/budget/categories", as: a})
	require.Equal(t, http.StatusOK, rec.Code)
	// Which family is the default is the resolver's call; it must be one of
	// the two and never mix them.
	for _, c := range decode[[]categoryJSON](t, rec) {
		assert.Contains(t, []string{hhA, hhB}, c.HouseholdID)
	}
}

func TestBillFieldsRoundTrip(t *testing.T) {
	f := setup(t)
	hh := f.household(t)
	owner := f.member(t, hh, auth.RoleOwner)
	cat := f.category(t, owner, map[string]any{"name": "Insurance"})

	rec := f.do(t, req{method: http.MethodPost, path: "/api/v1/bills", as: owner, body: map[string]any{
		"name": "Home insurance", "amount": "1200.00", "frequency": "annual",
		"due_date": "2027-03-01", "budget_category_id": cat.ID,
	}})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	b := decode[billJSON](t, rec)
	assert.Equal(t, "annual", b.Frequency)
	assert.Equal(t, ptr("2027-03-01"), b.DueDate)
	assert.Equal(t, ptr(cat.ID), b.BudgetCategoryID)
	assert.Nil(t, b.DueDay)

	rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/bills/" + b.ID, as: owner})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, b, decode[billJSON](t, rec))

	// Default frequency is monthly.
	rec = f.do(t, req{method: http.MethodPost, path: "/api/v1/bills", as: owner, body: map[string]any{"name": "Internet", "due_day": 5}})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	m := decode[billJSON](t, rec)
	assert.Equal(t, "monthly", m.Frequency)
	assert.Equal(t, ptr(5), m.DueDay)

	for _, body := range []map[string]any{
		{"name": "x", "frequency": "weekly"},
		{"name": "x", "frequency": "annual", "due_day": 3},
		{"name": "x", "due_date": "2027-01-01"},
		{"name": "x", "frequency": "once", "due_date": "01/02/2027"},
		{"name": "x", "due_day": 32},
		{"name": "x", "budget_category_id": "nope"},
	} {
		rec = f.do(t, req{method: http.MethodPost, path: "/api/v1/bills", as: owner, body: body})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%v: %s", body, rec.Body.String())
	}

	// A payment recorded by the MCP bill_pay shows up read-only.
	_, err := f.pool.Exec(context.Background(),
		`UPDATE bills SET paid_date = '2026-03-02', paid_for_due_date = '2026-03-01' WHERE id = $1`, b.ID)
	require.NoError(t, err)
	rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/bills/" + b.ID, as: owner, body: map[string]any{"name": "Home insurance (Acme)"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	b = decode[billJSON](t, rec)
	assert.Equal(t, ptr("2026-03-02"), b.PaidDate)
	assert.Equal(t, ptr("2026-03-01"), b.PaidForDueDate, "unchanged schedule keeps coverage")
	assert.Equal(t, ptr(cat.ID), b.BudgetCategoryID, "absent keeps the category")

	// Moving the due date resets coverage (052).
	rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/bills/" + b.ID, as: owner, body: map[string]any{"due_date": "2027-04-01"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	b = decode[billJSON](t, rec)
	assert.Equal(t, ptr("2027-04-01"), b.DueDate)
	assert.Nil(t, b.PaidForDueDate)

	// Switching to monthly drops the due_date; due_day is now allowed.
	rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/bills/" + b.ID, as: owner, body: map[string]any{"frequency": "monthly", "due_day": 15, "budget_category_id": nil}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	b = decode[billJSON](t, rec)
	assert.Equal(t, "monthly", b.Frequency)
	assert.Equal(t, ptr(15), b.DueDay)
	assert.Nil(t, b.DueDate)
	assert.Nil(t, b.BudgetCategoryID, "null clears the category")

	// And back: quarterly drops the stored due_day; sending one is an error.
	rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/bills/" + b.ID, as: owner, body: map[string]any{"frequency": "quarterly", "due_day": 15}})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/bills/" + b.ID, as: owner, body: map[string]any{"frequency": "quarterly", "due_date": "2027-01-15"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	b = decode[billJSON](t, rec)
	assert.Nil(t, b.DueDay)
	assert.Equal(t, ptr("2027-01-15"), b.DueDate)

	// Deleting the category keeps the bill, uncategorized.
	rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/bills/" + b.ID, as: owner, body: map[string]any{"budget_category_id": cat.ID}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = f.do(t, req{method: http.MethodDelete, path: "/api/v1/budget/categories/" + cat.ID, as: owner})
	require.Equal(t, http.StatusNoContent, rec.Code)
	rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/bills/" + b.ID, as: owner})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, decode[billJSON](t, rec).BudgetCategoryID)

	// Cross-family bill delete is a 404, not a 500.
	other := f.member(t, f.household(t), auth.RoleOwner)
	rec = f.do(t, req{method: http.MethodDelete, path: "/api/v1/bills/" + b.ID, as: other})
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestLoanFieldsRoundTrip(t *testing.T) {
	f := setup(t)
	hh := f.household(t)
	owner := f.member(t, hh, auth.RoleOwner)
	cat := f.category(t, owner, map[string]any{"name": "Housing"})

	base := map[string]any{
		"name": "Mortgage", "lender": "First Bank", "original_amount": "300000",
		"remaining_balance": "250000", "interest_rate": "6.25", "monthly_payment": "1850.00",
		"start_date": "2020-06-01",
	}
	body := map[string]any{"due_day": 1, "term_end": "2050-06-01", "account_number": "****1234", "budget_category_id": cat.ID}
	for k, v := range base {
		body[k] = v
	}
	rec := f.do(t, req{method: http.MethodPost, path: "/api/v1/loans", as: owner, body: body})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	l := decode[loanJSON](t, rec)
	assert.Equal(t, ptr(1), l.DueDay)
	assert.Equal(t, ptr("2050-06-01"), l.TermEnd)
	assert.Equal(t, ptr("****1234"), l.AccountNumber)
	assert.Equal(t, ptr(cat.ID), l.BudgetCategoryID)
	assert.Equal(t, ptr("First Bank"), l.Lender)
	assert.Equal(t, ptr("1850.00"), l.MonthlyPayment)

	rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/loans/" + l.ID, as: owner})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, l, decode[loanJSON](t, rec))

	// A PUT from a client that doesn't know the 044 fields keeps them.
	base["remaining_balance"] = "249000"
	rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/loans/" + l.ID, as: owner, body: base})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decode[loanJSON](t, rec)
	assert.Equal(t, l.DueDay, got.DueDay)
	assert.Equal(t, l.TermEnd, got.TermEnd)
	assert.Equal(t, l.AccountNumber, got.AccountNumber)
	assert.Equal(t, l.BudgetCategoryID, got.BudgetCategoryID)

	// Null clears; a value replaces.
	clear := map[string]any{"due_day": nil, "budget_category_id": nil, "account_number": "****9999"}
	for k, v := range base {
		clear[k] = v
	}
	rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/loans/" + l.ID, as: owner, body: clear})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got = decode[loanJSON](t, rec)
	assert.Nil(t, got.DueDay)
	assert.Nil(t, got.BudgetCategoryID)
	assert.Equal(t, ptr("****9999"), got.AccountNumber)
	assert.Equal(t, l.TermEnd, got.TermEnd)

	for _, bad := range []map[string]any{
		{"due_day": 40},
		{"term_end": "June 2050"},
		{"term_end": "2019-01-01"}, // before start_date (loans_term_end_check)
		{"budget_category_id": "x"},
	} {
		b := map[string]any{}
		for k, v := range base {
			b[k] = v
		}
		for k, v := range bad {
			b[k] = v
		}
		rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/loans/" + l.ID, as: owner, body: b})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%v: %s", bad, rec.Body.String())
	}

	other := f.member(t, f.household(t), auth.RoleOwner)
	rec = f.do(t, req{method: http.MethodPut, path: "/api/v1/loans/" + l.ID, as: other, body: base})
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

type itemJSON struct {
	Kind          string  `json:"kind"`
	Name          string  `json:"name"`
	Frequency     string  `json:"frequency"`
	Amount        *string `json:"amount"`
	MonthlyAmount *string `json:"monthly_amount"`
	DueDay        *int    `json:"due_day"`
	DueDate       *string `json:"due_date"`
}

type groupJSON struct {
	BudgetCategoryID *string    `json:"budget_category_id"`
	Name             string     `json:"name"`
	Budgeted         *string    `json:"budgeted"`
	Committed        string     `json:"committed"`
	Remaining        *string    `json:"remaining"`
	Items            []itemJSON `json:"items"`
}

type obligationsJSON struct {
	Month         string      `json:"month"`
	Categories    []groupJSON `json:"categories"`
	Uncategorized groupJSON   `json:"uncategorized"`
	Totals        struct {
		Budgeted     string `json:"budgeted"`
		Committed    string `json:"committed"`
		Bills        string `json:"bills"`
		LoanPayments string `json:"loan_payments"`
		Uncounted    int    `json:"uncounted"`
	} `json:"totals"`
}

func names(items []itemJSON) []string {
	out := []string{}
	for _, i := range items {
		out = append(out, i.Name)
	}
	return out
}

func TestObligations(t *testing.T) {
	f := setup(t)
	hh := f.household(t)
	owner := f.member(t, hh, auth.RoleOwner)
	housing := f.category(t, owner, map[string]any{"name": "Housing", "monthly_amount": "2000"})
	fun := f.category(t, owner, map[string]any{"name": "Fun"})

	post := func(path string, body map[string]any) {
		t.Helper()
		rec := f.do(t, req{method: http.MethodPost, path: path, as: owner, body: body})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	}
	post("/api/v1/loans", map[string]any{"name": "Mortgage", "original_amount": "300000", "remaining_balance": "250000",
		"monthly_payment": "1500", "due_day": 1, "budget_category_id": housing.ID})
	post("/api/v1/loans", map[string]any{"name": "Paid off car", "original_amount": "20000", "remaining_balance": "0",
		"monthly_payment": "400", "budget_category_id": housing.ID})
	post("/api/v1/loans", map[string]any{"name": "Ended loan", "original_amount": "1000", "remaining_balance": "10",
		"monthly_payment": "50", "start_date": "2020-01-01", "term_end": "2026-10-31"})
	post("/api/v1/loans", map[string]any{"name": "Family loan", "original_amount": "1000", "remaining_balance": "500"})
	post("/api/v1/bills", map[string]any{"name": "Home insurance", "amount": "1200", "frequency": "annual",
		"due_date": "2027-03-01", "budget_category_id": housing.ID})
	post("/api/v1/bills", map[string]any{"name": "Water", "amount": "100", "frequency": "quarterly"})
	post("/api/v1/bills", map[string]any{"name": "HOA", "amount": "125.50", "frequency": "semiannual", "budget_category_id": housing.ID})
	post("/api/v1/bills", map[string]any{"name": "Concert", "amount": "300", "frequency": "once",
		"due_date": "2026-11-20", "budget_category_id": fun.ID})
	post("/api/v1/bills", map[string]any{"name": "Streaming", "due_day": 9})

	get := func(month string) obligationsJSON {
		t.Helper()
		rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/budget/obligations?month=" + month, as: owner})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decode[obligationsJSON](t, rec)
	}

	nov := get("2026-11")
	assert.Equal(t, "2026-11", nov.Month)
	require.Len(t, nov.Categories, 2)
	assert.Equal(t, "Fun", nov.Categories[0].Name)
	assert.Equal(t, "300.00", nov.Categories[0].Committed)
	assert.Nil(t, nov.Categories[0].Budgeted)
	assert.Nil(t, nov.Categories[0].Remaining)

	h := nov.Categories[1]
	assert.Equal(t, ptr(housing.ID), h.BudgetCategoryID)
	assert.Equal(t, []string{"HOA", "Home insurance", "Mortgage"}, names(h.Items), "paid-off loan left out")
	// 1500 + 1200/12 + 125.50/6 (20.92) = 1620.92
	assert.Equal(t, "1620.92", h.Committed)
	assert.Equal(t, ptr("2000.00"), h.Budgeted)
	assert.Equal(t, ptr("379.08"), h.Remaining)
	assert.Equal(t, ptr("100.00"), h.Items[1].MonthlyAmount)
	assert.Equal(t, ptr("1200.00"), h.Items[1].Amount)
	assert.Equal(t, ptr("2027-03-01"), h.Items[1].DueDate)
	assert.Equal(t, "loan", h.Items[2].Kind)
	assert.Equal(t, ptr(1), h.Items[2].DueDay)

	assert.Nil(t, nov.Uncategorized.BudgetCategoryID)
	assert.Equal(t, []string{"Family loan", "Streaming", "Water"}, names(nov.Uncategorized.Items), "ended loan left out")
	assert.Equal(t, "33.33", nov.Uncategorized.Committed)

	assert.Equal(t, "2000.00", nov.Totals.Budgeted)
	assert.Equal(t, "454.25", nov.Totals.Bills) // 100 + 20.92 + 300 + 33.33
	assert.Equal(t, "1500.00", nov.Totals.LoanPayments)
	assert.Equal(t, "1954.25", nov.Totals.Committed)
	assert.Equal(t, 2, nov.Totals.Uncounted, "Streaming (no amount) and Family loan (no payment)")

	// The once bill only counts in its own month.
	dec := get("2026-12")
	assert.Empty(t, dec.Categories[0].Items)
	assert.Equal(t, "0.00", dec.Categories[0].Committed)
	assert.Equal(t, "1654.25", dec.Totals.Committed)

	rec := f.do(t, req{method: http.MethodGet, path: "/api/v1/budget/obligations?month=nov", as: owner})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/budget/obligations", as: owner})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Regexp(t, `^\d{4}-\d{2}$`, decode[obligationsJSON](t, rec).Month)

	// Another family sees none of it.
	other := f.member(t, f.household(t), auth.RoleOwner)
	rec = f.do(t, req{method: http.MethodGet, path: "/api/v1/budget/obligations?month=2026-11", as: other})
	require.Equal(t, http.StatusOK, rec.Code)
	o := decode[obligationsJSON](t, rec)
	assert.Empty(t, o.Categories)
	assert.Empty(t, o.Uncategorized.Items)
	assert.Equal(t, "0.00", o.Totals.Committed)
}
