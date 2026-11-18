package file

// Tests for #4941 (war game R1 gap G1): a document uploaded from a person's
// own text must store that text byte for byte and must be able to prove it
// afterwards. In R1 a parent pasted "Well visit copay — $35.00", the document
// body was re-typed as "Well visit copay — 5.00", and a later verification
// turn "confirmed" the amount because every artifact carried the same
// mistake. Nothing in storage held the original text.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/testutil"
)

// r1Paste is the text from the R1 war game turn (T9), em dash and all, with
// the kind of untidy whitespace a real paste carries. Every byte of it must
// survive the round trip.
const r1Paste = "Riverside Pediatrics\r\nWell visit copay — $35.00 \n  Paid to date:\t$0.00\r\nVisit  11/10/2026\n"

// fullText pages a document's text out the way a client does, so the test
// sees exactly what document_get would deliver end to end.
func fullText(t *testing.T, c caller[wireDoc], ctx context.Context, id uuid.UUID) string {
	t.Helper()
	var out strings.Builder
	offset := 0
	for {
		env := c.mustOK(t, ctx, map[string]any{"document_id": id.String(), "text_offset": offset})
		p := env.Data.Text
		if p == nil {
			t.Fatalf("no text page at offset %d", offset)
		}
		out.WriteString(p.Content)
		if !p.HasMore {
			return out.String()
		}
		offset = *p.NextOffset
	}
}

// TestDocumentUpload_RawTextStoredVerbatim is the ticket's acceptance case:
// pasting "Well visit copay — $35.00" as a document and reading it back
// returns the original text verbatim, byte for byte.
func TestDocumentUpload_RawTextStoredVerbatim(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)

	upName, upTool, upH := NewDocumentUploadTool(pool, nil)
	up := newCaller[wireDoc](t, upName, upTool, upH)
	getName, getTool, getH := NewDocumentGetTool(pool)
	get := newCaller[wireDoc](t, getName, getTool, getH)

	env := up.mustOK(t, ctx, map[string]any{
		"name": "riverside-statement.txt", "content_text": r1Paste,
		"title": "Riverside Pediatrics statement", "category": "medical", "doc_date": "2026-11-10",
	})
	d := env.Data
	if d.TextOrigin != textOriginRaw {
		t.Fatalf("text_origin = %q, want %q", d.TextOrigin, textOriginRaw)
	}
	if d.TextChars != len([]rune(r1Paste)) {
		t.Errorf("text_chars = %d, want %d", d.TextChars, len([]rune(r1Paste)))
	}
	if d.Text != nil {
		t.Error("upload must not echo the text")
	}
	if !strings.Contains(env.Summary, "stored verbatim") {
		t.Errorf("summary should say the text is verbatim: %q", env.Summary)
	}

	// What storage holds: the body, the blob bytes and the fingerprint.
	var body string
	var blob, hash []byte
	var origin string
	if err := pool.QueryRow(context.Background(),
		`SELECT f.extracted_text, b.data, f.text_sha256, f.text_origin
		   FROM files f JOIN file_blobs b ON b.id = f.blob_id WHERE f.id = $1`, d.ID,
	).Scan(&body, &blob, &hash, &origin); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if body != r1Paste {
		t.Errorf("stored body differs from the paste:\n got %q\nwant %q", body, r1Paste)
	}
	if string(blob) != r1Paste {
		t.Errorf("stored bytes differ from the paste:\n got %q\nwant %q", blob, r1Paste)
	}
	if origin != textOriginRaw {
		t.Errorf("stored text_origin = %q, want %q", origin, textOriginRaw)
	}
	want := sha256.Sum256([]byte(r1Paste))
	if string(hash) != string(want[:]) {
		t.Errorf("stored text_sha256 = %x, want %x", hash, want)
	}

	// What document_get delivers: the same text over as many pages as it
	// takes, plus the fingerprint and the verified flag.
	got := get.mustOK(t, ctx, map[string]any{"document_id": d.ID.String()})
	if got.Data.TextSHA256 == nil || *got.Data.TextSHA256 != fmt.Sprintf("%x", want) {
		t.Errorf("text_sha256 = %v, want %x", got.Data.TextSHA256, want)
	}
	if got.Data.TextVerified == nil || !*got.Data.TextVerified {
		t.Errorf("text_verified = %v, want true", got.Data.TextVerified)
	}
	if got.Data.TextOrigin != textOriginRaw {
		t.Errorf("text_origin = %q", got.Data.TextOrigin)
	}
	if full := fullText(t, get, ctx, d.ID); full != r1Paste {
		t.Errorf("document_get text differs from the paste:\n got %q\nwant %q", full, r1Paste)
	}
}

