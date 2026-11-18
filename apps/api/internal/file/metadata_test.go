package file

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
)

// These tests cover #4478: document metadata (migration 048) round-trips
// through upload, GET, list and PATCH; viewers never see files attached to
// credentials; and the metadata is found by hybrid search. They need
// TEST_DATABASE_URL; TestDocumentHybridSearch also needs Typesense 30.2 at
// TYPESENSE_TEST_ADDR (key TYPESENSE_TEST_KEY), on per-test collections.

func metaRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Post("/api/v1/files/upload", h.UploadFile)
	r.Get("/api/v1/files", h.ListFiles)
	r.Get("/api/v1/files/{id}", h.GetFile)
	r.Get("/api/v1/files/{id}/content", h.GetFileContent)
	r.Patch("/api/v1/files/{id}", h.UpdateFile)
	r.Delete("/api/v1/files/{id}", h.DeleteFile)
	return r
}

func as(req *http.Request, hh uuid.UUID, role string) *http.Request {
	return req.WithContext(middleware.WithClaims(req.Context(),
		&auth.Claims{UserID: uuid.NewString(), HouseholdID: hh.String(), Role: role}))
}

// uploadForm posts a file with extra form fields and returns status + body.
func uploadForm(t *testing.T, h http.Handler, hh uuid.UUID, name, text string, fields map[string]string) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		require.NoError(t, mw.WriteField(k, v))
	}
	fw, err := mw.CreateFormFile("file", name)
	require.NoError(t, err)
	_, _ = io.WriteString(fw, text)
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, as(req, hh, auth.RoleFamilyMember))
	return rec.Code, rec.Body.Bytes()
}

func call(t *testing.T, h http.Handler, hh uuid.UUID, role, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(b)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, as(httptest.NewRequest(method, path, rd), hh, role))
	return rec.Code, rec.Body.Bytes()
}

func decodeFile(t *testing.T, b []byte) File {
	t.Helper()
	var out struct {
		Data File `json:"data"`
	}
	require.NoError(t, json.Unmarshal(b, &out), string(b))
	return out.Data
}

func decodeFiles(t *testing.T, b []byte) []File {
	t.Helper()
	var out struct {
		Data []File `json:"data"`
	}
	require.NoError(t, json.Unmarshal(b, &out), string(b))
	return out.Data
}

func person(t *testing.T, pool *pgxpool.Pool, hh uuid.UUID, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO people (household_id, display_name) VALUES ($1, $2) RETURNING id`, hh, name).Scan(&id))
	return id
}

func secretRow(t *testing.T, pool *pgxpool.Pool, hh uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO secrets (household_id, encrypted_data, iv, name, secret_type)
		 VALUES ($1, '\x00', '\x00', 'Bank login', 'login') RETURNING id`, hh).Scan(&id))
	return id
}

func strp(s string) *string { return &s }

