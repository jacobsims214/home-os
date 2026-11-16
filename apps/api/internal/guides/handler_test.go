package guides_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/guides"
	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
	"home-os/api/internal/search/searchtest"
)

// Integration tests: they need TEST_DATABASE_URL (all migrations applied) and
// skip without it. The search test also needs TYPESENSE_TEST_ADDR and
// TYPESENSE_TEST_KEY (a Typesense 30.x that can load ts/all-MiniLM-L12-v2).

func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// router mounts the guides routes behind a stand-in for RequireAuth that takes
// the household, user and role from test headers.
func router(h *guides.Handler) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				claims := &auth.Claims{
					UserID:      req.Header.Get("X-Test-User"),
					HouseholdID: req.Header.Get("X-Test-Household"),
					Role:        req.Header.Get("X-Test-Role"),
				}
				next.ServeHTTP(w, req.WithContext(middleware.WithClaims(req.Context(), claims)))
			})
		})
		h.RegisterRoutes(r)
	})
	return r
}

type client struct {
	t    *testing.T
	h    http.Handler
	hid  string
	uid  string
	role string
}

func (c client) do(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(c.t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Household", c.hid)
	req.Header.Set("X-Test-User", c.uid)
	role := c.role
	if role == "" {
		role = auth.RoleFamilyMember
	}
	req.Header.Set("X-Test-Role", role)
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out struct {
		Data T `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out.Data
}

type guideJSON struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Summary    *string  `json:"summary"`
	Body       string   `json:"body"`
	Tags       []string `json:"tags"`
	PropertyID *string  `json:"property_id"`
	AssetID    *string  `json:"asset_id"`
	VehicleID  *string  `json:"vehicle_id"`
}

type revisionJSON struct {
	ID           string  `json:"id"`
	GuideID      string  `json:"guide_id"`
	Body         *string `json:"body"`
	BodyLength   int     `json:"body_length"`
	EditedBy     *string `json:"edited_by"`
	EditedByName *string `json:"edited_by_name"`
	ChangeNote   *string `json:"change_note"`
}

// family inserts a household with one member (role) and returns a client
// acting as that member.
func family(t *testing.T, pool *pgxpool.Pool, h http.Handler, role string) client {
	t.Helper()
	ctx := context.Background()
	var hid, uid string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Guides HH') RETURNING id::text`).Scan(&hid))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, 'Sam', 'x') RETURNING id::text`,
		uuid.NewString()+"@example.com").Scan(&uid))
	_, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hid, uid, role)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM households WHERE id = $1`, hid)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, uid)
	})
	return client{t: t, h: h, hid: hid, uid: uid, role: role}
}

func insertID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), sql, args...).Scan(&id))
	return id
}

