package file

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"home-os/mcp/internal/testutil"
)

// testTypesense returns a client on its own, freshly created collections
// (household_search and files schemas with the local auto-embedding field,
// as the API and worker create them), dropped when t ends. It skips unless
// TYPESENSE_TEST_ADDR and TYPESENSE_TEST_KEY are set:
//
//	docker run -d --name ts-test -p 127.0.0.1::8108 --tmpfs /data typesense/typesense:30.2 --api-key=k --data-dir=/data
//	TYPESENSE_TEST_ADDR=127.0.0.1:$(docker port ts-test 8108 | cut -d: -f2) TYPESENSE_TEST_KEY=k go test ./internal/file/ -run Semantic
//
// The first run downloads the ts/all-MiniLM-L12-v2 model into the container.
func testTypesense(t *testing.T) *Typesense {
	t.Helper()
	addr, key := os.Getenv("TYPESENSE_TEST_ADDR"), os.Getenv("TYPESENSE_TEST_KEY")
	if addr == "" || key == "" {
		t.Skip("TYPESENSE_TEST_ADDR/TYPESENSE_TEST_KEY not set")
	}
	ts := newTypesense("http://"+addr, key)
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	ts.household = "household_search_" + suffix
	ts.files = "files_" + suffix
	slow := *ts
	slow.hc = &http.Client{Timeout: 5 * time.Minute} // model download + embedding
	embed := func(from ...string) map[string]any {
		return map[string]any{"name": embeddingField, "type": "float[]",
			"embed": map[string]any{"from": from, "model_config": map[string]any{"model_name": "ts/all-MiniLM-L12-v2"}}}
	}
	schemas := []map[string]any{
		{"name": ts.household, "default_sorting_field": "updated_at", "fields": []any{
			map[string]any{"name": "household_id", "type": "string", "facet": true},
			map[string]any{"name": "entity_type", "type": "string", "facet": true},
			map[string]any{"name": "entity_id", "type": "string"},
			map[string]any{"name": "title", "type": "string"},
			map[string]any{"name": "body", "type": "string"},
			map[string]any{"name": "tags", "type": "string[]", "facet": true},
			map[string]any{"name": "property_id", "type": "string", "optional": true, "facet": true},
			map[string]any{"name": "created_at", "type": "int64"},
			map[string]any{"name": "updated_at", "type": "int64"},
			embed("title", "body"),
		}},
		{"name": ts.files, "default_sorting_field": "created_at", "fields": []any{
			map[string]any{"name": "household_id", "type": "string", "facet": true},
			map[string]any{"name": "name", "type": "string"},
			map[string]any{"name": "extracted_text", "type": "string"},
			map[string]any{"name": "entity_type", "type": "string", "optional": true, "facet": true},
			map[string]any{"name": "entity_id", "type": "string", "optional": true},
			map[string]any{"name": "tags", "type": "string[]", "facet": true},
			map[string]any{"name": "created_at", "type": "int64"},
			map[string]any{"name": "file_id", "type": "string", "optional": true},
			map[string]any{"name": "chunk_index", "type": "int32", "optional": true},
			embed("name", "extracted_text"),
		}},
	}
	for _, s := range schemas {
		if err := slow.do(context.Background(), http.MethodPost, "/collections", s, nil); err != nil {
			t.Fatalf("create collection %s: %v", s["name"], err)
		}
		name := s["name"].(string)
		t.Cleanup(func() { _ = ts.do(context.Background(), http.MethodDelete, "/collections/"+name, nil, nil) })
	}
	return ts
}

