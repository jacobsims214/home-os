package calendar

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// ─────────────────────────────────────────────
// calendar_create
// ─────────────────────────────────────────────

// maxCalendarName is calendars.name's VARCHAR(255) limit, in runes.
const maxCalendarName = 255

// colorPattern is the catalog's color format, #RRGGBB.
var colorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// NewCalendarCreateTool creates calendar_create: a new custom calendar in the
// caller's family. The row gets its own caldav_uid and ctag, so CalDAV
// discovery (apps/calendar ListCalendars) lists it on phones at their next
// sync. A name already used in the family (case-insensitive) is refused with
// the existing calendar's id, because calendar_id resolves names and two
// calendars with one name would make every name lookup ambiguous.
func NewCalendarCreateTool(pool *pgxpool.Pool) (mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("calendar_create", toolOptions(classC, "Create calendar",
		mcp.WithDescription(`Create a calendar (e.g. "Kids", "Pets"). Phones see it over CalDAV. Then pass its name as calendar_id to event_create.`),
		mcp.WithString("name", mcp.Required(), mcp.Description(`Calendar name, unique in the family, e.g. "Kids".`)),
		mcp.WithString("color", mcp.Description(`Color as #RRGGBB, e.g. "#10B981".`)),
		respond.Schema[Calendar](),
	)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return authRequired(), nil
		}
		args := req.GetArguments()
		name, color, bad := calendarCreateArgs(args)
		if bad != nil {
			return bad, nil
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return respond.Failed("create the calendar", err), nil
		}
		defer func() { _ = tx.Rollback(ctx) }()

		// Serialize creates of the same name in one family, so two parallel
		// calls can't both pass the duplicate check. There is no unique index
		// on (household_id, lower(name)): older families may already hold
		// duplicates, and this tool must not depend on a migration.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			"calendar_create:"+claims.HouseholdID+":"+strings.ToLower(name)); err != nil {
			return respond.Failed("create the calendar", err), nil
		}

		existing, err := scanCalendars(ctx, tx, `SELECT id::text, name, type::text, color, property_id::text
			FROM calendars WHERE household_id = $1 AND lower(name) = lower($2) ORDER BY created_at LIMIT 1`,
			claims.HouseholdID, name)
		if err != nil {
			return respond.Failed("create the calendar", err), nil
		}
		if len(existing) > 0 {
			return duplicateCalendar(name, existing[0]), nil
		}

		var cal Calendar
		err = tx.QueryRow(ctx,
			`INSERT INTO calendars (household_id, name, type, color, caldav_uid)
			 VALUES ($1, $2, 'custom', $3, gen_random_uuid()::text)
			 RETURNING id::text, name, type::text, color, property_id::text`,
			claims.HouseholdID, name, color,
		).Scan(&cal.ID, &cal.Name, &cal.Type, &cal.Color, &cal.PropertyID)
		if err != nil {
			slog.Error("calendar_create: insert error", "error", err)
			return respond.Failed("create the calendar", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("create the calendar", err), nil
		}

		return respond.OK(
			fmt.Sprintf("Created calendar %q. Phones show it after their next calendar sync.", cal.Name),
			cal,
			fmt.Sprintf("Add events to it with event_create calendar_id=%q.", cal.Name),
			"List all calendars with calendar_find.",
		), nil
	}
	return tool, handler
}

// calendarCreateArgs validates calendar_create's name and color. color is
// nil when not given (CalDAV then shows its default color).
func calendarCreateArgs(args map[string]any) (string, *string, *mcp.CallToolResult) {
	name := strings.Join(strings.Fields(getStringArg(args, "name")), " ")
	switch {
	case name == "":
		return "", nil, respond.Invalid("name", "name is required.",
			"Pass the new calendar's name.", map[string]any{"name": "Kids"})
	case utf8.RuneCountInString(name) > maxCalendarName:
		return "", nil, respond.Invalid("name",
			fmt.Sprintf("name is %d characters; the limit is %d.", utf8.RuneCountInString(name), maxCalendarName),
			"Pass a shorter name.", map[string]any{"name": "Kids"})
	case isUUID(name):
		// calendar_id treats a UUID as an id, so this name could never be
		// used to pick the calendar.
		return "", nil, respond.Invalid("name", "name looks like an id, not a calendar name.",
			"Pass a readable name such as \"Kids\" or \"Pets\".", map[string]any{"name": "Kids"})
	}

	raw := strings.TrimSpace(getStringArg(args, "color"))
	if raw == "" {
		return name, nil, nil
	}
	if !colorPattern.MatchString(raw) {
		return "", nil, respond.Invalid("color", fmt.Sprintf("color %q is not #RRGGBB.", raw),
			"Pass a hex color with # and six digits, or omit color.",
			map[string]any{"name": name, "color": "#10B981"})
	}
	color := strings.ToUpper(raw)
	return name, &color, nil
}

// duplicateCalendar is the corrective error for a name the family already
// uses: it names the existing calendar and both ways forward.
func duplicateCalendar(name string, existing Calendar) *mcp.CallToolResult {
	return respond.Invalid("name",
		fmt.Sprintf("The family already has a calendar named %q (id %s).", existing.Name, existing.ID),
		fmt.Sprintf("To add events to it, call event_create with calendar_id=%q; nothing needs creating. "+
			"For a separate calendar, pass a different name.", existing.Name),
		map[string]any{"name": name + " 2"})
}
