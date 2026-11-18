package file_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/activity"
	"home-os/api/internal/auth"
	"home-os/api/internal/file"
	"home-os/api/internal/link"
	"home-os/api/internal/middleware"
	"home-os/api/internal/note"
)

// #4775: the credential rule (file.SecretFileSQL / VisibleFileIDs) also
// covers everything that shows a file outside /api/v1/files: notes on the
// file, links to and of it, and the activity feed. A viewer sees none of
// it for a file linked from a credential; family roles see all of it.
// Needs TEST_DATABASE_URL.

type secretLinkFixture struct {
	pool                *pgxpool.Pool
	hh, petID           uuid.UUID
	linked, plain, note uuid.UUID
	viewer, owner       *auth.Claims
}

func newSecretLinkFixture(t *testing.T) *secretLinkFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	f := &secretLinkFixture{pool: pool}
	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, sql, args...)
		require.NoError(t, err, sql)
	}

	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO households (name) VALUES ('Secret link test') RETURNING id`).Scan(&f.hh))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, f.hh)
		_, _ = pool.Exec(context.Background(), `DELETE FROM file_search_deletions WHERE household_id = $1`, f.hh)
	})
	member := func(role string) *auth.Claims {
		var uid uuid.UUID
		require.NoError(t, pool.QueryRow(ctx,
			`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, $2, 'x', $3) RETURNING id`,
			"secretlink-"+uuid.NewString()[:8]+"@example.com", role, "sub-"+uuid.NewString()).Scan(&uid))
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, uid) })
		exec(`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, f.hh, uid, role)
		return &auth.Claims{UserID: uid.String(), HouseholdID: f.hh.String(), Role: role}
	}
	f.viewer, f.owner = member(auth.RoleViewer), member(auth.RoleOwner)

	var sec uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type)
		 VALUES ($1, '\x00', '\x00', 'Passport', 'document') RETURNING id`, f.hh).Scan(&sec))
	repo := file.NewRepo(pool)
	mk := func(name, title string) uuid.UUID {
		fl, err := repo.CreateFile(ctx, f.hh, &file.File{Name: name, ContentType: "image/jpeg", Title: &title}, []byte("x"))
		require.NoError(t, err)
		return fl.ID
	}
	f.linked, f.plain = mk("passport.jpg", "Passport scan"), mk("card.jpg", "Library card")
	exec(`INSERT INTO document_links (household_id, entity_type, entity_id, link_type, link_id, title)
	      VALUES ($1, 'secret', $2, 'file', $3, 'Passport scan')`, f.hh, sec, f.linked.String())

	f.petID = uuid.New()
	exec(`INSERT INTO pets (id, household_id, name) VALUES ($1, $2, 'Rex')`, f.petID, f.hh)
	for _, id := range []uuid.UUID{f.linked, f.plain} {
		exec(`INSERT INTO document_links (household_id, entity_type, entity_id, link_type, link_id, title)
		      VALUES ($1, 'pet', $2, 'file', $3, 'doc')`, f.hh, f.petID, id.String())
	}
	n, err := note.NewRepo(pool).Create(ctx, &note.Note{HouseholdID: f.hh, EntityType: "file", EntityID: f.linked, Body: "number 123"})
	require.NoError(t, err)
	f.note = n.ID
	return f
}

func (f *secretLinkFixture) router() http.Handler {
	files := file.NewRepo(f.pool)
	nh := note.NewHandler(note.NewRepo(f.pool)).WithFileChecker(files)
	lh := link.NewHandler(link.NewRepo(f.pool)).WithFileChecker(files)
	r := chi.NewRouter()
	r.Get("/api/v1/notes", nh.List)
	r.Get("/api/v1/notes/{id}", nh.Get)
	r.Get("/api/v1/links", lh.List)
	activity.NewHandler(activity.NewRepo(f.pool)).
		WithReadPolicy(func(role string) (bool, []string) { return true, auth.HiddenEntityKinds(role) }).
		RegisterRoutes(r)
	return r
}

func (f *secretLinkFixture) get(t *testing.T, h http.Handler, c *auth.Claims, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(middleware.WithClaims(req.Context(), c))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// ids collects every "id", "entity_id", "parent_id" and "link_id" in a
// JSON response, wherever it is.
func ids(t *testing.T, b []byte) map[string]bool {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal(b, &v), string(b))
	out := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if s, ok := e.(string); ok && (k == "id" || k == "entity_id" || k == "parent_id" || k == "link_id") {
					out[s] = true
				}
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

func TestCredentialLinkedFileHiddenEverywhere(t *testing.T) {
	f := newSecretLinkFixture(t)
	h := f.router()
	linked, plain, noteID := f.linked.String(), f.plain.String(), f.note.String()

	t.Run("notes", func(t *testing.T) {
		code, body := f.get(t, h, f.viewer, "/api/v1/notes")
		require.Equal(t, http.StatusOK, code, string(body))
		assert.False(t, ids(t, body)[noteID], "viewer: household notes")
		code, body = f.get(t, h, f.viewer, "/api/v1/notes?entity_type=file&entity_id="+linked)
		require.Equal(t, http.StatusOK, code)
		assert.False(t, ids(t, body)[noteID], "viewer: notes of the file")
		code, hidden := f.get(t, h, f.viewer, "/api/v1/notes/"+noteID)
		mcode, missing := f.get(t, h, f.viewer, "/api/v1/notes/"+uuid.NewString())
		assert.Equal(t, http.StatusNotFound, code)
		assert.Equal(t, mcode, code)
		assert.Equal(t, string(missing), string(hidden), "hidden and missing answer alike")

		_, body = f.get(t, h, f.owner, "/api/v1/notes")
		assert.True(t, ids(t, body)[noteID], "owner: household notes")
		code, _ = f.get(t, h, f.owner, "/api/v1/notes/"+noteID)
		assert.Equal(t, http.StatusOK, code)
	})

	t.Run("links", func(t *testing.T) {
		code, body := f.get(t, h, f.viewer, "/api/v1/links?entity_type=pet&entity_id="+f.petID.String())
		require.Equal(t, http.StatusOK, code, string(body))
		got := ids(t, body)
		assert.False(t, got[linked], "viewer: link to the hidden file")
		assert.True(t, got[plain], "viewer: link to a plain file")
		code, _ = f.get(t, h, f.viewer, "/api/v1/links?entity_type=file&entity_id="+linked)
		assert.Equal(t, http.StatusNotFound, code, "viewer: links of the hidden file")

		_, body = f.get(t, h, f.owner, "/api/v1/links?entity_type=pet&entity_id="+f.petID.String())
		got = ids(t, body)
		assert.True(t, got[linked] && got[plain], "owner sees both links")
		code, _ = f.get(t, h, f.owner, "/api/v1/links?entity_type=file&entity_id="+linked)
		assert.Equal(t, http.StatusOK, code)
	})

	t.Run("activity", func(t *testing.T) {
		code, body := f.get(t, h, f.viewer, "/api/v1/activity?limit=200")
		require.Equal(t, http.StatusOK, code, string(body))
		got := ids(t, body)
		assert.False(t, got[linked], "viewer: the hidden file's entries")
		assert.False(t, got[noteID], "viewer: a note on the hidden file")
		assert.True(t, got[plain], "viewer: a plain file's entries")
		assert.NotContains(t, string(body), "Passport scan", "viewer: no line names the hidden file")

		_, body = f.get(t, h, f.owner, "/api/v1/activity?limit=200")
		got = ids(t, body)
		assert.True(t, got[linked] && got[noteID] && got[plain], "owner sees every entry")
	})
}
