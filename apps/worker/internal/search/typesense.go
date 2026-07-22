// Package search provides the worker's Typesense client for indexing files.
//
// The worker maintains a dedicated `files` collection in Typesense (separate
// from the API's `household_search` collection). Files are indexed here once
// their OCR pipeline finishes, so the global search bar can find documents by
// their extracted content. The worker is the sole writer of this collection —
// the API never writes to Typesense directly (see architecture/search-platform.md
// for the source-of-truth / outbox pattern that governs Typesense).
//
// The collection schema mirrors the shape of a files row plus a denormalized
// `entity_name` (resolved from the attached entity table) so search results can
// show "this file is attached to <entity>" without a second round-trip.
package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/typesense/typesense-go/v3/typesense"
	"github.com/typesense/typesense-go/v3/typesense/api"
	"github.com/typesense/typesense-go/v3/typesense/api/pointer"
)

// collectionName is the Typesense collection that holds file documents.
// It is distinct from the API's `household_search` collection: file content
// search needs the full extracted_text field and its own filter facets, and
// keeping it separate lets the worker re-index files independently of the
// household_search pipeline.
const collectionName = "files"

// Client wraps the Typesense client for the worker's file-indexing operations.
// It is safe for concurrent use — the underlying typesense.Client uses an
// HTTP connection pool.
type Client struct {
	ts *typesense.Client
}

// NewClient creates a search client connected to the Typesense server at
// host:port with the given API key. Both come from the worker config
// (TYPESENSE_HOST / TYPESENSE_PORT / TYPESENSE_API_KEY).
func NewClient(host, port, apiKey string) *Client {
	serverURL := fmt.Sprintf("http://%s:%s", host, port)
	ts := typesense.NewClient(
		typesense.WithServer(serverURL),
		typesense.WithAPIKey(apiKey),
		typesense.WithConnectionTimeout(10*time.Second),
	)
	return &Client{ts: ts}
}

// InitCollection ensures the `files` collection exists in Typesense. If the
// collection already exists this is a no-op. If Typesense is unreachable, a
// warning is logged and nil is returned so the worker can still start — the
// processor will simply fail to index files until Typesense comes back, which
// is preferable to crashing the OCR pipeline on a search-side outage.
//
// Matches the resilience posture of apps/api/internal/search.Client.InitCollection.
func (c *Client) InitCollection(ctx context.Context) error {
	cols, err := c.ts.Collections().Retrieve(ctx)
	if err != nil {
		slog.Warn("search: failed to retrieve collections, skipping init", "error", err)
		return nil
	}
	for _, col := range cols {
		if col.Name == collectionName {
			slog.Info("search: collection already exists", "collection", collectionName)
			return nil
		}
	}

	// Fields mirror the file-indexing contract:
	//   - id              the file UUID (string)
	//   - household_id    tenancy filter (facet)
	//   - name            the uploaded file's display name
	//   - extracted_text  OCR output from Tika — the searchable body
	//   - entity_type     polymorphic attach target type (facet, optional —
	//                     a file may be unattached)
	//   - entity_id       polymorphic attach target UUID (optional)
	//   - entity_name     denormalized name of the attached entity, resolved
	//                     by the worker from the entity's own table at index
	//                     time so the search UI never has to join
	//   - tags            user-applied tags (facet)
	//   - created_at      unix-millis, for sort-by-newest
	//
	// entity_type / entity_id / entity_name are optional because files can be
	// uploaded without being attached to anything (the schema allows NULL).
	schema := &api.CollectionSchema{
		Name: collectionName,
		Fields: []api.Field{
			{Name: "id", Type: "string"},
			{Name: "household_id", Type: "string", Facet: pointer.True()},
			{Name: "name", Type: "string"},
			{Name: "extracted_text", Type: "string"},
			{Name: "entity_type", Type: "string", Optional: pointer.True(), Facet: pointer.True()},
			{Name: "entity_id", Type: "string", Optional: pointer.True()},
			{Name: "entity_name", Type: "string", Optional: pointer.True()},
			{Name: "tags", Type: "string[]", Facet: pointer.True()},
			{Name: "created_at", Type: "int64"},
		},
		DefaultSortingField: pointer.String("created_at"),
	}

	if _, err = c.ts.Collections().Create(ctx, schema); err != nil {
		return fmt.Errorf("search: create collection %s: %w", collectionName, err)
	}

	slog.Info("search: created collection", "collection", collectionName)
	return nil
}

// FileDocument is the document shape indexed in the `files` collection.
// It is the worker-facing view of a file row plus the denormalized entity_name.
type FileDocument struct {
	ID            string   `json:"id"`
	HouseholdID   string   `json:"household_id"`
	Name          string   `json:"name"`
	ExtractedText string   `json:"extracted_text"`
	EntityType    string   `json:"entity_type,omitempty"`
	EntityID      string   `json:"entity_id,omitempty"`
	EntityName    string   `json:"entity_name,omitempty"`
	Tags          []string `json:"tags"`
	CreatedAt     int64    `json:"created_at"`
}

// IndexFile upserts a file document into the `files` collection. Call this
// after OCR completes (ocr_status=done) so the file becomes searchable by its
// extracted text. If a document with the same id already exists it is updated.
func (c *Client) IndexFile(ctx context.Context, doc FileDocument) error {
	// Tags must be a non-nil slice in the JSON payload so Typesense always
	// sees an array (an empty array is fine; nil would marshal as null and
	// fail schema validation for a string[] field).
	if doc.Tags == nil {
		doc.Tags = []string{}
	}

	if _, err := c.ts.Collection(collectionName).Documents().Upsert(ctx, doc, &api.DocumentIndexParameters{}); err != nil {
		return fmt.Errorf("search: index file %s: %w", doc.ID, err)
	}
	return nil
}

// DeleteFile removes a file document from the `files` collection by id.
// Missing-document errors from Typesense are treated as success — the file is
// already gone, which is exactly what the caller wanted.
func (c *Client) DeleteFile(ctx context.Context, fileID string) error {
	if _, err := c.ts.Collection(collectionName).Document(fileID).Delete(ctx); err != nil {
		// Typesense returns 404 when the document doesn't exist. That's the
		// desired end-state for a delete, so swallow it rather than surfacing
		// a spurious error to the processor. The typesense-go v3 client
		// wraps non-200 Delete responses in *typesense.HTTPError (see
		// typesense/document.go — Delete path returns &HTTPError{Status,
		// Body}); use errors.As so the check still works if a future caller
		// wraps the error with fmt.Errorf("%w", ...) before reaching us.
		var httpErr *typesense.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("search: delete file %s: %w", fileID, err)
	}
	return nil
}
