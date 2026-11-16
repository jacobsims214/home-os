package file

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// wireDeleted is document_delete's data.
type wireDeleted struct {
	ID           uuid.UUID `json:"id"`
	Title        string    `json:"title"`
	AboutType    string    `json:"about_type"`
	LinksRemoved int       `json:"links_removed"`
}

// memberCtx adds a member with role to hh and returns their claims context.
func memberCtx(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) (context.Context, uuid.UUID) {
	t.Helper()
	bg := context.Background()
	email := role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(bg, `INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`, email, role).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hh.HouseholdID, uid, role); err != nil {
		t.Fatal(err)
	}
	return server.ContextWithClaims(bg, &auth.Claims{
		Identity: auth.Identity{Subject: uid.String(), Email: email}, UserID: uid.String(),
		HouseholdID: hh.HouseholdID.String(), Role: role,
	}), uid
}

type docTools struct {
	up   caller[wireDoc]
	find caller[wireItem]
	get  caller[wireDoc]
	upd  caller[wireDoc]
	del  caller[wireDeleted]
}

func newDocTools(t *testing.T, pool *pgxpool.Pool, ts *Typesense) docTools {
	t.Helper()
	var d docTools
	n, tl, h := NewDocumentUploadTool(pool, ts)
	d.up = newCaller[wireDoc](t, n, tl, h)
	n, tl, h = NewDocumentFindTool(pool, ts)
	d.find = newCaller[wireItem](t, n, tl, h)
	n, tl, h = NewDocumentGetTool(pool)
	d.get = newCaller[wireDoc](t, n, tl, h)
	n, tl, h = NewDocumentUpdateTool(pool, ts)
	d.upd = newCaller[wireDoc](t, n, tl, h)
	n, tl, h = NewDocumentDeleteTool(pool, ts)
	d.del = newCaller[wireDeleted](t, n, tl, h)
	return d
}

