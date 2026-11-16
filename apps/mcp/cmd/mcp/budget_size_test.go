package main

// budget_summary payload size (#4809).
//
// budget_summary is what a family agent calls to answer "what do we pay each
// month?" and "what's due?". Its default answer used to be ~24.7 KB of
// content[0].text (~6.2k tokens) because it returned every section and up to
// eight bill lines per category. On a small local model that single result
// ate a large share of the context window, and the 2026-11-15 war game showed
// what that costs: lost turns and a 167-second call.
//
// The default is now a summary: budget and due_soon, no per-bill lines, with
// a coverage block saying what it left out and how to ask for it. This test
// holds the default under its budget on the benchmark household (300 bills,
// 10 budget categories, loans) and proves the detail stays reachable, so a
// future field cannot quietly grow the default back.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/finance"
	"home-os/mcp/internal/testutil"
)

// maxDefaultBudgetSummaryBytes is the budget for budget_summary's default
// answer on the benchmark household: 8 KB, as the ticket requires. The
// measured default is about 5.1 KB, so there is headroom for a field or two
// without a test failure being the first anyone hears about a size problem.
const maxDefaultBudgetSummaryBytes = 8 * 1024

// TestBudgetSummaryDefaultPayloadSize seeds the benchmark household (a year of
// a busy family's data) and checks that budget_summary with no arguments is
// small, complete in what it claims, and that every detail level is reachable.
func TestBudgetSummaryDefaultPayloadSize(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	seedBusyFamily(t, pool, hh.HouseholdID, hh.UserID, 5)
	if _, err := pool.Exec(context.Background(), `ANALYZE`); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	_, _, h := finance.NewBudgetSummaryTool(pool)

	call := func(args map[string]any) (text string, env budgetSizeEnvelope) {
		t.Helper()
		var req mcp.CallToolRequest
		req.Params.Name = "budget_summary"
		req.Params.Arguments = args
		res, err := h(ctx, req)
		if err != nil {
			t.Fatalf("budget_summary(%v): %v", args, err)
		}
		text = res.Content[0].(mcp.TextContent).Text
		if res.IsError {
			t.Fatalf("budget_summary(%v) failed: %s", args, text)
		}
		if err := json.Unmarshal([]byte(text), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return text, env
	}

	// The default answer: small, and honest about what it left out.
	text, env := call(nil)
	if n := len(text); n > maxDefaultBudgetSummaryBytes {
		t.Errorf("default budget_summary is %d bytes, budget is %d", n, maxDefaultBudgetSummaryBytes)
	} else {
		t.Logf("default budget_summary: %d bytes (~%d tokens)", n, n/4)
	}
	d := env.Data
	if d == nil {
		t.Fatalf("no data: %s", text)
	}
	if d.Budget == nil || d.DueSoon == nil {
		t.Fatalf("default is missing budget or due_soon: %s", text)
	}
	if d.Obligations != nil || d.NetWorth != nil {
		t.Errorf("default includes obligations or net_worth, which it should leave out")
	}
	// A summary must still be complete in the numbers it reports: the totals
	// cover every bill and loan, even though no line is listed.
	if d.Budget.Totals.BillCount < 250 {
		t.Errorf("budget totals cover %d bills, want the whole household's ~300", d.Budget.Totals.BillCount)
	}
	if d.Budget.Totals.Committed == "0.00" {
		t.Errorf("default committed total is zero on a seeded household")
	}
	for _, cat := range d.Budget.Categories {
		if len(cat.Lines) != 0 {
			t.Errorf("detail=summary listed %d lines in %s", len(cat.Lines), cat.Name)
		}
		if cat.MoreLines != cat.LineCount {
			t.Errorf("%s: more_lines %d, line_count %d; summary should count every line as unlisted",
				cat.Name, cat.MoreLines, cat.LineCount)
		}
	}
	// Coverage is what stops a model reading the summary as everything.
	c := d.Coverage
	if c.Detail != "summary" {
		t.Errorf("coverage.detail = %q, want summary", c.Detail)
	}
	if c.DueWindow == "" || c.Month == "" {
		t.Errorf("coverage does not state the window/month: %+v", c)
	}
	if c.LinesNotListed == nil || *c.LinesNotListed == 0 {
		t.Errorf("coverage does not say how many lines were left out: %+v", c)
	}
	if c.DueItemsNotListed == nil {
		t.Errorf("coverage does not say how many due items were left out: %+v", c)
	}
	if len(c.NotIncluded) != 2 || c.NotIncluded[0] != "obligations" || c.NotIncluded[1] != "net_worth" {
		t.Errorf("coverage.not_included = %v, want obligations and net_worth", c.NotIncluded)
	}
	if c.HowToGetDetail == "" {
		t.Error("coverage.how_to_get_detail is empty")
	}

	// Detail stays reachable, and each level is bigger than the one before.
	prev := len(text)
	for _, detail := range []string{"lines", "full"} {
		text, env := call(map[string]any{"detail": detail})
		if len(text) <= prev {
			t.Errorf("detail=%s is %d bytes, not bigger than the previous level's %d", detail, len(text), prev)
		}
		if got := env.Data.Coverage.Detail; got != detail {
			t.Errorf("coverage.detail = %q, want %q", got, detail)
		}
		if listed := linesListed(env); listed == 0 {
			t.Errorf("detail=%s listed no lines", detail)
		}
		prev = len(text)
	}

	// A category can be asked for on its own: its lines, without the rest.
	text, env = call(map[string]any{"category": "groceries"})
	if n := len(env.Data.Budget.Categories); n != 1 {
		t.Fatalf("category=groceries returned %d categories", n)
	}
	if env.Data.Budget.Categories[0].Name != "Groceries" {
		t.Errorf("category=groceries returned %q", env.Data.Budget.Categories[0].Name)
	}
	if len(env.Data.Budget.Categories[0].Lines) == 0 {
		t.Errorf("category=groceries listed no lines; asking for a category is asking for its detail")
	}
	if len(text) > maxDefaultBudgetSummaryBytes {
		t.Errorf("category=groceries is %d bytes, budget is %d", len(text), maxDefaultBudgetSummaryBytes)
	}

	// obligations and net_worth still work on request.
	for _, s := range []string{"obligations", "net_worth"} {
		text, env := call(map[string]any{"sections": []string{s}})
		if len(text) == 0 {
			t.Fatalf("sections=[%s] returned nothing", s)
		}
		if (s == "obligations") != (env.Data.Obligations != nil) || (s == "net_worth") != (env.Data.NetWorth != nil) {
			t.Errorf("sections=[%s] returned the wrong sections", s)
		}
	}

	// due_within_days narrows the window and says so.
	text, env = call(map[string]any{"sections": []string{"due_soon"}, "due_within_days": 7})
	if env.Data.DueSoon.Days != 7 {
		t.Errorf("due_within_days=7 gave days=%d", env.Data.DueSoon.Days)
	}
	if len(text) >= maxDefaultBudgetSummaryBytes {
		t.Errorf("due_soon for 7 days is %d bytes", len(text))
	}
}

// linesListed counts the bill and loan lines a result carries.
func linesListed(env budgetSizeEnvelope) int {
	if env.Data == nil || env.Data.Budget == nil {
		return 0
	}
	n := len(env.Data.Budget.Uncategorized.Lines)
	for _, cat := range env.Data.Budget.Categories {
		n += len(cat.Lines)
	}
	return n
}

// budgetSizeEnvelope is the part of a budget_summary result this test reads.
type budgetSizeEnvelope struct {
	OK   bool `json:"ok"`
	Data *struct {
		Coverage struct {
			Detail            string   `json:"detail"`
			Month             string   `json:"month"`
			DueWindow         string   `json:"due_window"`
			Sections          []string `json:"sections"`
			NotIncluded       []string `json:"not_included"`
			LinesListed       *int     `json:"lines_listed"`
			LinesNotListed    *int     `json:"lines_not_listed"`
			DueItemsListed    *int     `json:"due_items_listed"`
			DueItemsNotListed *int     `json:"due_items_not_listed"`
			HowToGetDetail    string   `json:"how_to_get_detail"`
		} `json:"coverage"`
		Month       string   `json:"month"`
		Sections    []string `json:"sections"`
		Obligations *struct {
			MonthlyTotal float64 `json:"monthly_total"`
		} `json:"obligations"`
		Budget *struct {
			Categories []struct {
				Name      string `json:"name"`
				LineCount int    `json:"line_count"`
				MoreLines int    `json:"more_lines"`
				Lines     []struct {
					Name string `json:"name"`
				} `json:"lines"`
			} `json:"categories"`
			Uncategorized struct {
				MoreLines int `json:"more_lines"`
				Lines     []struct {
					Name string `json:"name"`
				} `json:"lines"`
			} `json:"uncategorized"`
			Totals struct {
				Committed string `json:"committed"`
				BillCount int    `json:"bill_count"`
				LoanCount int    `json:"loan_count"`
			} `json:"totals"`
		} `json:"budget"`
		DueSoon *struct {
			Days  int `json:"days"`
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
			MoreItems int `json:"more_items"`
		} `json:"due_soon"`
		NetWorth *struct {
			EstimatedNetWorth float64 `json:"estimated_net_worth"`
		} `json:"net_worth"`
	} `json:"data"`
}
