// Package finance provides the MCP budget tool (catalog v2, KB
// architecture/mcp-tool-catalog.md "budget"): budget_summary, the one call for
// "what do we pay each month?" and "what are we worth?". It replaces
// get_financial_summary and the bill package's get_bill_summary. Results are
// built with internal/respond; the tool declares all four annotation hints and
// an outputSchema. Everything is scoped to the authenticated household.
//
// obligations and net_worth are built here; budget (every budget category
// with budgeted, committed and remaining, line by line with each line's
// source) and due_soon (the next 30 days) come from internal/budget (#4470).
// Money is readable by the family roles and viewer only (budget.CanRead).
package finance

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/bill"
	"home-os/mcp/internal/budget"
	"home-os/mcp/internal/respond"
)

// Section names, in the order the catalog lists them and results report them.
const (
	sectionObligations = "obligations"
	sectionBudget      = "budget"
	sectionDueSoon     = "due_soon"
	sectionNetWorth    = "net_worth"
)

// allSections is the sections enum and the default when none are given.
var allSections = []string{sectionObligations, sectionBudget, sectionDueSoon, sectionNetWorth}

// dueSoonDays is the due_soon window: "what's due in the next 30 days".
const dueSoonDays = 30

// budgetSummary is budget_summary's data. Only the requested sections are
// present.
type budgetSummary struct {
	Month       string          `json:"month" jsonschema_description:"The month summarized, YYYY-MM"`
	Sections    []string        `json:"sections" jsonschema_description:"The sections included in this result"`
	Obligations *obligations    `json:"obligations,omitempty" jsonschema_description:"Monthly bills by their category text plus loan payments"`
	Budget      *budget.Report  `json:"budget,omitempty" jsonschema_description:"What the family owes each month grouped by budget category: budgeted, committed, remaining, and every bill and loan payment line with its source"`
	DueSoon     *budget.DueSoon `json:"due_soon,omitempty" jsonschema_description:"Bills and loan payments due from today through the next 30 days"`
	NetWorth    *netWorth       `json:"net_worth,omitempty" jsonschema_description:"What the family owns minus what it owes, today"`
}

// obligations is what the family pays each month. BillsByCategory is exactly
// what bill.SummarizeByCategory returns (the old get_bill_summary numbers).
type obligations struct {
	BillsByCategory     []bill.CategoryTotal `json:"bills_by_category" jsonschema_description:"Bills grouped by category, largest total first"`
	BillCount           int                  `json:"bill_count"`
	BillsTotal          float64              `json:"bills_total" jsonschema_description:"Sum of all bill amounts"`
	LoanCount           int                  `json:"loan_count" jsonschema_description:"Active loans with a monthly payment in this month"`
	LoanPaymentsTotal   float64              `json:"loan_payments_total" jsonschema_description:"Sum of those loans' monthly payments"`
	LoansWithoutPayment int                  `json:"loans_without_payment" jsonschema_description:"Active loans with no monthly_payment set (not counted)"`
	MonthlyTotal        float64              `json:"monthly_total" jsonschema_description:"bills_total + loan_payments_total"`
}

// netWorth is the household's estimated net worth. Loans are split by their
// typed subject (domain model v2): a property loan has property_id and no
// asset_id, a vehicle loan has vehicle_id; everything else is other_loans.
type netWorth struct {
	PropertyValue     float64 `json:"property_value" jsonschema_description:"Sum of properties' current_value"`
	AssetValue        float64 `json:"asset_value" jsonschema_description:"Sum of assets' current_value, else purchase_price"`
	VehicleValue      float64 `json:"vehicle_value" jsonschema_description:"Sum of vehicles' current_value"`
	TotalValue        float64 `json:"total_value" jsonschema_description:"property_value + asset_value + vehicle_value"`
	MortgageDebt      float64 `json:"mortgage_debt" jsonschema_description:"Remaining balance of loans on a property"`
	VehicleLoans      float64 `json:"vehicle_loans" jsonschema_description:"Remaining balance of loans on a vehicle"`
	OtherLoans        float64 `json:"other_loans" jsonschema_description:"Remaining balance of every other loan"`
	TotalLiabilities  float64 `json:"total_liabilities" jsonschema_description:"Remaining balance of all loans"`
	EstimatedNetWorth float64 `json:"estimated_net_worth" jsonschema_description:"total_value - total_liabilities"`
}

// summaryInput is the validated budget_summary arguments.
type summaryInput struct {
	// month is the first day of the month, UTC; zero means "this month".
	month    time.Time
	sections []string
}

