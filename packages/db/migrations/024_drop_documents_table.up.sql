-- Migration 024: Drop the orphaned `documents` table.
--
-- The `documents` table was created in migration 010 as a Paperless-ngx
-- document cache (paperless_id, title, tags, ...). No Go model, repo, or
-- handler ever reads or writes it — the unified link system (migration 019)
-- and the native file storage (migration 023) superseded it. It is dead
-- schema.
--
-- `document_links.document_id` still carries a foreign key
-- (`document_links_document_id_fkey`) to `documents(id)` (created in
-- migration 011, made nullable in 019). PostgreSQL refuses to drop a
-- table that is still referenced by a foreign key, so the constraint must
-- be dropped first. The `document_id` column itself is retained on
-- `document_links` for backward compatibility with any pre-existing
-- rows; only the referential constraint is removed.
--
-- Roll back with 024_drop_documents_table.down.sql, which recreates the
-- table (matching migration 010) and restores the foreign key.

ALTER TABLE document_links
    DROP CONSTRAINT IF EXISTS document_links_document_id_fkey;

DROP TABLE IF EXISTS documents;
