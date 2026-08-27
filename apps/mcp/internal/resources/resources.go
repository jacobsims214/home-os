// Package resources provides MCP resource templates for reading Home OS
// domain data as structured JSON resources. Each template uses RFC 6570
// URI patterns and returns application/json content.
package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
)

// NewHouseholdResourceTemplate creates the "household://{id}" resource
// template that returns household data as JSON.
func NewHouseholdResourceTemplate() mcp.ResourceTemplate {
	return mcp.NewResourceTemplate(
		"household://{id}",
		"Household",
		mcp.WithTemplateDescription("Household details by ID"),
		mcp.WithTemplateMIMEType("application/json"),
	)
}

// HandleHouseholdResource handles reads of the household://{id} resource.
func HandleHouseholdResource(pool *pgxpool.Pool) func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return nil, fmt.Errorf("unauthorized: missing claims")
		}

		id, ok := req.Params.Arguments["id"].(string)
		if !ok || id == "" {
			return nil, fmt.Errorf("missing or invalid household id")
		}

		var name string
		var memberCount int
		err := pool.QueryRow(ctx,
			`SELECT name FROM households WHERE id = $1 AND id = $2`, id, claims.HouseholdID,
		).Scan(&name)
		if err != nil {
			return nil, fmt.Errorf("get household: %w", err)
		}

		err = pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM memberships WHERE household_id = $1 AND household_id = $2`, id, claims.HouseholdID,
		).Scan(&memberCount)
		if err != nil {
			return nil, fmt.Errorf("count members: %w", err)
		}

		data, err := json.Marshal(map[string]any{
			"id":           id,
			"name":         name,
			"member_count": memberCount,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal household: %w", err)
		}

		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(data),
			},
		}, nil
	}
}

// NewCalendarEventsResourceTemplate creates the "calendar://{id}/events"
// resource template that returns calendar events as JSON.
func NewCalendarEventsResourceTemplate() mcp.ResourceTemplate {
	return mcp.NewResourceTemplate(
		"calendar://{id}/events",
		"Calendar Events",
		mcp.WithTemplateDescription("Events for a calendar by calendar ID"),
		mcp.WithTemplateMIMEType("application/json"),
	)
}

// HandleCalendarEventsResource handles reads of the calendar://{id}/events resource.
func HandleCalendarEventsResource(pool *pgxpool.Pool) func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return nil, fmt.Errorf("unauthorized: missing claims")
		}

		id, ok := req.Params.Arguments["id"].(string)
		if !ok || id == "" {
			return nil, fmt.Errorf("missing or invalid calendar id")
		}

		// First verify the calendar exists and belongs to the caller's household.
		var calendarExists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM calendars WHERE id = $1 AND household_id = $2)`, id, claims.HouseholdID,
		).Scan(&calendarExists)
		if err != nil {
			return nil, fmt.Errorf("check calendar: %w", err)
		}
		if !calendarExists {
			return nil, fmt.Errorf("calendar not found: %s", id)
		}

		// Query events for this calendar.
		rows, err := pool.Query(ctx,
			`SELECT id, uid, event_type, created_at, updated_at
			 FROM calendar_objects
			 WHERE calendar_id = $1
			 ORDER BY created_at`,
			id,
		)
		if err != nil {
			return nil, fmt.Errorf("list events: %w", err)
		}
		defer rows.Close()

		type eventSummary struct {
			ID        string    `json:"id"`
			UID       string    `json:"uid"`
			EventType string    `json:"event_type"`
			CreatedAt time.Time `json:"created_at"`
			UpdatedAt time.Time `json:"updated_at"`
		}

		var events []eventSummary
		for rows.Next() {
			var e eventSummary
			if err := rows.Scan(&e.ID, &e.UID, &e.EventType, &e.CreatedAt, &e.UpdatedAt); err != nil {
				return nil, fmt.Errorf("scan event: %w", err)
			}
			events = append(events, e)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate events: %w", err)
		}
		if events == nil {
			events = []eventSummary{}
		}

		data, err := json.Marshal(events)
		if err != nil {
			return nil, fmt.Errorf("marshal events: %w", err)
		}

		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(data),
			},
		}, nil
	}
}

// NewAssetResourceTemplate creates the "asset://{id}" resource template
// that returns asset details as JSON.
func NewAssetResourceTemplate() mcp.ResourceTemplate {
	return mcp.NewResourceTemplate(
		"asset://{id}",
		"Asset",
		mcp.WithTemplateDescription("Asset details by ID"),
		mcp.WithTemplateMIMEType("application/json"),
	)
}

// HandleAssetResource handles reads of the asset://{id} resource.
func HandleAssetResource(pool *pgxpool.Pool) func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return nil, fmt.Errorf("unauthorized: missing claims")
		}

		id, ok := req.Params.Arguments["id"].(string)
		if !ok || id == "" {
			return nil, fmt.Errorf("missing or invalid asset id")
		}

		type assetData struct {
			ID           string  `json:"id"`
			HouseholdID  string  `json:"household_id"`
			Name         string  `json:"name"`
			Category     *string `json:"category,omitempty"`
			Manufacturer *string `json:"manufacturer,omitempty"`
			Model        *string `json:"model,omitempty"`
			SerialNumber *string `json:"serial_number,omitempty"`
		}

		var a assetData
		err := pool.QueryRow(ctx,
			`SELECT id, household_id, name, category, manufacturer, model, serial_number
			 FROM assets WHERE id = $1 AND household_id = $2`, id, claims.HouseholdID,
		).Scan(&a.ID, &a.HouseholdID, &a.Name, &a.Category, &a.Manufacturer, &a.Model, &a.SerialNumber)
		if err != nil {
			return nil, fmt.Errorf("get asset: %w", err)
		}

		data, err := json.Marshal(a)
		if err != nil {
			return nil, fmt.Errorf("marshal asset: %w", err)
		}

		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(data),
			},
		}, nil
	}
}