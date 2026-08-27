// Package bill provides MCP tools for managing household bills.
// All operations are scoped to the authenticated household via JWT claims.
package bill

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
)

// NewListBillsTool creates the list_bills MCP tool.
// It returns all bills for the household, optionally filtered by category.
func NewListBillsTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("list_bills",
		mcp.WithDescription("List bills for the household, optionally filtered by category"),
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

		var rows pgx.Rows
		var err error

		if category != "" {
			rows, err = pool.Query(ctx,
				`SELECT id, name, amount, due_day, category, is_autopay
				 FROM bills
				 WHERE household_id = $1 AND category = $2
				 ORDER BY due_day, name`,
				claims.HouseholdID, category,
			)
		} else {
			rows, err = pool.Query(ctx,
				`SELECT id, name, amount, due_day, category, is_autopay
				 FROM bills
				 WHERE household_id = $1
				 ORDER BY due_day, name`,
				claims.HouseholdID,
			)
		}
		if err != nil {
			return nil, fmt.Errorf("list bills: %w", err)
		}
		defer rows.Close()

		type billResult struct {
			ID       uuid.UUID `json:"id"`
			Name     string    `json:"name"`
			Amount   *float64  `json:"amount,omitempty"`
			DueDay   *int      `json:"due_day"`
			Category *string   `json:"category"`
			AutoPay  *bool     `json:"auto_pay"`
		}

		var bills []billResult
		for rows.Next() {
			var b billResult
			if err := rows.Scan(&b.ID, &b.Name, &b.Amount, &b.DueDay, &b.Category, &b.AutoPay); err != nil {
				return nil, fmt.Errorf("scan bill: %w", err)
			}
			bills = append(bills, b)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate bills: %w", err)
		}

		if bills == nil {
			bills = []billResult{}
		}

		result, _ := json.Marshal(bills)
		return mcp.NewToolResultText(string(result)), nil
	}

	return "list_bills", tool, handler
}

// NewGetUpcomingBillsTool creates the get_upcoming_bills MCP tool.
// It returns bills with a due_day within the next N days from the current date.
// Results are sorted by next due date with amount and days_until_due.
func NewGetUpcomingBillsTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("get_upcoming_bills",
		mcp.WithDescription("Get bills due within the next N days"),
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

		// Use PostgreSQL date arithmetic to compute next due date from due_day.
		rows, err := pool.Query(ctx,
			`SELECT id, name, amount, due_day, category, is_autopay,
			        CASE
			          WHEN EXTRACT(DAY FROM CURRENT_DATE) <= due_day THEN
			            DATE_TRUNC('month', CURRENT_DATE)::date + (due_day - 1)
			          ELSE
			            DATE_TRUNC('month', CURRENT_DATE + INTERVAL '1 month')::date + (due_day - 1)
			        END AS next_due_date,
			        CASE
			          WHEN EXTRACT(DAY FROM CURRENT_DATE) <= due_day THEN
			            due_day - EXTRACT(DAY FROM CURRENT_DATE)::int
			          ELSE
			            (EXTRACT(DAY FROM DATE_TRUNC('month', CURRENT_DATE) + INTERVAL '1 month - 1 day')::int -
			             EXTRACT(DAY FROM CURRENT_DATE)::int + due_day)
			        END AS days_until_due
			 FROM bills
			 WHERE household_id = $1
			   AND due_day IS NOT NULL
			   AND (
			     CASE
			       WHEN EXTRACT(DAY FROM CURRENT_DATE) <= due_day THEN
			         DATE_TRUNC('month', CURRENT_DATE)::date + (due_day - 1)
			       ELSE
			         DATE_TRUNC('month', CURRENT_DATE + INTERVAL '1 month')::date + (due_day - 1)
			     END
			   ) BETWEEN CURRENT_DATE AND CURRENT_DATE + $2::int
			 ORDER BY next_due_date`,
			claims.HouseholdID, days,
		)
		if err != nil {
			return nil, fmt.Errorf("get upcoming bills: %w", err)
		}
		defer rows.Close()

		type upcomingBillResult struct {
			ID           uuid.UUID `json:"id"`
			Name         string    `json:"name"`
			Amount       *float64  `json:"amount,omitempty"`
			DueDay       *int      `json:"due_day"`
			Category     *string   `json:"category"`
			AutoPay      *bool     `json:"auto_pay"`
			NextDueDate  string    `json:"next_due_date"`
			DaysUntilDue int       `json:"days_until_due"`
		}

		var bills []upcomingBillResult
		for rows.Next() {
			var b upcomingBillResult
			var nextDueDate time.Time
			if err := rows.Scan(&b.ID, &b.Name, &b.Amount, &b.DueDay, &b.Category, &b.AutoPay,
				&nextDueDate, &b.DaysUntilDue); err != nil {
				return nil, fmt.Errorf("scan upcoming bill: %w", err)
			}
			b.NextDueDate = nextDueDate.Format("2006-01-02")
			bills = append(bills, b)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate upcoming bills: %w", err)
		}

		if bills == nil {
			bills = []upcomingBillResult{}
		}

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