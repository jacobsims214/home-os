package note

// #4881: note_find and note_get follow the household role policy by the
// note's PARENT. A note is derived data ("passport number 123" on a
// credential, "the renewal is late" on a bill), so a note the role may not
// see is treated as absent: omitted from note_find and "not found" on
// note_get, exactly as the API answers (/api/v1/notes, #4775/#4798).
//
// The matrix, on a real fully migrated Postgres:
//
//	note about        owner/family_manager/family_member  viewer  housesitter  vendor
//	family            yes                                 yes     no           no
//	pet               yes                                 yes     yes          no
//	property          yes                                 yes     yes          no
//	bill (money)      yes                                 yes     NO           no
//	secret            yes                                 NO      no           no
//	file on a secret  yes                                 NO      no           no
//	file linked from
//	  a secret        yes                                 NO      no           no
//	plain file        yes                                 yes     no           no

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// testPool is the database these tests run on: the package's usual one (a
// fresh, fully migrated Postgres 16 container, one per test binary) or, when
// TEST_DATABASE_URL is set, that database instead (every migration applied,
// the app role created — the same contract apps/api's DB tests have). The
// TEST_DATABASE_URL form lets a run pin the schema it tests against.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		return testutil.NewPool(t)
	}
	pool, err := server.NewPool(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect TEST_DATABASE_URL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// cleanupHousehold removes a seeded household from a shared database (no-op
// in effect on the container, which is dropped with the test binary): the
// household cascade takes its notes, bills, secrets and files with it, and
// the blobs left without a file go too.
func cleanupHousehold(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		return
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM households WHERE id = $1`, id)
		_, _ = pool.Exec(bg, `DELETE FROM file_blobs b WHERE NOT EXISTS (SELECT 1 FROM files f WHERE f.blob_id = b.id)`)
	})
}

// roleCtx adds a member with role to hh and returns their context.
func roleCtx(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) context.Context {
	t.Helper()
	email := role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
		email, role).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
		hh.HouseholdID, uid, role); err != nil {
		t.Fatal(err)
	}
	return server.ContextWithClaims(context.Background(), &auth.Claims{
		Identity:    auth.Identity{Subject: uid.String(), Email: email},
		UserID:      uid.String(),
		HouseholdID: hh.HouseholdID.String(),
		Role:        role,
	})
}

// noteFixture is one household with a note on every kind of parent the role
// matrix distinguishes, including the two ways a document can be tied to a
// credential (attached, and linked from).
type noteFixture struct {
	notes map[string]uuid.UUID // keyed by the parent: family, pet, property, bill, secret, file_secret, file_linked, file_plain
}

// seedNoteFixture builds the household. Every note's body names its parent,
// so a leak is visible in a preview as well as in the id list.
func seedNoteFixture(t *testing.T, pool *pgxpool.Pool, hh testutil.Household) noteFixture {
	t.Helper()
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	seedID := func(sql string, args ...any) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(bg, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}

	petID := seedRow(t, pool, "pets", hh.HouseholdID)
	propertyID := seedRow(t, pool, "properties", hh.HouseholdID)
	billID := seedID(`INSERT INTO bills (household_id, name, amount) VALUES ($1, 'Power', 120) RETURNING id`, hh.HouseholdID)
	secretID := seedID(`INSERT INTO secrets (household_id, entity_type, entity_id, encrypted_data, iv, name, secret_type)
		VALUES ($1, 'household', $1, 'enc', 'iv', 'Passport', 'document') RETURNING id`, hh.HouseholdID)

	// A document attached to the credential (files.entity_type = 'secret').
	attached := seedID(`WITH b AS (INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id)
		INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id, title)
		SELECT $1, b.id, 'passport.jpg', 1, 'secret', $2, 'Passport scan' FROM b RETURNING id`,
		hh.HouseholdID, secretID)
	// A plain document, and a second one linked FROM the credential (a
	// document_links row of a secret pointing at the file).
	plain := seedID(`WITH b AS (INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id)
		INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id, title)
		SELECT $1, b.id, 'manual.txt', 1, 'household', $1, 'Router manual' FROM b RETURNING id`,
		hh.HouseholdID)
	linked := seedID(`WITH b AS (INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id)
		INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id, title)
		SELECT $1, b.id, 'backup.txt', 1, 'household', $1, 'Router backup' FROM b RETURNING id`,
		hh.HouseholdID)
	exec(`INSERT INTO document_links (household_id, entity_type, entity_id, link_type, link_id, title)
	      VALUES ($1, 'secret', $2, 'file', $3, 'Router backup')`, hh.HouseholdID, secretID, linked.String())

	f := noteFixture{notes: map[string]uuid.UUID{}}
	insert := func(key, kind string, parent uuid.UUID) {
		t.Helper()
		f.notes[key] = seedID(`INSERT INTO notes (household_id, entity_type, entity_id, title, body, author_id)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			hh.HouseholdID, kind, parent, "About "+key, "the "+key+" note body", hh.UserID)
	}
	insert("family", "household", hh.HouseholdID)
	insert("pet", "pet", petID)
	insert("property", "property", propertyID)
	insert("bill", "bill", billID)
	insert("secret", "secret", secretID)
	insert("file_secret", "file", attached)
	insert("file_linked", "file", linked)
	insert("file_plain", "file", plain)
	return f
}

