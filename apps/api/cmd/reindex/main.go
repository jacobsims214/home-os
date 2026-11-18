package main

import (
	"context"
	"fmt"
	"log"
	"time"

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

	type entityDef struct {
		table      string
		entityType string
		nameCol    string
		bodyCol    string
	}

	entities := []entityDef{
		{"assets", "asset", "name", "COALESCE(notes,'')"},
		{"vehicles", "vehicle", "make || ' ' || model", "COALESCE(notes,'')"},
		{"pets", "pet", "name", "COALESCE(notes,'')"},
		{"vendors", "vendor", "name", "COALESCE(notes,'') || ' ' || COALESCE(specialty,'')"},
		{"properties", "property", "name", "COALESCE(notes,'') || ' ' || COALESCE(address,'')"},
		{"bills", "bill", "name", "COALESCE(category,'')"},
		{"notes", "note", "COALESCE(title,'')", "COALESCE(body,'')"},
		{"secrets", "secret", "name", "secret_type"},
	}

	total := 0
	for _, e := range entities {
		sql := fmt.Sprintf(
			`SELECT id::text, household_id::text, %s AS name, %s AS body FROM %s`,
			e.nameCol, e.bodyCol, e.table)
		rows, err := pool.Query(ctx, sql)
		if err != nil {
			log.Printf("skip %s: %v", e.table, err)
			continue
		}
		for rows.Next() {
			var id, householdID, name, body string
			if err := rows.Scan(&id, &householdID, &name, &body); err != nil {
				continue
			}
			doc := search.SearchDocument{
				ID:          e.entityType + "-" + id,
				HouseholdID: householdID,
				EntityType:  e.entityType,
				EntityID:    id,
				Title:       name,
				Body:        body,
				CreatedAt:   time.Now().Unix(),
				UpdatedAt:   time.Now().Unix(),
			}
			if err := sc.IndexDocument(ctx, doc); err != nil {
				log.Printf("ERR %s/%s: %v", e.table, id, err)
			} else {
				total++
			}
		}
		rows.Close()
	}
	// Guides, and every other type whose document needs more than a name and a
	// body (tags, property_id, the secret parent rule), are built by the ONE
	// shared builder in packages/searchindex (#5314) rather than a copy here.
	// 075's triggers queue every row; this drains what the loop above did not
	// write. Re-running it is harmless — the builder is deterministic.
	for _, typ := range searchindex.Types() {
		if err := drainType(ctx, pool, sc, typ); err != nil {
			log.Printf("skip %s: %v", typ, err)
			continue
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
