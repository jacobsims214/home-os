// Package maintenance provides MCP tools for managing home maintenance tasks.
// All operations are scoped to the authenticated household via JWT claims.
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
)

// NewListMaintenanceTasksTool creates the list_maintenance_tasks MCP tool.
// It returns maintenance tasks for the household, optionally filtered by status
// and/or property_id.
func NewListMaintenanceTasksTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("list_maintenance_tasks",
		mcp.WithDescription("List maintenance tasks for the household, optionally filtered by status and/or property"),
		mcp.WithString("status",
			mcp.Description("Optional status filter: 'pending' or 'done'"),
		),
		mcp.WithString("property_id",
			mcp.Description("Optional property ID (UUID) filter"),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		args := req.GetArguments()
		status, _ := args["status"].(string)
		propertyIDStr, _ := args["property_id"].(string)

		// Validate status.
		if status != "" && status != "pending" && status != "done" {
			return mcp.NewToolResultText(`{"error":"status must be 'pending' or 'done'"}`), nil
		}

		// Build query with optional filters.
		query := `SELECT id, name, description, status, due_date, property_id, asset_id, completed_at, created_at
				  FROM maintenance_tasks
				  WHERE household_id = $1`
		queryArgs := []interface{}{claims.HouseholdID}
		argIdx := 2

		if status != "" {
			query += fmt.Sprintf(" AND status = $%d", argIdx)
			queryArgs = append(queryArgs, status)
			argIdx++
		}

		if propertyIDStr != "" {
			propertyID, err := uuid.Parse(propertyIDStr)
			if err != nil {
				return mcp.NewToolResultText(fmt.Sprintf(`{"error":"invalid property_id: %s"}`, err.Error())), nil
			}
			query += fmt.Sprintf(" AND property_id = $%d", argIdx)
			queryArgs = append(queryArgs, propertyID)
			argIdx++
		}

		query += " ORDER BY due_date ASC NULLS LAST, created_at DESC"

		rows, err := pool.Query(ctx, query, queryArgs...)
		if err != nil {
			return nil, fmt.Errorf("list maintenance tasks: %w", err)
		}
		defer rows.Close()

		type taskResult struct {
			ID          uuid.UUID  `json:"id"`
			Name        string     `json:"name"`
			Description *string    `json:"description"`
			Status      string     `json:"status"`
			DueDate     *string    `json:"due_date"`
			PropertyID  *uuid.UUID `json:"property_id"`
			AssetID     *uuid.UUID `json:"asset_id"`
			CompletedAt *string    `json:"completed_at"`
			CreatedAt   time.Time  `json:"created_at"`
		}

		var tasks []taskResult
		for rows.Next() {
			var t taskResult
			var dueDate, completedAt interface{}
			if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Status, &dueDate,
				&t.PropertyID, &t.AssetID, &completedAt, &t.CreatedAt); err != nil {
				return nil, fmt.Errorf("scan task: %w", err)
			}
			if dueDate != nil {
				if d, ok := dueDate.(time.Time); ok {
					s := d.Format("2006-01-02")
					t.DueDate = &s
				}
			}
			if completedAt != nil {
				if d, ok := completedAt.(time.Time); ok {
					s := d.Format(time.RFC3339)
					t.CompletedAt = &s
				}
			}
			tasks = append(tasks, t)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate maintenance tasks: %w", err)
		}

		if tasks == nil {
			tasks = []taskResult{}
		}

		result, _ := json.Marshal(tasks)
		return mcp.NewToolResultText(string(result)), nil
	}

	return "list_maintenance_tasks", tool, handler
}

// NewGetUpcomingMaintenanceTool creates the get_upcoming_maintenance MCP tool.
// It returns pending maintenance tasks with due_date within the next N days,
// sorted by due_date.
func NewGetUpcomingMaintenanceTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
tool := mcp.NewTool("get_upcoming_maintenance",
		mcp.WithDescription("Get pending maintenance tasks due within the next N days"),
		mcp.WithNumber("days",
			mcp.Description("Number of days to look ahead (default 30)"),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		days := 30
		args := req.GetArguments()
		if daysVal, ok := args["days"].(float64); ok {
			days = int(daysVal)
		}
		if days < 1 {
			days = 1
		}

		rows, err := pool.Query(ctx,
			`SELECT id, name, description, status, due_date, property_id, asset_id, completed_at, created_at
			 FROM maintenance_tasks
			 WHERE household_id = $1
			   AND status = 'pending'
			   AND due_date BETWEEN CURRENT_DATE AND CURRENT_DATE + $2::int
			 ORDER BY due_date`,
			claims.HouseholdID, days,
		)
		if err != nil {
			return nil, fmt.Errorf("get upcoming maintenance: %w", err)
		}
		defer rows.Close()

		type upcomingTaskResult struct {
			ID          uuid.UUID  `json:"id"`
			Name        string     `json:"name"`
			Description *string    `json:"description"`
			Status      string     `json:"status"`
			DueDate     *string    `json:"due_date"`
			PropertyID  *uuid.UUID `json:"property_id"`
			AssetID     *uuid.UUID `json:"asset_id"`
			CreatedAt   time.Time  `json:"created_at"`
		}

		var tasks []upcomingTaskResult
		for rows.Next() {
			var t upcomingTaskResult
			var dueDate interface{}
			if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Status, &dueDate,
				&t.PropertyID, &t.AssetID, nil, &t.CreatedAt); err != nil {
				return nil, fmt.Errorf("scan upcoming task: %w", err)
			}
			if dueDate != nil {
				if d, ok := dueDate.(time.Time); ok {
					s := d.Format("2006-01-02")
					t.DueDate = &s
				}
			}
			tasks = append(tasks, t)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate upcoming maintenance: %w", err)
		}

		if tasks == nil {
			tasks = []upcomingTaskResult{}
		}

		result, _ := json.Marshal(tasks)
		return mcp.NewToolResultText(string(result)), nil
	}

	return "get_upcoming_maintenance", tool, handler
}

