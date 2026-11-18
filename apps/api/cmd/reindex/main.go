package main

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/config"
	"home-os/api/internal/file"
	"home-os/api/internal/search"
	"home-os/searchindex"
)

func main() {
	cfg, _ := config.Load()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	sc := search.NewClient(cfg)

	// Full rebuild: drop household_search and recreate it with the current
	// schema (auto-embedding field backed by Typesense's local
	// ts/all-MiniLM-L12-v2 model), then re-import every row below. Typesense
	// computes each document's embedding during the upsert. The first create
	// downloads the model into the Typesense data dir if it is not there yet.
	if err := sc.RecreateCollection(ctx); err != nil {
		log.Fatalf("recreate household_search: %v", err)
	}

	// The worker owns the files collection and is its only writer, so it is
	// not re-imported here: altering it to add the auto-embedding field makes
	// Typesense embed every existing file document in place.
	if err := sc.EnsureFilesEmbedding(ctx); err != nil {
		log.Fatalf("files embedding migration: %v", err)
	}

	// Every entity type the collection covers is rebuilt through the ONE shared
	// builder in packages/searchindex (#5314), so this tool has no document
	// knowledge of its own. It used to carry a table list here with its own
	// name/body columns — a second copy of the rules, and it wrote
	// created_at/updated_at as "now", which sorted every result by reindex time
	// instead of by when the thing actually changed. Build reads the real
	// columns, so a rebuilt index is identical to the one the queue writes.
	total := 0
	for _, typ := range searchindex.Types() {
		ids, err := searchindex.IDs(ctx, pool, typ)
		if err != nil {
			log.Printf("skip %s: %v", typ, err)
			continue
		}
		for _, id := range ids {
			doc, found, err := searchindex.Build(ctx, pool, typ, id)
			if err != nil {
				log.Printf("ERR %s/%s: %v", typ, id, err)
				continue
			}
			if !found {
				continue
			}
			if err := sc.IndexDocument(ctx, search.SearchDocument(*doc)); err != nil {
				log.Printf("ERR %s/%s: %v", typ, id, err)
			} else {
				total++
			}
		}
	}

	// Document metadata (048): one household_search document per file, built
	// by the same function the file handler uses on upload/PATCH. The OCR
	// text stays in the worker's files collection.
	if err := file.NewRepo(pool).AllForIndex(ctx, func(f *file.File, person string) error {
		if err := sc.IndexDocument(ctx, file.SearchDocument(f, person)); err != nil {
			log.Printf("ERR files/%s: %v", f.ID, err)
		} else {
			total++
		}
		return nil
	}); err != nil {
		log.Printf("skip files: %v", err)
	}

	fmt.Printf("Indexed %d documents\n", total)
}

// drainType writes the documents of every row of one entity type through the
// shared builder. It exists so cmd/reindex has no per-type document knowledge:
// the builder list in packages/searchindex is the only list.
func drainType(ctx context.Context, pool *pgxpool.Pool, sc *search.Client, typ string) error {
	ids, err := searchindex.IDs(ctx, pool, typ)
	if err != nil {
		return err
	}
	for _, id := range ids {
		doc, found, err := searchindex.Build(ctx, pool, typ, id)
		if err != nil {
			log.Printf("ERR %s/%s: %v", typ, id, err)
			continue
		}
		if !found {
			continue
		}
		if err := sc.IndexDocument(ctx, search.SearchDocument(*doc)); err != nil {
			log.Printf("ERR %s/%s: %v", typ, id, err)
		}
	}
	return nil
}
