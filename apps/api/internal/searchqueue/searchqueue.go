// Package searchqueue adapts the shared home-os/searchindex package to the
// API's Typesense client and starts its drain.
//
// The queue, the triggers that fill it and the document builders live in
// packages/searchindex, because apps/api and apps/mcp are separate Go modules
// and both must use the same ones. This file is only the API's side of that
// wiring: it turns *search.Client into a searchindex.Indexer.
//
// # Why indexing moved here (#5314)
//
// Indexing used to live in the API's HTTP handlers, one index call per
// handler. The MCP tool layer shares no code with the API, so a note created
// by an agent was never indexed: #5298 measured household_search at 1 document
// against 5 bills, 60 calendar events, 16 notes and 5 people in Postgres.
// Migration 075 puts a trigger on every indexed table, so a write is queued by
// whichever process made it, and packages/searchindex is the only code that
// writes household_search documents. See its package doc for the reasoning.
package searchqueue

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/search"
	"home-os/searchindex"
)

// adapter implements searchindex.Indexer over the API's client.
type adapter struct{ c *search.Client }

func (a adapter) IndexDocument(ctx context.Context, doc searchindex.Document) error {
	return a.c.IndexDocument(ctx, search.SearchDocument(doc))
}

func (a adapter) DeleteDocument(ctx context.Context, id string) error {
	return a.c.DeleteDocument(ctx, id)
}

// indexer wraps the API's client as a searchindex.Indexer. A nil client stays
// a nil interface, which disables draining (Typesense unconfigured).
func indexer(c *search.Client) searchindex.Indexer {
	if c == nil {
		return nil
	}
	return adapter{c: c}
}

// asIndexer adapts a handler-held client through the shared Document type.
type handlerAdapter struct{ c TypedIndexer }

func (a handlerAdapter) IndexDocument(ctx context.Context, doc searchindex.Document) error {
	return a.c.IndexDocument(ctx, search.SearchDocument(doc))
}

func (a handlerAdapter) DeleteDocument(ctx context.Context, id string) error {
	return a.c.DeleteDocument(ctx, id)
}

// New returns the drain worker for this process. Run it with Run.
func New(pool *pgxpool.Pool, c *search.Client) *searchindex.Worker {
	return searchindex.New(pool, indexer(c))
}

// IndexNow writes one entity's document synchronously through the shared
// builder, so the handler that just wrote the row can make it searchable
// before responding. Same builder and document as the drain.
//
// The handler's client is taken through the small interface each package
// already declares (searchIndexer), so a handler test's fake keeps working and
// no handler needs to know about *search.Client.
func IndexNow(ctx context.Context, pool *pgxpool.Pool, c TypedIndexer, entityType, entityID string) error {
	return searchindex.IndexNow(ctx, pool, handlerAdapter{c: c}, entityType, entityID)
}

// TypedIndexer is what a handler holds: the API-typed index methods. Every
// handler package declares a private copy of this interface; they are
// identical, so any of them satisfies it.
type TypedIndexer interface {
	IndexDocument(ctx context.Context, doc search.SearchDocument) error
	DeleteDocument(ctx context.Context, id string) error
}
