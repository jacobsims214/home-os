-- 048_document_metadata.down.sql
--
-- Removes the document metadata columns from files (ticket #4456).
--
-- Lost on down: every file's title, category, document_date, expires_on,
-- description and person_id. The files themselves, their blobs and their
-- subjects are untouched.
--
-- Run this before 041 down: files_person_fkey references people.

BEGIN;

DROP INDEX IF EXISTS idx_files_person;
DROP INDEX IF EXISTS idx_files_expires_on;
DROP INDEX IF EXISTS idx_files_household_category;

ALTER TABLE files
    DROP CONSTRAINT IF EXISTS files_person_fkey,
    DROP CONSTRAINT IF EXISTS files_expires_after_document_date,
    DROP CONSTRAINT IF EXISTS files_title_not_blank,
    DROP CONSTRAINT IF EXISTS files_category_check;

ALTER TABLE files
    DROP COLUMN IF EXISTS person_id,
    DROP COLUMN IF EXISTS description,
    DROP COLUMN IF EXISTS expires_on,
    DROP COLUMN IF EXISTS document_date,
    DROP COLUMN IF EXISTS category,
    DROP COLUMN IF EXISTS title;

COMMIT;