func TestDocumentMetadataRoundTrip(t *testing.T) {
	pool := testDB(t)
	hh, other := household(t, pool), household(t, pool)
	jo := person(t, pool, hh, "Jo")
	stranger := person(t, pool, other, "Stranger")
	h := metaRouter(NewHandler(NewRepo(pool)))

	// Upload with every metadata field.
	code, body := uploadForm(t, h, hh, "scan001.pdf", "policy text", map[string]string{
		"title": "  2026 homeowners policy ", "category": "insurance", "document_date": "2026-01-15",
		"expires_on": "2027-01-15", "description": "Covers water damage", "person_id": jo.String(),
	})
	require.Equal(t, http.StatusCreated, code, string(body))
	f := decodeFile(t, body)
	assert.Equal(t, strp("2026 homeowners policy"), f.Title, "title is trimmed")
	assert.Equal(t, "insurance", f.Category)
	assert.Equal(t, strp("2026-01-15"), f.DocumentDate)
	assert.Equal(t, strp("2027-01-15"), f.ExpiresOn)
	assert.Equal(t, strp("Covers water damage"), f.Description)
	require.NotNil(t, f.PersonID)
	assert.Equal(t, jo, *f.PersonID)

	// GET returns the same metadata; the JSON keys are present.
	code, body = call(t, h, hh, auth.RoleFamilyMember, http.MethodGet, "/api/v1/files/"+f.ID.String(), nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, f, decodeFile(t, body))
	for _, k := range []string{`"title"`, `"category"`, `"document_date"`, `"expires_on"`, `"description"`, `"person_id"`} {
		assert.Contains(t, string(body), k)
	}

	// Upload without metadata: category defaults to other, the rest null.
	code, body = uploadForm(t, h, hh, "plain.txt", "x", nil)
	require.Equal(t, http.StatusCreated, code, string(body))
	plain := decodeFile(t, body)
	assert.Equal(t, "other", plain.Category)
	assert.Nil(t, plain.Title)
	assert.Nil(t, plain.PersonID)

	// PATCH: change title and category, clear description and person with
	// null, leave the dates alone.
	code, body = call(t, h, hh, auth.RoleFamilyMember, http.MethodPatch, "/api/v1/files/"+f.ID.String(),
		map[string]any{"title": "Homeowners", "category": "home", "description": nil, "person_id": nil})
	require.Equal(t, http.StatusOK, code, string(body))
	p := decodeFile(t, body)
	assert.Equal(t, strp("Homeowners"), p.Title)
	assert.Equal(t, "home", p.Category)
	assert.Nil(t, p.Description)
	assert.Nil(t, p.PersonID)
	assert.Equal(t, strp("2026-01-15"), p.DocumentDate, "absent field unchanged")
	assert.Equal(t, "scan001.pdf", p.Name)

	// A blank title clears it (no blank-title spelling).
	code, body = call(t, h, hh, auth.RoleFamilyMember, http.MethodPatch, "/api/v1/files/"+f.ID.String(),
		map[string]any{"title": "   "})
	require.Equal(t, http.StatusOK, code, string(body))
	assert.Nil(t, decodeFile(t, body).Title)

	// The old name/tags PATCH still works.
	code, body = call(t, h, hh, auth.RoleFamilyMember, http.MethodPatch, "/api/v1/files/"+f.ID.String(),
		map[string]any{"tags": []string{"house"}})
	require.Equal(t, http.StatusOK, code, string(body))
	assert.Equal(t, []string{"house"}, decodeFile(t, body).Tags)
	assert.Equal(t, "home", decodeFile(t, body).Category)

	// Validation.
	bad := []struct {
		name string
		body map[string]any
		want int
	}{
		{"unknown category", map[string]any{"category": "recipes"}, http.StatusBadRequest},
		{"null category", map[string]any{"category": nil}, http.StatusBadRequest},
		{"bad date", map[string]any{"document_date": "15/01/2026"}, http.StatusBadRequest},
		{"expires before date in body", map[string]any{"document_date": "2026-02-01", "expires_on": "2026-01-01"}, http.StatusBadRequest},
		{"expires before stored date (048 CHECK)", map[string]any{"expires_on": "2025-12-31"}, http.StatusBadRequest},
		{"another family's person", map[string]any{"person_id": stranger.String()}, http.StatusNotFound},
		{"empty patch", map[string]any{}, http.StatusBadRequest},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			code, body := call(t, h, hh, auth.RoleFamilyMember, http.MethodPatch, "/api/v1/files/"+f.ID.String(), tc.body)
			assert.Equal(t, tc.want, code, string(body))
		})
	}
	code, _ = uploadForm(t, h, hh, "a.txt", "x", map[string]string{"category": "recipes"})
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = uploadForm(t, h, hh, "a.txt", "x", map[string]string{"person_id": stranger.String()})
	assert.Equal(t, http.StatusNotFound, code, "another family's person")

	// Another household cannot PATCH it.
	code, _ = call(t, h, other, auth.RoleOwner, http.MethodPatch, "/api/v1/files/"+f.ID.String(), map[string]any{"title": "x"})
	assert.Equal(t, http.StatusNotFound, code)

	// List filters.
	code, body = uploadForm(t, h, hh, "card.pdf", "grades", map[string]string{"category": "school", "person_id": jo.String()})
	require.Equal(t, http.StatusCreated, code, string(body))
	card := decodeFile(t, body)
	code, body = call(t, h, hh, auth.RoleFamilyMember, http.MethodGet, "/api/v1/files?category=school", nil)
	require.Equal(t, http.StatusOK, code)
	if got := decodeFiles(t, body); assert.Len(t, got, 1) {
		assert.Equal(t, card.ID, got[0].ID)
	}
	code, body = call(t, h, hh, auth.RoleFamilyMember, http.MethodGet, "/api/v1/files?person_id="+jo.String(), nil)
	require.Equal(t, http.StatusOK, code)
	assert.Len(t, decodeFiles(t, body), 1)
	code, _ = call(t, h, hh, auth.RoleFamilyMember, http.MethodGet, "/api/v1/files?category=nope", nil)
	assert.Equal(t, http.StatusBadRequest, code)
	code, body = call(t, h, hh, auth.RoleFamilyMember, http.MethodGet, "/api/v1/files", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Len(t, decodeFiles(t, body), 3)
}

