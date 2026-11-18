package file

// Provenance fields on a file read (#4564, the UI half of #4941).
//
// The API now reports `text_verified` alongside `text_origin` and the
// uploader's name, so a file row can say honestly what its text is. These
// tests are pure: they need no database, because finalize() is the only thing
// that decides, and the rule is "never claim verbatim without a matching
// fingerprint".

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rawFile(body string) *File {
	sum := sha256.Sum256([]byte(body))
	return &File{
		Name:          "copay.pdf",
		ExtractedText: &body,
		TextOrigin:    TextOriginRaw,
		TextSHA256:    sum[:],
	}
}

func TestFinalizeRawBodyMatches(t *testing.T) {
	f := rawFile("Well visit copay — $35.00")
	f.finalize()
	if f.TextVerified == nil || !*f.TextVerified {
		t.Fatalf("a raw body that still matches its fingerprint must be verified, got %v", f.TextVerified)
	}
}

func TestFinalizeRawBodyChangedIsNotVerified(t *testing.T) {
	f := rawFile("Well visit copay — $35.00")
	// The R1 failure: the body was re-typed to say $5.
	changed := "Well visit copay — 5.00"
	f.ExtractedText = &changed
	f.finalize()
	if f.TextVerified == nil || *f.TextVerified {
		t.Fatalf("a re-typed body must report text_verified=false, got %v", f.TextVerified)
	}
}

func TestFinalizeRawBodyWithoutFingerprintClaimsNothing(t *testing.T) {
	body := "pasted text"
	f := &File{ExtractedText: &body, TextOrigin: TextOriginRaw}
	f.finalize()
	if f.TextVerified != nil {
		t.Fatalf("a raw body with no fingerprint must leave text_verified unset, got %v", *f.TextVerified)
	}
}

func TestFinalizeExtractedTextClaimsNothing(t *testing.T) {
	body := "OCR output"
	f := &File{ExtractedText: &body, TextOrigin: TextOriginExtracted}
	f.finalize()
	if f.TextVerified != nil {
		t.Fatalf("derived text must not carry text_verified, got %v", *f.TextVerified)
	}
}

func TestFinalizeNilAndEmpty(t *testing.T) {
	var nilFile *File
	nilFile.finalize() // must not panic

	empty := &File{TextOrigin: TextOriginRaw}
	empty.finalize()
	if empty.TextVerified != nil {
		t.Fatalf("a raw file with no body must not claim verification, got %v", *empty.TextVerified)
	}
}

func TestFinalizeAllCoversEveryFile(t *testing.T) {
	good := rawFile("ok")
	bad := rawFile("original")
	changed := "changed"
	bad.ExtractedText = &changed
	derived := &File{ExtractedText: strPtr("ocr"), TextOrigin: TextOriginExtracted}

	got := finalizeAll([]*File{good, bad, derived})
	if len(got) != 3 {
		t.Fatalf("finalizeAll returned %d files", len(got))
	}
	if got[0].TextVerified == nil || !*got[0].TextVerified {
		t.Error("good file should be verified")
	}
	if got[1].TextVerified == nil || *got[1].TextVerified {
		t.Error("changed file should not be verified")
	}
	if got[2].TextVerified != nil {
		t.Error("derived file should claim nothing")
	}
}

func TestFinalizeHashIsOverTheExactStoredBytes(t *testing.T) {
	// CRLF, double spaces, a tab and trailing spaces all count: the point of
	// the fingerprint is byte-for-byte equality, not "looks the same".
	body := "line one\r\nline  two\tend  "
	f := rawFile(body)
	f.finalize()
	if f.TextVerified == nil || !*f.TextVerified {
		t.Fatal("a body with CRLF and trailing spaces must verify against its own hash")
	}
	// A single trailing space removed flips it.
	trimmed := body[:len(body)-1]
	f.ExtractedText = &trimmed
	f.finalize()
	if f.TextVerified == nil || *f.TextVerified {
		t.Fatal("dropping one trailing space must flip text_verified to false")
	}
	if !bytes.Equal(f.TextSHA256, sha256Sum(body)) {
		t.Fatal("the stored fingerprint must still be the original body's")
	}
}

func sha256Sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

func strPtr(s string) *string { return &s }

// TestFileReadsCarryUploaderAndProvenance is the database half of #4564: a
// file row names who uploaded it (files.created_by, migration 050) and says
// whether its text is still the text that was filed. The war game's $5/$35
// bill is the case: a raw body that was re-typed after filing must not read as
// verified.
func TestFileReadsCarryUploaderAndProvenance(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	hh := household(t, pool)

	// A real user, because files.created_by is a foreign key.
	var userID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, dex_sub) VALUES ($1, 'Amber Sims', 'x', $2) RETURNING id`,
		"amber-"+uuid.NewString()[:8]+"@example.com", "CiQ"+uuid.NewString()+"EgVsb2NhbA").Scan(&userID))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })

	repo := NewRepo(pool)
	body := "Well visit copay — $35.00"
	sum := sha256.Sum256([]byte(body))
	f := &File{
		Name: "copay.txt", ContentType: "text/plain", EntityType: EntityTypeHousehold,
		EntityID: hh,
	}
	created, err := repo.CreateFile(ctx, hh, f, []byte(body))
	require.NoError(t, err)

	// The API's upload path stores bytes only; a raw body is written by the
	// MCP's document_upload (072). Set the provenance columns as that path
	// does, plus created_by, which the authorship trigger fills from the
	// request's session actor (050's own behaviour, tested there). What #4564
	// adds is the READ: the uploader's name and text_verified come back.
	_, err = pool.Exec(ctx,
		`UPDATE files SET extracted_text = $1, text_origin = 'raw', text_sha256 = $2, created_by = $3 WHERE id = $4`,
		body, sum[:], userID, created.ID)
	require.NoError(t, err)

	created, err = repo.GetFile(ctx, hh, created.ID)
	require.NoError(t, err)
	require.NotNil(t, created.UploadedByName, "the uploader's name must come back on a read")
	assert.Equal(t, "Amber Sims", *created.UploadedByName)
	require.NotNil(t, created.TextVerified)
	assert.True(t, *created.TextVerified, "an unaltered raw body is verified")

	// A list read carries both too.
	list, err := repo.ListFiles(ctx, hh, ListFilter{EntityType: EntityTypeHousehold, EntityID: hh})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.NotNil(t, list[0].UploadedByName)
	assert.Equal(t, "Amber Sims", *list[0].UploadedByName)
	require.NotNil(t, list[0].TextVerified)
	assert.True(t, *list[0].TextVerified)

	// Re-typing the body (the R1 failure) flips it, and the read says so.
	changed := "Well visit copay — 5.00"
	_, err = pool.Exec(ctx, `UPDATE files SET extracted_text = $1 WHERE id = $2`, changed, created.ID)
	require.NoError(t, err)

	got, err := repo.GetFile(ctx, hh, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.TextVerified)
	assert.False(t, *got.TextVerified, "a re-typed body must not read as verified")
	assert.Equal(t, TextOriginRaw, got.TextOrigin)
}