func revisions(t *testing.T, c client, guideID string) []revisionJSON {
	t.Helper()
	rec := c.do(http.MethodGet, "/api/v1/guides/"+guideID+"/revisions", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decode[[]revisionJSON](t, rec)
}

func revisionCount(t *testing.T, pool *pgxpool.Pool, guideID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM guide_revisions WHERE guide_id = $1`, guideID).Scan(&n))
	return n
}

func TestGuideRevisions(t *testing.T) {
	pool := testDB(t)
	h := router(guides.NewHandler(guides.NewRepo(pool)))
	c := family(t, pool, h, auth.RoleOwner)

	rec := c.do(http.MethodPost, "/api/v1/guides", map[string]any{
		"title":   "  Reset the router ",
		"summary": "When the wifi drops",
		"body":    "1. Unplug the modem.",
		"tags":    []string{"Network", " wifi ", "network", ""},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	g := decode[guideJSON](t, rec)
	assert.Equal(t, "Reset the router", g.Title)
	assert.Equal(t, []string{"network", "wifi"}, g.Tags)
	require.NotNil(t, g.Summary)

	// Creating writes the first revision, by the acting user.
	revs := revisions(t, c, g.ID)
	require.Len(t, revs, 1)
	require.NotNil(t, revs[0].EditedBy)
	assert.Equal(t, c.uid, *revs[0].EditedBy)
	require.NotNil(t, revs[0].EditedByName)
	assert.Equal(t, "Sam", *revs[0].EditedByName)
	assert.Nil(t, revs[0].Body, "list omits bodies")
	assert.Nil(t, revs[0].ChangeNote, "create stores no change note")
	first := revs[0].ID

	// A body edit writes a revision, with its change note (trimmed).
	rec = c.do(http.MethodPut, "/api/v1/guides/"+g.ID, map[string]any{"body": "1. Unplug the modem.\n2. Wait 30s.", "change_note": "  added a wait "})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "Reset the router", decode[guideJSON](t, rec).Title, "omitted fields are unchanged")

	// Every save writes a revision, even one that leaves the body alone.
	rec = c.do(http.MethodPut, "/api/v1/guides/"+g.ID, map[string]any{"title": "Reset the internet router", "summary": ""})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	g = decode[guideJSON](t, rec)
	assert.Nil(t, g.Summary, `summary "" clears it`)

	revs = revisions(t, c, g.ID)
	require.Len(t, revs, 3)
	assert.Equal(t, first, revs[2].ID, "newest first")
	require.NotNil(t, revs[1].ChangeNote)
	assert.Equal(t, "added a wait", *revs[1].ChangeNote)
	assert.Nil(t, revs[0].ChangeNote, "a save without change_note stores none")

	// The newest revision equals the guide's body.
	rec = c.do(http.MethodGet, "/api/v1/guides/"+g.ID+"/revisions/"+revs[0].ID, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	newest := decode[revisionJSON](t, rec)
	require.NotNil(t, newest.Body)
	assert.Equal(t, g.Body, *newest.Body)

	// Restoring an old revision is a new save with that body.
	rec = c.do(http.MethodPost, "/api/v1/guides/"+g.ID+"/revisions/"+first+"/restore", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	g = decode[guideJSON](t, rec)
	assert.Equal(t, "1. Unplug the modem.", g.Body)
	assert.Equal(t, "Reset the internet router", g.Title, "restore changes only the body")
	revs = revisions(t, c, g.ID)
	require.Len(t, revs, 4)
	require.NotNil(t, revs[0].ChangeNote)
	assert.Equal(t, "Restored revision "+first, *revs[0].ChangeNote)
	rec = c.do(http.MethodGet, "/api/v1/guides/"+g.ID+"/revisions/"+revs[0].ID, nil)
	restored := decode[revisionJSON](t, rec)
	assert.Equal(t, "1. Unplug the modem.", *restored.Body)
	require.NotNil(t, restored.ChangeNote)
	assert.Equal(t, "Restored revision "+first, *restored.ChangeNote)

	// A failed save writes nothing.
	rec = c.do(http.MethodPut, "/api/v1/guides/"+g.ID, map[string]any{"title": "  ", "body": "lost"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, 4, revisionCount(t, pool, g.ID))
	rec = c.do(http.MethodPut, "/api/v1/guides/"+g.ID, map[string]any{"body": "lost", "change_note": strings.Repeat("é", 501)})
	assert.Equal(t, http.StatusBadRequest, rec.Code, "change_note over 500 characters")
	assert.Equal(t, 4, revisionCount(t, pool, g.ID))
	// 500 characters is fine; a blank note stores none.
	rec = c.do(http.MethodPut, "/api/v1/guides/"+g.ID, map[string]any{"change_note": strings.Repeat("é", 500)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = c.do(http.MethodPut, "/api/v1/guides/"+g.ID, map[string]any{"change_note": "   "})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	revs = revisions(t, c, g.ID)
	require.Len(t, revs, 6)
	assert.Nil(t, revs[0].ChangeNote)
	require.NotNil(t, revs[1].ChangeNote)
	assert.Equal(t, strings.Repeat("é", 500), *revs[1].ChangeNote)

	// Unknown revision.
	rec = c.do(http.MethodPost, "/api/v1/guides/"+g.ID+"/revisions/"+uuid.NewString()+"/restore", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Deleting removes the guide and its history.
	rec = c.do(http.MethodDelete, "/api/v1/guides/"+g.ID, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, http.StatusNotFound, c.do(http.MethodGet, "/api/v1/guides/"+g.ID, nil).Code)
	assert.Equal(t, http.StatusNotFound, c.do(http.MethodGet, "/api/v1/guides/"+g.ID+"/revisions", nil).Code)
	assert.Equal(t, 0, revisionCount(t, pool, g.ID))
}

// Concurrent saves each write exactly one revision and the newest revision
// still equals the body (the guide row is locked for the save).
func TestGuideConcurrentSaves(t *testing.T) {
	pool := testDB(t)
	h := router(guides.NewHandler(guides.NewRepo(pool)))
	c := family(t, pool, h, auth.RoleOwner)
	g := decode[guideJSON](t, c.do(http.MethodPost, "/api/v1/guides", map[string]any{"title": "Race", "body": "0"}))

	var wg sync.WaitGroup
	for i := 1; i <= 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := c.do(http.MethodPut, "/api/v1/guides/"+g.ID, map[string]any{"body": string(rune('a' + i))})
			assert.Equal(t, http.StatusOK, rec.Code)
		}(i)
	}
	wg.Wait()
	assert.Equal(t, 11, revisionCount(t, pool, g.ID))
	var body, newest string
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT g.body, (SELECT r.body FROM guide_revisions r WHERE r.guide_id = g.id
		                ORDER BY r.created_at DESC, r.id DESC LIMIT 1)
		FROM guides g WHERE g.id = $1`, g.ID).Scan(&body, &newest))
	assert.Equal(t, body, newest)
}

