-- 053_file_search_deletions down (#4602).
--
-- Drops the delete queue and its trigger. Files deleted after this no longer
-- leave Typesense (the bug 053 fixes), and any ids still queued are lost, so
-- their documents stay searchable until the collection is rebuilt.
--
-- Runs in one transaction.

BEGIN;

DROP TRIGGER IF EXISTS files_queue_search_deletion ON files;
DROP FUNCTION IF EXISTS queue_file_search_deletion();
DROP TABLE IF EXISTS file_search_deletions;

COMMIT;