func (in summaryInput) wants(section string) bool {
	for _, s := range in.sections {
		if s == section {
			return true
		}
	}
	return false
}

// NewBudgetSummaryTool returns budget_summary's name, definition and handler.
func NewBudgetSummaryTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("budget_summary",
		mcp.WithDescription("One call for \"what do we pay each month?\": bills and loan payments by category, "+
			"and net worth. Amounts are in the family's currency."),
		mcp.WithString("month", mcp.Description("Month to summarize, YYYY-MM, e.g. 2026-11 (default this month)")),
		mcp.WithArray("sections",
			mcp.Description("Sections to include (default all), e.g. [\"obligations\"]"),
			mcp.WithStringEnumItems(allSections)),
		respond.Schema[budgetSummary](),
		mcp.WithTitleAnnotation("Budget summary"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		caller, res := budget.CallerOf(ctx, pool)
		if res != nil {
			return res, nil
		}
		if !budget.CanRead(caller.Role) {
			return budget.CannotRead(caller.Role), nil
		}
		householdID := caller.HouseholdID

		in, res := parseSummaryInput(req.GetArguments())
		if res != nil {
			return res, nil
		}
		if in.month.IsZero() {
			m, err := householdMonth(ctx, pool, householdID)
			if err != nil {
				return respond.Failed("load the family's timezone", err), nil
			}
			in.month = m
		}

		out := budgetSummary{Month: in.month.Format("2006-01"), Sections: in.sections}

		if in.wants(sectionObligations) {
			ob, err := loadObligations(ctx, pool, householdID, in.month)
			if err != nil {
				return respond.Failed("total the family's bills and loan payments", err), nil
			}
			out.Obligations = ob
		}
		if in.wants(sectionBudget) {
			rep, err := budget.Load(ctx, pool, householdID, in.month)
			if err != nil {
				return respond.Failed("total the family's budget categories", err), nil
			}
			out.Budget = rep
		}
		if in.wants(sectionDueSoon) {
			today, err := bill.HouseholdToday(ctx, pool, householdID)
			if err != nil {
				return respond.Failed("load the family's timezone", err), nil
			}
			ds, err := budget.LoadDueSoon(ctx, pool, householdID, today, dueSoonDays)
			if err != nil {
				return respond.Failed("list what is due soon", err), nil
			}
			out.DueSoon = ds
		}
		if in.wants(sectionNetWorth) {
			nw, err := loadNetWorth(ctx, pool, householdID)
			if err != nil {
				return respond.Failed("estimate the family's net worth", err), nil
			}
			out.NetWorth = nw
		}

		return respond.OK(summarize(out), out, nextHints(out)...), nil
	}

	return "budget_summary", tool, handler
}

// parseSummaryInput validates month and sections. It returns an isError
// result (and a zero input) when an argument is wrong.
func parseSummaryInput(args map[string]any) (summaryInput, *mcp.CallToolResult) {
	var in summaryInput

	if raw, ok := args["month"]; ok && raw != nil {
		s, isString := raw.(string)
		s = strings.TrimSpace(s)
		if !isString {
			return in, respond.Invalid("month", fmt.Sprintf("month %v is not a YYYY-MM string.", raw),
				"Pass month as YYYY-MM, or leave it out for this month.", map[string]any{"month": "2026-11"})
		}
		if s != "" {
			m, err := time.Parse("2006-01", s)
			if err != nil {
				return in, respond.Invalid("month", fmt.Sprintf("month %q is not a month in YYYY-MM form.", s),
					"Pass month as YYYY-MM, e.g. 2026-11, or leave it out for this month.", map[string]any{"month": "2026-11"})
			}
			in.month = m
		}
	}

	sections, err := parseSections(args["sections"])
	if err != nil {
		return in, respond.Invalid("sections", err.Error(),
			"Pass sections as a list of: "+strings.Join(allSections, ", ")+"; or leave it out for all of them.",
			map[string]any{"sections": []string{sectionObligations, sectionNetWorth}})
	}
	in.sections = sections
	return in, nil
}

