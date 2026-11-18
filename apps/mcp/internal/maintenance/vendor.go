package maintenance

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/respond"
)

// vendorScope returns the vendors row the caller's vendor membership is
// linked to (memberships.vendor_id, migration 064, #4799), or nil. A linked
// vendor sees only maintenance tasks with that vendor_id: maintenance_find
// lists and gets only those, the same rule the API, search_all and CalDAV
// apply. Every other caller gets nil, including a vendor without a link,
// who still sees every job (role policy decision, KB
// decisions/2026-11-15-role-policy.md). Role and link are re-read from
// memberships on every call, so a role change or a new link applies at
// once. A caller the claims call a vendor whose membership cannot be read
// is refused (fail closed).
func vendorScope(ctx context.Context, pool *pgxpool.Pool, c *auth.Claims) (*uuid.UUID, *mcp.CallToolResult) {
	_, vendorID, fail := callerAccess(ctx, pool, c)
	return vendorID, fail
}

// callerAccess resolves the caller's current role AND vendor link from
// memberships in one query, so the narrow-write decision (statusOnly) and the
// own-jobs scope come from the same reading: a member demoted from owner to
// vendor between two calls cannot be treated as an owner by one check and a
// vendor by the other. Without a users.id (auth-disabled local development)
// the claims' role is used and there is no link; an agent token always
// resolves to a users.id, so one that cannot be read is refused.
func callerAccess(ctx context.Context, pool *pgxpool.Pool, c *auth.Claims) (string, *uuid.UUID, *mcp.CallToolResult) {
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.AgentTokenID != "" {
			return "", nil, authRequired()
		}
		return c.Role, nil, nil // auth-disabled local mode: no users.id, no link
	}
	var (
		role     string
		vendorID *uuid.UUID
	)
	err = pool.QueryRow(ctx,
		`SELECT role::text, CASE WHEN role = 'vendor' THEN vendor_id END
		 FROM memberships WHERE household_id = $1 AND user_id = $2`,
		c.HouseholdID, uid,
	).Scan(&role, &vendorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, authRequired()
	}
	if err != nil {
		return "", nil, respond.Failed("check your access", err)
	}
	return role, vendorID, nil
}

// roleCanWrite reports whether role may change any field of a maintenance
// task (API ResMaintenance write: the family roles). A role that may only
// ActUpdateStatus (a vendor) may change a task's status, completion and notes
// and nothing else, the narrow write the role policy grants (#4689, #4717).
func roleCanWrite(role string) bool { return auth.Can(role, auth.ResMaintenance, auth.ActWrite) }

// statusOnly reports whether role is limited to the narrow maintenance write.
func statusOnly(role string) bool {
	return !roleCanWrite(role) && auth.Can(role, auth.ResMaintenance, auth.ActUpdateStatus)
}

// cannotChangeMessage is the refusal for a role that may only change a task's
// status, completion and notes, naming what it asked to change.
func cannotChangeMessage(role, field string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("The %s role can only change a task's status, completion and notes, not its %s (tool maintenance_update).",
			auth.RoleLabel(role), field),
		Fix: "Mark the job done with maintenance_complete, or add a note with maintenance_update notes=…; ask an owner or family manager for anything else.",
	})
}
