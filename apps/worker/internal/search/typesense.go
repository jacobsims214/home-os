// Package search provides the worker's Typesense client for indexing files.
//
// The worker owns the `files` collection in Typesense (separate from the API's
// `household_search` collection). Files are indexed once their OCR pipeline
// finishes, and again whenever their row changes (rename, new tags), so the
// global search bar can find documents by name and by extracted content. The
// worker is the sole writer of this collection; the API only queries it and
// may add the embedding field to an old collection (see
// apps/api/internal/search.EnsureFilesEmbedding).
//
// # Semantic search
//
// The collection has an auto-embedding field, `embedding`, computed by
// Typesense from [name, extracted_text] with its built-in local model
// EmbeddingModel (ts/all-MiniLM-L12-v2). The model runs inside the Typesense
// process; no file text ever leaves the cluster. The field definition must stay
// identical to the API's (apps/api/internal/search.embeddingFieldDef with
// filesEmbedFrom) so the schema is the same whichever service creates or
// migrates the collection first.
//
// # Chunks
//
// MiniLM reads only the first ~256 tokens of its input, so one document per
// file would embed only the first paragraph of a long PDF. A file is therefore
// stored as one or more chunk documents (see Chunk). Every chunk carries the
// file's metadata (name, tags, entity) plus one slice of the extracted text,
// and gets its own embedding. Chunk 0 has id = the file id (the shape the
// collection always had); chunk n > 0 has id "<file id>_<n>". All chunks share
// file_id, so callers that list results collapse them by file_id (the API's
// Search does), and deleting a file deletes by file_id.
package search

import (
	"bufio"
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
)

const (
	// collectionName is the Typesense collection that holds file documents.
	collectionName = "files"

	// EmbeddingField is the auto-embedding vector field. Typesense fills it
	// at index time from embedFrom; writers never send it.
	EmbeddingField = "embedding"

	// EmbeddingModel is Typesense's built-in local sentence-embedding model
	// (384 dimensions, ONNX, in-process). Never switch this to a remote
	// (openai/, google/, ...) model: family data must not leave the cluster.
	// Must match apps/api/internal/search.EmbeddingModel.
	EmbeddingModel = "ts/all-MiniLM-L12-v2"

	// schemaTimeout bounds collection create/alter calls. The first call that
	// references the model downloads it (~128MB) and an alter re-embeds every
	// existing document synchronously.
	schemaTimeout = 5 * time.Minute

	// requestTimeout bounds document and search calls. An import of a large
	// file embeds up to importBatchSize chunks in one request.
	requestTimeout = 60 * time.Second

	// importBatchSize is how many chunk documents go in one import request.
	importBatchSize = 40
)

// embedFrom is the text the embedding is computed from. Typesense joins the
// field values with a space, so each chunk's vector covers the file name plus
// that chunk's text. Must match apps/api/internal/search.filesEmbedFrom.
var embedFrom = []string{"name", "extracted_text"}

// Client talks to Typesense over its REST API. Raw HTTP is used (as in the
// API's search package) because the typesense-go generated types cannot
// express auto-embedding fields cleanly and have no bulk-delete-by-filter
// result type. It is safe for concurrent use.
type Client struct {
	serverURL    string
	apiKey       string
	httpClient   *http.Client
	schemaClient *http.Client
}

// NewClient creates a search client connected to the Typesense server at
// host:port with the given API key (TYPESENSE_HOST / TYPESENSE_PORT /
// TYPESENSE_API_KEY in the worker config).
func NewClient(host, port, apiKey string) *Client {
	return &Client{
		serverURL:    fmt.Sprintf("http://%s:%s", host, port),
		apiKey:       apiKey,
		httpClient:   &http.Client{Timeout: requestTimeout},
		schemaClient: &http.Client{Timeout: schemaTimeout},
	}
}

// --- schema -----------------------------------------------------------------

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
	Embed    *embedConfig `json:"embed,omitempty"`
	Drop     bool         `json:"drop,omitempty"`
}

