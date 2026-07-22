-- Migration 023: Down — revert native file storage.
-- Reverse order of up migration (files depends on file_blobs).

DROP TABLE IF EXISTS files;
DROP TABLE IF EXISTS file_blobs;
