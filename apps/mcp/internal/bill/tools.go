// Package bill provides MCP tools for managing household bills.
// All operations are scoped to the authenticated household via JWT claims.
package bill

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
)

// Bill status values reported by the read tools.
const (
	// statusDue means the bill's current due date has not been paid yet.
	statusDue = "due"
	// statusPaid means bills.paid_date already covers the current due date,
	// so next_due_date is the following one.
	statusPaid = "paid"
)

// billDueInfo is where a monthly bill stands on a given day.
type billDueInfo struct {
	// next is the next due date that still needs paying.
	next time.Time
	// status is statusPaid when the current due date (the first one on or
	// after today) is already covered by paid_date, otherwise statusDue.
	status string
	// paidFor is the due date paid_date covers, nil when never paid.
	paidFor *time.Time
}

// billDue computes a bill's next unpaid due date as of today (household-local
// midnight UTC, see householdToday).
//
// The current due date is due_day of this month or, once it has passed, of
// next month, with due_day clamped to the month's length (31 is Nov 30 and
// Feb 28 or 29). This is the same rule the worker's bill_due notification job
// uses (#4418), so agents and notifications agree. If bills.paid_date covers
// that due date or a later one (paidForDueDate, the rule bill_pay uses), the
// bill is paid and moves on to the due date after the one paid.
func billDue(today time.Time, dueDay int, paidDate *time.Time) billDueInfo {
	info := billDueInfo{next: nextDueDate(today, dueDay), status: statusDue}
	if paidDate == nil {
		return info
	}
	covered := paidForDueDate(*paidDate, dueDay)
	info.paidFor = &covered
	if covered.Before(info.next) {
		return info
	}
	info.status = statusPaid
	for !covered.Before(info.next) {
		info.next = dueDateInMonth(info.next.Year(), info.next.Month()+1, dueDay)
	}
	return info
}

// daysBetween returns the whole days from a to b (both midnight UTC dates).
func daysBetween(a, b time.Time) int {
	return int(b.Sub(a).Hours() / 24)
}

// billRow is a bill as the read tools load it. amount, due_day and
// is_autopay are pointers because API-created rows can leave them NULL.
type billRow struct {
	ID       uuid.UUID
	Name     string
	Amount   *float64
	DueDay   *int
	Category *string
	AutoPay  *bool
	PaidDate *time.Time
}

// billResult is a bill in list_bills and get_upcoming_bills output.
// next_due_date is the next unpaid due date (YYYY-MM-DD) and days_until_due
// counts from today in the household's timezone; both are null, and status
// is omitted, when the bill has no due_day.
type billResult struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Amount         *float64 `json:"amount,omitempty"`
	DueDay         *int     `json:"due_day"`
	Category       *string  `json:"category"`
	AutoPay        *bool    `json:"auto_pay"`
	NextDueDate    *string  `json:"next_due_date"`
	DaysUntilDue   *int     `json:"days_until_due"`
	Status         string   `json:"status,omitempty"`
	PaidDate       *string  `json:"paid_date"`
	PaidForDueDate *string  `json:"paid_for_due_date"`
}

// validDueDay reports whether a bill has a usable monthly due day.
func (r billRow) validDueDay() bool {
	return r.DueDay != nil && *r.DueDay >= 1 && *r.DueDay <= 31
}

// result builds the output for r as of today.
func (r billRow) result(today time.Time) billResult {
	b := billResult{
		ID: r.ID.String(), Name: r.Name, Amount: r.Amount, DueDay: r.DueDay,
		Category: r.Category, AutoPay: r.AutoPay,
	}
	if r.PaidDate != nil {
		s := r.PaidDate.Format(time.DateOnly)
		b.PaidDate = &s
	}
	if !r.validDueDay() {
		return b
	}
	info := billDue(today, *r.DueDay, r.PaidDate)
	next := info.next.Format(time.DateOnly)
	days := daysBetween(today, info.next)
	b.NextDueDate, b.DaysUntilDue, b.Status = &next, &days, info.status
	if info.paidFor != nil {
		s := info.paidFor.Format(time.DateOnly)
		b.PaidForDueDate = &s
	}
	return b
}

// scanBillRows reads id, name, amount, due_day, category, is_autopay and
// paid_date rows.
func scanBillRows(rows pgx.Rows) ([]billRow, error) {
	defer rows.Close()
	var out []billRow
	for rows.Next() {
		var r billRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Amount, &r.DueDay, &r.Category, &r.AutoPay, &r.PaidDate); err != nil {
			return nil, fmt.Errorf("scan bill: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bills: %w", err)
	}
	return out, nil
}