// A viewer never sees a file attached to a credential: not in lists, 404 on
// get and content. Family roles still do.
func TestSecretFilesHiddenFromViewer(t *testing.T) {
	pool := testDB(t)
	hh := household(t, pool)
	sec := secretRow(t, pool, hh)
	h := metaRouter(NewHandler(NewRepo(pool)))

	code, body := uploadForm(t, h, hh, "recovery-codes.txt", "codes", map[string]string{
		"entity_type": "secret", "entity_id": sec.String()})
	require.Equal(t, http.StatusCreated, code, string(body))
	sf := decodeFile(t, body)
	code, body = uploadForm(t, h, hh, "manual.pdf", "manual", map[string]string{"category": "manual"})
	require.Equal(t, http.StatusCreated, code, string(body))
	mf := decodeFile(t, body)

	ids := func(fs []File) []uuid.UUID {
		out := []uuid.UUID{}
		for _, f := range fs {
			out = append(out, f.ID)
		}
		return out
	}
	_, body = call(t, h, hh, auth.RoleViewer, http.MethodGet, "/api/v1/files", nil)
	assert.Equal(t, []uuid.UUID{mf.ID}, ids(decodeFiles(t, body)))
	_, body = call(t, h, hh, auth.RoleViewer, http.MethodGet, "/api/v1/files?entity_type=secret&entity_id="+sec.String(), nil)
	assert.Empty(t, decodeFiles(t, body))
	for _, path := range []string{"/api/v1/files/" + sf.ID.String(), "/api/v1/files/" + sf.ID.String() + "/content"} {
		code, _ = call(t, h, hh, auth.RoleViewer, http.MethodGet, path, nil)
		assert.Equal(t, http.StatusNotFound, code, path)
	}
	code, _ = call(t, h, hh, auth.RoleViewer, http.MethodGet, "/api/v1/files/"+mf.ID.String(), nil)
	assert.Equal(t, http.StatusOK, code)

	_, body = call(t, h, hh, auth.RoleFamilyMember, http.MethodGet, "/api/v1/files", nil)
	assert.ElementsMatch(t, []uuid.UUID{sf.ID, mf.ID}, ids(decodeFiles(t, body)))
	code, _ = call(t, h, hh, auth.RoleOwner, http.MethodGet, "/api/v1/files/"+sf.ID.String(), nil)
	assert.Equal(t, http.StatusOK, code)
}

