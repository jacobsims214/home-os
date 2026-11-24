// Package searchrecon reconciles the household_search Typesense collection
// with Postgres, which is the source of truth for every searchable record.
//
// WHY THIS EXISTS (#5298). Search promised thirteen kinds of record and could
// only find what happened to be written through an API handler that called
// search.Client.IndexDocument. Two independent holes followed:
//
//   - Calendar events had no indexer in ANY binary. apps/api/internal/calendar
//     never referenced search, and the MCP and CalDAV write calendar_objects
//     directly, so "dentist" and "soccer" could not match a real appointment.
//   - The MCP's write tools (bill_create, note writes, people, vendors, pets,
//     properties, vehicles, lists, recipes) write SQL only: the MCP builds a
//     Typesense indexer for assets alone (apps/mcp/internal/asset). The R2
//     household was written almost entirely through agents, so its bills,
//     notes and events were never indexed at all — the index held one secret.
//
// Handler-level indexing cannot close either hole: a record has several
// writers (REST, MCP, CalDAV, the worker), and a new writer silently
// reintroduces the bug. So the invariant is stated once, here: for every
// searchable table, this file is the one mapping from a Postgres row to its
// household_search document, and Reconcile makes the index converge to it.
// Both the reindex CLI (the full backfill) and the API's periodic sweep call it.
//
// A handler may still index on write for freshness, but it cannot change what
// a record's document IS.
package searchrecon

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/file"
	"home-os/api/internal/guides"
	"home-os/api/internal/kb"
	"home-os/api/internal/search"
)

// entityTypeEvent is the household_search entity_type of a calendar event.
// The UI groups it under "Events" (apps/ui/lib/search/results.ts GROUPS).
const entityTypeEvent = "calendar_event"

// source is one searchable table: the SQL that returns one document per row.
//
// Every query returns the SAME nine columns, in this order, so one scan builds
// every document: doc_id, entity_id, household_id, title, body, tags,
// property_id, created_at, updated_at. Keeping the shape uniform is what lets
// this be one mapping instead of sixteen bespoke ones.
type source struct {
	entityType string
	query      string
}

