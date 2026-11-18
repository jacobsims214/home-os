package searchindex_test

// #5314: a row created through the MCP tool layer must be indexed.
//
// The defect: indexing lived in the API's HTTP handlers, so a note made by an
// agent through note_create was written to Postgres and never reached
// Typesense. #5298 measured household_search at 1 document against 5 bills, 60
// calendar events, 16 notes and 5 people.
//
// These tests prove the fix from the side that was broken: the row is written
// with a plain INSERT, exactly as an MCP tool writes it (the tools hold a
// pgxpool and never call the API), and the index is then produced by the same
// code the API's drain runs. If indexing ever goes back to depending on a
// caller, the first test below stops finding a document.
//
// The database is the real one (TEST_DATABASE_URL, migrated), because the
// thing under test is a trigger: a fake cannot show that a write queues.

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/searchindex"
)

func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// fakeIndex is what a Typesense would be: it records the documents written and
// the ids deleted. fail makes every write fail, to prove a rejection is parked
// rather than lost.
type fakeIndex struct {
	mu      sync.Mutex
	docs    map[string]searchindex.Document
	deleted []string
	fail    bool
	calls   int
}

func newFake() *fakeIndex { return &fakeIndex{docs: map[string]searchindex.Document{}} }

func (f *fakeIndex) IndexDocument(_ context.Context, d searchindex.Document) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail {
		return errors.New("typesense unavailable")
	}
	f.docs[d.ID] = d
	return nil
}

func (f *fakeIndex) DeleteDocument(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail {
		return errors.New("typesense unavailable")
	}
	delete(f.docs, id)
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeIndex) get(id string) (searchindex.Document, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.docs[id]
	return d, ok
}

// family inserts a household and cleans it up (deleting it cascades to every
// row the tests below write).
func family(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO households (name) VALUES ('#5314 search index') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("create household: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, id)
	})
	return id
}

// clearQueue empties the queue before a test starts. It is deliberately NOT
// scoped to one household: Sweep claims globally — one queue, shared by every
// worker, which is what production needs — so a row another test (or another
// package sharing TEST_DATABASE_URL) left behind would be claimed by this
// test's Sweep and eat into its batch. Nothing else in the test database needs
// a queue row, so dropping them all is both simpler and honest about what the
// drain does. Assertions are still per-document-id, so they cannot be fooled
// by a neighbour's document.
func clearQueue(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `DELETE FROM search_index_queue`); err != nil {
		t.Fatalf("clear queue: %v", err)
	}
}

// TestMCPWrittenNoteIsIndexed is the ticket's acceptance test. It writes a
// note the way note_create does — a plain INSERT, no API handler in the path —
// and asserts the row is queued by the trigger and that draining the queue
// produces a searchable document with the note's own text.
func TestMCPWrittenNoteIsIndexed(t *testing.T) {
	pool := testDB(t)
	hid := family(t, pool)
	clearQueue(t, pool)

	const body = "Bin day moved to Thursday because of the holiday."
	const title = "Bin day"

	var noteID string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO notes (household_id, entity_type, entity_id, title, body)
		VALUES ($1, 'household', $1, $2, $3) RETURNING id::text`,
		hid, title, body).Scan(&noteID); err != nil {
		t.Fatalf("insert note (the MCP write path): %v", err)
	}

	// The trigger queued it: this is the whole fix. Before migration 075
	// nothing at all happened for a write like this.
	var queued int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM search_index_queue WHERE entity_type = 'note' AND entity_id = $1`,
		noteID).Scan(&queued); err != nil {
		t.Fatalf("read queue: %v", err)
	}
	if queued != 1 {
		t.Fatalf("an MCP-style note insert queued %d index rows, want 1", queued)
	}

	// Drain it with the real worker, against a fake Typesense.
	idx := newFake()
	if _, err := searchindex.New(pool, idx).Sweep(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}

	doc, ok := idx.get("note-" + noteID)
	if !ok {
		t.Fatalf("a note created outside the REST path was not indexed; documents: %v", idx.docs)
	}
	if doc.EntityType != "note" || doc.EntityID != noteID {
		t.Errorf("document entity = %s/%s, want note/%s", doc.EntityType, doc.EntityID, noteID)
	}
	if doc.HouseholdID != hid {
		t.Errorf("document household = %s, want %s", doc.HouseholdID, hid)
	}
	if doc.Title != title || doc.Body != body {
		t.Errorf("document text = %q / %q, want %q / %q — a search for its words would miss it", doc.Title, doc.Body, title, body)
	}
	if doc.UpdatedAt == 0 || doc.CreatedAt == 0 {
		t.Errorf("document times are zero: %+v", doc)
	}
}

