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
	"strconv"
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

// allSections is the sections enum: every section a caller may ask for.
var allSections = []string{sectionObligations, sectionBudget, sectionDueSoon, sectionNetWorth}

// defaultSections is what budget_summary answers when sections is absent
// (#4809): what the family owes each month by budget category, and what falls
// due soon. obligations and net_worth only on request, because obligations
// repeats the same monthly total budget already reports and net_worth is a
// different question ("what are we worth").
var defaultSections = []string{sectionBudget, sectionDueSoon}

// dueSoonDays is due_soon's default window: "what's due in the next 30 days".
const dueSoonDays = 30

// maxDueSoonDays bounds due_within_days.
const maxDueSoonDays = 365

// Detail levels, smallest first. The default is summary: no per-bill lines,
// so a small local model gets an answer it can reason about in one small
// result, and asks for lines only when it needs them (#4809).
const (
	detailSummary = "summary"
	detailLines   = "lines"
	detailFull    = "full"
)

// detailLevels is the detail enum, smallest first.
var detailLevels = []string{detailSummary, detailLines, detailFull}

// depthOf maps a validated detail level to the budget package's depth.
func depthOf(detail string) budget.Depth {
	switch detail {
	case detailFull:
		return budget.DepthFull
	case detailLines:
		return budget.DepthLines
	default:
		return budget.DepthSummary
	}
}

// budgetSummary is budget_summary's data. Only the requested sections are
// present.
//
// Coverage always states what this result does and does not cover (#4809):
// the war game's worst failure was an agent confidently answering "nothing is
// due" from a summary that never said what it had left out. So every result
// carries the scope it was asked for, the detail level, the window, every
// section that was not included, and a count of the lines it did not list.
type budgetSummary struct {
	// Coverage comes first in the payload: a small model reading top-down
	// meets the scope before any number.
	Coverage    coverage        `json:"coverage" jsonschema_description:"What this result covers: the month, the scope, the detail level and how much detail it left out. Read this before concluding anything is missing."`
	Month       string          `json:"month" jsonschema_description:"The month summarized, YYYY-MM"`
	Sections    []string        `json:"sections" jsonschema_description:"The sections included in this result"`
	Obligations *obligations    `json:"obligations,omitempty" jsonschema_description:"Monthly bills by their category text plus loan payments"`
	Budget      *budget.Report  `json:"budget,omitempty" jsonschema_description:"What the family owes each month grouped by budget category: budgeted, committed, remaining, and (with detail=lines or full) the bill and loan payment lines behind each category"`
	DueSoon     *budget.DueSoon `json:"due_soon,omitempty" jsonschema_description:"Bills and loan payments due from today through the end of the window"`
	NetWorth    *netWorth       `json:"net_worth,omitempty" jsonschema_description:"What the family owns minus what it owes, today"`
}

