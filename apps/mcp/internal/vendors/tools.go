// Package vendor provides MCP tools for managing vendor records.
// All tools are scoped to the caller's household via JWT claims.
package vendors

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

// Vendor represents a row from the vendors table.
// Struct field order must match SELECT column order for pgx.RowToAddrOfStructByPos.
type Vendor struct {
	ID          uuid.UUID  `json:"id"`
	HouseholdID uuid.UUID  `json:"household_id"`
	PropertyID  *uuid.UUID `json:"property_id,omitempty"`
	Name        string     `json:"name"`
	Specialty   *string    `json:"specialty,omitempty"`
	Phone       *string    `json:"phone,omitempty"`
	Email       *string    `json:"email,omitempty"`
	Website     *string    `json:"website,omitempty"`
	Notes       *string    `json:"notes,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// NewListTool returns the list_vendors tool definition.
func NewListTool() mcp.Tool {
	return mcp.NewTool("list_vendors",
		mcp.WithDescription("List all vendors for the current household. Returns id, household_id, property_id, name, specialty, phone, email, website, notes, created_at, updated_at."),
		mcp.WithReadOnlyHintAnnotation(true),
	)
}

// HandleList handles the list_vendors tool call.
func HandleList(pool *pgxpool.Pool) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		householdID, err := uuid.Parse(claims.HouseholdID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid household ID: %v", err)), nil
		}

		rows, err := pool.Query(ctx,
			`SELECT id, household_id, property_id, name, specialty, phone, email, website, notes, created_at, updated_at
			 FROM vendors WHERE household_id = $1 ORDER BY name ASC`,
			householdID,
		)
		if err != nil {
			return nil, fmt.Errorf("list vendors: %w", err)
		}
		defer rows.Close()

		vendors, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Vendor])
		if err != nil {
			return nil, fmt.Errorf("collect vendors: %w", err)
		}

		if vendors == nil {
			vendors = []*Vendor{}
		}

		data, err := json.Marshal(vendors)
		if err != nil {
			return nil, fmt.Errorf("marshal vendors: %w", err)
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// NewGetTool returns the get_vendor tool definition.
func NewGetTool() mcp.Tool {
	return mcp.NewTool("get_vendor",
		mcp.WithDescription("Get details for a specific vendor by ID."),
		mcp.WithString("id",
			mcp.Description("The vendor UUID"),
			mcp.Required(),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	)
}

// HandleGet handles the get_vendor tool call.
func HandleGet(pool *pgxpool.Pool) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		householdID, err := uuid.Parse(claims.HouseholdID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid household ID: %v", err)), nil
		}

		idStr := req.GetString("id", "")
		if idStr == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		id, err := uuid.Parse(idStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid id: %v", err)), nil
		}

		rows, err := pool.Query(ctx,
			`SELECT id, household_id, property_id, name, specialty, phone, email, website, notes, created_at, updated_at
			 FROM vendors WHERE id = $1 AND household_id = $2`,
			id, householdID,
		)
		if err != nil {
			return nil, fmt.Errorf("get vendor: %w", err)
		}
		defer rows.Close()

		vendor, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Vendor])
		if err != nil {
			if err == pgx.ErrNoRows {
				return mcp.NewToolResultText(`{"error":"vendor not found"}`), nil
			}
			return nil, fmt.Errorf("collect vendor: %w", err)
		}

		data, err := json.Marshal(vendor)
		if err != nil {
			return nil, fmt.Errorf("marshal vendor: %w", err)
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}