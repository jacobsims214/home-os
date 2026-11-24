package testutil

// Migration 086 (#5518): memberships are audited.
//
// Finding F24 in architecture/schema-review-2026-11.md: `memberships` was the
// one real activity_log gap. Measured at 084, a grant wrote only the 041 person
// side effect, a role change wrote NOTHING, and a delete wrote only the 041
// person unlink. A role decides whether a person can see money, credentials and
// documents, so "who promoted whom, and when?" is the security question the
// family's own history could not answer.
//
// The rows come from a log_activity('membership') TRIGGER, not handler code,
// and that is what these tests are really about: both write paths -- the REST
// handlers' statements and an MCP tool -- produce the SAME rows without either
// of them knowing the audit exists. A handler-code audit site would pass a
// REST-only test and leak on the other path.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
)

// actorCtx returns a context whose claims make the pool's PrepareConn hook set
// app.user_id / app.household_id (server/dbactor.go), so an actor is recorded
// without any SQL naming one. This is what an MCP tool call carries.
func actorCtx(userID, householdID, role string) context.Context {
	return server.ContextWithClaims(context.Background(), &auth.Claims{
		Identity:    auth.Identity{Subject: userID},
		UserID:      userID,
		HouseholdID: householdID,
		Role:        role,
	})
}

// grantMembership inserts a membership the way apps/api/internal/household's
// CreateMembership does -- the exact statement, naming no audit column.
func grantMembership(t *testing.T, pool *pgxpool.Pool, ctx context.Context, hh, user, role string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
		hh, user, role); err != nil {
		t.Fatalf("grant membership: %v", err)
	}
}

