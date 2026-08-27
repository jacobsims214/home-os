// Package pet provides MCP tools for managing pet records.
// All tools are scoped to the caller's household via JWT claims.
package pet

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

// Pet represents a row from the pets table, including all columns
// returned by the API's pet model.
type Pet struct {
	ID          uuid.UUID  `json:"id"`
	HouseholdID uuid.UUID  `json:"household_id"`
	Name        string     `json:"name"`
	Species     *string    `json:"species,omitempty"`
	Breed       *string    `json:"breed,omitempty"`
	DateOfBirth *time.Time `json:"date_of_birth,omitempty"`
	VetName     *string    `json:"vet_name,omitempty"`
	VetPhone    *string    `json:"vet_phone,omitempty"`
	Notes       *string    `json:"notes,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// NewListTool returns the list_pets tool definition.
func NewListTool() mcp.Tool {
	return mcp.NewTool("list_pets",
		mcp.WithDescription("List all pets for the current household. Returns id, name, species, breed, date_of_birth, vet_name, vet_phone."),
		mcp.WithReadOnlyHintAnnotation(true),
	)
}

// HandleList handles the list_pets tool call.
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
			`SELECT id, household_id, name, species, breed, date_of_birth, vet_name, vet_phone, notes, created_at, updated_at
			 FROM pets WHERE household_id = $1 ORDER BY created_at DESC`,
			householdID,
		)
		if err != nil {
			return nil, fmt.Errorf("list pets: %w", err)
		}
		defer rows.Close()

		pets, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Pet])
		if err != nil {
			return nil, fmt.Errorf("collect pets: %w", err)
		}

		if pets == nil {
			pets = []*Pet{}
		}

		data, err := json.Marshal(pets)
		if err != nil {
			return nil, fmt.Errorf("marshal pets: %w", err)
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// NewGetTool returns the get_pet tool definition.
func NewGetTool() mcp.Tool {
	return mcp.NewTool("get_pet",
		mcp.WithDescription("Get details for a specific pet by ID."),
		mcp.WithString("id",
			mcp.Description("The pet UUID"),
			mcp.Required(),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	)
}

// HandleGet handles the get_pet tool call.
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
			`SELECT id, household_id, name, species, breed, date_of_birth, vet_name, vet_phone, notes, created_at, updated_at
			 FROM pets WHERE id = $1 AND household_id = $2`,
			id, householdID,
		)
		if err != nil {
			return nil, fmt.Errorf("get pet: %w", err)
		}
		defer rows.Close()

		pet, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Pet])
		if err != nil {
			if err == pgx.ErrNoRows {
				return mcp.NewToolResultText(`{"error":"pet not found"}`), nil
			}
			return nil, fmt.Errorf("collect pet: %w", err)
		}

		data, err := json.Marshal(pet)
		if err != nil {
			return nil, fmt.Errorf("marshal pet: %w", err)
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}