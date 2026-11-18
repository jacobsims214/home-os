package search

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/config"
)

func TestEmbeddingMigration(t *testing.T) {
	plain := field{Name: EmbeddingField, Type: "float[]", NumDim: 384, Optional: true}
	other := field{Name: EmbeddingField, Type: "float[]", Embed: &embedConfig{
		From: []string{"title"}, ModelConfig: modelConfig{ModelName: "ts/e5-small"}}}

	tests := []struct {
		name     string
		fields   []field
		wantNil  bool
		wantDrop bool
	}{
		{name: "already auto-embedded", fields: []field{{Name: "title"}, embeddingFieldDef(householdEmbedFrom)}, wantNil: true},
		{name: "no embedding field adds it", fields: []field{{Name: "name"}}},
		{name: "plain float[] placeholder is dropped and re-added", fields: []field{{Name: "title"}, plain}, wantDrop: true},
		{name: "different model is replaced", fields: []field{other}, wantDrop: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := embeddingMigration(&collectionSchema{Name: "c", Fields: tt.fields}, householdEmbedFrom)
			if tt.wantNil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			last := got.Fields[len(got.Fields)-1]
			assert.Equal(t, EmbeddingField, last.Name)
			require.NotNil(t, last.Embed)
			assert.Equal(t, EmbeddingModel, last.Embed.ModelConfig.ModelName)
			assert.Equal(t, householdEmbedFrom, last.Embed.From)
			if tt.wantDrop {
				require.Len(t, got.Fields, 2)
				assert.True(t, got.Fields[0].Drop)
			} else {
				assert.Len(t, got.Fields, 1)
			}
		})
	}
}

func TestEmbeddingModelIsLocal(t *testing.T) {
	// Privacy: only Typesense's built-in in-process models (ts/ namespace)
	// are allowed. Remote providers (openai/, google/, ...) would send family
	// data off-cluster.
	assert.True(t, strings.HasPrefix(EmbeddingModel, "ts/"), "embedding model must be a local ts/ model")
}

func TestSearchParams(t *testing.T) {
	filter := buildFilter("h1", SearchFilters{EntityTypes: []string{"asset", "note"}, PropertyID: "p1"})
	assert.Equal(t, "household_id:=`h1` && entity_type:=[`asset`,`note`] && property_id:=`p1`", filter)

	hybrid := searchParams("air filter", "title,body", filter, true)
	assert.Equal(t, "title,body,embedding", hybrid.Get("query_by"))
	assert.Equal(t, "embedding:([], distance_threshold: 0.75)", hybrid.Get("vector_query"))
	assert.Equal(t, "embedding", hybrid.Get("exclude_fields"))
	assert.Equal(t, filter, hybrid.Get("filter_by"))

	kw := searchParams("air filter", "title,body", filter, false)
	assert.Equal(t, "title,body", kw.Get("query_by"))
	assert.Empty(t, kw.Get("vector_query"))
}

// --- integration ------------------------------------------------------------

// testClient returns a client on household_search_<8hex> / files_<8hex>
// (not created yet; both dropped when the test ends), so this package never
// touches the real collections or another package's test collections on a
// shared Typesense. It skips unless TYPESENSE_TEST_ADDR and
// TYPESENSE_TEST_KEY are set. (Other packages use package searchtest, which
// this package cannot import.)
func testClient(t *testing.T) *Client {
	t.Helper()
	addr, key := os.Getenv("TYPESENSE_TEST_ADDR"), os.Getenv("TYPESENSE_TEST_KEY")
	if addr == "" || key == "" {
		t.Skip("TYPESENSE_TEST_ADDR/TYPESENSE_TEST_KEY not set; skipping Typesense integration test")
	}
	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	b := make([]byte, 4)
	_, err = rand.Read(b)
	require.NoError(t, err)
	sfx := hex.EncodeToString(b)
	c := NewClient(&config.Config{TypesenseHost: host, TypesensePort: port, TypesenseAPIKey: key}).
		WithCollections("household_search_"+sfx, "files_"+sfx)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, n := range []string{c.household, c.files} {
			_ = c.do(ctx, c.httpClient, http.MethodDelete, "/collections/"+url.PathEscape(n), nil, nil)
		}
	})
	return c
}

