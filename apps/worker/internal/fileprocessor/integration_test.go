package fileprocessor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"home-os/worker/internal/files"
	"home-os/worker/internal/search"
	"home-os/worker/internal/tika"
)

// TestSemanticIndexingIntegration runs the real pipeline: a PDF in Postgres
// is OCR'd by Tika, chunked and indexed into Typesense with local embeddings,
// and found by a paraphrased query that shares no words with the text. It
// also covers migrating a pre-chunking `files` collection, re-indexing on a
// rename, and dropping stale chunks when the text shrinks.
//
// Needs Docker (Postgres 16, typesense/typesense:30.2, apache/tika:4.2.0-1-full,
// all on random host ports) and, on a cold Typesense data dir, a route to
// models.typesense.org for the one-time model download. Skipped with -short.
func TestSemanticIndexingIntegration(t *testing.T) {
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
	tikaHost, tikaPort := startContainer(ctx, t, testcontainers.ContainerRequest{
		Image:        "apache/tika:4.2.0-1-full",
		ExposedPorts: []string{"9998/tcp"},
		WaitingFor:   wait.ForHTTP("/tika").WithPort("9998/tcp").WithStartupTimeout(3 * time.Minute),
	}, "9998/tcp")
	ts := &tsAdmin{base: fmt.Sprintf("http://%s:%s", tsHost, tsPort), key: "test-key"}

	// A `files` collection as the worker created it before chunking and
	// embeddings, holding one legacy document.
	legacy := map[string]any{
		"name": "files",
		"fields": []map[string]any{
			{"name": "id", "type": "string"},
			{"name": "household_id", "type": "string", "facet": true},
			{"name": "name", "type": "string"},
			{"name": "extracted_text", "type": "string"},
			{"name": "entity_type", "type": "string", "optional": true, "facet": true},
			{"name": "entity_id", "type": "string", "optional": true},
			{"name": "entity_name", "type": "string", "optional": true},
			{"name": "tags", "type": "string[]", "facet": true},
			{"name": "created_at", "type": "int64"},
		},
		"default_sorting_field": "created_at",
	}
	ts.mustDo(t, http.MethodPost, "/collections", legacy, nil)

	var hh string
	if err := pool.QueryRow(ctx, `INSERT INTO households (name) VALUES ('Test') RETURNING id::text`).Scan(&hh); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	pdfID := insertFile(ctx, t, pool, hh, "home-policy-2026.pdf", "application/pdf", policyPDF())
	petID := insertFile(ctx, t, pool, hh, "rex.txt", "text/plain",
		[]byte("Rex received his rabies and distemper vaccinations at the veterinary clinic."))
	ts.mustDo(t, http.MethodPost, "/collections/files/documents",
		map[string]any{"id": petID, "household_id": hh, "name": "rex.txt", "extracted_text": "old",
			"tags": []string{}, "created_at": 1}, nil)

	searchClient := search.NewClient(tsHost, tsPort, "test-key")
	if err := searchClient.InitCollection(ctx); err != nil {
		t.Fatalf("InitCollection: %v", err)
	}
	var schema struct {
		Fields []struct {
			Name  string `json:"name"`
			Embed *struct {
				From []string `json:"from"`
			} `json:"embed"`
		} `json:"fields"`
	}
	ts.mustDo(t, http.MethodGet, "/collections/files", nil, &schema)
	got := map[string]bool{}
	for _, f := range schema.Fields {
		got[f.Name] = true
		if f.Name == search.EmbeddingField && (f.Embed == nil || strings.Join(f.Embed.From, ",") != "name,extracted_text") {
			t.Errorf("embedding field = %+v, want embed from name,extracted_text", f)
		}
	}
	if !got[search.EmbeddingField] {
		// InitCollection only logs a failed embedding alter (keyword search
		// keeps working), so repeat the alter here to put Typesense's reason
		// in the failure. The usual cause is the one-time model download from
		// models.typesense.org failing (no route, proxy, slow link), not code.
		t.Fatalf("migrated schema lacks %s; re-running the alter gives: %s", search.EmbeddingField, ts.alterEmbedding())
	}
	for _, name := range []string{"file_id", "chunk_index", "chunk_count", "row_version"} {
		if !got[name] {
			t.Errorf("migrated schema lacks %s", name)
		}
	}

	p := New(files.NewRepo(pool), tika.NewClient(tika.WithBaseURL(fmt.Sprintf("http://%s:%s", tikaHost, tikaPort))),
		WithSearchClient(searchClient))
	if err := p.processOnce(ctx); err != nil {
		t.Fatalf("processOnce: %v", err)
	}

	var status, text string
	if err := pool.QueryRow(ctx, `SELECT ocr_status, COALESCE(extracted_text, '') FROM files WHERE id = $1`, pdfID).Scan(&status, &text); err != nil {
		t.Fatal(err)
	}
	if status != files.OCRStatusDone || !strings.Contains(text, "burst pipe") {
		t.Fatalf("pdf ocr_status = %s, text has the clause = %v", status, strings.Contains(text, "burst pipe"))
	}
	pdfChunks := ts.countFileDocs(t, pdfID)
	if pdfChunks < 5 {
		t.Fatalf("pdf indexed as %d chunk documents, want a multi-page document chunked", pdfChunks)
	}
	if n := ts.countFileDocs(t, petID); n != 1 {
		t.Errorf("pet file: %d documents, want 1 (legacy doc replaced)", n)
	}

	// Pure semantic search (query_by=embedding): the query shares no words
	// with the document. The clause is deep in the PDF, far past the ~256
	// tokens one whole-file embedding would cover.
	const query = "flooding caused by a broken plumbing line"
	for _, w := range strings.Fields(query) {
		if len(w) > 3 && strings.Contains(strings.ToLower(text), w) {
			t.Fatalf("query word %q occurs in the document; the test must be purely semantic", w)
		}
	}
	hits := ts.search(t, hh, query, "embedding")
	if len(hits) == 0 || hits[0].FileID != pdfID {
		t.Fatalf("semantic query %q: hits = %+v, want the policy PDF first", query, hits)
	}
	if hits[0].ChunkIndex == 0 || !strings.Contains(hits[0].Text, "burst pipe") {
		t.Errorf("best chunk = #%d %q, want the later chunk holding the water-damage clause", hits[0].ChunkIndex, hits[0].Text)
	}
	t.Logf("semantic hit: chunk %d/%d distance %.3f", hits[0].ChunkIndex, pdfChunks, hits[0].Distance)

	// The API's hybrid query (keyword + vector, distance threshold 0.75).
	hits = ts.search(t, hh, query, "name,extracted_text,embedding")
	if len(hits) == 0 || hits[0].FileID != pdfID {
		t.Fatalf("hybrid query: hits = %+v, want the policy PDF first", hits)
	}
	hits = ts.search(t, hh, "pet immunization", "name,extracted_text,embedding")
	if len(hits) == 0 || hits[0].FileID != petID {
		t.Errorf("hybrid 'pet immunization': hits = %+v, want rex.txt first", hits)
	}

	// Rename + tags (what PATCH /api/v1/files/{id} writes): re-indexed on
	// the next tick, every chunk.
	if _, err := pool.Exec(ctx, `UPDATE files SET name = 'Homeowners insurance.pdf', tags = '{insurance,house}', updated_at = NOW() WHERE id = $1`, pdfID); err != nil {
		t.Fatal(err)
	}
	if err := p.processOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var renamed struct {
		Found int `json:"found"`
	}
	ts.mustDo(t, http.MethodGet, "/collections/files/documents/search?"+url.Values{
		"q": {"*"}, "filter_by": {fmt.Sprintf("file_id:=`%s` && name:=`Homeowners insurance.pdf` && tags:=[insurance]", pdfID)},
	}.Encode(), nil, &renamed)
	if renamed.Found != pdfChunks {
		t.Errorf("after rename: %d of %d chunks carry the new name and tags", renamed.Found, pdfChunks)
	}

	// A fresh processor (restart) resumes from the watermark: nothing to do.
	p2 := New(files.NewRepo(pool), tika.NewClient(), WithSearchClient(searchClient))
	if err := p2.indexOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var updatedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM files WHERE id = $1`, pdfID).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if !p2.indexCursor.UpdatedAt.Equal(updatedAt) {
		t.Errorf("restart cursor = %v, want the renamed file's updated_at %v", p2.indexCursor.UpdatedAt, updatedAt)
	}

	// Shorter text: the extra chunks are deleted.
	if _, err := pool.Exec(ctx, `UPDATE files SET extracted_text = 'Short replacement text.', updated_at = NOW() WHERE id = $1`, pdfID); err != nil {
		t.Fatal(err)
	}
	if err := p.processOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := ts.countFileDocs(t, pdfID); n != 1 {
		t.Errorf("after shrinking: %d documents, want 1", n)
	}

	if err := searchClient.DeleteFile(ctx, pdfID); err != nil {
		t.Fatal(err)
	}
	if n := ts.countFileDocs(t, pdfID); n != 0 {
		t.Errorf("after DeleteFile: %d documents, want 0", n)
	}
}

// policyPDF builds a 12-page PDF: unrelated filler, with the water-damage
// clause on page 9.
func policyPDF() []byte {
	var pages [][]string
	topics := []string{
		"The insured dwelling is the single family residence at the address listed on the declarations page.",
		"Premiums are payable annually and a grace period of thirty days applies to late payments.",
		"Liability coverage protects against claims from guests injured on the premises.",
		"Personal property such as furniture, clothing and electronics is covered at replacement cost.",
		"The deductible for windstorm and hail claims is two percent of the dwelling limit.",
	}
	for i := range 12 {
		var lines []string
		lines = append(lines, fmt.Sprintf("Section %d", i+1))
		if i == 8 {
			lines = append(lines,
				"Water damage. Sudden and accidental discharge of water from a burst pipe,",
				"water heater or other household system is covered up to 25,000 dollars per",
				"occurrence, including the cost of tearing out and replacing walls to reach the leak.")
		}
		for j := range 14 {
			lines = append(lines, fmt.Sprintf("%s Clause %d.%d.", topics[(i+j)%len(topics)], i+1, j+1))
		}
		pages = append(pages, lines)
	}
	return buildPDF(pages)
}

// buildPDF writes a minimal valid PDF (Helvetica text, one content stream per
// page) with a correct xref table, so Tika's PDF parser extracts the text.
func buildPDF(pages [][]string) []byte {
	var buf bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	esc := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`)

	buf.WriteString("%PDF-1.4\n")
	n := len(pages)
	// 1: catalog, 2: pages, 3: font, then page/content pairs.
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, n)
	for i := range n {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for i, lines := range pages {
		var cs strings.Builder
		cs.WriteString("BT /F1 9 Tf 40 760 Td 12 TL\n")
		for _, l := range lines {
			fmt.Fprintf(&cs, "(%s) Tj T*\n", esc.Replace(l))
		}
		cs.WriteString("ET")
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 5+2*i))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", cs.Len(), cs.String()))
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return buf.Bytes()
}

