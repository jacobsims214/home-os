package testutil

// Tests for migration 050 (authorship columns and the activity log). See KB
// decisions/domain-model-v2.md, "050 authorship".

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// actor is what the API/MCP set for a write: app.user_id and, for agents,
// app.agent_token_id. Empty strings mean "not set".
type actor struct {
	user, agentToken string
}

// asActor runs fn in one transaction with the actor settings set locally,
// the way the API and MCP server are expected to (set_config(..., true)).
func asActor(t *testing.T, pool *pgxpool.Pool, a actor, fn func(tx pgx.Tx)) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if a.user != "" {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.user_id', $1, true)`, a.user); err != nil {
			t.Fatal(err)
		}
	}
	if a.agentToken != "" {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.agent_token_id', $1, true)`, a.agentToken); err != nil {
			t.Fatal(err)
		}
	}
	fn(tx)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func txExec(t *testing.T, tx pgx.Tx, sql string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func txInsertID(t *testing.T, tx pgx.Tx, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := tx.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("insert %q: %v", sql, err)
	}
	return id
}

type activityRow struct {
	actorUser, actorAgent, entityCreatedBy *uuid.UUID
	entityType, action, summary            string
	parentType                             *string
	parentID                               *uuid.UUID
	changed                                []string
}

func activityFor(t *testing.T, pool *pgxpool.Pool, entity uuid.UUID) []activityRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT actor_user_id, actor_agent_token_id, entity_created_by, entity_type,
		       action, summary, parent_type, parent_id, changed_fields
		  FROM activity_log WHERE entity_id = $1 ORDER BY id`, entity)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []activityRow
	for rows.Next() {
		var r activityRow
		if err := rows.Scan(&r.actorUser, &r.actorAgent, &r.entityCreatedBy, &r.entityType,
			&r.action, &r.summary, &r.parentType, &r.parentID, &r.changed); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func eqID(p *uuid.UUID, want uuid.UUID) bool { return p != nil && *p == want }

func newAgentToken(t *testing.T, pool *pgxpool.Pool, household, user uuid.UUID) uuid.UUID {
	t.Helper()
	return insertID(t, pool, `INSERT INTO agent_tokens (user_id, household_id, name, token_prefix, token_hash)
		VALUES ($1, $2, 'agent', 'hos_', sha256(gen_random_uuid()::text::bytea)) RETURNING id`, user, household)
}

// With app.user_id set: created_by/updated_by are filled, every write is
// logged with the actor (and the agent token), and an update by someone
// else moves updated_by but not created_by.
func TestAuthorship050_ActorWrites(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	alex := newUser(t, pool, "Alex")
	sam := newUser(t, pool, "Sam")
	token := newAgentToken(t, pool, f.household, sam)

	var bill uuid.UUID
	asActor(t, pool, actor{user: alex.String()}, func(tx pgx.Tx) {
		bill = txInsertID(t, tx, `INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Netflix', 15) RETURNING id`, f.household)
	})
	var createdBy, updatedBy *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT created_by, updated_by FROM bills WHERE id = $1`, bill), &createdBy, &updatedBy)
	if !eqID(createdBy, alex) || !eqID(updatedBy, alex) {
		t.Fatalf("insert: created_by=%v updated_by=%v, want both %v", createdBy, updatedBy, alex)
	}

	asActor(t, pool, actor{user: sam.String(), agentToken: token.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE bills SET amount = 18, notes = 'price rise' WHERE id = $1`, bill)
	})
	mustScan(t, pool.QueryRow(ctx, `SELECT created_by, updated_by FROM bills WHERE id = $1`, bill), &createdBy, &updatedBy)
	if !eqID(createdBy, alex) || !eqID(updatedBy, sam) {
		t.Fatalf("update: created_by=%v updated_by=%v, want %v / %v", createdBy, updatedBy, alex, sam)
	}

	asActor(t, pool, actor{user: alex.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `DELETE FROM bills WHERE id = $1`, bill)
	})

	got := activityFor(t, pool, bill)
	if len(got) != 3 {
		t.Fatalf("want 3 activity rows, got %d: %+v", len(got), got)
	}
	for i, want := range []string{"create", "update", "delete"} {
		r := got[i]
		if r.action != want || r.entityType != "bill" {
			t.Errorf("row %d: %s %s, want %s bill", i, r.action, r.entityType, want)
		}
		if !eqID(r.entityCreatedBy, alex) {
			t.Errorf("row %d: entity_created_by=%v, want %v", i, r.entityCreatedBy, alex)
		}
	}
	if !eqID(got[0].actorUser, alex) || got[0].actorAgent != nil {
		t.Errorf("create actor: user=%v agent=%v", got[0].actorUser, got[0].actorAgent)
	}
	if !eqID(got[1].actorUser, sam) || !eqID(got[1].actorAgent, token) {
		t.Errorf("update actor: user=%v agent=%v, want %v / %v", got[1].actorUser, got[1].actorAgent, sam, token)
	}
	if strings.Join(got[1].changed, ",") != "amount,notes" {
		t.Errorf("update changed_fields = %v, want [amount notes]", got[1].changed)
	}
	if got[0].summary != `created bill "Netflix"` || got[1].summary != `updated bill "Netflix" (amount, notes)` || got[2].summary != `deleted bill "Netflix"` {
		t.Errorf("summaries: %q / %q / %q", got[0].summary, got[1].summary, got[2].summary)
	}
	if got[0].parentType != nil {
		t.Errorf("family-level bill should have no parent, got %v", *got[0].parentType)
	}

	// Child rows name their container; notes name their subject.
	var list, item, note uuid.UUID
	asActor(t, pool, actor{user: alex.String()}, func(tx pgx.Tx) {
		list = txInsertID(t, tx, `INSERT INTO lists (household_id, name) VALUES ($1, 'Groceries') RETURNING id`, f.household)
		item = txInsertID(t, tx, `INSERT INTO list_items (list_id, text) VALUES ($1, 'Milk') RETURNING id`, list)
		note = txInsertID(t, tx, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'vehicle', $2, 'Tires', 'rotate at 5k') RETURNING id`, f.household, f.vehicle)
	})
	if r := activityFor(t, pool, item); len(r) != 1 || r[0].parentType == nil || *r[0].parentType != "list" || !eqID(r[0].parentID, list) || r[0].summary != `created list item "Milk"` {
		t.Errorf("list item activity: %+v", r)
	}
	if r := activityFor(t, pool, note); len(r) != 1 || r[0].parentType == nil || *r[0].parentType != "vehicle" || !eqID(r[0].parentID, f.vehicle) {
		t.Errorf("note activity: %+v", r)
	}
	// The family feed query uses the (household_id, created_at) index.
	if n := countRows(t, pool, `SELECT count(*) FROM activity_log WHERE household_id = $1`, f.household); n < 6 {
		t.Errorf("household feed: %d rows", n)
	}
}

// Without app.user_id (or with a bad one) writes still succeed; the actor is
// NULL everywhere.
func TestAuthorship050_NoActor(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	alex := newUser(t, pool, "Alex")

	pet := insertID(t, pool, `INSERT INTO pets (household_id, name) VALUES ($1, 'Rex') RETURNING id`, f.household)
	for _, bad := range []string{"not-a-uuid", uuid.NewString(), "   "} {
		asActor(t, pool, actor{user: bad, agentToken: bad}, func(tx pgx.Tx) {
			txExec(t, tx, `UPDATE pets SET notes = $2 WHERE id = $1`, pet, "seen with "+bad)
		})
	}
	var createdBy, updatedBy *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT created_by, updated_by FROM pets WHERE id = $1`, pet), &createdBy, &updatedBy)
	if createdBy != nil || updatedBy != nil {
		t.Fatalf("no actor: created_by=%v updated_by=%v", createdBy, updatedBy)
	}
	got := activityFor(t, pool, pet)
	if len(got) != 4 {
		t.Fatalf("want 4 activity rows, got %d", len(got))
	}
	for _, r := range got {
		if r.actorUser != nil || r.actorAgent != nil || r.entityCreatedBy != nil {
			t.Errorf("no actor: row %+v", r)
		}
	}

	// An update without an actor clears updated_by (unknown editor), but an
	// explicit updated_by is respected and created_by is never touched.
	asActor(t, pool, actor{user: alex.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE pets SET notes = 'x', created_by = $2 WHERE id = $1`, pet, alex)
	})
	mustExec(t, pool, `UPDATE pets SET notes = 'y' WHERE id = $1`, pet)
	mustScan(t, pool.QueryRow(ctx, `SELECT created_by, updated_by FROM pets WHERE id = $1`, pet), &createdBy, &updatedBy)
	if !eqID(createdBy, alex) || updatedBy != nil {
		t.Errorf("system update: created_by=%v updated_by=%v", createdBy, updatedBy)
	}
	mustExec(t, pool, `UPDATE pets SET notes = 'z', updated_by = $2 WHERE id = $1`, pet, alex)
	mustScan(t, pool.QueryRow(ctx, `SELECT updated_by FROM pets WHERE id = $1`, pet), &updatedBy)
	if !eqID(updatedBy, alex) {
		t.Errorf("explicit updated_by not kept: %v", updatedBy)
	}

	// calendar_objects carry no household_id; it comes from the calendar.
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id`, f.household)
	ev := insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, 'u1', E'BEGIN:VEVENT\r\nSUMMARY:Dentist\r\nEND:VEVENT', 'e1') RETURNING id`, cal)
	if r := activityFor(t, pool, ev); len(r) != 1 || r[0].summary != `created calendar event "Dentist"` || r[0].parentType == nil || *r[0].parentType != "calendar" {
		t.Errorf("calendar event activity: %+v", r)
	}
}

// notes.author_id and created_by stay in step both ways, and
// guide_revisions.edited_by likewise.
func TestAuthorship050_LegacyAuthorColumns(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	alex := newUser(t, pool, "Alex")
	sam := newUser(t, pool, "Sam")

	// Today's API: writes author_id, sets no actor.
	n1 := insertID(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body, author_id) VALUES ($1, 'household', $1, 'a', $2) RETURNING id`, f.household, alex)
	// After #4491/#4492: sets the actor, writes no author_id.
	var n2 uuid.UUID
	asActor(t, pool, actor{user: sam.String()}, func(tx pgx.Tx) {
		n2 = txInsertID(t, tx, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'household', $1, 'b') RETURNING id`, f.household)
	})
	check := func(note, want uuid.UUID) {
		t.Helper()
		var author, createdBy *uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT author_id, created_by FROM notes WHERE id = $1`, note), &author, &createdBy)
		if !eqID(author, want) || !eqID(createdBy, want) {
			t.Errorf("note %v: author_id=%v created_by=%v, want %v", note, author, createdBy, want)
		}
	}
	check(n1, alex)
	check(n2, sam)
	mustExec(t, pool, `UPDATE notes SET author_id = $2 WHERE id = $1`, n1, sam)
	check(n1, sam)
	mustExec(t, pool, `UPDATE notes SET created_by = $2 WHERE id = $1`, n1, alex)
	check(n1, alex)

	guide := insertID(t, pool, `INSERT INTO guides (household_id, title) VALUES ($1, 'Reset router') RETURNING id`, f.household)
	rev := insertID(t, pool, `INSERT INTO guide_revisions (guide_id, body, edited_by) VALUES ($1, 'x', $2) RETURNING id`, guide, alex)
	var createdBy *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT created_by FROM guide_revisions WHERE id = $1`, rev), &createdBy)
	if !eqID(createdBy, alex) {
		t.Errorf("revision created_by=%v, want %v", createdBy, alex)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM activity_log WHERE entity_id = $1`, rev); n != 0 {
		t.Errorf("guide revisions are not logged, got %d rows", n)
	}

	// Deleting a user nulls authorship without blaming anyone or logging.
	before := countRows(t, pool, `SELECT count(*) FROM activity_log`)
	asActor(t, pool, actor{user: sam.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `DELETE FROM users WHERE id = $1`, alex)
	})
	var author, updatedBy *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT author_id, created_by, updated_by FROM notes WHERE id = $1`, n1), &author, &createdBy, &updatedBy)
	if author != nil || createdBy != nil || updatedBy != nil {
		t.Errorf("after user delete: author_id=%v created_by=%v updated_by=%v", author, createdBy, updatedBy)
	}
	if after := countRows(t, pool, `SELECT count(*) FROM activity_log`); after != before {
		t.Errorf("user delete wrote %d activity rows", after-before)
	}
}

// What is not logged: no-op updates, bookkeeping-only updates, deletes that
// follow from another delete, and anything while a household is deleted.
// Secret values, note bodies and URLs never reach the log.
func TestAuthorship050_NoiseAndSecrets(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)

	list := insertID(t, pool, `INSERT INTO lists (household_id, name) VALUES ($1, 'Packing') RETURNING id`, f.household)
	for _, text := range []string{"Socks", "Tent"} {
		mustExec(t, pool, `INSERT INTO list_items (list_id, text) VALUES ($1, $2)`, list, text)
	}
	mustExec(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'list', $2, 'remember the stove')`, f.household, list)
	mustExec(t, pool, `UPDATE lists SET name = name WHERE id = $1`, list)
	mustExec(t, pool, `UPDATE lists SET updated_at = now() + interval '1 minute' WHERE id = $1`, list)
	before := countRows(t, pool, `SELECT count(*) FROM activity_log`)
	mustExec(t, pool, `DELETE FROM lists WHERE id = $1`, list)
	if n := countRows(t, pool, `SELECT count(*) FROM activity_log`) - before; n != 1 {
		t.Errorf("deleting a list with items and a note: want 1 activity row, got %d", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM activity_log WHERE entity_id = $1 AND action = 'update'`, list); n != 0 {
		t.Errorf("no-op / bookkeeping updates logged: %d", n)
	}

	// A property delete logs the property, not its cascaded rooms and assets.
	asset := insertID(t, pool, `INSERT INTO assets (household_id, room_id, name) VALUES ($1, $2, 'Fridge') RETURNING id`, f.household, f.room)
	mustExec(t, pool, `DELETE FROM properties WHERE id = $1`, f.property)
	if n := countRows(t, pool, `SELECT count(*) FROM activity_log WHERE action = 'delete' AND entity_id IN ($1, $2)`, f.room, asset); n != 0 {
		t.Errorf("cascaded room/asset deletes logged: %d", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM activity_log WHERE action = 'delete' AND entity_id = $1`, f.property); n != 1 {
		t.Errorf("property delete not logged")
	}

	secret := insertID(t, pool, `INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type) VALUES ($1, 'TOPSECRETBYTES', 'iv', 'Wi-Fi', 'password') RETURNING id`, f.household)
	mustExec(t, pool, `UPDATE secrets SET encrypted_data = 'OTHERSECRET' WHERE id = $1`, secret)
	mustExec(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'household', $1, 'Alarm', 'code 4321')`, f.household)
	mustExec(t, pool, `INSERT INTO document_links (household_id, entity_type, entity_id, link_type, url) VALUES ($1, 'vehicle', $2, 'url', 'https://x/?token=abc')`, f.household, f.vehicle)
	if n := countRows(t, pool, `SELECT count(*) FROM activity_log
		WHERE summary ~ '(TOPSECRET|OTHERSECRET|4321|token=abc)' OR array_to_string(changed_fields, ',') ~ '(TOPSECRET|OTHERSECRET)'`); n != 0 {
		t.Errorf("%d activity rows leak secret values, bodies or URLs", n)
	}
	if r := activityFor(t, pool, secret); len(r) != 2 || r[1].summary != `updated secret "Wi-Fi" (encrypted_data)` {
		t.Errorf("secret activity: %+v", r)
	}

	// Deleting the household removes its log and writes nothing.
	mustExec(t, pool, `DELETE FROM households WHERE id = $1`, f.household)
	if n := countRows(t, pool, `SELECT count(*) FROM activity_log WHERE household_id = $1`, f.household); n != 0 {
		t.Errorf("household delete left %d activity rows", n)
	}
}

// up/down/up: down drops the log, the columns and the triggers and keeps
// notes.author_id; up backfills created_by from it again.
func TestAuthorship050_Reversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	f := seedV2Family(t, pool)
	alex := newUser(t, pool, "Alex")

	var note uuid.UUID
	asActor(t, pool, actor{user: alex.String()}, func(tx pgx.Tx) {
		note = txInsertID(t, tx, `INSERT INTO notes (household_id, entity_type, entity_id, body) VALUES ($1, 'household', $1, 'x') RETURNING id`, f.household)
		txExec(t, tx, `INSERT INTO guides (household_id, title) VALUES ($1, 'G')`, f.household)
	})

	for cycle := 0; cycle < 2; cycle++ {
		applyMigration(t, pool, "073_legacy_bill_events.down.sql")        // newest first: 073 sits on top of everything below
		applyMigration(t, pool, "071_person_color.down.sql")              // newest first: drops people.color and its palette CHECK
		applyMigration(t, pool, "070_entity_kind_notifications.down.sql") // newest first: notifications/calendar_objects entity_type back to VARCHAR
		applyMigration(t, pool, "055_activity_event_titles.down.sql")     // restores 050's log_activity
		applyMigration(t, pool, "054_change_notifications.down.sql")      // its trigger is on 050's activity_log
		applyMigration(t, pool, "050_authorship.down.sql")
		if n := countRows(t, pool, `SELECT count(*) FROM pg_tables WHERE tablename = 'activity_log'`); n != 0 {
			t.Fatalf("cycle %d: activity_log left", cycle)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns WHERE column_name IN ('created_by', 'updated_by') AND table_schema = 'public'`); n != 0 {
			t.Fatalf("cycle %d: %d authorship columns left", cycle, n)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname LIKE '%\_authorship' OR tgname LIKE '%\_activity'`); n != 0 {
			t.Fatalf("cycle %d: %d triggers left", cycle, n)
		}
		var author *uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT author_id FROM notes WHERE id = $1`, note), &author)
		if !eqID(author, alex) {
			t.Fatalf("cycle %d: down lost notes.author_id: %v", cycle, author)
		}
		// Writes work while 050 is down.
		mustExec(t, pool, `UPDATE notes SET body = 'y' WHERE id = $1`, note)

		applyMigration(t, pool, "050_authorship.up.sql")
		// 055 restored before 070 and 073: 073's reconcile titles entries with
		// 055's calendar_event_label(), and 055 sits between 050 and 070.
		applyMigration(t, pool, "055_activity_event_titles.up.sql")
		applyMigration(t, pool, "070_entity_kind_notifications.up.sql")
		applyMigration(t, pool, "071_person_color.up.sql")
		applyMigration(t, pool, "073_legacy_bill_events.up.sql") // back to the head
		var createdBy *uuid.UUID
		mustScan(t, pool.QueryRow(ctx, `SELECT created_by FROM notes WHERE id = $1`, note), &createdBy)
		if !eqID(createdBy, alex) {
			t.Fatalf("cycle %d: up did not backfill created_by: %v", cycle, createdBy)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname LIKE '%\_authorship'`); n != 26 {
			t.Errorf("cycle %d: want 26 authorship triggers, got %d", cycle, n)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname LIKE '%\_activity'`); n != 25 {
			t.Errorf("cycle %d: want 25 activity triggers, got %d", cycle, n)
		}
	}
}
