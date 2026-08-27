// Package note provides MCP tools for managing notes in Home OS.
// Notes are polymorphic — they can be attached to any entity type
// (property, vehicle, pet, asset, vendor, bill, etc.) via entity_type
// and entity_id. All operations are scoped to the authenticated household.
package note

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
)

// NewListNotesTool creates the list_notes MCP tool.
// It returns id, title, content_preview (first 100 chars), created_at, and
// updated_at for the authenticated household, optionally filtered by
// entity_type and entity_id.
func NewListNotesTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("list_notes",
		mcp.WithDescription("List notes for the household, optionally filtered by entity type and entity ID"),
		mcp.WithString("entity_type",
			mcp.Description("Optional entity type filter (e.g. property, vehicle, pet)"),
		),
		mcp.WithString("entity_id",
			mcp.Description("Optional entity ID filter (UUID)"),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		args := req.GetArguments()
		entityType, _ := args["entity_type"].(string)
		entityIDStr, _ := args["entity_id"].(string)

		if (entityType == "") != (entityIDStr == "") {
			return mcp.NewToolResultText(`{"error":"entity_type and entity_id must be provided together"}`), nil
		}

		var rows pgx.Rows
		var err error

		if entityType != "" && entityIDStr != "" {
			entityID, parseErr := uuid.Parse(entityIDStr)
			if parseErr != nil {
				return mcp.NewToolResultText(fmt.Sprintf(`{"error":"invalid entity_id: %s"}`, parseErr.Error())), nil
			}
			rows, err = pool.Query(ctx,
				`SELECT id, title, body, created_at, updated_at
				 FROM notes
				 WHERE household_id = $1 AND entity_type = $2 AND entity_id = $3
				 ORDER BY created_at DESC`,
				claims.HouseholdID, entityType, entityID,
			)
		} else {
			rows, err = pool.Query(ctx,
				`SELECT id, title, body, created_at, updated_at
				 FROM notes
				 WHERE household_id = $1
				 ORDER BY created_at DESC`,
				claims.HouseholdID,
			)
		}
		if err != nil {
			return nil, fmt.Errorf("list notes: %w", err)
		}
		defer rows.Close()

		type noteResult struct {
			ID             uuid.UUID `json:"id"`
			Title          *string   `json:"title"`
			ContentPreview string    `json:"content_preview"`
			CreatedAt      time.Time `json:"created_at"`
			UpdatedAt      time.Time `json:"updated_at"`
		}

		var notes []noteResult
		for rows.Next() {
			var n noteResult
			var body string
			if err := rows.Scan(&n.ID, &n.Title, &body, &n.CreatedAt, &n.UpdatedAt); err != nil {
				return nil, fmt.Errorf("scan note: %w", err)
			}
			n.ContentPreview = truncate(body, 100)
			notes = append(notes, n)
		}

		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate notes: %w", err)
		}

		if notes == nil {
			notes = []noteResult{}
		}

		result, _ := json.Marshal(notes)
		return mcp.NewToolResultText(string(result)), nil
	}

	return "list_notes", tool, handler
}

// NewCreateNoteTool creates the create_note MCP tool.
// It inserts a new note with the given title and body, optionally
// associated with an entity via entity_type and entity_id.
func NewCreateNoteTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("create_note",
		mcp.WithDescription("Create a new note, optionally associated with an entity"),
		mcp.WithString("title",
			mcp.Description("Note title"),
			mcp.Required(),
		),
		mcp.WithString("content",
			mcp.Description("Note body content (markdown supported)"),
			mcp.Required(),
		),
		mcp.WithString("entity_type",
			mcp.Description("Optional entity type to associate the note with"),
		),
		mcp.WithString("entity_id",
			mcp.Description("Optional entity ID (UUID) to associate the note with"),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		args := req.GetArguments()
		title, _ := args["title"].(string)
		content, _ := args["content"].(string)
		entityType, _ := args["entity_type"].(string)
		entityIDStr, _ := args["entity_id"].(string)

		if title == "" || content == "" {
			return mcp.NewToolResultText(`{"error":"title and content are required"}`), nil
		}

		// Use defaults for entity_type/entity_id if not provided.
		// The notes table requires these columns, so we use empty string
		// and zero UUID as "standalone" defaults.
		if entityType == "" {
			entityType = ""
		}
		var entityID uuid.UUID
		if entityIDStr != "" {
			var parseErr error
			entityID, parseErr = uuid.Parse(entityIDStr)
			if parseErr != nil {
				return mcp.NewToolResultText(fmt.Sprintf(`{"error":"invalid entity_id: %s"}`, parseErr.Error())), nil
			}
		}

		var noteID uuid.UUID
		var createdAt, updatedAt time.Time
		err := pool.QueryRow(ctx,
			`INSERT INTO notes (household_id, entity_type, entity_id, title, body)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING id, created_at, updated_at`,
			claims.HouseholdID, entityType, entityID, title, content,
		).Scan(&noteID, &createdAt, &updatedAt)
		if err != nil {
			return nil, fmt.Errorf("create note: %w", err)
		}

		result, _ := json.Marshal(map[string]interface{}{
			"id":         noteID.String(),
			"title":      title,
			"created_at": createdAt,
			"updated_at": updatedAt,
		})
		return mcp.NewToolResultText(string(result)), nil
	}

	return "create_note", tool, handler
}

// truncate returns the first n characters of s, respecting Unicode boundaries.
// If s is shorter than n, it returns s unchanged.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	var count int
	for i := range s {
		if count >= n {
			return s[:i] + "..."
		}
		count++
	}
	return s
}