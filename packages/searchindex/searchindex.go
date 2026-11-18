// Package searchindex drains search_index_queue (migration 075): the rows
// whose household_search document must be written or removed.
//
// # Why this exists (#5314)
//
// Indexing used to live in the API's HTTP handlers. Each handler called its
// own indexNote / indexBill / indexAsset after a successful write, so a row
// created through the MCP tool layer — which shares no code with the API, the
// two are separate Go modules — was never indexed. #5298 measured the result:
// household_search held 1 document while Postgres held 5 bills, 60 calendar
// events, 16 notes and 5 people.
//
// Copying the index call into every MCP tool would have fixed search and left
// the class of bug: two copies of one rule, drifting. Instead a trigger on
// each indexed table (migration 075) queues the row, so the queue is filled
// by every writer — API handler, MCP tool, psql, a future import — and this
// package is the only code that talks to Typesense for household_search.
//
// # What it writes
//
// The document is built HERE, from the row as it is when the queue is
// drained, not from a snapshot taken at write time:
//
//   - a row written twice while the worker was down produces one document,
//     not two upserts of stale values;
//   - a field added to a document builder applies to the next write of any
//     row, whichever caller makes it;
//   - the builders are the ones the handlers used (same ids, same title and
//     body rules), moved rather than rewritten, so what is indexed does not
//     change — only who triggers it.
//
// A deleted entity is deleted from the index by its document id ("note-<id>"),
// which is the same synthetic id the builders use, so no document outlives
// its row.
package searchindex

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxAttempts is how many times a row is tried before it is left alone. A
// document Typesense permanently rejects (a schema mismatch, a body over
// Typesense's size limit) must not hold the queue head forever; the row stays
// with its last_error for a human to see.
const maxAttempts = 10

// Document is one household_search document. It is declared here rather than
// imported from either app because apps/api and apps/mcp are separate Go
// modules: a shared type is what lets both of them use one builder, and each
// app adapts it to its own Typesense client (see apiAdapter / mcpAdapter).
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

// Indexer is the Typesense surface this package needs. Each app implements it
// over its own client; a test uses a fake.
type Indexer interface {
	IndexDocument(ctx context.Context, doc Document) error
	DeleteDocument(ctx context.Context, id string) error
}

// Worker drains the queue. Run it with Run; Sweep does one batch.
type Worker struct {
	pool *pgxpool.Pool
	idx  Indexer
	// batch is how many queue rows one drain reads. Small enough that a
	// Typesense outage delays one batch, not the whole backlog.
	batch int
	// interval is how often Run drains.
	interval time.Duration
}

// New returns a Worker. A nil indexer disables draining entirely (Typesense
// unconfigured): the queue keeps filling, so a later deployment with search
// configured indexes everything that accumulated instead of losing it.
func New(pool *pgxpool.Pool, idx Indexer) *Worker {
	return &Worker{pool: pool, idx: idx, batch: 200, interval: 2 * time.Second}
}

// WithInterval changes the drain period (tests and single-shot runs).
func (w *Worker) WithInterval(d time.Duration) *Worker {
	w.interval = d
	return w
}

// Run drains the queue until ctx is cancelled. It is safe to run in every API
// replica: the claim below takes each row out of the pending set before it is
// processed, so two replicas never index the same document concurrently.
func (w *Worker) Run(ctx context.Context) {
	if w.idx == nil {
		slog.Info("searchindex: Typesense not configured, not draining the index queue")
		return
	}
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := w.Sweep(ctx); err != nil {
				slog.Warn("searchindex: drain failed", "error", err)
			} else if n > 0 {
				slog.Debug("searchindex: drained", "rows", n)
			}
		}
	}
}

// queueRow is one claim on search_index_queue.
type queueRow struct {
	EntityType string
	EntityID   string
	Deleted    bool
}

// Sweep processes up to batch queue rows and reports how many it resolved.
//
// Claiming: the row is deleted from the queue first (with RETURNING, so the
// claim is atomic), then the document is written. If the process dies between
// the two, the queue row is gone and one document is missing until that row
// is written again — the trade a queue drained by an at-least-once worker
// always makes. It is the right way round here: a row left in the queue would
// be retried by every replica forever, and the backfill (migration 076) plus
// cmd/reindex both rebuild a missed document.
func (w *Worker) Sweep(ctx context.Context) (int, error) {
	if w.idx == nil {
		return 0, nil
	}
	rows, err := w.claim(ctx)
	if err != nil || len(rows) == 0 {
		return 0, err
	}

	var written int
	for _, r := range rows {
		if err := w.write(ctx, r); err != nil {
			slog.Warn("searchindex: index write failed", "entity", r.EntityType, "id", r.EntityID, "error", err)
			w.park(ctx, r, err)
			continue
		}
		written++
	}
	return written, nil
}