// sources lists every table the Search page's empty-state copy promises, in
// the order the UI groups them. Shared document builders (files, guides) are
// not here: building their document needs a join only their own package can
// do, so Documents appends them.
//
// The bills calendar mirror and the maintenance mirror are events in
// calendar_objects, but they are NOT indexed as events: they project a bill
// and a maintenance task that are already indexed from their own tables.
// Indexing them again would put a second row for one record in the index, and
// their description carries "Amount: $X" — exactly the leak the calendar role
// policy closed in #4807 — so the event query excludes them.
var sources = []source{
	{
		entityType: entityTypeEvent,
		query: `
			SELECT 'calendar_event-' || co.id::text,
			       co.id::text,
			       c.household_id::text,
			       COALESCE(co.ical_data::json->>'title', ''),
			       concat_ws(' ', co.ical_data::json->>'description',
			                      co.ical_data::json->>'location'),
			       '{}'::text[],
			       c.property_id::text,
			       extract(epoch FROM co.created_at)::bigint,
			       extract(epoch FROM co.updated_at)::bigint
			FROM calendar_objects co
			JOIN calendars c ON c.id = co.calendar_id
			WHERE c.household_id = $1 AND co.superseded_at IS NULL
			  AND COALESCE(co.event_type, '') NOT IN ('bill', 'maintenance')
			  AND COALESCE(co.entity_type::text, '')
			      NOT IN ('bill', 'maintenance_task', 'maintenance')`,
	},
	{
		entityType: "bill",
		query: `
			SELECT 'bill-' || b.id::text, b.id::text, b.household_id::text,
			       b.name,
			       concat_ws(' ', b.category, b.notes),
			       array_remove(ARRAY[b.category], NULL),
			       b.property_id::text,
			       extract(epoch FROM b.created_at)::bigint,
			       extract(epoch FROM b.updated_at)::bigint
			FROM bills b
			WHERE b.household_id = $1`,
	},
	{
		entityType: "note",
		query: `
			SELECT 'note-' || n.id::text, n.id::text, n.household_id::text,
			       COALESCE(n.title, ''),
			       COALESCE(n.body, ''),
			       '{}'::text[],
			       NULL::text,
			       extract(epoch FROM n.created_at)::bigint,
			       extract(epoch FROM n.updated_at)::bigint
			FROM notes n
			WHERE n.household_id = $1`,
	},
	{
		entityType: "person",
		query: `
			SELECT 'person-' || p.id::text, p.id::text, p.household_id::text,
			       p.display_name,
			       concat_ws(' ', p.relationship, p.school, p.notes,
			                      array_to_string(p.allergies, ' ')),
			       COALESCE(p.allergies, '{}'::text[]),
			       NULL::text,
			       extract(epoch FROM p.created_at)::bigint,
			       extract(epoch FROM p.updated_at)::bigint
			FROM people p
			WHERE p.household_id = $1`,
	},
	{
		entityType: "vendor",
		query: `
			SELECT 'vendor-' || v.id::text, v.id::text, v.household_id::text,
			       v.name,
			       concat_ws(' ', v.specialty, array_to_string(v.trades, ' '),
			                      v.notes, v.address),
			       COALESCE(v.trades, '{}'::text[]),
			       NULL::text,
			       extract(epoch FROM v.created_at)::bigint,
			       extract(epoch FROM v.updated_at)::bigint
			FROM vendors v
			WHERE v.household_id = $1`,
	},
	{
		entityType: "list",
		query: `
			SELECT 'list-' || l.id::text, l.id::text, l.household_id::text,
			       l.name,
			       COALESCE((SELECT string_agg(i.text, ' ' ORDER BY i.position)
			                 FROM list_items i WHERE i.list_id = l.id), ''),
			       '{}'::text[],
			       NULL::text,
			       extract(epoch FROM l.created_at)::bigint,
			       extract(epoch FROM l.updated_at)::bigint
			FROM lists l
			WHERE l.household_id = $1 AND NOT l.is_template`,
	},
	{
		entityType: "recipe",
		query: `
			SELECT 'recipe-' || r.id::text, r.id::text, r.household_id::text,
			       r.title,
			       concat_ws(' ', r.description, array_to_string(r.tags, ' '),
			                 (SELECT string_agg(e->>'name', ' ')
			                  FROM jsonb_array_elements(
			                       CASE WHEN jsonb_typeof(r.ingredients) = 'array'
			                            THEN r.ingredients ELSE '[]'::jsonb END) e)),
			       COALESCE(r.tags, '{}'::text[]),
			       NULL::text,
			       extract(epoch FROM r.created_at)::bigint,
			       extract(epoch FROM r.updated_at)::bigint
			FROM recipes r
			WHERE r.household_id = $1`,
	},
	{
		entityType: "asset",
		query: `
			SELECT 'asset-' || a.id::text, a.id::text, a.household_id::text,
			       a.name,
			       concat_ws(' ', a.category, a.manufacturer, a.model,
			                      a.serial_number, a.notes),
			       array_remove(ARRAY[a.category], NULL),
			       a.property_id::text,
			       extract(epoch FROM a.created_at)::bigint,
			       extract(epoch FROM a.updated_at)::bigint
			FROM assets a
			WHERE a.household_id = $1`,
	},
	{
		entityType: "vehicle",
		query: `
			SELECT 'vehicle-' || v.id::text, v.id::text, v.household_id::text,
			       concat_ws(' ', v.year::text, v.make, v.model),
			       concat_ws(' ', v.license_plate, v.color, v.vin, v.notes),
			       '{}'::text[],
			       NULL::text,
			       extract(epoch FROM v.created_at)::bigint,
			       extract(epoch FROM v.updated_at)::bigint
			FROM vehicles v
			WHERE v.household_id = $1`,
	},
	{
		entityType: "pet",
		query: `
			SELECT 'pet-' || p.id::text, p.id::text, p.household_id::text,
			       p.name,
			       concat_ws(' ', p.species, p.breed, p.notes,
			                      p.microchip_id, p.insurance_provider),
			       array_remove(ARRAY[p.species], NULL),
			       NULL::text,
			       extract(epoch FROM p.created_at)::bigint,
			       extract(epoch FROM p.updated_at)::bigint
			FROM pets p
			WHERE p.household_id = $1`,
	},
	{
		entityType: "property",
		query: `
			SELECT 'property-' || p.id::text, p.id::text, p.household_id::text,
			       p.name,
			       concat_ws(' ', p.address, p.property_type, p.notes),
			       '{}'::text[],
			       p.id::text,
			       extract(epoch FROM p.created_at)::bigint,
			       extract(epoch FROM p.updated_at)::bigint
			FROM properties p
			WHERE p.household_id = $1`,
	},
	{
		entityType: "loan",
		query: `
			SELECT 'loan-' || l.id::text, l.id::text, l.household_id::text,
			       l.name,
			       concat_ws(' ', l.lender, l.notes),
			       '{}'::text[],
			       l.property_id::text,
			       extract(epoch FROM l.created_at)::bigint,
			       extract(epoch FROM l.updated_at)::bigint
			FROM loans l
			WHERE l.household_id = $1`,
	},
	{
		entityType: "maintenance_task",
		query: `
			SELECT 'maintenance_task-' || m.id::text, m.id::text,
			       m.household_id::text, m.name,
			       concat_ws(' ', m.description, m.notes, m.status),
			       '{}'::text[],
			       m.property_id::text,
			       extract(epoch FROM m.created_at)::bigint,
			       extract(epoch FROM m.updated_at)::bigint
			FROM maintenance_tasks m
			WHERE m.household_id = $1`,
	},
	{
		entityType: "maintenance_schedule",
		query: `
			SELECT 'maintenance_schedule-' || m.id::text, m.id::text,
			       m.household_id::text, m.name,
			       concat_ws(' ', m.description, m.rrule),
			       '{}'::text[],
			       m.property_id::text,
			       extract(epoch FROM m.created_at)::bigint,
			       extract(epoch FROM m.updated_at)::bigint
			FROM maintenance_schedules m
			WHERE m.household_id = $1`,
	},
	{
		// A credential's document carries its name and type only, never the
		// value (the secret handler's rule, and the reason the index is safe
		// to log against). entity_type is 'secret' so search.VisibleResults
		// drops it for a role without secrets read.
		entityType: "secret",
		query: `
			SELECT 'secret-' || s.id::text, s.id::text, s.household_id::text,
			       s.name,
			       s.secret_type,
			       array_remove(ARRAY[s.secret_type, s.entity_type::text], NULL),
			       NULL::text,
			       extract(epoch FROM s.created_at)::bigint,
			       extract(epoch FROM s.updated_at)::bigint
			FROM secrets s
			WHERE s.household_id = $1`,
	},
}

