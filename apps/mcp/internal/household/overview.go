package household

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// familyHeader is family_overview.family: who the family is and what day it
// is for them.
type familyHeader struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Timezone string `json:"timezone" jsonschema_description:"IANA timezone, e.g. America/New_York"`
	Today    string `json:"today" jsonschema_description:"Today's date in the family timezone, YYYY-MM-DD"`
	Weekday  string `json:"weekday" jsonschema_description:"Today's weekday in the family timezone, e.g. Thu"`
}

// calendarRow is one entry of family_overview.calendars.
type calendarRow struct {
	ID    string  `json:"id" jsonschema_description:"calendar_id; a calendar name also works wherever calendar_id is asked for"`
	Name  string  `json:"name"`
	Type  string  `json:"type"`
	Color *string `json:"color"`
}

// overview is the data of a family_overview result. Sections follow the
// catalog order; #4385 adds counts, next_days and unread_notifications.
type overview struct {
	Family    familyHeader  `json:"family"`
	People    []personRow   `json:"people"`
	Calendars []calendarRow `json:"calendars"`
	HowTo     []string      `json:"how_to" jsonschema_description:"Short hints on using the Home OS tools"`
}

// howTo is family_overview.how_to: conventions every tool shares.
var howTo = []string{
	"Dates are YYYY-MM-DD in the family timezone; times are ISO 8601 with an offset, e.g. 2026-11-19T17:30:00-05:00.",
	"Every date in a result also carries local_date and weekday; trust those over your own weekday math.",
	"find tools list and search in one call; pass id for exactly one full record.",
	"When a list is truncated, pass the cursor from narrow to get the next page.",
	"person_id comes from people[].id here or from person_find.",
	"Change the family's name or timezone with family_update.",
}

// NewFamilyOverviewTool creates family_overview (catalog v2; replaces
// get_household and is what protocol tests call instead of ping). This is
// the basic version: family, people and calendars. #4385 adds counts, the
// next days and unread notifications.
func NewFamilyOverviewTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("family_overview",
		mcp.WithDescription("Start here. One call returns the family's name, timezone, today's date and weekday, people, calendars, counts per area, the next 7 days in brief, and how to use these tools."),
		mcp.WithNumber("days",
			mcp.Description("Days ahead to preview (1-31, default 7)"),
			mcp.Min(1), mcp.Max(31),
		),
		mcp.WithTitleAnnotation("Family overview"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.Schema[overview](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}
		// days is accepted and checked now so callers can rely on it; the
		// next_days section it sizes arrives with #4385.
		if days, given, ok := intArg(req.GetArguments(), "days"); !ok || (given && (days < 1 || days > 31)) {
			return respond.Invalid("days", "days must be a whole number from 1 to 31.",
				"Pass days as a number, or leave it out for 7.", map[string]any{"days": 7}), nil
		}

		var ov overview
		err := pool.QueryRow(ctx,
			`SELECT id::text, name, timezone FROM households WHERE id = $1`,
			claims.HouseholdID,
		).Scan(&ov.Family.ID, &ov.Family.Name, &ov.Family.Timezone)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("household", claims.HouseholdID,
				"Tell the user to reconnect Home OS; retrying will not help."), nil
		}
		if err != nil {
			return respond.Failed("load the family", err), nil
		}
		ov.Family.Today, ov.Family.Weekday = familyClock(time.Now(), ov.Family.Timezone)

		rows, err := pool.Query(ctx, peopleSelect+` ORDER BY m.created_at, p.id`, claims.HouseholdID)
		if err != nil {
			return respond.Failed("load the family's people", err), nil
		}
		if ov.People, _, err = scanPeople(rows); err != nil {
			return respond.Failed("load the family's people", err), nil
		}

		ov.Calendars = []calendarRow{}
		rows, err = pool.Query(ctx,
			`SELECT id::text, name, type::text, color FROM calendars WHERE household_id = $1 ORDER BY name, id`,
			claims.HouseholdID)
		if err != nil {
			return respond.Failed("load the family's calendars", err), nil
		}
		ov.Calendars, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (calendarRow, error) {
			var c calendarRow
			err := r.Scan(&c.ID, &c.Name, &c.Type, &c.Color)
			return c, err
		})
		if err != nil {
			return respond.Failed("load the family's calendars", err), nil
		}
		if ov.Calendars == nil {
			ov.Calendars = []calendarRow{}
		}
		ov.HowTo = howTo

		summary := fmt.Sprintf("%s: %d %s, %d %s. Today is %s %s (%s).",
			ov.Family.Name,
			len(ov.People), plural(len(ov.People), "person", "people"),
			len(ov.Calendars), plural(len(ov.Calendars), "calendar", "calendars"),
			ov.Family.Weekday, ov.Family.Today, ov.Family.Timezone)
		return respond.OK(summary, ov,
			fmt.Sprintf("See what's on with agenda_get from=%s.", ov.Family.Today),
			"Add an event with event_create.",
			"Look someone up with person_find.",
		), nil
	}
	return tool.Name, tool, handler
}
