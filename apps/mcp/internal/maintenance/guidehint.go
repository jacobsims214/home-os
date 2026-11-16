package maintenance

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
)

// guideReaders are the roles that may read guides (API ResGuides read,
// rolesSitter; internal/guides canRead). A vendor and unknown roles get no
// guide hint, so a new role fails closed.
var guideReaders = map[string]bool{
	"owner": true, "family_manager": true, "family_member": true, "viewer": true, "housesitter": true,
}

// guideHint is the guide_find next step for one task (#4770): pointed at the
// task's subject (its asset, else its car, else its home), and only when a
// guide about that subject exists and the caller may read guides, so the
// hint never sends a model to an empty search or a refusal. The role is
// re-read from memberships (claims' role in auth-disabled local mode) in the
// same query as the existence check. It is best effort: a failed check
// gives no hint. "" when there is nothing to hint.
func guideHint(ctx context.Context, pool *pgxpool.Pool, c *auth.Claims, d taskData) string {
	kind, id := "", ""
	switch {
	case d.AssetID != nil:
		kind, id = "asset", *d.AssetID
	case d.VehicleID != nil:
		kind, id = "vehicle", *d.VehicleID
	case d.PropertyID != nil:
		kind, id = "property", *d.PropertyID
	default:
		return ""
	}
	var uid *uuid.UUID
	if u, err := uuid.Parse(c.UserID); err == nil {
		uid = &u
	}
	var role *string
	var exists bool
	// kind is one of three constants, so the column name is not user input.
	err := pool.QueryRow(ctx,
		`SELECT CASE WHEN $2::uuid IS NULL THEN $3
		             ELSE (SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2) END,
		        EXISTS (SELECT 1 FROM guides WHERE household_id = $1 AND `+kind+`_id = $4)`,
		c.HouseholdID, uid, c.Role, id,
	).Scan(&role, &exists)
	if err != nil {
		slog.Warn("maintenance: guide hint check failed", "err", err)
		return ""
	}
	if !exists || role == nil || !guideReaders[*role] {
		return ""
	}
	return fmt.Sprintf("Steps for this job: guide_find about_type=%s about_id=%s.", kind, id)
}
