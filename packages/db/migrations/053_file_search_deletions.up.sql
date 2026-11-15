-- 053_file_search_deletions.up.sql (#4602)
--
-- Deleted files must never stay searchable. The worker indexes each file into
-- the Typesense `files` collection as up to 300 chunk documents (#4444), but
-- nothing told it when a file was deleted, so deleted files stayed in search.
--
-- file_search_deletions is a queue of file ids whose Typesense documents must
-- be removed. An AFTER DELETE trigger on files fills it, so every delete path
-- is covered: the API's DELETE /api/v1/files/{id}, a household delete that
-- cascades to its files, and any later MCP or admin delete. Re-homing a file
-- when its entity is deleted (rehome_entity_refs, 038) is an UPDATE, so it
-- never queues anything.
--
-- The worker drains the queue every tick, after its index sweep (so an index
-- that was in flight when the file was deleted is removed too), and deletes a
-- row only after Typesense confirms the delete. The API also tries an
-- immediate delete; that is best effort and the queue row is still drained.
--
-- No foreign keys on purpose: the files row is already gone, and a FK to
-- households would make a household delete fail (the trigger inserts a row
-- that references the household being deleted in the same statement) or
-- cascade the queue row away before the worker sees it.
--
-- The blob is removed by files_delete_blob (038); this migration does not
-- touch it.
--
-- Runs in one transaction.

BEGIN;

CREATE TABLE IF NOT EXISTS file_search_deletions (
    file_id      UUID PRIMARY KEY,
    household_id UUID NOT NULL,
    deleted_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE file_search_deletions IS
    'Files deleted from Postgres whose Typesense documents the worker still has to remove (#4602).';

CREATE OR REPLACE FUNCTION queue_file_search_deletion()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO file_search_deletions (file_id, household_id)
    VALUES (OLD.id, OLD.household_id)
    ON CONFLICT (file_id) DO UPDATE SET deleted_at = NOW();
    RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS files_queue_search_deletion ON files;
CREATE TRIGGER files_queue_search_deletion AFTER DELETE ON files
    FOR EACH ROW EXECUTE FUNCTION queue_file_search_deletion();

-- Same pattern as 050: grant only where the app role exists. The trigger
-- inserts as whoever deletes the file; the worker selects and deletes.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE file_search_deletions TO app;
    END IF;
END
$$;

COMMIT;