// linkFromSecret links f from credential sec (a document_links row of the
// secret, the way the MCP's document_update add_links stores it).
func linkFromSecret(t *testing.T, pool *pgxpool.Pool, hh, sec, f uuid.UUID) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO document_links (household_id, entity_type, entity_id, link_type, link_id, title)
		 VALUES ($1, 'secret', $2, 'file', $3, 'Passport scan')`, hh, sec, f.String())
	require.NoError(t, err)
}

// #4775: a file linked from a credential (not attached to it) is hidden from
// a viewer exactly like an attached one and like a file that does not
// exist: no list row, the same 404 on get, content and PATCH, and
// VisibleFileIDs (search) leaves it out. Family roles still see it.
func TestCredentialLinkedFilesHiddenFromViewer(t *testing.T) {
	pool := testDB(t)
	hh := household(t, pool)
	sec := secretRow(t, pool, hh)
	repo := NewRepo(pool)
	h := metaRouter(NewHandler(repo))

	up := func(name, title string) File {
		code, body := uploadForm(t, h, hh, name, "scan", map[string]string{"title": title, "category": "legal"})
		require.Equal(t, http.StatusCreated, code, string(body))
		return decodeFile(t, body)
	}
	linked := up("passport.jpg", "Passport scan")
	plain := up("library-card.jpg", "Library card")
	linkFromSecret(t, pool, hh, sec, linked.ID)
	// A link of another kind, or from another record, does not hide a file.
	pet := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO pets (id, household_id, name) VALUES ($1, $2, 'Rex')`, pet, hh)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO document_links (household_id, entity_type, entity_id, link_type, link_id, title)
		 VALUES ($1, 'pet', $2, 'file', $3, 'Library card')`, hh, pet, plain.ID.String())
	require.NoError(t, err)

	ids := func(b []byte) []uuid.UUID {
		out := []uuid.UUID{}
		for _, f := range decodeFiles(t, b) {
			out = append(out, f.ID)
		}
		return out
	}
	_, body := call(t, h, hh, auth.RoleViewer, http.MethodGet, "/api/v1/files", nil)
	assert.Equal(t, []uuid.UUID{plain.ID}, ids(body), "viewer list")
	_, body = call(t, h, hh, auth.RoleViewer, http.MethodGet, "/api/v1/files?category=legal", nil)
	assert.Equal(t, []uuid.UUID{plain.ID}, ids(body), "viewer list with a filter")

	missing := uuid.New()
	for _, tc := range []struct{ method, suffix string }{
		{http.MethodGet, ""}, {http.MethodGet, "/content"}, {http.MethodPatch, ""},
	} {
		var b any
		if tc.method == http.MethodPatch {
			b = map[string]any{"title": "x"}
		}
		code, hidden := call(t, h, hh, auth.RoleViewer, tc.method, "/api/v1/files/"+linked.ID.String()+tc.suffix, b)
		mcode, absent := call(t, h, hh, auth.RoleViewer, tc.method, "/api/v1/files/"+missing.String()+tc.suffix, b)
		assert.Equal(t, http.StatusNotFound, code, tc.method+tc.suffix)
		assert.Equal(t, mcode, code, "hidden and missing answer alike: "+tc.method+tc.suffix)
		assert.Equal(t, string(absent), string(hidden), "hidden and missing answer alike: "+tc.method+tc.suffix)
	}
	code, _ := call(t, h, hh, auth.RoleViewer, http.MethodGet, "/api/v1/files/"+plain.ID.String(), nil)
	assert.Equal(t, http.StatusOK, code)

	ok, err := repo.VisibleFileIDs(context.Background(), hh.String(), []string{linked.ID.String(), plain.ID.String()}, false)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{plain.ID.String(): true}, ok, "search check for a viewer")
	ok, err = repo.VisibleFileIDs(context.Background(), hh.String(), []string{linked.ID.String(), plain.ID.String()}, true)
	require.NoError(t, err)
	assert.Len(t, ok, 2, "search check for family")

	for _, role := range []string{auth.RoleOwner, auth.RoleFamilyManager, auth.RoleFamilyMember} {
		_, body = call(t, h, hh, role, http.MethodGet, "/api/v1/files", nil)
		assert.ElementsMatch(t, []uuid.UUID{linked.ID, plain.ID}, ids(body), role)
		for _, suffix := range []string{"", "/content"} {
			code, _ = call(t, h, hh, role, http.MethodGet, "/api/v1/files/"+linked.ID.String()+suffix, nil)
			assert.Equal(t, http.StatusOK, code, role+suffix)
		}
	}
	code, body = call(t, h, hh, auth.RoleOwner, http.MethodPatch, "/api/v1/files/"+linked.ID.String(),
		map[string]any{"description": "Expires 2031"})
	assert.Equal(t, http.StatusOK, code, string(body))
}

// TestSecretRuleMatchesMCP keeps the API's credential rule identical to the
// MCP's (apps/mcp/internal/file/access.go secretFileWhere, #4477): the MCP
// is a separate module whose internal package can't be imported, so the
// test reads its source, renders its SQL template and compares.
func TestSecretRuleMatchesMCP(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "mcp", "internal", "file", "access.go"))
	require.NoError(t, err)
	m := regexp.MustCompile("(?s)func secretFileWhere\\(n int\\) string \\{\\s*return fmt\\.Sprintf\\(`([^`]*)`, n\\)").FindSubmatch(src)
	require.NotNil(t, m, "secretFileWhere not found in the MCP's access.go")
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	for _, n := range []int{1, 3, 13} {
		assert.Equal(t, norm(fmt.Sprintf(string(m[1]), n)), norm(secretFileWhere(n)), "n=%d", n)
	}
	// SecretFileSQL is the same rule with the bypass off.
	assert.Equal(t, norm(`EXISTS (SELECT 1 FROM files f WHERE f.id = x.id AND NOT `+
		strings.Replace(fmt.Sprintf(string(m[1]), 0), "$0::boolean", "false", 1)+`)`),
		norm(SecretFileSQL("x.id")))
}

