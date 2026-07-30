package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/config"
	"home-os/api/internal/search"
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
	sc.InitCollection(ctx)

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
	fmt.Printf("Indexed %d documents\n", total)
}
