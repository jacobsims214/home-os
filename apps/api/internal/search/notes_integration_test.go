package search_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/file"
	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
	"home-os/api/internal/search/searchtest"
)

// TestNoteHitsFollowParentIntegration (#4798): the index stores a note's
// text but not what it is attached to, so /api/v1/search re-checks every
// note hit against Postgres. A viewer searching the exact text of a note on
// a credential, or on a file linked from a credential, gets no hit; the
// owner finds both; a note on a pet stays visible to the viewer; a note
// deleted while the index kept it is gone for everyone. Needs
// TEST_DATABASE_URL and TYPESENSE_TEST_ADDR/TYPESENSE_TEST_KEY (CI runs it
// in the "must PASS" Typesense step).
func TestNoteHitsFollowParentIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	client, _ := searchtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	require.NoError(t, client.InitCollection(ctx))

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, sql, args...)
		require.NoError(t, err, sql)
	}

	var hh uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO households (name) VALUES ('Note search test') RETURNING id`).Scan(&hh))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hh)
		_, _ = pool.Exec(context.Background(), `DELETE FROM file_search_deletions WHERE household_id = $1`, hh)
	})

	var sec uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type)
		 VALUES ($1, '\x00', '\x00', 'Bank login', 'login') RETURNING id`, hh).Scan(&sec))
	files := file.NewRepo(pool)
	title := "Passport scan"
	linked, err := files.CreateFile(ctx, hh, &file.File{Name: "passport.jpg", ContentType: "image/jpeg", Title: &title}, []byte("x"))
	require.NoError(t, err)
	exec(`INSERT INTO document_links (household_id, entity_type, entity_id, link_type, link_id, title)
	      VALUES ($1, 'secret', $2, 'file', $3, 'Passport scan')`, hh, sec, linked.ID.String())
	pet := uuid.New()
	exec(`INSERT INTO pets (id, household_id, name) VALUES ($1, $2, 'Rex')`, pet, hh)

	// Each note is in Postgres and indexed the way note.Handler indexes it.
	note := func(kind string, parent uuid.UUID, title, body string) string {
		t.Helper()
		var id uuid.UUID
		require.NoError(t, pool.QueryRow(ctx,
			`INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			hh, kind, parent, title, body).Scan(&id))
		require.NoError(t, client.IndexDocument(ctx, search.SearchDocument{
			ID: "note-" + id.String(), HouseholdID: hh.String(), EntityType: "note", EntityID: id.String(),
			Title: title, Body: body, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
		}))
		return id.String()
	}
	onSecret := note("secret", sec, "Security questions", "Mother maiden name is Quokkastein")
	onFile := note("file", linked.ID, "Renewal", "Passport number Zebrafinch 4471")
	onPet := note("pet", pet, "Vet", "Rex allergic to Wombatine")
	gone := note("pet", pet, "Old", "Pangolinium dosage chart")
	exec(`DELETE FROM notes WHERE id = $1`, gone) // the index still has it

	h := search.NewHandler(client).WithFileChecker(files).WithNoteChecker(search.NewNoteRepo(pool, files))
	noteHits := func(role, q string) map[string]bool {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/search?q="+url.QueryEscape(q), nil)
		req = req.WithContext(middleware.WithClaims(req.Context(),
			&auth.Claims{UserID: uuid.NewString(), HouseholdID: hh.String(), Role: role}))
		rec := httptest.NewRecorder()
		h.Search(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out struct {
			Data struct {
				Results []search.SearchResult `json:"results"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		got := map[string]bool{}
		for _, r := range out.Data.Results {
			if r.EntityType == "note" {
				got[r.EntityID] = true
			}
		}
		return got
	}

	for _, tc := range []struct {
		q, id string
	}{
		{"Mother maiden name is Quokkastein", onSecret},
		{"Passport number Zebrafinch 4471", onFile},
	} {
		assert.True(t, noteHits(auth.RoleOwner, tc.q)[tc.id], "owner finds %q", tc.q)
		assert.True(t, noteHits(auth.RoleFamilyMember, tc.q)[tc.id], "family member finds %q", tc.q)
		assert.False(t, noteHits(auth.RoleViewer, tc.q)[tc.id], "viewer must not find %q", tc.q)
	}
	assert.True(t, noteHits(auth.RoleViewer, "Rex allergic to Wombatine")[onPet], "viewer finds a note on a pet")
	assert.False(t, noteHits(auth.RoleOwner, "Pangolinium dosage chart")[gone], "a deleted note is not returned")
}
