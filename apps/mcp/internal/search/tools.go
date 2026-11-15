// Package search provides an MCP tool for cross-entity search across all
// Home OS entity tables. It performs ILIKE queries over the searchable
// columns of assets, vehicles, pets, vendors, properties, bills, notes,
// secrets, and files, plus the title, description and location of calendar
// events, returning results grouped by entity_type with relevance scores.
// All queries are scoped to the authenticated household.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
		mcp.WithDescription("Search across all entities (assets, vehicles, pets, vendors, properties, bills, notes, secrets, files, calendar events) and return results grouped by entity type with relevance scores. Event hits also carry start and calendar_name."),
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
			ID           string `json:"id"`
			Title        string `json:"title"`
			Score        int    `json:"score"`
			Start        string `json:"start,omitempty"`
			CalendarName string `json:"calendar_name,omitempty"`
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

		events, err := searchEvents(ctx, pool, claims.HouseholdID, query)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 {
			hits := make([]searchHit, 0, len(events))
			for _, e := range events {
				hits = append(hits, searchHit{ID: e.ID, Title: e.Title, Score: 2, Start: e.Start, CalendarName: e.CalendarName})
			}
			groups = append(groups, "event")
			results["event"] = hits
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

// eventLimit caps the event hits returned, matching the per-table LIMIT of
// the specs queries.
const eventLimit = 20

// eventScanLimit bounds how many household calendar objects are read and
// decoded per search. Household calendars are small (hundreds of events), so
// this is a safety net rather than a real constraint.
const eventScanLimit = 5000

// eventHit is a calendar event matching a search.
type eventHit struct {
	ID           string
	Title        string
	Start        string
	CalendarName string
}

// eventFields is the subset of the ical_data JSON document that search reads.
type eventFields struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Location    string `json:"location"`
	Start       string `json:"start"`
}

// searchEvents returns the household's calendar events whose title,
// description or location contains query (case-insensitive).
//
// Events are not a searchSpec because they need a JOIN for household scoping
// (calendar_objects has no household_id; it belongs to calendars) and their
// fields live in the ical_data TEXT column as JSON. The JSON is decoded in Go
// rather than with a ::jsonb cast so a single malformed row is skipped instead
// of failing the whole search, and so matching only looks at the three
// searchable fields (never JSON keys like "title" or other fields).
//
// When query contains no characters JSON would escape, a raw ILIKE over
// ical_data narrows the rows first; a substring of a field value then also
// appears verbatim in the encoded document.
func searchEvents(ctx context.Context, pool *pgxpool.Pool, householdID any, query string) ([]eventHit, error) {
	sql := `
		SELECT co.id::text, co.ical_data, c.name
		FROM calendar_objects co
		JOIN calendars c ON c.id = co.calendar_id
		WHERE c.household_id = $1`
	args := []any{householdID}
	if !needsJSONEscape(query) {
		sql += ` AND co.ical_data ILIKE '%' || $2 || '%' ESCAPE '\'`
		args = append(args, escapeLike(query))
	}
	sql += fmt.Sprintf(` ORDER BY co.created_at DESC LIMIT %d`, eventScanLimit)

	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search calendar_objects: %w", err)
	}
	defer rows.Close()

	needle := strings.ToLower(query)
	hits := []eventHit{}
	for rows.Next() {
		var id, raw, calendarName string
		if err := rows.Scan(&id, &raw, &calendarName); err != nil {
			return nil, fmt.Errorf("scan calendar_objects result: %w", err)
		}
		var f eventFields
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			continue // not a JSON event document; nothing searchable
		}
		if !strings.Contains(strings.ToLower(f.Title), needle) &&
			!strings.Contains(strings.ToLower(f.Description), needle) &&
			!strings.Contains(strings.ToLower(f.Location), needle) {
			continue
		}
		hits = append(hits, eventHit{ID: id, Title: f.Title, Start: f.Start, CalendarName: calendarName})
		if len(hits) == eventLimit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate calendar_objects result: %w", err)
	}
	return hits, nil
}

// needsJSONEscape reports whether encoding s as a JSON string might change any
// of its characters (some writers escape non-ASCII as \uXXXX), in which case a
// raw substring match on the stored JSON could miss a real hit.
func needsJSONEscape(s string) bool {
	for _, r := range s {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '<' || r == '>' || r == '&' {
			return true
		}
	}
	return false
}

// escapeLike escapes LIKE wildcards so query matches literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
