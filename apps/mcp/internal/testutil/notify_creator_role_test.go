package testutil

// Tests for migration 061 (creator notifications follow the creator's
// current role). See KB architecture/change-notifications.md and
// decisions/2026-11-15-role-policy.md. The SQL role table is kept in step
// with apps/api/internal/auth/roles.go by apps/api/internal/auth's
// TestRoleCanSeeEntitySQLMatchesPolicy.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The reviewer's case from #4501: an owner creates a credential, is demoted
// to viewer, and another member updates it. The viewer must not be told the
// credential's name.
func TestNotifyCreatorRole061_DemotedViewerGetsNoCredentialNames(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)
	amber := newUser(t, pool, "Amber")
	mustExec(t, pool, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, 'owner')`, f.household, amber)
	dana := newMember(t, pool, f.household, "Dana")

	var secret, secretNote, bill uuid.UUID
	asActor(t, pool, actor{user: amber.String()}, func(tx pgx.Tx) {
		secret = txInsertID(t, tx, `INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type) VALUES ($1, 'BYTES', 'iv', 'Bank of America login', 'password') RETURNING id`, f.household)
		secretNote = txInsertID(t, tx, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'secret', $2, 'Security questions', 'x') RETURNING id`, f.household, secret)
		bill = txInsertID(t, tx, `INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Electric', 120) RETURNING id`, f.household)
	})
	mustExec(t, pool, `UPDATE memberships SET role = 'viewer' WHERE household_id = $1 AND user_id = $2`, f.household, amber)

	asActor(t, pool, actor{user: dana.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `UPDATE secrets SET encrypted_data = 'NEW' WHERE id = $1`, secret)
		txExec(t, tx, `UPDATE notes SET title = 'Security Qs' WHERE id = $1`, secretNote)
		txExec(t, tx, `UPDATE bills SET amount = 130 WHERE id = $1`, bill)
	})
	asActor(t, pool, actor{user: dana.String()}, func(tx pgx.Tx) {
		txExec(t, tx, `DELETE FROM notes WHERE id = $1`, secretNote) // a delete links to the secret
		txExec(t, tx, `DELETE FROM secrets WHERE id = $1`, secret)
	})

	got := changeNotices(t, pool, amber)
	if len(got) != 1 || got[0].title != `Dana updated bill "Electric"` {
		t.Fatalf("viewer: want only the bill notice, got %+v", got)
	}
}

