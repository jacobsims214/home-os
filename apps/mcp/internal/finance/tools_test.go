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

	"home-os/mcp/internal/bill"
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
	for p := range tool.InputSchema.Properties {
		if p != "month" && p != "sections" {
			t.Errorf("unexpected param %q", p)
		}
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
		{"nil means all", nil, allSections, false},
		{"empty list means all", []any{}, allSections, false},
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

	_, w := call(t, ctx, h, nil)
	d := w.Data
	if d == nil || d.Obligations == nil || d.NetWorth == nil {
		t.Fatalf("data = %+v", d)
	}
	if want := time.Now().UTC().Format("2006-01"); d.Month != want {
		// Household timezone is unset (UTC) in the seed.
		t.Errorf("month = %s, want %s", d.Month, want)
	}
	if !reflect.DeepEqual(d.Sections, []string{"obligations", "net_worth"}) {
		t.Errorf("sections = %v", d.Sections)
	}
	if len(d.Pending) != 2 || d.Pending[0].Section != "budget" || d.Pending[1].Section != "due_soon" {
		t.Errorf("pending = %+v, want budget and due_soon", d.Pending)
	}
	if d.Obligations.BillsByCategory == nil || len(d.Obligations.BillsByCategory) != 0 || d.Obligations.MonthlyTotal != 0 {
		t.Errorf("obligations = %+v", d.Obligations)
	}
	if !strings.Contains(w.Summary, "no bills or loan payments recorded yet") || !strings.Contains(w.Summary, "Estimated net worth $0.00") {
		t.Errorf("summary = %q", w.Summary)
	}
	if len(w.Next) == 0 || !strings.Contains(w.Next[0], "bill_create") {
		t.Errorf("next = %v", w.Next)
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

	cats, grand, err := bill.SummarizeByCategory(context.Background(), pool, hh.HouseholdID.String())
	if err != nil {
		t.Fatal(err)
	}

	_, _, h := NewBudgetSummaryTool(pool)
	_, w := call(t, ctx, h, map[string]any{"month": "2026-11", "sections": []any{"obligations"}})
	d := w.Data
	if d.Month != "2026-11" || d.NetWorth != nil || len(d.Pending) != 0 || !reflect.DeepEqual(d.Sections, []string{"obligations"}) {
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
// purchase_price) and the loan split by typed subject, with the legacy
// entity_type fallback for loans MCP loan_create wrote before #4450.
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
		{"Truck loan (legacy entity_type)", "entity_type, entity_id", 7000, []any{"vehicle", truck}},
		{"HELOC (legacy entity_type)", "entity_type, entity_id", 20000, []any{"property", house}},
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

func TestBudgetSummary_PendingOnly(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	if _, err := pool.Exec(context.Background(), `UPDATE households SET timezone = 'Pacific/Kiritimati' WHERE id = $1`, hh.HouseholdID); err != nil {
		t.Fatal(err)
	}
	_, _, h := NewBudgetSummaryTool(pool)
	_, w := call(t, ctx, h, map[string]any{"sections": []any{"due_soon"}})
	d := w.Data
	loc, _ := time.LoadLocation("Pacific/Kiritimati")
	if want := time.Now().In(loc).Format("2006-01"); d.Month != want {
		t.Errorf("month = %s, want %s (family timezone)", d.Month, want)
	}
	if len(d.Sections) != 0 || d.Obligations != nil || d.NetWorth != nil || len(d.Pending) != 1 {
		t.Fatalf("data = %+v", d)
	}
	if !strings.Contains(w.Summary, "aren't available yet") || len(w.Next) != 1 || !strings.Contains(w.Next[0], "bill_find due_within_days=30") {
		t.Errorf("summary/next = %q %v", w.Summary, w.Next)
	}
}
