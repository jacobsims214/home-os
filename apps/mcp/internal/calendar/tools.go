// Package calendar provides MCP tools for calendar operations.
// Each tool function accepts a *pgxpool.Pool and returns a tool definition
// plus handler function. Handlers extract JWT claims from context to scope
// all queries by household_id.
package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
)

// Calendar represents a calendar collection returned by list_calendars.
type Calendar struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Color      *string `json:"color,omitempty"`
	PropertyID *string `json:"property_id,omitempty"`
}

// Event represents a calendar event parsed from calendar_objects + ical_data JSON.
type Event struct {
	ID          string `json:"id"`
	CalendarID  string `json:"calendar_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Start       string `json:"start"`
	End         string `json:"end"`
	AllDay      bool   `json:"all_day"`
	Location    string `json:"location"`
	Color       string `json:"color"`
	EventType   string `json:"event_type"`
	CalendarName string `json:"calendar_name,omitempty"`
}

// eventJSON is the JSON structure stored in ical_data column.
type eventJSON struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Start       string `json:"start"`
	End         string `json:"end"`
	AllDay      bool   `json:"all_day"`
	Location    string `json:"location"`
	Color       string `json:"color"`
	EventType   string `json:"event_type"`
}

// AvailableSlot represents a free time slot returned by find_available_slots.
type AvailableSlot struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// ConflictEvent represents a conflicting event returned by check_conflicts.
type ConflictEvent struct {
	ID           string `json:"id"`
	CalendarID   string `json:"calendar_id"`
	Title        string `json:"title"`
	Start        string `json:"start"`
	End          string `json:"end"`
	CalendarName string `json:"calendar_name,omitempty"`
}

// ─────────────────────────────────────────────
// Helper: extract a string param from the tool request
// ─────────────────────────────────────────────

func getStringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func getBoolArg(args map[string]any, key string, defaultVal bool) bool {
	if args == nil {
		return defaultVal
	}
	v, ok := args[key]
	if !ok || v == nil {
		return defaultVal
	}
	b, ok := v.(bool)
	if !ok {
		return defaultVal
	}
	return b
}

func getFloat64Arg(args map[string]any, key string, defaultVal float64) float64 {
	if args == nil {
		return defaultVal
	}
	v, ok := args[key]
	if !ok || v == nil {
		return defaultVal
	}
	f, ok := v.(float64)
	if !ok {
		return defaultVal
	}
	return f
}

func getStringSliceArg(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// ─────────────────────────────────────────────
// Helper: parse event from calendar_objects row
// ─────────────────────────────────────────────

// eventRow holds the raw columns from calendar_objects joined with calendars.
type eventRow struct {
	ID           string
	CalendarID   string
	IcalData     string
	EventType    string
	CalendarName string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func parseEventFromRow(row eventRow) (*Event, error) {
	var ej eventJSON
	if err := json.Unmarshal([]byte(row.IcalData), &ej); err != nil {
		return nil, fmt.Errorf("parse ical_data: %w", err)
	}
	ev := &Event{
		ID:           row.ID,
		CalendarID:   row.CalendarID,
		Title:        ej.Title,
		Description:  ej.Description,
		Start:        ej.Start,
		End:          ej.End,
		AllDay:       ej.AllDay,
		Location:     ej.Location,
		Color:        ej.Color,
		EventType:    row.EventType,
		CalendarName: row.CalendarName,
	}
	return ev, nil
}

// ─────────────────────────────────────────────
// Helper: marshal an event's "editable fields" into ical_data JSON
// ─────────────────────────────────────────────

func marshalEventFields(title, description, start, end, location string, allDay bool) (string, error) {
	ej := eventJSON{
		Title:       title,
		Description: description,
		Start:       start,
		End:         end,
		AllDay:      allDay,
		Location:    location,
	}
	b, err := json.Marshal(ej)
	if err != nil {
		return "", fmt.Errorf("marshal event: %w", err)
	}
	return string(b), nil
}

// mergeEventFields merges non-empty optional fields into an existing ical_data JSON.
func mergeEventFields(existingIcalData string, title, description, start, end, location *string, allDay *bool) (string, error) {
	var ej eventJSON
	if err := json.Unmarshal([]byte(existingIcalData), &ej); err != nil {
		return "", fmt.Errorf("parse existing ical_data: %w", err)
	}
	if title != nil {
		ej.Title = *title
	}
	if description != nil {
		ej.Description = *description
	}
	if start != nil {
		ej.Start = *start
	}
	if end != nil {
		ej.End = *end
	}
	if location != nil {
		ej.Location = *location
	}
	if allDay != nil {
		ej.AllDay = *allDay
	}
	b, err := json.Marshal(ej)
	if err != nil {
		return "", fmt.Errorf("marshal merged event: %w", err)
	}
	return string(b), nil
}

// ─────────────────────────────────────────────
// Helper: verify a calendar belongs to the household
// ─────────────────────────────────────────────

func verifyCalendarOwnership(ctx context.Context, pool *pgxpool.Pool, calendarID, householdID string) error {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM calendars WHERE id = $1 AND household_id = $2)`,
		calendarID, householdID,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("verify calendar: %w", err)
	}
	if !exists {
		return fmt.Errorf("calendar not found")
	}
	return nil
}

