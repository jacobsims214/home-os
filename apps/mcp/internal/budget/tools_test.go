package budget

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

type envelope struct {
	OK      bool            `json:"ok"`
	Summary string          `json:"summary"`
	Data    *CategoryResult `json:"data"`
	Next    []string        `json:"next"`
	Error   *struct {
		Message string `json:"message"`
		Field   string `json:"field"`
		Fix     string `json:"fix"`
	} `json:"error"`
}

func call(t *testing.T, ctx context.Context, h toolHandler, args map[string]any) envelope {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = "budget_set_category"
	req.Params.Arguments = args
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("Go error: %v", err)
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok || len(res.Content) != 1 {
		t.Fatalf("want one text block, got %+v", res.Content)
	}
	var env envelope
	if err := json.Unmarshal([]byte(text.Text), &env); err != nil {
		t.Fatal(err)
	}
	if env.OK == res.IsError {
		t.Fatalf("ok=%v but isError=%v", env.OK, res.IsError)
	}
	return env
}

func memberCtx(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) context.Context {
	t.Helper()
	bg := context.Background()
	email := role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(bg, `INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`, email, role).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hh.HouseholdID, uid, role); err != nil {
		t.Fatal(err)
	}
	return server.ContextWithClaims(bg, &auth.Claims{
		Identity: auth.Identity{Subject: uid.String(), Email: email}, UserID: uid.String(),
		HouseholdID: hh.HouseholdID.String(), Role: role,
	})
}

// fixture is a family with budget categories, bills of every frequency and
// loans, plus a neighbour family whose rows must never show up.
type fixture struct {
	pool *pgxpool.Pool
	ctx  context.Context
	hh   testutil.Household
	cats map[string]uuid.UUID
}

func seed(t *testing.T) fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, other := testutil.SeedHousehold(t, pool)
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	f := fixture{pool: pool, ctx: ctx, hh: hh, cats: map[string]uuid.UUID{}}
	for name, amount := range map[string]any{"Housing": "1900", "Utilities": "150", "Kids": nil} {
		var id uuid.UUID
		if err := pool.QueryRow(bg, `INSERT INTO budget_categories (household_id, name, monthly_amount) VALUES ($1, $2, $3::numeric) RETURNING id`,
			hh.HouseholdID, name, amount).Scan(&id); err != nil {
			t.Fatal(err)
		}
		f.cats[name] = id
	}
	var otherCat uuid.UUID
	if err := pool.QueryRow(bg, `INSERT INTO budget_categories (household_id, name, monthly_amount) VALUES ($1, 'Housing', 5) RETURNING id`,
		other.HouseholdID).Scan(&otherCat); err != nil {
		t.Fatal(err)
	}
	h := hh.HouseholdID
	bill := `INSERT INTO bills (household_id, name, amount, frequency, due_day, due_date, category, budget_category_id, paid_date)
	         VALUES ($1, $2, $3::numeric, $4, $5, $6::date, $7, $8, $9::date)`
	exec(bill, h, "Electric", "142.50", "monthly", 18, nil, "Utilities", f.cats["Utilities"], nil)
	exec(bill, h, "Water", "90", "quarterly", nil, "2026-10-05", "Utilities", f.cats["Utilities"], nil)
	exec(bill, h, "Property tax", "1800", "annual", nil, "2027-04-01", "Taxes", f.cats["Housing"], nil)
	exec(bill, h, "Summer camp deposit", "400", "once", nil, "2026-11-20", nil, f.cats["Kids"], nil)
	exec(bill, h, "Netflix", "15.49", "monthly", 16, nil, "Streaming", nil, "2026-11-16")
	exec(bill, h, "Gym", nil, "monthly", 1, nil, nil, nil, nil)
	exec(bill, other.HouseholdID, "Neighbour rent", "999", "monthly", 1, nil, nil, otherCat, nil)

	loan := `INSERT INTO loans (household_id, name, original_amount, remaining_balance, monthly_payment, due_day, start_date, term_end, budget_category_id)
	         VALUES ($1, $2, 1000, $3::numeric, $4::numeric, $5, $6::date, $7::date, $8)`
	exec(loan, h, "Mortgage", "250000", "1850", 1, "2021-06-01", "2051-06-01", f.cats["Housing"])
	exec(loan, h, "Car", "9000", "410.25", 31, nil, nil, nil)
	exec(loan, h, "HELOC", "1000", nil, nil, nil, nil, f.cats["Housing"])
	exec(loan, h, "Paid off", "0", "300", 5, nil, nil, f.cats["Housing"])
	exec(loan, other.HouseholdID, "Neighbour loan", "5000", "777", 1, nil, nil, nil)
	return f
}