// visibleTo is the expected matrix above, written out per role.
func visibleTo(role string) map[string]bool {
	all := []string{"family", "pet", "property", "bill", "secret", "file_secret", "file_linked", "file_plain"}
	out := map[string]bool{}
	for _, k := range all {
		out[k] = false
	}
	switch role {
	case auth.RoleOwner, auth.RoleFamilyManager, auth.RoleFamilyMember:
		for _, k := range all {
			out[k] = true
		}
	case auth.RoleViewer:
		for _, k := range []string{"family", "pet", "property", "bill", "file_plain"} {
			out[k] = true
		}
	case auth.RoleHousesitter:
		for _, k := range []string{"pet", "property"} {
			out[k] = true
		}
	}
	return out
}

// TestNoteReadsFollowRolePolicy is the #4881 regression test: every role
// sees exactly the notes whose parent it may see, and a hidden note is
// indistinguishable from a missing one on note_get.
func TestNoteReadsFollowRolePolicy(t *testing.T) {
	pool := testPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool) // owner
	cleanupHousehold(t, pool, hh.HouseholdID)
	f := seedNoteFixture(t, pool, hh)

	_, findTool, findHandler := NewNoteFindTool(pool)
	find := newCaller[wireSummary](t, "note_find", findTool, findHandler)
	_, getTool, getHandler := NewNoteGetTool(pool)
	get := newCaller[wireNote](t, "note_get", getTool, getHandler)

	// note_find: exactly the visible notes, and their previews name only
	// visible parents.
	list := func(roleCtx context.Context) map[string]uuid.UUID {
		t.Helper()
		env := find.mustOK(t, roleCtx, map[string]any{"limit": maxFindLimit})
		got := map[string]uuid.UUID{}
		for _, it := range env.Items {
			if it.Title == nil {
				t.Fatalf("note %s has no title", it.ID)
			}
			got[*it.Title] = it.ID
		}
		return got
	}

	for _, role := range auth.AllRoles {
		role := role
		t.Run("note_find/"+role, func(t *testing.T) {
			mctx := ctx // the owner seeded with the household
			if role != auth.RoleOwner {
				mctx = roleCtx(t, pool, hh, role)
			}
			want := visibleTo(role)
			got := list(mctx)
			for key, id := range f.notes {
				title := "About " + key
				if want[key] {
					if got[title] != id {
						t.Errorf("%s: note on %s is missing from note_find", role, key)
					}
					continue
				}
				if _, found := got[title]; found {
					t.Errorf("%s: note on %s leaked into note_find", role, key)
				}
			}
			if len(got) != lenKeys(want) {
				t.Errorf("%s: note_find returned %d notes, want %d", role, len(got), lenKeys(want))
			}
		})

		t.Run("note_get/"+role, func(t *testing.T) {
			mctx := ctx
			if role != auth.RoleOwner {
				mctx = roleCtx(t, pool, hh, role)
			}
			want := visibleTo(role)
			// A note that does not exist, as a template: a hidden note must
			// answer with the same message and fix, the id aside.
			missing := uuid.NewString()
			missingMsg, missingFix := getNotFound(t, get, mctx, missing)
			for key, id := range f.notes {
				isErr, env := get.call(t, mctx, map[string]any{"note_id": id.String()})
				if want[key] {
					if isErr || !env.OK || env.Data == nil || env.Data.ID != id {
						t.Errorf("%s: note on %s: want the note, got isError=%v %+v", role, key, isErr, env)
					}
					continue
				}
				if !isErr || env.Error == nil {
					t.Fatalf("%s: note on %s leaked: isError=%v %+v", role, key, isErr, env)
				}
				if env.Data != nil {
					t.Errorf("%s: note on %s leaked data on the error: %+v", role, key, env.Data)
				}
				// The only difference allowed from a missing note is the id
				// the caller passed in. Nothing about the note itself (its
				// title, parent kind, author) may appear.
				gotMsg := strings.ReplaceAll(env.Error.Message, id.String(), "<id>")
				wantMsg := strings.ReplaceAll(missingMsg, missing, "<id>")
				if gotMsg != wantMsg || env.Error.Fix != missingFix {
					t.Errorf("%s: note on %s answers differently from a missing note:\n got %q / %q\nwant %q / %q",
						role, key, gotMsg, env.Error.Fix, wantMsg, missingFix)
				}
			}
		})
	}
}