func TestGuideSubjectsAndFilters(t *testing.T) {
	pool := testDB(t)
	h := router(guides.NewHandler(guides.NewRepo(pool)))
	c := family(t, pool, h, auth.RoleFamilyMember)
	other := family(t, pool, h, auth.RoleOwner)

	prop := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Lake house') RETURNING id::text`, c.hid)
	asset := insertID(t, pool, `INSERT INTO assets (household_id, property_id, name) VALUES ($1, $2, 'HVAC') RETURNING id::text`, c.hid, prop)
	car := insertID(t, pool, `INSERT INTO vehicles (household_id, make, model) VALUES ($1, 'Subaru', 'Outback') RETURNING id::text`, c.hid)
	otherAsset := insertID(t, pool, `INSERT INTO assets (household_id, name) VALUES ($1, 'Their boiler') RETURNING id::text`, other.hid)

	// An asset subject derives the property.
	rec := c.do(http.MethodPost, "/api/v1/guides", map[string]any{"title": "Replace the HVAC filter", "asset_id": asset, "tags": []string{"hvac"}})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	hvac := decode[guideJSON](t, rec)
	require.NotNil(t, hvac.PropertyID)
	assert.Equal(t, prop, *hvac.PropertyID)

	carGuide := decode[guideJSON](t, c.do(http.MethodPost, "/api/v1/guides", map[string]any{"title": "Jump-start the car", "vehicle_id": car}))
	famGuide := decode[guideJSON](t, c.do(http.MethodPost, "/api/v1/guides", map[string]any{"title": "Wifi password reset", "tags": []string{"network"}}))

	ids := func(path string) []string {
		rec := c.do(http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out []string
		for _, g := range decode[[]guideJSON](t, rec) {
			out = append(out, g.ID)
		}
		return out
	}
	assert.Equal(t, []string{carGuide.ID, hvac.ID, famGuide.ID}, ids("/api/v1/guides"), "alphabetical")
	assert.Equal(t, []string{hvac.ID}, ids("/api/v1/guides?property_id="+prop), "property includes its assets' guides")
	assert.Equal(t, []string{hvac.ID}, ids("/api/v1/guides?asset_id="+asset))
	assert.Equal(t, []string{carGuide.ID}, ids("/api/v1/guides?vehicle_id="+car))
	assert.Equal(t, []string{famGuide.ID}, ids("/api/v1/guides?tag=NETWORK"))
	assert.Equal(t, []string{famGuide.ID}, ids("/api/v1/guides?subject=family"))
	assert.Equal(t, http.StatusBadRequest, c.do(http.MethodGet, "/api/v1/guides?asset_id=nope", nil).Code)

	// A vehicle excludes property and asset.
	rec = c.do(http.MethodPost, "/api/v1/guides", map[string]any{"title": "x", "vehicle_id": car, "property_id": prop})
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Another family's asset is "not found", on create and on update.
	rec = c.do(http.MethodPost, "/api/v1/guides", map[string]any{"title": "x", "asset_id": otherAsset})
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	rec = c.do(http.MethodPut, "/api/v1/guides/"+famGuide.ID, map[string]any{"asset_id": otherAsset})
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, 1, revisionCount(t, pool, famGuide.ID), "a refused save writes no revision")

	// Switching subject is one field; "" clears.
	rec = c.do(http.MethodPut, "/api/v1/guides/"+carGuide.ID, map[string]any{"property_id": prop})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	g := decode[guideJSON](t, rec)
	assert.Nil(t, g.VehicleID)
	assert.Equal(t, prop, *g.PropertyID)
	rec = c.do(http.MethodPut, "/api/v1/guides/"+carGuide.ID, map[string]any{"property_id": ""})
	assert.Nil(t, decode[guideJSON](t, rec).PropertyID)

	// The guide outlives its subject (SET NULL).
	_, err := pool.Exec(context.Background(), `DELETE FROM assets WHERE id = $1`, asset)
	require.NoError(t, err)
	rec = c.do(http.MethodGet, "/api/v1/guides/"+hvac.ID, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	g = decode[guideJSON](t, rec)
	assert.Nil(t, g.AssetID)
	assert.Equal(t, prop, *g.PropertyID)
}

func TestGuideCrossFamilyIsNotFound(t *testing.T) {
	pool := testDB(t)
	h := router(guides.NewHandler(guides.NewRepo(pool)))
	c := family(t, pool, h, auth.RoleOwner)
	other := family(t, pool, h, auth.RoleOwner)

	g := decode[guideJSON](t, c.do(http.MethodPost, "/api/v1/guides", map[string]any{"title": "Ours", "body": "secret steps"}))
	rev := revisions(t, c, g.ID)[0].ID

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/guides/" + g.ID},
		{http.MethodPut, "/api/v1/guides/" + g.ID},
		{http.MethodDelete, "/api/v1/guides/" + g.ID},
		{http.MethodGet, "/api/v1/guides/" + g.ID + "/revisions"},
		{http.MethodGet, "/api/v1/guides/" + g.ID + "/revisions/" + rev},
		{http.MethodPost, "/api/v1/guides/" + g.ID + "/revisions/" + rev + "/restore"},
		{http.MethodGet, "/api/v1/guides/not-a-uuid"},
	} {
		rec := other.do(tc.method, tc.path, map[string]any{"body": "hijack"})
		assert.Equal(t, http.StatusNotFound, rec.Code, "%s %s", tc.method, tc.path)
	}
	assert.Empty(t, decode[[]guideJSON](t, other.do(http.MethodGet, "/api/v1/guides", nil)))

	// A revision id from another guide doesn't resolve under this one.
	g2 := decode[guideJSON](t, c.do(http.MethodPost, "/api/v1/guides", map[string]any{"title": "Second"}))
	assert.Equal(t, http.StatusNotFound, c.do(http.MethodGet, "/api/v1/guides/"+g2.ID+"/revisions/"+rev, nil).Code)

	// Untouched.
	assert.Equal(t, "secret steps", decode[guideJSON](t, c.do(http.MethodGet, "/api/v1/guides/"+g.ID, nil)).Body)
	assert.Equal(t, 1, revisionCount(t, pool, g.ID))
}

