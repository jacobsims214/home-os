// Package search provides a Typesense client and search API for the Home OS platform.
// It manages the household_search collection, indexes documents, and performs
// household-scoped hybrid (keyword + semantic) search queries.
//
// Semantic search uses Typesense's built-in local embedding model
// (EmbeddingModel, ts/all-MiniLM-L12-v2). The model runs inside the Typesense
// process; no document text or query ever leaves the cluster. Typesense
// downloads the model files once, on the first collection that references it,
// into <data-dir>/models/ts_all-MiniLM-L12-v2/ (model.onnx, vocab.txt,
// config.json, ~128MB) — the data dir is the Typesense PVC, so it survives
// restarts and can be pre-seeded for air-gapped clusters.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"home-os/api/internal/config"
)

const (
	collectionName = "household_search"

	// filesCollectionName is the worker-owned collection of OCR'd files
	// (apps/worker/internal/search). The worker creates it; the API only adds
	// the auto-embedding field to it (see EnsureFilesEmbedding).
	filesCollectionName = "files"

	// EmbeddingField is the auto-embedding vector field present on both
	// collections. Typesense fills it at index time from the embed "from" fields.
	EmbeddingField = "embedding"

	// EmbeddingModel is Typesense's built-in local sentence-embedding model
	// (384 dimensions). It runs in-process inside Typesense via ONNX; no
	// external API is called. Never switch this to a remote (openai/, google/)
	// model: family data must not leave the cluster.
	EmbeddingModel = "ts/all-MiniLM-L12-v2"

	// semanticDistanceThreshold drops vector neighbours whose cosine distance
	// is above this value. Without it a hybrid query returns every document in
	// the household (everything has *some* nearest-neighbour distance).
	// Measured with all-MiniLM-L12-v2: related pairs ("air filter" ->
	// "HVAC filter replacement", "pet shots" -> "Dog vaccination") land at
	// 0.50-0.69, unrelated ones at 0.88+.
	semanticDistanceThreshold = 0.75

	// schemaTimeout bounds collection create/alter calls. The first call that
	// references the model downloads it (~128MB) and an alter re-embeds every
	// existing document synchronously, so this is much longer than the
	// per-request timeout.
	schemaTimeout = 5 * time.Minute

	// filesPerPage / filesMaxResults: the worker stores a long file as
	// several chunk documents sharing file_id (apps/worker/internal/search),
	// so the files query fetches more hits than it returns and keeps only the
	// best-ranked chunk of each file.
	filesPerPage    = 50
	filesMaxResults = 10
)

// householdEmbedFrom / filesEmbedFrom are the text fields each collection's
// embedding is computed from.
var (
	householdEmbedFrom = []string{"title", "body"}
	filesEmbedFrom     = []string{"name", "extracted_text"}
)

// Client talks to Typesense over its REST API. Raw HTTP is used throughout
// because the typesense-go generated types cannot express auto-embedding
// fields, field drops, or hybrid_search_info on hits.
type Client struct {
	serverURL  string
	apiKey     string
	httpClient *http.Client
	// schemaClient is used for collection create/alter, which may download
	// the embedding model and re-embed documents.
	schemaClient *http.Client
}

