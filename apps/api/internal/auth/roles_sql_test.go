package auth

import (
	"context"
	"os"
	"regexp"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRoleCanSeeEntitySQLMatchesPolicy keeps the SQL copy of CanSeeEntity
// (role_can_see_entity, migration 061, used by the creator notifications
// trigger) in step with this package's policy table. It checks every role
// (plus unknown ones) against every kind roles.go knows, every value of the
// entity_kind domain, every kind a log_activity trigger writes to
// activity_log and a kind nobody knows. Change roles.go and the SQL together
// (in a new migration that replaces role_can_see_entity).
func TestRoleCanSeeEntitySQLMatchesPolicy(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	kinds := map[string]bool{"zz_future_kind": true}
	for _, k := range EntityKinds() {
		kinds[k] = true
	}

	// entity_kind domain values (the kinds notes, links, files and secrets
	// may point at, and so activity_log parent kinds).
	var def string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c
		  JOIN pg_type ty ON ty.oid = c.contypid
		 WHERE ty.typname = 'entity_kind' AND c.contype = 'c'`).Scan(&def); err != nil {
		t.Fatalf("entity_kind domain: %v", err)
	}
	domainKinds := regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(def, -1)
	if len(domainKinds) == 0 {
		t.Fatalf("no kinds parsed from entity_kind constraint %q", def)
	}
	for _, m := range domainKinds {
		kinds[m[1]] = true
	}

	// Kinds the log_activity triggers write (activity_log.entity_type).
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT replace(encode(tg.tgargs, 'escape'), '\000', '')
		  FROM pg_trigger tg JOIN pg_proc p ON p.oid = tg.tgfoid
		 WHERE p.proname = 'log_activity' AND NOT tg.tgisinternal`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		kinds[k] = true
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if n == 0 {
		t.Fatal("no log_activity triggers found")
	}

	var sorted []string
	for k := range kinds {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	// Every Go kind is checked for every role, so a kind missing from the
	// SQL mapping (falling back to family-only) fails for the viewer at least.
	roles := append([]string{"", "bogus_role"}, AllRoles...)
	for _, role := range roles {
		for _, kind := range sorted {
			var got bool
			if err := pool.QueryRow(ctx, `SELECT role_can_see_entity($1, $2)`, role, kind).Scan(&got); err != nil {
				t.Fatalf("role_can_see_entity(%q, %q): %v", role, kind, err)
			}
			if want := CanSeeEntity(role, kind); got != want {
				t.Errorf("role_can_see_entity(%q, %q) = %v, auth.CanSeeEntity = %v: update the SQL (migration 061) and roles.go together", role, kind, got, want)
			}
		}
	}
}
