-- Migration 019: Extend document_links table to support unified entity-to-resource linking.
-- Adds link_type, link_id, title, and url columns alongside the existing document_id
-- for backward compatibility. Existing rows are migrated: their document_id becomes
-- link_id with link_type='paperless'.
--
-- New link_type values: 'paperless', 'vaultwarden', 'minio'
-- This enables any entity (asset, property, vehicle, pet, vendor, bill, maintenance_task)
-- to link to paperless documents, vaultwarden secrets, or minio files.
--
-- document_id is made nullable below (DROP NOT NULL) so new links of type
-- 'vaultwarden' or 'minio' can be created without a paperless document.
-- Existing paperless rows keep their document_id and also populate link_id.

ALTER TABLE document_links
    ADD COLUMN IF NOT EXISTS link_type VARCHAR(50) NOT NULL DEFAULT 'paperless',
    ADD COLUMN IF NOT EXISTS link_id VARCHAR(500),
    ADD COLUMN IF NOT EXISTS title VARCHAR(500),
    ADD COLUMN IF NOT EXISTS url TEXT;

-- Make document_id nullable so new links (vaultwarden, minio) work without it
ALTER TABLE document_links ALTER COLUMN document_id DROP NOT NULL;

-- Migrate existing data: copy document_id into link_id for all existing rows
UPDATE document_links
SET link_id = document_id::text,
    link_type = 'paperless'
WHERE document_id IS NOT NULL AND link_id IS NULL;