func TestDocumentUpdate(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, other := testutil.SeedHousehold(t, pool)
	d := newDocTools(t, pool, nil)

	vehicleID := seed(t, pool, `INSERT INTO vehicles (household_id, year, make, model) VALUES ($1, 2022, 'Honda', 'Odyssey') RETURNING id`, hh.HouseholdID)
	petID := seed(t, pool, `INSERT INTO pets (household_id, name, species) VALUES ($1, 'Rex', 'dog') RETURNING id`, hh.HouseholdID)
	theirVehicle := seed(t, pool, `INSERT INTO vehicles (household_id, make, model) VALUES ($1, 'Kia', 'Soul') RETURNING id`, other.HouseholdID)
	seed(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Nora') RETURNING id`, hh.HouseholdID)
	seed(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, 'Stranger') RETURNING id`, other.HouseholdID)

	doc := d.up.mustOK(t, ctx, map[string]any{"name": "scan.txt", "text": "Declarations page", "doc_date": "2026-01-10",
		"description": "old notes", "tags": []any{"a", "b"}}).Data
	id := doc.ID.String()

	t.Run("fields change, others stay", func(t *testing.T) {
		env := d.upd.mustOK(t, ctx, map[string]any{"document_id": id, "title": "Geico auto policy", "category": "Insurance",
			"person_id": "nora", "expires_on": "2027-01-10", "about_type": "vehicle", "about_id": vehicleID.String()})
		g := env.Data
		if g.Title != "Geico auto policy" || g.Category != "insurance" || g.PersonName == nil || *g.PersonName != "Nora" ||
			g.ExpiresOn == nil || *g.ExpiresOn != "2027-01-10" || g.AboutType != "vehicle" || g.AboutName == nil ||
			*g.AboutName != "2022 Honda Odyssey" {
			t.Fatalf("update: %+v", g)
		}
		if g.DocumentDate == nil || *g.DocumentDate != "2026-01-10" || g.Description == nil || *g.Description != "old notes" ||
			strings.Join(g.Tags, ",") != "a,b" {
			t.Errorf("untouched fields changed: %+v", g)
		}
		if !strings.Contains(env.Summary, "Changed title, category, person_id, expires_on, about") {
			t.Errorf("summary: %s", env.Summary)
		}
		// a repeat changes nothing more
		again := d.upd.mustOK(t, ctx, map[string]any{"document_id": id, "title": "Geico auto policy"}).Data
		if again.Title != g.Title || again.Category != g.Category {
			t.Errorf("repeat: %+v", again)
		}
	})

	t.Run("null clears, empty string is not given", func(t *testing.T) {
		g := d.upd.mustOK(t, ctx, map[string]any{"document_id": id, "description": nil, "person_id": nil,
			"tags": nil, "title": "", "expires_on": nil}).Data
		if g.Description != nil || g.PersonID != nil || len(g.Tags) != 0 || g.ExpiresOn != nil {
			t.Errorf("not cleared: %+v", g)
		}
		if g.Title != "Geico auto policy" {
			t.Errorf("empty title must not change it: %q", g.Title)
		}
		g = d.upd.mustOK(t, ctx, map[string]any{"document_id": id, "title": nil, "tags": []any{"car"}}).Data
		if g.Title != "scan.txt" || strings.Join(g.Tags, ",") != "car" {
			t.Errorf("title null falls back to the file name: %+v", g)
		}
	})

	t.Run("links add, list, are idempotent, remove", func(t *testing.T) {
		link := []any{map[string]any{"about_type": "pet", "about_id": petID.String()}}
		env := d.upd.mustOK(t, ctx, map[string]any{"document_id": id, "add_links": link})
		if len(env.Data.Links) != 1 || env.Data.Links[0].AboutType != "pet" || env.Data.Links[0].AboutID != petID ||
			env.Data.Links[0].AboutName == nil || *env.Data.Links[0].AboutName != "Rex" {
			t.Fatalf("links: %+v", env.Data.Links)
		}
		env = d.upd.mustOK(t, ctx, map[string]any{"document_id": id, "add_links": link})
		if len(env.Data.Links) != 1 || !strings.Contains(env.Summary, "1 already linked") {
			t.Errorf("repeat add: %+v %s", env.Data.Links, env.Summary)
		}
		got := d.get.mustOK(t, ctx, map[string]any{"document_id": id}).Data
		if len(got.Links) != 1 {
			t.Errorf("document_get links: %+v", got.Links)
		}
		env = d.upd.mustOK(t, ctx, map[string]any{"document_id": id, "remove_links": link})
		if len(env.Data.Links) != 0 || !strings.Contains(env.Summary, "Removed 1 link") {
			t.Errorf("remove: %+v %s", env.Data.Links, env.Summary)
		}
	})

	t.Run("about family", func(t *testing.T) {
		g := d.upd.mustOK(t, ctx, map[string]any{"document_id": id, "about_type": "family"}).Data
		if g.AboutType != "family" || g.AboutID != nil {
			t.Errorf("family: %+v", g)
		}
	})

	theirs := d.up.mustOK(t, otherCtx, map[string]any{"name": "theirs.txt", "text": "x"}).Data
	runErrCases(t, d.upd, []errCase{
		{"missing id", ctx, map[string]any{"title": "x"}, "document_id", "required"},
		{"bad id", ctx, map[string]any{"document_id": "Geico", "title": "x"}, "document_id", "not a UUID"},
		{"nothing to change", ctx, map[string]any{"document_id": id, "title": ""}, "document_id", "Nothing to change"},
		{"another family's document", ctx, map[string]any{"document_id": theirs.ID.String(), "title": "x"}, "", "No document"},
		{"category null", ctx, map[string]any{"document_id": id, "category": nil}, "category", "can't be cleared"},
		{"unknown category", ctx, map[string]any{"document_id": id, "category": "spaceship"}, "category", "not one of"},
		{"bad date", ctx, map[string]any{"document_id": id, "doc_date": "Jan 5"}, "doc_date", "not a date"},
		{"expiry before stored date", ctx, map[string]any{"document_id": id, "expires_on": "2025-01-01"}, "expires_on", "before doc_date 2026-01-10"},
		{"person of another family", ctx, map[string]any{"document_id": id, "person_id": "Stranger"}, "person_id", "No person named"},
		{"about another family's vehicle", ctx, map[string]any{"document_id": id, "about_type": "vehicle", "about_id": theirVehicle.String()}, "about_id", "No vehicle"},
		{"about without id", ctx, map[string]any{"document_id": id, "about_type": "vehicle"}, "about_id", "needs about_id"},
		{"link to another family's vehicle", ctx, map[string]any{"document_id": id,
			"add_links": []any{map[string]any{"about_type": "vehicle", "about_id": theirVehicle.String()}}}, "add_links", "No vehicle"},
		{"link to family", ctx, map[string]any{"document_id": id,
			"add_links": []any{map[string]any{"about_type": "family", "about_id": hh.HouseholdID.String()}}}, "add_links", "not a kind"},
		{"link without uuid", ctx, map[string]any{"document_id": id,
			"add_links": []any{map[string]any{"about_type": "pet", "about_id": "Rex"}}}, "add_links", "not a UUID"},
		{"tags not a list", ctx, map[string]any{"document_id": id, "tags": 5.0}, "tags", "list of strings"},
	})
	// nothing above changed their document
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM files WHERE id = $1 AND title IS NULL`, theirs.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("their document changed: %d %v", n, err)
	}
}