func TestGuidePolicyHook(t *testing.T) {
	pool := testDB(t)
	viewersReadOnly := func(role string, a guides.Action) bool {
		return a == guides.ActionRead || role != auth.RoleViewer
	}
	h := router(guides.NewHandler(guides.NewRepo(pool)).WithPolicy(viewersReadOnly))
	owner := family(t, pool, h, auth.RoleOwner)
	viewer := owner
	viewer.role = auth.RoleViewer

	g := decode[guideJSON](t, owner.do(http.MethodPost, "/api/v1/guides", map[string]any{"title": "Shut off the water"}))
	assert.Equal(t, http.StatusOK, viewer.do(http.MethodGet, "/api/v1/guides/"+g.ID, nil).Code)
	assert.Equal(t, http.StatusOK, viewer.do(http.MethodGet, "/api/v1/guides/"+g.ID+"/revisions", nil).Code)
	assert.Equal(t, http.StatusForbidden, viewer.do(http.MethodPost, "/api/v1/guides", map[string]any{"title": "x"}).Code)
	assert.Equal(t, http.StatusForbidden, viewer.do(http.MethodPut, "/api/v1/guides/"+g.ID, map[string]any{"body": "x"}).Code)
	assert.Equal(t, http.StatusForbidden, viewer.do(http.MethodDelete, "/api/v1/guides/"+g.ID, nil).Code)
}

