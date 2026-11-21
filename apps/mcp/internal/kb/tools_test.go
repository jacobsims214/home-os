package kb

// #5502: the four KB tools over MCP, and the "one agent writes, another finds
// it" case that is the whole point of the feature.
//
// The A-writes / B-finds half runs against a real Postgres and a real
// Typesense when TYPESENSE_TEST_ADDR/KEY are set (the same contract as the
// guides tests and CI's must-PASS list); without them the hybrid half is
// skipped and the word fallback is asserted instead. The search half is the
// same code path kb_search runs in production: the reconciler indexes the row
// from Postgres, and the tool queries the index.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// testPool is the database these tests run on: the package's usual one (a
// fresh, fully migrated Postgres 16 container) or, when TEST_DATABASE_URL is
// set, that database instead (every migration applied, the app role created —
// the same contract apps/api's DB tests have). The TEST_DATABASE_URL form lets
// a run without a working Docker daemon still exercise these tools.
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

// cleanupHousehold removes a seeded household from a shared database (a no-op
// on the container, which is dropped with the test binary): the household
// cascade takes its KB docs with it.
func cleanupHousehold(t *testing.T, pool *pgxpool.Pool, hh uuid.UUID) {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		return
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hh)
	})
}

// env is the JSON a KB tool returns.
type env struct {
	OK      bool            `json:"ok"`
	Summary string          `json:"summary"`
	Data    json.RawMessage `json:"data"`
	Items   []hitData       `json:"items"`
	Next    []string        `json:"next"`
	Error   *struct {
		Message string `json:"message"`
		Fix     string `json:"fix"`
		Field   string `json:"field"`
	} `json:"error"`
}

type tool struct {
	def mcp.Tool
	h   toolHandler
}

type tools struct{ search, get, write, list tool }

// newTools builds the four tools. ts is the Typesense the search tool queries;
// nil means "no index configured", which is the word-fallback path.
func newTools(pool *pgxpool.Pool, ts *typesense) tools {
	mk := func(_ string, d mcp.Tool, h toolHandler) tool { return tool{d, h} }
	return tools{
		search: mk(NewKBSearchTool(pool, ts)),
		get:    mk(NewKBGetTool(pool)),
		write:  mk(NewKBWriteTool(pool)),
		list:   mk(NewKBListTool(pool)),
	}
}

// run calls the tool and returns its decoded envelope.
func (w tool) run(t *testing.T, ctx context.Context, args map[string]any) env {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = w.def.Name
	req.Params.Arguments = args
	res, err := w.h(ctx, req)
	if err != nil {
		t.Fatalf("%s returned a Go error: %v", w.def.Name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("%s: want 1 content item, got %d", w.def.Name, len(res.Content))
	}
	text := res.Content[0].(mcp.TextContent).Text
	var e env
	if err := json.Unmarshal([]byte(text), &e); err != nil {
		t.Fatalf("%s: decode %q: %v", w.def.Name, text, err)
	}
	if e.OK == res.IsError {
		t.Fatalf("%s: ok=%v but isError=%v (%s)", w.def.Name, e.OK, res.IsError, text)
	}
	if len(e.Next) > 3 {
		t.Errorf("%s gives %d next hints: %v", w.def.Name, len(e.Next), e.Next)
	}
	return e
}

func (e env) doc(t *testing.T) docData {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s %+v", e.Summary, e.Error)
	}
	var d docData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("decode doc: %v (%s)", err, e.Data)
	}
	return d
}

// paths is the hit list's paths, in order.
func (e env) paths() []string {
	out := make([]string, 0, len(e.Items))
	for _, it := range e.Items {
		out = append(out, it.Path)
	}
	return out
}

// memberCtx adds a member with role to hh and returns their claims context.
func memberCtx(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) context.Context {
	t.Helper()
	bg := context.Background()
	email := role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(bg,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
		email, "Member "+role).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
		hh.HouseholdID, uid, role); err != nil {
		t.Fatal(err)
	}
	return server.ContextWithClaims(bg, &auth.Claims{
		Identity: auth.Identity{Subject: uid.String(), Email: email},
		UserID:   uid.String(), HouseholdID: hh.HouseholdID.String(), Role: role,
	})
}