// ReconcileStats reports what a run did, for the reindex CLI and the sweep log.
type ReconcileStats struct {
	Households int
	Indexed    int // documents built from Postgres
	Pruned     int // documents removed because their record is gone
}

// Reconcile makes household_search converge to Postgres for every household,
// or for one when onlyHousehold is set.
//
// A document is upserted for every searchable row, so pre-existing records are
// searchable without a write (the R2 dataset needs exactly this), and a
// document whose row no longer exists is deleted, so search never offers a
// record that is gone.
func Reconcile(ctx context.Context, pool *pgxpool.Pool, client *search.Client, onlyHousehold string) (ReconcileStats, error) {
	var stats ReconcileStats
	households, err := householdIDs(ctx, pool, onlyHousehold)
	if err != nil {
		return stats, err
	}
	stats.Households = len(households)

	for _, hh := range households {
		docs, err := Documents(ctx, pool, hh)
		if err != nil {
			return stats, err
		}
		want := make(map[string]bool, len(docs))
		for _, doc := range docs {
			if err := client.IndexDocument(ctx, doc); err != nil {
				return stats, fmt.Errorf("household %s: %w", hh, err)
			}
			want[doc.ID] = true
			stats.Indexed++
		}

		have, err := client.HouseholdDocumentIDs(ctx, hh)
		if err != nil {
			return stats, fmt.Errorf("household %s: %w", hh, err)
		}
		sort.Strings(have)
		for _, id := range have {
			if want[id] {
				continue
			}
			if err := client.DeleteDocument(ctx, id); err != nil {
				return stats, fmt.Errorf("household %s: prune %s: %w", hh, id, err)
			}
			stats.Pruned++
		}
	}
	return stats, nil
}

