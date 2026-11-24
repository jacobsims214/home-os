package testutil

// Tests for migration 078 (#5509): activity_entity_kind, the audit feed's own
// vocabulary.
//
// THE DEFECT. activity_log.entity_type and parent_type were plain VARCHAR(50)
// -- the only two entity_type columns in the database not governed by a
// domain. `entity_type = 'not_a_real_table'` was ACCEPTED on a live cluster,
// and activity_log is the one table where a wrong write is permanent: a row
// whose kind no reader resolves is a change nobody can ever see.
//
// THE DRIFT THESE TESTS PIN DOWN. Three vocabularies answer "what kind of
// thing is this", and they had quietly diverged:
//
//	entity_kind domain (070)                 23 members
//	log_activity('...') trigger arguments    25 (23 - household + list_item,
//	                                            meal_feedback, person_preference)
//	role_can_see_entity(kind) branches       29 (the 25 + document, maintenance,
//	                                            kb_doc)
//
// Criterion 5 asks for set equality between the trigger arguments and the
// domain, "no drift either way". The two sets CANNOT be equal in the literal
// sense -- 'household' is a domain member that no trigger logs -- so the test
// asserts the equality that is actually true and more useful: every kind the
// writer can produce is in the domain, every domain member is either written
// by a trigger or named in the one documented exception, and every kind in
// either is visible to role_can_see_entity. A new kind added to any one of
// the three vocabularies without the others fails here.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// neverLoggedKind is the one domain member no log_activity() trigger writes.
// It is a member because it is a valid entity_kind and role_can_see_entity has
// its branch; it is not logged because nothing logs the household row itself.
// Named here rather than left implied, so a second such kind cannot appear
// unnoticed.
const neverLoggedKind = "household"

