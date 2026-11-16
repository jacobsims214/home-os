package calendar

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
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
	// role is the caller's current role, for the per-kind event rule #4807
	// applies (an upkeep job's mirror is not an appointment).
	role string
}

// agendaViewFor resolves the caller's current role and vendor link from
// memberships on every call, so a demotion or a new link applies at once. A
// non-nil result is the isError to return.
func agendaViewFor(ctx context.Context, pool *pgxpool.Pool, c *auth.Claims) (agendaView, *mcp.CallToolResult) {
	hid, err := uuid.Parse(c.HouseholdID)
	if err != nil {
		return agendaView{}, authRequired()
	}
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.AgentTokenID != "" {
			return agendaView{}, authRequired()
		}
		// Local-dev claims without a user: keep the claims' role.
		var exists bool
		if qerr := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM households WHERE id = $1)`, hid).Scan(&exists); qerr == nil && !exists {
			return agendaView{}, authRequired()
		}
		return newAgendaView(c.Role, nil), nil
	}
	var (
		role     string
		vendorID *uuid.UUID
	)
	err = pool.QueryRow(ctx,
		`SELECT role::text, CASE WHEN role = 'vendor' THEN vendor_id END
		 FROM memberships WHERE household_id = $1 AND user_id = $2`, hid, uid).Scan(&role, &vendorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return agendaView{}, authRequired()
	}
	if err != nil {
		return agendaView{}, respond.Failed("check your access", err)
	}
	return newAgendaView(role, vendorID), nil
}

// newAgendaView decides what role may see of the agenda.
func newAgendaView(role string, vendorID *uuid.UUID) agendaView {
	return agendaView{
		role:        role,
		bills:       auth.CanSeeMoney(role),
		maintenance: auth.CanSeeEntity(role, "maintenance_task"),
		events:      auth.CanSeeEntity(role, "calendar_event"),
		lists:       auth.CanSeeEntity(role, "list"),
		ownJobs:     vendorID,
	}
}
