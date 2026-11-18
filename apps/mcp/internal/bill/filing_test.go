package bill

// Tests for #5306: a bill with no document must not be reportable as filed.
//
// The reported turn: "File the dog's vet bill $82.50 due 12/05, add a
// reminder" → "Done, Amber — all filed in home-os…", while document_find
// returned 0 documents and the bill's source_document_id was null. No tool
// had claimed filing; the result was simply silent about documents, and a
// successful bill_create in a "file this" turn was read as filing. These
// tests pin the signal that silence replaced: document_filed in every bill
// payload, and a summary/hint that say when nothing was filed.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/testutil"
)

// countDocuments is what the reporter did by hand: search Documents.
func countDocuments(t *testing.T, pool *pgxpool.Pool, householdID any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM files WHERE household_id = $1`, householdID).Scan(&n); err != nil {
		t.Fatalf("count documents: %v", err)
	}
	return n
}

// TestBillCreateWithoutDocumentSaysSo is the ticket's case: the vet bill is
// created from a sentence, no document is filed, and the result must say so
// instead of leaving a "filed" reading open.
func TestBillCreateWithoutDocumentSaysSo(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)

	_, _, create := NewBillCreateTool(pool)

	res, text := callTool(t, ctx, create, "bill_create", map[string]any{
		"name": "Vet bill — dog", "amount": 82.50, "due_date": "2026-12-05", "frequency": "once",
	})
	if res.IsError {
		t.Fatalf("bill_create: %s", text)
	}
	var b billData
	if err := unmarshalData(text, &b); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// 1. The payload carries the filing state, and it is false.
	if b.DocumentFiled {
		t.Errorf("document_filed = true with no source document")
	}
	if b.SourceDocumentID != nil {
		t.Errorf("source_document_id = %v, want null", b.SourceDocumentID)
	}

	// 2. The summary says nothing is filed, in words the model cannot read as
	// success. The old summary was "Created bill Vet bill — dog, $82.50 due …"
	// and said nothing about documents at all.
	env := decodeEnvelope(t, text)
	if !strings.Contains(env.Summary, "No document is filed") || !strings.Contains(env.Summary, "not in Documents") {
		t.Errorf("summary must say no document is filed: %q", env.Summary)
	}

	// 3. The hint names both steps that would file one, with the arguments
	// they need: a hint that only says "file it" is the same gap elsewhere.
	next := strings.Join(env.Next, " ")
	if !strings.Contains(next, "document_upload content_text=") ||
		!strings.Contains(next, "bill_update bill_id="+b.ID) ||
		!strings.Contains(next, "source_document_id=") {
		t.Errorf("next must point at document_upload and bill_update: %v", env.Next)
	}

	// 4. And the ground truth still says nothing was filed — the claim the
	// assistant made was false, and the tool now reports the same fact.
	if n := countDocuments(t, pool, hh.HouseholdID); n != 0 {
		t.Errorf("documents = %d, want 0", n)
	}

	// The same state is readable through bill_find, so a later turn cannot
	// reason its way back to "filed" from a bare success.
	_, _, find := NewBillFindTool(pool)
	_, found := callTool(t, ctx, find, "bill_find", map[string]any{"id": b.ID})
	var rows []billData
	if err := unmarshalData(found, &rows); err != nil {
		t.Fatalf("decode find: %v", err)
	}
	if len(rows) != 1 || rows[0].DocumentFiled {
		t.Errorf("bill_find should report document_filed=false: %+v", rows)
	}
}

// TestBillCreateWithDocumentIsFiled is the other half: when the bill really
// is linked to the document it came from, the result says so and does not
// warn about filing.
func TestBillCreateWithDocumentIsFiled(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)

	_, _, create := NewBillCreateTool(pool)
	docID := seedRawDocument(t, pool, hh.HouseholdID, "vet-bill.txt", "Muddy Paws Vet\nExam — $82.50\nDue 12/05/2026")

	res, text := callTool(t, ctx, create, "bill_create", map[string]any{
		"name": "Vet bill — dog", "amount": 82.50, "due_date": "2026-12-05", "frequency": "once",
		"source_document_id": docID.String(),
	})
	if res.IsError {
		t.Fatalf("bill_create: %s", text)
	}
	var b billData
	if err := unmarshalData(text, &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !b.DocumentFiled {
		t.Errorf("document_filed = false with source_document_id set")
	}
	env := decodeEnvelope(t, text)
	if strings.Contains(env.Summary, "No document is filed") {
		t.Errorf("a linked bill must not warn about filing: %q", env.Summary)
	}
	// seedRawDocument titles every document "Riverside Pediatrics statement".
	if !strings.Contains(env.Summary, "Riverside Pediatrics statement") {
		t.Errorf("summary should name the document it is filed with: %q", env.Summary)
	}
	if next := strings.Join(env.Next, " "); strings.Contains(next, "document_upload") {
		t.Errorf("a filed bill must not be told to file: %v", env.Next)
	}
	if n := countDocuments(t, pool, hh.HouseholdID); n != 1 {
		t.Errorf("documents = %d, want 1", n)
	}
}

// TestFilingCopy pins the wording without a database: the two notes are
// mutually exclusive and neither is ever empty for the wrong state, so no
// bill payload can be silent about documents.
func TestFilingCopy(t *testing.T) {
	t.Parallel()
	title := "Muddy Paws Vet statement"
	unfiled := billData{ID: "b1"}
	filed := billData{ID: "b1", SourceDocumentID: strptr("d1"), SourceDocumentTitle: &title, DocumentFiled: true}
	linkedNoTitle := billData{ID: "b1", SourceDocumentID: strptr("d1"), DocumentFiled: true}

	tests := []struct {
		name        string
		b           billData
		wantNote    bool
		wantHint    bool
		wantLinked  bool
		notInNote   string
		inWantHints string
	}{
		{"unfiled", unfiled, true, true, false, "not in Documents", "document_upload"},
		{"filed", filed, false, false, true, "", ""},
		{"filed without a title", linkedNoTitle, false, false, true, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filingNote(tt.b) != ""; got != tt.wantNote {
				t.Errorf("filingNote = %q, want present=%v", filingNote(tt.b), tt.wantNote)
			}
			if got := filingHint(tt.b) != ""; got != tt.wantHint {
				t.Errorf("filingHint = %q, want present=%v", filingHint(tt.b), tt.wantHint)
			}
			if got := linkedNote(tt.b) != ""; got != tt.wantLinked {
				t.Errorf("linkedNote = %q, want present=%v", linkedNote(tt.b), tt.wantLinked)
			}
			if tt.notInNote != "" && !strings.Contains(filingNote(tt.b), tt.notInNote) {
				t.Errorf("filingNote %q should say %q", filingNote(tt.b), tt.notInNote)
			}
			if tt.inWantHints != "" && !strings.Contains(filingHint(tt.b), tt.inWantHints) {
				t.Errorf("filingHint %q should name %q", filingHint(tt.b), tt.inWantHints)
			}
			// DocumentFiled is derived, never set independently of the link.
			if (tt.b.SourceDocumentID != nil) != tt.b.DocumentFiled {
				t.Errorf("document_filed must follow source_document_id")
			}
		})
	}
}

// strptr is a *string for the copy test.
func strptr(s string) *string { return &s }