// ─────────────────────────────────────────────
// Helper: get calendar_id for an event and verify household ownership
// ─────────────────────────────────────────────

func getEventCalendarID(ctx context.Context, pool *pgxpool.Pool, eventID string) (string, error) {
	var calendarID string
	err := pool.QueryRow(ctx,
		`SELECT calendar_id FROM calendar_objects WHERE id = $1`,
		eventID,
	).Scan(&calendarID)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("event not found")
	}
	if err != nil {
		return "", fmt.Errorf("get event calendar: %w", err)
	}
	return calendarID, nil
}

// ─────────────────────────────────────────────
// Helper: record a calendar_changes entry and bump ctag in a transaction
// ─────────────────────────────────────────────

func recordChangeAndBumpCtag(ctx context.Context, tx pgx.Tx, calendarID, eventUID, action string) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO calendar_changes (calendar_id, event_uid, action) VALUES ($1, $2, $3)`,
		calendarID, eventUID, action,
	); err != nil {
		return fmt.Errorf("record change: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE calendars SET ctag = gen_random_uuid()::text, updated_at = NOW() WHERE id = $1`,
		calendarID,
	); err != nil {
		return fmt.Errorf("bump ctag: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────
// Helper: build a time.Time from a YYYY-MM-DD string at the start of day
// in the given location (e.g. the household's configured timezone).
// ─────────────────────────────────────────────

func parseDate(dateStr string, loc *time.Location) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", dateStr, loc)
}

// ─────────────────────────────────────────────
// Helper: load the household's configured timezone from the database.
// Returns UTC as a safe fallback (with a warning log) if the timezone
// column is empty or not a valid IANA timezone name.
// ─────────────────────────────────────────────

func loadHouseholdLocation(ctx context.Context, pool *pgxpool.Pool, householdID string) *time.Location {
	var tz string
	err := pool.QueryRow(ctx,
		`SELECT timezone FROM households WHERE id = $1`, householdID,
	).Scan(&tz)
	if err != nil {
		slog.Warn("loadHouseholdLocation: query failed, falling back to UTC", "household_id", householdID, "error", err)
		return time.UTC
	}
	if tz == "" || tz == "UTC" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		slog.Warn("loadHouseholdLocation: invalid timezone, falling back to UTC", "household_id", householdID, "timezone", tz, "error", err)
		return time.UTC
	}
	return loc
}

// ─────────────────────────────────────────────
// Helper: query events for a household, optionally filtered by calendar IDs
// and date range. Date filtering is done in SQL via JSONB extraction of
// ical_data.start/end timestamps. Callers may still use eventOverlapsRange
// as a correctness backstop.
// ─────────────────────────────────────────────

func queryEvents(ctx context.Context, pool *pgxpool.Pool, householdID string, calendarIDs []string, startDate, endDate time.Time) ([]Event, error) {
	// Base query: join calendar_objects with calendars for household scoping
	query := `SELECT co.id, co.calendar_id, co.ical_data, co.event_type, c.name AS calendar_name, co.created_at, co.updated_at
		FROM calendar_objects co
		JOIN calendars c ON c.id = co.calendar_id
		WHERE c.household_id = $1`
	args := []any{householdID}
	argIdx := 2

	// If specific calendar IDs are provided, filter by them
	if len(calendarIDs) > 0 {
		placeholders := make([]string, len(calendarIDs))
		for i, calID := range calendarIDs {
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			args = append(args, calID)
			argIdx++
		}
		query += fmt.Sprintf(" AND co.calendar_id IN (%s)", strings.Join(placeholders, ","))
	}

	// Filter by date range using JSONB extraction from ical_data.
	// Overlap condition: event start < range end AND event end > range start.
	// This narrows the result set in SQL; callers keep Go-side filtering as a backstop.
	if !startDate.IsZero() && !endDate.IsZero() {
		query += fmt.Sprintf(" AND (co.ical_data::jsonb->>'start')::timestamptz < $%d", argIdx)
		args = append(args, endDate)
		argIdx++
		query += fmt.Sprintf(" AND (co.ical_data::jsonb->>'end')::timestamptz > $%d", argIdx)
		args = append(args, startDate)
		argIdx++
	}

	query += " ORDER BY co.created_at"

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var row eventRow
		if err := rows.Scan(&row.ID, &row.CalendarID, &row.IcalData, &row.EventType, &row.CalendarName, &row.CreatedAt, &row.UpdatedAt); err != nil {
			slog.Warn("calendar: query events: scan error", "error", err)
			continue
		}
		ev, err := parseEventFromRow(row)
		if err != nil {
			slog.Warn("calendar: query events: parse error", "event_id", row.ID, "error", err)
			continue
		}
		events = append(events, *ev)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}

	return events, nil
}