// TestDocumentUpload_RawTextAliases covers the accepted spellings: a small
// model reaching for text, raw_text or body files the same verbatim body, and
// giving both contents is refused rather than guessed.
func TestDocumentUpload_RawTextAliases(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)

	upName, upTool, upH := NewDocumentUploadTool(pool, nil)
	up := newCaller[wireDoc](t, upName, upTool, upH)
	getName, getTool, getH := NewDocumentGetTool(pool)
	get := newCaller[wireDoc](t, getName, getTool, getH)

	for _, key := range []string{"content_text", "text", "raw_text", "body"} {
		t.Run(key, func(t *testing.T) {
			// One distinct name per alias so the 10-minute loop guard cannot
			// hand back the previous document.
			env := up.mustOK(t, ctx, map[string]any{"name": "alias-" + key + ".txt", key: r1Paste})
			if env.Data.TextOrigin != textOriginRaw || env.Data.TextChars != len([]rune(r1Paste)) {
				t.Errorf("%s: %+v", key, env.Data)
			}
			if full := fullText(t, get, ctx, env.Data.ID); full != r1Paste {
				t.Errorf("%s: text %q", key, full)
			}
		})
	}

	runErrCases(t, up, []errCase{
		{"text and bytes", ctx, map[string]any{"name": "a.txt", "content_text": r1Paste, "content_base64": "eA=="},
			"content_text", "both given"},
		{"blank text", ctx, map[string]any{"name": "a.txt", "content_text": "   \n\t "},
			"content_text", "no content"},
		{"NUL byte", ctx, map[string]any{"name": "a.txt", "content_text": "copay $35\x00.00"},
			"content_text", "NUL"},
	})
}

// TestDocumentUpload_ExtractedTextHasNoFingerprint is the other half of the
// rule: text the worker reads out of a file's bytes is derived, so it claims
// no origin and no fingerprint, and document_get reports neither.
func TestDocumentUpload_ExtractedTextHasNoFingerprint(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)

	upName, upTool, upH := NewDocumentUploadTool(pool, nil)
	up := newCaller[wireDoc](t, upName, upTool, upH)
	getName, getTool, getH := NewDocumentGetTool(pool)
	get := newCaller[wireDoc](t, getName, getTool, getH)

	d := up.mustOK(t, ctx, map[string]any{
		"name": "statement.png", "content_base64": "iVBORw0KGgo=", "content_type": "image/png",
	}).Data
	if d.TextOrigin != textOriginExtracted {
		t.Errorf("text_origin = %q, want %q", d.TextOrigin, textOriginExtracted)
	}
	// The worker writes the text, as it would once OCR finishes.
	if _, err := pool.Exec(context.Background(),
		`UPDATE files SET extracted_text = $2, ocr_status = 'done' WHERE id = $1`,
		d.ID, "Well visit copay 35.00",
	); err != nil {
		t.Fatal(err)
	}
	got := get.mustOK(t, ctx, map[string]any{"document_id": d.ID.String()})
	if got.Data.TextSHA256 != nil || got.Data.TextVerified != nil {
		t.Errorf("derived text must carry no fingerprint: %v %v", got.Data.TextSHA256, got.Data.TextVerified)
	}
	if got.Data.TextOrigin != textOriginExtracted || !strings.Contains(got.Data.TextNote, "extracted") {
		t.Errorf("origin/note: %q %q", got.Data.TextOrigin, got.Data.TextNote)
	}
}

// TestDocumentGet_DetectsAChangedBody proves the fingerprint does the job it
// exists for: once the stored body is no longer the text that was filed (an
// edit, a re-typed copy, a tool written without going through upload),
// document_get says so instead of calling it the source.
func TestDocumentGet_DetectsAChangedBody(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)

	upName, upTool, upH := NewDocumentUploadTool(pool, nil)
	up := newCaller[wireDoc](t, upName, upTool, upH)
	getName, getTool, getH := NewDocumentGetTool(pool)
	get := newCaller[wireDoc](t, getName, getTool, getH)

	d := up.mustOK(t, ctx, map[string]any{"name": "copay.txt", "content_text": r1Paste}).Data
	// The corruption R1 suffered, applied to the body after the fact.
	if _, err := pool.Exec(context.Background(),
		`UPDATE files SET extracted_text = replace(extracted_text, '$35.00', '$5.00') WHERE id = $1`, d.ID,
	); err != nil {
		t.Fatal(err)
	}
	got := get.mustOK(t, ctx, map[string]any{"document_id": d.ID.String()})
	if got.Data.TextVerified == nil || *got.Data.TextVerified {
		t.Fatalf("text_verified = %v, want false after the body changed", got.Data.TextVerified)
	}
	if got.Data.TextSHA256 == nil {
		t.Error("the filed fingerprint should still be reported")
	}
	if !strings.Contains(fullText(t, get, ctx, d.ID), "$5.00") {
		t.Fatal("the test did not change the body")
	}
	if !strings.Contains(strings.Join(got.Next, " "), "no longer matches") {
		t.Errorf("next should warn the text changed: %v", got.Next)
	}
}

// dbHasRawText asserts the provenance columns of one document.
func dbHasRawText(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, want string) {
	t.Helper()
	var body, origin string
	var hash []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT extracted_text, text_origin, text_sha256 FROM files WHERE id = $1`, id,
	).Scan(&body, &origin, &hash); err != nil {
		t.Fatalf("read back: %v", err)
	}
	sum := sha256.Sum256([]byte(want))
	if body != want || origin != textOriginRaw || string(hash) != string(sum[:]) {
		t.Errorf("stored provenance = %q %q %x", body, origin, hash)
	}
}