func insertFile(ctx context.Context, t *testing.T, pool *pgxpool.Pool, hh, name, contentType string, data []byte) string {
	t.Helper()
	var id string
	err := pool.QueryRow(ctx, `
		WITH b AS (INSERT INTO file_blobs (data, size) VALUES ($1, $2) RETURNING id)
		INSERT INTO files (household_id, blob_id, name, content_type, size)
		SELECT $3, b.id, $4, $5, $2 FROM b RETURNING id::text`,
		data, len(data), hh, name, contentType).Scan(&id)
	if err != nil {
		t.Fatalf("insert file %s: %v", name, err)
	}
	return id
}

// --- Typesense helpers --------------------------------------------------------

type tsAdmin struct{ base, key string }

func (a *tsAdmin) mustDo(t *testing.T, method, path string, in, out any) {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, a.base+path, body)
	req.Header.Set("X-TYPESENSE-API-KEY", a.key)
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
}

// alterEmbedding PATCHes the auto-embedding field onto `files` and returns
// Typesense's status and body (or the transport error), for diagnostics.
func (a *tsAdmin) alterEmbedding() string {
	b, _ := json.Marshal(map[string]any{"fields": []map[string]any{{
		"name": search.EmbeddingField, "type": "float[]",
		"embed": map[string]any{
			"from":         []string{"name", "extracted_text"},
			"model_config": map[string]any{"model_name": search.EmbeddingModel},
		},
	}}})
	req, _ := http.NewRequest(http.MethodPatch, a.base+"/collections/files", bytes.NewReader(b))
	req.Header.Set("X-TYPESENSE-API-KEY", a.key)
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return fmt.Sprintf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func (a *tsAdmin) countFileDocs(t *testing.T, fileID string) int {
	t.Helper()
	var r struct {
		Found int `json:"found"`
	}
	a.mustDo(t, http.MethodGet, "/collections/files/documents/search?"+url.Values{
		"q": {"*"}, "filter_by": {fmt.Sprintf("file_id:=`%s`", fileID)},
	}.Encode(), nil, &r)
	return r.Found
}

type tsHit struct {
	FileID     string
	ChunkIndex int
	Text       string
	Distance   float64
}

// search runs the query the way the API does (vector distance threshold
// 0.75), household-scoped, with the given query_by.
func (a *tsAdmin) search(t *testing.T, hh, q, queryBy string) []tsHit {
	t.Helper()
	var r struct {
		Hits []struct {
			Document struct {
				FileID        string `json:"file_id"`
				ChunkIndex    int    `json:"chunk_index"`
				ExtractedText string `json:"extracted_text"`
			} `json:"document"`
			VectorDistance float64 `json:"vector_distance"`
		} `json:"hits"`
	}
	a.mustDo(t, http.MethodGet, "/collections/files/documents/search?"+url.Values{
		"q":              {q},
		"query_by":       {queryBy},
		"filter_by":      {fmt.Sprintf("household_id:=`%s`", hh)},
		"exclude_fields": {"embedding"},
		"vector_query":   {"embedding:([], distance_threshold: 0.75)"},
	}.Encode(), nil, &r)
	out := make([]tsHit, len(r.Hits))
	for i, h := range r.Hits {
		out[i] = tsHit{h.Document.FileID, h.Document.ChunkIndex, h.Document.ExtractedText, h.VectorDistance}
	}
	return out
}

// --- containers ---------------------------------------------------------------

func startContainer(ctx context.Context, t *testing.T, req testcontainers.ContainerRequest, port string) (string, string) {
	t.Helper()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		t.Fatalf("start %s (is Docker running?): %v", req.Image, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = c.Terminate(ctx)
	})
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.MappedPort(ctx, port)
	if err != nil {
		t.Fatal(err)
	}
	return host, p.Port()
}

// startPostgres starts Postgres 16 on a random port and applies every
// packages/db/migrations/*.up.sql (same approach as cmd/worker's tests).
func startPostgres(ctx context.Context, t *testing.T) *pgxpool.Pool {
	t.Helper()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("homeos"),
		tcpostgres.WithUsername("homeos"),
		tcpostgres.WithPassword("homeos"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container (is Docker running?): %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = container.Terminate(ctx)
	})
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	// Migrations GRANT to the application role, which a fresh Postgres lacks.
	if _, err := conn.Exec(ctx, `CREATE ROLE app`); err != nil {
		t.Fatalf("create app role: %v", err)
	}
	_, file, _, _ := runtime.Caller(0)
	var dir string
	for d := filepath.Dir(file); ; d = filepath.Dir(d) {
		if fi, err := os.Stat(filepath.Join(d, "packages", "db", "migrations")); err == nil && fi.IsDir() {
			dir = filepath.Join(d, "packages", "db", "migrations")
			break
		}
		if filepath.Dir(d) == d {
			t.Fatal("packages/db/migrations not found")
		}
	}
	migrations, _ := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	sort.Strings(migrations)
	for _, f := range migrations {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(f), err)
		}
	}
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
