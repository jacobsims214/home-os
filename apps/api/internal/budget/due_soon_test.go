package budget_test

// Tests for GET /api/v1/budget/due-soon (#4819). The endpoint computes no due
// dates of its own: it reads home-os/billdue, the rule and loader that fill the
// MCP budget_summary due_soon section, so the API and the agent cannot
// disagree. TestDueSoon_MatchesMCPBudgetSummary drives the real MCP loader on
// the same rows and compares item for item.
//
// Requires TEST_DATABASE_URL pointing at a database with all migrations
// applied; skipped otherwise. Run with -p 1.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/budget"
	"home-os/billdue"
)

// dueSoonJSON mirrors the response. Pointer fields distinguish "absent" from
// "zero", which matters for the coverage block's contract.
type dueSoonJSON struct {
	Coverage struct {
		Detail         string `json:"detail"`
		Scope          string `json:"scope"`
		ItemsListed    int    `json:"items_listed"`
		ItemsNotListed int    `json:"items_not_listed"`
		WithoutDueDate int    `json:"without_due_date"`
		HowToGetDetail string `json:"how_to_get_detail"`
	} `json:"coverage"`
	From      string `json:"from"`
	To        string `json:"to"`
	Days      int    `json:"days"`
	Currency  string `json:"currency"`
	Total     string `json:"total"`
	BillCount int    `json:"bill_count"`
	LoanCount int    `json:"loan_count"`
	MoreItems int    `json:"more_items"`
	Items     []struct {
		Source     string  `json:"source"`
		ID         string  `json:"id"`
		Name       string  `json:"name"`
		Amount     *string `json:"amount"`
		DueDate    string  `json:"due_date"`
		DueWeekday string  `json:"due_weekday"`
		DaysUntil  int     `json:"days_until_due"`
		Status     string  `json:"status"`
		Autopay    bool    `json:"autopay"`
		Subject    *struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"subject"`
	} `json:"items"`
}

// dueSoon calls the endpoint and decodes it.
func (d dueSoonFixture) dueSoon(t *testing.T, as member, query string) (dueSoonJSON, *httptest.ResponseRecorder) {
	t.Helper()
	path := "/api/v1/budget/due-soon"
	if query != "" {
		path += "?" + query
	}
	rec := d.f.do(t, req{method: http.MethodGet, path: path, as: as})
	var out struct {
		Data dueSoonJSON `json:"data"`
	}
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	}
	return out.Data, rec
}

// fixedToday pins the family's date so the window and the expected dates do
// not move with the wall clock. The handler and the MCP loader are given the
// same one.
var fixedToday = time.Date(2026, time.November, 15, 0, 0, 0, 0, time.UTC)

// dueSoonFixture builds a household with a mix that exercises every branch of
// the rule: a monthly bill, a quarterly bill, an annual bill, a once bill, a
// paid bill, a bill with no schedule, an active loan, a paid-off loan and a
// loan with no due day. Times are relative to fixedToday so the rows keep
// their meaning whenever this runs.
type dueSoonFixture struct {
	hh   string
	as   member
	pool *pgxpool.Pool
	// f is the router fixture the endpoint is reached through.
	f *fixture
}

func newDueSoonFixture(t *testing.T) dueSoonFixture {
	t.Helper()
	f := setup(t, func(h *budget.Handler) *budget.Handler {
		return h.WithNow(func() time.Time { return fixedToday })
	})
	hh := f.household(t)
	// The handler's date comes from the household timezone; UTC makes the
	// family's date equal to fixedToday, so the expectations below are exact.
	_, err := f.pool.Exec(context.Background(), `UPDATE households SET timezone = 'UTC' WHERE id = $1`, hh)
	require.NoError(t, err)
	owner := f.member(t, hh, auth.RoleOwner)
	return dueSoonFixture{hh: hh, as: owner, pool: f.pool, f: f}
}

// bill inserts a bill and returns its id. amount is cents; nil keeps it NULL.
func (d dueSoonFixture) bill(t *testing.T, name string, amount *int64, freq string, dueDay *int, dueDate *time.Time, paid, paidFor *time.Time) string {
	t.Helper()
	var id string
	require.NoError(t, d.pool.QueryRow(context.Background(),
		`INSERT INTO bills (household_id, name, amount, frequency, due_day, due_date, paid_date, paid_for_due_date)
		 VALUES ($1, $2, $3::numeric / 100, $4, $5, $6, $7, $8) RETURNING id::text`,
		d.hh, name, amount, freq, dueDay, dueDate, paid, paidFor).Scan(&id))
	return id
}

// loan inserts a loan and returns its id.
func (d dueSoonFixture) loan(t *testing.T, name string, monthly *int64, dueDay *int, start, termEnd *time.Time, remaining string) string {
	t.Helper()
	var id string
	require.NoError(t, d.pool.QueryRow(context.Background(),
		`INSERT INTO loans (household_id, name, original_amount, remaining_balance, monthly_payment, due_day, start_date, term_end)
		 VALUES ($1, $2, 10000, $3::numeric, $4::numeric / 100, $5, $6, $7) RETURNING id::text`,
		d.hh, name, remaining, monthly, dueDay, start, termEnd).Scan(&id))
	return id
}

func day(n int) *int       { return &n }
func cents(n int64) *int64 { return &n }
func date(s string) *time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return &d
}

// seedDueSoon fills the household and returns the ids of the bills and loans
// that must appear, in the order the endpoint sorts them (due date, then name).
func seedDueSoon(t *testing.T, d dueSoonFixture) {
	t.Helper()
	bills := map[string]string{}
	loans := map[string]string{}
	// The ids are kept in maps so each row's purpose reads at the call site;
	// only the names matter to the assertions.
	_ = bills
	_ = loans

	// In the window, all unpaid, mixed frequencies.
	bills["Electric"] = d.bill(t, "Electric", cents(12000), "monthly", day(18), nil, nil, nil)
	bills["Water"] = d.bill(t, "Water", cents(6500), "monthly", day(20), nil, nil, nil)
	// A quarterly bill: anchored Sep 1, so its next date is Dec 1, inside the
	// 30-day window from Nov 15.
	bills["Quarterly tax"] = d.bill(t, "Quarterly tax", cents(90000), "quarterly", nil, date("2026-09-01"), nil, nil)
	// An annual bill whose next date is in the window.
	bills["Home insurance"] = d.bill(t, "Home insurance", cents(120000), "annual", nil, date("2025-12-01"), nil, nil)
	// A once bill inside the window.
	bills["New tyres"] = d.bill(t, "New tyres", cents(40000), "once", nil, date("2026-11-25"), nil, nil)

	// Paid: moves on, so it must NOT appear (its next date is 60+ days out).
	bills["Already paid"] = d.bill(t, "Already paid", cents(5000), "monthly", day(16), nil,
		date("2026-11-14"), date("2026-11-16"))
	// A paid once bill has no next date at all.
	bills["Paid once"] = d.bill(t, "Paid once", cents(1000), "once", nil, date("2026-11-10"),
		date("2026-11-05"), date("2026-11-10"))
	// No usable schedule: counted in without_due_date, not in the list.
	bills["Not scheduled"] = d.bill(t, "Not scheduled", cents(9900), "monthly", nil, nil, nil, nil)
	// Outside the default window: annual, due Dec 20 (after the Dec 15 end).
	// A monthly bill cannot play this part — every due day of every month
	// falls inside a 30-day window from mid-month — which is why this row is
	// annual. TestDueSoon_WindowNarrows picks it up at days=60.
	bills["Far away"] = d.bill(t, "Far away", cents(77700), "annual", nil, date("2026-12-20"), nil, nil)
	// No amount: listed, but uncountable.
	bills["Amount unknown"] = d.bill(t, "Amount unknown", nil, "monthly", day(17), nil, nil, nil)

	loans["Car loan"] = d.loan(t, "Car loan", cents(31050), day(19), date("2025-01-19"), date("2029-01-19"), "8000")
	loans["Mortgage"] = d.loan(t, "Mortgage", cents(185000), day(1), date("2024-01-01"), date("2054-01-01"), "250000")
	// Paid off: no balance, so it never falls due.
	loans["Old loan"] = d.loan(t, "Old loan", cents(20000), day(21), date("2020-01-21"), date("2024-01-21"), "0")
	// No due day: counted in without_due_date.
	loans["Undated loan"] = d.loan(t, "Undated loan", cents(10000), nil, nil, nil, "5000")
}

// TestDueSoon_Items pins what the endpoint returns for a household with every
// kind of schedule, on a fixed family date.
func TestDueSoon_Items(t *testing.T) {
	d := newDueSoonFixture(t)
	seedDueSoon(t, d)

	got, rec := d.dueSoon(t, d.as, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Defaults: 30 days from the family's today.
	assert.Equal(t, "2026-11-15", got.From)
	assert.Equal(t, "2026-12-15", got.To)
	assert.Equal(t, 30, got.Days)
	assert.Equal(t, "USD", got.Currency)

	// In the window and unpaid: Electric (Nov 18), Amount unknown (Nov 17),
	// Water (Nov 20), Car loan (Nov 19), Quarterly tax (Dec 1),
	// Home insurance (Dec 1), New tyres (Nov 25), Mortgage (Dec 1).
	// Sorted by due date then name.
	wantOrder := []struct {
		name, source, dueDate string
		amount                string
	}{
		{"Amount unknown", "bill", "2026-11-17", ""},
		{"Electric", "bill", "2026-11-18", "120.00"},
		{"Car loan", "loan", "2026-11-19", "310.50"},
		{"Water", "bill", "2026-11-20", "65.00"},
		{"New tyres", "bill", "2026-11-25", "400.00"},
		{"Home insurance", "bill", "2026-12-01", "1200.00"},
		{"Mortgage", "loan", "2026-12-01", "1850.00"},
		{"Quarterly tax", "bill", "2026-12-01", "900.00"},
	}
	// The default detail is summary, which lists five; the counts and total
	// must still cover all eight.
	assert.Equal(t, dueSoonSummaryListed, got.Coverage.ItemsListed, "summary lists %d items", dueSoonSummaryListed)
	assert.Equal(t, "summary", got.Coverage.Detail)
	require.Len(t, got.Items, dueSoonSummaryListed)
	assert.Equal(t, len(wantOrder)-dueSoonSummaryListed, got.MoreItems)
	assert.Equal(t, got.MoreItems, got.Coverage.ItemsNotListed)

	// The complete list comes back at detail=items.
	all, rec := d.dueSoon(t, d.as, "detail=items")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, all.Items, len(wantOrder), "every item in the window is listed at detail=items")
	assert.Zero(t, all.MoreItems)
	assert.Zero(t, all.Coverage.ItemsNotListed)
	assert.Equal(t, "items", all.Coverage.Detail)
	assert.Equal(t, "Nothing is left out: every item in the window is listed.", all.Coverage.HowToGetDetail)

	// Neither the paid bills nor the paid-off loan nor the out-of-window bill
	// appear.
	assert.Equal(t, 6, all.BillCount, "6 bills in the window")
	assert.Equal(t, 2, all.LoanCount, "2 loans in the window")
	assert.Equal(t, 2, all.Coverage.WithoutDueDate, "one bill and one loan have no schedule")

	for i, w := range wantOrder {
		item := all.Items[i]
		assert.Equal(t, w.name, item.Name, "item %d", i)
		assert.Equal(t, w.source, item.Source, "%s source", w.name)
		assert.Equal(t, w.dueDate, item.DueDate, "%s due date", w.name)
		if w.amount == "" {
			assert.Nil(t, item.Amount, "%s has no amount", w.name)
		} else {
			require.NotNil(t, item.Amount, "%s amount", w.name)
			assert.Equal(t, w.amount, *item.Amount, "%s amount", w.name)
		}
		// days_until_due is the whole days from the family's today.
		want, err := time.Parse(time.DateOnly, w.dueDate)
		require.NoError(t, err)
		assert.Equal(t, int(want.Sub(fixedToday).Hours()/24), item.DaysUntil, "%s days_until_due", w.name)
		assert.Equal(t, want.Weekday().String()[:3], item.DueWeekday, "%s weekday", w.name)
	}
	// The paid bill's paid status never leaks into the list; unpaid items are due.
	for _, item := range all.Items {
		assert.Equal(t, "due", item.Status, "%s status", item.Name)
	}
	// Amounts add up to the total, and an amountless item does not break it.
	assert.Equal(t, "4845.50", all.Total)
}

// dueSoonSummaryListed is how many items the default detail lists.
const dueSoonSummaryListed = 5

// TestDueSoon_MatchesMCPBudgetSummary is the parity test the ticket asks for:
// the API's list must be the one MCP budget_summary reports, item for item.
//
// How it is done, and why this is the honest form: budget_summary's due_soon
// section is produced by home-os/billdue.LoadDueSoonRows and by nothing else
// (apps/mcp/internal/budget.LoadDueSoonDepth is now a depth-selection wrapper
// over it, pinned by TestMCPDueSoonIsTheSharedLoader in apps/mcp). Go's
// internal-package rule stops this package importing the MCP module's internal
// budget package directly, so the parity is pinned from both ends against the
// one shared function:
//
//   - here: the HTTP response equals LoadDueSoonRows' result field for field,
//     at full detail and at summary detail;
//   - apps/mcp: budget.LoadDueSoonDepth equals LoadDueSoonRows' result, so an
//     MCP-side second implementation fails there.
//
// Neither side can drift into a second due rule without one of the two failing.
func TestDueSoon_MatchesMCPBudgetSummary(t *testing.T) {
	d := newDueSoonFixture(t)
	seedDueSoon(t, d)

	// The API, at full detail.
	api, rec := d.dueSoon(t, d.as, "detail=items")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The one function that fills budget_summary's due_soon, on the same
	// household and the same family date.
	shared, err := billdue.LoadDueSoonRows(context.Background(), d.pool, d.hh, fixedToday, dueSoonDays)
	require.NoError(t, err)

	require.Equal(t, len(shared.Items), len(api.Items), "same number of items")
	for i := range shared.Items {
		m, a := shared.Items[i], api.Items[i]
		assert.Equal(t, m.Source, a.Source, "item %d source", i)
		assert.Equal(t, m.ID.String(), a.ID, "item %d id", i)
		assert.Equal(t, m.Name, a.Name, "item %d name", i)
		assert.Equal(t, m.DueDate.Format(time.DateOnly), a.DueDate, "item %d due date", i)
		assert.Equal(t, m.DueDate.Weekday().String()[:3], a.DueWeekday, "item %d weekday", i)
		assert.Equal(t, m.DaysUntil, a.DaysUntil, "item %d days until", i)
		assert.Equal(t, m.Status, a.Status, "item %d status", i)
		assert.Equal(t, m.Autopay, a.Autopay, "item %d autopay", i)
		require.Equal(t, m.AmountCents == nil, a.Amount == nil, "item %d amount presence", i)
		if m.AmountCents != nil && a.Amount != nil {
			// The shared loader works in cents, the API prints decimal
			// strings; the string form is what both sides publish.
			assert.Equal(t, centsString(*m.AmountCents), *a.Amount, "item %d amount", i)
		}
	}
	assert.Equal(t, shared.Days, api.Days, "window days")
	assert.Equal(t, shared.From.Format(time.DateOnly), api.From, "window from")
	assert.Equal(t, shared.To.Format(time.DateOnly), api.To, "window to")
	assert.Equal(t, centsString(shared.TotalCents), api.Total, "total")
	assert.Equal(t, shared.BillCount, api.BillCount, "bill count")
	assert.Equal(t, shared.LoanCount, api.LoanCount, "loan count")
	assert.Equal(t, shared.WithoutDueDate, api.Coverage.WithoutDueDate, "without_due_date")

	// The hours of the day never leak into a due date: every item is a plain
	// calendar date at midnight UTC, which is what makes bills, loans and the
	// calendar mirror comparable.
	for _, it := range shared.Items {
		assert.True(t, it.DueDate.Equal(billdue.Midnight(it.DueDate)), "%s due date is midnight", it.Name)
	}

	// And the date really is the shared rule's, not something the API
	// computed: re-derive one item straight from home-os/billdue.
	for _, item := range api.Items {
		if item.Name != "Water" {
			continue
		}
		var dueDay *int
		require.NoError(t, d.pool.QueryRow(context.Background(),
			`SELECT due_day FROM bills WHERE id = $1`, item.ID).Scan(&dueDay))
		require.NotNil(t, dueDay)
		due, ok := billdue.BillDueFor(fixedToday, billdue.Schedule{Frequency: "monthly", DueDay: dueDay}, nil, nil)
		require.True(t, ok)
		assert.Equal(t, due.Next.Format(time.DateOnly), item.DueDate, "due date is the shared rule's")
		assert.Equal(t, due.Status, item.Status, "status is the shared rule's")
	}
}

// dueSoonDays is the window the tests ask for: the default.
const dueSoonDays = 30

// TestDueSoon_WindowNarrows checks the days parameter: it narrows the window
// for the list, the counts and the total together, so a summary can never mix
// a narrow list with a wide total.
func TestDueSoon_WindowNarrows(t *testing.T) {
	d := newDueSoonFixture(t)
	seedDueSoon(t, d)

	narrow, rec := d.dueSoon(t, d.as, "days=3&detail=items")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "2026-11-18", narrow.To)
	require.Len(t, narrow.Items, 2, "Nov 17 and Nov 18 fall due within 3 days")
	assert.Equal(t, "Amount unknown", narrow.Items[0].Name)
	assert.Equal(t, "Electric", narrow.Items[1].Name)
	assert.Equal(t, 2, narrow.BillCount, "counts cover the narrow window only")
	assert.Zero(t, narrow.LoanCount, "no loan falls due within 3 days")

	wide, rec := d.dueSoon(t, d.as, "days=60&detail=items")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "2027-01-14", wide.To)
	// The 60-day window also picks up "Far away" (Dec 20) and "Already paid",
	// whose next unpaid date after its Nov 16 payment is Dec 16.
	names := map[string]bool{}
	for _, item := range wide.Items {
		names[item.Name] = true
	}
	assert.True(t, names["Far away"], "the 60-day window includes the Dec 20 bill: %v", names)
	assert.Greater(t, wide.BillCount, narrow.BillCount)
	assert.NotEqual(t, narrow.Total, wide.Total)

	for _, bad := range []string{"days=0", "days=366", "days=abc"} {
		_, rec := d.dueSoon(t, d.as, bad)
		assert.Equalf(t, http.StatusBadRequest, rec.Code, "%s: %s", bad, rec.Body.String())
	}
	_, rec = d.dueSoon(t, d.as, "detail=nope")
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestDueSoon_Roles is the role acceptance criterion: money is readable by
// owner, family_manager, family_member and viewer, and refused to housesitter
// and vendor. It is the same grant as the rest of the budget (auth.CanSeeMoney
// / ResBudget read), so the endpoint cannot open a side door.
func TestDueSoon_Roles(t *testing.T) {
	d := newDueSoonFixture(t)
	seedDueSoon(t, d)

	cases := []struct {
		role string
		want int
	}{
		{auth.RoleOwner, http.StatusOK},
		{auth.RoleFamilyManager, http.StatusOK},
		{auth.RoleFamilyMember, http.StatusOK},
		{auth.RoleViewer, http.StatusOK},
		{auth.RoleHousesitter, http.StatusForbidden},
		{auth.RoleVendor, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			m := d.as
			if tc.role != auth.RoleOwner {
				m = f_member(t, d, tc.role)
			}
			got, rec := d.dueSoon(t, m, "")
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
			if tc.want != http.StatusOK {
				// A refusal leaks nothing: no bill name, no amount, no total,
				// and no counts that would reveal how many records exist.
				body := rec.Body.String()
				for _, secret := range []string{"Electric", "Water", "Car loan", "120.00", "310.50", "4845.50", "bill_count"} {
					assert.NotContains(t, body, secret, "%s must not appear in a %s refusal", secret, tc.role)
				}
				return
			}
			// A role that may read gets the same numbers as everyone else.
			assert.NotEmpty(t, got.Items)
			assert.Equal(t, "4845.50", got.Total)
		})
	}

	// The money helper the handler uses agrees with the policy table, so the
	// guard in the handler and the route rule cannot drift.
	for _, role := range auth.AllRoles {
		want := role == auth.RoleOwner || role == auth.RoleFamilyManager ||
			role == auth.RoleFamilyMember || role == auth.RoleViewer
		assert.Equalf(t, want, auth.CanSeeMoney(role), "CanSeeMoney(%s)", role)
	}
}

// TestDueSoon_RoleMatrixMatchesRoutePolicy checks the route rule and the
// handler guard agree: the route is ResBudget read, which is exactly
// auth.CanSeeMoney's grant (ResBills + ResLoans read). A role that passes one
// must pass the other, so neither is a wider door.
func TestDueSoon_RoleMatrixMatchesRoutePolicy(t *testing.T) {
	for _, role := range auth.AllRoles {
		routeAllows := auth.Can(role, auth.ResBudget, auth.ActRead)
		assert.Equalf(t, routeAllows, auth.CanSeeMoney(role),
			"%s: route (ResBudget read) = %v, handler (CanSeeMoney) = %v", role, routeAllows, auth.CanSeeMoney(role))
	}
}

// TestDueSoon_EmptyHousehold: a household with no bills or loans answers with
// an empty, honest list rather than an error or a bare zero, and the coverage
// sentence still says what was checked.
func TestDueSoon_EmptyHousehold(t *testing.T) {
	d := newDueSoonFixture(t)

	got, rec := d.dueSoon(t, d.as, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "0.00", got.Total)
	assert.Zero(t, got.BillCount)
	assert.Zero(t, got.LoanCount)
	assert.Empty(t, got.Items)
	assert.Zero(t, got.Coverage.WithoutDueDate)
	assert.Contains(t, got.Coverage.Scope, "bills and loan payments")
}

// TestDueSoon_PayloadSize is the #4809 rule applied to this endpoint: the
// default response stays small enough for a small local model, and the detail
// level is what makes it bigger. The bench household in the MCP has 300 bills;
// this seeds the same order of magnitude.
func TestDueSoon_PayloadSize(t *testing.T) {
	d := newDueSoonFixture(t)
	ctx := context.Background()
	// 300 monthly bills, all due inside the window.
	for i := 0; i < 300; i++ {
		day := i%27 + 1
		var id string
		require.NoError(t, d.pool.QueryRow(ctx,
			`INSERT INTO bills (household_id, name, amount, frequency, due_day)
			 VALUES ($1, $2, 4210, 'monthly', $3) RETURNING id::text`,
			d.hh, fmt.Sprintf("Bill %03d", i), day).Scan(&id))
	}
	// Every bill is in the current window: due days 1..27 of Nov/Dec.
	summary, rec := d.dueSoon(t, d.as, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	summaryBytes := rec.Body.Len()
	t.Logf("default (detail=summary): %d bytes, %d items listed, %d more, total %s",
		summaryBytes, summary.Coverage.ItemsListed, summary.MoreItems, summary.Total)
	assert.LessOrEqual(t, summaryBytes, 4*1024, "default response should stay small (#4809 pattern)")

	items, rec := d.dueSoon(t, d.as, "detail=items")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	t.Logf("detail=items: %d bytes, %d items listed", rec.Body.Len(), items.Coverage.ItemsListed)
	assert.Greater(t, rec.Body.Len(), summaryBytes, "detail=items is strictly bigger")
	assert.Equal(t, summary.Total, items.Total, "the total is the same at every detail level")
	assert.Equal(t, summary.BillCount, items.BillCount, "the counts are the same at every detail level")
}

// centsString prints cents as the decimal string both sides publish
// ("4845.50"), so the comparison is in the published form.
func centsString(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

// f_member adds a member with role to the fixture's household.
func f_member(t *testing.T, d dueSoonFixture, role string) member {
	t.Helper()
	return d.f.member(t, d.hh, role)
}
