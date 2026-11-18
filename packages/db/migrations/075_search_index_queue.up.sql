-- 075_search_index_queue.up.sql (#5314)
--
-- Every write that should be searchable must reach Typesense, whichever
-- caller made it. It did not: indexing lived inside the API's HTTP handlers
-- (h.indexNote, h.indexBill, ...), so a note created through the MCP tool
-- layer was never indexed. #5298 found the result: household_search held 1
-- document while Postgres held 5 bills, 60 calendar events, 16 notes and 5
-- people. The API's handlers were the only path that indexed.
--
-- The fix is NOT to copy the handler's index call into the MCP tools. That
-- makes two copies of one rule and they drift: the next entity type, the next
-- field added to a document, the next "skip secrets" rule would have to be
-- written twice, and the second copy is the one nobody remembers. It is also
-- not possible to share an in-process layer: apps/api and apps/mcp are
-- separate Go modules (go.work), so neither can import the other's services.
--
-- The one layer both callers already go through is Postgres. So the trigger
-- on each indexed table is the write-path layer, exactly as 050 made
-- activity_log a trigger (every writer logs) and 053 made file deletions a
-- trigger-fed queue. search_index_queue is a queue of (entity, id) rows that
-- must be (re)indexed or unindexed; an AFTER INSERT OR UPDATE OR DELETE
-- trigger on every table the search index covers fills it, so a write is
-- queued no matter which process made it.
--
-- Who drains it: apps/api's index worker (internal/searchqueue), which owns
-- the Typesense client and the document builders. It builds the document from
-- the ROW AS IT IS NOW - not from a payload in the queue - so a row written
-- and updated twice collapses into one document, and the index can never hold
-- a stale snapshot that was captured at write time.
--
-- Why a queue and not a trigger that calls Typesense: a trigger cannot make
-- an HTTP call, and doing it in the transaction would make every write fail
-- when Typesense is down. Indexing is best-effort by design (the handlers
-- already logged and continued); the queue keeps that property and adds
-- retry, which the handlers never had.
--
-- Deletes: the row is kept with deleted = true, so the drain deletes
-- "type-id" from the index. No FK on purpose - the entity row is already gone
-- (same reasoning as 053).
--
-- Backfill: see 076, which enqueues every existing row once so the index
-- catches up with Postgres on deploy. Running the API's cmd/reindex instead
-- is still supported and does the same thing more slowly (it rebuilds the
-- collection from scratch).
--
-- Runs in one transaction.

BEGIN;

CREATE TABLE IF NOT EXISTS search_index_queue (
    entity_type  TEXT        NOT NULL,
    entity_id    UUID        NOT NULL,
    household_id UUID        NOT NULL,
    deleted      BOOLEAN     NOT NULL DEFAULT FALSE,
    queued_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    attempts     INT         NOT NULL DEFAULT 0,
    last_error   TEXT,
    PRIMARY KEY (entity_type, entity_id)
);

COMMENT ON TABLE search_index_queue IS
    'Rows whose household_search document must be written (or removed) by the index worker (#5314). Filled by triggers on every indexed table, so API and MCP writes are treated alike.';

-- The drain reads oldest first and only what it has not given up on.
CREATE INDEX IF NOT EXISTS idx_search_index_queue_pending
    ON search_index_queue (queued_at)
    WHERE attempts < 10;

-- enqueue_search_index() is the one place the queue is written. Re-queueing a
-- row that is already waiting updates its timestamp; a pending write resets
-- the retry budget, a delete does not (a row Typesense keeps rejecting must
-- not be retried forever because something touched it).
CREATE OR REPLACE FUNCTION enqueue_search_index(
    p_entity_type TEXT, p_entity_id UUID, p_household_id UUID, p_deleted BOOLEAN
) RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO search_index_queue (entity_type, entity_id, household_id, deleted, queued_at)
    VALUES (p_entity_type, p_entity_id, p_household_id, p_deleted, NOW())
    ON CONFLICT (entity_type, entity_id) DO UPDATE
        SET deleted      = EXCLUDED.deleted,
            household_id = EXCLUDED.household_id,
            queued_at    = NOW(),
            attempts     = CASE WHEN EXCLUDED.deleted THEN search_index_queue.attempts ELSE 0 END,
            last_error   = NULL;
END;
$$;

-- queue_search_index_row() is the trigger function every indexed table gets.
-- It is generic: the table's entity type arrives as a trigger argument and is
-- the same string the handlers used as the document id prefix ("note-<id>"),
-- so documents keep their existing ids and nothing in Typesense is orphaned
-- by this migration.
CREATE OR REPLACE FUNCTION queue_search_index_row()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_type TEXT := TG_ARGV[0];
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM enqueue_search_index(v_type, OLD.id, OLD.household_id, TRUE);
        RETURN NULL;
    END IF;

    -- Re-homing a row to another household (038's rehome_entity_refs) moves
    -- its document between households. The document id is the same, so one
    -- queued write carries the new household_id; nothing extra is needed.
    PERFORM enqueue_search_index(v_type, NEW.id, NEW.household_id, FALSE);
    RETURN NULL;
END;
$$;

-- Every table the household_search collection covers. The entity_type strings
-- are the ones the API's handlers and cmd/reindex already write, so this
-- changes WHICH writes are indexed, not what a document looks like.
--
-- Excluded on purpose:
--   calendar_objects / calendar_events - not in household_search (the
--     calendar service serves them), so they get no trigger.
--   files - the worker owns their documents and their deletions (053) and
--     the API's file handler indexes metadata directly. Left as it is so this
--     ticket does not change the files path.
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN SELECT * FROM (VALUES
        ('notes',                 'note'),
        ('bills',                 'bill'),
        ('assets',                'asset'),
        ('vehicles',              'vehicle'),
        ('pets',                  'pet'),
        ('properties',            'property'),
        ('vendors',               'vendor'),
        ('maintenance_tasks',     'maintenance_task'),
        ('maintenance_schedules', 'maintenance_schedule'),
        ('secrets',               'secret'),
        ('guides',                'guide'),
        ('people',                'person')
    ) AS v(tbl, kind) LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS %I ON %I', r.tbl || '_search_index', r.tbl);
        EXECUTE format(
            'CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I
                 FOR EACH ROW EXECUTE FUNCTION queue_search_index_row(%L)',
            r.tbl || '_search_index', r.tbl, r.kind);
    END LOOP;
END
$$;

-- Same grant pattern as 050 and 053: only where the app role exists. Every
-- caller (api, mcp, worker) writes as that role, so all of them queue.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE search_index_queue TO app;
    END IF;
END
$$;

COMMIT;