// TestDocumentSemanticSearch is the ticket's acceptance test: "file this
// receipt" from pasted text makes a categorized document, and "car
// insurance" finds the car insurance documents by meaning (metadata and
// extracted text), scoped to the household and the caller's role, with no
// shared words for a substring match.
func TestDocumentSemanticSearch(t *testing.T) {
	ts := testTypesense(t)
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)
	d := newDocTools(t, pool, ts)
	plain := newDocTools(t, pool, nil) // no Typesense: substring only
	bg := context.Background()

	// "File this receipt" from pasted text.
	receipt := d.up.mustOK(t, ctx, map[string]any{"name": "costco-receipt.txt", "title": "Costco receipt", "category": "receipt",
		"text": "COSTCO WHOLESALE #482\n2026-03-01\nKIRKLAND PAPER TOWELS 19.99\nROTISSERIE CHICKEN 4.99\nTOTAL 142.50"}).Data
	if receipt.Category != "receipt" || receipt.TextStatus != "done" {
		t.Fatalf("receipt: %+v", receipt)
	}

	geico := d.up.mustOK(t, ctx, map[string]any{"name": "geico-dec-page.txt", "title": "Geico auto policy declarations",
		"category": "insurance", "doc_date": "2026-01-15", "expires_on": "2026-07-15",
		"text": "Named insured: the family. Liability limits 100/300/100. Collision deductible 500."}).Data
	dog := d.up.mustOK(t, ctx, map[string]any{"name": "rex-shots.txt", "title": "Dog vaccination record", "category": "medical",
		"text": "Rabies and distemper boosters given."}).Data
	// A scanned PDF with bland metadata: only its extracted text (indexed
	// by the worker into files, simulated here) says what it is.
	scan := d.up.mustOK(t, ctx, map[string]any{"name": "scan-0042.pdf",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 scan"))}).Data
	if err := ts.do(bg, http.MethodPost, "/collections/"+ts.files+"/documents?action=upsert", map[string]any{
		"id": scan.ID.String(), "file_id": scan.ID.String(), "chunk_index": 0, "household_id": hh.HouseholdID.String(),
		"name": "scan-0042.pdf", "extracted_text": "Progressive automobile insurance card for the Honda Odyssey minivan. Policy period 2026.",
		"tags": []string{}, "created_at": time.Now().Unix(),
	}, nil); err != nil {
		t.Fatal(err)
	}
	theirs := d.up.mustOK(t, otherCtx, map[string]any{"name": "their-auto.txt", "title": "Car insurance policy", "category": "insurance",
		"text": "Car insurance for their sedan."}).Data

	// A credential-attached insurance document, indexed as secret-document-<id>.
	secretID := seed(t, pool, `INSERT INTO secrets (household_id, entity_type, entity_id, encrypted_data, iv, name, secret_type)
		VALUES ($1, 'household', $1, 'enc', 'iv', 'Insurer portal', 'login') RETURNING id`, hh.HouseholdID)
	hidden := seed(t, pool, `WITH b AS (INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id)
		INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id, extracted_text, ocr_status, title, category)
		SELECT $1, b.id, 'portal.txt', 1, 'secret', $2, 'x', 'done', 'Auto policy portal login help', 'insurance' FROM b RETURNING id`,
		hh.HouseholdID, secretID)
	hr, err := loadDocument(bg, pool, hidden, hh.HouseholdID)
	if err != nil {
		t.Fatal(err)
	}
	ts.indexDocument(bg, hr)
	var probe map[string]any
	if err := ts.do(bg, http.MethodGet, "/collections/"+ts.household+"/documents/secret-document-"+hidden.String(), nil, &probe); err != nil {
		t.Fatalf("credential-attached document not indexed under the secret- id: %v", err)
	}

	names := func(items []wireItem) map[uuid.UUID]bool {
		m := map[uuid.UUID]bool{}
		for _, it := range items {
			m[it.ID] = true
		}
		return m
	}

	// No substring match: without Typesense, nothing.
	if items := plain.find.mustOK(t, ctx, map[string]any{"query": "car insurance"}).Items; len(items) != 0 {
		t.Fatalf("substring search should find nothing for 'car insurance', got %+v", items)
	}

	env := d.find.mustOK(t, ctx, map[string]any{"query": "car insurance"})
	got := names(env.Items)
	t.Logf("car insurance -> %+v", env.Items)
	if !got[geico.ID] || !got[scan.ID] {
		t.Errorf("by meaning: want the Geico policy and the scanned insurance card, got %+v", env.Items)
	}
	if got[dog.ID] || got[receipt.ID] {
		t.Errorf("unrelated documents matched: %+v", env.Items)
	}
	if got[theirs.ID] {
		t.Error("another household's document leaked")
	}
	if !got[hidden] {
		t.Errorf("owner should see the credential-attached policy: %+v", env.Items)
	}
	if len(env.Items) > 0 && env.Items[0].ID != geico.ID && env.Items[0].ID != scan.ID && env.Items[0].ID != hidden {
		t.Errorf("best hit is not an insurance document: %+v", env.Items[0])
	}
	for _, it := range env.Items {
		if it.Snippet != nil && len([]rune(*it.Snippet)) > snippetLen+2 {
			t.Errorf("snippet too long: %d", len([]rune(*it.Snippet)))
		}
	}

	// The user's own words work too.
	if !names(d.find.mustOK(t, ctx, map[string]any{"query": "find our car insurance"}).Items)[geico.ID] {
		t.Error("'find our car insurance' did not find the Geico policy")
	}

	// Filters apply to semantic hits (re-checked in Postgres).
	items := d.find.mustOK(t, ctx, map[string]any{"query": "car insurance", "category": "insurance", "from": "2026-01-01", "to": "2026-01-31"}).Items
	if len(items) != 1 || items[0].ID != geico.ID {
		t.Errorf("filtered: %+v", items)
	}

	// A viewer never sees the credential-attached one, even by meaning.
	vctx, _ := memberCtx(t, pool, hh, "viewer")
	if names(d.find.mustOK(t, vctx, map[string]any{"query": "car insurance"}).Items)[hidden] {
		t.Error("viewer saw a credential-attached document through semantic search")
	}

	// Update re-indexes the metadata; delete removes it from search.
	d.upd.mustOK(t, ctx, map[string]any{"document_id": dog.ID.String(), "title": "Rex rabies certificate"})
	var doc struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := ts.do(bg, http.MethodGet, "/collections/"+ts.household+"/documents/document-"+dog.ID.String(), nil, &doc); err != nil ||
		doc.Title != "Rex rabies certificate" || !strings.Contains(doc.Body, "Category: medical") || !strings.Contains(doc.Body, "File: rex-shots.txt") {
		t.Errorf("re-index after update: %+v %v", doc, err)
	}
	d.del.mustOK(t, ctx, map[string]any{"document_id": scan.ID.String()})
	if err := ts.do(bg, http.MethodGet, "/collections/"+ts.files+"/documents/"+scan.ID.String(), nil, nil); !isTSStatus(err, http.StatusNotFound) {
		t.Errorf("deleted document's text still in search: %v", err)
	}
	if err := ts.do(bg, http.MethodGet, "/collections/"+ts.household+"/documents/document-"+scan.ID.String(), nil, nil); !isTSStatus(err, http.StatusNotFound) {
		t.Errorf("deleted document's metadata still in search: %v", err)
	}
	if names(d.find.mustOK(t, ctx, map[string]any{"query": "car insurance"}).Items)[scan.ID] {
		t.Error("deleted document still found")
	}

	// Typesense down: document_find falls back to substring matching.
	broken := newDocTools(t, pool, newTypesense("http://127.0.0.1:1", "k"))
	if items := broken.find.mustOK(t, ctx, map[string]any{"query": "Geico"}).Items; len(items) != 1 || items[0].ID != geico.ID {
		t.Errorf("fallback: %+v", items)
	}
}
