package maintenance

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
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.Role == "vendor" {
			return nil, authRequired()
		}
		return nil, nil // auth-disabled local mode: no users.id, no link
	}
	var vendorID *uuid.UUID
	err = pool.QueryRow(ctx,
		`SELECT CASE WHEN role = 'vendor' THEN vendor_id END
		 FROM memberships WHERE household_id = $1 AND user_id = $2`,
		c.HouseholdID, uid,
	).Scan(&vendorID)
	if errors.Is(err, pgx.ErrNoRows) {
		if c.Role == "vendor" {
			return nil, authRequired()
		}
		return nil, nil
	}
	if err != nil {
		return nil, respond.Failed("check your access", err)
	}
	return vendorID, nil
}