// newLogin creates a user who is not yet a member of anything. (The existing
// newMember in change_notifications_test.go creates a member; this one is the
// pre-grant state, which is what the audit tests need.)
func newLogin(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id::text`,
		strings.ToLower(strings.ReplaceAll(name, " ", "-"))+"@test.homeos.local", name).Scan(&id); err != nil {
		t.Fatalf("insert user %s: %v", name, err)
	}
	return id
}

// membershipRows returns the activity_log rows about memberships for a
// household, oldest first, as "action|summary".
func membershipRows(t *testing.T, pool *pgxpool.Pool, hh string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT action || '|' || summary
		  FROM activity_log
		 WHERE household_id = $1 AND entity_type = 'membership'
		 ORDER BY id`, hh)
	if err != nil {
		t.Fatalf("read membership activity: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMembershipAudit086_Criterion1 is criterion 1: the trigger fires on
// INSERT, UPDATE and DELETE and it is the shared log_activity() writer taking
// 'membership' -- not a bespoke function that could drift.
func TestMembershipAudit086_Criterion1(t *testing.T) {
	pool := NewPool(t)

	var def, kind string
	if err := pool.QueryRow(context.Background(), `
		SELECT pg_get_triggerdef(tg.oid),
		       replace(encode(tg.tgargs, 'escape'), '\000', '')
		  FROM pg_trigger tg
		  JOIN pg_proc p ON p.oid = tg.tgfoid
		 WHERE tg.tgrelid = 'public.memberships'::regclass
		   AND NOT tg.tgisinternal
		   AND p.proname = 'log_activity'`).Scan(&def, &kind); err != nil {
		t.Fatalf("no log_activity trigger on memberships: %v", err)
	}
	for _, ev := range []string{"INSERT", "UPDATE", "DELETE"} {
		if !strings.Contains(def, ev) {
			t.Errorf("memberships log_activity trigger does not fire on %s: %s", ev, def)
		}
	}
	if kind != "membership" {
		t.Errorf("log_activity kind on memberships = %q, want %q", kind, "membership")
	}

	var found bool
	for _, m := range kindsInDomain(t, pool, "activity_entity_kind") {
		if m == "membership" {
			found = true
		}
	}
	if !found {
		t.Errorf("activity_entity_kind has no 'membership' member: %v", kindsInDomain(t, pool, "activity_entity_kind"))
	}
}

// TestMembershipAudit086_Criterion2 is criterion 2: granting a membership
// writes exactly one row, in the F1 domain, naming the person and the role.
func TestMembershipAudit086_Criterion2(t *testing.T) {
	pool := NewPool(t)
	ctx, hh := SeedHousehold(t, pool)
	hid := hh.HouseholdID.String()
	before := len(membershipRows(t, pool, hid))

	kid := newLogin(t, pool, "Sam Kid")
	grantMembership(t, pool, ctx, hid, kid, "viewer")

	rows := membershipRows(t, pool, hid)
	if got := len(rows) - before; got != 1 {
		t.Fatalf("granting a membership wrote %d membership rows, want exactly 1: %v", got, rows)
	}
	got := rows[len(rows)-1]
	if !strings.HasPrefix(got, "create|") {
		t.Errorf("grant row = %q, want action create", got)
	}
	if !strings.Contains(got, "Sam Kid") {
		t.Errorf("grant summary does not name the person: %q", got)
	}
	if !strings.Contains(got, "viewer") {
		t.Errorf("grant summary does not name the role: %q", got)
	}

	// The actor is recorded: SeedHousehold's context carries the owner's
	// claims, so app.user_id is set on the connection.
	var actor *string
	if err := pool.QueryRow(context.Background(), `
		SELECT actor_user_id::text FROM activity_log
		 WHERE household_id = $1 AND entity_type = 'membership'
		 ORDER BY id DESC LIMIT 1`, hh.HouseholdID).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor == nil || *actor != hh.UserID.String() {
		t.Errorf("membership row actor_user_id = %v, want %s", actor, hh.UserID)
	}
}

// TestMembershipAudit086_Criterion3 is criterion 3, and the point of the
// ticket: a role change records BOTH values.
func TestMembershipAudit086_Criterion3(t *testing.T) {
	pool := NewPool(t)
	ctx, hh := SeedHousehold(t, pool)
	hid := hh.HouseholdID.String()

	member := newLogin(t, pool, "Alex Teen")
	grantMembership(t, pool, ctx, hid, member, "viewer")

	if _, err := pool.Exec(ctx,
		`UPDATE memberships SET role = 'family_member' WHERE household_id = $1 AND user_id = $2`,
		hid, member); err != nil {
		t.Fatalf("change role: %v", err)
	}

	var changed []string
	var summary string
	var oldRole, newRole *string
	if err := pool.QueryRow(context.Background(), `
		SELECT changed_fields, summary, changes -> 'role' ->> 'from', changes -> 'role' ->> 'to'
		  FROM activity_log
		 WHERE household_id = $1 AND entity_type = 'membership' AND action = 'update'
		 ORDER BY id DESC LIMIT 1`, hh.HouseholdID).Scan(&changed, &summary, &oldRole, &newRole); err != nil {
		t.Fatalf("no membership update row: %v", err)
	}
	if len(changed) != 1 || changed[0] != "role" {
		t.Errorf("changed_fields = %v, want [role]", changed)
	}
	if oldRole == nil || *oldRole != "viewer" {
		t.Errorf("changes.role.from = %v, want viewer", oldRole)
	}
	if newRole == nil || *newRole != "family_member" {
		t.Errorf("changes.role.to = %v, want family_member", newRole)
	}
	if !strings.Contains(summary, "viewer -> family_member") {
		t.Errorf("summary does not spell out the role transition: %q", summary)
	}

	// A no-op UPDATE stays silent (the 077 invariant, now on memberships).
	beforeRows := len(membershipRows(t, pool, hid))
	if _, err := pool.Exec(ctx,
		`UPDATE memberships SET role = 'family_member' WHERE household_id = $1 AND user_id = $2`,
		hid, member); err != nil {
		t.Fatalf("no-op role update: %v", err)
	}
	if after := len(membershipRows(t, pool, hid)); after != beforeRows {
		t.Errorf("a no-op UPDATE wrote %d membership rows; updated_at/updated_by must not count as a change", after-beforeRows)
	}
}

// TestMembershipAudit086_Criterion4 is criterion 4: removing a membership
// writes one delete row, and the 041 people side effect does not double-log it.
func TestMembershipAudit086_Criterion4(t *testing.T) {
	pool := NewPool(t)
	ctx, hh := SeedHousehold(t, pool)
	hid := hh.HouseholdID.String()

	member := newLogin(t, pool, "Robin Guest")
	grantMembership(t, pool, ctx, hid, member, "housesitter")

	if _, err := pool.Exec(ctx,
		`DELETE FROM memberships WHERE household_id = $1 AND user_id = $2`, hid, member); err != nil {
		t.Fatalf("remove membership: %v", err)
	}

	// Every delete row written by this membership's removal, by kind.
	rows, err := pool.Query(context.Background(), `
		SELECT entity_type || '|' || action
		  FROM activity_log
		 WHERE household_id = $1 AND action = 'delete'
		   AND entity_id IN (SELECT entity_id FROM activity_log
		                      WHERE household_id = $1 AND entity_type = 'membership' AND action = 'delete')
		 ORDER BY id`, hh.HouseholdID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var deleteRows []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		deleteRows = append(deleteRows, s)
	}
	if len(deleteRows) != 1 || deleteRows[0] != "membership|delete" {
		t.Errorf("removing a membership wrote delete rows %v, want exactly [membership|delete]", deleteRows)
	}

	// The whole removal, counted: one membership delete, and one person UPDATE
	// (041 unlinking the login) -- two facts about two tables, not one event
	// logged twice. If the membership ever appears twice, this catches it.
	var membershipDeletes, personDeletes int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FILTER (WHERE entity_type = 'membership' AND action = 'delete'),
		       count(*) FILTER (WHERE entity_type = 'person' AND action = 'delete')
		  FROM activity_log WHERE household_id = $1`, hh.HouseholdID).
		Scan(&membershipDeletes, &personDeletes); err != nil {
		t.Fatal(err)
	}
	if membershipDeletes != 1 {
		t.Errorf("membership delete rows = %d, want 1", membershipDeletes)
	}
	if personDeletes != 0 {
		t.Errorf("a membership delete produced %d person DELETE rows; 041 unlinks (person update), it does not delete the person", personDeletes)
	}
}

// TestMembershipAudit086_Criterion5 is criterion 5: memberships has updated_at
// and updated_by, both move on a role change, and both are filled by the
// database so a writer that forgets cannot make the row lie.
func TestMembershipAudit086_Criterion5(t *testing.T) {
	pool := NewPool(t)
	ctx, hh := SeedHousehold(t, pool)
	hid := hh.HouseholdID.String()

	member := newLogin(t, pool, "Casey Child")
	grantMembership(t, pool, ctx, hid, member, "viewer")

	var before time.Time
	if err := pool.QueryRow(ctx,
		`SELECT updated_at FROM memberships WHERE household_id = $1 AND user_id = $2`,
		hid, member).Scan(&before); err != nil {
		t.Fatal(err)
	}

	// A distinct actor changes the role. The statement names neither column.
	other := newLogin(t, pool, "Terry Parent")
	grantMembership(t, pool, ctx, hid, other, "family_manager")

	if _, err := pool.Exec(actorCtx(other, hid, "family_manager"),
		`UPDATE memberships SET role = 'family_member' WHERE household_id = $1 AND user_id = $2`,
		hid, member); err != nil {
		t.Fatal(err)
	}

	var after time.Time
	var updatedBy *string
	if err := pool.QueryRow(context.Background(),
		`SELECT updated_at, updated_by::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
		hid, member).Scan(&after, &updatedBy); err != nil {
		t.Fatal(err)
	}
	if !after.After(before) {
		t.Errorf("updated_at = %v after a role change, want later than %v: the trigger must move it", after, before)
	}
	if updatedBy == nil || *updatedBy != other {
		t.Errorf("updated_by = %v after a role change by %s, want that user", updatedBy, other)
	}
}

// TestMembershipAudit086_Criterion7 is criterion 7, the leak-vector guard: the
// credential tables must never gain an activity trigger.
func TestMembershipAudit086_Criterion7(t *testing.T) {
	pool := NewPool(t)

	for _, table := range []string{"agent_tokens", "secret_keys", "password_reset_tokens"} {
		var n int
		if err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_trigger tg
			  JOIN pg_proc p ON p.oid = tg.tgfoid
			 WHERE tg.tgrelid = format('public.%I', $1::text)::regclass
			   AND NOT tg.tgisinternal
			   AND p.proname = 'log_activity'`, table).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s has %d log_activity trigger(s); credential tables must have NONE (leak vector)", table, n)
		}
	}
}

// TestMembershipAudit086_ParityAcrossWritePaths is the ticket's real point:
// the same three operations produce the same rows no matter which path runs
// them, because neither path knows the audit exists.
func TestMembershipAudit086_ParityAcrossWritePaths(t *testing.T) {
	pool := NewPool(t)
	_, hh := SeedHousehold(t, pool)
	hid := hh.HouseholdID.String()

	apiMember := newLogin(t, pool, "Api Member")
	mcpMember := newLogin(t, pool, "Mcp Member")

	// Path 1: the REST repo's own statements (household/repo.go).
	apiCtx := actorCtx(hh.UserID.String(), hid, "owner")
	if _, err := pool.Exec(apiCtx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
		hid, apiMember, "viewer"); err != nil {
		t.Fatalf("api grant: %v", err)
	}
	if _, err := pool.Exec(apiCtx,
		`UPDATE memberships SET role = $3::membership_role WHERE household_id = $1 AND user_id = $2`,
		hid, apiMember, "family_member"); err != nil {
		t.Fatalf("api role change: %v", err)
	}
	if _, err := pool.Exec(apiCtx,
		`DELETE FROM memberships WHERE household_id = $1 AND user_id = $2`, hid, apiMember); err != nil {
		t.Fatalf("api remove: %v", err)
	}

	// Path 2: the same three through an MCP tool's context.
	mcpCtx := actorCtx(hh.UserID.String(), hid, "owner")
	if _, err := pool.Exec(mcpCtx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
		hid, mcpMember, "viewer"); err != nil {
		t.Fatalf("mcp grant: %v", err)
	}
	if _, err := pool.Exec(mcpCtx,
		`UPDATE memberships SET role = $3::membership_role WHERE household_id = $1 AND user_id = $2`,
		hid, mcpMember, "family_member"); err != nil {
		t.Fatalf("mcp role change: %v", err)
	}
	if _, err := pool.Exec(mcpCtx,
		`DELETE FROM memberships WHERE household_id = $1 AND user_id = $2`, hid, mcpMember); err != nil {
		t.Fatalf("mcp remove: %v", err)
	}

	var apiRows, mcpRows []string
	for _, r := range membershipRows(t, pool, hid) {
		switch {
		case strings.Contains(r, "Api Member"):
			apiRows = append(apiRows, r)
		case strings.Contains(r, "Mcp Member"):
			mcpRows = append(mcpRows, r)
		}
	}
	if len(apiRows) != 3 {
		t.Fatalf("the REST path wrote %d membership rows, want 3: %v", len(apiRows), apiRows)
	}
	if len(mcpRows) != 3 {
		t.Fatalf("the MCP path wrote %d membership rows, want 3: %v", len(mcpRows), mcpRows)
	}

	// The person's own name is the only thing that legitimately differs.
	strip := func(rows []string, name string) []string {
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, strings.ReplaceAll(r, name, "<person>"))
		}
		return out
	}
	gotAPI, gotMCP := strip(apiRows, "Api Member"), strip(mcpRows, "Mcp Member")
	for i := range gotAPI {
		if gotAPI[i] != gotMCP[i] {
			t.Errorf("path parity broken at row %d:\n  REST: %s\n  MCP:  %s", i, gotAPI[i], gotMCP[i])
		}
	}
	for _, c := range []struct {
		name string
		rows []string
	}{{"REST", gotAPI}, {"MCP", gotMCP}} {
		for i, p := range []string{"create|", "update|", "delete|"} {
			if !strings.HasPrefix(c.rows[i], p) {
				t.Errorf("%s row %d = %q, want prefix %q", c.name, i, c.rows[i], p)
			}
		}
		if !strings.Contains(c.rows[1], "viewer -> family_member") {
			t.Errorf("%s role-change row does not record both roles: %q", c.name, c.rows[1])
		}
	}
}

// TestMembershipAudit086_VisibilityIsManagersOnly is criterion 6's schema half:
// the visibility decision is enforced by the mechanism every reader asks.
func TestMembershipAudit086_VisibilityIsManagersOnly(t *testing.T) {
	pool := NewPool(t)

	want := map[string]bool{"owner": true, "family_manager": true}
	for _, role := range []string{"owner", "family_manager", "family_member", "viewer", "housesitter", "vendor"} {
		var seen bool
		if err := pool.QueryRow(context.Background(),
			`SELECT role_can_see_entity($1, 'membership')`, role).Scan(&seen); err != nil {
			t.Fatal(err)
		}
		if seen != want[role] {
			t.Errorf("role_can_see_entity(%q, 'membership') = %v, want %v: the membership audit is owner/family_manager only",
				role, seen, want[role])
		}
	}

	// 'members' is unchanged: a housesitter may list the family's members and
	// must still not read the audit. If these ever agree, the audit leaked.
	var auditSeen bool
	if err := pool.QueryRow(context.Background(),
		`SELECT role_can_see_entity('housesitter', 'membership')`).Scan(&auditSeen); err != nil {
		t.Fatal(err)
	}
	if auditSeen {
		t.Error("a housesitter can see the membership audit; it must be managers-only")
	}
}

// TestMembershipAudit086_Reversible is the down migration.
func TestMembershipAudit086_Reversible(t *testing.T) {
	pool := NewPool(t)
	ctx, hh := SeedHousehold(t, pool)
	hid := hh.HouseholdID.String()

	member := newLogin(t, pool, "Revert Person")
	grantMembership(t, pool, ctx, hid, member, "viewer")

	var membershipCount, activityCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM memberships`).Scan(&membershipCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_log`).Scan(&activityCount); err != nil {
		t.Fatal(err)
	}

	applyMigration(t, pool, "086_membership_audit.down.sql")

	t.Run("the writer, the function and the columns are gone", func(t *testing.T) {
		checks := map[string]string{
			"log_activity trigger on memberships": `SELECT count(*) FROM pg_trigger tg JOIN pg_proc p ON p.oid = tg.tgfoid
			  WHERE tg.tgrelid = 'public.memberships'::regclass AND p.proname = 'log_activity'`,
			"membership_set_actor function":      `SELECT count(*) FROM pg_proc WHERE proname = 'membership_set_actor'`,
			"memberships_set_updated_at trigger": `SELECT count(*) FROM pg_trigger WHERE tgname = 'memberships_set_updated_at'`,
			"memberships.updated_at column": `SELECT count(*) FROM information_schema.columns
			  WHERE table_name = 'memberships' AND column_name = 'updated_at'`,
			"memberships.updated_by column": `SELECT count(*) FROM information_schema.columns
			  WHERE table_name = 'memberships' AND column_name = 'updated_by'`,
		}
		for name, sql := range checks {
			var n int
			if err := pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if n != 0 {
				t.Errorf("down left %d of %s", n, name)
			}
		}
	})

	t.Run("no data was deleted", func(t *testing.T) {
		var m, a int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM memberships`).Scan(&m); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_log`).Scan(&a); err != nil {
			t.Fatal(err)
		}
		if m != membershipCount {
			t.Errorf("memberships went from %d to %d; down must not delete members", membershipCount, m)
		}
		if a != activityCount {
			t.Errorf("activity_log went from %d to %d; down must not delete audit history", activityCount, a)
		}
	})

	// The domain member deliberately SURVIVES the rollback: narrowing it would
	// validate every activity_log row and fail on the very rows 086 wrote. See
	// the down migration's section 4. Asserted so the choice is not mistaken
	// for an oversight.
	t.Run("the domain member survives, and the history it names is intact", func(t *testing.T) {
		var def string
		if err := pool.QueryRow(ctx, `
			SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c
			  JOIN pg_type ty ON ty.oid = c.contypid
			 WHERE ty.typname = 'activity_entity_kind' AND c.contype = 'c'`).Scan(&def); err != nil {
			t.Fatalf("activity_entity_kind: %v", err)
		}
		if !strings.Contains(def, "'membership'") {
			t.Errorf("down narrowed 'membership' out of activity_entity_kind; that would require deleting audit history: %s", def)
		}
		var kept int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM activity_log WHERE entity_type = 'membership'`).Scan(&kept); err != nil {
			t.Fatal(err)
		}
		if kept == 0 {
			t.Error("down deleted the membership audit rows; a rollback removes the writer, not the record")
		}
	})

	t.Run("down is idempotent", func(t *testing.T) {
		applyMigration(t, pool, "086_membership_audit.down.sql")
	})

	t.Run("up again round-trips", func(t *testing.T) {
		applyMigration(t, pool, "086_membership_audit.up.sql")
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger tg JOIN pg_proc p ON p.oid = tg.tgfoid
			  WHERE tg.tgrelid = 'public.memberships'::regclass AND p.proname = 'log_activity'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("after up-again memberships has %d log_activity triggers, want 1", n)
		}
		var m int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM memberships`).Scan(&m); err != nil {
			t.Fatal(err)
		}
		if m != membershipCount {
			t.Errorf("memberships has %d rows after round trip, want %d", m, membershipCount)
		}
	})
}