type collectionSchema struct {
	Name                string  `json:"name,omitempty"`
	Fields              []field `json:"fields"`
	DefaultSortingField string  `json:"default_sorting_field,omitempty"`
}

// embeddingFieldDef is the auto-embedding field. Identical to the API's
// embeddingFieldDef(filesEmbedFrom).
func embeddingFieldDef() field {
	return field{
		Name:  EmbeddingField,
		Type:  "float[]",
		Embed: &embedConfig{From: embedFrom, ModelConfig: modelConfig{ModelName: EmbeddingModel}},
	}
}

// chunkFields were added with chunking. They are optional so an existing
// collection (whose documents lack them) can be altered to include them.
//   - file_id      the file UUID, shared by all of a file's chunks
//   - chunk_index  0-based position of the chunk in the file
//   - chunk_count  how many chunks the file has
//   - row_version  files.updated_at in unix microseconds when the file was
//     indexed; the highest value is the worker's re-index watermark
func chunkFields() []field {
	return []field{
		{Name: "file_id", Type: "string", Optional: true},
		{Name: "chunk_index", Type: "int32", Optional: true},
		{Name: "chunk_count", Type: "int32", Optional: true},
		{Name: "row_version", Type: "int64", Optional: true},
	}
}

// filesSchema is the `files` collection schema. withEmbedding=false yields
// the keyword-only fallback used when the model cannot be loaded.
//
// entity_type / entity_id / entity_name are optional because files can be
// uploaded without being attached to anything.
func filesSchema(withEmbedding bool) collectionSchema {
	s := collectionSchema{
		Name: collectionName,
		Fields: []field{
			{Name: "id", Type: "string"},
			{Name: "household_id", Type: "string", Facet: true},
			{Name: "name", Type: "string"},
			{Name: "extracted_text", Type: "string"},
			{Name: "entity_type", Type: "string", Optional: true, Facet: true},
			{Name: "entity_id", Type: "string", Optional: true},
			{Name: "entity_name", Type: "string", Optional: true},
			{Name: "tags", Type: "string[]", Facet: true},
			{Name: "created_at", Type: "int64"},
		},
		DefaultSortingField: "created_at",
	}
	s.Fields = append(s.Fields, chunkFields()...)
	if withEmbedding {
		s.Fields = append(s.Fields, embeddingFieldDef())
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

// missingChunkFields returns the chunk fields the collection does not have.
func missingChunkFields(s *collectionSchema) []field {
	have := make(map[string]bool, len(s.Fields))
	for _, f := range s.Fields {
		have[f.Name] = true
	}
	var out []field
	for _, f := range chunkFields() {
		if !have[f.Name] {
			out = append(out, f)
		}
	}
	return out
}

// embeddingMigration returns the alter fields that give s the auto-embedding
// field, or nil when it already has it. A non-auto `embedding` field (an old
// placeholder or another model) is dropped and re-added in the same alter,
// matching the API's migration.
func embeddingMigration(s *collectionSchema) []field {
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
	return append(fields, embeddingFieldDef())
}

// InitCollection ensures the `files` collection exists with the current
// schema: the chunk fields and the auto-embedding field.
//
//   - Typesense unreachable: logs a warning and returns nil, so the worker
//     still starts and OCR keeps working; the processor calls InitCollection
//     again before it indexes.
//   - Collection missing: creates it with the embedding field. If the model
//     cannot be loaded (no route to models.typesense.org and nothing cached),
//     it creates the keyword-only schema and logs a warning; the next start
//     (or the API's InitCollection) adds the embedding field.
//   - Collection exists: adds any missing chunk fields, then the embedding
//     field. Typesense embeds existing documents during the alter.
func (c *Client) InitCollection(ctx context.Context) error {
	existing, err := c.getCollection(ctx)
	if err != nil {
		if !isStatusErr(err) {
			slog.Warn("search: typesense unreachable, skipping collection init", "error", err)
			return nil
		}
		return fmt.Errorf("search: get collection %s: %w", collectionName, err)
	}

	if existing == nil {
		err := c.do(ctx, c.schemaClient, http.MethodPost, "/collections", filesSchema(true), nil)
		if err == nil {
			slog.Info("search: created collection", "collection", collectionName, "embedding_model", EmbeddingModel)
			return nil
		}
		if isStatus(err, http.StatusConflict) {
			return nil // created concurrently (by another worker replica)
		}
		slog.Warn("search: could not create collection with embeddings, falling back to keyword-only",
			"collection", collectionName, "error", err)
		if err := c.do(ctx, c.schemaClient, http.MethodPost, "/collections", filesSchema(false), nil); err != nil && !isStatus(err, http.StatusConflict) {
			return fmt.Errorf("search: create collection %s: %w", collectionName, err)
		}
		slog.Info("search: created collection (keyword-only)", "collection", collectionName)
		return nil
	}

	if missing := missingChunkFields(existing); len(missing) > 0 {
		if err := c.alter(ctx, missing); err != nil {
			return fmt.Errorf("search: add chunk fields to %s: %w", collectionName, err)
		}
		slog.Info("search: added chunk fields", "collection", collectionName, "fields", len(missing))
	}
	if patch := embeddingMigration(existing); patch != nil {
		start := time.Now()
		if err := c.alter(ctx, patch); err != nil {
			slog.Warn("search: semantic search unavailable for collection, keyword search still works",
				"collection", collectionName, "error", err)
			return nil
		}
		slog.Info("search: collection migrated to auto-embedding", "collection", collectionName, "took", time.Since(start))
	}
	return nil
}

func (c *Client) alter(ctx context.Context, fields []field) error {
	return c.do(ctx, c.schemaClient, http.MethodPatch, "/collections/"+collectionName,
		collectionSchema{Fields: fields}, nil)
}

// getCollection returns the collection schema, or (nil, nil) when it does
// not exist.
func (c *Client) getCollection(ctx context.Context) (*collectionSchema, error) {
	var s collectionSchema
	err := c.do(ctx, c.httpClient, http.MethodGet, "/collections/"+collectionName, nil, &s)
	if isStatus(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// --- documents --------------------------------------------------------------

// FileDocument is the file-level data indexed in the `files` collection: a
// files row plus the denormalized entity_name. IndexFile turns it into one
// document per chunk.
type FileDocument struct {
	ID          string
	HouseholdID string
	Name        string
	EntityType  string // "" when the file is not attached
	EntityID    string // "" when the file is not attached
	EntityName  string
	Tags        []string
	CreatedAt   int64 // unix millis
	RowVersion  int64 // files.updated_at, unix micros
}

// chunkDocument is one document as stored in Typesense.
type chunkDocument struct {
	ID            string   `json:"id"`
	FileID        string   `json:"file_id"`
	ChunkIndex    int      `json:"chunk_index"`
	ChunkCount    int      `json:"chunk_count"`
	HouseholdID   string   `json:"household_id"`
	Name          string   `json:"name"`
	ExtractedText string   `json:"extracted_text"`
	EntityType    string   `json:"entity_type,omitempty"`
	EntityID      string   `json:"entity_id,omitempty"`
	EntityName    string   `json:"entity_name,omitempty"`
	Tags          []string `json:"tags"`
	CreatedAt     int64    `json:"created_at"`
	RowVersion    int64    `json:"row_version"`
}

// ChunkID returns the Typesense document id of chunk i of a file.
func ChunkID(fileID string, i int) string {
	if i == 0 {
		return fileID
	}
	return fileID + "_" + strconv.Itoa(i)
}

// chunkDocuments builds the documents for a file's chunks. An empty chunks
// slice still yields one document so the file is findable by name and tags.
func chunkDocuments(doc FileDocument, chunks []string) []chunkDocument {
	if len(chunks) == 0 {
		chunks = []string{""}
	}
	tags := doc.Tags
	if tags == nil {
		// string[] must be an array, never null.
		tags = []string{}
	}
	out := make([]chunkDocument, len(chunks))
	for i, text := range chunks {
		out[i] = chunkDocument{
			ID:            ChunkID(doc.ID, i),
			FileID:        doc.ID,
			ChunkIndex:    i,
			ChunkCount:    len(chunks),
			HouseholdID:   doc.HouseholdID,
			Name:          doc.Name,
			ExtractedText: text,
			EntityType:    doc.EntityType,
			EntityID:      doc.EntityID,
			EntityName:    doc.EntityName,
			Tags:          tags,
			CreatedAt:     doc.CreatedAt,
			RowVersion:    doc.RowVersion,
		}
	}
	return out
}

// IndexFile upserts a file as len(chunks) chunk documents (one if chunks is
// empty) and then deletes any higher-numbered chunks left from an earlier,
// longer version of the file. Typesense embeds each chunk during the import.
//
// Chunks are imported in batches of importBatchSize. A document Typesense
// rejects is reported as a *DocumentError (permanent: retrying the same
// document will fail the same way); transport errors and 5xx responses are
// returned as they are (transient).
func (c *Client) IndexFile(ctx context.Context, doc FileDocument, chunks []string) error {
	docs := chunkDocuments(doc, chunks)
	for start := 0; start < len(docs); start += importBatchSize {
		end := min(start+importBatchSize, len(docs))
		if err := c.importBatch(ctx, docs[start:end]); err != nil {
			return fmt.Errorf("search: index file %s: %w", doc.ID, err)
		}
	}
	filter := fmt.Sprintf("file_id:=`%s` && chunk_index:>=%d", doc.ID, len(docs))
	if err := c.deleteByFilter(ctx, filter); err != nil {
		return fmt.Errorf("search: delete stale chunks of %s: %w", doc.ID, err)
	}
	return nil
}

// DocumentError is a document Typesense refused to index (bad field value,
// embedding failure for that text, ...). Retrying it unchanged will not help.
type DocumentError struct {
	DocID   string
	Message string
}

func (e *DocumentError) Error() string {
	return fmt.Sprintf("document %s rejected: %s", e.DocID, e.Message)
}

// IsPermanent reports whether err means the file cannot be indexed as it is
// (a rejected document or a 4xx other than 404/408/429), as opposed to
// Typesense being down or busy.
func IsPermanent(err error) bool {
	var de *DocumentError
	if errors.As(err, &de) {
		return true
	}
	var se *statusError
	if errors.As(err, &se) {
		switch se.Status {
		case http.StatusNotFound, http.StatusRequestTimeout, http.StatusTooManyRequests:
			return false
		}
		return se.Status >= 400 && se.Status < 500
	}
	return false
}

// importBatch sends docs as JSONL to the import endpoint with action=upsert.
// Typesense answers 200 with one JSON result line per document.
func (c *Client) importBatch(ctx context.Context, docs []chunkDocument) error {
	var body bytes.Buffer
	enc := json.NewEncoder(&body) // Encode appends '\n': JSONL
	for _, d := range docs {
		if err := enc.Encode(d); err != nil {
			return fmt.Errorf("marshal document %s: %w", d.ID, err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.serverURL+"/collections/"+collectionName+"/documents/import?action=upsert", &body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-TYPESENSE-API-KEY", c.apiKey)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &statusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for i := 0; sc.Scan(); i++ {
		var r struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return fmt.Errorf("decode import result: %w", err)
		}
		if !r.Success {
			id := "?"
			if i < len(docs) {
				id = docs[i].ID
			}
			return &DocumentError{DocID: id, Message: r.Error}
		}
	}
	return sc.Err()
}

// deleteByFilter deletes every document matching filter. No match is fine.
func (c *Client) deleteByFilter(ctx context.Context, filter string) error {
	q := url.Values{"filter_by": {filter}}
	return c.do(ctx, c.httpClient, http.MethodDelete,
		"/collections/"+collectionName+"/documents?"+q.Encode(), nil, nil)
}

// DeleteFile removes all of a file's chunk documents. The document with
// id = fileID is also deleted by id, which covers documents indexed before
// chunking (they have no file_id). A missing file, document or collection is
// success: there is nothing left to find.
func (c *Client) DeleteFile(ctx context.Context, fileID string) error {
	if err := c.deleteByFilter(ctx, fmt.Sprintf("file_id:=`%s`", fileID)); err != nil {
		if isStatus(err, http.StatusNotFound) {
			return nil // no collection, so no documents
		}
		return fmt.Errorf("search: delete file %s: %w", fileID, err)
	}
	err := c.do(ctx, c.httpClient, http.MethodDelete,
		"/collections/"+collectionName+"/documents/"+url.PathEscape(fileID), nil, nil)
	if err != nil && !isStatus(err, http.StatusNotFound) {
		return fmt.Errorf("search: delete file %s: %w", fileID, err)
	}
	return nil
}

// IndexedFileIDs returns the id of every file that has at least one
// document in the collection, from the export endpoint (JSONL, id and file_id
// only). Chunk documents carry file_id; documents indexed before chunking
// have none, and their id is the file id. A missing collection returns no
// ids. The processor compares the result with Postgres once per start to
// remove files deleted before deletes were queued (migration 053).
func (c *Client) IndexedFileIDs(ctx context.Context) ([]string, error) {
	q := url.Values{"include_fields": {"id,file_id"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.serverURL+"/collections/"+collectionName+"/documents/export?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("search: export ids: %w", err)
	}
	req.Header.Set("X-TYPESENSE-API-KEY", c.apiKey)
	// The export streams the whole collection, so it gets the schema timeout.
	resp, err := c.schemaClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search: export ids: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return []string{}, nil
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("search: export ids: %w",
			&statusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))})
	}
	seen := map[string]bool{}
	ids := []string{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var d struct {
			ID     string `json:"id"`
			FileID string `json:"file_id"`
		}
		if err := json.Unmarshal(line, &d); err != nil {
			return nil, fmt.Errorf("search: export ids: decode line: %w", err)
		}
		id := d.FileID
		if id == "" {
			id = d.ID
		}
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("search: export ids: read: %w", err)
	}
	return ids, nil
}

// Watermark is the newest file version in the index: the row_version
// (files.updated_at in unix micros) and file id of the most recently changed
// indexed file. Zero means nothing has been indexed with a version yet.
type Watermark struct {
	RowVersion int64
	FileID     string
}

// IndexWatermark returns the highest row_version (and its file_id) in the
// collection. The processor resumes its re-index sweep from there after a
// restart, so files changed while the worker was down are still re-indexed
// and files already indexed are not embedded again.
func (c *Client) IndexWatermark(ctx context.Context) (Watermark, error) {
	q := url.Values{
		"q":              {"*"},
		"filter_by":      {"row_version:>0"},
		"sort_by":        {"row_version:desc"},
		"per_page":       {"1"},
		"include_fields": {"row_version,file_id"},
	}
	var resp struct {
		Hits []struct {
			Document struct {
				RowVersion int64  `json:"row_version"`
				FileID     string `json:"file_id"`
			} `json:"document"`
		} `json:"hits"`
	}
	if err := c.do(ctx, c.httpClient, http.MethodGet,
		"/collections/"+collectionName+"/documents/search?"+q.Encode(), nil, &resp); err != nil {
		return Watermark{}, fmt.Errorf("search: read index watermark: %w", err)
	}
	if len(resp.Hits) == 0 {
		return Watermark{}, nil
	}
	d := resp.Hits[0].Document
	return Watermark{RowVersion: d.RowVersion, FileID: d.FileID}, nil
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

func isStatusErr(err error) bool {
	var se *statusError
	return errors.As(err, &se)
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
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &statusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