// Every role gets notices only for kinds (and parent kinds) it may read.
func TestNotifyCreatorRole061_RoleMatrix(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)
	dana := newMember(t, pool, f.household, "Dana")

	type record struct {
		name   string
		create func(tx pgx.Tx) uuid.UUID
		update string // $1 = the record's id
	}
	var (
		secretRec = record{"secret", func(tx pgx.Tx) uuid.UUID {
			return txInsertID(t, tx, `INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type) VALUES ($1, 'B', 'iv', 'Wi-Fi', 'password') RETURNING id`, f.household)
		}, `UPDATE secrets SET name = 'WiFi' WHERE id = $1`}
		secretNoteRec = record{"note on secret", func(tx pgx.Tx) uuid.UUID {
			s := txInsertID(t, tx, `INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type) VALUES ($1, 'B', 'iv', 'Alarm', 'pin') RETURNING id`, f.household)
			return txInsertID(t, tx, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'secret', $2, 'Alarm codes', 'x') RETURNING id`, f.household, s)
		}, `UPDATE notes SET title = 'Alarm code' WHERE id = $1`}
		billRec = record{"bill", func(tx pgx.Tx) uuid.UUID {
			return txInsertID(t, tx, `INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Water', 40) RETURNING id`, f.household)
		}, `UPDATE bills SET amount = 41 WHERE id = $1`}
		billNoteRec = record{"note on bill", func(tx pgx.Tx) uuid.UUID {
			b := txInsertID(t, tx, `INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Gas', 40) RETURNING id`, f.household)
			return txInsertID(t, tx, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'bill', $2, 'Autopay', 'x') RETURNING id`, f.household, b)
		}, `UPDATE notes SET title = 'Auto-pay' WHERE id = $1`}
		listItemRec = record{"list item", func(tx pgx.Tx) uuid.UUID {
			l := txInsertID(t, tx, `INSERT INTO lists (household_id, name) VALUES ($1, 'Groceries') RETURNING id`, f.household)
			return txInsertID(t, tx, `INSERT INTO list_items (list_id, text) VALUES ($1, 'Milk') RETURNING id`, l)
		}, `UPDATE list_items SET checked = true WHERE id = $1`}
		petRec = record{"pet", func(tx pgx.Tx) uuid.UUID {
			return txInsertID(t, tx, `INSERT INTO pets (household_id, name) VALUES ($1, 'Rex') RETURNING id`, f.household)
		}, `UPDATE pets SET name = 'Rexy' WHERE id = $1`}
		taskRec = record{"maintenance task", func(tx pgx.Tx) uuid.UUID {
			return txInsertID(t, tx, `INSERT INTO maintenance_tasks (household_id, property_id, name) VALUES ($1, $2, 'Gutters') RETURNING id`, f.household, f.property)
		}, `UPDATE maintenance_tasks SET status = 'done' WHERE id = $1`}
		vehicleRec = record{"vehicle", func(tx pgx.Tx) uuid.UUID {
			return txInsertID(t, tx, `INSERT INTO vehicles (household_id, make) VALUES ($1, 'Honda') RETURNING id`, f.household)
		}, `UPDATE vehicles SET make = 'Toyota' WHERE id = $1`}
	)

	cases := []struct {
		role   string
		rec    record
		notify bool
	}{
		{"viewer", secretRec, false},
		{"viewer", secretNoteRec, false},
		{"viewer", billRec, true},
		{"viewer", vehicleRec, true},
		{"housesitter", billRec, false},
		{"housesitter", billNoteRec, false},
		{"housesitter", vehicleRec, false},
		{"housesitter", secretRec, false},
		{"housesitter", listItemRec, true},
		{"housesitter", petRec, true},
		{"vendor", billRec, false},
		{"vendor", listItemRec, false},
		{"vendor", petRec, false},
		{"vendor", taskRec, true},
		// Family roles are unchanged.
		{"family_member", secretRec, true},
		{"family_member", secretNoteRec, true},
		{"family_manager", billNoteRec, true},
		{"owner", secretRec, true},
	}
	for _, tc := range cases {
		t.Run(tc.role+"/"+tc.rec.name, func(t *testing.T) {
			creator := newMember(t, pool, f.household, "Creator")
			var id uuid.UUID
			asActor(t, pool, actor{user: creator.String()}, func(tx pgx.Tx) { id = tc.rec.create(tx) })
			mustExec(t, pool, `UPDATE memberships SET role = $3 WHERE household_id = $1 AND user_id = $2`, f.household, creator, tc.role)
			asActor(t, pool, actor{user: dana.String()}, func(tx pgx.Tx) { txExec(t, tx, tc.rec.update, id) })
			got := changeNotices(t, pool, creator)
			if tc.notify && len(got) != 1 {
				t.Errorf("want 1 notice, got %+v", got)
			}
			if !tc.notify && len(got) != 0 {
				t.Errorf("want no notice, got %+v", got)
			}
		})
	}
}

// role_can_see_entity fails closed for unknown kinds and roles.
func TestNotifyCreatorRole061_FailsClosed(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	for _, tc := range []struct {
		role, kind string
		want       bool
	}{
		{"owner", "future_kind", true},
		{"family_manager", "future_kind", true},
		{"family_member", "future_kind", true},
		{"viewer", "future_kind", false},
		{"housesitter", "future_kind", false},
		{"vendor", "future_kind", false},
		{"bogus_role", "bill", false},
		{"", "pet", false},
	} {
		var got bool
		mustScan(t, pool.QueryRow(ctx, `SELECT role_can_see_entity($1, $2)`, tc.role, tc.kind), &got)
		if got != tc.want {
			t.Errorf("role_can_see_entity(%q, %q) = %v, want %v", tc.role, tc.kind, got, tc.want)
		}
	}
	var got *bool
	mustScan(t, pool.QueryRow(ctx, `SELECT role_can_see_entity(NULL, 'pet')`), &got)
	if got == nil || *got {
		t.Errorf("role_can_see_entity(NULL, pet) = %v, want false", got)
	}
}

// up/down/up: down restores 054's membership-only check; up brings the role
// check back.
func TestNotifyCreatorRole061_Reversible(t *testing.T) {
	pool := NewPool(t)
	f := seedV2Family(t, pool)
	dana := newMember(t, pool, f.household, "Dana")
	viewer := newMember(t, pool, f.household, "Vic")

	touchSecret := func() {
		var id uuid.UUID
		asActor(t, pool, actor{user: viewer.String()}, func(tx pgx.Tx) {
			id = txInsertID(t, tx, `INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type) VALUES ($1, 'B', 'iv', 'Router', 'password') RETURNING id`, f.household)
		})
		asActor(t, pool, actor{user: dana.String()}, func(tx pgx.Tx) {
			txExec(t, tx, `UPDATE secrets SET name = 'Router admin' WHERE id = $1`, id)
		})
	}
	mustExec(t, pool, `UPDATE memberships SET role = 'viewer' WHERE household_id = $1 AND user_id = $2`, f.household, viewer)

	for cycle := 0; cycle < 2; cycle++ {
		before := len(changeNotices(t, pool, viewer))
		touchSecret()
		if after := len(changeNotices(t, pool, viewer)); after != before {
			t.Fatalf("cycle %d: up: viewer notified about a secret", cycle)
		}

		applyMigration(t, pool, "061_notify_creator_role.down.sql")
		if n := countRows(t, pool, `SELECT count(*) FROM pg_proc WHERE proname = 'role_can_see_entity'`); n != 0 {
			t.Fatalf("cycle %d: role_can_see_entity left after down", cycle)
		}
		touchSecret() // 054's behaviour: members are notified whatever their role
		if after := len(changeNotices(t, pool, viewer)); after != before+1 {
			t.Fatalf("cycle %d: down did not restore 054 (%d -> %d)", cycle, before, after)
		}
		applyMigration(t, pool, "061_notify_creator_role.up.sql")
	}
}
