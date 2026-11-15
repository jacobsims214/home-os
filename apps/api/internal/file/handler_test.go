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
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/config"
	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
)

// These tests cover #4602: deleting a file removes its blob and every
// Typesense document for it, and never fails because Typesense is down.
//
// They need TEST_DATABASE_URL (all migrations applied, run with -p 1).
// TestDeleteFile_RemovesBlobAndSearchDocs also needs a Typesense (30.2) at
// TEST_TYPESENSE_HOST / TEST_TYPESENSE_PORT with key TEST_TYPESENSE_API_KEY.
// Each test skips when its services are not configured. The worker's
// TestDeletedFilesLeaveSearchIntegration runs the same flow in containers.

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

// household inserts a household (cleaned up afterwards) and returns its id.
func household(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var hh uuid.UUID
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO households (name) VALUES ('File delete test') RETURNING id`).Scan(&hh))
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM households WHERE id = $1`, hh)
		_, _ = pool.Exec(ctx, `DELETE FROM file_search_deletions WHERE household_id = $1`, hh)
	})
	return hh
}

func fileRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Post("/api/v1/files/upload", h.UploadFile)
	r.Delete("/api/v1/files/{id}", h.DeleteFile)
	return r
}

func asMember(req *http.Request, hh uuid.UUID) *http.Request {
	return req.WithContext(middleware.WithClaims(req.Context(),
		&auth.Claims{UserID: uuid.NewString(), HouseholdID: hh.String(), Role: "family_member"}))
}

