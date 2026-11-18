package finance

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/bill"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// wire is the JSON budget_summary returns.
type wire struct {
	OK      bool           `json:"ok"`
	Summary string         `json:"summary"`
	Data    *budgetSummary `json:"data"`
	Next    []string       `json:"next"`
	Error   *struct {
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Field   string         `json:"field"`
		Example map[string]any `json:"example"`
	} `json:"error"`
}

// resolvedSchema is the outputSchema budget_summary advertises.
func resolvedSchema(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	_, tool, _ := NewBudgetSummaryTool(nil)
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.OutputSchema) == 0 {
		t.Fatal("budget_summary declares no outputSchema")
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw.OutputSchema, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve outputSchema: %v", err)
	}
	return rs
}

// call runs the handler and checks the respond invariants: one text block
// equal to structuredContent, valid against the advertised outputSchema
// (errors included), and ok == !isError.
func call(t *testing.T, ctx context.Context, h toolHandler, args map[string]any) (*mcp.CallToolResult, wire) {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = "budget_summary"
	req.Params.Arguments = args
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("budget_summary returned a Go error: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("want 1 content block, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	sc, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if string(sc) != text.Text {
		t.Fatalf("text differs from structuredContent:\n%s\n%s", text.Text, sc)
	}
	var instance map[string]any
	if err := json.Unmarshal(sc, &instance); err != nil {
		t.Fatal(err)
	}
	if err := resolvedSchema(t).Validate(instance); err != nil {
		t.Fatalf("result does not match outputSchema: %v\n%s", err, sc)
	}
	var w wire
	if err := json.Unmarshal(sc, &w); err != nil {
		t.Fatal(err)
	}
	if w.OK == res.IsError {
		t.Fatalf("ok=%v but isError=%v", w.OK, res.IsError)
	}
	return res, w
}

func TestBudgetSummaryDefinition(t *testing.T) {
	name, tool, _ := NewBudgetSummaryTool(nil)
	if name != "budget_summary" || tool.Name != "budget_summary" {
		t.Fatalf("name = %q / %q", name, tool.Name)
	}
	if len(tool.Description) > 200 {
		t.Errorf("description is %d chars, max 200", len(tool.Description))
	}
	a := tool.Annotations
	for hint, got := range map[string]*bool{
		"readOnlyHint": a.ReadOnlyHint, "destructiveHint": a.DestructiveHint,
		"idempotentHint": a.IdempotentHint, "openWorldHint": a.OpenWorldHint,
	} {
		if got == nil {
			t.Errorf("%s is not set explicitly", hint)
		}
	}
	if a.Title != "Budget summary" || !*a.ReadOnlyHint || *a.DestructiveHint || !*a.IdempotentHint || *a.OpenWorldHint {
		t.Errorf("annotations = %+v, want class R", a)
	}
	if len(tool.InputSchema.Required) != 0 {
		t.Errorf("required = %v, want none", tool.InputSchema.Required)
	}
	wantParams := map[string]bool{"month": true, "sections": true, "detail": true, "category": true, "due_within_days": true}
	for p := range tool.InputSchema.Properties {
		if !wantParams[p] {
			t.Errorf("unexpected param %q", p)
		}
	}
	for p := range wantParams {
		if _, ok := tool.InputSchema.Properties[p]; !ok {
			t.Errorf("missing param %q", p)
		}
	}
	// #4809: the default answer is the small one, and the description says so
	// rather than promising everything.
	if !strings.Contains(tool.Description, "summary by default") {
		t.Errorf("description does not say it is a summary by default: %q", tool.Description)
	}
	db, _ := json.Marshal(tool.InputSchema.Properties["detail"])
	if !strings.Contains(string(db), `"enum":["summary","lines","full"]`) {
		t.Errorf("detail schema = %s, want the detail enum", db)
	}
	b, _ := json.Marshal(tool.InputSchema.Properties["sections"])
	if !strings.Contains(string(b), `"enum":["obligations","budget","due_soon","net_worth"]`) {
		t.Errorf("sections schema = %s, want the catalog enum", b)
	}
	for p, prop := range tool.InputSchema.Properties {
		if d, _ := prop.(map[string]any)["description"].(string); len(d) > 80 {
			t.Errorf("param %s description is %d chars, max 80", p, len(d))
		}
	}
}

func TestParseSections(t *testing.T) {
	tests := []struct {
		name    string
		raw     any
		want    []string
		wantErr bool
	}{
		{"nil means the default", nil, defaultSections, false},
		{"empty list means the default", []any{}, defaultSections, false},
		{"explicit all", []any{"obligations", "budget", "due_soon", "net_worth"}, allSections, false},
		{"list, catalog order, deduped", []any{"net_worth", "obligations", "net_worth"}, []string{"obligations", "net_worth"}, false},
		{"string", "obligations", []string{"obligations"}, false},
		{"comma string, any case, dash", "Net-Worth, OBLIGATIONS", []string{"obligations", "net_worth"}, false},
		{"space form", []any{"due soon"}, []string{"due_soon"}, false},
		{"unknown", []any{"transactions"}, nil, true},
		{"non-string item", []any{1.0}, nil, true},
		{"wrong type", 3.0, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSections(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMoney(t *testing.T) {
	for in, want := range map[float64]string{
		0: "$0.00", 15.5: "$15.50", 999.999: "$1,000.00", 1234.5: "$1,234.50",
		412000: "$412,000.00", 1234567.891: "$1,234,567.89", -2500: "-$2,500.00",
	} {
		if got := money(in); got != want {
			t.Errorf("money(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestBudgetSummary_Errors(t *testing.T) {
	_, _, h := NewBudgetSummaryTool(nil)
	res, w := call(t, context.Background(), h, nil)
	if !res.IsError || !strings.Contains(w.Summary, "not signed in") {
		t.Errorf("unauthenticated = %+v", w)
	}

	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	_, _, h = NewBudgetSummaryTool(pool)
	for _, tc := range []struct {
		args  map[string]any
		field string
	}{
		{map[string]any{"month": "November"}, "month"},
		{map[string]any{"month": "2026-11-05"}, "month"},
		{map[string]any{"month": 202611.0}, "month"},
		{map[string]any{"sections": []any{"spending"}}, "sections"},
	} {
		res, w := call(t, ctx, h, tc.args)
		if !res.IsError || w.Error == nil || w.Error.Field != tc.field || w.Error.Fix == "" || w.Error.Example == nil {
			t.Errorf("%v: want isError on %s with fix and example, got %+v", tc.args, tc.field, w)
		}
	}
}

func TestBudgetSummary_Empty(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	_, _, h := NewBudgetSummaryTool(pool)

	// #4809: no arguments means the small answer, budget and due_soon only.
	_, w := call(t, ctx, h, nil)
	d := w.Data
	if d == nil || d.Budget == nil || d.DueSoon == nil {
		t.Fatalf("data = %+v", d)
	}
	if d.Obligations != nil || d.NetWorth != nil {
		t.Errorf("default includes obligations/net_worth: %+v", d)
	}
	if want := time.Now().UTC().Format("2006-01"); d.Month != want {
		// Household timezone is unset (UTC) in the seed.
		t.Errorf("month = %s, want %s", d.Month, want)
	}
	if !reflect.DeepEqual(d.Sections, defaultSections) {
		t.Errorf("sections = %v, want %v", d.Sections, defaultSections)
	}
	if len(d.Budget.Categories) != 0 || d.Budget.Totals.Committed != "0.00" || d.Budget.Currency != "USD" ||
		len(d.DueSoon.Items) != 0 || d.DueSoon.Total != "0.00" || d.DueSoon.Days != 30 {
		t.Errorf("budget/due_soon = %+v / %+v", d.Budget, d.DueSoon)
	}
	if !strings.Contains(w.Summary, "no bills or loan payments recorded yet") ||
		!strings.Contains(w.Summary, "No budget categories set yet.") || !strings.Contains(w.Summary, "Nothing falls due in the next 30 days.") {
		t.Errorf("summary = %q", w.Summary)
	}
	if strings.Contains(w.Summary, "net worth") {
		t.Errorf("summary mentions net worth, which was not asked for: %q", w.Summary)
	}
	if len(w.Next) == 0 || !strings.Contains(w.Next[0], "bill_create") {
		t.Errorf("next = %v", w.Next)
	}
	// The coverage block names what the default left out, so a small model
	// cannot read the default as the whole picture.
	c := d.Coverage
	if c.Detail != detailSummary || !reflect.DeepEqual(c.Sections, defaultSections) ||
		!reflect.DeepEqual(c.NotIncluded, []string{sectionObligations, sectionNetWorth}) {
		t.Errorf("coverage = %+v", c)
	}
	if !strings.Contains(c.HowToGetDetail, `sections=["obligations","net_worth"]`) {
		t.Errorf("how_to_get_detail = %q", c.HowToGetDetail)
	}
	if c.DueWindow == "" || c.Month != d.Month {
		t.Errorf("coverage window/month = %+v", c)
	}

	// Every section still reachable on request.
	_, w = call(t, ctx, h, map[string]any{"sections": allSections})
	d = w.Data
	if d == nil || d.Obligations == nil || d.NetWorth == nil || d.Budget == nil || d.DueSoon == nil {
		t.Fatalf("all sections: data = %+v", d)
	}
	if d.Obligations.BillsByCategory == nil || len(d.Obligations.BillsByCategory) != 0 || d.Obligations.MonthlyTotal != 0 {
		t.Errorf("obligations = %+v", d.Obligations)
	}
	if !strings.Contains(w.Summary, "Estimated net worth $0.00") {
		t.Errorf("all-sections summary = %q", w.Summary)
	}
	if c := d.Coverage; len(c.NotIncluded) != 0 {
		t.Errorf("all sections: not_included = %v, want none", c.NotIncluded)
	}
}

func insertBill(t *testing.T, pool *pgxpool.Pool, hh uuid.UUID, name string, amount float64, category *string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO bills (household_id, name, amount, due_day, category) VALUES ($1, $2, $3, 15, $4)`,
		hh, name, amount, category); err != nil {
		t.Fatalf("insert bill %s: %v", name, err)
	}
}

func ptr(s string) *string { return &s }

// TestBudgetSummary_Obligations checks that the bill numbers are exactly
// bill.SummarizeByCategory's and that loan payments are added for loans
// active in the requested month.
func TestBudgetSummary_Obligations(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, otherHH := testutil.SeedHousehold(t, pool)

	insertBill(t, pool, hh.HouseholdID, "Electric", 142.5, ptr("Utilities"))
	insertBill(t, pool, hh.HouseholdID, "Water", 40.1, ptr("Utilities"))
	insertBill(t, pool, hh.HouseholdID, "Netflix", 15.49, ptr("Streaming"))
	insertBill(t, pool, hh.HouseholdID, "Gym", 30, nil)
	insertBill(t, pool, otherHH.HouseholdID, "Other family", 999, ptr("Utilities"))

	loan := func(name string, payment *float64, balance float64, start, end *string) {
		t.Helper()
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO loans (household_id, name, original_amount, remaining_balance, monthly_payment, start_date, term_end)
			 VALUES ($1, $2, $3, $3, $4, $5::date, $6::date)`,
			hh.HouseholdID, name, balance, payment, start, end); err != nil {
			t.Fatalf("insert loan %s: %v", name, err)
		}
	}
	f := func(v float64) *float64 { return &v }
	loan("Mortgage", f(1850), 250000, ptr("2020-01-01"), ptr("2050-01-01"))
	loan("Car", f(410.25), 9000, nil, nil)
	loan("Ended before Nov 2026", f(100), 50, ptr("2020-01-01"), ptr("2026-10-31"))
	loan("Starts Dec 2026", f(200), 5000, ptr("2026-12-01"), nil)
	loan("No payment set", nil, 1000, nil, nil)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO loans (household_id, name, original_amount, remaining_balance, monthly_payment) VALUES ($1, 'Paid off', 5000, 0, 300)`,
		hh.HouseholdID); err != nil {
		t.Fatal(err)
	}

	cats, grand, err := bill.SummarizeByCategory(context.Background(), pool, hh.HouseholdID.String(), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}

	_, _, h := NewBudgetSummaryTool(pool)
	_, w := call(t, ctx, h, map[string]any{"month": "2026-11", "sections": []any{"obligations"}})
	d := w.Data
	if d.Month != "2026-11" || d.NetWorth != nil || d.Budget != nil || d.DueSoon != nil || !reflect.DeepEqual(d.Sections, []string{"obligations"}) {
		t.Fatalf("data = %+v", d)
	}
	ob := d.Obligations
	if !reflect.DeepEqual(ob.BillsByCategory, cats) || ob.BillsTotal != cents(grand) {
		t.Errorf("bills = %+v / %v, want SummarizeByCategory %+v / %v", ob.BillsByCategory, ob.BillsTotal, cats, grand)
	}
	if ob.BillsByCategory[0].Category != "Utilities" || ob.BillsByCategory[0].Total != 182.6 || ob.BillCount != 4 || ob.BillsTotal != 228.09 {
		t.Errorf("bill totals = %+v", ob)
	}
	if ob.LoanCount != 2 || ob.LoanPaymentsTotal != 2260.25 || ob.LoansWithoutPayment != 1 || ob.MonthlyTotal != 2488.34 {
		t.Errorf("loan totals = %+v, want Mortgage + Car only", ob)
	}
	if w.Summary != "November 2026: $2,488.34 a month in bills and loan payments (4 bills, 2 loans)." {
		t.Errorf("summary = %q", w.Summary)
	}
	if len(w.Next) != 2 || !strings.Contains(w.Next[0], `bill_find category="Utilities"`) || !strings.Contains(w.Next[1], "loan_update") {
		t.Errorf("next = %v", w.Next)
	}

	// December: the ended loan is still out, the December loan is in.
	_, w = call(t, ctx, h, map[string]any{"month": "2026-12", "sections": "obligations"})
	if ob := w.Data.Obligations; ob.LoanCount != 3 || ob.LoanPaymentsTotal != 2460.25 {
		t.Errorf("December loans = %+v", ob)
	}
}

// TestBudgetSummary_NetWorth checks values (asset current_value, else
// purchase_price) and the loan split by typed subject (domain model v2; the
// legacy entity_type/entity_id pair is no longer read).
func TestBudgetSummary_NetWorth(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, otherHH := testutil.SeedHousehold(t, pool)
	bg := context.Background()
	id := func(sql string, args ...any) uuid.UUID {
		t.Helper()
		var v uuid.UUID
		if err := pool.QueryRow(bg, sql, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return v
	}

	house := id(`INSERT INTO properties (household_id, name, current_value) VALUES ($1, 'House', 400000) RETURNING id`, hh.HouseholdID)
	id(`INSERT INTO properties (household_id, name, current_value) VALUES ($1, 'Theirs', 1000000) RETURNING id`, otherHH.HouseholdID)
	hvac := id(`INSERT INTO assets (household_id, property_id, name, current_value, purchase_price) VALUES ($1, $2, 'HVAC', 6000, 9000) RETURNING id`, hh.HouseholdID, house)
	id(`INSERT INTO assets (household_id, name, purchase_price) VALUES ($1, 'Laptop', 1500) RETURNING id`, hh.HouseholdID)
	id(`INSERT INTO assets (household_id, name) VALUES ($1, 'Rug') RETURNING id`, hh.HouseholdID)
	car := id(`INSERT INTO vehicles (household_id, make, current_value) VALUES ($1, 'Subaru', 22000) RETURNING id`, hh.HouseholdID)
	truck := id(`INSERT INTO vehicles (household_id, make, current_value) VALUES ($1, 'Ford', 18000) RETURNING id`, hh.HouseholdID)

	loans := []struct {
		name    string
		cols    string
		balance float64
		args    []any
	}{
		{"Mortgage (typed)", "property_id", 250000, []any{house}},
		{"Car loan (typed)", "vehicle_id", 9000, []any{car}},
		{"Truck loan (typed)", "vehicle_id", 7000, []any{truck}},
		{"HELOC (typed)", "property_id", 20000, []any{house}},
		{"HVAC financing (asset in the house)", "asset_id", 3000, []any{hvac}},
		{"Student loan (family)", "", 12000, nil},
	}
	for _, l := range loans {
		cols, vals := "", ""
		args := []any{hh.HouseholdID, l.name, l.balance}
		if l.cols != "" {
			cols = ", " + l.cols
			for i := range l.args {
				vals += fmt.Sprintf(", $%d", 4+i)
			}
			args = append(args, l.args...)
		}
		if _, err := pool.Exec(bg,
			`INSERT INTO loans (household_id, name, original_amount, remaining_balance`+cols+`) VALUES ($1, $2, $3, $3`+vals+`)`,
			args...); err != nil {
			t.Fatalf("insert %s: %v", l.name, err)
		}
	}
	// The derive trigger gives the asset loan the house's property_id; it must
	// still not count as mortgage debt.
	var derived *uuid.UUID
	if err := pool.QueryRow(bg, `SELECT property_id FROM loans WHERE asset_id = $1`, hvac).Scan(&derived); err != nil || derived == nil || *derived != house {
		t.Fatalf("asset loan property_id = %v (%v), want derived house", derived, err)
	}

	_, _, h := NewBudgetSummaryTool(pool)
	_, w := call(t, ctx, h, map[string]any{"sections": []any{"net_worth"}})
	nw := w.Data.NetWorth
	if w.Data.Obligations != nil {
		t.Errorf("obligations present without being asked for")
	}
	want := netWorth{
		PropertyValue: 400000, AssetValue: 7500, VehicleValue: 40000, TotalValue: 447500,
		MortgageDebt: 270000, VehicleLoans: 16000, OtherLoans: 15000, TotalLiabilities: 301000,
		EstimatedNetWorth: 146500,
	}
	if nw == nil || *nw != want {
		t.Fatalf("net worth = %+v\nwant %+v", nw, want)
	}
	if !strings.Contains(w.Summary, "Estimated net worth $146,500.00 ($447,500.00 owned, $301,000.00 owed).") {
		t.Errorf("summary = %q", w.Summary)
	}
}

// TestBudgetSummary_DueSoonOnly: the default month and "today" both come
// from the family's timezone, and only the asked-for section is built.
func TestBudgetSummary_DueSoonOnly(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	if _, err := pool.Exec(context.Background(), `UPDATE households SET timezone = 'Pacific/Kiritimati' WHERE id = $1`, hh.HouseholdID); err != nil {
		t.Fatal(err)
	}
	_, _, h := NewBudgetSummaryTool(pool)
	_, w := call(t, ctx, h, map[string]any{"sections": []any{"due_soon"}})
	d := w.Data
	loc, _ := time.LoadLocation("Pacific/Kiritimati")
	now := time.Now().In(loc)
	if want := now.Format("2006-01"); d.Month != want {
		t.Errorf("month = %s, want %s (family timezone)", d.Month, want)
	}
	if !reflect.DeepEqual(d.Sections, []string{"due_soon"}) || d.Obligations != nil || d.NetWorth != nil || d.Budget != nil || d.DueSoon == nil {
		t.Fatalf("data = %+v", d)
	}
	if want := now.Format("2006-01-02"); d.DueSoon.From != want {
		t.Errorf("due_soon.from = %s, want %s", d.DueSoon.From, want)
	}
	if !strings.Contains(w.Summary, "Nothing falls due in the next 30 days.") {
		t.Errorf("summary = %q", w.Summary)
	}
}

// TestBudgetSummary_WhatDoWeOweEachMonth: one call answers "what do we owe
// each month?": the monthly total from bills + loans, grouped by budget
// category with every line's source, and the budget section's committed
// total is exactly the obligations section's monthly total.
func TestBudgetSummary_WhatDoWeOweEachMonth(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	bg := context.Background()
	var housing uuid.UUID
	if err := pool.QueryRow(bg, `INSERT INTO budget_categories (household_id, name, monthly_amount) VALUES ($1, 'Housing', 2000) RETURNING id`,
		hh.HouseholdID).Scan(&housing); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO bills (household_id, name, amount, frequency, due_date, category, budget_category_id) VALUES ($1, 'Home insurance', 1200, 'annual', '2027-03-01', 'Insurance', $2)`,
		`INSERT INTO bills (household_id, name, amount, due_day, category) VALUES ($1, 'Electric', 142.5, 18, 'Utilities')`,
		`INSERT INTO loans (household_id, name, original_amount, remaining_balance, monthly_payment, due_day, budget_category_id) VALUES ($1, 'Mortgage', 300000, 250000, 1850, 1, $2)`,
	} {
		args := []any{hh.HouseholdID}
		if strings.Contains(sql, "$2") {
			args = append(args, housing)
		}
		if _, err := pool.Exec(bg, sql, args...); err != nil {
			t.Fatal(err)
		}
	}

	_, _, h := NewBudgetSummaryTool(pool)
	_, w := call(t, ctx, h, map[string]any{"month": "2026-11", "sections": []any{"obligations", "budget"}, "detail": "lines"})
	d := w.Data
	if d.Budget == nil || d.Obligations == nil {
		t.Fatalf("data = %+v", d)
	}
	b := d.Budget
	if b.Totals.Committed != "2092.50" || b.Totals.Bills != "242.50" || b.Totals.LoanPayments != "1850.00" ||
		fmt.Sprintf("%.2f", d.Obligations.MonthlyTotal) != b.Totals.Committed {
		t.Errorf("totals = %+v, obligations monthly_total = %v", b.Totals, d.Obligations.MonthlyTotal)
	}
	if len(b.Categories) != 1 || b.Categories[0].Committed != "1950.00" || *b.Categories[0].Remaining != "50.00" || len(b.Categories[0].Lines) != 2 {
		t.Errorf("Housing = %+v", b.Categories)
	}
	for _, l := range b.Categories[0].Lines {
		if (l.Name == "Mortgage") != (l.Source == "loan") {
			t.Errorf("line source = %+v", l)
		}
	}
	if len(b.Uncategorized.Lines) != 1 || b.Uncategorized.Lines[0].Source != "bill" || b.Uncategorized.Lines[0].Name != "Electric" {
		t.Errorf("uncategorized = %+v", b.Uncategorized)
	}
	if !strings.HasPrefix(w.Summary, "November 2026: $2,092.50 a month in bills and loan payments (2 bills, 1 loan). Budgets: $2,000.00 a month across 1 budgeted category, $1,950.00 committed against them; 1 line not in a budget category.") {
		t.Errorf("summary = %q", w.Summary)
	}
	if len(w.Next) == 0 || !strings.Contains(w.Next[0], "budget_set_category") {
		t.Errorf("next = %v", w.Next)
	}

	// Only the budget section still says what the month costs in total.
	_, w = call(t, ctx, h, map[string]any{"month": "2026-11", "sections": []any{"budget"}})
	if !strings.HasPrefix(w.Summary, "November 2026: $2,092.50 a month in bills and loan payments (2 bills, 1 loan). Budgets:") {
		t.Errorf("budget-only summary = %q", w.Summary)
	}
}

// TestBudgetSummary_Roles: money is hidden from housesitter and vendor,
// viewers read it; the role is re-read from memberships.
func TestBudgetSummary_Roles(t *testing.T) {
	pool := testutil.NewPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	_, _, h := NewBudgetSummaryTool(pool)
	member := func(role string) context.Context {
		bg := context.Background()
		email := role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
		var uid uuid.UUID
		if err := pool.QueryRow(bg, `INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`, email, role).Scan(&uid); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(bg, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hh.HouseholdID, uid, role); err != nil {
			t.Fatal(err)
		}
		// The claims say owner; memberships decide.
		return server.ContextWithClaims(bg, &auth.Claims{
			Identity: auth.Identity{Subject: uid.String(), Email: email}, UserID: uid.String(),
			HouseholdID: hh.HouseholdID.String(), Role: "owner",
		})
	}
	for _, role := range []string{"family_member", "viewer"} {
		if res, w := call(t, member(role), h, nil); res.IsError {
			t.Errorf("%s refused: %+v", role, w.Error)
		}
	}
	for _, role := range []string{"housesitter", "vendor"} {
		res, w := call(t, member(role), h, nil)
		if !res.IsError || !strings.Contains(w.Error.Message, "The "+role+" role can't see the budget") {
			t.Errorf("%s = %+v", role, w)
		}
	}
}

// TestBudgetSummary_CoverageStatesScope is the #4809 anti-regression test for
// the war game's worst failure: an agent confidently answering "nothing is
// due" from an answer that was never the whole picture. Every result must say
// what it covers, what it left out, and how to ask for the rest.
func TestBudgetSummary_CoverageStatesScope(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	bg := context.Background()
	var housing uuid.UUID
	if err := pool.QueryRow(bg, `INSERT INTO budget_categories (household_id, name, monthly_amount) VALUES ($1, 'Housing', 2000) RETURNING id`,
		hh.HouseholdID).Scan(&housing); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO bills (household_id, name, amount, due_day, category, budget_category_id) VALUES ($1, 'Electric', 142.5, 18, 'Utilities', $2)`,
		`INSERT INTO bills (household_id, name, amount, due_day, category, budget_category_id) VALUES ($1, 'Water', 40, 20, 'Utilities', $2)`,
		`INSERT INTO bills (household_id, name, amount, due_day, category) VALUES ($1, 'Gym', 30, 25, 'Health')`,
		`INSERT INTO loans (household_id, name, original_amount, remaining_balance, monthly_payment, due_day, budget_category_id) VALUES ($1, 'Mortgage', 300000, 250000, 1850, 1, $2)`,
	} {
		args := []any{hh.HouseholdID}
		if strings.Contains(sql, "$2") {
			args = append(args, housing)
		}
		if _, err := pool.Exec(bg, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	_, _, h := NewBudgetSummaryTool(pool)

	// The default lists no lines, says how many it left out, and names the
	// argument that lists them.
	_, w := call(t, ctx, h, nil)
	c := w.Data.Coverage
	if c.Detail != detailSummary || c.LinesNotListed == nil || *c.LinesNotListed != 4 || c.LinesListed == nil || *c.LinesListed != 0 {
		t.Errorf("default coverage = %+v, want 0 listed and 4 not listed", c)
	}
	if !strings.Contains(c.HowToGetDetail, `detail="lines"`) || !strings.Contains(c.HowToGetDetail, `detail="full"`) {
		t.Errorf("how_to_get_detail does not name detail: %q", c.HowToGetDetail)
	}
	// The summary must not read as "nothing is due": the total covers all.
	if w.Data.DueSoon == nil || w.Data.DueSoon.Total == "0.00" {
		t.Errorf("due_soon lost its total: %+v", w.Data.DueSoon)
	}
	if !strings.Contains(c.DueWindow, " to ") {
		t.Errorf("coverage does not state the due window: %q", c.DueWindow)
	}

	// detail=full lists every line and then claims no omission of lines.
	_, w = call(t, ctx, h, map[string]any{"detail": "full"})
	if c := w.Data.Coverage; c.LinesNotListed == nil || *c.LinesNotListed != 0 || *c.LinesListed != 4 {
		t.Errorf("detail=full coverage = %+v, want all 4 listed", c)
	} else if strings.Contains(c.HowToGetDetail, "bill and loan lines are not listed") {
		t.Errorf("detail=full still claims missing lines: %q", c.HowToGetDetail)
	}

	// category=… returns one category with its lines and says so in scope.
	_, w = call(t, ctx, h, map[string]any{"category": "housing"})
	d := w.Data
	if len(d.Budget.Categories) != 1 || d.Budget.Categories[0].Name != "Housing" {
		t.Fatalf("category=housing categories = %+v", d.Budget.Categories)
	}
	// Housing holds the Mortgage loan and both bills are filed under Housing
	// in this seed (they carry budget_category_id $2).
	listed := len(d.Budget.Categories[0].Lines)
	if listed == 0 {
		t.Errorf("category=housing listed no lines: %+v", d.Budget.Categories[0].Lines)
	}
	if !strings.Contains(d.Coverage.Scope, "Housing") || d.Coverage.Detail != detailLines {
		t.Errorf("category coverage = %+v, want it to name the category and the detail it listed", d.Coverage)
	}
	// The other categories' lines were dropped from this result, so coverage
	// must still say lines are missing, or the answer looks complete (#4809).
	if d.Coverage.LinesNotListed == nil || *d.Coverage.LinesNotListed == 0 {
		t.Errorf("narrowed coverage claims nothing was left out: %+v", d.Coverage)
	}
	if *d.Coverage.LinesListed != listed {
		t.Errorf("lines_listed %d, want the %d listed", *d.Coverage.LinesListed, listed)
	}
	// The totals still cover the whole household, and the summary says so
	// instead of reading like Housing's own total.
	if d.Budget.Totals.Committed != "2062.50" {
		t.Errorf("narrowed totals = %q, want the household's 2062.50", d.Budget.Totals.Committed)
	}
	if !strings.Contains(w.Summary, "the whole household pays $2,062.50") {
		t.Errorf("narrowed summary reads as the category's total: %q", w.Summary)
	}

	// An unknown category is refused with the names that exist, never a zero.
	res, w := call(t, ctx, h, map[string]any{"category": "Groceries"})
	if !res.IsError || w.Error == nil || w.Error.Field != "category" || !strings.Contains(w.Error.Fix, "Housing") {
		t.Errorf("unknown category = %+v", w)
	}

	// due_within_days narrows the window and the coverage states it.
	_, w = call(t, ctx, h, map[string]any{"sections": []any{"due_soon"}, "due_within_days": 3})
	if d := w.Data.DueSoon; d.Days != 3 {
		t.Errorf("due_within_days=3 gave %d", d.Days)
	}
	if c := w.Data.Coverage; !strings.Contains(c.DueWindow, w.Data.DueSoon.From+" to "+w.Data.DueSoon.To) {
		t.Errorf("coverage window = %q, want %s to %s", c.DueWindow, w.Data.DueSoon.From, w.Data.DueSoon.To)
	}
}

// TestBudgetSummary_ParseNewArgs covers the #4809 parameters' accepted and
// rejected forms, including the sloppy ones small models send.
func TestBudgetSummary_ParseNewArgs(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	_, _, h := NewBudgetSummaryTool(pool)

	for raw, want := range map[any]string{
		nil: detailSummary, "": detailSummary, "summary": detailSummary,
		"LINES": detailLines, "lines": detailLines, "full": detailFull, " Full ": detailFull,
	} {
		if got, err := parseDetail(raw); err != nil || got != want {
			t.Errorf("parseDetail(%v) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []any{"verbose", "all", 3.0} {
		if _, err := parseDetail(raw); err == nil {
			t.Errorf("parseDetail(%v) accepted a bad level", raw)
		}
	}

	for raw, want := range map[any]int{nil: 0, 7.0: 7, 1.0: 1, 365.0: 365, "14": 14, "": 0} {
		if got, err := parseDueWithin(raw); err != nil || got != want {
			t.Errorf("parseDueWithin(%v) = %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, raw := range []any{0.0, 366.0, -5.0, 2.5, "soon", true} {
		if _, err := parseDueWithin(raw); err == nil {
			t.Errorf("parseDueWithin(%v) accepted a bad window", raw)
		}
	}

	// A bad value reaches the caller as an isError result with a fix.
	for _, tc := range []struct {
		args  map[string]any
		field string
	}{
		{map[string]any{"detail": "verbose"}, "detail"},
		{map[string]any{"due_within_days": 9999.0}, "due_within_days"},
		{map[string]any{"sections": []any{"due_soon"}, "category": "Housing"}, "category"},
		{map[string]any{"category": "Housing"}, "category"}, // no such category yet here
	} {
		res, w := call(t, ctx, h, tc.args)
		if !res.IsError || w.Error == nil || w.Error.Field != tc.field || w.Error.Fix == "" || w.Error.Example == nil {
			t.Errorf("%v: want an isError on %s with fix and example, got %+v", tc.args, tc.field, w)
		}
	}
}