// SearchDocument represents a document to be indexed in Typesense.
// Fields match the household_search collection schema. The embedding field
// is deliberately absent: Typesense computes it from title and body.
type SearchDocument struct {
	ID          string   `json:"id"`
	HouseholdID string   `json:"household_id"`
	EntityType  string   `json:"entity_type"`
	EntityID    string   `json:"entity_id"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Tags        []string `json:"tags"`
	PropertyID  *string  `json:"property_id,omitempty"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

// SearchFilters holds optional filters applied to search queries.
type SearchFilters struct {
	EntityTypes []string
	PropertyID  string
}

// SearchResult is the API response shape for a single search hit.
type SearchResult struct {
	EntityType string  `json:"entity_type"`
	EntityID   string  `json:"entity_id"`
	Title      string  `json:"title"`
	Body       string  `json:"body"`
	Score      float64 `json:"score"`
}

// NewClient creates a new search client connected to the Typesense server
// configured in cfg. The server URL is built from TypesenseHost and TypesensePort.
func NewClient(cfg *config.Config) *Client {
	return &Client{
		serverURL:    fmt.Sprintf("http://%s:%s", cfg.TypesenseHost, cfg.TypesensePort),
		apiKey:       cfg.TypesenseAPIKey,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		schemaClient: &http.Client{Timeout: schemaTimeout},
	}
}

// --- schema types -----------------------------------------------------------

// The typesense-go generated Field type models "embed" as an anonymous struct
// and has no "drop" flag, so schema calls use these small local types instead.

type modelConfig struct {
	ModelName string `json:"model_name"`
}

type embedConfig struct {
	From        []string    `json:"from"`
	ModelConfig modelConfig `json:"model_config"`
}

type field struct {
	Name     string       `json:"name"`
	Type     string       `json:"type,omitempty"`
	Facet    bool         `json:"facet,omitempty"`
	Optional bool         `json:"optional,omitempty"`
	NumDim   int          `json:"num_dim,omitempty"`
	Embed    *embedConfig `json:"embed,omitempty"`
	Drop     bool         `json:"drop,omitempty"`
}

type collectionSchema struct {
	Name                string  `json:"name,omitempty"`
	Fields              []field `json:"fields"`
	DefaultSortingField string  `json:"default_sorting_field,omitempty"`
}

// embeddingFieldDef returns the auto-embedding field computed from `from`.
func embeddingFieldDef(from []string) field {
	return field{
		Name:  EmbeddingField,
		Type:  "float[]",
		Embed: &embedConfig{From: from, ModelConfig: modelConfig{ModelName: EmbeddingModel}},
	}
}

// householdSchema is the household_search schema. withEmbedding=false yields
// the keyword-only fallback used when the model cannot be loaded.
func householdSchema(withEmbedding bool) collectionSchema {
	s := collectionSchema{
		Name: collectionName,
		Fields: []field{
			{Name: "id", Type: "string"},
			{Name: "household_id", Type: "string", Facet: true},
			{Name: "entity_type", Type: "string", Facet: true},
			{Name: "entity_id", Type: "string"},
			{Name: "title", Type: "string"},
			{Name: "body", Type: "string"},
			{Name: "tags", Type: "string[]", Facet: true},
			{Name: "property_id", Type: "string", Optional: true, Facet: true},
			{Name: "created_at", Type: "int64"},
			{Name: "updated_at", Type: "int64"},
		},
		DefaultSortingField: "updated_at",
	}
	if withEmbedding {
		s.Fields = append(s.Fields, embeddingFieldDef(householdEmbedFrom))
	}
	return s
}

// hasAutoEmbedding reports whether the collection already has the
// auto-embedding field backed by EmbeddingModel.
func hasAutoEmbedding(s *collectionSchema) bool {
	for _, f := range s.Fields {
		if f.Name == EmbeddingField && f.Embed != nil && f.Embed.ModelConfig.ModelName == EmbeddingModel {
			return true
		}
	}
	return false
}

// embeddingMigration returns the PATCH body that converts s to the
// auto-embedding schema, or nil when no change is needed. An existing
// embedding field that is not auto-embedded (the original plain float[]
// placeholder, or a different model) is dropped and re-added in the same
// alter; Typesense then embeds every existing document.
func embeddingMigration(s *collectionSchema, from []string) *collectionSchema {
	if hasAutoEmbedding(s) {
		return nil
	}
	var fields []field
	for _, f := range s.Fields {
		if f.Name == EmbeddingField {
			fields = append(fields, field{Name: EmbeddingField, Drop: true})
			break
		}
	}
	fields = append(fields, embeddingFieldDef(from))
	return &collectionSchema{Fields: fields}
}

// --- HTTP helpers -----------------------------------------------------------

// statusError is a non-2xx response from Typesense.
type statusError struct {
	Status int
	Body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("typesense returned %d: %s", e.Status, e.Body)
}

func isStatus(err error, code int) bool {
	var se *statusError
	return errors.As(err, &se) && se.Status == code
}

// do sends a JSON request to Typesense and decodes the response into out
// (when non-nil).
func (c *Client) do(ctx context.Context, hc *http.Client, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.serverURL+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-TYPESENSE-API-KEY", c.apiKey)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &statusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(respBody))}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// getCollection returns the collection schema, or (nil, nil) when it does
// not exist.
func (c *Client) getCollection(ctx context.Context, name string) (*collectionSchema, error) {
	var s collectionSchema
	err := c.do(ctx, c.httpClient, http.MethodGet, "/collections/"+url.PathEscape(name), nil, &s)
	if isStatus(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// --- collection lifecycle ---------------------------------------------------

// InitCollection ensures the household_search collection exists with the
// auto-embedding field, and migrates existing collections (household_search
// and the worker-owned files collection) to it.
//
// If the Typesense server is unreachable, a warning is logged but no error is
// returned — this allows the API to start even when Typesense is not yet
// available. If the embedding model cannot be loaded (e.g. first start with
// no route to models.typesense.org and no pre-seeded model), household_search
// is created keyword-only and the migration is retried on the next start or
// reindex; search keeps working, just without the semantic half.
func (c *Client) InitCollection(ctx context.Context) error {
	existing, err := c.getCollection(ctx, collectionName)
	if err != nil {
		var se *statusError
		if !errors.As(err, &se) {
			slog.Warn("search: typesense unreachable, skipping init", "error", err)
			return nil
		}
		return fmt.Errorf("search: get collection %s: %w", collectionName, err)
	}

	if existing == nil {
		if err := c.createHousehold(ctx, true); err != nil {
			return err
		}
	} else if err := c.ensureEmbedding(ctx, existing, householdEmbedFrom); err != nil {
		slog.Warn("search: semantic search unavailable for collection, keyword search still works",
			"collection", collectionName, "error", err)
	}

	if err := c.EnsureFilesEmbedding(ctx); err != nil {
		slog.Warn("search: semantic search unavailable for collection, keyword search still works",
			"collection", filesCollectionName, "error", err)
	}
	return nil
}

// createHousehold creates household_search with the auto-embedding field.
// When fallback is true and the embedded create fails (model unavailable),
// it creates the keyword-only schema instead so search still works.
func (c *Client) createHousehold(ctx context.Context, fallback bool) error {
	err := c.do(ctx, c.schemaClient, http.MethodPost, "/collections", householdSchema(true), nil)
	if err == nil {
		slog.Info("search: created collection", "collection", collectionName, "embedding_model", EmbeddingModel)
		return nil
	}
	if !fallback {
		return fmt.Errorf("search: create collection %s: %w", collectionName, err)
	}
	slog.Warn("search: could not create collection with embeddings, falling back to keyword-only",
		"collection", collectionName, "error", err)
	if err := c.do(ctx, c.schemaClient, http.MethodPost, "/collections", householdSchema(false), nil); err != nil {
		return fmt.Errorf("search: create collection %s: %w", collectionName, err)
	}
	slog.Info("search: created collection (keyword-only)", "collection", collectionName)
	return nil
}

// ensureEmbedding alters an existing collection to add the auto-embedding
// field. Typesense embeds all existing documents as part of the alter, so no
// re-import is needed.
func (c *Client) ensureEmbedding(ctx context.Context, s *collectionSchema, from []string) error {
	patch := embeddingMigration(s, from)
	if patch == nil {
		return nil
	}
	slog.Info("search: migrating collection to auto-embedding (embeds existing documents)",
		"collection", s.Name, "embedding_model", EmbeddingModel)
	start := time.Now()
	if err := c.do(ctx, c.schemaClient, http.MethodPatch, "/collections/"+url.PathEscape(s.Name), patch, nil); err != nil {
		return fmt.Errorf("alter collection %s: %w", s.Name, err)
	}
	slog.Info("search: collection migrated to auto-embedding", "collection", s.Name, "took", time.Since(start))
	return nil
}

// EnsureFilesEmbedding adds the auto-embedding field (from name +
// extracted_text) to the worker-owned files collection. The worker creates
// that collection; if it does not exist yet this is a no-op and the next
// InitCollection or reindex migrates it.
func (c *Client) EnsureFilesEmbedding(ctx context.Context) error {
	s, err := c.getCollection(ctx, filesCollectionName)
	if err != nil {
		return fmt.Errorf("search: get collection %s: %w", filesCollectionName, err)
	}
	if s == nil {
		slog.Info("search: files collection not created yet (worker owns it), skipping embedding migration")
		return nil
	}
	return c.ensureEmbedding(ctx, s, filesEmbedFrom)
}

// RecreateCollection drops household_search and creates it again with the
// current (auto-embedding) schema. Used by the reindex tool, which then
// re-imports every document from Postgres. Unlike InitCollection it does not
// fall back to keyword-only: a rebuild must produce the full schema.
func (c *Client) RecreateCollection(ctx context.Context) error {
	err := c.do(ctx, c.httpClient, http.MethodDelete, "/collections/"+collectionName, nil, nil)
	if err != nil && !isStatus(err, http.StatusNotFound) {
		return fmt.Errorf("search: drop collection %s: %w", collectionName, err)
	}
	return c.createHousehold(ctx, false)
}

// --- documents --------------------------------------------------------------

// IndexDocument upserts a document into the household_search collection.
// Typesense computes the embedding from title + body during the upsert.
// Uses direct HTTP instead of the Typesense Go client to avoid a nil-pointer
// panic in the client's Upsert method.
func (c *Client) IndexDocument(ctx context.Context, doc SearchDocument) error {
	if doc.Tags == nil {
		doc.Tags = []string{}
	}
	path := fmt.Sprintf("/collections/%s/documents?action=upsert", collectionName)
	if err := c.do(ctx, c.httpClient, http.MethodPost, path, doc, nil); err != nil {
		return fmt.Errorf("search: index document %s: %w", doc.ID, err)
	}
	return nil
}

// DeleteDocument removes a document from the household_search collection by ID.
func (c *Client) DeleteDocument(ctx context.Context, id string) error {
	path := fmt.Sprintf("/collections/%s/documents/%s", collectionName, url.PathEscape(id))
	err := c.do(ctx, c.httpClient, http.MethodDelete, path, nil, nil)
	if err != nil && !isStatus(err, http.StatusNotFound) {
		return fmt.Errorf("search: delete document %s: %w", id, err)
	}
	return nil
}

// --- search -----------------------------------------------------------------

// Search queries all indexed collections (household_search + files), scoped to
// a single household. Each collection is queried in hybrid mode (keyword +
// semantic, fused by Typesense's rank fusion). Results from all collections
// are merged and returned.
func (c *Client) Search(ctx context.Context, householdID, query string, filters SearchFilters) ([]SearchResult, error) {
	mainResults, err := c.searchCollection(ctx, collectionName, householdID, query, filters, "title,body,tags", 0)
	if err != nil {
		slog.Warn("search: household_search query failed", "error", err)
	}

	// Files use entity_type for the attached entity, not the file itself.
	// If the user filters by entity type, we want to match files attached to that type.
	fileResults, err := c.searchCollection(ctx, filesCollectionName, householdID, query, filters, "name,extracted_text", filesPerPage)
	if err != nil {
		slog.Warn("search: files query failed", "error", err)
	}
	if len(fileResults) > filesMaxResults {
		fileResults = fileResults[:filesMaxResults]
	}

	results := make([]SearchResult, 0, len(mainResults)+len(fileResults))
	results = append(results, mainResults...)
	results = append(results, fileResults...)
	return results, nil
}

// buildFilter returns the Typesense filter_by expression enforcing household
// scoping plus the optional filters.
func buildFilter(householdID string, filters SearchFilters) string {
	parts := []string{fmt.Sprintf("household_id:=`%s`", householdID)}
	if len(filters.EntityTypes) > 0 {
		quoted := make([]string, len(filters.EntityTypes))
		for i, t := range filters.EntityTypes {
			quoted[i] = fmt.Sprintf("`%s`", t)
		}
		parts = append(parts, fmt.Sprintf("entity_type:=[%s]", strings.Join(quoted, ",")))
	}
	if filters.PropertyID != "" {
		parts = append(parts, fmt.Sprintf("property_id:=`%s`", filters.PropertyID))
	}
	return strings.Join(parts, " && ")
}

// searchParams builds the query string. With hybrid=true the embedding field
// is added to query_by, which makes Typesense embed the query with the same
// local model and fuse keyword and vector ranks.
func searchParams(query, queryBy, filterBy string, hybrid bool) url.Values {
	v := url.Values{}
	v.Set("q", query)
	v.Set("filter_by", filterBy)
	v.Set("exclude_fields", EmbeddingField)
	if hybrid {
		v.Set("query_by", queryBy+","+EmbeddingField)
		v.Set("vector_query", fmt.Sprintf("%s:([], distance_threshold: %s)",
			EmbeddingField, strconv.FormatFloat(semanticDistanceThreshold, 'f', -1, 64)))
	} else {
		v.Set("query_by", queryBy)
	}
	return v
}

type searchHit struct {
	Document      map[string]any `json:"document"`
	TextMatchInfo *struct {
		Score string `json:"score"`
	} `json:"text_match_info"`
	HybridSearchInfo *struct {
		RankFusionScore float64 `json:"rank_fusion_score"`
	} `json:"hybrid_search_info"`
}

type searchResponse struct {
	Hits []searchHit `json:"hits"`
}

// searchCollection queries a single Typesense collection with household
// scoping. It tries a hybrid query first and falls back to keyword-only when
// the collection has no embedding field yet (e.g. files created by the
// worker before the API migrated it, or a keyword-only fallback schema).
//
// perPage > 0 sets per_page (Typesense's default is 10).
func (c *Client) searchCollection(ctx context.Context, collName, householdID, query string, filters SearchFilters, queryBy string, perPage int) ([]SearchResult, error) {
	filterBy := buildFilter(householdID, filters)
	path := "/collections/" + url.PathEscape(collName) + "/documents/search?"
	params := func(hybrid bool) string {
		v := searchParams(query, queryBy, filterBy, hybrid)
		if perPage > 0 {
			v.Set("per_page", strconv.Itoa(perPage))
		}
		return v.Encode()
	}

	var resp searchResponse
	err := c.do(ctx, c.httpClient, http.MethodGet, path+params(true), nil, &resp)
	var se *statusError
	if errors.As(err, &se) && !strings.Contains(se.Body, "Collection not found") {
		slog.Debug("search: hybrid query failed, retrying keyword-only", "collection", collName, "error", err)
		resp = searchResponse{}
		err = c.do(ctx, c.httpClient, http.MethodGet, path+params(false), nil, &resp)
	}
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", collName, err)
	}
	return toResults(resp.Hits), nil
}

// toResults maps Typesense hits to SearchResults. Score is the rank-fusion
// score for hybrid hits (0..1) and the text-match score otherwise. Hits
// carrying a file_id (chunks of one file in the files collection) are
// collapsed to the first, best-ranked one, whose body is the matching chunk.
func toResults(hits []searchHit) []SearchResult {
	results := make([]SearchResult, 0, len(hits))
	seenFiles := map[string]bool{}
	for _, hit := range hits {
		doc := hit.Document
		if doc == nil {
			continue
		}
		if fid, ok := doc["file_id"].(string); ok && fid != "" {
			if seenFiles[fid] {
				continue
			}
			seenFiles[fid] = true
		}
		str := func(keys ...string) string {
			for _, k := range keys {
				if v, ok := doc[k]; ok && v != nil {
					return fmt.Sprint(v)
				}
			}
			return ""
		}
		var score float64
		switch {
		case hit.HybridSearchInfo != nil:
			score = hit.HybridSearchInfo.RankFusionScore
		case hit.TextMatchInfo != nil:
			score, _ = strconv.ParseFloat(hit.TextMatchInfo.Score, 64)
		}
		results = append(results, SearchResult{
			EntityType: str("entity_type"),
			EntityID:   str("entity_id"),
			// Files collection uses "name" instead of "title", "extracted_text" instead of "body"
			Title: str("title", "name"),
			Body:  str("body", "extracted_text"),
			Score: score,
		})
	}
	return results
}