// parseSections accepts a list of section names, or a single (possibly
// comma-separated) string, which small models often send instead. Names are
// matched in any case, with "-" or " " for "_". The result is in catalog
// order without duplicates; nothing given means every section.
func parseSections(raw any) ([]string, error) {
	var names []string
	switch v := raw.(type) {
	case nil:
	case string:
		names = strings.Split(v, ",")
	case []string:
		names = v
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("sections must be a list of names; %v is not a name.", item)
			}
			names = append(names, s)
		}
	default:
		return nil, fmt.Errorf("sections must be a list of names, not %T.", raw)
	}

	want := map[string]bool{}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		n = strings.NewReplacer("-", "_", " ", "_").Replace(n)
		if n == "" {
			continue
		}
		known := false
		for _, s := range allSections {
			if s == n {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("%q is not a budget_summary section.", n)
		}
		want[n] = true
	}
	if len(want) == 0 {
		return append([]string(nil), allSections...), nil
	}
	out := make([]string, 0, len(want))
	for _, s := range allSections {
		if want[s] {
			out = append(out, s)
		}
	}
	return out, nil
}

// householdMonth is the first day of the current month in the family's
// timezone (UTC when the timezone is unset or unknown).
func householdMonth(ctx context.Context, pool *pgxpool.Pool, householdID string) (time.Time, error) {
	var tz *string
	if err := pool.QueryRow(ctx, `SELECT timezone FROM households WHERE id = $1`, householdID).Scan(&tz); err != nil {
		return time.Time{}, fmt.Errorf("load household timezone: %w", err)
	}
	loc := time.UTC
	if tz != nil && *tz != "" {
		if l, err := time.LoadLocation(*tz); err == nil {
			loc = l
		}
	}
	y, m, _ := time.Now().In(loc).Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC), nil
}

