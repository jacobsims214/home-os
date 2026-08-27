// Package property provides MCP tools for managing household properties.
// Properties represent physical locations (homes, rentals) that belong to a
// household. Rooms are organized under properties. Every operation is scoped
// to the household_id extracted from the JWT claims.
package property

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

// Tools holds the database pool and provides property MCP tool handlers.
type Tools struct {
	pool *pgxpool.Pool
}

// NewTools creates a new property Tools instance.
func NewTools(pool *pgxpool.Pool) *Tools {
	return &Tools{pool: pool}
}

// Property represents a property for JSON serialization in MCP responses.
type Property struct {
	ID              string   `json:"id"`
	HouseholdID     string   `json:"household_id"`
	Name            string   `json:"name"`
	Address         *string  `json:"address,omitempty"`
	PropertyType    *string  `json:"property_type,omitempty"`
	PurchasePrice   *float64 `json:"purchase_price,omitempty"`
	PurchaseDate    *string  `json:"purchase_date,omitempty"`
	CurrentValue    *float64 `json:"current_value,omitempty"`
	MortgageAmount  *float64 `json:"mortgage_amount,omitempty"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

// Room represents a room for JSON serialization in MCP responses.
type Room struct {
	ID         string `json:"id"`
	PropertyID string `json:"property_id"`
	Name       string `json:"name"`
	Floor      *int   `json:"floor,omitempty"`
	CreatedAt  string `json:"created_at"`
}

// PropertyDetail is a property with its nested rooms array.
type PropertyDetail struct {
	Property
	Rooms []Room `json:"rooms"`
}

// --- Tool definitions ---

// ListPropertiesTool returns the tool definition for listing properties.
func (t *Tools) ListPropertiesTool() mcp.Tool {
	return mcp.NewTool("list_properties",
		mcp.WithDescription("List all properties belonging to the household. Returns financial fields including purchase_price, purchase_date, current_value, and mortgage_amount."),
	)
}

// GetPropertyTool returns the tool definition for getting a single property.
func (t *Tools) GetPropertyTool() mcp.Tool {
	return mcp.NewTool("get_property",
		mcp.WithDescription("Get details of a single property by its ID, including its rooms and financial fields (purchase_price, purchase_date, current_value, mortgage_amount)."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The property ID"),
		),
	)
}

// --- Handler implementations ---

// HandleListProperties handles the list_properties tool call.
func (t *Tools) HandleListProperties(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	claims := server.ClaimsFromContext(ctx)
	if claims == nil {
		return mcp.NewToolResultError("unauthorized: no claims in context"), nil
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid household_id in claims: %v", err)), nil
	}

	rows, err := t.pool.Query(ctx, `
		SELECT id, household_id, name, address, property_type, purchase_price, purchase_date, current_value, mortgage_amount, created_at, updated_at
		FROM properties
		WHERE household_id = $1
		ORDER BY name`, householdID)
	if err != nil {
		return nil, fmt.Errorf("list properties: %w", err)
	}
	defer rows.Close()

	props, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[propertyRow])
	if err != nil {
		return nil, fmt.Errorf("collect properties: %w", err)
	}

	result := make([]Property, 0, len(props))
	for _, p := range props {
		result = append(result, propertyRowToResponse(p))
	}

	payload, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal properties: %w", err)
	}

	return mcp.NewToolResultText(string(payload)), nil
}

// HandleGetProperty handles the get_property tool call.
// Returns the property with a nested rooms array.
func (t *Tools) HandleGetProperty(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	claims := server.ClaimsFromContext(ctx)
	if claims == nil {
		return mcp.NewToolResultError("unauthorized: no claims in context"), nil
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid household_id in claims: %v", err)), nil
	}

	propertyIDStr, err := req.RequireString("id")
	if err != nil {
		return mcp.NewToolResultError("id is required"), nil
	}
	propertyID, err := uuid.Parse(propertyIDStr)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid property id: %v", err)), nil
	}

	// Fetch the property.
	propRows, err := t.pool.Query(ctx, `
		SELECT id, household_id, name, address, property_type, purchase_price, purchase_date, current_value, mortgage_amount, created_at, updated_at
		FROM properties
		WHERE id = $1 AND household_id = $2`, propertyID, householdID)
	if err != nil {
		return nil, fmt.Errorf("get property: %w", err)
	}

	prop, err := pgx.CollectOneRow(propRows, pgx.RowToAddrOfStructByPos[propertyRow])
	propRows.Close()
	if err != nil {
		if err == pgx.ErrNoRows {
			return mcp.NewToolResultText(`null`), nil
		}
		return nil, fmt.Errorf("collect property: %w", err)
	}

	// Fetch rooms for this property.
	roomRows, err := t.pool.Query(ctx, `
		SELECT id, property_id, name, floor, created_at
		FROM rooms
		WHERE property_id = $1
		ORDER BY floor, name`, propertyID)
	if err != nil {
		return nil, fmt.Errorf("list rooms: %w", err)
	}
	defer roomRows.Close()

	rooms, err := pgx.CollectRows(roomRows, pgx.RowToAddrOfStructByPos[roomRow])
	if err != nil {
		return nil, fmt.Errorf("collect rooms: %w", err)
	}

	detail := PropertyDetail{
		Property: propertyRowToResponse(prop),
		Rooms:    make([]Room, 0, len(rooms)),
	}
	for _, r := range rooms {
		detail.Rooms = append(detail.Rooms, roomRowToResponse(r))
	}

	payload, err := json.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("marshal property detail: %w", err)
	}

	return mcp.NewToolResultText(string(payload)), nil
}

// --- Internal types and helpers ---

// propertyRow is the database row scan target for properties.
type propertyRow struct {
	ID             uuid.UUID
	HouseholdID    uuid.UUID
	Name           string
	Address        *string
	PropertyType   *string
	PurchasePrice  *float64
	PurchaseDate   *time.Time
	CurrentValue   *float64
	MortgageAmount *float64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// roomRow is the database row scan target for rooms.
type roomRow struct {
	ID         uuid.UUID
	PropertyID uuid.UUID
	Name       string
	Floor      *int
	CreatedAt  time.Time
}

func propertyRowToResponse(p *propertyRow) Property {
	r := Property{
		ID:            p.ID.String(),
		HouseholdID:   p.HouseholdID.String(),
		Name:          p.Name,
		Address:       p.Address,
		PropertyType:  p.PropertyType,
		PurchasePrice: p.PurchasePrice,
		CurrentValue:  p.CurrentValue,
		MortgageAmount: p.MortgageAmount,
		CreatedAt:     p.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     p.UpdatedAt.Format(time.RFC3339),
	}
	if p.PurchaseDate != nil {
		s := p.PurchaseDate.Format("2006-01-02")
		r.PurchaseDate = &s
	}
	return r
}

func roomRowToResponse(r *roomRow) Room {
	return Room{
		ID:         r.ID.String(),
		PropertyID: r.PropertyID.String(),
		Name:       r.Name,
		Floor:      r.Floor,
		CreatedAt:  r.CreatedAt.Format(time.RFC3339),
	}
}
