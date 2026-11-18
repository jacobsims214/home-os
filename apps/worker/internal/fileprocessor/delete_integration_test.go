package fileprocessor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"home-os/worker/internal/files"
	"home-os/worker/internal/search"
	"home-os/worker/internal/tika"
)

// TestDeletedFilesLeaveSearchIntegration proves deleted files never stay
// searchable (#4602), against real Postgres 16 (all migrations) and
// typesense/typesense:30.2 on random ports:
//
//   - upload -> index (several chunk documents) -> SQL delete: the blob row is
//     gone at once (038's trigger) and the file is queued (053's trigger);
//   - with Typesense unreachable the queue row survives a tick;
//   - with Typesense back, every chunk is gone, a keyword search finds
//     nothing, and the queue is empty;
//   - a document left by a file deleted before 053 is removed by the
//     start-up orphan sweep;
//   - deleting the entity a file is attached to re-homes the file and keeps
//     it searchable (nothing is queued);
//   - deleting a household queues its files without failing, and they leave
//     the index too.
//
// Text files skip Tika, so no Tika container is needed. Skipped with -short.
func TestDeletedFilesLeaveSearchIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker; skipped with -short")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	pool := startPostgres(ctx, t)
	tsHost, tsPort := startContainer(ctx, t, testcontainers.ContainerRequest{
		Image:        "typesense/typesense:30.2",
		Cmd:          []string{"--api-key=test-key", "--data-dir=/data"},
		Tmpfs:        map[string]string{"/data": "rw"},
		ExposedPorts: []string{"8108/tcp"},
		WaitingFor:   wait.ForHTTP("/health").WithPort("8108/tcp").WithStartupTimeout(2 * time.Minute),
	}, "8108/tcp")
	ts := &tsAdmin{base: fmt.Sprintf("http://%s:%s", tsHost, tsPort), key: "test-key"}
	searchClient := search.NewClient(tsHost, tsPort, "test-key")
	if err := searchClient.InitCollection(ctx); err != nil {
		t.Fatalf("InitCollection: %v", err)
	}
	noTika := tika.NewClient(tika.WithBaseURL("http://127.0.0.1:1"))

	var hh string
	mustQuery(ctx, t, pool, `INSERT INTO households (name) VALUES ('Delete test') RETURNING id::text`, &hh)
	secretText := strings.Repeat("The zanzibarquokka safe combination is kept in the study drawer. ", 40)
	secret := insertFile(ctx, t, pool, hh, "safe.txt", "text/plain", []byte(secretText))
	kept := insertFile(ctx, t, pool, hh, "kept.txt", "text/plain", []byte("The marmalade recipe uses Seville oranges."))
	var pet string
	mustQuery(ctx, t, pool, `INSERT INTO pets (household_id, name) VALUES ($1, 'Rex') RETURNING id::text`, &pet, hh)
	petFile := insertFile(ctx, t, pool, hh, "rex.txt", "text/plain", []byte("Rex had his quokkavaccine booster."))
	if _, err := pool.Exec(ctx, `UPDATE files SET entity_type = 'pet', entity_id = $1 WHERE id = $2`, pet, petFile); err != nil {
		t.Fatal(err)
	}
	// A document of a file deleted before 053 (no files row, never queued).
	orphan := "7b0c5c1e-2f43-4c55-9d7e-0f6f6c1d2a11"
	ts.mustDo(t, http.MethodPost, "/collections/files/documents",
		map[string]any{"id": orphan, "file_id": orphan, "household_id": hh, "name": "old.txt",
			"extracted_text": "zanzibarquokka leftover", "tags": []string{}, "created_at": 1}, nil)

	p := New(files.NewRepo(pool), noTika, WithSearchClient(searchClient))
	if err := p.processOnce(ctx); err != nil {
		t.Fatalf("processOnce: %v", err)
	}
	if n := ts.countFileDocs(t, secret); n < 2 {
		t.Fatalf("secret file indexed as %d documents, want several chunks", n)
	}
	if n := ts.countFileDocs(t, orphan); n != 0 {
		t.Errorf("orphan document survived the start-up sweep (%d docs)", n)
	}
	if hits := keywordHits(t, ts, hh, "zanzibarquokka"); len(hits) == 0 {
		t.Fatal("secret file not found by keyword before delete")
	}

	// Delete the file with the API's SQL.
	var blobID string
	mustQuery(ctx, t, pool, `SELECT blob_id::text FROM files WHERE id = $1`, &blobID, secret)
	if _, err := pool.Exec(ctx, `DELETE FROM files WHERE id = $1 AND household_id = $2`, secret, hh); err != nil {
		t.Fatalf("delete file: %v", err)
	}
	if n := count(ctx, t, pool, `SELECT count(*) FROM file_blobs WHERE id = $1`, blobID); n != 0 {
		t.Errorf("blob row survived the file delete")
	}
	if n := count(ctx, t, pool, `SELECT count(*) FROM file_search_deletions WHERE file_id = $1`, secret); n != 1 {
		t.Fatalf("file not queued for search delete (%d rows)", n)
	}

	// Typesense unreachable: the delete stays queued.
	down := New(files.NewRepo(pool), noTika, WithSearchClient(search.NewClient("127.0.0.1", "1", "x")))
	down.indexReady, down.orphansSwept = true, true
	if err := down.purgeOnce(ctx); err != nil {
		t.Fatalf("purge with Typesense down: %v", err)
	}
	if n := count(ctx, t, pool, `SELECT count(*) FROM file_search_deletions WHERE file_id = $1`, secret); n != 1 {
		t.Fatalf("queue row lost while Typesense was down")
	}

	// Typesense back: every chunk goes and search finds nothing.
	if err := p.processOnce(ctx); err != nil {
		t.Fatalf("processOnce: %v", err)
	}
	if n := ts.countFileDocs(t, secret); n != 0 {
		t.Errorf("deleted file still has %d documents", n)
	}
	if hits := keywordHits(t, ts, hh, "zanzibarquokka"); len(hits) != 0 {
		t.Errorf("deleted file still searchable: %v", hits)
	}
	if n := count(ctx, t, pool, `SELECT count(*) FROM file_search_deletions`); n != 0 {
		t.Errorf("queue has %d rows after purge, want 0", n)
	}
	if n := ts.countFileDocs(t, kept); n != 1 {
		t.Errorf("kept file has %d documents, want 1", n)
	}

	// Deleting the pet re-homes its file: still there, still searchable.
	if _, err := pool.Exec(ctx, `DELETE FROM pets WHERE id = $1`, pet); err != nil {
		t.Fatal(err)
	}
	if n := count(ctx, t, pool, `SELECT count(*) FROM files WHERE id = $1 AND entity_type = 'household'`, petFile); n != 1 {
		t.Fatalf("pet file not re-homed to the household")
	}
	if n := count(ctx, t, pool, `SELECT count(*) FROM file_search_deletions`); n != 0 {
		t.Errorf("re-homing queued a search delete")
	}
	if err := p.processOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if hits := keywordHits(t, ts, hh, "quokkavaccine"); len(hits) == 0 {
		t.Error("re-homed file no longer searchable")
	}

	// Deleting the household cascades to its files; they leave the index.
	if _, err := pool.Exec(ctx, `DELETE FROM households WHERE id = $1`, hh); err != nil {
		t.Fatalf("delete household: %v", err)
	}
	if n := count(ctx, t, pool, `SELECT count(*) FROM file_search_deletions`); n != 2 {
		t.Fatalf("household delete queued %d files, want 2", n)
	}
	if err := p.processOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{kept, petFile} {
		if n := ts.countFileDocs(t, id); n != 0 {
			t.Errorf("file %s of the deleted household still has %d documents", id, n)
		}
	}
	if n := count(ctx, t, pool, `SELECT count(*) FROM file_blobs`); n != 0 {
		t.Errorf("%d blob rows left after the household delete", n)
	}
}

// keywordHits runs a household-scoped keyword-only search and returns the
// file ids hit.
func keywordHits(t *testing.T, ts *tsAdmin, hh, q string) []string {
	t.Helper()
	var r struct {
		Hits []struct {
			Document struct {
				ID     string `json:"id"`
				FileID string `json:"file_id"`
			} `json:"document"`
		} `json:"hits"`
	}
	ts.mustDo(t, http.MethodGet, "/collections/files/documents/search?"+url.Values{
		"q":              {q},
		"query_by":       {"name,extracted_text"},
		"filter_by":      {fmt.Sprintf("household_id:=`%s`", hh)},
		"exclude_fields": {"embedding"},
	}.Encode(), nil, &r)
	out := []string{}
	for _, h := range r.Hits {
		out = append(out, h.Document.ID)
	}
	return out
}

func mustQuery(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sql string, dst any, args ...any) {
	t.Helper()
	if err := pool.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func count(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	mustQuery(ctx, t, pool, sql, &n, args...)
	return n
}
