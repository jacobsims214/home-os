package bill

// Tests for #4941 (war game R1 gap G1): a bill can point at the document it
// came from, its amount is compared with that document's own text, and a
// change to the amount is visible in the change feed with before and after.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/testutil"
)

// r1Paste is the war game R1 paste: the amount the family actually owes.
const r1Paste = "Riverside Pediatrics\nWell visit copay — $35.00\nDue 12/10/2026"

// seedRawDocument files a document the way document_upload does for pasted
// text: the body verbatim, text_origin 'raw', and the fingerprint.
func seedRawDocument(t *testing.T, pool *pgxpool.Pool, householdID uuid.UUID, name, text string) uuid.UUID {
	t.Helper()
	var blobID, fileID uuid.UUID
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `INSERT INTO file_blobs (data, size) VALUES ($1, $2) RETURNING id`,
		[]byte(text), len(text)).Scan(&blobID); err != nil {
		t.Fatalf("insert blob: %v", err)
	}
	sum := sha256.Sum256([]byte(text))
	if err := pool.QueryRow(ctx,
		`INSERT INTO files (household_id, blob_id, name, content_type, size, entity_type, entity_id,
		                    extracted_text, ocr_status, title, category, text_origin, text_sha256)
		 VALUES ($1, $2, $3, 'text/plain; charset=utf-8', $4, 'household', $1, $5, 'done', $6, 'medical', 'raw', $7)
		 RETURNING id`,
		householdID, blobID, name, len(text), text, "Riverside Pediatrics statement", sum[:],
	).Scan(&fileID); err != nil {
		t.Fatalf("insert document: %v", err)
	}
	return fileID
}

