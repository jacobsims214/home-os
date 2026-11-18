-- 075_search_index_queue.down.sql (#5314)
--
-- Drops the queue and the triggers that fill it. Indexing goes back to
-- living in the API handlers, which is what the code did before the ticket.

BEGIN;

DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN SELECT * FROM (VALUES
        ('notes'), ('bills'), ('assets'), ('vehicles'), ('pets'),
        ('properties'), ('vendors'), ('maintenance_tasks'),
        ('maintenance_schedules'), ('secrets'), ('guides'), ('people')
    ) AS v(tbl) LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS %I ON %I', r.tbl || '_search_index', r.tbl);
    END LOOP;
END
$$;

DROP FUNCTION IF EXISTS queue_search_index_row();
DROP FUNCTION IF EXISTS enqueue_search_index(TEXT, UUID, UUID, BOOLEAN);
DROP TABLE IF EXISTS search_index_queue;

COMMIT;