// upload posts a text file through the handler and returns the created file.
func upload(t *testing.T, h http.Handler, hh uuid.UUID, name, text string) File {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", name)
	require.NoError(t, err)
	_, err = io.WriteString(fw, text)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, asMember(req, hh))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var out struct {
		Data File `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out.Data
}

func deleteFile(t *testing.T, h http.Handler, hh, id uuid.UUID) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, asMember(httptest.NewRequest(http.MethodDelete, "/api/v1/files/"+id.String(), nil), hh))
	return rec.Code
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

// ts is a minimal Typesense admin client for the test fixtures.
type ts struct{ base, key string }

func (c ts) do(t *testing.T, method, path, contentType string, body io.Reader) []byte {
	t.Helper()
	req, err := http.NewRequest(method, c.base+path, body)
	require.NoError(t, err)
	req.Header.Set("X-TYPESENSE-API-KEY", c.key)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	require.Less(t, resp.StatusCode, 300, "%s %s: %s", method, path, b)
	return b
}

func (c ts) found(t *testing.T, params url.Values) int {
	t.Helper()
	var r struct {
		Found int `json:"found"`
	}
	require.NoError(t, json.Unmarshal(c.do(t, http.MethodGet, "/collections/files/documents/search?"+params.Encode(), "", nil), &r))
	return r.Found
}

// index stores a file the way the worker does (apps/worker/internal/search):
// chunk 0 with id = file id, chunk n with id "<file id>_<n>", all sharing
// file_id.
func (c ts) index(t *testing.T, f File, chunks []string) {
	t.Helper()
	var lines []string
	for i, text := range chunks {
		id := f.ID.String()
		if i > 0 {
			id = fmt.Sprintf("%s_%d", f.ID, i)
		}
		b, _ := json.Marshal(map[string]any{
			"id": id, "file_id": f.ID.String(), "chunk_index": i, "chunk_count": len(chunks),
			"household_id": f.HouseholdID.String(), "name": f.Name, "extracted_text": text,
			"tags": []string{}, "created_at": f.CreatedAt.UnixMilli(),
		})
		lines = append(lines, string(b))
	}
	out := c.do(t, http.MethodPost, "/collections/files/documents/import?action=upsert", "text/plain",
		strings.NewReader(strings.Join(lines, "\n")))
	require.NotContains(t, string(out), `"success":false`)
}

// upload -> index -> delete -> the blob row is gone, no chunk is left, and a
// search finds nothing. Another file in the household is untouched.
func TestDeleteFile_RemovesBlobAndSearchDocs(t *testing.T) {
	pool := testDB(t)
	host, port, key := os.Getenv("TEST_TYPESENSE_HOST"), os.Getenv("TEST_TYPESENSE_PORT"), os.Getenv("TEST_TYPESENSE_API_KEY")
	if host == "" || port == "" {
		t.Skip("TEST_TYPESENSE_HOST/PORT not set; skipping Typesense integration test")
	}
	tsc := ts{base: fmt.Sprintf("http://%s:%s", host, port), key: key}
	// Use the worker's collection if it exists, otherwise an auto-schema one
	// with the fields the delete filters on.
	if req, _ := http.NewRequest(http.MethodGet, tsc.base+"/collections/files", nil); req != nil {
		req.Header.Set("X-TYPESENSE-API-KEY", key)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			tsc.do(t, http.MethodPost, "/collections", "application/json", strings.NewReader(
				`{"name":"files","fields":[{"name":"household_id","type":"string","facet":true},`+
					`{"name":"file_id","type":"string","optional":true},{"name":".*","type":"auto"}]}`))
		}
	}

	hh := household(t, pool)
	client := search.NewClient(&config.Config{TypesenseHost: host, TypesensePort: port, TypesenseAPIKey: key})
	h := fileRouter(NewHandler(NewRepo(pool)).WithSearch(client))

	secret := upload(t, h, hh, "safe.txt", "the zanzibarquokka safe combination")
	kept := upload(t, h, hh, "kept.txt", "marmalade recipe")
	tsc.index(t, secret, []string{
		"the zanzibarquokka safe combination, part one",
		"zanzibarquokka part two",
		"zanzibarquokka part three",
	})
	tsc.index(t, kept, []string{"marmalade recipe"})
	byFile := func(f File) url.Values {
		return url.Values{"q": {"*"}, "filter_by": {fmt.Sprintf("file_id:=`%s`", f.ID)}}
	}
	keyword := url.Values{"q": {"zanzibarquokka"}, "query_by": {"extracted_text"},
		"filter_by": {fmt.Sprintf("household_id:=`%s`", hh)}}
	require.Equal(t, 3, tsc.found(t, byFile(secret)))
	require.Equal(t, 3, tsc.found(t, keyword))

	require.Equal(t, http.StatusNoContent, deleteFile(t, h, hh, secret.ID))

	assert.Zero(t, count(t, pool, `SELECT count(*) FROM file_blobs WHERE id = $1`, secret.BlobID), "blob row left behind")
	assert.Zero(t, count(t, pool, `SELECT count(*) FROM files WHERE id = $1`, secret.ID))
	assert.Zero(t, tsc.found(t, byFile(secret)), "chunk documents left in Typesense")
	assert.Zero(t, tsc.found(t, keyword), "deleted file still searchable")
	assert.Equal(t, 1, tsc.found(t, byFile(kept)), "other file removed from search")
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM file_blobs WHERE id = $1`, kept.BlobID))
	// Queued as well; the worker's purge is idempotent.
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM file_search_deletions WHERE file_id = $1`, secret.ID))
}

// With Typesense unreachable the delete still succeeds, the blob goes, and
// the file is queued for the worker to remove from search.
func TestDeleteFile_TypesenseDownStillDeletesAndQueues(t *testing.T) {
	pool := testDB(t)
	hh := household(t, pool)
	down := search.NewClient(&config.Config{TypesenseHost: "127.0.0.1", TypesensePort: "1", TypesenseAPIKey: "x"})
	h := fileRouter(NewHandler(NewRepo(pool)).WithSearch(down))

	f := upload(t, h, hh, "a.txt", "hello")
	require.Equal(t, http.StatusNoContent, deleteFile(t, h, hh, f.ID))

	assert.Zero(t, count(t, pool, `SELECT count(*) FROM file_blobs WHERE id = $1`, f.BlobID), "blob row left behind")
	assert.Equal(t, 1, count(t, pool,
		`SELECT count(*) FROM file_search_deletions WHERE file_id = $1 AND household_id = $2`, f.ID, hh))
}

// Another household's file is a 404 and is left alone: blob, row and search.
func TestDeleteFile_OtherHouseholdNotFound(t *testing.T) {
	pool := testDB(t)
	owner, other := household(t, pool), household(t, pool)
	h := fileRouter(NewHandler(NewRepo(pool)))

	f := upload(t, h, owner, "a.txt", "hello")
	assert.Equal(t, http.StatusNotFound, deleteFile(t, h, other, f.ID))
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM file_blobs WHERE id = $1`, f.BlobID))
	assert.Zero(t, count(t, pool, `SELECT count(*) FROM file_search_deletions WHERE file_id = $1`, f.ID))
}

// A blob shared by two files survives deleting one of them (038's trigger
// keeps it); the old explicit blob delete would have failed on the FK.
func TestDeleteFile_SharedBlobKept(t *testing.T) {
	pool := testDB(t)
	hh := household(t, pool)
	h := fileRouter(NewHandler(NewRepo(pool)))

	a := upload(t, h, hh, "a.txt", "shared bytes")
	var b uuid.UUID
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO files (household_id, blob_id, name, content_type, size, entity_type, entity_id)
		 SELECT household_id, blob_id, 'b.txt', content_type, size, entity_type, entity_id FROM files WHERE id = $1
		 RETURNING id`, a.ID).Scan(&b))

	require.Equal(t, http.StatusNoContent, deleteFile(t, h, hh, a.ID))
	assert.Equal(t, 1, count(t, pool, `SELECT count(*) FROM file_blobs WHERE id = $1`, a.BlobID), "shared blob deleted")
	require.Equal(t, http.StatusNoContent, deleteFile(t, h, hh, b))
	assert.Zero(t, count(t, pool, `SELECT count(*) FROM file_blobs WHERE id = $1`, a.BlobID), "blob orphaned")
}