func TestGuideValidation(t *testing.T) {
	pool := testDB(t)
	h := router(guides.NewHandler(guides.NewRepo(pool)))
	c := family(t, pool, h, auth.RoleOwner)

	tooMany := make([]string, 31)
	for i := range tooMany {
		tooMany[i] = uuid.NewString()[:8]
	}
	for name, body := range map[string]any{
		"blank title":  map[string]any{"title": "  "},
		"long title":   map[string]any{"title": string(bytes.Repeat([]byte("a"), 256))},
		"long tag":     map[string]any{"title": "t", "tags": []string{string(bytes.Repeat([]byte("a"), 51))}},
		"too many":     map[string]any{"title": "t", "tags": tooMany},
		"bad asset id": map[string]any{"title": "t", "asset_id": "nope"},
		"not json":     "nope",
	} {
		rec := c.do(http.MethodPost, "/api/v1/guides", body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
	}
	assert.Equal(t, http.StatusNotFound, c.do(http.MethodPut, "/api/v1/guides/"+uuid.NewString(), map[string]any{"body": "x"}).Code)

	// No household context.
	c.hid = ""
	assert.Equal(t, http.StatusForbidden, c.do(http.MethodGet, "/api/v1/guides", nil).Code)
}

// fakeIndex records search calls.
type fakeIndex struct {
	mu      sync.Mutex
	docs    map[string]search.SearchDocument
	deleted []string
	fail    bool
}

func (f *fakeIndex) IndexDocument(_ context.Context, d search.SearchDocument) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("typesense down")
	}
	f.docs[d.ID] = d
	return nil
}

func (f *fakeIndex) DeleteDocument(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("typesense down")
	}
	delete(f.docs, id)
	f.deleted = append(f.deleted, id)
	return nil
}

