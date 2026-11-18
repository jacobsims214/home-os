// Package searchindex adapts the MCP server's Typesense indexer to the shared
// home-os/searchindex package, so MCP writes and API writes index through the
// same document builder (#5314).
//
// Before this, the MCP tools that indexed at all (assets, guides) built their
// own documents and every other tool indexed nothing: note_create,
// bill_create and the rest wrote Postgres and stopped. Migration 075's
// triggers now queue every such write, and the API's drain writes the
// document, so an MCP write is searchable without the tool doing anything.
//
// The adapter still exists because a tool may want the document written before
// it answers. It uses the shared builder, never a second copy of the rules.
package searchindex

import (
	"context"

	"home-os/mcp/internal/search"
	shared "home-os/searchindex"
)

// adapter implements shared.Indexer over the MCP's indexer.
type adapter struct{ idx *search.Indexer }

func (a adapter) IndexDocument(ctx context.Context, doc shared.Document) error {
	return a.idx.IndexDocument(ctx, search.Document{
		ID: doc.ID, HouseholdID: doc.HouseholdID, EntityType: doc.EntityType,
		EntityID: doc.EntityID, Title: doc.Title, Body: doc.Body, Tags: doc.Tags,
		PropertyID: doc.PropertyID, CreatedAt: doc.CreatedAt, UpdatedAt: doc.UpdatedAt,
	})
}

func (a adapter) DeleteDocument(ctx context.Context, id string) error {
	return a.idx.DeleteDocument(ctx, id)
}

// Indexer wraps the MCP's indexer; nil stays nil (Typesense unconfigured).
func Indexer(idx *search.Indexer) shared.Indexer {
	if idx == nil {
		return nil
	}
	return adapter{idx: idx}
}