// getNotFound returns the message and fix a not-found note_get answers, so a
// hidden note can be compared with a missing one.
func getNotFound(t *testing.T, get caller[wireNote], ctx context.Context, id string) (message, fix string) {
	t.Helper()
	isErr, env := get.call(t, ctx, map[string]any{"note_id": id})
	if !isErr || env.Error == nil {
		t.Fatalf("note_get %s: want not found, got %+v", id, env)
	}
	return env.Error.Message, env.Error.Fix
}

func lenKeys(m map[string]bool) int {
	n := 0
	for _, v := range m {
		if v {
			n++
		}
	}
	return n
}

// TestNoteReadsUseCurrentRole: the role is re-read from memberships on every
// call, so a demotion applies at once (an owner demoted to viewer loses the
// note on the credential, though the claims still say owner).
func TestNoteReadsUseCurrentRole(t *testing.T) {
	pool := testPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	cleanupHousehold(t, pool, hh.HouseholdID)
	f := seedNoteFixture(t, pool, hh)

	_, getTool, getHandler := NewNoteGetTool(pool)
	get := newCaller[wireNote](t, "note_get", getTool, getHandler)
	secretNote := f.notes["secret"]

	// The claims in ctx still carry the owner role; the membership now says
	// viewer, and that is what the tool must use.
	if _, err := pool.Exec(context.Background(),
		`UPDATE memberships SET role = 'viewer' WHERE household_id = $1 AND user_id = $2`,
		hh.HouseholdID, hh.UserID); err != nil {
		t.Fatal(err)
	}
	isErr, env := get.call(t, ctx, map[string]any{"note_id": secretNote.String()})
	if !isErr || env.Error == nil || env.Data != nil {
		t.Fatalf("a demoted owner still read the note on the credential: isError=%v %+v", isErr, env)
	}
	if want := fmt.Sprintf("No note with id %q was found.", secretNote); env.Error.Message != want {
		t.Errorf("message: want %q, got %q", want, env.Error.Message)
	}
}

// TestVisibleNotesSQLMentionsBothRules guards the helper: the role and the
// secrets flag are bound parameters (never role names in SQL text), and the
// credential-file rule is the shared file.SecretFileSQL condition.
func TestVisibleNotesSQLMentionsBothRules(t *testing.T) {
	got := visibleNotesSQL("$7::text", "$8::boolean")
	for _, want := range []string{
		"role_can_see_entity($7::text, 'note')",
		"role_can_see_entity($7::text, n.entity_type::text)",
		"$8::boolean",
		"n.entity_type::text <> 'secret'",
		"n.entity_type::text = 'file'",
		"document_links",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("visibleNotesSQL is missing %q:\n%s", want, got)
		}
	}
	for _, role := range auth.AllRoles {
		if strings.Contains(got, "'"+role+"'") {
			t.Errorf("visibleNotesSQL hard-codes the role %q:\n%s", role, got)
		}
	}
}
