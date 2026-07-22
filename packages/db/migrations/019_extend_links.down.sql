-- Migration 019: Down — remove the link columns added for unified linking.
-- Does NOT remove original document_id column since it was pre-existing.

ALTER TABLE document_links
    DROP COLUMN IF EXISTS link_type,
    DROP COLUMN IF EXISTS link_id,
    DROP COLUMN IF EXISTS title,
    DROP COLUMN IF EXISTS url;