func TestSearchDocumentBuilder(t *testing.T) {
	hh, id := uuid.New(), uuid.New()
	f := &File{ID: id, HouseholdID: hh, Name: "scan.pdf", Category: "medical", Title: strp("Rex vaccination record"),
		Description: strp("Rabies booster"), DocumentDate: strp("2026-03-01"), Tags: []string{"pet"},
		EntityType: "pet", CreatedAt: time.Unix(10, 0), UpdatedAt: time.Unix(20, 0)}
	d := SearchDocument(f, "Jo")
	assert.Equal(t, "document-"+id.String(), d.ID)
	assert.Equal(t, hh.String(), d.HouseholdID)
	assert.Equal(t, "document", d.EntityType)
	assert.Equal(t, id.String(), d.EntityID)
	assert.Equal(t, "Rex vaccination record", d.Title)
	assert.Equal(t, "Rabies booster. Category: medical. For: Jo. Dated 2026-03-01. File: scan.pdf", d.Body)
	assert.Equal(t, []string{"pet", "medical"}, d.Tags)
	assert.Equal(t, []string{"pet"}, f.Tags, "builder must not alias the file's tags")

	f.EntityType = EntityTypeSecret
	cur, all := SearchDocIDs(f)
	assert.Equal(t, "secret-document-"+id.String(), cur)
	assert.ElementsMatch(t, []string{"document-" + id.String(), "secret-document-" + id.String()}, all)
	assert.Equal(t, "scan.pdf", (&File{Name: "scan.pdf"}).DisplayTitle())
}

// tsReady creates the test's own household_search_<8hex> with the
// auto-embedding field (the API's InitCollection) and returns a client on it,
// or skips.
func tsReady(t *testing.T) (*search.Client, ts) {
	t.Helper()
	client, tsc := newTS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	require.NoError(t, client.InitCollection(ctx))
	return client, tsc
}

type searchOut struct {
	Data struct {
		Results []struct {
			EntityType string  `json:"entity_type"`
			EntityID   string  `json:"entity_id"`
			FileID     string  `json:"file_id"`
			Title      string  `json:"title"`
			Score      float64 `json:"score"`
		} `json:"results"`
	} `json:"data"`
}

