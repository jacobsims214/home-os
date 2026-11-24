package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/config"
	"home-os/api/internal/search"
	"home-os/api/internal/searchrecon"
)

// reindex rebuilds the household_search Typesense collection from Postgres.
//
// Postgres is the source of truth; the index is a derived copy of it. This
// tool used to carry its own ad-hoc SQL per table, which is how calendar
// events came to be missing entirely (#5298): the list here and the list the
// handlers wrote had nothing in common. The mapping now lives in
// searchrecon.Documents, the same code the API's periodic sweep runs, so a
// record that is searchable after a write is searchable after a reindex.
func main() {
	household := flag.String("household", "", "reindex one household id only (default: every household)")
	keepCollection := flag.Bool("keep", false, "keep the existing collection and reconcile into it (default: drop and recreate)")
	flag.Parse()

	cfg, _ := config.Load()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	sc := search.NewClient(cfg)

	if *keepCollection {
		// Reconcile in place: upsert every document, prune the ones whose
		// record is gone. This is what the API's sweep does per tick, so a
		// -keep run reproduces exactly what a healthy deployment converges to.
		if err := sc.InitCollection(ctx); err != nil {
			log.Fatalf("init collection: %v", err)
		}
		start := time.Now()
		stats, err := searchrecon.Reconcile(ctx, pool, sc, *household)
		if err != nil {
			log.Fatalf("reconcile: %v", err)
		}
		fmt.Printf("Reconciled %d households: %d documents indexed, %d pruned (took %s)\n",
			stats.Households, stats.Indexed, stats.Pruned, time.Since(start).Round(time.Millisecond))
		return
	}

	// Full rebuild: drop household_search and recreate it with the current
	// schema (auto-embedding field backed by Typesense's local
	// ts/all-MiniLM-L12-v2 model), then re-import every row. Typesense
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

	start := time.Now()
	stats, err := searchrecon.Reconcile(ctx, pool, sc, *household)
	if err != nil {
		log.Fatalf("index from postgres: %v", err)
	}
	fmt.Printf("Indexed %d documents across %d households (took %s)\n",
		stats.Indexed, stats.Households, time.Since(start).Round(time.Millisecond))
}