// loadObligations totals bills with bill.SummarizeByCategory for month (bills
// spread to monthly amounts, #4468; so the numbers
// match what bill tools report) and adds the monthly payments of loans that
// are active in month: a balance left, started before the month ends (or no
// start date) and not ended before it begins (or no term_end).
func loadObligations(ctx context.Context, pool *pgxpool.Pool, householdID string, month time.Time) (*obligations, error) {
	cats, billsTotal, err := bill.SummarizeByCategory(ctx, pool, householdID, month)
	if err != nil {
		return nil, err
	}
	ob := &obligations{BillsByCategory: cats, BillsTotal: cents(billsTotal)}
	for _, c := range cats {
		ob.BillCount += c.Count
	}

	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FILTER (WHERE monthly_payment > 0),
		        COALESCE(SUM(monthly_payment) FILTER (WHERE monthly_payment > 0), 0)::float8,
		        COUNT(*) FILTER (WHERE monthly_payment IS NULL)
		 FROM loans
		 WHERE household_id = $1
		   AND remaining_balance > 0
		   AND (start_date IS NULL OR start_date < $3)
		   AND (term_end IS NULL OR term_end >= $2)`,
		householdID, month, month.AddDate(0, 1, 0),
	).Scan(&ob.LoanCount, &ob.LoanPaymentsTotal, &ob.LoansWithoutPayment)
	if err != nil {
		return nil, fmt.Errorf("sum loan payments: %w", err)
	}
	ob.LoanPaymentsTotal = cents(ob.LoanPaymentsTotal)
	ob.MonthlyTotal = cents(ob.BillsTotal + ob.LoanPaymentsTotal)
	return ob, nil
}

// Loan subject predicates over the typed subject columns (domain model v2,
// 038/039). An asset loan also carries its asset's property_id
// (derive_subject_property), so it is not counted as mortgage debt.
const (
	propertyLoan = `(property_id IS NOT NULL AND asset_id IS NULL)`
	vehicleLoan  = `(vehicle_id IS NOT NULL)`
)

// loadNetWorth sums what the family owns and owes. An asset counts at its
// current_value, or its purchase_price when no current value is known.
func loadNetWorth(ctx context.Context, pool *pgxpool.Pool, householdID string) (*netWorth, error) {
	var nw netWorth
	err := pool.QueryRow(ctx,
		`SELECT (SELECT COALESCE(SUM(current_value), 0)::float8 FROM properties WHERE household_id = $1),
		        (SELECT COALESCE(SUM(COALESCE(current_value, purchase_price)), 0)::float8 FROM assets WHERE household_id = $1),
		        (SELECT COALESCE(SUM(current_value), 0)::float8 FROM vehicles WHERE household_id = $1),
		        COALESCE(SUM(remaining_balance), 0)::float8,
		        COALESCE(SUM(remaining_balance) FILTER (WHERE `+propertyLoan+`), 0)::float8,
		        COALESCE(SUM(remaining_balance) FILTER (WHERE `+vehicleLoan+`), 0)::float8
		 FROM loans
		 WHERE household_id = $1`,
		householdID,
	).Scan(&nw.PropertyValue, &nw.AssetValue, &nw.VehicleValue, &nw.TotalLiabilities, &nw.MortgageDebt, &nw.VehicleLoans)
	if err != nil {
		return nil, fmt.Errorf("sum net worth: %w", err)
	}
	nw.OtherLoans = math.Max(0, cents(nw.TotalLiabilities-nw.MortgageDebt-nw.VehicleLoans))
	nw.TotalValue = cents(nw.PropertyValue + nw.AssetValue + nw.VehicleValue)
	nw.EstimatedNetWorth = cents(nw.TotalValue - nw.TotalLiabilities)
	return &nw, nil
}

// summarize writes the one-line summary, e.g. "November 2026: $1,642.50 a
// month in bills and loan payments (6 bills, 1 loan). Estimated net worth
// $412,000.00."
func summarize(out budgetSummary) string {
	month, _ := time.Parse("2006-01", out.Month)
	parts := []string{month.Format("January 2006") + ":"}
	switch ob, rep := out.Obligations, out.Budget; {
	case ob != nil && ob.BillCount == 0 && ob.LoanCount == 0,
		ob == nil && rep != nil && rep.Totals.BillCount == 0 && rep.Totals.LoanCount == 0:
		parts = append(parts, "no bills or loan payments recorded yet.")
	case ob != nil:
		parts = append(parts, fmt.Sprintf("%s a month in bills and loan payments (%s, %s).",
			money(ob.MonthlyTotal), plural(ob.BillCount, "bill"), plural(ob.LoanCount, "loan")))
	case rep != nil:
		parts = append(parts, fmt.Sprintf("%s a month in bills and loan payments (%s, %s).",
			budget.Money(rep.CommittedCents()), plural(rep.Totals.BillCount, "bill"), plural(rep.Totals.LoanCount, "loan")))
	}
	if rep := out.Budget; rep != nil {
		parts = append(parts, rep.Describe())
	}
	if ds := out.DueSoon; ds != nil {
		parts = append(parts, ds.Describe())
	}
	if nw := out.NetWorth; nw != nil {
		parts = append(parts, fmt.Sprintf("Estimated net worth %s (%s owned, %s owed).",
			money(nw.EstimatedNetWorth), money(nw.TotalValue), money(nw.TotalLiabilities)))
	}
	return strings.Join(parts, " ")
}

// nextHints suggests 1-3 follow-up calls that name real tools.
func nextHints(out budgetSummary) []string {
	var next []string
	billCount := -1
	if ob := out.Obligations; ob != nil {
		billCount = ob.BillCount
	} else if rep := out.Budget; rep != nil {
		billCount = rep.Totals.BillCount
	}
	if billCount == 0 {
		next = append(next, "No bills yet. Add one with bill_create; don't put bills on the calendar as events.")
	}
	if rep := out.Budget; rep != nil {
		over := ""
		for _, c := range rep.Categories {
			if c.OverBudget {
				over = c.Name
				break
			}
		}
		switch {
		case len(rep.Categories) == 0:
			next = append(next, "No budget categories yet. Ask what the family wants to budget, then set each with budget_set_category category=… monthly_amount=….")
		case over != "":
			next = append(next, fmt.Sprintf("%s is over budget: if the family agrees, raise it with budget_set_category category=%q monthly_amount=…, or review its lines with bill_find and loan_find.", over, over))
		default:
			next = append(next, "Change a category's monthly budget with budget_set_category.")
		}
	}
	if ob := out.Obligations; ob != nil {
		if ob.BillCount > 0 && len(ob.BillsByCategory) > 0 {
			next = append(next, fmt.Sprintf("See the bills behind a category with bill_find category=%q.", ob.BillsByCategory[0].Category))
		}
		if ob.LoansWithoutPayment > 0 {
			next = append(next, fmt.Sprintf("%s no monthly_payment; find them with loan_find and set it with loan_update.",
				pluralVerb(ob.LoansWithoutPayment, "loan has", "loans have")))
		}
	}
	if ds := out.DueSoon; ds != nil && ds.BillCount > 0 {
		next = append(next, "Record a payment with bill_pay bill_id=<id from due_soon.items>.")
	}
	if len(next) > 3 {
		next = next[:3]
	}
	return next
}

// cents rounds to two decimals so float sums don't print as 0.30000000000000004.
func cents(x float64) float64 {
	return math.Round(x*100) / 100
}

// money formats an amount as "$1,234.50" ("-$12.00" when negative).
func money(x float64) string {
	sign := ""
	if x < 0 {
		sign, x = "-", -x
	}
	s := fmt.Sprintf("%.2f", x)
	whole, frac := s[:len(s)-3], s[len(s)-3:]
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return sign + "$" + b.String() + frac
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func pluralVerb(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