// coverage says what a budget_summary result does and does not cover.
//
// It is the guard against the war game's worst failure: an agent answering
// "nothing is due" from a summary that was never the whole picture (#4809).
// Everything a caller needs to tell a summary from the full answer is here,
// in words, at the top of the payload.
type coverage struct {
	Detail      string   `json:"detail" jsonschema_description:"How much detail this result carries: summary (no per-bill lines), lines (the largest few per category) or full (every line)"`
	Month       string   `json:"month" jsonschema_description:"The month the budget section covers, YYYY-MM"`
	DueWindow   string   `json:"due_window,omitempty" jsonschema_description:"The due_soon window, \"<from> to <to>\"; absent when due_soon is not included"`
	Scope       string   `json:"scope" jsonschema_description:"What these numbers cover, in words. Nothing outside this scope is counted or implied to be missing"`
	Sections    []string `json:"sections" jsonschema_description:"The sections included in this result"`
	NotIncluded []string `json:"not_included,omitempty" jsonschema_description:"Sections that exist but are not in this result, so their numbers are not here at all. Ask for one with sections to get it"`

	// The counters are pointers so they only appear for a section that is
	// present: an obligations-only answer has no lines to count (#4809).
	LinesListed       *int `json:"lines_listed,omitempty" jsonschema_description:"Bill and loan payment lines listed in this result"`
	LinesNotListed    *int `json:"lines_not_listed,omitempty" jsonschema_description:"Lines that exist for the month but are not listed here. Their money IS in every total and per-category figure (totals always cover everything); only the per-line names and amounts are missing"`
	DueItemsListed    *int `json:"due_items_listed,omitempty" jsonschema_description:"Bills and loan payments listed in due_soon"`
	DueItemsNotListed *int `json:"due_items_not_listed,omitempty" jsonschema_description:"Bills and loan payments that fall due in the window but are not listed. They ARE in due_soon.total and its counts; only the per-item names and amounts are missing"`

	HowToGetDetail string `json:"how_to_get_detail" jsonschema_description:"Exactly how to ask for the detail this result left out"`
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
	// detail is detailSummary, detailLines or detailFull.
	detail string
	// category narrows the budget section to one category (any case) and, when
	// it is given, lists that category's lines even at detail=summary, since
	// asking for one category is asking for its detail.
	category string
	// dueWithinDays is how many days ahead due_soon looks; zero means
	// dueSoonDays. Ignored when due_soon is not requested.
	dueWithinDays int
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
		mcp.WithDescription("What the family pays each month by budget category, and what is due soon. "+
			"A summary by default; coverage in the result says what it left out. Amounts are in the family's currency."),
		mcp.WithString("month", mcp.Description("Month to summarize, YYYY-MM (default this month)")),
		mcp.WithString("detail", mcp.Description("How much detail: summary (default, no lines), lines (largest few), full (all)"),
			mcp.Enum(detailLevels...)),
		mcp.WithString("category", mcp.Description("Only this budget category, e.g. Groceries; lists its lines")),
		mcp.WithNumber("due_within_days", mcp.Description("due_soon window in days, 1-365 (default 30)"),
			mcp.Min(1), mcp.Max(maxDueSoonDays)),
		mcp.WithArray("sections",
			mcp.Description("Sections to include (default budget, due_soon), e.g. [\"net_worth\"]"),
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
		depth := depthOf(in.detail)
		// Asking about one category is asking for its detail: list its lines
		// even at detail=summary, or the answer would name no bills at all.
		budgetDepth := depth
		if in.category != "" && budgetDepth == budget.DepthSummary {
			budgetDepth = budget.DepthLines
		}

		if in.wants(sectionObligations) {
			ob, err := loadObligations(ctx, pool, householdID, in.month)
			if err != nil {
				return respond.Failed("total the family's bills and loan payments", err), nil
			}
			out.Obligations = ob
		}
		if in.wants(sectionBudget) {
			rep, err := budget.LoadDepth(ctx, pool, householdID, in.month, budgetDepth)
			if err != nil {
				return respond.Failed("total the family's budget categories", err), nil
			}
			if in.category != "" {
				if narrowed := rep.Narrow(in.category); narrowed == nil {
					names := rep.CategoryNames()
					return respond.Invalid("category", fmt.Sprintf("%q is not a budget category in this household.", in.category),
						"Pass category as one of: "+strings.Join(names, ", ")+"; or leave it out for every category.",
						map[string]any{"category": firstOr(names, in.category)}), nil
				} else {
					rep = narrowed
				}
			}
			out.Budget = rep
		}
		if in.wants(sectionDueSoon) {
			today, err := bill.HouseholdToday(ctx, pool, householdID)
			if err != nil {
				return respond.Failed("load the family's timezone", err), nil
			}
			days := in.dueWithinDays
			if days == 0 {
				days = dueSoonDays
			}
			ds, err := budget.LoadDueSoonDepth(ctx, pool, householdID, today, days, depth)
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

		// The coverage block must describe what this result actually carries:
		// asking for one category lists its lines even at detail=summary, so
		// report that level rather than claiming there are no lines (#4809).
		effectiveDetail := in.detail
		if in.category != "" && effectiveDetail == detailSummary {
			effectiveDetail = detailLines
		}
		out.Coverage = coverOf(in, out, effectiveDetail)
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
			"Pass sections as a list of: "+strings.Join(allSections, ", ")+"; or leave it out for "+strings.Join(defaultSections, " and ")+".",
			map[string]any{"sections": []string{sectionObligations, sectionNetWorth}})
	}
	in.sections = sections

	detail, err := parseDetail(args["detail"])
	if err != nil {
		return in, respond.Invalid("detail", err.Error(),
			"Pass detail as one of: "+strings.Join(detailLevels, ", ")+"; or leave it out for the summary.",
			map[string]any{"detail": detailFull})
	}
	in.detail = detail

	if raw := args["category"]; raw != nil {
		rawName, ok := raw.(string)
		if !ok {
			return in, respond.Invalid("category", fmt.Sprintf("category %v is not a name.", raw),
				"Pass category as a budget category name, e.g. Groceries, or leave it out for every category.",
				map[string]any{"category": "Groceries"})
		}
		c := strings.Join(strings.Fields(rawName), " ")
		if c != "" && !in.wants(sectionBudget) {
			return in, respond.Invalid("category", "category narrows the budget section, which sections leaves out.",
				"Pass sections=[\"budget\"] with category, or leave the category out.",
				map[string]any{"sections": []string{"budget"}, "category": "Groceries"})
		}
		in.category = c
	}

	days, err := parseDueWithin(args["due_within_days"])
	if err != nil {
		return in, respond.Invalid("due_within_days", err.Error(),
			fmt.Sprintf("Pass due_within_days as a whole number of days, 1-%d, or leave it out for %d.", maxDueSoonDays, dueSoonDays),
			map[string]any{"due_within_days": 7})
	}
	in.dueWithinDays = days
	return in, nil
}