// ─────────────────────────────────────────────
// Helper: filter events that overlap a given date range (inclusive of boundaries)
// ─────────────────────────────────────────────

func eventOverlapsRange(ev Event, start, end time.Time) bool {
	evStart, err := time.Parse(time.RFC3339, ev.Start)
	if err != nil {
		return false
	}
	evEnd, err := time.Parse(time.RFC3339, ev.End)
	if err != nil {
		return false
	}
	// Overlap: event starts before the range ends AND event ends after the range starts
	return evStart.Before(end) && evEnd.After(start)
}

func parseEventTime(ev Event) (time.Time, time.Time, error) {
	start, err := time.Parse(time.RFC3339, ev.Start)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse start: %w", err)
	}
	end, err := time.Parse(time.RFC3339, ev.End)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse end: %w", err)
	}
	return start, end, nil
}

// ─────────────────────────────────────────────
// 1. list_calendars
// ─────────────────────────────────────────────

// NewListCalendarsTool creates the list_calendars tool and its handler.
func NewListCalendarsTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("list_calendars",
		mcp.WithDescription("List all calendars for the current household"),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		rows, err := pool.Query(ctx,
			`SELECT id, name, type, color, property_id
			 FROM calendars WHERE household_id = $1 ORDER BY name`,
			claims.HouseholdID,
		)
		if err != nil {
			slog.Error("list_calendars: query error", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("query error: %v", err)), nil
		}
		defer rows.Close()

		calendars := make([]Calendar, 0)
		for rows.Next() {
			var cal Calendar
			if err := rows.Scan(&cal.ID, &cal.Name, &cal.Type, &cal.Color, &cal.PropertyID); err != nil {
				slog.Error("list_calendars: scan error", "error", err)
				continue
			}
			calendars = append(calendars, cal)
		}

		if err := rows.Err(); err != nil {
			slog.Error("list_calendars: iterate error", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("iterate calendars: %v", err)), nil
		}

		data, err := json.Marshal(calendars)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}

	return tool, handler
}

// ─────────────────────────────────────────────
// 2. list_events
// ─────────────────────────────────────────────

// NewListEventsTool creates the list_events tool and its handler.
func NewListEventsTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("list_events",
		mcp.WithDescription("List events across calendars, optionally filtered by calendar, start date, and end date"),
		mcp.WithString("calendar_id", mcp.Description("Optional calendar ID to filter by")),
		mcp.WithString("start_date", mcp.Description("Start date (YYYY-MM-DD). Defaults to beginning of current month.")),
		mcp.WithString("end_date", mcp.Description("End date (YYYY-MM-DD). Defaults to start_date + 1 month.")),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)

		args := req.GetArguments()
		calendarID := getStringArg(args, "calendar_id")

		// Determine date range in the household's timezone
		now := time.Now().In(loc)
		startDateStr := getStringArg(args, "start_date")
		var startDate time.Time
		if startDateStr != "" {
			var err error
			startDate, err = parseDate(startDateStr, loc)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid start_date format (use YYYY-MM-DD): %v", err)), nil
			}
		} else {
			// Default to beginning of current month in household timezone
			startDate = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		}

		endDateStr := getStringArg(args, "end_date")
		var endDate time.Time
		if endDateStr != "" {
			var err error
			endDate, err = parseDate(endDateStr, loc)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid end_date format (use YYYY-MM-DD): %v", err)), nil
			}
			// End date is inclusive, add 1 day so events on the end date are included
			endDate = endDate.Add(24 * time.Hour)
		} else {
			// Default to start_date + 1 month
			endDate = startDate.AddDate(0, 1, 0)
		}

		// Build query: start with household-scoped calendars
		query := `SELECT co.id, co.calendar_id, co.ical_data, co.event_type, c.name AS calendar_name, co.created_at, co.updated_at
			FROM calendar_objects co
			JOIN calendars c ON c.id = co.calendar_id
			WHERE c.household_id = $1`
		queryArgs := []any{claims.HouseholdID}

		if calendarID != "" {
			query += " AND co.calendar_id = $2"
			queryArgs = append(queryArgs, calendarID)
		}

		query += " ORDER BY co.created_at"

		rows, err := pool.Query(ctx, query, queryArgs...)
		if err != nil {
			slog.Error("list_events: query error", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("query error: %v", err)), nil
		}
		defer rows.Close()

		events := make([]Event, 0)
		for rows.Next() {
			var row eventRow
			if err := rows.Scan(&row.ID, &row.CalendarID, &row.IcalData, &row.EventType, &row.CalendarName, &row.CreatedAt, &row.UpdatedAt); err != nil {
				slog.Error("list_events: scan error", "error", err)
				continue
			}
			ev, err := parseEventFromRow(row)
			if err != nil {
				slog.Warn("list_events: parse error", "event_id", row.ID, "error", err)
				continue
			}
			// Filter by date range in Go
			if eventOverlapsRange(*ev, startDate, endDate) {
				events = append(events, *ev)
			}
		}

		if err := rows.Err(); err != nil {
			slog.Error("list_events: iterate error", "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("iterate events: %v", err)), nil
		}

		data, err := json.Marshal(events)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}

	return tool, handler
}