// TestTypesenseHybridIntegration runs against a real Typesense (30.x) and
// downloads the local embedding model on first run (~128MB). Enable with
//
//	docker run -d --name ts-test -p 127.0.0.1::8108 --tmpfs /data \
//	  typesense/typesense:30.2 --api-key=k --data-dir=/data
//	TYPESENSE_TEST_ADDR=127.0.0.1:$(docker port ts-test 8108 | cut -d: -f2) \
//	  TYPESENSE_TEST_KEY=k go test ./internal/search/ -run Integration
//
// It works on its own per-test collections (testClient), so it is safe next
// to the file and guides tests on one Typesense.
func TestTypesenseHybridIntegration(t *testing.T) {
	c := testClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Start from the pre-#4438 state: household_search with the plain float[]
	// placeholder, and files as the worker created it before chunking (no
	// embedding, no file_id).
	legacy := householdSchema(c.household, false)
	legacy.Fields = append(legacy.Fields, field{Name: EmbeddingField, Type: "float[]", NumDim: 384, Optional: true})
	require.NoError(t, c.do(ctx, c.httpClient, http.MethodPost, "/collections", legacy, nil))
	require.NoError(t, c.do(ctx, c.httpClient, http.MethodPost, "/collections", collectionSchema{
		Name: c.files,
		Fields: []field{
			{Name: "id", Type: "string"},
			{Name: "household_id", Type: "string", Facet: true},
			{Name: "name", Type: "string"},
			{Name: "extracted_text", Type: "string"},
			{Name: "entity_type", Type: "string", Optional: true, Facet: true},
			{Name: "entity_id", Type: "string", Optional: true},
			{Name: "tags", Type: "string[]", Facet: true},
			{Name: "created_at", Type: "int64"},
		},
		DefaultSortingField: "created_at",
	}, nil))

	docs := []SearchDocument{
		{ID: "asset-1", HouseholdID: "h1", EntityType: "asset", EntityID: "1", Title: "HVAC filter replacement", Body: "Swap the furnace filter every three months"},
		{ID: "note-2", HouseholdID: "h1", EntityType: "note", EntityID: "2", Title: "Dog vaccination", Body: "Rabies booster due for Max"},
		{ID: "vehicle-3", HouseholdID: "h1", EntityType: "vehicle", EntityID: "3", Title: "Toyota Camry", Body: "Oil change at 5000 miles"},
		{ID: "bill-4", HouseholdID: "h1", EntityType: "bill", EntityID: "4", Title: "Electric bill", Body: "Monthly utilities"},
		{ID: "asset-5", HouseholdID: "h2", EntityType: "asset", EntityID: "5", Title: "Air purifier filter", Body: "Other household"},
	}
	for _, d := range docs {
		require.NoError(t, c.IndexDocument(ctx, d))
	}
	file := map[string]any{"id": "f1", "household_id": "h1", "name": "scan-0042.pdf",
		"extracted_text": "Homeowners insurance declarations page. Dwelling coverage and liability limits.",
		"entity_type":    "property", "entity_id": "p1", "tags": []string{}, "created_at": 1}
	require.NoError(t, c.do(ctx, c.httpClient, http.MethodPost,
		"/collections/"+c.files+"/documents?action=upsert", file, nil))

	// Migration: both collections gain the auto-embedding field and existing
	// documents are embedded in place.
	require.NoError(t, c.InitCollection(ctx))
	for _, n := range []string{c.household, c.files} {
		s, err := c.getCollection(ctx, n)
		require.NoError(t, err)
		assert.True(t, hasAutoEmbedding(s), "%s should have the auto-embedding field", n)
	}
	// Idempotent.
	require.NoError(t, c.InitCollection(ctx))

	titles := func(q string) []string {
		res, err := c.Search(ctx, "h1", q, SearchFilters{})
		require.NoError(t, err)
		out := make([]string, 0, len(res))
		for _, r := range res {
			out = append(out, r.Title)
		}
		return out
	}

	got := titles("air filter")
	require.NotEmpty(t, got)
	assert.Equal(t, "HVAC filter replacement", got[0])
	assert.NotContains(t, got, "Air purifier filter", "household scoping")
	assert.NotContains(t, got, "Electric bill", "distance threshold drops unrelated docs")

	// Pure semantic hits: no shared keyword with the stored text.
	assert.Contains(t, titles("pet immunization"), "Dog vaccination")
	assert.Contains(t, titles("house insurance policy"), "scan-0042.pdf")

	// Reindex path: recreate, re-import, still hybrid.
	require.NoError(t, c.RecreateCollection(ctx))
	s, err := c.getCollection(ctx, c.household)
	require.NoError(t, err)
	require.True(t, hasAutoEmbedding(s))
	require.NoError(t, c.IndexDocument(ctx, docs[0]))
	var count struct {
		Found int `json:"found"`
	}
	q := url.Values{"q": {"heating maintenance"}, "query_by": {"embedding"}, "exclude_fields": {"embedding"}}
	require.NoError(t, c.do(ctx, c.httpClient, http.MethodGet,
		"/collections/"+c.household+"/documents/search?"+q.Encode(), nil, &count))
	assert.Equal(t, 1, count.Found, "reindexed document is embedded")
}

// Chunks of one file (worker-side chunking, shared file_id) collapse to the
// best-ranked one; household docs (no file_id) are never collapsed.
func TestToResultsCollapsesFileChunks(t *testing.T) {
	hit := func(doc map[string]any, score float64) searchHit {
		h := searchHit{Document: doc}
		h.HybridSearchInfo = &struct {
			RankFusionScore float64 `json:"rank_fusion_score"`
		}{score}
		return h
	}
	got := toResults([]searchHit{
		hit(map[string]any{"file_id": "f1", "name": "Policy.pdf", "extracted_text": "burst pipe clause"}, 0.9),
		hit(map[string]any{"file_id": "f2", "name": "Rex.txt", "extracted_text": "vaccination"}, 0.8),
		hit(map[string]any{"file_id": "f1", "name": "Policy.pdf", "extracted_text": "premiums"}, 0.7),
		hit(map[string]any{"title": "Note A", "body": "x"}, 0.6),
		hit(map[string]any{"title": "Note B", "body": "y"}, 0.5),
	}, false)
	if assert.Len(t, got, 4) {
		assert.Equal(t, "burst pipe clause", got[0].Body)
		assert.Equal(t, "Rex.txt", got[1].Title)
		assert.Equal(t, "Note A", got[2].Title)
		assert.Equal(t, "Note B", got[3].Title)
	}
}
