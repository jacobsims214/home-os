// Package search provides a Typesense client and search API for the Home OS platform.
// It manages the household_search collection, indexes documents, and performs
// household-scoped search queries.
package search

import (
	"context"
	"fmt"
	"log/slog"
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
	ts *typesense.Client
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
	return &Client{ts: ts}
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
// If a document with the same ID already exists, it is updated.
func (c *Client) IndexDocument(ctx context.Context, doc SearchDocument) error {
	_, err := c.ts.Collection(collectionName).Documents().Upsert(ctx, doc, nil)
	if err != nil {
		return fmt.Errorf("search: index document %s: %w", doc.ID, err)
	}
	return nil
}

// Search queries the household_search collection, scoped to a single household.
// The query is run against the title, body, and tags fields. Results are filtered
// by household_id and optionally by entity type and property.
func (c *Client) Search(ctx context.Context, householdID, query string, filters SearchFilters) ([]SearchResult, error) {
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
		QueryBy:  pointer.String("title,body,tags"),
		FilterBy: pointer.String(filterBy),
	}

	result, err := c.ts.Collection(collectionName).Documents().Search(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
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
			if v, ok := (*doc)["title"]; ok {
				title = fmt.Sprint(v)
			}
			if v, ok := (*doc)["body"]; ok {
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