func doSearch(t *testing.T, sh *search.Handler, hh uuid.UUID, role, q string) searchOut {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?q="+strings.ReplaceAll(q, " ", "+"), nil)
	sh.Search(rec, as(req, hh, role))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out searchOut
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func fileHits(o searchOut) []string {
	out := []string{}
	for _, r := range o.Data.Results {
		if r.FileID != "" {
			out = append(out, r.FileID)
		}
	}
	return out
}

// Metadata is found by hybrid search, including pure semantic matches;
// results are scoped to the caller's household; a viewer never gets a
// credential's file; PATCH re-indexes; delete removes the document; and a
// stale document whose file is gone is filtered out against Postgres.
func TestDocumentHybridSearch(t *testing.T) {
	pool := testDB(t)
	client, _ := tsReady(t)
	hh, other := household(t, pool), household(t, pool)
	repo := NewRepo(pool)
	h := metaRouter(NewHandler(repo).WithSearch(client))
	sh := search.NewHandler(client).WithFileChecker(repo)

	up := func(hh uuid.UUID, name string, fields map[string]string) File {
		code, body := uploadForm(t, h, hh, name, "binary", fields)
		require.Equal(t, http.StatusCreated, code, string(body))
		return decodeFile(t, body)
	}
	vacc := up(hh, "IMG_2231.jpg", map[string]string{"title": "Dog vaccination record", "category": "medical"})
	roof := up(hh, "scan7.pdf", map[string]string{"title": "Roof repair invoice", "category": "receipt",
		"description": "Shingles replaced after the storm"})
	otherVacc := up(other, "x.jpg", map[string]string{"title": "Dog vaccination record", "category": "medical"})

	// Pure semantic: no keyword in common with "Dog vaccination record".
	res := doSearch(t, sh, hh, auth.RoleFamilyMember, "pet immunization")
	hits := fileHits(res)
	require.NotEmpty(t, hits, "semantic match expected")
	assert.Equal(t, vacc.ID.String(), hits[0])
	assert.NotContains(t, hits, otherVacc.ID.String(), "another household's document")
	assert.Equal(t, "document", res.Data.Results[0].EntityType)
	assert.Equal(t, "Dog vaccination record", res.Data.Results[0].Title)

	// Keyword on the description.
	assert.Contains(t, fileHits(doSearch(t, sh, hh, auth.RoleFamilyMember, "shingles")), roof.ID.String())

	// PATCH re-indexes: the new title is searchable, semantically too.
	code, body := call(t, h, hh, auth.RoleFamilyMember, http.MethodPatch, "/api/v1/files/"+roof.ID.String(),
		map[string]any{"title": "Car insurance card", "category": "insurance", "description": nil})
	require.Equal(t, http.StatusOK, code, string(body))
	assert.Contains(t, fileHits(doSearch(t, sh, hh, auth.RoleFamilyMember, "auto insurance")), roof.ID.String())
	assert.NotContains(t, fileHits(doSearch(t, sh, hh, auth.RoleFamilyMember, "shingles")), roof.ID.String())

	// A credential's file: family sees it, a viewer does not.
	sec := secretRow(t, pool, hh)
	sf := up(hh, "codes.txt", map[string]string{"title": "Bank recovery codes", "entity_type": "secret", "entity_id": sec.String()})
	assert.Contains(t, fileHits(doSearch(t, sh, hh, auth.RoleOwner, "bank recovery codes")), sf.ID.String())
	assert.NotContains(t, fileHits(doSearch(t, sh, hh, auth.RoleViewer, "bank recovery codes")), sf.ID.String())

	// A file linked from (not attached to) a credential (#4775): its
	// metadata document is "document-<id>", but the Postgres check drops it
	// for a viewer.
	pp := up(hh, "IMG_0042.jpg", map[string]string{"title": "Passport scan", "category": "legal"})
	linkFromSecret(t, pool, hh, sec, pp.ID)
	assert.Contains(t, fileHits(doSearch(t, sh, hh, auth.RoleOwner, "passport scan")), pp.ID.String())
	assert.NotContains(t, fileHits(doSearch(t, sh, hh, auth.RoleViewer, "passport scan")), pp.ID.String())

	// Delete removes the metadata document from household_search.
	require.Equal(t, http.StatusNoContent, deleteFile(t, h, hh, vacc.ID))
	assert.NotContains(t, fileHits(doSearch(t, sh, hh, auth.RoleFamilyMember, "dog vaccination")), vacc.ID.String())

	// Stale index (file row deleted behind the API's back, document left in
	// Typesense): the Postgres check drops the hit.
	_, err := pool.Exec(context.Background(), `DELETE FROM files WHERE id = $1`, otherVacc.ID)
	require.NoError(t, err)
	assert.Empty(t, fileHits(doSearch(t, sh, other, auth.RoleFamilyMember, "dog vaccination")))
	_ = client.DeleteDocument(context.Background(), "document-"+otherVacc.ID.String())
}
