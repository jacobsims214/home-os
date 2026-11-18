package search

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// sentences returns n distinct numbered sentences.
func sentences(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "Sentence number %d talks about item%d in some detail.\n\n", i, i)
	}
	return b.String()
}

func TestChunk(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		wantChunks    int // -1: more than one, exact count not checked
		wantTruncated bool
	}{
		{name: "empty", text: "", wantChunks: 0},
		{name: "whitespace only", text: " \n\t ", wantChunks: 0},
		{name: "short", text: "Home insurance policy\n\nrenewal 2026", wantChunks: 1},
		{name: "exactly max", text: strings.Repeat("a", MaxChunkChars), wantChunks: 1},
		{name: "long prose", text: sentences(200), wantChunks: -1},
		{name: "one huge word", text: strings.Repeat("x", 5*MaxChunkChars), wantChunks: -1},
		{name: "multibyte no spaces", text: strings.Repeat("é日", 3*MaxChunkChars), wantChunks: -1},
		{name: "over the cap", text: sentences(MaxChunks * 20), wantChunks: MaxChunks, wantTruncated: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chunks, truncated := Chunk(tc.text)
			if truncated != tc.wantTruncated {
				t.Errorf("truncated = %v, want %v", truncated, tc.wantTruncated)
			}
			switch {
			case tc.wantChunks >= 0 && len(chunks) != tc.wantChunks:
				t.Fatalf("got %d chunks, want %d", len(chunks), tc.wantChunks)
			case tc.wantChunks < 0 && len(chunks) < 2:
				t.Fatalf("got %d chunks, want several", len(chunks))
			}
			for i, c := range chunks {
				if len(c) > MaxChunkChars {
					t.Errorf("chunk %d is %d bytes, max %d", i, len(c), MaxChunkChars)
				}
				if c == "" || c != strings.TrimSpace(c) {
					t.Errorf("chunk %d is empty or untrimmed: %q", i, c)
				}
				if !utf8.ValidString(c) {
					t.Errorf("chunk %d is not valid UTF-8", i)
				}
				if strings.ContainsAny(c, "\n\t") || strings.Contains(c, "  ") {
					t.Errorf("chunk %d has uncollapsed whitespace", i)
				}
			}
		})
	}
}

// Every sentence of a long text is wholly inside at least one chunk (thanks
// to sentence breaks and overlap), so no content is lost to the boundaries.
func TestChunkCoversEverySentence(t *testing.T) {
	const n = 300
	chunks, truncated := Chunk(sentences(n))
	if truncated {
		t.Fatal("unexpected truncation")
	}
	for i := range n {
		s := fmt.Sprintf("Sentence number %d talks about item%d in some detail.", i, i)
		found := false
		for _, c := range chunks {
			if strings.Contains(c, s) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("sentence %d not wholly inside any chunk", i)
		}
	}
	// Chunks end on sentence boundaries when one is available.
	for i, c := range chunks[:len(chunks)-1] {
		if !strings.HasSuffix(c, ".") {
			t.Errorf("chunk %d does not end at a sentence: ...%q", i, c[max(0, len(c)-20):])
		}
	}
}

func TestChunkDocuments(t *testing.T) {
	doc := FileDocument{ID: "f1", HouseholdID: "h1", Name: "Policy.pdf", Tags: nil, CreatedAt: 5, RowVersion: 9}

	docs := chunkDocuments(doc, nil)
	if len(docs) != 1 || docs[0].ID != "f1" || docs[0].ChunkCount != 1 || docs[0].ExtractedText != "" {
		t.Fatalf("no chunks: got %+v, want one empty chunk with id f1", docs)
	}
	b, _ := json.Marshal(docs[0])
	if !strings.Contains(string(b), `"tags":[]`) || strings.Contains(string(b), "entity_id") {
		t.Errorf("JSON = %s, want tags [] and no entity fields for an unattached file", b)
	}

	docs = chunkDocuments(doc, []string{"a", "b", "c"})
	for i, d := range docs {
		if d.ID != ChunkID("f1", i) || d.FileID != "f1" || d.ChunkIndex != i || d.ChunkCount != 3 ||
			d.Name != "Policy.pdf" || d.RowVersion != 9 {
			t.Errorf("chunk %d = %+v", i, d)
		}
	}
	if docs[0].ID != "f1" || docs[2].ID != "f1_2" {
		t.Errorf("ids = %s, %s; want f1, f1_2", docs[0].ID, docs[2].ID)
	}
}

func TestEmbeddingModelIsLocal(t *testing.T) {
	if !strings.HasPrefix(EmbeddingModel, "ts/") {
		t.Fatalf("EmbeddingModel %q is not a Typesense built-in local model; family data must not leave the cluster", EmbeddingModel)
	}
}

// The embedding field must be byte-for-byte the definition the API uses for
// files (apps/api/internal/search: embeddingFieldDef(filesEmbedFrom)), so the
// schema is identical whichever service creates the collection.
func TestEmbeddingFieldMatchesAPI(t *testing.T) {
	b, err := json.Marshal(embeddingFieldDef())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":"embedding","type":"float[]","embed":{"from":["name","extracted_text"],"model_config":{"model_name":"ts/all-MiniLM-L12-v2"}}}`
	if string(b) != want {
		t.Fatalf("embedding field = %s\nwant %s", b, want)
	}
}

func TestSchemaMigration(t *testing.T) {
	legacy := &collectionSchema{Fields: []field{{Name: "id", Type: "string"}, {Name: "name", Type: "string"}}}
	if got := missingChunkFields(legacy); len(got) != 4 {
		t.Errorf("legacy schema: %d missing chunk fields, want 4", len(got))
	}
	if got := embeddingMigration(legacy); len(got) != 1 || got[0].Embed == nil {
		t.Errorf("legacy schema: migration = %+v, want one embed field", got)
	}

	placeholder := &collectionSchema{Fields: []field{{Name: "embedding", Type: "float[]"}}}
	if got := embeddingMigration(placeholder); len(got) != 2 || !got[0].Drop || got[1].Embed == nil {
		t.Errorf("placeholder: migration = %+v, want drop + embed", got)
	}

	current := filesSchema(true)
	if got := missingChunkFields(&current); len(got) != 0 {
		t.Errorf("current schema: missing %+v", got)
	}
	if got := embeddingMigration(&current); got != nil {
		t.Errorf("current schema: migration = %+v, want nil", got)
	}
}

func TestIsPermanent(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{&DocumentError{DocID: "x", Message: "bad"}, true},
		{fmt.Errorf("wrapped: %w", &DocumentError{}), true},
		{&statusError{Status: 400}, true},
		{&statusError{Status: 404}, false},
		{&statusError{Status: 429}, false},
		{&statusError{Status: 503}, false},
		{fmt.Errorf("dial tcp: connection refused"), false},
	}
	for _, tc := range tests {
		if got := IsPermanent(tc.err); got != tc.want {
			t.Errorf("IsPermanent(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