// NewListBillsTool creates the list_bills MCP tool.
// It returns all bills for the household, optionally filtered by category,
// each with its next unpaid due date and paid status.
func NewListBillsTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("list_bills",
		mcp.WithDescription("List bills for the household, optionally filtered by category, with each bill's next unpaid due date and paid status"),
		mcp.WithString("category",
			mcp.Description("Optional category filter (e.g. Mortgage, Utilities, Subscription)"),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		args := req.GetArguments()
		category, _ := args["category"].(string)

		today, err := householdToday(ctx, pool, claims.HouseholdID)
		if err != nil {
			return nil, err
		}

		rows, err := pool.Query(ctx,
			`SELECT id, name, amount, due_day, category, is_autopay, paid_date
			 FROM bills
			 WHERE household_id = $1 AND ($2 = '' OR category = $2)
			 ORDER BY due_day, name`,
			claims.HouseholdID, category,
		)
		if err != nil {
			return nil, fmt.Errorf("list bills: %w", err)
		}
		loaded, err := scanBillRows(rows)
		if err != nil {
			return nil, err
		}

		bills := make([]billResult, 0, len(loaded))
		for _, r := range loaded {
			bills = append(bills, r.result(today))
		}

		result, _ := json.Marshal(bills)
		return mcp.NewToolResultText(string(result)), nil
	}

	return "list_bills", tool, handler
}

// NewGetUpcomingBillsTool creates the get_upcoming_bills MCP tool.
// It returns bills whose next unpaid due date falls within
// [today, today + days] (today in the household's timezone), sorted by that
// date. A bill already paid for its current due date is listed under the
// following due date, if that is inside the window.
func NewGetUpcomingBillsTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("get_upcoming_bills",
		mcp.WithDescription("Get bills with an unpaid due date within the next N days (today included); paid bills show their next due date"),
		mcp.WithNumber("days",
			mcp.Description("Number of days to look ahead (default 7)"),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		// Parse days parameter, default 7.
		days := 7
		args := req.GetArguments()
		if daysVal, ok := args["days"].(float64); ok {
			days = int(daysVal)
		}
		if days < 1 {
			days = 1
		}

		today, err := householdToday(ctx, pool, claims.HouseholdID)
		if err != nil {
			return nil, err
		}

		// Due dates are computed in Go (billDue) so month ends clamp and
		// paid_date is respected the same way as bill_pay.
		rows, err := pool.Query(ctx,
			`SELECT id, name, amount, due_day, category, is_autopay, paid_date
			 FROM bills
			 WHERE household_id = $1 AND due_day BETWEEN 1 AND 31`,
			claims.HouseholdID,
		)
		if err != nil {
			return nil, fmt.Errorf("get upcoming bills: %w", err)
		}
		loaded, err := scanBillRows(rows)
		if err != nil {
			return nil, err
		}

		bills := []billResult{}
		for _, r := range loaded {
			b := r.result(today)
			if b.DaysUntilDue == nil || *b.DaysUntilDue < 0 || *b.DaysUntilDue > days {
				continue
			}
			bills = append(bills, b)
		}
		sort.SliceStable(bills, func(i, j int) bool {
			if *bills[i].DaysUntilDue != *bills[j].DaysUntilDue {
				return *bills[i].DaysUntilDue < *bills[j].DaysUntilDue
			}
			return bills[i].Name < bills[j].Name
		})

		result, _ := json.Marshal(bills)
		return mcp.NewToolResultText(string(result)), nil
	}

	return "get_upcoming_bills", tool, handler
}

// NewGetBillSummaryTool creates the get_bill_summary MCP tool.
// It aggregates all recurring bills by category with total amounts.
func NewGetBillSummaryTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("get_bill_summary",
		mcp.WithDescription("Aggregate all recurring bills by category with total amounts"),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		type categorySummary struct {
			Category string  `json:"category"`
			Count    int     `json:"count"`
			Total    float64 `json:"total"`
		}

		rows, err := pool.Query(ctx,
			`SELECT COALESCE(category, 'Uncategorized') AS category,
			        COUNT(*) AS count,
			        COALESCE(SUM(amount), 0) AS total
			 FROM bills
			 WHERE household_id = $1
			 GROUP BY category
			 ORDER BY total DESC`,
			claims.HouseholdID,
		)
		if err != nil {
			return nil, fmt.Errorf("get bill summary: %w", err)
		}
		defer rows.Close()

		var categories []categorySummary
		grandTotal := 0.0
		for rows.Next() {
			var cs categorySummary
			if err := rows.Scan(&cs.Category, &cs.Count, &cs.Total); err != nil {
				return nil, fmt.Errorf("scan category summary: %w", err)
			}
			grandTotal += cs.Total
			categories = append(categories, cs)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate bill summary: %w", err)
		}

		if categories == nil {
			categories = []categorySummary{}
		}

		result, _ := json.Marshal(map[string]interface{}{
			"categories":  categories,
			"grand_total": grandTotal,
		})
		return mcp.NewToolResultText(string(result)), nil
	}

	return "get_bill_summary", tool, handler
}
