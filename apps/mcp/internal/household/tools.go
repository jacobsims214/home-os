// Package household provides MCP tools for querying household and membership
// data. All queries are scoped to the authenticated user's household from the
// JWT claims.
package household

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// householdInfo is the response shape for get_household and the data of a
// family_update result. ID is a string (not uuid.UUID) so the outputSchema
// inferred by jsonschema-go matches the UUID string on the wire.
type householdInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Timezone    string `json:"timezone" jsonschema_description:"IANA timezone, e.g. America/New_York"`
	MemberCount int    `json:"member_count"`
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
// timezone and member count.
func NewGetHouseholdTool() mcp.Tool {
	return mcp.NewTool("get_household",
		mcp.WithDescription("Get the current household's id, name, timezone (IANA), and member count"),
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
			`SELECT id::text, name, timezone FROM households WHERE id = $1`,
			claims.HouseholdID,
		).Scan(&info.ID, &info.Name, &info.Timezone)
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

// maxHouseholdNameRunes matches households.name VARCHAR(255).
const maxHouseholdNameRunes = 255

// canManageFamily lists the membership roles allowed to change the family's
// settings with family_update.
var canManageFamily = map[string]bool{"owner": true, "family_manager": true}

// NewFamilyUpdateTool creates the family_update MCP tool (catalog v2 name;
// the Stage 1 working name was update_household). It changes the
// household's name and/or timezone. Only owners and family managers may call
// it; the role is read from memberships, not trusted from the token.
func NewFamilyUpdateTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("family_update",
		mcp.WithDescription("Change the family's name or timezone (IANA, e.g. America/New_York). The timezone controls how every date and weekday is shown."),
		mcp.WithString("name",
			mcp.Description("New family name, e.g. \"The Sims Family\" (max 255 characters)"),
		),
		mcp.WithString("timezone",
			mcp.Description("IANA timezone name, e.g. America/New_York or Europe/London"),
		),
		mcp.WithTitleAnnotation("Update family"),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.Schema[householdInfo](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return respond.Fail(respond.Error{
				Message: "You are not signed in to a household.",
				Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
			}), nil
		}

		args := req.GetArguments()
		rawName, _ := args["name"].(string)
		rawTZ, _ := args["timezone"].(string)
		// Small models send "" for unused optional params, so empty
		// (after trimming) means "not given".
		name := strings.TrimSpace(rawName)
		tz := strings.TrimSpace(rawTZ)

		tzExample := map[string]any{"timezone": "America/New_York"}
		if name == "" && tz == "" {
			return respond.Invalid("", "Nothing to change: pass name or timezone.",
				"Pass timezone as an IANA name to set the family's timezone, or name to rename the family.",
				tzExample), nil
		}
		if n := utf8.RuneCountInString(name); n > maxHouseholdNameRunes {
			return respond.Invalid("name",
				fmt.Sprintf("name is %d characters; the limit is %d.", n, maxHouseholdNameRunes),
				"Shorten the family name.",
				map[string]any{"name": "The Sims Family"}), nil
		}
		if tz != "" {
			canonical, ok := validTimezone(tz)
			if !ok {
				return respond.Invalid("timezone",
					fmt.Sprintf("timezone %q is not an IANA timezone name.", tz),
					"Pass an IANA name like America/New_York, America/Chicago, America/Los_Angeles or Europe/London (not an abbreviation like EST or an offset like -05:00).",
					tzExample), nil
			}
			tz = canonical
		}

		var role string
		err := pool.QueryRow(ctx,
			`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
			claims.HouseholdID, claims.UserID,
		).Scan(&role)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return respond.Failed("check your role", err), nil
		}
		if !canManageFamily[role] {
			return respond.Fail(respond.Error{
				Message: "Only the family's owner or a family manager can change the family's name or timezone.",
				Fix:     "Ask an owner or family manager to make this change; retrying will not help.",
			}), nil
		}

		var info householdInfo
		err = pool.QueryRow(ctx,
			`UPDATE households SET
				name = CASE WHEN $2::text <> '' THEN $2::text ELSE name END,
				timezone = CASE WHEN $3::text <> '' THEN $3::text ELSE timezone END,
				updated_at = NOW()
			WHERE id = $1
			RETURNING id::text, name, timezone,
				(SELECT COUNT(*) FROM memberships WHERE household_id = $1)::int`,
			claims.HouseholdID, name, tz,
		).Scan(&info.ID, &info.Name, &info.Timezone, &info.MemberCount)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("household", claims.HouseholdID,
				"Tell the user to reconnect Home OS; retrying will not help."), nil
		}
		if err != nil {
			return respond.Failed("update the family", err), nil
		}

		var changed []string
		if name != "" {
			changed = append(changed, fmt.Sprintf("name %q", info.Name))
		}
		if tz != "" {
			changed = append(changed, "timezone "+info.Timezone)
		}
		return respond.OK(
			fmt.Sprintf("Updated the family: %s.", strings.Join(changed, ", ")),
			info,
			fmt.Sprintf("Dates and weekdays now show in %s.", info.Timezone),
		), nil
	}

	return tool.Name, tool, handler
}

// validTimezone reports whether tz is a loadable IANA zone name and returns
// the name to store. "Local" is rejected: it means the server's zone, not a
// real place. time.LoadLocation("") would also mean UTC, but callers never
// pass an empty name here.
func validTimezone(tz string) (string, bool) {
	if tz == "" || tz == "Local" {
		return "", false
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return "", false
	}
	return loc.String(), true
}
