// Package search provides a Typesense client and search API for the Home OS platform.
// It manages the household_search collection, indexes documents, and performs
// household-scoped search queries.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/typesense/typesense-go/v3/typesense"
	"github.com/typesense/typesense-go/v3/typesense/api"
	"github.com/typesense/typesense-go/v3/typesense/api/pointer"

	"home-os/api/internal/config"
)

const collectionName = "household_search"

// Client wraps the Typesense client for search operations.
type Client struct {
	ts         *typesense.Client
	serverURL  string
	apiKey     string
	httpClient *http.Client
}

// SearchDocument represents a document to be indexed in Typesense.
// Fields match the household_search collection schema.
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
	serverURL := fmt.Sprintf("http://%s:%s", cfg.TypesenseHost, cfg.TypesensePort)
	ts := typesense.NewClient(
		typesense.WithServer(serverURL),
		typesense.WithAPIKey(cfg.TypesenseAPIKey),
		typesense.WithConnectionTimeout(10*time.Second),
	)
	return &Client{
		ts:         ts,
		serverURL:  serverURL,
		apiKey:     cfg.TypesenseAPIKey,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// InitCollection ensures the household_search collection exists in Typesense.
// If the collection already exists, this is a no-op. If the Typesense server is
// unreachable, a warning is logged but no error is returned — this allows the
// API to start even when Typesense is not yet available.
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

	schema := &api.CollectionSchema{
		Name: collectionName,
		Fields: []api.Field{
			{Name: "id", Type: "string"},
			{Name: "household_id", Type: "string", Facet: pointer.True()},
			{Name: "entity_type", Type: "string", Facet: pointer.True()},
			{Name: "entity_id", Type: "string"},
			{Name: "title", Type: "string"},
			{Name: "body", Type: "string"},
			{Name: "tags", Type: "string[]", Facet: pointer.True()},
			{Name: "property_id", Type: "string", Optional: pointer.True(), Facet: pointer.True()},
			{Name: "created_at", Type: "int64"},
			{Name: "updated_at", Type: "int64"},
			{Name: "embedding", Type: "float[]", NumDim: pointer.Int(384), Optional: pointer.True()},
		},
		DefaultSortingField: pointer.String("updated_at"),
	}

	_, err = c.ts.Collections().Create(ctx, schema)
	if err != nil {
		return fmt.Errorf("search: create collection %s: %w", collectionName, err)
	}

	slog.Info("search: created collection", "collection", collectionName)
	return nil
}

// IndexDocument upserts a document into the household_search collection.
// Uses direct HTTP instead of the Typesense Go client to avoid a nil-pointer
// panic in the client's Upsert method.
func (c *Client) IndexDocument(ctx context.Context, doc SearchDocument) error {
	body, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("search: marshal document: %w", err)
	}

	url := fmt.Sprintf("%s/collections/%s/documents?action=upsert", c.serverURL, collectionName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("search: build index request: %w", err)
	}
	req.Header.Set("X-TYPESENSE-API-KEY", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("search: index document %s: %w", doc.ID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("search: typesense returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// DeleteDocument removes a document from the household_search collection by ID.
func (c *Client) DeleteDocument(ctx context.Context, id string) error {
	url := fmt.Sprintf("%s/collections/%s/documents/%s", c.serverURL, collectionName, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("search: build delete request: %w", err)
	}
	req.Header.Set("X-TYPESENSE-API-KEY", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("search: delete document %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 && resp.StatusCode != 404 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("search: typesense returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// Search queries all indexed collections (household_search + files), scoped to
// a single household. Results from all collections are merged and returned.
func (c *Client) Search(ctx context.Context, householdID, query string, filters SearchFilters) ([]SearchResult, error) {
	// Search the main household_search collection
	mainResults, err := c.searchCollection(ctx, collectionName, householdID, query, filters, "title,body,tags")
	if err != nil {
		slog.Warn("search: household_search query failed", "error", err)
	}
	if mainResults == nil {
		mainResults = []SearchResult{}
	}

	// Search the files collection (indexed by the worker after OCR)
	fileFilters := filters
	// Files use entity_type for the attached entity, not the file itself.
	// If the user filters by entity type, we want to match files attached to that type.
	fileResults, err := c.searchCollection(ctx, "files", householdID, query, fileFilters, "name,extracted_text")
	if err != nil {
		slog.Warn("search: files query failed", "error", err)
	}
	if fileResults == nil {
		fileResults = []SearchResult{}
	}

	// Merge results
	results := append(mainResults, fileResults...)

	if results == nil {
		results = []SearchResult{}
	}

	return results, nil
}

// searchCollection queries a single Typesense collection with household scoping.
func (c *Client) searchCollection(ctx context.Context, collName, householdID, query string, filters SearchFilters, queryBy string) ([]SearchResult, error) {
	var filterParts []string
	filterParts = append(filterParts, fmt.Sprintf("household_id:=`%s`", householdID))

	if len(filters.EntityTypes) > 0 {
		quoted := make([]string, len(filters.EntityTypes))
		for i, t := range filters.EntityTypes {
			quoted[i] = fmt.Sprintf("`%s`", t)
		}
		filterParts = append(filterParts, fmt.Sprintf("entity_type:=[%s]", strings.Join(quoted, ",")))
	}
	if filters.PropertyID != "" {
		filterParts = append(filterParts, fmt.Sprintf("property_id:=`%s`", filters.PropertyID))
	}

	filterBy := strings.Join(filterParts, " && ")

	params := &api.SearchCollectionParams{
		Q:        pointer.String(query),
		QueryBy:  pointer.String(queryBy),
		FilterBy: pointer.String(filterBy),
	}

	result, err := c.ts.Collection(collName).Documents().Search(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", collName, err)
	}

	var results []SearchResult
	if result.Hits != nil {
		for _, hit := range *result.Hits {
			doc := hit.Document
			if doc == nil {
				continue
			}

			var entityType, entityID, title, body string
			if v, ok := (*doc)["entity_type"]; ok {
				entityType = fmt.Sprint(v)
			}
			if v, ok := (*doc)["entity_id"]; ok {
				entityID = fmt.Sprint(v)
			}
			// Files collection uses "name" instead of "title", "extracted_text" instead of "body"
			if v, ok := (*doc)["title"]; ok {
				title = fmt.Sprint(v)
			} else if v, ok := (*doc)["name"]; ok {
				title = fmt.Sprint(v)
			}
			if v, ok := (*doc)["body"]; ok {
				body = fmt.Sprint(v)
			} else if v, ok := (*doc)["extracted_text"]; ok {
				body = fmt.Sprint(v)
			}

			var score float64
			if hit.TextMatchInfo != nil && hit.TextMatchInfo.Score != nil {
				_, _ = fmt.Sscanf(*hit.TextMatchInfo.Score, "%f", &score)
			}

			results = append(results, SearchResult{
				EntityType: entityType,
				EntityID:   entityID,
				Title:      title,
				Body:       body,
				Score:      score,
			})
		}
	}

	if results == nil {
		results = []SearchResult{}
	}

	return results, nil
}