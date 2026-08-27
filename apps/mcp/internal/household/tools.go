// Package household provides MCP tools for querying household and membership
// data. All queries are scoped to the authenticated user's household from the
// JWT claims.
package household

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
)

// householdInfo is the response shape for the get_household tool.
type householdInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MemberCount  int    `json:"member_count"`
}

// memberRow is a single member in the list_members response.
type memberRow struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

// NewGetHouseholdTool creates the "get_household" tool definition.
// It takes no parameters and returns the current household's id, name,
// and member count.
func NewGetHouseholdTool() mcp.Tool {
	return mcp.NewTool("get_household",
		mcp.WithDescription("Get the current household's id, name, and member count"),
	)
}

// HandleGetHousehold handles the get_household tool call.
// It queries the households table using the household_id from the JWT claims
// and counts memberships.
func HandleGetHousehold(pool *pgxpool.Pool) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("unauthorized: missing claims"), nil
		}

		var info householdInfo
		err := pool.QueryRow(ctx,
			`SELECT id, name FROM households WHERE id = $1`,
			claims.HouseholdID,
		).Scan(&info.ID, &info.Name)
		if err != nil {
			return nil, fmt.Errorf("get household: %w", err)
		}

		err = pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM memberships WHERE household_id = $1`,
			claims.HouseholdID,
		).Scan(&info.MemberCount)
		if err != nil {
			return nil, fmt.Errorf("count members: %w", err)
		}

		data, err := json.Marshal(info)
		if err != nil {
			return nil, fmt.Errorf("marshal household: %w", err)
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// NewListMembersTool creates the "list_members" tool definition.
// It takes no parameters and returns all members of the current household.
func NewListMembersTool() mcp.Tool {
	return mcp.NewTool("list_members",
		mcp.WithDescription("List all members of the current household with their id, name, email, and role"),
	)
}

// HandleListMembers handles the list_members tool call.
// It joins memberships with users to return member details for the
// authenticated user's household.
func HandleListMembers(pool *pgxpool.Pool) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("unauthorized: missing claims"), nil
		}

		rows, err := pool.Query(ctx,
			`SELECT u.id, u.name, u.email, m.role
			 FROM memberships m
			 JOIN users u ON u.id = m.user_id
			 WHERE m.household_id = $1
			 ORDER BY m.created_at`,
			claims.HouseholdID,
		)
		if err != nil {
			return nil, fmt.Errorf("list members: %w", err)
		}
		defer rows.Close()

		var members []memberRow
		for rows.Next() {
			var m memberRow
			if err := rows.Scan(&m.ID, &m.Name, &m.Email, &m.Role); err != nil {
				return nil, fmt.Errorf("scan member: %w", err)
			}
			members = append(members, m)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate members: %w", err)
		}
		if members == nil {
			members = []memberRow{}
		}

		data, err := json.Marshal(members)
		if err != nil {
			return nil, fmt.Errorf("marshal members: %w", err)
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}