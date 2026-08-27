// Package finance provides MCP tools for household financial summaries.
// It aggregates data from multiple entity tables (properties, assets, vehicles,
// loans) into a consolidated net worth snapshot, scoped to the authenticated
// household.
package finance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
)

// FinancialSummary holds the aggregated financial data for a household.
type FinancialSummary struct {
	TotalPropertyValue  float64 `json:"total_property_value"`
	TotalAssetValue     float64 `json:"total_asset_value"`
	TotalVehicleValue   float64 `json:"total_vehicle_value"`
	TotalAssetsValue    float64 `json:"total_assets_value"`
	TotalLiabilities    float64 `json:"total_liabilities"`
	MortgageDebt        float64 `json:"mortgage_debt"`
	VehicleLoans        float64 `json:"vehicle_loans"`
	OtherLoans          float64 `json:"other_loans"`
	EstimatedNetWorth   float64 `json:"estimated_net_worth"`
}

// NewGetFinancialSummaryTool returns the tool definition for get_financial_summary.
func NewGetFinancialSummaryTool() mcp.Tool {
	return mcp.NewTool("get_financial_summary",
		mcp.WithDescription("Get a financial summary for the household including property, asset, and vehicle values, loan liabilities, and estimated net worth."),
	)
}

// HandleGetFinancialSummary handles the get_financial_summary tool call.
func HandleGetFinancialSummary(pool *pgxpool.Pool) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		householdID, err := uuid.Parse(claims.HouseholdID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid household ID: %v", err)), nil
		}

		// Total property value: SUM of current_value from properties table.
		var totalPropertyValue float64
		err = pool.QueryRow(ctx,
			`SELECT COALESCE(SUM(current_value), 0) FROM properties WHERE household_id = $1`,
			householdID,
		).Scan(&totalPropertyValue)
		if err != nil {
			return nil, fmt.Errorf("sum property values: %w", err)
		}

		// Total asset value: use current_value if set, otherwise purchase_price.
		var totalAssetValue float64
		err = pool.QueryRow(ctx, `
			SELECT COALESCE(SUM(current_value), 0) + COALESCE(SUM(purchase_price), 0)
			FROM assets
			WHERE household_id = $1 AND current_value IS NULL`,
			householdID,
		).Scan(&totalAssetValue)
		if err != nil {
			return nil, fmt.Errorf("sum asset values: %w", err)
		}

		// Total vehicle value: use current_value if set (vehicles don't have purchase_price).
		var totalVehicleValue float64
		err = pool.QueryRow(ctx,
			`SELECT COALESCE(SUM(current_value), 0) FROM vehicles WHERE household_id = $1`,
			householdID,
		).Scan(&totalVehicleValue)
		if err != nil {
			return nil, fmt.Errorf("sum vehicle values: %w", err)
		}

		// Total liabilities: SUM of remaining_balance from loans.
		var totalLiabilities float64
		err = pool.QueryRow(ctx,
			`SELECT COALESCE(SUM(remaining_balance), 0) FROM loans WHERE household_id = $1`,
			householdID,
		).Scan(&totalLiabilities)
		if err != nil {
			return nil, fmt.Errorf("sum liabilities: %w", err)
		}

		// Mortgage debt: loans where entity_type = 'property'.
		var mortgageDebt float64
		err = pool.QueryRow(ctx,
			`SELECT COALESCE(SUM(remaining_balance), 0) FROM loans WHERE household_id = $1 AND entity_type = 'property'`,
			householdID,
		).Scan(&mortgageDebt)
		if err != nil {
			return nil, fmt.Errorf("sum mortgage debt: %w", err)
		}

		// Vehicle loans: loans where entity_type = 'vehicle'.
		var vehicleLoans float64
		err = pool.QueryRow(ctx,
			`SELECT COALESCE(SUM(remaining_balance), 0) FROM loans WHERE household_id = $1 AND entity_type = 'vehicle'`,
			householdID,
		).Scan(&vehicleLoans)
		if err != nil {
			return nil, fmt.Errorf("sum vehicle loans: %w", err)
		}

		// Other loans: everything else.
		otherLoans := totalLiabilities - mortgageDebt - vehicleLoans
		if otherLoans < 0 {
			otherLoans = 0
		}

		// Total assets = property + asset + vehicle.
		totalAssetsValue := totalPropertyValue + totalAssetValue + totalVehicleValue

		// Estimated net worth.
		estimatedNetWorth := totalAssetsValue - totalLiabilities

		summary := FinancialSummary{
			TotalPropertyValue: totalPropertyValue,
			TotalAssetValue:    totalAssetValue,
			TotalVehicleValue:  totalVehicleValue,
			TotalAssetsValue:   totalAssetsValue,
			TotalLiabilities:   totalLiabilities,
			MortgageDebt:       mortgageDebt,
			VehicleLoans:       vehicleLoans,
			OtherLoans:         otherLoans,
			EstimatedNetWorth:  estimatedNetWorth,
		}

		data, err := json.Marshal(summary)
		if err != nil {
			return nil, fmt.Errorf("marshal financial summary: %w", err)
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}