// TestKBTools covers write -> get -> list -> replace -> delete, and the role
// scoping the ticket asks for: every member reads, a housesitter does not
// write.
func TestKBTools(t *testing.T) {
	pool := testPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	cleanupHousehold(t, pool, hh.HouseholdID)
	ts := newTools(pool, nil) // no index: the word fallback answers searches

	// --- a family member writes the doc the ticket names ------------------
	written := ts.write.run(t, ctx, map[string]any{
		"path": "how-to/pets/vet-after-hours", "title": "Vet: what to do after hours",
		"body": "Call Northside Veterinary Emergency on 555-0142 after 8pm.",
		"tags": []any{"pets", "vets"},
	})
	var wd writeData
	if err := json.Unmarshal(written.Data, &wd); err != nil {
		t.Fatal(err)
	}
	d := wd.Doc
	if d.Path != "how-to/pets/vet-after-hours" {
		t.Fatalf("path = %q", d.Path)
	}
	if d.CreatedBy == nil || *d.CreatedBy != "Test Owner" {
		t.Errorf("created_by = %v, want the writer's name", d.CreatedBy)
	}
	if !wd.Created {
		t.Error("first write should report created=true")
	}

	// --- kb_get reads it back by path ------------------------------------
	got := ts.get.run(t, ctx, map[string]any{"path": "how-to/pets/vet-after-hours"}).doc(t)
	if !strings.Contains(got.Body, "Northside Veterinary Emergency") {
		t.Errorf("body = %q", got.Body)
	}

	// --- kb_list discovers it --------------------------------------------
	listed := ts.list.run(t, ctx, map[string]any{})
	if len(listed.Items) != 1 || listed.Items[0].Path != "how-to/pets/vet-after-hours" {
		t.Fatalf("list = %v", listed.paths())
	}

	// --- a second write REPLACES, and says so ----------------------------
	replaced := ts.write.run(t, ctx, map[string]any{
		"path": "how-to/pets/vet-after-hours", "title": "Vet: after hours",
		"body": "New number: 555-0199.",
	})
	var rd writeData
	if err := json.Unmarshal(replaced.Data, &rd); err != nil {
		t.Fatal(err)
	}
	if rd.Created {
		t.Error("a write to an existing path must report created=false")
	}
	if !strings.Contains(replaced.Summary, "REPLACED") {
		t.Errorf("summary should say the doc was replaced: %q", replaced.Summary)
	}
	after := ts.get.run(t, ctx, map[string]any{"path": "how-to/pets/vet-after-hours"}).doc(t)
	if strings.Contains(after.Body, "Northside") || !strings.Contains(after.Body, "555-0199") {
		t.Errorf("the replacement did not replace the body: %q", after.Body)
	}

	// --- a housesitter reads but cannot write ----------------------------
	sitter := memberCtx(t, pool, hh, "housesitter")
	read := ts.get.run(t, sitter, map[string]any{"path": "how-to/pets/vet-after-hours"})
	if !read.OK {
		t.Fatalf("a housesitter must be able to read the KB: %s", read.Summary)
	}
	if got := ts.list.run(t, sitter, map[string]any{}); !got.OK {
		t.Errorf("a housesitter must be able to list the KB: %s", got.Summary)
	}
	denied := ts.write.run(t, sitter, map[string]any{
		"path": "how-to/pets/vet-after-hours", "title": "Hijacked", "body": "no",
	})
	if denied.OK {
		t.Fatal("a housesitter must not write the KB")
	}
	if denied.Error == nil || !strings.Contains(strings.ToLower(denied.Error.Message), "housesitter") {
		t.Errorf("the refusal should name the role: %+v", denied.Error)
	}
	// ...and the doc is untouched.
	if got := ts.get.run(t, ctx, map[string]any{"path": "how-to/pets/vet-after-hours"}).doc(t); got.Title != "Vet: after hours" {
		t.Errorf("the refused write changed the doc: title = %q", got.Title)
	}

	// --- a vendor reads too (the policy's "everyone") --------------------
	vendor := memberCtx(t, pool, hh, "vendor")
	if got := ts.get.run(t, vendor, map[string]any{"path": "how-to/pets/vet-after-hours"}); !got.OK {
		t.Errorf("a vendor must be able to read the KB: %s", got.Summary)
	}

	// --- a bad path is refused with a fix --------------------------------
	bad := ts.write.run(t, ctx, map[string]any{"path": "How To/Vet!", "title": "x", "body": "y"})
	if bad.OK {
		t.Fatal("an invalid path must be refused")
	}
	if bad.Error == nil || bad.Error.Fix == "" {
		t.Errorf("a refusal must carry a fix: %+v", bad.Error)
	}

	// --- a search finds it by words when there is no index ---------------
	byWord := ts.search.run(t, ctx, map[string]any{"query": "after hours"})
	if !byWord.OK {
		t.Fatalf("search failed: %s", byWord.Summary)
	}
	if got := byWord.paths(); len(got) == 0 || got[0] != "how-to/pets/vet-after-hours" {
		t.Errorf("word search = %v", got)
	}

	// --- delete is a soft delete, and the doc leaves the shelf ----------
	// (kb_delete is not in the ticket's four tools; the API's DELETE does it.
	// Prove the list/get path honours the soft-delete flag instead.)
	if _, err := pool.Exec(ctx, `UPDATE kb_docs SET deleted_at = now() WHERE household_id = $1`, hh.HouseholdID); err != nil {
		t.Fatal(err)
	}
	if got := ts.list.run(t, ctx, map[string]any{}); len(got.Items) != 0 {
		t.Errorf("a soft-deleted doc must not list: %v", got.paths())
	}
	missing := ts.get.run(t, ctx, map[string]any{"path": "how-to/pets/vet-after-hours"})
	if missing.OK {
		t.Error("a soft-deleted doc must not be gettable")
	}
}

