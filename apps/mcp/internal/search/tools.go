// Package search provides an MCP tool for cross-entity search across all
// Home OS entity tables. It performs ILIKE queries over the searchable
// columns of assets, vehicles, pets, vendors, properties, bills, notes,
// secrets, and files, returning results grouped by entity_type with
// relevance scores. All queries are scoped to the authenticated household.
package search

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
)

// searchSpec describes one entity table to include in the cross-entity search:
// the entity_type label, the table name, the display title expression, and the
// searchable columns.
type searchSpec struct {
	EntityType string
	Table      string
	Title      string // SQL expression producing the display title
	Columns    string // SQL expression of searchable columns
}

// specs lists every entity type searched by the "search" tool, in priority
// order. The order determines the grouping order in the response.
var specs = []searchSpec{
	{EntityType: "asset", Table: "assets", Title: "name", Columns: "COALESCE(name,'') || ' ' || COALESCE(manufacturer,'') || ' ' || COALESCE(model,'')"},
	{EntityType: "vehicle", Table: "vehicles", Title: "COALESCE(make,'') || ' ' || COALESCE(model,'')", Columns: "COALESCE(make,'') || ' ' || COALESCE(model,'')"},
	{EntityType: "pet", Table: "pets", Title: "name", Columns: "COALESCE(name,'') || ' ' || COALESCE(species,'')"},
	{EntityType: "vendor", Table: "vendors", Title: "name", Columns: "COALESCE(name,'') || ' ' || COALESCE(specialty,'')"},
	{EntityType: "property", Table: "properties", Title: "name", Columns: "COALESCE(name,'') || ' ' || COALESCE(address,'')"},
	{EntityType: "bill", Table: "bills", Title: "name", Columns: "COALESCE(name,'') || ' ' || COALESCE(category,'')"},
	{EntityType: "note", Table: "notes", Title: "COALESCE(title,'')", Columns: "COALESCE(title,'') || ' ' || body"},
	{EntityType: "secret", Table: "secrets", Title: "name", Columns: "COALESCE(name,'') || ' ' || COALESCE(secret_type,'')"},
	{EntityType: "file", Table: "files", Title: "name", Columns: "COALESCE(name,'') || ' ' || COALESCE(extracted_text,'')"},
}

// NewSearchTool creates the "search" MCP tool.
// It searches across all entity tables with ILIKE and returns results
// grouped by entity_type with relevance scores.
func NewSearchTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("search",
		mcp.WithDescription("Search across all entities (assets, vehicles, pets, vendors, properties, bills, notes, secrets, files) and return results grouped by entity type with relevance scores"),
		mcp.WithString("query",
			mcp.Description("Search query string"),
			mcp.Required(),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultText(`{"error":"unauthorized"}`), nil
		}

		args := req.GetArguments()
		query, _ := args["query"].(string)
		if query == "" {
			return mcp.NewToolResultText(`{"error":"query is required"}`), nil
		}

		pattern := "%" + query + "%"

		// searchHit is a single matching row within a group.
		type searchHit struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Score int    `json:"score"`
		}

		// results maps entity_type -> hits, preserving spec order via groups slice.
		groups := []string{}
		results := map[string][]searchHit{}

		for _, spec := range specs {
			querySQL := fmt.Sprintf(`
				SELECT id, %s AS title, 2 AS score
				FROM %s
				WHERE household_id = $1
				  AND (%s) ILIKE $2
				ORDER BY created_at DESC
				LIMIT 20`,
				spec.Title, spec.Table, spec.Columns,
			)

			rows, err := pool.Query(ctx, querySQL, claims.HouseholdID, pattern)
			if err != nil {
				return nil, fmt.Errorf("search %s: %w", spec.Table, err)
			}

			hits := []searchHit{}
			for rows.Next() {
				var hit searchHit
				if err := rows.Scan(&hit.ID, &hit.Title, &hit.Score); err != nil {
					rows.Close()
					return nil, fmt.Errorf("scan %s result: %w", spec.Table, err)
				}
				hits = append(hits, hit)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("iterate %s result: %w", spec.Table, err)
			}

			if len(hits) > 0 {
				if _, ok := results[spec.EntityType]; !ok {
					groups = append(groups, spec.EntityType)
				}
				results[spec.EntityType] = hits
			}
		}

		// Build grouped output.
		grouped := make([]map[string]interface{}, 0, len(groups))
		total := 0
		for _, entityType := range groups {
			hits := results[entityType]
			total += len(hits)
			grouped = append(grouped, map[string]interface{}{
				"entity_type": entityType,
				"count":       len(hits),
				"results":     hits,
			})
		}

		output, _ := json.Marshal(map[string]interface{}{
			"query":   query,
			"total":   total,
			"results": grouped,
		})
		return mcp.NewToolResultText(string(output)), nil
	}

	return "search", tool, handler
}