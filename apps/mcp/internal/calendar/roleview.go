package calendar

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/bill"
	"home-os/mcp/internal/respond"
)

// agendaView is what the caller's role may see of agenda_get (#4717). The
// tool is an entry point for every role, so it trims sections rather than
// refusing:
//
//   - bills: money (auth.CanSeeMoney), so a housesitter and a vendor get no
//     bills section at all. An empty list would read as "nothing is due",
//     which is a lie about the family's money.
//   - maintenance: the maintenance read grant, so a housesitter gets none;
//     a vendor linked to a vendors row sees only their own jobs (#4799).
//   - events: the calendar read grant, so a vendor gets no events.
//   - lists: the list read grant, so a vendor gets no list items.
//
// Meals keep their own existing gate (meals.AgendaMeals answers
// allowed=false for a vendor).
type agendaView struct {
	bills       bool
	maintenance bool
	events      bool
	lists       bool
	ownJobs     *uuid.UUID
	// role is the caller's role, so agenda_get reads its events with the same
	// per-record view event_find uses (#4808): a bill's mirror event is not an
	// agenda item for a role that may not see money, even though it is not
	// listed as an event either way (isBillEvent).
	role string
}

// agendaViewFor resolves the caller's current role and vendor link from
// memberships on every call, so a demotion or a new link applies at once. A
// non-nil result is the isError to return.
func agendaViewFor(ctx context.Context, pool *pgxpool.Pool, c *auth.Claims) (agendaView, *mcp.CallToolResult) {
	role, vendorID, bad := callerRoleOf(ctx, pool, c)
	if bad != nil {
		return agendaView{}, bad
	}
	return newAgendaView(role, vendorID), nil
}

// newAgendaView decides what role may see of the agenda.
func newAgendaView(role string, vendorID *uuid.UUID) agendaView {
	return agendaView{
		bills:       auth.CanSeeMoney(role),
		maintenance: auth.CanSeeEntity(role, "maintenance_task"),
		events:      auth.CanSeeEntity(role, "calendar_event"),
		lists:       auth.CanSeeEntity(role, "list"),
		ownJobs:     vendorID,
		role:        role,
	}
}

// ---------------------------------------------------------------------------
// Event role view (#4808): which calendars and events a role may see
// ---------------------------------------------------------------------------

// eventView is what the caller's role may see of the calendar, record by
// record. It is the MCP half of #4807 (apps/api/internal/calendar/access.go)
// and answers the same question CalDAV asks (#4718), so a housesitter's three
// clients agree.
//
// A record's kind comes from what it is: a bill's mirror event is a bill, a
// maintenance event is a maintenance task, and the Bills calendar is bills. The
// policy table (auth) then decides, per kind, so nothing here names a role:
//
//   - A bill mirror's title is the bill's NAME and its description carries
//     "Amount: $X", the category, autopay and the payment URL, so the whole
//     record is dropped rather than stripped: no partial version of it is safe.
//   - A housesitter keeps every ordinary event (kind 'calendar_event') and
//     loses the Bills calendar and its mirrors. A vendor never reaches the
//     calendar (ResCalendar read is sitter-only, so requirePolicy refuses the
//     tool outright); this view still answers for it, for the same rule.
//   - Unknown kinds, roles and calendar types fail closed.
type eventView struct {
	role string
}

// eventViewFor re-reads the caller's role from memberships (claims' role in
// auth-disabled local mode) so a demotion applies at once. A non-nil result is
// the isError to return.
func eventViewFor(ctx context.Context, pool *pgxpool.Pool, c *auth.Claims) (eventView, *mcp.CallToolResult) {
	role, _, bad := callerRoleOf(ctx, pool, c)
	if bad != nil {
		return eventView{}, bad
	}
	return eventView{role: role}, nil
}

// anyEvent reports whether a role can see any calendar event at all. A role
// that cannot gets no calendar and no event, an empty list rather than a hole
// in the rule.
func (v eventView) anyEvent() bool { return auth.CanSeeEntity(v.role, "calendar_event") }

// calendarVisible reports whether the role may see a calendar of the given
// calendars.type.
func (v eventView) calendarVisible(calendarType string) bool {
	return auth.CanSeeEntity(v.role, calendarKind(calendarType))
}

// eventVisible reports whether the role may see one event: both its calendar's
// kind and its own kind must be visible, so an ordinary event a person linked
// to a bill is a bill (the link, not the calendar, is what makes it a money
// record), and a bill mirror on a custom calendar stays a bill.
func (v eventView) eventVisible(calendarType, eventType string, entityType *string) bool {
	return v.calendarVisible(calendarType) && auth.CanSeeEntity(v.role, recordKind(eventType, entityType))
}

// recordKind maps an event row to the kind of record it is: the record it
// mirrors when it mirrors one (event_type or entity_type), else an ordinary
// calendar event.
func recordKind(eventType string, entityType *string) string {
	switch {
	case eventType == bill.CalendarEventType || (entityType != nil && *entityType == "bill"):
		return "bill"
	case eventType == "maintenance", entityType != nil && (*entityType == "maintenance_task" || *entityType == "maintenance"):
		return "maintenance_task"
	default:
		return "calendar_event"
	}
}

// rowVisible reports whether the role may see the event a calendar_objects row
// holds. Every calendar read drops an invisible ROW before it is parsed or
// expanded, so all of a series' occurrences go with it and no read can forget
// the rule.
func (v eventView) rowVisible(r eventRow) bool {
	return v.eventVisible(r.CalendarType, r.EventType, r.EntityType)
}

// keepCalendars returns the calendars of cals the role may see, in order.
func (v eventView) keepCalendars(cals []Calendar) []Calendar {
	if !v.anyEvent() {
		return []Calendar{}
	}
	out := make([]Calendar, 0, len(cals))
	for _, c := range cals {
		if v.calendarVisible(c.Type) {
			out = append(out, c)
		}
	}
	return out
}

// callerRoleOf re-reads the caller's current role from memberships for the
// claims' user and household (claims' role in auth-disabled local mode; an
// agent token always resolves to a users.id, so one that cannot be read is
// refused). A non-nil result is the isError to return.
func callerRoleOf(ctx context.Context, pool *pgxpool.Pool, c *auth.Claims) (string, *uuid.UUID, *mcp.CallToolResult) {
	hid, err := uuid.Parse(c.HouseholdID)
	if err != nil {
		return "", nil, authRequired()
	}
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.AgentTokenID != "" {
			return "", nil, authRequired()
		}
		var exists bool
		if qerr := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM households WHERE id = $1)`, hid).Scan(&exists); qerr == nil && !exists {
			return "", nil, authRequired()
		}
		return c.Role, nil, nil
	}
	var (
		role     string
		vendorID *uuid.UUID
	)
	err = pool.QueryRow(ctx,
		`SELECT role::text, CASE WHEN role = 'vendor' THEN vendor_id END
		 FROM memberships WHERE household_id = $1 AND user_id = $2`, hid, uid).Scan(&role, &vendorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, authRequired()
	}
	if err != nil {
		return "", nil, respond.Failed("check your access", err)
	}
	return role, vendorID, nil
}
