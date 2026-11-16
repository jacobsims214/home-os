package notification

// #4717: maintenance_due is family-wide (user_id NULL), so without a filter a
// vendor linked to a vendors row would read the name and description of every
// upkeep job in the family through notification_find, its counts and
// family_overview's unread_notifications (#4799's remaining gap).

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// TestMaintenanceDueLimitedToOwnJobs: a linked vendor sees only the
// maintenance_due notices about their own jobs; an unlinked vendor and every
// other role see them all.
func TestMaintenanceDueLimitedToOwnJobs(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, _, find := NewNotificationFindTool(pool)

	mineVendor := insertID(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Acme') RETURNING id`, hh.HouseholdID)
	otherVendor := insertID(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Other') RETURNING id`, hh.HouseholdID)

	mineTask := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, status, vendor_id)
		VALUES ($1, 'My job', CURRENT_DATE, 'pending', $2) RETURNING id`, hh.HouseholdID, mineVendor)
	otherTask := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, status, vendor_id)
		VALUES ($1, 'Their job', CURRENT_DATE, 'pending', $2) RETURNING id`, hh.HouseholdID, otherVendor)
	unassignedTask := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, status)
		VALUES ($1, 'Nobody''s job', CURRENT_DATE, 'pending') RETURNING id`, hh.HouseholdID)

	mineN := notify(t, pool, hh.HouseholdID, "", "maintenance_due", "My job due soon", "maintenance_task", mineTask, time.Hour)
	otherN := notify(t, pool, hh.HouseholdID, "", "maintenance_due", "Their job due soon", "maintenance_task", otherTask, time.Hour)
	unassignedN := notify(t, pool, hh.HouseholdID, "", "maintenance_due", "Nobody's job due soon", "maintenance_task", unassignedTask, time.Hour)

	vendorCtx, uid := vendorMember(t, pool, hh, mineVendor)

	env := call[findResult](t, vendorCtx, "notification_find", find, map[string]any{})
	ids := map[string]bool{}
	for _, n := range env.data(t).Notifications {
		ids[n.ID] = true
	}
	if !ids[mineN] {
		t.Errorf("linked vendor does not see their own job's notice %s: %+v", mineN, env.data(t))
	}
	if ids[otherN] {
		t.Errorf("linked vendor sees another vendor's job notice %s", otherN)
	}
	// A task assigned to nobody is not the vendor's job either.
	if ids[unassignedN] {
		t.Errorf("linked vendor sees an unassigned job's notice %s", unassignedN)
	}
	if got := env.data(t).Total; got != 1 {
		t.Errorf("linked vendor total = %d, want 1", got)
	}

	// Unlinked: every job, the manager's decision (#4799).
	if _, err := pool.Exec(context.Background(),
		`UPDATE memberships SET vendor_id = NULL WHERE household_id = $1 AND user_id = $2`, hh.HouseholdID, uid); err != nil {
		t.Fatal(err)
	}
	if got := call[findResult](t, vendorCtx, "notification_find", find, map[string]any{}).data(t).Total; got != 3 {
		t.Errorf("unlinked vendor total = %d, want 3", got)
	}

	// A family member (and the owner context) sees all three.
	if got := call[findResult](t, ctx, "notification_find", find, map[string]any{}).data(t).Total; got != 3 {
		t.Errorf("owner total = %d, want 3", got)
	}
}

// TestMaintenanceDueCountsFollowTheFilter: UnreadCount (family_overview's
// unread_notifications) applies the same rule as find, so the two agree.
func TestMaintenanceDueCountsFollowTheFilter(t *testing.T) {
	pool := testutil.NewPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	mineVendor := insertID(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Acme') RETURNING id`, hh.HouseholdID)
	otherVendor := insertID(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Other') RETURNING id`, hh.HouseholdID)
	mineTask := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, status, vendor_id)
		VALUES ($1, 'My job', CURRENT_DATE, 'pending', $2) RETURNING id`, hh.HouseholdID, mineVendor)
	otherTask := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, status, vendor_id)
		VALUES ($1, 'Their job', CURRENT_DATE, 'pending', $2) RETURNING id`, hh.HouseholdID, otherVendor)
	notify(t, pool, hh.HouseholdID, "", "maintenance_due", "Mine", "maintenance_task", mineTask, time.Hour)
	notify(t, pool, hh.HouseholdID, "", "maintenance_due", "Theirs", "maintenance_task", otherTask, time.Hour)

	vendorCtx, uid := vendorMember(t, pool, hh, mineVendor)
	if got, err := UnreadCount(vendorCtx, pool, hh.HouseholdID.String(), uid); err != nil || got != 1 {
		t.Errorf("linked vendor unread = %d, %v; want 1", got, err)
	}
}

// vendorMember adds a vendor member linked to vendorID and returns a context
// acting as them.
func vendorMember(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, vendorID string) (context.Context, string) {
	t.Helper()
	email := "vendor-" + uuid.NewString()[:8] + "@test.homeos.local"
	uid := insertID(t, pool, `INSERT INTO users (email, name, password_hash) VALUES ($1, 'Vendor', 'x') RETURNING id`, email)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO memberships (household_id, user_id, role, vendor_id) VALUES ($1, $2, 'vendor', $3)`,
		hh.HouseholdID, uid, vendorID); err != nil {
		t.Fatalf("membership: %v", err)
	}
	claims := &auth.Claims{Identity: auth.Identity{Subject: uid, Email: email, Name: "Vendor"},
		UserID: uid, HouseholdID: hh.HouseholdID.String(), Role: "vendor"}
	return server.ContextWithClaims(context.Background(), claims), uid
}