// TestKBWriteIndexesNothingAndTheReconcilerDoes is the #5298 lesson, tested on
// the KB: kb_write puts the row in Postgres and does NOT index it, so a doc
// written by an agent is only findable after the reconciler runs. A test that
// skipped the "not findable yet" step would pass on a tree where the write
// path indexed and the reconciler was broken.
func TestKBWriteIndexesNothingAndTheReconcilerDoes(t *testing.T) {
	pool := testPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	cleanupHousehold(t, pool, hh.HouseholdID)
	ts := newTools(pool, nil)

	ts.write.run(t, ctx, map[string]any{
		"path": "how-to/networking/router-reset", "title": "Router reset",
		"body": "Unplug it for 30 seconds.",
	})

	var indexed int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM kb_docs WHERE household_id = $1 AND deleted_at IS NULL`,
		hh.HouseholdID).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != 1 {
		t.Fatalf("kb_write should have written the row; count = %d", indexed)
	}
	// The row is exactly what the reconciler reads. (The reconciler itself is
	// exercised against a real index in apps/api/internal/kb's integration
	// test; here we pin only that the write path leaves indexing to it.)
	var title, body string
	if err := pool.QueryRow(ctx,
		`SELECT title, body FROM kb_docs WHERE household_id = $1`, hh.HouseholdID).Scan(&title, &body); err != nil {
		t.Fatal(err)
	}
	if title != "Router reset" || !strings.Contains(body, "Unplug it") {
		t.Errorf("row = %q / %q", title, body)
	}
}

// --- the A-writes / B-finds case, over the real index ---------------------

// TestAgentAWritesAgentBFindsByMeaning is the ticket's headline test, run
// through the MCP tools themselves. Agent A calls kb_write; the reconciler
// (the same one the API runs) indexes from Postgres; Agent B — a HOUSESITTER —
// calls kb_search with a question in her own words and finds the doc by
// meaning. Skipped without a Typesense (TYPESENSE_TEST_ADDR/KEY), like the
// other hybrid tests in CI's must-PASS list.
func TestAgentAWritesAgentBFindsByMeaning(t *testing.T) {
	addr, key := os.Getenv("TYPESENSE_TEST_ADDR"), os.Getenv("TYPESENSE_TEST_KEY")
	if addr == "" || key == "" {
		t.Skip("TYPESENSE_TEST_ADDR/TYPESENSE_TEST_KEY not set; skipping the semantic half")
	}
	pool := testPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	cleanupHousehold(t, pool, hh.HouseholdID)
	collection := "household_search_kbtest_" + uuid.NewString()[:8]

	host, port, ok := strings.Cut(addr, ":")
	if !ok {
		t.Fatalf("TYPESENSE_TEST_ADDR %q is not host:port", addr)
	}
	ts := &typesense{baseURL: "http://" + host + ":" + port, apiKey: key, client: &http.Client{Timeout: 10 * time.Second}}
	// The tool queries the collection it is configured with; this test owns a
	// collection of its own, so it points the tool at it.
	own := &typesense{baseURL: ts.baseURL, apiKey: key, client: ts.client}
	t.Cleanup(func() { dropCollection(t, own, collection) })
	createCollection(t, own, collection)

	tools := newTools(pool, own.WithCollection(collection))

	// Agent A writes the doc, as a family member.
	agentA := memberCtx(t, pool, hh, "family_manager")
	tools.write.run(t, agentA, map[string]any{
		"path": "how-to/pets/vet-after-hours", "title": "Vet: what to do after hours",
		"body": "Northside Veterinary Emergency is open all night on 555-0142. " +
			"For bloat or trouble breathing go straight there; a mild limp can wait for our own vet at 8am.",
		"tags": []any{"pets", "vets", "emergency"},
	})

	// Postgres is the source of truth; index it the way the API's reconciler
	// does — read the live rows and upsert their documents.
	reindex(t, pool, own, collection, hh.HouseholdID)
	waitForIndex(t, own, collection, hh.HouseholdID)

	// Agent B is the housesitter: read-only, and that is the point.
	agentB := memberCtx(t, pool, hh, "housesitter")
	found := tools.search.run(t, agentB, map[string]any{"query": "what do we do if the dog is sick at night?"})
	if !found.OK {
		t.Fatalf("Agent B's search failed: %s", found.Summary)
	}
	if len(found.Items) == 0 {
		t.Fatal("Agent B found nothing; the KB is not searchable by meaning")
	}
	if found.Items[0].Path != "how-to/pets/vet-after-hours" {
		t.Fatalf("Agent B's best hit = %q, want how-to/pets/vet-after-hours", found.Items[0].Path)
	}
	if found.Items[0].UpdatedBy == nil || *found.Items[0].UpdatedBy == "" {
		t.Error("a hit should say whose knowledge it is")
	}
	t.Logf("Agent B asked %q → %v", "what do we do if the dog is sick at night?", found.paths())

	// And she reads the whole doc with kb_get, which is what a person does next.
	doc := tools.get.run(t, agentB, map[string]any{"path": found.Items[0].Path}).doc(t)
	if !strings.Contains(doc.Body, "555-0142") {
		t.Errorf("kb_get after kb_search returned the wrong doc: %q", doc.Title)
	}
}

// --- index helpers -------------------------------------------------------

// WithCollection returns a copy of the client pointed at another collection.
func (t *typesense) WithCollection(name string) *typesense {
	c := *t
	c.collection = name
	return &c
}

func createCollection(t *testing.T, ts *typesense, name string) {
	t.Helper()
	body := map[string]any{
		"name": name,
		"fields": []any{
			map[string]any{"name": "entity_id", "type": "string"},
			map[string]any{"name": "household_id", "type": "string", "facet": true},
			map[string]any{"name": "entity_type", "type": "string", "facet": true},
			map[string]any{"name": "title", "type": "string"},
			map[string]any{"name": "body", "type": "string"},
			map[string]any{"name": "tags", "type": "string[]", "optional": true},
			map[string]any{"name": "embedding", "type": "float[]", "optional": true,
				"embed": map[string]any{"from": []string{"title", "body"}, "model_config": map[string]any{"model_name": "ts/all-MiniLM-L12-v2"}}},
		},
	}
	resp := doJSON(t, ts, http.MethodPost, "/collections", body)
	if resp.status >= 300 && resp.status != 409 {
		t.Fatalf("create collection %s: %d %s", name, resp.status, resp.body)
	}
}

func dropCollection(t *testing.T, ts *typesense, name string) {
	t.Helper()
	doJSON(t, ts, http.MethodDelete, "/collections/"+name, nil)
}

// reindex upserts each live kb_doc's document, exactly as searchrecon does.
func reindex(t *testing.T, pool *pgxpool.Pool, ts *typesense, collection string, hh uuid.UUID) {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT id, path, title, body, tags FROM kb_docs WHERE household_id = $1 AND deleted_at IS NULL`, hh)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var path, title, body string
		var tags []string
		if err := rows.Scan(&id, &path, &title, &body, &tags); err != nil {
			t.Fatal(err)
		}
		doc := map[string]any{
			"id":           "kb_doc-" + id.String(),
			"entity_id":    id.String(),
			"household_id": hh.String(),
			"entity_type":  "kb_doc",
			"title":        title,
			"body":         path + "\n\n" + body,
			"tags":         tags,
		}
		resp := doJSON(t, ts, http.MethodPost, "/collections/"+collection+"/documents?action=upsert", doc)
		if resp.status >= 300 {
			t.Fatalf("upsert: %d %s", resp.status, resp.body)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// waitForIndex waits until Typesense reports the document (embeddings are
// computed asynchronously).
func waitForIndex(t *testing.T, ts *typesense, collection string, hh uuid.UUID) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		q := url.Values{}
		q.Set("q", "*")
		q.Set("filter_by", "household_id:="+hh.String())
		resp := doJSON(t, ts, http.MethodGet, "/collections/"+collection+"/documents/search?"+q.Encode(), nil)
		if resp.status < 300 && strings.Contains(resp.body, `"found":1`) {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("the document never appeared in the index")
}

type jsonResp struct {
	status int
	body   string
}

func doJSON(t *testing.T, ts *typesense, method, path string, body any) jsonResp {
	t.Helper()
	var rdr *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = strings.NewReader(string(b))
	} else {
		rdr = strings.NewReader("")
	}
	req, err := http.NewRequest(method, ts.baseURL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-TYPESENSE-API-KEY", ts.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return jsonResp{status: resp.StatusCode, body: string(b)}
}
