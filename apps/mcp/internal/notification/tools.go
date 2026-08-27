// Package notification provides MCP tools for querying user notifications.
// It uses the pgx connection pool from the server and reads the authenticated
// user's claims from the request context to scope queries.
package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
)

// notificationRow represents a single notification row returned by the
// list_notifications tool. is_read is derived from read_at IS NOT NULL.
type notificationRow struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Body      *string   `json:"body"`
	IsRead    bool      `json:"is_read"`
	CreatedAt time.Time `json:"created_at"`
}

// NewListNotificationsTool creates the "list_notifications" tool definition.
// It accepts an optional "unread_only" boolean parameter (default false).
func NewListNotificationsTool() mcp.Tool {
	return mcp.NewTool("list_notifications",
		mcp.WithDescription("List notifications for the authenticated user, optionally filtered to unread only"),
		mcp.WithBoolean("unread_only",
			mcp.Description("If true, only return unread notifications"),
		),
	)
}

// HandleListNotifications handles the list_notifications tool call.
// It queries the notifications table scoped to the authenticated user's ID
// from the JWT claims, with an optional unread filter.
func HandleListNotifications(pool *pgxpool.Pool) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("unauthorized: missing claims"), nil
		}

		unreadOnly := req.GetBool("unread_only", false)

		query := `SELECT id, type, title, body, read_at IS NOT NULL AS is_read, created_at
				  FROM notifications
				  WHERE user_id = $1`
		args := []any{claims.UserID}

		if unreadOnly {
			query += ` AND read_at IS NULL`
		}
		query += ` ORDER BY created_at DESC`

		rows, err := pool.Query(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("list notifications: %w", err)
		}
		defer rows.Close()

		var notifications []notificationRow
		for rows.Next() {
			var n notificationRow
			if err := rows.Scan(&n.ID, &n.Type, &n.Title, &n.Body, &n.IsRead, &n.CreatedAt); err != nil {
				return nil, fmt.Errorf("scan notification: %w", err)
			}
			notifications = append(notifications, n)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate notifications: %w", err)
		}
		if notifications == nil {
			notifications = []notificationRow{}
		}

		data, err := json.Marshal(notifications)
		if err != nil {
			return nil, fmt.Errorf("marshal notifications: %w", err)
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// NewGetUnreadCountTool creates the "get_unread_count" tool definition.
// It takes no parameters and returns the count of unread notifications.
func NewGetUnreadCountTool() mcp.Tool {
	return mcp.NewTool("get_unread_count",
		mcp.WithDescription("Get the count of unread notifications for the authenticated user"),
	)
}

// HandleGetUnreadCount handles the get_unread_count tool call.
// It counts notifications where read_at IS NULL for the authenticated user.
func HandleGetUnreadCount(pool *pgxpool.Pool) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("unauthorized: missing claims"), nil
		}

		var count int
		err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL`,
			claims.UserID,
		).Scan(&count)
		if err != nil {
			return nil, fmt.Errorf("count unread notifications: %w", err)
		}

		data, err := json.Marshal(map[string]int{"count": count})
		if err != nil {
			return nil, fmt.Errorf("marshal count: %w", err)
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}