func TestDocumentDelete(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)
	d := newDocTools(t, pool, nil)
	bg := context.Background()

	petID := seed(t, pool, `INSERT INTO pets (household_id, name, species) VALUES ($1, 'Rex', 'dog') RETURNING id`, hh.HouseholdID)
	doc := d.up.mustOK(t, ctx, map[string]any{"name": "vaccines.txt", "text": "Rabies 2026", "category": "medical"}).Data
	d.upd.mustOK(t, ctx, map[string]any{"document_id": doc.ID.String(),
		"add_links": []any{map[string]any{"about_type": "pet", "about_id": petID.String()}}})
	theirs := d.up.mustOK(t, otherCtx, map[string]any{"name": "theirs.txt", "text": "x"}).Data

	runErrCases(t, d.del, []errCase{
		{"another family's", ctx, map[string]any{"document_id": theirs.ID.String()}, "", "No document"},
		{"bad id", ctx, map[string]any{"document_id": "vaccines"}, "document_id", "not a UUID"},
	})

	env := d.del.mustOK(t, ctx, map[string]any{"document_id": doc.ID.String()})
	if env.Data.ID != doc.ID || env.Data.LinksRemoved != 1 || !strings.Contains(env.Summary, "Deleted document") {
		t.Fatalf("delete: %+v %s", env.Data, env.Summary)
	}
	var files, blobs, links, queued int
	err := pool.QueryRow(bg, `SELECT
		(SELECT count(*) FROM files WHERE id = $1),
		(SELECT count(*) FROM file_blobs b WHERE NOT EXISTS (SELECT 1 FROM files f WHERE f.blob_id = b.id)),
		(SELECT count(*) FROM document_links WHERE link_id = $1::text),
		(SELECT count(*) FROM file_search_deletions WHERE file_id = $1)`, doc.ID).Scan(&files, &blobs, &links, &queued)
	if err != nil || files != 0 || blobs != 0 || links != 0 || queued != 1 {
		t.Errorf("after delete: files=%d orphan blobs=%d links=%d queued=%d err=%v", files, blobs, links, queued, err)
	}
	// a repeat is a safe not-found
	runErrCases(t, d.del, []errCase{{"repeat", ctx, map[string]any{"document_id": doc.ID.String()}, "", "No document"}})
	if err := pool.QueryRow(bg, `SELECT count(*) FROM files WHERE id = $1`, theirs.ID).Scan(&files); err != nil || files != 1 {
		t.Errorf("their document was touched: %d %v", files, err)
	}
}