// kindsInDomain reads the domain's members from the catalogue's constraint
// definition, so the assertion compares the schema to itself.
func kindsInDomain(t *testing.T, pool *pgxpool.Pool, domain string) []string {
	t.Helper()
	var def string
	err := pool.QueryRow(context.Background(),
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = $1`,
		domain+"_check").Scan(&def)
	if err != nil {
		t.Fatalf("read constraint %s_check: %v (does the domain exist?)", domain, err)
	}
	re := regexp.MustCompile(`'([a-z_]+)'`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(def, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// kindsInDomainHas is a small helper so the "entity_kind did not widen" check
// reads as one line.
func kindsInDomainHas(t *testing.T, pool *pgxpool.Pool, domain, kind string) bool {
	t.Helper()
	for _, k := range kindsInDomain(t, pool, domain) {
		if k == kind {
			return true
		}
	}
	return false
}

// loggedKinds asks the catalogue which kinds a log_activity() trigger is
// actually attached with -- the writer's real vocabulary, not a grep of the
// migrations.
func loggedKinds(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT DISTINCT pg_get_triggerdef(t.oid)
		   FROM pg_trigger t
		  WHERE NOT t.tgisinternal AND t.tgfoid = 'log_activity()'::regprocedure`)
	if err != nil {
		t.Fatalf("read log_activity triggers: %v", err)
	}
	defer rows.Close()
	re := regexp.MustCompile(`log_activity\('([a-z_]+)'\)`)
	seen := map[string]bool{}
	for rows.Next() {
		var def string
		if err := rows.Scan(&def); err != nil {
			t.Fatalf("scan trigger def: %v", err)
		}
		for _, m := range re.FindAllStringSubmatch(def, -1) {
			seen[m[1]] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate triggers: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("no log_activity() triggers found: the writer's vocabulary cannot be read")
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// visibleKinds asks role_can_see_entity which kinds it has a branch for, by
// reading the function body: a CASE branch maps the kind to a resource.
func visibleKinds(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	var def string
	if err := pool.QueryRow(context.Background(),
		`SELECT pg_get_functiondef('role_can_see_entity(text, text)'::regprocedure)`).Scan(&def); err != nil {
		t.Fatalf("read role_can_see_entity: %v", err)
	}
	// Only the kind->resource CASE: it ends where the resource CASE begins.
	end := regexp.MustCompile(`WHEN 'family_notes' THEN ARRAY`).FindStringIndex(def)
	if end == nil {
		t.Fatal("role_can_see_entity: kind->resource CASE not found (body changed shape)")
	}
	body := def[:end[0]]
	re := regexp.MustCompile(`WHEN '([a-z_]+)'\s+THEN\s+'([a-z_]+)'`)
	out := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		out[m[1]] = m[2]
	}
	if len(out) == 0 {
		t.Fatal("role_can_see_entity: no kind branches found")
	}
	return out
}

// TestActivityEntityKind078_DomainIsEntityKindPlusFeedOnlyKinds is criterion 1:
// the domain's members are exactly the 23 entity_kind members plus list_item,
// meal_feedback and person_preference.
func TestActivityEntityKind078_DomainIsEntityKindPlusFeedOnlyKinds(t *testing.T) {
	pool := NewPool(t)

	base := kindsInDomain(t, pool, "entity_kind")
	if len(base) != 23 {
		t.Fatalf("entity_kind has %d members, want 23: %v", len(base), base)
	}

	// The kinds only the audit feed writes, each with a real table.
	// membership was added by 086 (#5518); 078 named it as this ticket's job.
	feedOnly := []string{"list_item", "meal_feedback", "person_preference", "membership"}

	want := map[string]bool{}
	for _, k := range base {
		want[k] = true
	}
	for _, k := range feedOnly {
		want[k] = true
	}

	got := kindsInDomain(t, pool, "activity_entity_kind")
	if len(got) != len(want) {
		t.Errorf("activity_entity_kind has %d members, want %d: %v", len(got), len(want), got)
	}
	have := map[string]bool{}
	for _, k := range got {
		have[k] = true
		if !want[k] {
			t.Errorf("activity_entity_kind has %q, which is neither an entity_kind member nor a feed-only kind", k)
		}
	}
	for k := range want {
		if !have[k] {
			t.Errorf("activity_entity_kind is missing %q", k)
		}
	}

	// entity_kind stays exactly as it was: widening it would let a note attach
	// to a person_preference, which is what the separate domain avoids.
	for _, k := range feedOnly {
		if kindsInDomainHas(t, pool, "entity_kind", k) {
			t.Errorf("entity_kind gained %q; the feed-only kinds must not widen it", k)
		}
	}
}

// TestActivityEntityKind078_BothColumnsUseTheDomain is criterion 2.
func TestActivityEntityKind078_BothColumnsUseTheDomain(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()

	for _, col := range []struct {
		name     string
		nullable string
	}{
		{"entity_type", "NO"},  // 050: NOT NULL
		{"parent_type", "YES"}, // 050: nullable, NULL for a top-level entry
	} {
		var domain, nullable string
		err := pool.QueryRow(ctx,
			`SELECT COALESCE(domain_name, '-'), is_nullable
			   FROM information_schema.columns
			  WHERE table_schema = 'public' AND table_name = 'activity_log' AND column_name = $1`,
			col.name).Scan(&domain, &nullable)
		if err != nil {
			t.Fatalf("read activity_log.%s: %v", col.name, err)
		}
		if domain != "activity_entity_kind" {
			t.Errorf("activity_log.%s uses domain %q, want activity_entity_kind", col.name, domain)
		}
		if nullable != col.nullable {
			t.Errorf("activity_log.%s is_nullable = %s, want %s (the domain must not change nullability)",
				col.name, nullable, col.nullable)
		}
	}
}

// TestActivityEntityKind078_RejectsAnUnknownKind is criterion 3, and criterion
// 4 for the valid kinds that must still work.
func TestActivityEntityKind078_RejectsAnUnknownKind(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	h := insertID(t, pool, `INSERT INTO households (name) VALUES ('kind family') RETURNING id`)

	insert := func(entityType, parentType string) error {
		t.Helper()
		if parentType == "" {
			_, err := pool.Exec(ctx,
				`INSERT INTO activity_log (household_id, entity_type, entity_id, action, summary)
				 VALUES ($1, $2, gen_random_uuid(), 'create', 'probe')`, h, entityType)
			return err
		}
		_, err := pool.Exec(ctx,
			`INSERT INTO activity_log (household_id, entity_type, entity_id, parent_type, parent_id, action, summary)
			 VALUES ($1, $2, gen_random_uuid(), $3, gen_random_uuid(), 'create', 'probe')`, h, entityType, parentType)
		return err
	}

	// The live probe from the schema review, now refused.
	t.Run("the reviewer's not_a_real_table is rejected", func(t *testing.T) {
		err := insert("not_a_real_table", "")
		if err == nil {
			t.Fatal("entity_type = 'not_a_real_table' was ACCEPTED; the domain is not enforced")
		}
		wantSQLState(t, err, sqlstateCheckViolation)
	})

	t.Run("an unknown parent_type is rejected", func(t *testing.T) {
		err := insert("bill", "not_a_real_table")
		if err == nil {
			t.Fatal("parent_type = 'not_a_real_table' was ACCEPTED")
		}
		wantSQLState(t, err, sqlstateCheckViolation)
	})

	// A valid kind must still get in: the constraint has to be a vocabulary,
	// not a wall in front of the writer.
	for _, k := range []string{"bill", "list_item", "household", "person_preference", "meal_feedback"} {
		t.Run("valid kind "+k+" is accepted", func(t *testing.T) {
			if err := insert(k, ""); err != nil {
				t.Fatalf("entity_type = %q was refused: %v", k, err)
			}
		})
	}

	t.Run("a top-level entry may leave parent_type NULL", func(t *testing.T) {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM activity_log WHERE household_id = $1 AND parent_type IS NULL`, h).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatal("no top-level rows with parent_type NULL: the nullable case is not legal any more")
		}
	})
}

// TestActivityEntityKind078_NoDriftBetweenWriterDomainAndPolicy is criterion 5.
func TestActivityEntityKind078_NoDriftBetweenWriterDomainAndPolicy(t *testing.T) {
	pool := NewPool(t)

	domain := kindsInDomain(t, pool, "activity_entity_kind")
	logged := loggedKinds(t, pool)
	visible := visibleKinds(t, pool)

	// 1. Everything the writer produces is in the domain. This is the
	//    direction that was broken: list_item, meal_feedback and
	//    person_preference were written but not members.
	inDomain := map[string]bool{}
	for _, k := range domain {
		inDomain[k] = true
	}
	for _, k := range logged {
		if !inDomain[k] {
			t.Errorf("log_activity writes kind %q, which is not in activity_entity_kind", k)
		}
	}

	// 2. Every domain member is written by the writer, except the one
	//    documented exception.
	written := map[string]bool{}
	for _, k := range logged {
		written[k] = true
	}
	for _, k := range domain {
		if !written[k] && k != neverLoggedKind {
			t.Errorf("activity_entity_kind has %q but no log_activity() trigger writes it (the only allowed exception is %q)",
				k, neverLoggedKind)
		}
	}

	// 3. No kind can be logged and not seen: every written kind resolves in
	//    role_can_see_entity, so a row the trigger writes is not one the feed's
	//    policy cannot decide about.
	for _, k := range logged {
		if _, ok := visible[k]; !ok {
			t.Errorf("log_activity writes kind %q, which role_can_see_entity has no branch for", k)
		}
	}

	// 4. A kind in the domain that the policy cannot see would be a row the
	//    audit feed cannot decide about. kb_doc is the mirror image (a policy
	//    branch with no writer), which is fine: nothing logs it yet.
	for _, k := range domain {
		if k == neverLoggedKind {
			continue
		}
		if _, ok := visible[k]; !ok {
			t.Errorf("activity_entity_kind has %q but role_can_see_entity has no branch for it", k)
		}
	}
}

// TestActivityEntityKind078_EveryMigrationArgumentIsAMember is criterion 5
// taken to the source: every kind a migration hands to log_activity() is a
// domain member, so a migration added later cannot introduce a kind the domain
// does not know.
//
// 050 attaches the triggers from a `FOR r IN SELECT * FROM (VALUES
// ('properties','property'), ...) AS v(tbl, kind)` block, and a later
// migration that adds a table does the same, so the kinds are read from that
// block rather than from literal log_activity('kind') calls (there are none:
// the argument is format(%L)). The catalogue test above only sees triggers
// that still exist; this one also catches a kind a later migration replaced.
func TestActivityEntityKind078_EveryMigrationArgumentIsAMember(t *testing.T) {
	pool := NewPool(t)
	dir, err := migrationsDir()
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no migrations in %s", dir)
	}

	domain := map[string]bool{}
	for _, k := range kindsInDomain(t, pool, "activity_entity_kind") {
		domain[k] = true
	}

	// The kind is the second element of each (table, kind) pair.
	pairRe := regexp.MustCompile(`\('([a-z_]+)'\s*,\s*'([a-z_]+)'\)`)
	found := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		src := string(b)
		// Only the block that feeds log_activity(%L): migrations with no such
		// template attach no kind.
		if !strings.Contains(src, "EXECUTE FUNCTION log_activity(%L)") {
			continue
		}
		start := strings.Index(src, "FOR r IN SELECT * FROM (VALUES")
		if start < 0 {
			t.Fatalf("%s attaches log_activity but its (table, kind) VALUES block was not found: this test needs updating for how the writer is declared", filepath.Base(f))
		}
		end := strings.Index(src[start:], ") AS v(tbl, kind)")
		if end < 0 {
			t.Fatalf("%s: VALUES block has no closing AS v(tbl, kind)", filepath.Base(f))
		}
		for _, m := range pairRe.FindAllStringSubmatch(src[start:start+end], -1) {
			found++
			if !domain[m[2]] {
				t.Errorf("%s attaches log_activity with kind %q, which is not in activity_entity_kind",
					filepath.Base(f), m[2])
			}
		}
	}
	if found < 25 {
		t.Fatalf("found only %d log_activity kinds across the migrations, want at least 25: the pattern does not match how the writer is attached", found)
	}
}

// TestActivityEntityKind078_RowsSurviveAndAreRepaired is criterion 6: the
// migration reports and repairs what it can, does not fail on existing rows,
// and does not delete any.
func TestActivityEntityKind078_RowsSurviveAndAreRepaired(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	h := insertID(t, pool, `INSERT INTO households (name) VALUES ('history family') RETURNING id`)

	// Four rows the old schema accepted, of the kinds that have a named
	// repair. They cannot be inserted at 078 (that is the point), so the test
	// goes back to 077 first -- the shape every existing database is in when
	// this migration runs.
	applyMigration(t, pool, "078_activity_entity_kind.down.sql")
	for _, e := range []struct{ entityType, parentType string }{
		{"document", ""},    // 019/024: documents folded into files
		{"maintenance", ""}, // 039: renamed maintenance_task
		{"document", "maintenance"},
		{"list_item", "list"}, // an ordinary feed row, unchanged
	} {
		if err := seedActivityRow(ctx, pool, h, e.entityType, e.parentType); err != nil {
			t.Fatalf("seed legacy row %s/%s: %v", e.entityType, e.parentType, err)
		}
	}

	applyMigration(t, pool, "078_activity_entity_kind.up.sql")

	var total, repaired, untouched int
	if err := pool.QueryRow(ctx,
		`SELECT count(*),
		        count(*) FILTER (WHERE entity_type::text = 'file' OR parent_type::text = 'file'
		                            OR entity_type::text = 'maintenance_task' OR parent_type::text = 'maintenance_task'),
		        count(*) FILTER (WHERE entity_type::text = 'list_item')
		   FROM activity_log WHERE household_id = $1`, h).Scan(&total, &repaired, &untouched); err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Errorf("activity_log kept %d of 4 rows; the migration must not delete history", total)
	}
	if repaired != 3 {
		t.Errorf("repaired %d rows, want 3 (2x document->file, 1x maintenance->maintenance_task)", repaired)
	}
	if untouched != 1 {
		t.Errorf("the list_item row changed; only out-of-domain values may be rewritten")
	}

	// The columns are constrained again, so the defect is closed once more.
	if _, err := pool.Exec(ctx,
		`INSERT INTO activity_log (household_id, entity_type, entity_id, action, summary)
		 VALUES ($1, 'not_a_real_table', gen_random_uuid(), 'create', 'x')`, h); err == nil {
		t.Fatal("after the repair the table accepts an unknown kind again")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != sqlstateCheckViolation {
			t.Fatalf("want SQLSTATE %s, got %v", sqlstateCheckViolation, err)
		}
	}

	// A kind with no named repair stops the migration instead of being guessed
	// at or dropped. This is the "do not silently rewrite history" rule.
	t.Run("an unnameable kind stops the migration", func(t *testing.T) {
		// Back to 077 so the row can go in, then 078 must refuse it.
		applyMigration(t, pool, "078_activity_entity_kind.down.sql")
		if _, err := pool.Exec(ctx,
			`INSERT INTO activity_log (household_id, entity_type, entity_id, action, summary)
			 VALUES ($1, 'widget', gen_random_uuid(), 'create', 'x')`, h); err != nil {
			t.Fatalf("seed the unnameable kind: %v", err)
		}

		dir, err := migrationsDir()
		if err != nil {
			t.Fatal(err)
		}
		sql, err := os.ReadFile(filepath.Join(dir, "078_activity_entity_kind.up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err == nil {
			t.Fatal("078 re-ran over an unnameable kind and did not complain")
		}

		// Nothing changed: the kind is still there, one row of it.
		var after int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM activity_log WHERE household_id = $1 AND entity_type::text = 'widget'`, h).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != 1 {
			t.Fatalf("the refused migration changed the table: %d widget rows, want 1 (it must not rewrite or delete)", after)
		}
	})
}

// seedActivityRow writes one activity_log row, with or without a parent.
// Two statements rather than one CASE: a nil parameter cannot be typed by the
// server, and parent_type/parent_id must be NULL together anyway
// (activity_log_parent_check).
func seedActivityRow(ctx context.Context, pool *pgxpool.Pool, household uuid.UUID, entityType, parentType string) error {
	if parentType == "" {
		_, err := pool.Exec(ctx,
			`INSERT INTO activity_log (household_id, entity_type, entity_id, action, summary)
			 VALUES ($1, $2, gen_random_uuid(), 'create', 'history')`, household, entityType)
		return err
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO activity_log (household_id, entity_type, entity_id, parent_type, parent_id, action, summary)
		 VALUES ($1, $2, gen_random_uuid(), $3, gen_random_uuid(), 'create', 'history')`,
		household, entityType, parentType)
	return err
}

// TestActivityEntityKind078_DownRestoresVarchar is the down migration's own
// assertion: the columns are VARCHAR(50) again and the domain is gone, with
// every row still there.
func TestActivityEntityKind078_DownRestoresVarchar(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	h := insertID(t, pool, `INSERT INTO households (name) VALUES ('down family') RETURNING id`)
	rows := []struct{ entityType, parentType string }{
		{"bill", ""}, {"list_item", "list"}, {"person_preference", "person"},
	}
	for _, e := range rows {
		if err := seedActivityRow(ctx, pool, h, e.entityType, e.parentType); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	applyMigration(t, pool, "078_activity_entity_kind.down.sql")

	for _, col := range []string{"entity_type", "parent_type"} {
		var domain, dataType string
		var length *int
		if err := pool.QueryRow(ctx,
			`SELECT COALESCE(domain_name, '-'), data_type, character_maximum_length
			   FROM information_schema.columns
			  WHERE table_schema = 'public' AND table_name = 'activity_log' AND column_name = $1`,
			col).Scan(&domain, &dataType, &length); err != nil {
			t.Fatal(err)
		}
		if domain != "-" || dataType != "character varying" || length == nil || *length != 50 {
			t.Errorf("after down activity_log.%s is %s(%v) domain=%s, want character varying(50) and no domain",
				col, dataType, length, domain)
		}
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_log WHERE household_id = $1`, h).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(rows) {
		t.Errorf("down kept %d of %d rows", n, len(rows))
	}

	var domains int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.domains WHERE domain_name = 'activity_entity_kind'`).Scan(&domains); err != nil {
		t.Fatal(err)
	}
	if domains != 0 {
		t.Error("activity_entity_kind still exists after down")
	}

	// Down runs twice safely (the older chains may already have undone it).
	applyMigration(t, pool, "078_activity_entity_kind.down.sql")

	// And the defect really is back: this is what proves the down reverted the
	// guard rather than half of it.
	if _, err := pool.Exec(ctx,
		`INSERT INTO activity_log (household_id, entity_type, entity_id, action, summary)
		 VALUES ($1, 'not_a_real_table', gen_random_uuid(), 'create', 'x')`, h); err != nil {
		t.Fatalf("after down an unknown kind is still refused (%v); the down did not restore 077's schema", err)
	}
}