// TestUpdateAndDeleteThroughTheQueue pins the rest of the write path: an update
// re-indexes the row as it now is (one document, the new text), and a delete
// removes it. Both go through the same trigger, so neither needs a caller to
// remember anything.
func TestUpdateAndDeleteThroughTheQueue(t *testing.T) {
	pool := testDB(t)
	hid := family(t, pool)
	clearQueue(t, pool)

	var noteID string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO notes (household_id, entity_type, entity_id, title, body)
		VALUES ($1, 'household', $1, 'Draft', 'first version') RETURNING id::text`,
		hid).Scan(&noteID); err != nil {
		t.Fatalf("insert note: %v", err)
	}
	idx := newFake()
	w := searchindex.New(pool, idx)
	if _, err := w.Sweep(context.Background()); err != nil {
		t.Fatalf("drain insert: %v", err)
	}
	if _, ok := idx.get("note-" + noteID); !ok {
		t.Fatalf("insert was not indexed")
	}

	// Update twice before draining: the queue holds one row, and the document
	// written is the row as it is NOW, not the value it had when queued.
	if _, err := pool.Exec(context.Background(),
		`UPDATE notes SET body = 'second version' WHERE id = $1`, noteID); err != nil {
		t.Fatalf("update note: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE notes SET body = 'third version' WHERE id = $1`, noteID); err != nil {
		t.Fatalf("update note again: %v", err)
	}
	if _, err := w.Sweep(context.Background()); err != nil {
		t.Fatalf("drain update: %v", err)
	}
	doc, _ := idx.get("note-" + noteID)
	if doc.Body != "third version" {
		t.Errorf("after two updates the index holds %q, want the latest %q", doc.Body, "third version")
	}

	if _, err := pool.Exec(context.Background(), `DELETE FROM notes WHERE id = $1`, noteID); err != nil {
		t.Fatalf("delete note: %v", err)
	}
	if _, err := w.Sweep(context.Background()); err != nil {
		t.Fatalf("drain delete: %v", err)
	}
	if _, ok := idx.get("note-" + noteID); ok {
		t.Errorf("a deleted note is still in the index")
	}
}

// TestBuildersCoverEveryQueuedType guards the two lists that must agree: the
// entity types migration 075 puts a trigger on, and the types this package can
// build a document for. A type queued with no builder would sit in the queue
// until it was parked, so a new indexed table has to be added in both places.
func TestBuildersCoverEveryQueuedType(t *testing.T) {
	pool := testDB(t)

	rows, err := pool.Query(context.Background(), `
		SELECT DISTINCT rtrim(encode(t.tgargs, 'escape'), E'\\000')
		  FROM pg_trigger t
		  JOIN pg_class c ON c.oid = t.tgrelid
		 WHERE NOT t.tgisinternal AND t.tgname LIKE '%\_search_index'`)
	if err != nil {
		t.Fatalf("read triggers: %v", err)
	}
	defer rows.Close()

	have := map[string]bool{}
	for _, typ := range searchindex.Types() {
		have[typ] = true
	}
	seen := 0
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatalf("scan trigger: %v", err)
		}
		seen++
		if !have[typ] {
			t.Errorf("migration 075 queues %q but packages/searchindex has no document builder for it", typ)
		}
	}
	if seen == 0 {
		t.Fatal("no search_index triggers found: run migrations (075) before this test")
	}
}

// TestRejectedWriteIsParkedNotLost proves a Typesense failure does not lose the
// row: the queue row comes back with its attempt counted, so the next tick
// retries it. The old handler code logged and forgot.
func TestRejectedWriteIsParkedNotLost(t *testing.T) {
	pool := testDB(t)
	hid := family(t, pool)
	clearQueue(t, pool)

	var noteID string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO notes (household_id, entity_type, entity_id, title, body)
		VALUES ($1, 'household', $1, 'Unlucky', 'Typesense rejects this') RETURNING id::text`,
		hid).Scan(&noteID); err != nil {
		t.Fatalf("insert note: %v", err)
	}

	idx := newFake()
	idx.fail = true
	w := searchindex.New(pool, idx)
	if _, err := w.Sweep(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}

	var attempts int
	var lastError *string
	if err := pool.QueryRow(context.Background(),
		`SELECT attempts, last_error FROM search_index_queue WHERE entity_type = 'note' AND entity_id = $1`,
		noteID).Scan(&attempts, &lastError); err != nil {
		t.Fatalf("the failed row was not put back in the queue: %v", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if lastError == nil || *lastError == "" {
		t.Errorf("the failure reason was not recorded")
	}

	// Typesense comes back: the retry indexes it.
	idx.mu.Lock()
	idx.fail = false
	idx.mu.Unlock()
	if _, err := w.Sweep(context.Background()); err != nil {
		t.Fatalf("drain retry: %v", err)
	}
	if _, ok := idx.get("note-" + noteID); !ok {
		t.Errorf("the retried row was not indexed")
	}
}

// TestIDsCoverEveryBuilder keeps the second table list honest: a type with a
// document builder but no id query would build fine and then be silently
// skipped by a full rebuild (cmd/reindex).
func TestIDsCoverEveryBuilder(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	for _, typ := range searchindex.Types() {
		if _, err := searchindex.IDs(ctx, pool, typ); err != nil {
			t.Errorf("entity type %q has a document builder but no usable id query: %v", typ, err)
		}
	}
}