func TestGuideIndexing(t *testing.T) {
	pool := testDB(t)
	idx := &fakeIndex{docs: map[string]search.SearchDocument{}}
	h := router(guides.NewHandler(guides.NewRepo(pool)).WithSearch(idx))
	c := family(t, pool, h, auth.RoleOwner)
	prop := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Home') RETURNING id::text`, c.hid)

	g := decode[guideJSON](t, c.do(http.MethodPost, "/api/v1/guides", map[string]any{
		"title": "Replace the HVAC filter", "summary": "Every 90 days", "body": "Use a 16x25x1 MERV 8.",
		"tags": []string{"HVAC"}, "property_id": prop,
	}))
	doc, ok := idx.docs["guide-"+g.ID]
	require.True(t, ok)
	assert.Equal(t, "guide", doc.EntityType)
	assert.Equal(t, g.ID, doc.EntityID)
	assert.Equal(t, c.hid, doc.HouseholdID)
	assert.Equal(t, "Replace the HVAC filter", doc.Title)
	assert.Equal(t, "Every 90 days\n\nUse a 16x25x1 MERV 8.", doc.Body)
	assert.Equal(t, []string{"hvac"}, doc.Tags)
	require.NotNil(t, doc.PropertyID)
	assert.Equal(t, prop, *doc.PropertyID)

	c.do(http.MethodPut, "/api/v1/guides/"+g.ID, map[string]any{"body": "Use a 20x25x1."})
	assert.Equal(t, "Every 90 days\n\nUse a 20x25x1.", idx.docs["guide-"+g.ID].Body, "edits re-index")

	rev := revisions(t, c, g.ID)[1].ID
	c.do(http.MethodPost, "/api/v1/guides/"+g.ID+"/revisions/"+rev+"/restore", nil)
	assert.Equal(t, "Every 90 days\n\nUse a 16x25x1 MERV 8.", idx.docs["guide-"+g.ID].Body, "restores re-index")

	// A search outage never fails the save.
	idx.fail = true
	rec := c.do(http.MethodPut, "/api/v1/guides/"+g.ID, map[string]any{"title": "HVAC filter"})
	assert.Equal(t, http.StatusOK, rec.Code)
	idx.fail = false

	assert.Equal(t, http.StatusNoContent, c.do(http.MethodDelete, "/api/v1/guides/"+g.ID, nil).Code)
	assert.Equal(t, []string{"guide-" + g.ID}, idx.deleted)
	assert.NotContains(t, idx.docs, "guide-"+g.ID)
}

// TestGuideHybridSearchIntegration indexes guides into a real Typesense
// (its own household_search_<8hex>) (local embedding model) and checks they come back from
// /search's hybrid query, including on meaning alone, and leave on delete.
func TestGuideHybridSearchIntegration(t *testing.T) {
	sc, _ := searchtest.New(t) // per-test collections; skips without TYPESENSE_TEST_ADDR/KEY
	pool := testDB(t)
	ctx := context.Background()
	require.NoError(t, sc.InitCollection(ctx))

	h := router(guides.NewHandler(guides.NewRepo(pool)).WithSearch(sc))
	c := family(t, pool, h, auth.RoleOwner)
	other := family(t, pool, h, auth.RoleOwner)

	hvac := decode[guideJSON](t, c.do(http.MethodPost, "/api/v1/guides", map[string]any{
		"title": "HVAC filter replacement", "body": "Turn the furnace off, slide out the old filter, insert a 16x25x1.",
		"tags": []string{"hvac"},
	}))
	router := decode[guideJSON](t, c.do(http.MethodPost, "/api/v1/guides", map[string]any{
		"title": "Reset the router", "summary": "What to do when the wifi stops working",
		"body": "Unplug the modem and the router, wait 30 seconds, plug the modem back in first.",
	}))

	find := func(hid, q string) []search.SearchResult {
		t.Helper()
		res, err := sc.Search(ctx, hid, q, search.SearchFilters{EntityTypes: []string{guides.EntityType}})
		require.NoError(t, err)
		return res
	}
	first := func(q string) string {
		t.Helper()
		res := find(c.hid, q)
		require.NotEmpty(t, res, "no hits for %q", q)
		return res[0].EntityID
	}

	assert.Equal(t, hvac.ID, first("air filter"), "semantic: air filter -> HVAC filter")
	assert.Equal(t, router.ID, first("internet is down"), "semantic: internet is down -> reset the router")
	assert.Equal(t, router.ID, first("router"), "keyword")
	assert.Empty(t, find(other.hid, "router"), "household scoped")

	// Edits are searchable at once.
	c.do(http.MethodPut, "/api/v1/guides/"+hvac.ID, map[string]any{"body": "Also check the condensate drain line."})
	assert.Equal(t, hvac.ID, first("condensate"))

	// Deleted guides leave the index.
	require.Equal(t, http.StatusNoContent, c.do(http.MethodDelete, "/api/v1/guides/"+router.ID, nil).Code)
	for _, r := range find(c.hid, "router") {
		assert.NotEqual(t, router.ID, r.EntityID)
	}
}