// ─────────────────────────────────────────────
// 3. create_event
// ─────────────────────────────────────────────

// NewCreateEventTool creates the create_event tool and its handler.
func NewCreateEventTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("create_event",
		mcp.WithDescription("Create a new calendar event. Records a calendar_changes entry and bumps the calendar's ctag for CalDAV sync."),
		mcp.WithString("calendar_id", mcp.Description("Calendar ID to create the event in"), mcp.Required()),
		mcp.WithString("title", mcp.Description("Event title"), mcp.Required()),
		mcp.WithString("start_time", mcp.Description("Start time (ISO 8601, e.g. 2026-08-03T14:00:00Z)"), mcp.Required()),
		mcp.WithString("end_time", mcp.Description("End time (ISO 8601, e.g. 2026-08-03T15:00:00Z)"), mcp.Required()),
		mcp.WithBoolean("all_day", mcp.Description("Whether this is an all-day event (default false)")),
		mcp.WithString("description", mcp.Description("Optional description")),
		mcp.WithString("location", mcp.Description("Optional location")),
		mcp.WithString("event_type", mcp.Description("Optional event type (default 'event')")),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		args := req.GetArguments()
		calendarID := getStringArg(args, "calendar_id")
		title := getStringArg(args, "title")
		startTime := getStringArg(args, "start_time")
		endTime := getStringArg(args, "end_time")
		allDay := getBoolArg(args, "all_day", false)
		description := getStringArg(args, "description")
		location := getStringArg(args, "location")
		eventType := getStringArg(args, "event_type")
		if eventType == "" {
			eventType = "event"
		}

		// Validate required fields
		if calendarID == "" {
			return mcp.NewToolResultError("calendar_id is required"), nil
		}
		if title == "" {
			return mcp.NewToolResultError("title is required"), nil
		}
		if startTime == "" {
			return mcp.NewToolResultError("start_time is required"), nil
		}
		if endTime == "" {
			return mcp.NewToolResultError("end_time is required"), nil
		}

		// Validate the calendar belongs to the household
		if err := verifyCalendarOwnership(ctx, pool, calendarID, claims.HouseholdID); err != nil {
			return mcp.NewToolResultError("calendar not found"), nil
		}

		// Validate time formats and ordering
		startT, err := time.Parse(time.RFC3339, startTime)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid start_time format (use ISO 8601): %v", err)), nil
		}
		endT, err := time.Parse(time.RFC3339, endTime)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid end_time format (use ISO 8601): %v", err)), nil
		}
		if !endT.After(startT) {
			return mcp.NewToolResultError("end_time must be after start_time"), nil
		}

		// Generate IDs
		eventID := uuid.New().String()
		uid := eventID
		etag := uuid.New().String()

		// Build ical_data JSON
		icalData, err := marshalEventFields(title, description, startTime, endTime, location, allDay)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
		}

		// Execute in transaction
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("begin transaction: %v", err)), nil
		}
		defer tx.Rollback(ctx)

		_, err = tx.Exec(ctx,
			`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, etag, event_type)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			eventID, calendarID, uid, icalData, etag, eventType,
		)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("insert event: %v", err)), nil
		}

		if err := recordChangeAndBumpCtag(ctx, tx, calendarID, uid, "create"); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("record change: %v", err)), nil
		}

		if err := tx.Commit(ctx); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("commit: %v", err)), nil
		}

		slog.Info("create_event: created", "event_id", eventID, "calendar_id", calendarID)
		return mcp.NewToolResultText(fmt.Sprintf(`{"id":"%s"}`, eventID)), nil
	}

	return tool, handler
}

// ─────────────────────────────────────────────
// 4. update_event
// ─────────────────────────────────────────────

// NewUpdateEventTool creates the update_event tool and its handler.
func NewUpdateEventTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("update_event",
		mcp.WithDescription("Update an existing calendar event. All fields except event_id are optional — only provided fields are updated."),
		mcp.WithString("event_id", mcp.Description("Event ID to update"), mcp.Required()),
		mcp.WithString("title", mcp.Description("New title")),
		mcp.WithString("start_time", mcp.Description("New start time (ISO 8601)")),
		mcp.WithString("end_time", mcp.Description("New end time (ISO 8601)")),
		mcp.WithBoolean("all_day", mcp.Description("Whether this is an all-day event")),
		mcp.WithString("description", mcp.Description("New description")),
		mcp.WithString("location", mcp.Description("New location")),
		mcp.WithString("event_type", mcp.Description("New event type")),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		args := req.GetArguments()
		eventID := getStringArg(args, "event_id")
		if eventID == "" {
			return mcp.NewToolResultError("event_id is required"), nil
		}

		// Get the event's calendar and verify household ownership
		calendarID, err := getEventCalendarID(ctx, pool, eventID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := verifyCalendarOwnership(ctx, pool, calendarID, claims.HouseholdID); err != nil {
			// Return not found rather than "not your calendar" to avoid leaking info
			return mcp.NewToolResultError("event not found"), nil
		}

		// Load existing ical_data
		var existingIcalData string
		var existingEventType string
		err = pool.QueryRow(ctx,
			`SELECT ical_data, event_type FROM calendar_objects WHERE id = $1`,
			eventID,
		).Scan(&existingIcalData, &existingEventType)
		if err != nil {
			return mcp.NewToolResultError("event not found"), nil
		}

		// Collect optional field pointers
		var titlePtr, descPtr, startPtr, endPtr, locPtr *string
		var allDayPtr *bool

		if v := getStringArg(args, "title"); v != "" {
			titlePtr = &v
		}
		if v := getStringArg(args, "description"); v != "" || args["description"] != nil {
			v := getStringArg(args, "description")
			descPtr = &v
		}
		if v := getStringArg(args, "start_time"); v != "" {
			startPtr = &v
		}
		if v := getStringArg(args, "end_time"); v != "" {
			endPtr = &v
		}
		if v := getStringArg(args, "location"); v != "" || args["location"] != nil {
			v := getStringArg(args, "location")
			locPtr = &v
		}
		if v, ok := args["all_day"]; ok && v != nil {
			if b, ok := v.(bool); ok {
				allDayPtr = &b
			}
		}

		// Validate time formats if provided
		if startPtr != nil {
			if _, err := time.Parse(time.RFC3339, *startPtr); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid start_time format (use ISO 8601): %v", err)), nil
			}
		}
		if endPtr != nil {
			if _, err := time.Parse(time.RFC3339, *endPtr); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid end_time format (use ISO 8601): %v", err)), nil
			}
		}

		// Merge existing ical_data with new fields
		mergedIcalData, err := mergeEventFields(existingIcalData, titlePtr, descPtr, startPtr, endPtr, locPtr, allDayPtr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("merge event data: %v", err)), nil
		}

		// Validate merged start/end ordering
		var mergedEj eventJSON
		if err := json.Unmarshal([]byte(mergedIcalData), &mergedEj); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("parse merged event: %v", err)), nil
		}
		mergedStart, err := time.Parse(time.RFC3339, mergedEj.Start)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid start_time in merged data (use ISO 8601): %v", err)), nil
		}
		mergedEnd, err := time.Parse(time.RFC3339, mergedEj.End)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid end_time in merged data (use ISO 8601): %v", err)), nil
		}
		if !mergedEnd.After(mergedStart) {
			return mcp.NewToolResultError("end_time must be after start_time"), nil
		}

		newEtag := uuid.New().String()

		// Execute in transaction
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("begin transaction: %v", err)), nil
		}
		defer tx.Rollback(ctx)

		// Update event_type if provided
		eventType := getStringArg(args, "event_type")
		if eventType != "" {
			_, err = tx.Exec(ctx,
				`UPDATE calendar_objects SET ical_data = $1, etag = $2, event_type = $3, updated_at = NOW() WHERE id = $4`,
				mergedIcalData, newEtag, eventType, eventID,
			)
		} else {
			_, err = tx.Exec(ctx,
				`UPDATE calendar_objects SET ical_data = $1, etag = $2, updated_at = NOW() WHERE id = $3`,
				mergedIcalData, newEtag, eventID,
			)
		}
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update event: %v", err)), nil
		}

		// Record change and bump ctag
		if err := recordChangeAndBumpCtag(ctx, tx, calendarID, eventID, "update"); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("record change: %v", err)), nil
		}

		if err := tx.Commit(ctx); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("commit: %v", err)), nil
		}

		slog.Info("update_event: updated", "event_id", eventID)
		return mcp.NewToolResultText(`{"success":true}`), nil
	}

	return tool, handler
}

// ─────────────────────────────────────────────
// 5. delete_event
// ─────────────────────────────────────────────

// NewDeleteEventTool creates the delete_event tool and its handler.
func NewDeleteEventTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("delete_event",
		mcp.WithDescription("Delete a calendar event. Records a calendar_changes entry (action='delete') and bumps the calendar's ctag for CalDAV sync."),
		mcp.WithString("event_id", mcp.Description("Event ID to delete"), mcp.Required()),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		args := req.GetArguments()
		eventID := getStringArg(args, "event_id")
		if eventID == "" {
			return mcp.NewToolResultError("event_id is required"), nil
		}

		// Get the event's calendar and verify household ownership
		calendarID, err := getEventCalendarID(ctx, pool, eventID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := verifyCalendarOwnership(ctx, pool, calendarID, claims.HouseholdID); err != nil {
			return mcp.NewToolResultError("event not found"), nil
		}

		// Execute in transaction
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("begin transaction: %v", err)), nil
		}
		defer tx.Rollback(ctx)

		// Delete the event and get the uid for the change record
		var eventUID string
		err = tx.QueryRow(ctx,
			`DELETE FROM calendar_objects WHERE id = $1 RETURNING uid`,
			eventID,
		).Scan(&eventUID)
		if err == pgx.ErrNoRows {
			return mcp.NewToolResultError("event not found"), nil
		}
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("delete event: %v", err)), nil
		}

		// Record the delete change (DB constraint allows 'create', 'update', 'delete')
		if err := recordChangeAndBumpCtag(ctx, tx, calendarID, eventUID, "delete"); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("record change: %v", err)), nil
		}

		if err := tx.Commit(ctx); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("commit: %v", err)), nil
		}

		slog.Info("delete_event: deleted", "event_id", eventID)
		return mcp.NewToolResultText(`{"success":true}`), nil
	}

	return tool, handler
}

// ─────────────────────────────────────────────
// 6. find_available_slots
// ─────────────────────────────────────────────

// NewFindAvailableSlotsTool creates the find_available_slots tool and its handler.
func NewFindAvailableSlotsTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("find_available_slots",
		mcp.WithDescription("Find available time slots on a given date. Default available hours are 8am-8pm. Checks events across specified calendars."),
		mcp.WithString("date", mcp.Description("Date to check (YYYY-MM-DD)"), mcp.Required()),
		mcp.WithNumber("duration_minutes", mcp.Description("Minimum slot duration in minutes"), mcp.Required()),
		mcp.WithArray("calendar_ids", mcp.Description("Optional list of calendar IDs to check (checks all calendars if omitted)")),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)

		args := req.GetArguments()
		dateStr := getStringArg(args, "date")
		if dateStr == "" {
			return mcp.NewToolResultError("date is required (YYYY-MM-DD)"), nil
		}

		date, err := parseDate(dateStr, loc)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid date format (use YYYY-MM-DD): %v", err)), nil
		}

		durationMin := getFloat64Arg(args, "duration_minutes", 30)
		if durationMin <= 0 {
			return mcp.NewToolResultError("duration_minutes must be positive"), nil
		}
		duration := time.Duration(durationMin) * time.Minute

		calendarIDs := getStringSliceArg(args, "calendar_ids")

		// Define the available window: 8am to 8pm in the household's timezone
		dayStart := time.Date(date.Year(), date.Month(), date.Day(), 8, 0, 0, 0, loc)
		dayEnd := time.Date(date.Year(), date.Month(), date.Day(), 20, 0, 0, 0, loc)

		// Query events for the day (from midnight to midnight in household timezone)
		queryStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
		queryEnd := queryStart.Add(24 * time.Hour)

		events, err := queryEvents(ctx, pool, claims.HouseholdID, calendarIDs, queryStart, queryEnd)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("query events: %v", err)), nil
		}

		// Filter events that overlap the day and collect busy intervals
		type busyInterval struct {
			start time.Time
			end   time.Time
		}
		busy := make([]busyInterval, 0)

		for _, ev := range events {
			evStart, evEnd, err := parseEventTime(ev)
			if err != nil {
				continue
			}
			// Clip event to the day window
			if evStart.Before(dayStart) {
				evStart = dayStart
			}
			if evEnd.After(dayEnd) {
				evEnd = dayEnd
			}
			// Skip if completely outside the day window
			if !evStart.Before(dayEnd) || !evEnd.After(dayStart) {
				continue
			}
			busy = append(busy, busyInterval{start: evStart, end: evEnd})
		}

		// Sort busy intervals by start time
		sort.Slice(busy, func(i, j int) bool {
			return busy[i].start.Before(busy[j].start)
		})

		// Merge overlapping busy intervals
		merged := make([]busyInterval, 0)
		for _, b := range busy {
			if len(merged) == 0 {
				merged = append(merged, b)
				continue
			}
			last := &merged[len(merged)-1]
			if b.start.Before(last.end) || b.start.Equal(last.end) {
				// Overlapping or adjacent: extend last interval
				if b.end.After(last.end) {
					last.end = b.end
				}
			} else {
				merged = append(merged, b)
			}
		}

		// Find gaps between merged busy intervals within the day window
		slots := make([]AvailableSlot, 0)
		cursor := dayStart

		for _, b := range merged {
			// Gap from cursor to start of busy interval
			if b.start.After(cursor) {
				gapEnd := b.start
				if gapEnd.Sub(cursor) >= duration {
					slots = append(slots, AvailableSlot{
						Start: cursor.Format(time.RFC3339),
						End:   gapEnd.Format(time.RFC3339),
					})
				}
			}
			// Move cursor past this busy interval
			if b.end.After(cursor) {
				cursor = b.end
			}
		}

		// Gap from cursor to end of day
		if dayEnd.After(cursor) {
			if dayEnd.Sub(cursor) >= duration {
				slots = append(slots, AvailableSlot{
					Start: cursor.Format(time.RFC3339),
					End:   dayEnd.Format(time.RFC3339),
				})
			}
		}

		data, err := json.Marshal(slots)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}

	return tool, handler
}

// ─────────────────────────────────────────────
// 7. get_daily_briefing
// ─────────────────────────────────────────────

// NewGetDailyBriefingTool creates the get_daily_briefing tool and its handler.
func NewGetDailyBriefingTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("get_daily_briefing",
		mcp.WithDescription("Get a formatted daily briefing with events, maintenance tasks due, and bills due for a given date"),
		mcp.WithString("date", mcp.Description("Date (YYYY-MM-DD). Defaults to today.")),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)

		args := req.GetArguments()
		dateStr := getStringArg(args, "date")
		var date time.Time
		if dateStr != "" {
			var err error
			date, err = parseDate(dateStr, loc)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid date format (use YYYY-MM-DD): %v", err)), nil
			}
		} else {
			now := time.Now().In(loc)
			date = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		}

		dayStart := date
		dayEnd := date.Add(24 * time.Hour)
		dateDisplay := date.Format("Monday, January 2, 2006")

		var briefing strings.Builder
		briefing.WriteString(fmt.Sprintf("# Daily Briefing — %s\n\n", dateDisplay))

		// ── Events ──
		events, eventsErr := queryEvents(ctx, pool, claims.HouseholdID, nil, dayStart, dayEnd)
		if eventsErr != nil {
			slog.Error("get_daily_briefing: query events error", "error", eventsErr)
		}

		// Filter events to this date
		todaysEvents := make([]Event, 0)
		if eventsErr == nil {
			for _, ev := range events {
				if eventOverlapsRange(ev, dayStart, dayEnd) {
					todaysEvents = append(todaysEvents, ev)
				}
			}
		}

		// Sort events by start time
		sort.Slice(todaysEvents, func(i, j int) bool {
			return todaysEvents[i].Start < todaysEvents[j].Start
		})

		briefing.WriteString(fmt.Sprintf("## 📅 Events (%d)\n\n", len(todaysEvents)))
		if eventsErr != nil {
			briefing.WriteString("⚠️ Could not load events (database error) — do not assume the day is free.\n\n")
		} else if len(todaysEvents) == 0 {
			briefing.WriteString("No events scheduled for today.\n\n")
		} else {
			for _, ev := range todaysEvents {
				startT, err := time.Parse(time.RFC3339, ev.Start)
				timeStr := ev.Start
				if err == nil {
					timeStr = startT.Format("3:04 PM")
				}
				if ev.AllDay {
					timeStr = "All day"
				}
				calInfo := ""
				if ev.CalendarName != "" {
					calInfo = fmt.Sprintf(" [%s]", ev.CalendarName)
				}
				briefing.WriteString(fmt.Sprintf("- **%s** — %s%s\n", timeStr, ev.Title, calInfo))
				if ev.Description != "" {
					briefing.WriteString(fmt.Sprintf("  %s\n", ev.Description))
				}
				if ev.Location != "" {
					briefing.WriteString(fmt.Sprintf("  📍 %s\n", ev.Location))
				}
			}
			briefing.WriteString("\n")
		}

		// ── Maintenance Tasks Due ──
		briefing.WriteString("## 🔧 Maintenance Tasks Due\n\n")
		mtRows, mtErr := pool.Query(ctx,
			`SELECT name, description, status, property_id
			 FROM maintenance_tasks
			 WHERE household_id = $1 AND due_date = $2
			 ORDER BY name`,
			claims.HouseholdID, date,
		)
		if mtErr != nil {
			slog.Error("get_daily_briefing: query maintenance error", "error", mtErr)
		}

		mtCount := 0
		if mtRows != nil {
			defer mtRows.Close()
			for mtRows.Next() {
				mtCount++
				var name, status string
				var description, propertyID *string
				if err := mtRows.Scan(&name, &description, &status, &propertyID); err != nil {
					slog.Error("get_daily_briefing: scan maintenance error", "error", err)
					continue
				}
				briefing.WriteString(fmt.Sprintf("- **%s** [%s]", name, status))
				if description != nil && *description != "" {
					briefing.WriteString(fmt.Sprintf(" — %s", *description))
				}
				briefing.WriteString("\n")
			}
			if err := mtRows.Err(); err != nil {
				slog.Error("get_daily_briefing: iterate maintenance error", "error", err)
			}
		}

		if mtErr != nil {
			briefing.WriteString("⚠️ Could not load maintenance tasks (database error) — do not assume no tasks are due.\n\n")
		} else if mtCount == 0 {
			briefing.WriteString("No maintenance tasks due today.\n\n")
		} else {
			briefing.WriteString("\n")
		}

		// ── Bills Due ──
		briefing.WriteString("## 💰 Bills Due\n\n")
		dueDay := date.Day()
		// Use COALESCE for entity_type to handle NULL values
		billRows, billErr := pool.Query(ctx,
			`SELECT name, COALESCE(amount, 0), category
			 FROM bills
			 WHERE household_id = $1 AND due_day = $2 AND paid_date IS NULL
			 ORDER BY name`,
			claims.HouseholdID, dueDay,
		)
		if billErr != nil {
			slog.Error("get_daily_briefing: query bills error", "error", billErr)
		}

		billCount := 0
		if billRows != nil {
			defer billRows.Close()
			for billRows.Next() {
				billCount++
				var name, category string
				var amount float64
				if err := billRows.Scan(&name, &amount, &category); err != nil {
					slog.Error("get_daily_briefing: scan bill error", "error", err)
					continue
				}
				catInfo := ""
				if category != "" {
					catInfo = fmt.Sprintf(" [%s]", category)
				}
				briefing.WriteString(fmt.Sprintf("- **%s** — $%.2f%s\n", name, amount, catInfo))
			}
			if err := billRows.Err(); err != nil {
				slog.Error("get_daily_briefing: iterate bills error", "error", err)
			}
		}

		if billErr != nil {
			briefing.WriteString("⚠️ Could not load bills (database error) — do not assume no bills are due.\n")
		} else if billCount == 0 {
			briefing.WriteString("No bills due today.\n")
		}

		return mcp.NewToolResultText(briefing.String()), nil
	}

	return tool, handler
}

// ─────────────────────────────────────────────
// 8. check_conflicts
// ─────────────────────────────────────────────

// NewCheckConflictsTool creates the check_conflicts tool and its handler.
func NewCheckConflictsTool(pool *pgxpool.Pool) (mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	tool := mcp.NewTool("check_conflicts",
		mcp.WithDescription("Check for conflicting events in a given time range. Returns events that overlap the proposed time."),
		mcp.WithString("start_time", mcp.Description("Proposed start time (ISO 8601)"), mcp.Required()),
		mcp.WithString("end_time", mcp.Description("Proposed end time (ISO 8601)"), mcp.Required()),
		mcp.WithArray("calendar_ids", mcp.Description("Optional list of calendar IDs to check (checks all calendars if omitted)")),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("authentication required"), nil
		}

		args := req.GetArguments()
		startTimeStr := getStringArg(args, "start_time")
		endTimeStr := getStringArg(args, "end_time")

		if startTimeStr == "" {
			return mcp.NewToolResultError("start_time is required"), nil
		}
		if endTimeStr == "" {
			return mcp.NewToolResultError("end_time is required"), nil
		}

		proposedStart, err := time.Parse(time.RFC3339, startTimeStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid start_time format (use ISO 8601): %v", err)), nil
		}
		proposedEnd, err := time.Parse(time.RFC3339, endTimeStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid end_time format (use ISO 8601): %v", err)), nil
		}
		if !proposedEnd.After(proposedStart) {
			return mcp.NewToolResultError("end_time must be after start_time"), nil
		}

		calendarIDs := getStringSliceArg(args, "calendar_ids")

		// Query all events for the household (or filtered calendars)
		events, err := queryEvents(ctx, pool, claims.HouseholdID, calendarIDs, proposedStart, proposedEnd)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("query events: %v", err)), nil
		}

		// Filter to events that overlap the proposed time range
		conflicts := make([]ConflictEvent, 0)
		for _, ev := range events {
			if eventOverlapsRange(ev, proposedStart, proposedEnd) {
				conflicts = append(conflicts, ConflictEvent{
					ID:           ev.ID,
					CalendarID:   ev.CalendarID,
					Title:        ev.Title,
					Start:        ev.Start,
					End:          ev.End,
					CalendarName: ev.CalendarName,
				})
			}
		}

		// Sort by start time
		slices.SortFunc(conflicts, func(a, b ConflictEvent) int {
			if a.Start < b.Start {
				return -1
			}
			if a.Start > b.Start {
				return 1
			}
			return 0
		})

		data, err := json.Marshal(conflicts)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	}

	return tool, handler
}