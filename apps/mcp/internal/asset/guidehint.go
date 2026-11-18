package asset

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/server"
)

// Guide roles follow the API's ResGuides policy (internal/guides canRead and
// canWrite): the family roles write guides, they plus viewer and housesitter
// read them, a vendor and unknown roles do neither (fail closed).
var (
	guideWriters = map[string]bool{"owner": true, "family_manager": true, "family_member": true}
	guideReaders = map[string]bool{
		"owner": true, "family_manager": true, "family_member": true, "viewer": true, "housesitter": true,
	}
)

// guideFindHint and guideCreateHint are the guide next steps for one asset.
func guideFindHint(id string) string {
	return fmt.Sprintf("How to look after it: guide_find about_type=asset about_id=%s.", id)
}

func guideCreateHint(id string) string {
	return fmt.Sprintf("Write how to look after it with guide_create about_type=asset about_id=%s title=… body=….", id)
}

// guideState re-reads the caller's role from memberships (claims' role in
// auth-disabled local mode) and, when checkGuide, whether a guide about the
// asset exists, in one query. role is "" when the caller has no membership.
func guideState(ctx context.Context, pool *pgxpool.Pool, assetID string, checkGuide bool) (role string, exists bool, err error) {
	c := server.ClaimsFromContext(ctx)
	if c == nil {
		return "", false, nil
	}
	var uid *uuid.UUID
	if u, perr := uuid.Parse(c.UserID); perr == nil {
		uid = &u
	}
	var r *string
	err = pool.QueryRow(ctx,
		`SELECT CASE WHEN $2::uuid IS NULL THEN $3
		             ELSE (SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2) END,
		        $5 AND EXISTS (SELECT 1 FROM guides WHERE household_id = $1 AND asset_id = $4)`,
		c.HouseholdID, uid, c.Role, assetID, checkGuide,
	).Scan(&r, &exists)
	if err != nil || r == nil {
		return "", false, err
	}
	return *r, exists, nil
}

// guideHint is asset_find's guide next step for one asset (#4770): guide_find
// to a reader when a guide about it exists, otherwise guide_create to a
// family role. Best effort: a failed check gives no hint.
func guideHint(ctx context.Context, pool *pgxpool.Pool, assetID string) string {
	role, exists, err := guideState(ctx, pool, assetID, true)
	if err != nil {
		slog.Warn("asset: guide hint check failed", "err", err)
		return ""
	}
	switch {
	case exists && guideReaders[role]:
		return guideFindHint(assetID)
	case !exists && guideWriters[role]:
		return guideCreateHint(assetID)
	}
	return ""
}

// createGuideHint is asset_create's guide_create next step for a family
// role. A new asset has no guide yet, so only the role is checked.
func createGuideHint(ctx context.Context, pool *pgxpool.Pool, assetID string) string {
	role, _, err := guideState(ctx, pool, assetID, false)
	if err != nil {
		slog.Warn("asset: guide hint check failed", "err", err)
		return ""
	}
	if guideWriters[role] {
		return guideCreateHint(assetID)
	}
	return ""
}
