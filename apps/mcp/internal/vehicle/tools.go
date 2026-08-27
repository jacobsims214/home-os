// Package vehicle provides MCP tools for managing vehicle records.
// All tools are scoped to the caller's household via JWT claims.
package vehicle

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

// Vehicle represents a row from the vehicles table, including all columns
// returned by the API's vehicle model.
type Vehicle struct {
	ID               uuid.UUID  `json:"id"`
	HouseholdID      uuid.UUID  `json:"household_id"`
	Year             *int       `json:"year,omitempty"`
	Make             *string    `json:"make,omitempty"`
	Model            *string    `json:"model,omitempty"`
	VIN              *string    `json:"vin,omitempty"`
	LicensePlate     *string    `json:"license_plate,omitempty"`
	Color            *string    `json:"color,omitempty"`
	Notes            *string    `json:"notes,omitempty"`
	PurchasePrice    *float64   `json:"purchase_price,omitempty"`
	PurchaseDate     *time.Time `json:"purchase_date,omitempty"`
	CurrentValue     *float64   `json:"current_value,omitempty"`
	LoanAmount       *float64   `json:"loan_amount,omitempty"`
	InsuranceCost    *float64   `json:"insurance_cost,omitempty"`
	RegistrationCost *float64   `json:"registration_cost,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// NewListTool returns the list_vehicles tool definition.
func NewListTool() mcp.Tool {
	return mcp.NewTool("list_vehicles",
		mcp.WithDescription("List all vehicles for the current household. Returns id, year, make, model, vin, license_plate, color, purchase_price, purchase_date, current_value, loan_amount, insurance_cost, registration_cost."),
		mcp.WithReadOnlyHintAnnotation(true),
	)
}

// HandleList handles the list_vehicles tool call.
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
			`SELECT id, household_id, year, make, model, vin, license_plate, color, notes, purchase_price, purchase_date, current_value, loan_amount, insurance_cost, registration_cost, created_at, updated_at
			 FROM vehicles WHERE household_id = $1 ORDER BY created_at DESC`,
			householdID,
		)
		if err != nil {
			return nil, fmt.Errorf("list vehicles: %w", err)
		}
		defer rows.Close()

		vehicles, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Vehicle])
		if err != nil {
			return nil, fmt.Errorf("collect vehicles: %w", err)
		}

		if vehicles == nil {
			vehicles = []*Vehicle{}
		}

		data, err := json.Marshal(vehicles)
		if err != nil {
			return nil, fmt.Errorf("marshal vehicles: %w", err)
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// NewGetTool returns the get_vehicle tool definition.
func NewGetTool() mcp.Tool {
	return mcp.NewTool("get_vehicle",
		mcp.WithDescription("Get details for a specific vehicle by ID."),
		mcp.WithString("id",
			mcp.Description("The vehicle UUID"),
			mcp.Required(),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	)
}

// HandleGet handles the get_vehicle tool call.
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
			`SELECT id, household_id, year, make, model, vin, license_plate, color, notes, purchase_price, purchase_date, current_value, loan_amount, insurance_cost, registration_cost, created_at, updated_at
			 FROM vehicles WHERE id = $1 AND household_id = $2`,
			id, householdID,
		)
		if err != nil {
			return nil, fmt.Errorf("get vehicle: %w", err)
		}
		defer rows.Close()

		vehicle, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Vehicle])
		if err != nil {
			if err == pgx.ErrNoRows {
				return mcp.NewToolResultText(`{"error":"vehicle not found"}`), nil
			}
			return nil, fmt.Errorf("collect vehicle: %w", err)
		}

		data, err := json.Marshal(vehicle)
		if err != nil {
			return nil, fmt.Errorf("marshal vehicle: %w", err)
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}