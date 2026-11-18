-- 076_search_index_backfill.up.sql (#5314)
--
-- Backfill for the rows that were never indexed because they were written
-- through the MCP tool layer (or before 075 existed).
--
-- #5298 measured the gap on the demo household: household_search held 1
-- document against 5 bills, 60 calendar events, 16 notes and 5 people in
-- Postgres. Every one of those rows is already in Postgres with everything
-- the document needs, so no separate reindex job is required: enqueueing them
-- once is enough, and the index worker (apps/api/internal/searchqueue) writes
-- them on its first tick after the migration runs.
--
-- This is a plain INSERT ... SELECT into the queue rather than a call to the
-- API's cmd/reindex: reindex drops and rebuilds the whole collection, which
-- takes Typesense's embedding model through every document again and leaves
-- search empty until it finishes. This catches up in place, in batches, while
-- search keeps answering from what it already has.
--
-- attempts starts at 0 and the worker's retry budget applies as usual, so a
-- row that Typesense rejects is retried like any other and then left for a
-- human rather than blocking the queue.
--
-- Idempotent: ON CONFLICT keeps an existing queue row's state and only
-- refreshes its timestamp, so re-running the migration is harmless.
--
-- Runs in one transaction.

BEGIN;

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
        EXECUTE format(
            'INSERT INTO search_index_queue (entity_type, entity_id, household_id, deleted)
             SELECT %L, id, household_id, FALSE FROM %I
             ON CONFLICT (entity_type, entity_id) DO UPDATE SET queued_at = NOW()',
            r.kind, r.tbl);
    END LOOP;
END
$$;

COMMIT;