func group(t *testing.T, r *Report, name string) CategoryLines {
	t.Helper()
	if name == "" {
		return r.Uncategorized
	}
	for _, c := range r.Categories {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no category %s in %+v", name, r.Categories)
	return CategoryLines{}
}

func lineNames(g CategoryLines) string {
	var out []string
	for _, l := range g.Lines {
		out = append(out, l.Source+":"+l.Name)
	}
	return strings.Join(out, ", ")
}

func strp(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// TestLoad: "what do we owe each month?" is one Load. Bills are spread per
// month (quarterly /3, annual /12, once in its due month only), active
// loans add their payment, every line keeps its source, and lines are
// grouped by budget category with budgeted/committed/remaining.
func TestLoad(t *testing.T) {
	f := seed(t)
	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	r, err := Load(context.Background(), f.pool, f.hh.HouseholdID.String(), nov)
	if err != nil {
		t.Fatal(err)
	}
	if r.Month != "2026-11" || r.Currency != "USD" || len(r.Categories) != 3 {
		t.Fatalf("report = %+v", r)
	}
	if got := []string{r.Categories[0].Name, r.Categories[1].Name, r.Categories[2].Name}; strings.Join(got, ",") != "Housing,Kids,Utilities" {
		t.Errorf("category order = %v", got)
	}

	housing := group(t, r, "Housing")
	if lineNames(housing) != "loan:Mortgage, bill:Property tax, loan:HELOC" || housing.LineCount != 3 || housing.MoreLines != 0 {
		t.Errorf("Housing lines = %s", lineNames(housing))
	}
	if housing.Committed != "2000.00" || strp(housing.Budgeted) != "1900.00" || strp(housing.Remaining) != "-100.00" || !housing.OverBudget {
		t.Errorf("Housing = %+v", housing)
	}
	for _, l := range housing.Lines {
		switch l.Name {
		case "Property tax":
			if strp(l.Amount) != "1800.00" || strp(l.MonthlyAmount) != "150.00" || l.Frequency != "annual" || strp(l.DueDate) != "2027-04-01" || strp(l.BillCategory) != "Taxes" {
				t.Errorf("property tax line = %+v", l)
			}
		case "HELOC":
			if l.MonthlyAmount != nil || l.BillCategory != nil {
				t.Errorf("HELOC line = %+v, want not counted", l)
			}
		case "Mortgage":
			if strp(l.MonthlyAmount) != "1850.00" || l.DueDay == nil || *l.DueDay != 1 || l.Frequency != "monthly" {
				t.Errorf("mortgage line = %+v", l)
			}
		}
	}

	utilities := group(t, r, "Utilities")
	if utilities.Committed != "172.50" || strp(utilities.Remaining) != "-22.50" || !utilities.OverBudget || len(utilities.Lines) != 2 {
		t.Errorf("Utilities = %+v", utilities)
	}
	kids := group(t, r, "Kids")
	if kids.Committed != "400.00" || kids.Budgeted != nil || kids.Remaining != nil || kids.OverBudget {
		t.Errorf("Kids = %+v", kids)
	}
	none := group(t, r, "")
	if none.BudgetCategoryID != nil || none.Committed != "425.74" || lineNames(none) != "loan:Car, bill:Netflix, bill:Gym" {
		t.Errorf("uncategorized = %+v (%s)", none, lineNames(none))
	}

	want := Totals{Budgeted: "2050.00", Committed: "2998.24", Bills: "737.99", LoanPayments: "2260.25", BillCount: 6, LoanCount: 3, Uncounted: 2}
	if r.Totals != want {
		t.Errorf("totals = %+v, want %+v", r.Totals, want)
	}
	if got := r.Describe(); got != "Budgets: $2,050.00 a month across 2 budgeted categories, $2,172.50 committed against them; over budget: Housing by $100.00, Utilities by $22.50; 3 lines not in a budget category." {
		t.Errorf("describe = %q", got)
	}

	// December: the once bill is gone; the quarterly and annual bills still
	// count their monthly share.
	r, err = Load(context.Background(), f.pool, f.hh.HouseholdID.String(), nov.AddDate(0, 1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if k := group(t, r, "Kids"); k.Committed != "0.00" || len(k.Lines) != 0 {
		t.Errorf("December Kids = %+v", k)
	}
	if r.Totals.Committed != "2598.24" || r.Totals.BillCount != 5 {
		t.Errorf("December totals = %+v", r.Totals)
	}
}

// TestLoadDueSoon: each bill and loan once, at its next unpaid due date, in
// date order; paid bills move on, loans outside their term are left out.
func TestLoadDueSoon(t *testing.T) {
	f := seed(t)
	bg := context.Background()
	for _, sql := range []string{
		`INSERT INTO loans (household_id, name, original_amount, remaining_balance, monthly_payment, due_day, start_date)
		 VALUES ($1, 'Starts in January', 1, 1, 50, 5, '2027-01-01')`,
		`INSERT INTO loans (household_id, name, original_amount, remaining_balance, monthly_payment, due_day, start_date, term_end)
		 VALUES ($1, 'Ends before its next payment', 1, 1, 60, 28, '2020-01-01', '2026-11-25')`,
		`INSERT INTO bills (household_id, name, amount, frequency, due_date) VALUES ($1, 'Missed once', 10, 'once', '2026-11-01')`,
	} {
		if _, err := f.pool.Exec(bg, sql, f.hh.HouseholdID); err != nil {
			t.Fatal(err)
		}
	}
	today := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	ds, err := LoadDueSoon(bg, f.pool, f.hh.HouseholdID.String(), today, 30)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range ds.Items {
		got = append(got, it.DueDate+" "+it.Source+":"+it.Name+" "+strp(it.Amount))
	}
	want := []string{
		"2026-11-18 bill:Electric 142.50",
		"2026-11-20 bill:Summer camp deposit 400.00",
		"2026-11-30 loan:Car 410.25",
		"2026-12-01 bill:Gym <nil>",
		"2026-12-01 loan:Mortgage 1850.00",
		"2026-12-16 bill:Netflix 15.49",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("items:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if ds.Total != "2818.24" || ds.BillCount != 4 || ds.LoanCount != 2 || ds.NoDueDate != 1 || ds.From != "2026-11-16" || ds.To != "2026-12-16" {
		t.Errorf("due soon = %+v", ds)
	}
	if ds.Items[0].DueWeekday != "Wed" || ds.Items[0].DaysUntil != 2 || ds.Items[5].DaysUntil != 30 {
		t.Errorf("first/last = %+v / %+v", ds.Items[0], ds.Items[5])
	}
	if got := ds.Describe(); got != "Due in the next 30 days: $2,818.24 (4 bills, 2 loan payments), first Electric $142.50 on Wed Nov 18." {
		t.Errorf("describe = %q", got)
	}
}

// TestSetCategory: create, update by name in any case, notes kept/cleared,
// 0 removes the budget but keeps the category, idempotent, aliases.
func TestSetCategory(t *testing.T) {
	f := seed(t)
	_, _, h := NewSetCategoryTool(f.pool)

	env := call(t, f.ctx, h, map[string]any{"category": "  Groceries ", "monthly_amount": 600.0, "notes": "Costco + Kroger"})
	if !env.OK || !env.Data.Created || env.Data.Name != "Groceries" || strp(env.Data.MonthlyAmount) != "600.00" ||
		strp(env.Data.Notes) != "Costco + Kroger" || env.Data.Committed != "0.00" || strp(env.Data.Remaining) != "600.00" || env.Data.Currency != "USD" {
		t.Fatalf("create = %+v", env)
	}
	if !strings.HasPrefix(env.Summary, "Created budget category Groceries at $600.00 a month") || len(env.Next) != 2 {
		t.Errorf("create summary/next = %q %v", env.Summary, env.Next)
	}
	id := env.Data.ID

	// Same call again: same row, nothing new.
	env = call(t, f.ctx, h, map[string]any{"category": "groceries", "monthly_amount": "$600.00"})
	if !env.OK || env.Data.Created || env.Data.ID != id || strp(env.Data.Notes) != "Costco + Kroger" {
		t.Errorf("repeat = %+v", env.Data)
	}

	// Existing category in another case: keeps its spelling, reports this month.
	env = call(t, f.ctx, h, map[string]any{"name": "HOUSING", "amount": "2,100"})
	if !env.OK || env.Data.Created || env.Data.ID != f.cats["Housing"].String() || env.Data.Name != "Housing" ||
		strp(env.Data.MonthlyAmount) != "2100.00" || env.Data.LineCount < 2 {
		t.Fatalf("housing = %+v", env)
	}
	if !strings.HasPrefix(env.Summary, "Set the Housing budget to $2,100.00 a month; bills and loans in it commit $") {
		t.Errorf("housing summary = %q", env.Summary)
	}

	// 0 removes the budget, keeps the category and its notes; "" clears notes.
	env = call(t, f.ctx, h, map[string]any{"category": "Groceries", "monthly_amount": 0.0, "notes": ""})
	if !env.OK || env.Data.MonthlyAmount != nil || env.Data.Remaining != nil || env.Data.Notes != nil || env.Data.ID != id {
		t.Errorf("zero = %+v", env.Data)
	}
	if !strings.HasPrefix(env.Summary, "Removed the monthly budget of Groceries") {
		t.Errorf("zero summary = %q", env.Summary)
	}
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM budget_categories WHERE household_id = $1`, f.hh.HouseholdID).Scan(&n); err != nil || n != 4 {
		t.Errorf("categories = %d (%v), want 4", n, err)
	}

	for _, tc := range []struct {
		args  map[string]any
		field string
	}{
		{map[string]any{"monthly_amount": 5.0}, "category"},
		{map[string]any{"category": "Pets"}, "monthly_amount"},
		{map[string]any{"category": "Pets", "monthly_amount": -5.0}, "monthly_amount"},
		{map[string]any{"category": "Pets", "monthly_amount": "lots"}, "monthly_amount"},
		{map[string]any{"category": "Pets", "monthly_amount": 1e13}, "monthly_amount"},
		{map[string]any{"category": "Pets", "monthly_amount": 5.0, "notes": 3.0}, "notes"},
	} {
		env := call(t, f.ctx, h, tc.args)
		if env.OK || env.Error == nil || env.Error.Field != tc.field || env.Error.Fix == "" {
			t.Errorf("%v: want error on %s, got %+v", tc.args, tc.field, env)
		}
	}
}

// TestSetCategoryRoles: only the family roles change budgets.
func TestSetCategoryRoles(t *testing.T) {
	f := seed(t)
	_, _, h := NewSetCategoryTool(f.pool)
	for _, role := range []string{"viewer", "housesitter", "vendor"} {
		env := call(t, memberCtx(t, f.pool, f.hh, role), h, map[string]any{"category": "Pets", "monthly_amount": 50.0})
		if env.OK || !strings.Contains(env.Error.Message, "The "+role+" role can't change the budget") {
			t.Errorf("%s = %+v", role, env)
		}
	}
	for _, role := range []string{"family_manager", "family_member"} {
		if env := call(t, memberCtx(t, f.pool, f.hh, role), h, map[string]any{"category": "Pets", "monthly_amount": 50.0}); !env.OK {
			t.Errorf("%s refused: %+v", role, env.Error)
		}
	}
	if env := call(t, context.Background(), h, map[string]any{"category": "Pets", "monthly_amount": 50.0}); env.OK || !strings.Contains(env.Error.Message, "not signed in") {
		t.Errorf("no claims = %+v", env)
	}
}

func TestParseCentsAndMoney(t *testing.T) {
	for raw, want := range map[any]int64{600.0: 60000, "$1,850.00": 185000, "600 USD": 60000, 33.333: 3333, "0": 0} {
		got, ok, err := parseCents(raw)
		if err != nil || !ok || got != want {
			t.Errorf("parseCents(%v) = %d %v %v, want %d", raw, got, ok, err, want)
		}
	}
	if _, ok, err := parseCents(" "); ok || err != nil {
		t.Errorf("blank = %v %v", ok, err)
	}
	for c, want := range map[int64]string{0: "$0.00", 5: "$0.05", 123456789: "$1,234,567.89", -12000: "-$120.00"} {
		if got := Money(c); got != want {
			t.Errorf("Money(%d) = %s, want %s", c, got, want)
		}
	}
	if FormatCents(-10050) != "-100.50" || FormatCents(7) != "0.07" {
		t.Errorf("FormatCents")
	}
}

func TestLargestLines(t *testing.T) {
	amt := func(s string) *string { return &s }
	var lines []Line
	for i := 0; i < 12; i++ {
		lines = append(lines, Line{Name: fmt.Sprintf("L%02d", i), MonthlyAmount: amt(fmt.Sprintf("%d.00", i))})
	}
	lines = append(lines, Line{Name: "Unknown"})
	got, more := largestLines(lines, MaxLines)
	if len(got) != MaxLines || more != 5 || got[0].Name != "L11" || got[MaxLines-1].Name != "L04" {
		t.Errorf("largestLines = %d more %d, first %s last %s", len(got), more, got[0].Name, got[len(got)-1].Name)
	}
}

func TestNextMonthlyDue(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	for _, tc := range []struct {
		today string
		day   int
		want  string
	}{
		{"2026-11-16", 31, "2026-11-30"},
		{"2026-11-16", 16, "2026-11-16"},
		{"2026-11-16", 1, "2026-12-01"},
		{"2027-01-31", 30, "2027-02-28"},
	} {
		if got := NextMonthlyDue(d(tc.today), tc.day).Format(time.DateOnly); got != tc.want {
			t.Errorf("NextMonthlyDue(%s, %d) = %s, want %s", tc.today, tc.day, got, tc.want)
		}
	}
}
