-- 068_guide_revision_change_note.up.sql (#4772)
--
-- guide_revisions.change_note: why a save changed the guide ("new filter
-- size"), up to 500 characters, NULL when none was given. Written by MCP
-- guide_update (change_note) and API PUT /guides/{id} (change_note); an API
-- restore stores "Restored revision <id>". Existing revisions keep NULL.
--
-- Runs in one transaction. Safe to run twice.

BEGIN;

ALTER TABLE guide_revisions ADD COLUMN IF NOT EXISTS change_note TEXT;

ALTER TABLE guide_revisions DROP CONSTRAINT IF EXISTS guide_revisions_change_note_check;
ALTER TABLE guide_revisions ADD CONSTRAINT guide_revisions_change_note_check
    CHECK (change_note IS NULL OR char_length(change_note) <= 500);

COMMENT ON COLUMN guide_revisions.change_note IS
    'Why this save changed the guide (at most 500 characters); NULL when no note was given (#4772).';

COMMIT;