// NewCreateMaintenanceTaskTool creates the create_maintenance_task MCP tool.
// It inserts a new maintenance task scoped to the household. If property_id is
// provided, it validates that the property belongs to the household.
func NewCreateMaintenanceTaskTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("create_maintenance_task",
		mcp.WithDescription("Create a new maintenance task"),
		mcp.WithString("name",
			mcp.Description("Task name (required)"),
			mcp.Required(),
		),
		mcp.WithString("description",
			mcp.Description("Optional task description"),
		),
		mcp.WithString("property_id",
			mcp.Description("Optional property ID (UUID) — must belong to the household"),
		),
		mcp.WithString("asset_id",
			mcp.Description("Optional asset ID (UUID)"),
		),
		mcp.WithString("due_date",
			mcp.Description("Optional due date (YYYY-MM-DD format)"),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		args := req.GetArguments()
		name, _ := args["name"].(string)
		description, _ := args["description"].(string)
		propertyIDStr, _ := args["property_id"].(string)
		assetIDStr, _ := args["asset_id"].(string)
		dueDateStr, _ := args["due_date"].(string)

		// Validate required name.
		if name == "" {
			return mcp.NewToolResultText(`{"error":"name is required"}`), nil
		}

		// Parse optional property_id and validate it belongs to the household.
		var propertyID *uuid.UUID
		if propertyIDStr != "" {
			pid, err := uuid.Parse(propertyIDStr)
			if err != nil {
				return mcp.NewToolResultText(fmt.Sprintf(`{"error":"invalid property_id: %s"}`, err.Error())), nil
			}
			// Verify property belongs to the household.
			var exists bool
			err = pool.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM properties WHERE id = $1 AND household_id = $2)`,
				pid, claims.HouseholdID,
			).Scan(&exists)
			if err != nil {
				return nil, fmt.Errorf("validate property: %w", err)
			}
			if !exists {
				return mcp.NewToolResultText(`{"error":"property_id does not belong to this household"}`), nil
			}
			propertyID = &pid
		}

		// Parse optional asset_id and validate it belongs to the household.
		var assetID *uuid.UUID
		if assetIDStr != "" {
			aid, err := uuid.Parse(assetIDStr)
			if err != nil {
				return mcp.NewToolResultText(fmt.Sprintf(`{"error":"invalid asset_id: %s"}`, err.Error())), nil
			}
			// Verify asset belongs to the household.
			var exists bool
			err = pool.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM assets WHERE id = $1 AND household_id = $2)`,
				aid, claims.HouseholdID,
			).Scan(&exists)
			if err != nil {
				return nil, fmt.Errorf("validate asset: %w", err)
			}
			if !exists {
				return mcp.NewToolResultText(`{"error":"asset_id does not belong to this household"}`), nil
			}
			assetID = &aid
		}

		// Parse optional due_date.
		var dueDate *time.Time
		if dueDateStr != "" {
			d, err := time.Parse("2006-01-02", dueDateStr)
			if err != nil {
				return mcp.NewToolResultText(fmt.Sprintf(`{"error":"invalid due_date: expected YYYY-MM-DD format, got %s"}`, dueDateStr)), nil
			}
			dueDate = &d
		}

		// Insert the task.
		var taskID uuid.UUID
		var createdAt time.Time
		err := pool.QueryRow(ctx,
			`INSERT INTO maintenance_tasks (household_id, property_id, asset_id, name, description, status, due_date)
			 VALUES ($1, $2, $3, $4, $5, 'pending', $6)
			 RETURNING id, created_at`,
			claims.HouseholdID, propertyID, assetID, name, nullIfEmpty(description), dueDate,
		).Scan(&taskID, &createdAt)
		if err != nil {
			return nil, fmt.Errorf("create maintenance task: %w", err)
		}

		result, _ := json.Marshal(map[string]interface{}{
			"id":          taskID.String(),
			"name":        name,
			"description": nullIfEmpty(description),
			"status":      "pending",
			"property_id": nullUUIDString(propertyID),
			"asset_id":    nullUUIDString(assetID),
			"due_date":    nullDateString(dueDate),
			"created_at":  createdAt.Format(time.RFC3339),
		})
		return mcp.NewToolResultText(string(result)), nil
	}

	return "create_maintenance_task", tool, handler
}

// nullIfEmpty returns nil if s is empty, otherwise returns s.
// Used for nullable text columns in INSERT statements.
func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// nullUUIDString returns the string representation of id, or nil if id is nil.
func nullUUIDString(id *uuid.UUID) interface{} {
	if id == nil {
		return nil
	}
	return id.String()
}

// nullDateString returns the formatted date string, or nil if d is nil.
func nullDateString(d *time.Time) interface{} {
	if d == nil {
		return nil
	}
	return d.Format("2006-01-02")
}