// TestDocumentRoles: family roles do everything; a viewer reads but never
// sees documents attached to (or linked from) a credential and never
// writes; housesitters and vendors see no documents at all. The role is
// re-read on every call.
func TestDocumentRoles(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	d := newDocTools(t, pool, nil)
	bg := context.Background()

	plain := d.up.mustOK(t, ctx, map[string]any{"name": "manual.txt", "title": "Router manual", "text": "Router setup steps"}).Data
	secretID := seed(t, pool, `INSERT INTO secrets (household_id, entity_type, entity_id, encrypted_data, iv, name, secret_type)
		VALUES ($1, 'household', $1, 'enc', 'iv', 'Router admin', 'login') RETURNING id`, hh.HouseholdID)
	attached := seed(t, pool, `WITH b AS (INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id)
		INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id, extracted_text, ocr_status, title)
		SELECT $1, b.id, 'router-recovery.txt', 1, 'secret', $2, 'Router recovery codes', 'done', 'Router recovery codes' FROM b RETURNING id`,
		hh.HouseholdID, secretID)
	linked := d.up.mustOK(t, ctx, map[string]any{"name": "router-backup.txt", "title": "Router backup", "text": "Router backup config"}).Data
	if _, err := pool.Exec(bg, `INSERT INTO document_links (household_id, entity_type, entity_id, link_type, link_id)
		VALUES ($1, 'secret', $2, 'file', $3)`, hh.HouseholdID, secretID, linked.ID.String()); err != nil {
		t.Fatal(err)
	}

	count := func(ctx context.Context) int {
		return len(d.find.mustOK(t, ctx, map[string]any{"query": "router"}).Items)
	}

	for _, role := range []string{"family_manager", "family_member"} {
		mctx, _ := memberCtx(t, pool, hh, role)
		if n := count(mctx); n != 3 {
			t.Errorf("%s sees %d router documents, want 3", role, n)
		}
		got := d.get.mustOK(t, mctx, map[string]any{"document_id": attached.String()}).Data
		if got.AboutType != "credential" || got.AboutName == nil || *got.AboutName != "Router admin" {
			t.Errorf("%s: credential-attached document: %+v", role, got)
		}
		lg := d.get.mustOK(t, mctx, map[string]any{"document_id": linked.ID.String()}).Data
		if len(lg.Links) != 1 || lg.Links[0].AboutType != "credential" {
			t.Errorf("%s: credential link: %+v", role, lg.Links)
		}
		d.upd.mustOK(t, mctx, map[string]any{"document_id": plain.ID.String(), "tags": []any{role}})
	}

	vctx, vuid := memberCtx(t, pool, hh, "viewer")
	items := d.find.mustOK(t, vctx, map[string]any{"query": "router"}).Items
	if len(items) != 1 || items[0].ID != plain.ID {
		t.Errorf("viewer find: %+v", items)
	}
	if n := len(d.find.mustOK(t, vctx, map[string]any{}).Items); n != 1 {
		t.Errorf("viewer unfiltered find: %d", n)
	}
	d.get.mustOK(t, vctx, map[string]any{"document_id": plain.ID.String()})
	runErrCases(t, d.get, []errCase{
		{"viewer: attached to a credential", vctx, map[string]any{"document_id": attached.String()}, "", "No document"},
		{"viewer: linked from a credential", vctx, map[string]any{"document_id": linked.ID.String()}, "", "No document"},
	})
	runErrCases(t, d.upd, []errCase{{"viewer can't update", vctx, map[string]any{"document_id": plain.ID.String(), "title": "x"}, "", "The viewer role can't change documents"}})
	runErrCases(t, d.del, []errCase{{"viewer can't delete", vctx, map[string]any{"document_id": plain.ID.String()}, "", "The viewer role can't change documents"}})
	runErrCases(t, d.up, []errCase{{"viewer can't upload", vctx, map[string]any{"name": "a.txt", "text": "x"}, "", "The viewer role can't change documents"}})

	for _, role := range []string{"housesitter", "vendor"} {
		mctx, _ := memberCtx(t, pool, hh, role)
		runErrCases(t, d.find, []errCase{{role + " find", mctx, map[string]any{"query": "router"}, "", "can't see the family's documents"}})
		runErrCases(t, d.get, []errCase{{role + " get", mctx, map[string]any{"document_id": plain.ID.String()}, "", "can't see the family's documents"}})
	}

	// demotion takes effect on the next call: the claims still say owner
	if _, err := pool.Exec(bg, `UPDATE memberships SET role = 'housesitter' WHERE user_id = $1`, vuid); err != nil {
		t.Fatal(err)
	}
	runErrCases(t, d.find, []errCase{{"demoted viewer", vctx, map[string]any{}, "", "housesitter role can't see"}})
	if _, err := pool.Exec(bg, `UPDATE memberships SET role = 'viewer' WHERE user_id = $1`, hh.UserID); err != nil {
		t.Fatal(err)
	}
	runErrCases(t, d.del, []errCase{{"demoted owner", ctx, map[string]any{"document_id": plain.ID.String()}, "", "The viewer role can't change documents"}})
}