// householdIDs lists the households to reconcile, oldest first.
func householdIDs(ctx context.Context, pool *pgxpool.Pool, only string) ([]string, error) {
	q := `SELECT id::text FROM households`
	args := []any{}
	if only != "" {
		if _, err := uuid.Parse(only); err != nil {
			return nil, fmt.Errorf("household %q is not a uuid: %w", only, err)
		}
		q += ` WHERE id = $1`
		args = append(args, only)
	}
	q += ` ORDER BY created_at, id`
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list households: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan household: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Documents returns every household_search document for one household, built
// from Postgres. Exported so a test can assert the mapping without writing to
// Typesense, and so a future per-household reindex can reuse it.
func Documents(ctx context.Context, pool *pgxpool.Pool, householdID string) ([]search.SearchDocument, error) {
	docs := make([]search.SearchDocument, 0, 256)

	for _, src := range sources {
		built, err := scanSource(ctx, pool, src, householdID)
		if err != nil {
			return nil, err
		}
		docs = append(docs, built...)
	}

	files, err := fileDocuments(ctx, pool, householdID)
	if err != nil {
		return nil, err
	}
	docs = append(docs, files...)

	guideDocs, err := guideDocuments(ctx, pool, householdID)
	if err != nil {
		return nil, err
	}
	docs = append(docs, guideDocs...)

	kbDocs, err := kbDocuments(ctx, pool, householdID)
	if err != nil {
		return nil, err
	}
	return append(docs, kbDocs...), nil
}

// scanSource runs one source's query and builds its documents.
func scanSource(ctx context.Context, pool *pgxpool.Pool, src source, householdID string) ([]search.SearchDocument, error) {
	rows, err := pool.Query(ctx, src.query, householdID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src.entityType, err)
	}
	defer rows.Close()

	var out []search.SearchDocument
	for rows.Next() {
		var (
			docID, entityID, hh, title, body string
			tags                             []string
			propertyID                       *string
			createdAt, updatedAt             int64
		)
		if err := rows.Scan(&docID, &entityID, &hh, &title, &body, &tags,
			&propertyID, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("%s: scan: %w", src.entityType, err)
		}
		if tags == nil {
			tags = []string{}
		}
		out = append(out, search.SearchDocument{
			ID:          docID,
			HouseholdID: hh,
			EntityType:  src.entityType,
			EntityID:    entityID,
			Title:       title,
			Body:        body,
			Tags:        tags,
			PropertyID:  propertyID,
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
		})
	}
	return out, rows.Err()
}

// fileDocuments builds the metadata document of every file in the household
// with the same builder the file handler uses on upload and PATCH, so a
// reconciled document is indistinguishable from a written one (including the
// "secret-" id a credential's file gets, which hides it from roles without
// credentials read).
func fileDocuments(ctx context.Context, pool *pgxpool.Pool, householdID string) ([]search.SearchDocument, error) {
	var out []search.SearchDocument
	err := file.NewRepo(pool).ForIndexInHousehold(ctx, householdID, func(f *file.File, person string) error {
		out = append(out, file.SearchDocument(f, person))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("files: %w", err)
	}
	return out, nil
}

// guideDocuments uses the guides package's own document builder, so a guide's
// summary still leads its body (the embedding model only reads the start).
func guideDocuments(ctx context.Context, pool *pgxpool.Pool, householdID string) ([]search.SearchDocument, error) {
	var out []search.SearchDocument
	err := guides.NewRepo(pool).AllInHousehold(ctx, householdID, func(g *guides.Guide) error {
		out = append(out, guides.Document(g))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("guides: %w", err)
	}
	return out, nil
}

// kbDocuments indexes the family's knowledge base (#5502). It uses kb's own
// document builder, reached through a household-scoped read of LIVE docs
// only: a soft-deleted doc leaves the index on the next pass, because the
// reconciler prunes any document whose row it did not build.
//
// The write path never has to index for a doc to be findable. That is the
// point of #5298 applied here: agents write KB docs through the MCP, people
// write them through the REST API, and both become searchable from the same
// Postgres rows.
func kbDocuments(ctx context.Context, pool *pgxpool.Pool, householdID string) ([]search.SearchDocument, error) {
	var out []search.SearchDocument
	err := kb.NewRepo(pool).AllInHousehold(ctx, householdID, func(d *kb.Doc) error {
		out = append(out, kb.Document(d))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("kb: %w", err)
	}
	return out, nil
}
