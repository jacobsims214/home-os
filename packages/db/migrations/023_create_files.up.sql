-- Migration 023: Native file storage (replaces MinIO)
--
-- file_blobs holds the raw bytes of an uploaded file as BYTEA. Separating
-- the bytes into their own table keeps the heavily-queried `files` metadata
-- row small: file list / search queries scan `files` without touching the
-- (potentially large) BYTEA column, and the blob is only fetched when the
-- client streams or OCRs the file. blob rows are shared 1:1 with files
-- today; the separate table gives us room for dedup or content-addressable
-- storage later without rewriting `files`.
--
-- `files` is the metadata table: a polymorphic (entity_type, entity_id)
-- association so a file can be attached to any household entity (property,
-- vehicle, pet, bill, maintenance task, ...), plus OCR pipeline columns
-- (extracted_text, ocr_status, ocr_error) and a tags array mirroring the
-- existing `documents` table pattern.

CREATE TABLE file_blobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    data BYTEA NOT NULL,
    size BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE files (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    blob_id UUID NOT NULL REFERENCES file_blobs(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    content_type VARCHAR(255),
    size BIGINT NOT NULL,
    entity_type VARCHAR(50),
    entity_id UUID,
    extracted_text TEXT,
    ocr_status VARCHAR(50) NOT NULL DEFAULT 'pending',
    ocr_error TEXT,
    tags TEXT[],
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Household-scoped listing of files (every API request filters by household_id).
CREATE INDEX idx_files_household ON files(household_id);

-- Polymorphic entity lookup: "all files attached to this entity".
-- Mirrors idx_notes_entity / idx_document_links_entity from 020/011.
CREATE INDEX idx_files_entity ON files(entity_type, entity_id);

-- Reverse-chronological household feed (dashboard "recent files").
CREATE INDEX idx_files_household_created ON files(household_id, created_at DESC);