// parseDetail accepts a detail level in any case and with "-" or " " for "_",
// as small models send it. Nothing given means detailSummary.
func parseDetail(raw any) (string, error) {
	if raw == nil {
		return detailSummary, nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("detail must be a name, not %T.", raw)
	}
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("-", "_", " ", "_").Replace(s)
	if s == "" {
		return detailSummary, nil
	}
	for _, l := range detailLevels {
		if l == s {
			return s, nil
		}
	}
	return "", fmt.Errorf("%q is not a budget_summary detail level.", s)
}

// parseDueWithin accepts a positive whole number of days; 0 means "not
// given". A float64 is how mcp-go decodes a JSON number.
func parseDueWithin(raw any) (int, error) {
	switch v := raw.(type) {
	case nil:
		return 0, nil
	case float64:
		if v != math.Trunc(v) {
			return 0, fmt.Errorf("due_within_days %v is not a whole number of days.", v)
		}
		if v < 1 || v > maxDueSoonDays {
			return 0, fmt.Errorf("due_within_days %v is outside 1-%d.", v, maxDueSoonDays)
		}
		return int(v), nil
	case int:
		if v < 1 || v > maxDueSoonDays {
			return 0, fmt.Errorf("due_within_days %v is outside 1-%d.", v, maxDueSoonDays)
		}
		return v, nil
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number of days.", s)
		}
		if n < 1 || n > maxDueSoonDays {
			return 0, fmt.Errorf("due_within_days %v is outside 1-%d.", n, maxDueSoonDays)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("due_within_days must be a number of days, not %T.", raw)
	}
}

// parseSections accepts a list of section names, or a single (possibly
// comma-separated) string, which small models often send instead. Names are
// matched in any case, with "-" or " " for "_". The result is in catalog
// order without duplicates; nothing given means defaultSections (#4809), the
// small answer: budget and due_soon. obligations and net_worth must be asked
// for by name.
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
		return append([]string(nil), defaultSections...), nil
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

// scope is what every budget_summary result covers, in one sentence. Home OS
// holds no transactions and no bank accounts (product direction of
// 2026-11-15); saying so stops a model reading a summary as a full accounting
// picture.
const scope = "The household's own bills, loans and budget categories. No transactions, bank accounts or other households."