// TestBillCreate_SourceDocumentLink is the ticket's acceptance case: a bill
// linked to a source document exposes the link, and the link is the same
// document on the database row.
func TestBillCreate_SourceDocumentLink(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)

	_, _, create := NewBillCreateTool(pool)
	_, _, find := NewBillFindTool(pool)
	docID := seedRawDocument(t, pool, hh.HouseholdID, "riverside-copay.txt", r1Paste)

	res, text := callTool(t, ctx, create, "bill_create", map[string]any{
		"name": "Copay — Nora well visit", "amount": 35.0, "due_day": 10,
		"category": "Medical", "source_document_id": docID.String(),
	})
	if res.IsError {
		t.Fatalf("bill_create: %s", text)
	}
	var b billData
	if err := unmarshalData(text, &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b.SourceDocumentID == nil || *b.SourceDocumentID != docID.String() {
		t.Fatalf("source_document_id = %v, want %s", b.SourceDocumentID, docID)
	}
	if b.SourceDocumentTitle == nil || *b.SourceDocumentTitle != "Riverside Pediatrics statement" {
		t.Errorf("source_document_title = %v", b.SourceDocumentTitle)
	}

	// Stored, and readable back through bill_find.
	var stored uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT source_document_id FROM bills WHERE id = $1`, b.ID).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != docID {
		t.Errorf("stored source_document_id = %s, want %s", stored, docID)
	}
	_, foundText := callTool(t, ctx, find, "bill_find", map[string]any{"id": b.ID})
	var found []billData
	if err := unmarshalData(foundText, &found); err != nil {
		t.Fatalf("decode find: %v", err)
	}
	if len(found) != 1 || found[0].SourceDocumentID == nil || *found[0].SourceDocumentID != docID.String() {
		t.Errorf("bill_find lost the link: %+v", found)
	}
	if found[0].SourceDocumentTitle == nil || *found[0].SourceDocumentTitle != "Riverside Pediatrics statement" {
		t.Errorf("bill_find source_document_title = %v", found[0].SourceDocumentTitle)
	}

	// Another family's document is refused, not silently dropped.
	_, other := testutil.SeedHousehold(t, pool)
	theirDoc := seedRawDocument(t, pool, other.HouseholdID, "theirs.txt", r1Paste)
	res, text = callTool(t, ctx, create, "bill_create", map[string]any{
		"name": "Their bill", "amount": 35.0, "due_day": 11, "source_document_id": theirDoc.String(),
	})
	if !res.IsError || !strings.Contains(text, "source_document_id") || !strings.Contains(text, "this family") {
		t.Errorf("another family's document should be refused: %s", text)
	}
}

// TestBillCreate_AmountDisagreesWithSource is the R1 failure at the write:
// filing 5 against a source that says $35.00 must say so and quote the line,
// so the mistake cannot pass as checked.
func TestBillCreate_AmountDisagreesWithSource(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)

	_, _, create := NewBillCreateTool(pool)
	docID := seedRawDocument(t, pool, hh.HouseholdID, "riverside-copay.txt", r1Paste)

	// The R1 bill: 5 instead of 35.
	res, text := callTool(t, ctx, create, "bill_create", map[string]any{
		"name": "Copay — Nora well visit", "amount": 5.0, "due_day": 10,
		"source_document_id": docID.String(),
	})
	if res.IsError {
		t.Fatalf("a disagreement is reported, not refused: %s", text)
	}
	var env struct {
		Summary string   `json:"summary"`
		Next    []string `json:"next"`
	}
	if err := unmarshalEnvelope(text, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(env.Summary, "$5.00") || !strings.Contains(env.Summary, "$35.00") ||
		!strings.Contains(env.Summary, "Well visit copay") {
		t.Errorf("summary should name both amounts and quote the source line: %q", env.Summary)
	}
	if !strings.Contains(env.Summary, "is not in") {
		t.Errorf("summary should say the amount is not in the document: %q", env.Summary)
	}
	if !strings.Contains(strings.Join(env.Next, " "), "document_get") {
		t.Errorf("next should point at the source document: %v", env.Next)
	}

	// The matching amount is cited, not warned about.
	_, text = callTool(t, ctx, create, "bill_create", map[string]any{
		"name": "Copay — right amount", "amount": 35.0, "due_day": 12,
		"source_document_id": docID.String(),
	})
	if err := unmarshalEnvelope(text, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(env.Summary, "matches") || strings.Contains(env.Summary, "is not in") {
		t.Errorf("summary should cite the match: %q", env.Summary)
	}
}

// TestBillAmountChange_IsAuditable is the ticket's third acceptance case: a
// change to amount through bill_update is traceable — the change feed shows
// who changed it, when, and the before and after values.
func TestBillAmountChange_IsAuditable(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)

	_, _, create := NewBillCreateTool(pool)
	_, _, update := NewBillUpdateTool(pool)
	docID := seedRawDocument(t, pool, hh.HouseholdID, "riverside-copay.txt", r1Paste)

	b := mustBill(t, ctx, create, map[string]any{
		"name": "Copay — Nora well visit", "amount": 5.0, "due_day": 10,
		"source_document_id": docID.String(),
	})

	// The correction: $5 -> $35, with the source document linked.
	res, text := callTool(t, ctx, update, "bill_update", map[string]any{
		"bill_id": b.ID, "amount": 35.0,
	})
	if res.IsError {
		t.Fatalf("bill_update: %s", text)
	}
	var corrected billData
	if err := unmarshalData(text, &corrected); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if corrected.Amount == nil || *corrected.Amount != 35.0 {
		t.Fatalf("amount = %v, want 35", corrected.Amount)
	}
	if corrected.SourceDocumentID == nil || *corrected.SourceDocumentID != docID.String() {
		t.Errorf("the link should survive an amount change: %v", corrected.SourceDocumentID)
	}

	// The activity log holds the before and after, and the actor.
	var action string
	var changed []string
	var changes []byte
	var actor uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT action, changed_fields, changes, actor_user_id FROM activity_log
		  WHERE entity_type = 'bill' AND entity_id = $1 AND action = 'update'
		  ORDER BY id DESC LIMIT 1`, b.ID,
	).Scan(&action, &changed, &changes, &actor); err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if !strings.Contains(fmt.Sprint(changed), "amount") {
		t.Errorf("changed_fields = %v", changed)
	}
	if actor != hh.UserID {
		t.Errorf("actor = %s, want %s", actor, hh.UserID)
	}
	want := `{"amount": {"from": 5.00, "to": 35.00}}`
	if got := string(changes); normaliseJSON(got) != normaliseJSON(want) {
		t.Errorf("changes = %s, want %s", got, want)
	}

	// A change to something the log keeps no values for still logs no values:
	// the correction is traceable, note bodies are not.
	if _, err := pool.Exec(context.Background(), `UPDATE bills SET notes = 'card ending 4242' WHERE id = $1`, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT changes FROM activity_log WHERE entity_type = 'bill' AND entity_id = $1 ORDER BY id DESC LIMIT 1`, b.ID,
	).Scan(&changes); err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if changes != nil {
		t.Errorf("a notes-only change must carry no values, got %s", changes)
	}
}

// mustBill creates a bill and returns its output.
func mustBill(t *testing.T, ctx context.Context, create toolHandler, args map[string]any) billData {
	t.Helper()
	res, text := callTool(t, ctx, create, "bill_create", args)
	if res.IsError {
		t.Fatalf("bill_create: %s", text)
	}
	var b billData
	if err := unmarshalData(text, &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return b
}

// unmarshalEnvelope decodes a result's summary and next hints.
func unmarshalEnvelope(text string, v any) error {
	var env wireEnvelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		return err
	}
	b, err := json.Marshal(map[string]any{"summary": env.Summary, "next": env.Next})
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// normaliseJSON re-encodes JSON so key order and spacing never fail a compare.
func normaliseJSON(s string) string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return "invalid:" + s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "invalid:" + s
	}
	return string(b)
}
