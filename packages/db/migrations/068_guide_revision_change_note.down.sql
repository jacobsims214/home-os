-- 068_guide_revision_change_note.down.sql (#4772)
--
-- Drops guide_revisions.change_note and its CHECK.
-- Lost on down: the change notes on every revision. Safe to run twice.

BEGIN;

ALTER TABLE IF EXISTS guide_revisions DROP CONSTRAINT IF EXISTS guide_revisions_change_note_check;
ALTER TABLE IF EXISTS guide_revisions DROP COLUMN IF EXISTS change_note;

COMMIT;