// coverOf builds the coverage block (#4809). It is the part of the result a
// small model must read before concluding that something does not exist: it
// names what was asked for, the detail level, the window, the sections that
// were left out, and how to get the detail this result omitted.
func coverOf(in summaryInput, out budgetSummary, effectiveDetail string) coverage {
	days := in.dueWithinDays
	if days == 0 {
		days = dueSoonDays
	}
	c := coverage{Detail: effectiveDetail, Month: out.Month, Scope: scope, Sections: in.sections}
	if in.category != "" {
		// The stored spelling, not what the caller typed.
		name := in.category
		if rep := out.Budget; rep != nil && rep.NarrowedTo() != "" {
			name = rep.NarrowedTo()
		}
		c.Scope = "Only the " + name + " budget category. Other categories' money is in the totals but their lines are not here."
	}
	if ds := out.DueSoon; ds != nil {
		c.DueWindow = ds.From + " to " + ds.To
		listed, notListed := len(ds.Items), ds.MoreItems
		c.DueItemsListed, c.DueItemsNotListed = &listed, &notListed
	}
	if rep := out.Budget; rep != nil {
		var listed, notListed int
		for _, cat := range rep.Categories {
			listed += len(cat.Lines)
			notListed += cat.MoreLines
		}
		listed += len(rep.Uncategorized.Lines)
		notListed += rep.Uncategorized.MoreLines
		// A narrowed report dropped the other categories' lines: they are not
		// in this result, so they must count as not listed (#4809).
		notListed += rep.DroppedLines()
		c.LinesListed, c.LinesNotListed = &listed, &notListed
	}
	for _, s := range allSections {
		if !in.wants(s) {
			c.NotIncluded = append(c.NotIncluded, s)
		}
	}
	c.HowToGetDetail = howToGetDetail(in, c, days)
	return c
}

// howToGetDetail is the one sentence that tells a caller exactly how to ask
// for what this result left out. It only claims an omission that exists, so a
// complete answer does not send a model chasing detail it already has. The
// argument names are the same ones the tool takes, so a small model can pass
// them straight through.
func howToGetDetail(in summaryInput, c coverage, days int) string {
	var asks []string
	if n := intOr(c.LinesNotListed); n > 0 {
		// The advice has to name the argument that actually returns the
		// missing lines: with category=… set, the lines still missing belong
		// to the categories this result left out, so detail=full alone would
		// not return them (#4809).
		ask := `detail="lines" for the largest lines per category, or detail="full" for every line`
		switch {
		case in.category != "":
			ask = `leave category out and pass detail="lines" for every category's largest lines, or detail="full" for every line`
		case in.detail == detailLines:
			ask = `detail="full" for every line`
		}
		asks = append(asks, fmt.Sprintf("%d of the month's bill and loan lines are not listed (their money is in committed and the totals): pass %s", n, ask))
	}
	if n := intOr(c.DueItemsNotListed); n > 0 {
		ask := `detail="lines" to list up to 20, or detail="full" for all`
		if in.detail != detailSummary {
			ask = fmt.Sprintf(`due_within_days=%d with detail="full" to list them all`, days)
		}
		asks = append(asks, fmt.Sprintf("%d of the items due in the window are not listed (their money is in due_soon.total): pass %s", n, ask))
	}
	if len(c.NotIncluded) > 0 {
		asks = append(asks, "sections="+sectionList(c.NotIncluded)+" to add the "+joinWords(c.NotIncluded)+" section")
	}
	if len(asks) == 0 {
		return "Nothing is omitted: this result carries every line and every item in scope."
	}
	return strings.Join(asks, "; ") + "."
}

// sectionList renders section names as a JSON list for a hint, e.g.
// ["net_worth","obligations"].
func sectionList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = `"` + n + `"`
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

// joinWords joins names as "a, b and c".
func joinWords(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// intOr reads an optional counter as an int.
func intOr(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// firstOr returns the first name, or def when names is empty.
func firstOr(names []string, def string) string {
	if len(names) > 0 {
		return names[0]
	}
	return def
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
	case rep != nil && rep.NarrowedTo() != "":
		// Narrowed: the totals are still the household's, so say so rather
		// than letting the category sentence below look like the total.
		parts = append(parts, fmt.Sprintf("the whole household pays %s a month in bills and loan payments (%s, %s);",
			budget.Money(rep.CommittedCents()), plural(rep.Totals.BillCount, "bill"), plural(rep.Totals.LoanCount, "loan")))
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
