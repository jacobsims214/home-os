-- Migration 024: Down — restore the orphaned `documents` table and the
-- `document_links.document_id` foreign key that pointed at it.
--
-- This recreates the exact schema produced by migration 010 and the
-- constraint added by migration 011, so the database returns to the state
-- it was in immediately after migration 023 was applied (i.e. before 024
-- ran). Existing `documents` data is NOT recoverable — the up migration
-- dropped the table unconditionally; only the schema is restored.

CREATE TABLE IF NOT EXISTS documents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    paperless_id INT,
    title VARCHAR(500) NOT NULL,
    tags TEXT[],
    paperless_created DATE,
    notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Restore the foreign key dropped by the up migration. Use `IF NOT EXISTS`
-- semantics by checking pg_constraint so the down migration is idempotent.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'document_links_document_id_fkey'
          AND conrelid = 'document_links'::regclass
    ) THEN
        ALTER TABLE document_links
            ADD CONSTRAINT document_links_document_id_fkey
            FOREIGN KEY (document_id) REFERENCES documents(id) ON DELETE CASCADE;
    END IF;
END $$;