// claim takes up to batch rows out of the queue, oldest first, in one
// statement.
func (w *Worker) claim(ctx context.Context) ([]queueRow, error) {
	rows, err := w.pool.Query(ctx, `
		DELETE FROM search_index_queue
		 WHERE (entity_type, entity_id) IN (
		       SELECT entity_type, entity_id
		         FROM search_index_queue
		        WHERE attempts < $2
		        ORDER BY queued_at
		        LIMIT $1
		        FOR UPDATE SKIP LOCKED)
		RETURNING entity_type, entity_id, deleted`, w.batch, maxAttempts)
	if err != nil {
		return nil, fmt.Errorf("claim index queue: %w", err)
	}
	defer rows.Close()

	var out []queueRow
	for rows.Next() {
		var r queueRow
		if err := rows.Scan(&r.EntityType, &r.EntityID, &r.Deleted); err != nil {
			return nil, fmt.Errorf("scan index queue: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// write builds and writes one document. A deleted entity is removed by its
// document id; a live one is read from Postgres, and a row that has since
// disappeared (deleted after it was queued, before its delete was queued) is
// removed too, so the index cannot keep a document for a row that is gone.
func (w *Worker) write(ctx context.Context, r queueRow) error {
	docID := r.EntityType + "-" + r.EntityID
	if r.Deleted {
		return w.idx.DeleteDocument(ctx, docID)
	}
	doc, found, err := Build(ctx, w.pool, r.EntityType, r.EntityID)
	if err != nil {
		return err
	}
	if !found {
		// The row is gone: queue the delete instead of indexing nothing.
		return w.idx.DeleteDocument(ctx, docID)
	}
	return w.idx.IndexDocument(ctx, *doc)
}

// park puts a row Typesense rejected back, with its attempt counted and the
// error recorded. Once it reaches maxAttempts the claim stops picking it up
// and it stays visible for a human.
func (w *Worker) park(ctx context.Context, r queueRow, cause error) {
	msg := cause.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	if _, err := w.pool.Exec(ctx, `
		INSERT INTO search_index_queue (entity_type, entity_id, household_id, deleted, attempts, last_error)
		VALUES ($1, $2, '00000000-0000-0000-0000-000000000000', $3, 1, $4)
		ON CONFLICT (entity_type, entity_id) DO UPDATE
		   SET attempts = search_index_queue.attempts + 1,
		       last_error = EXCLUDED.last_error`,
		r.EntityType, r.EntityID, r.Deleted, msg); err != nil {
		slog.Warn("searchindex: cannot park failed row", "entity", r.EntityType, "id", r.EntityID, "error", err)
	}
}

// Pending reports how many rows are still waiting, for health checks and
// tests. It counts rows the claim would still pick up.
func Pending(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM search_index_queue WHERE attempts < $1`, maxAttempts).Scan(&n)
	return n, err
}

// Document builds the household_search document for one row, reading it from
// Postgres. found is false when the row does not exist (or was re-homed to
// another household, which no longer matches); the caller deletes the
// document then.
//
// This is the single document builder for household_search: the API handlers
// call it after a write so the user sees a search hit immediately, and the
// drain calls it for every queued row. Same function, so the two paths cannot
// produce different documents.
func Build(ctx context.Context, pool *pgxpool.Pool, entityType, entityID string) (*Document, bool, error) {
	q, ok := queries[entityType]
	if !ok {
		return nil, false, fmt.Errorf("searchindex: no document builder for entity type %q", entityType)
	}
	var (
		doc Document
		hid string
	)
	err := pool.QueryRow(ctx, q.sql, entityID).Scan(&hid, &doc.Title, &doc.Body, &doc.PropertyID, &doc.Tags, &doc.CreatedAt, &doc.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("searchindex: read %s %s: %w", entityType, entityID, err)
	}
	doc.ID = entityType + "-" + entityID
	doc.HouseholdID = hid
	doc.EntityType = entityType
	doc.EntityID = entityID
	return &doc, true, nil
}

// builder is one entity type's read query. Every query returns
// (household_id, title, body, property_id, tags, created_at, updated_at) in
// that order, so Document scans them all identically.
type builder struct{ sql string }

// queries maps entity_type to the query that builds its document. A type
// missing here cannot be indexed: the queue's trigger arguments and this map
// are the two lists that must agree, and the test in this package fails if
// migration 075 queues a type this map does not know.
//
// title and body follow what the handlers indexed before this change, so
// existing documents keep the same text and the same relevance.
var queries = map[string]builder{
	"note": {sql: `
		SELECT n.household_id, COALESCE(n.title, ''), n.body, NULL::text, '{}'::text[],
		       extract(epoch FROM n.created_at)::bigint, extract(epoch FROM n.updated_at)::bigint
		  FROM notes n WHERE n.id = $1`},

	"bill": {sql: `
		SELECT b.household_id, b.name, COALESCE(b.category, ''), b.property_id::text, '{}'::text[],
		       extract(epoch FROM b.created_at)::bigint, extract(epoch FROM b.updated_at)::bigint
		  FROM bills b WHERE b.id = $1`},

	"asset": {sql: `
		SELECT a.household_id, a.name,
		       btrim(concat_ws(' ', a.category, a.manufacturer, a.model, a.serial_number)),
		       a.property_id::text, '{}'::text[],
		       extract(epoch FROM a.created_at)::bigint, extract(epoch FROM a.updated_at)::bigint
		  FROM assets a WHERE a.id = $1`},

	"vehicle": {sql: `
		SELECT v.household_id,
		       btrim(concat_ws(' ', v.year, v.make, v.model)),
		       btrim(concat_ws(' ', v.make, v.model, v.vin, v.license_plate, v.color)),
		       NULL::text, '{}'::text[],
		       extract(epoch FROM v.created_at)::bigint, extract(epoch FROM v.updated_at)::bigint
		  FROM vehicles v WHERE v.id = $1`},

	"pet": {sql: `
		SELECT p.household_id, p.name, btrim(concat_ws(' ', p.species, p.breed)),
		       NULL::text, '{}'::text[],
		       extract(epoch FROM p.created_at)::bigint, extract(epoch FROM p.updated_at)::bigint
		  FROM pets p WHERE p.id = $1`},

	"property": {sql: `
		SELECT p.household_id, p.name,
		       btrim(concat_ws(' ', p.address, p.property_type, p.notes)),
		       NULL::text, '{}'::text[],
		       extract(epoch FROM p.created_at)::bigint, extract(epoch FROM p.updated_at)::bigint
		  FROM properties p WHERE p.id = $1`},

	"vendor": {sql: `
		SELECT v.household_id, v.name,
		       btrim(concat_ws(' ', v.specialty,
		                      (SELECT string_agg(t, ' ') FROM unnest(v.trades) AS t))),
		       NULL::text, '{}'::text[],
		       extract(epoch FROM v.created_at)::bigint, extract(epoch FROM v.updated_at)::bigint
		  FROM vendors v WHERE v.id = $1`},

	"maintenance_task": {sql: `
		SELECT t.household_id, t.name,
		       btrim(concat_ws(' ', t.description, t.status::text, t.cost::text)),
		       t.property_id::text,
		       ARRAY(
		         SELECT x FROM (VALUES
		           (CASE WHEN t.asset_id IS NOT NULL THEN 'asset:' || t.asset_id::text END),
		           (CASE WHEN t.vehicle_id IS NOT NULL THEN 'vehicle:' || t.vehicle_id::text END)
		         ) AS v(x) WHERE x IS NOT NULL),
		       extract(epoch FROM t.created_at)::bigint, extract(epoch FROM t.updated_at)::bigint
		  FROM maintenance_tasks t WHERE t.id = $1`},

	"maintenance_schedule": {sql: `
		SELECT s.household_id, s.name,
		       btrim(concat_ws(' ', s.description, s.estimated_cost::text, s.rrule)),
		       s.property_id::text,
		       ARRAY(
		         SELECT x FROM (VALUES
		           (CASE WHEN s.asset_id IS NOT NULL THEN 'asset:' || s.asset_id::text END),
		           (CASE WHEN s.vehicle_id IS NOT NULL THEN 'vehicle:' || s.vehicle_id::text END)
		         ) AS v(x) WHERE x IS NOT NULL),
		       extract(epoch FROM s.created_at)::bigint, extract(epoch FROM s.updated_at)::bigint
		  FROM maintenance_schedules s WHERE s.id = $1`},

	// A secret is indexed under its parent entity's type and id so a search
	// hit navigates to the page where it can be viewed; the document id stays
	// "secret-<id>", which is what the delete and the queue use. The body is
	// the secret's type only — never its value.
	"secret": {sql: `
		SELECT s.household_id, s.name,
		       s.secret_type ||
		         CASE WHEN COALESCE(s.entity_type, '') IN ('', 'household') THEN '' ELSE ' ' || s.entity_type END,
		       NULL::text,
		       ARRAY[s.secret_type] ||
		         CASE WHEN COALESCE(s.entity_type, '') IN ('', 'household')
		              THEN '{}'::text[] ELSE ARRAY[s.entity_type] END,
		       extract(epoch FROM s.created_at)::bigint, extract(epoch FROM s.updated_at)::bigint
		  FROM secrets s WHERE s.id = $1`},

	"guide": {sql: `
		SELECT g.household_id, g.title,
		       CASE WHEN COALESCE(g.summary, '') = '' THEN g.body
		            ELSE g.summary || E'\n\n' || g.body END,
		       g.property_id::text, g.tags,
		       extract(epoch FROM g.created_at)::bigint, extract(epoch FROM g.updated_at)::bigint
		  FROM guides g WHERE g.id = $1`},

	"person": {sql: `
		SELECT p.household_id, p.display_name,
		       btrim(concat_ws(' ', p.relationship, p.school, p.grade,
		                      array_to_string(p.allergies, ' '), p.notes)),
		       NULL::text, '{}'::text[],
		       extract(epoch FROM p.created_at)::bigint, extract(epoch FROM p.updated_at)::bigint
		  FROM people p WHERE p.id = $1`},
}

// idQueries maps entity_type to a query returning every row id of that type.
// It is the table list from queries, once, so a caller that wants to rebuild a
// whole type (cmd/reindex) does not need its own copy of the tables.
var idQueries = map[string]string{
	"note":                 `SELECT id FROM notes`,
	"bill":                 `SELECT id FROM bills`,
	"asset":                `SELECT id FROM assets`,
	"vehicle":              `SELECT id FROM vehicles`,
	"pet":                  `SELECT id FROM pets`,
	"property":             `SELECT id FROM properties`,
	"vendor":               `SELECT id FROM vendors`,
	"maintenance_task":     `SELECT id FROM maintenance_tasks`,
	"maintenance_schedule": `SELECT id FROM maintenance_schedules`,
	"secret":               `SELECT id FROM secrets`,
	"guide":                `SELECT id FROM guides`,
	"person":               `SELECT id FROM people`,
}

// IDs returns every row id of one entity type, for a full rebuild. It is here
// so the table list lives beside the document builders: a type that gains a
// builder must gain an id query in the same edit.
func IDs(ctx context.Context, pool *pgxpool.Pool, entityType string) ([]string, error) {
	q, ok := idQueries[entityType]
	if !ok {
		return nil, fmt.Errorf("searchindex: no id query for entity type %q", entityType)
	}
	rows, err := pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("searchindex: list %s ids: %w", entityType, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("searchindex: scan %s id: %w", entityType, err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Types returns the entity types this package can build documents for. The
// test that reads migration 075 uses it to prove the trigger list and the
// builder list agree.
func Types() []string {
	out := make([]string, 0, len(queries))
	for k := range queries {
		out = append(out, k)
	}
	return out
}

// IndexNow writes one entity's document synchronously, so a caller can make a
// write searchable before it responds. It is the same builder and the same
// document the drain writes, so the two cannot disagree; a failure is
// returned for the caller to log (the queue row the trigger already wrote
// will be retried regardless).
func IndexNow(ctx context.Context, pool *pgxpool.Pool, idx Indexer, entityType, entityID string) error {
	if idx == nil {
		return nil
	}
	doc, found, err := Build(ctx, pool, entityType, entityID)
	if err != nil {
		return err
	}
	if !found {
		return idx.DeleteDocument(ctx, entityType+"-"+entityID)
	}
	return idx.IndexDocument(ctx, *doc)
}
