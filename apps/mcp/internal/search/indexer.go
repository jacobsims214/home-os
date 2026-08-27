// Package search provides Typesense indexing for the MCP server.
// It mirrors the API's search package (apps/api/internal/search/typesense.go)
// but uses direct HTTP calls to avoid adding the typesense-go dependency
// to the MCP module. If Typesense is unconfigured, the indexer is nil
// and indexing is silently skipped.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"home-os/mcp/internal/config"
)

const collectionName = "household_search"

// Document represents a document to be indexed in the Typesense household_search collection.
// Fields match the collection schema defined in apps/api/internal/search/typesense.go.
type Document struct {
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

// Indexer upserts documents into the Typesense household_search collection.
// Create with NewIndexer; if Typesense is unconfigured, NewIndexer returns nil.
type Indexer struct {
	serverURL  string
	apiKey     string
	httpClient *http.Client
}

// NewIndexer creates a new Indexer from config. Returns nil if Typesense is
// unconfigured (TYPESENSE_API_KEY is empty), in which case all indexing
// operations are silently skipped.
func NewIndexer(cfg *config.Config) *Indexer {
	if cfg.TypesenseAPIKey == "" {
		slog.Info("search: Typesense not configured, indexing disabled")
		return nil
	}
	serverURL := fmt.Sprintf("http://%s:%s", cfg.TypesenseHost, cfg.TypesensePort)
	return &Indexer{
		serverURL:  serverURL,
		apiKey:     cfg.TypesenseAPIKey,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// IndexDocument upserts a document into the household_search Typesense collection.
// Uses direct HTTP (not the typesense-go client) to avoid a nil-pointer panic
// in the client's Upsert method, matching the pattern in apps/api/internal/search/typesense.go.
func (idx *Indexer) IndexDocument(ctx context.Context, doc Document) error {
	body, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("search: marshal document: %w", err)
	}

	url := fmt.Sprintf("%s/collections/%s/documents?action=upsert", idx.serverURL, collectionName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("search: build index request: %w", err)
	}
	req.Header.Set("X-TYPESENSE-API-KEY", idx.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := idx.httpClient.Do(req)
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