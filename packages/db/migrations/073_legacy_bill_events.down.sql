-- 073_legacy_bill_events.down.sql (#4943)
--
-- Undoes 073: the supersede marks on calendar_objects and their matching
-- helpers.
--
-- Nothing 073 retired was deleted, so dropping superseded_at makes every
-- superseded entry live again on its own. CalDAV clients were told 'delete'
-- on the way up, so the way down tells them 'create' for the same uids and
-- bumps the ctag, the mirror image of what billcal.Sync does for a re-created
-- event. Both happen while the columns still exist.
--
-- The change rows 073 wrote to activity_log stay: they are the family's
-- history of what happened, not schema.
--
-- Runs in one transaction.

BEGIN;

-- 1. CalDAV clients get the retired entries back before the marks go away.
WITH back AS (
    SELECT co.calendar_id, co.uid
      FROM calendar_objects co
     WHERE co.superseded_at IS NOT NULL
), changes AS (
    INSERT INTO calendar_changes (calendar_id, event_uid, action)
    SELECT calendar_id, uid, 'create' FROM back
)
UPDATE calendars cal SET ctag = gen_random_uuid()::text, updated_at = NOW()
 WHERE cal.id IN (SELECT calendar_id FROM back);

-- 2. The supersede marks (the CHECK goes with the columns).
DROP INDEX IF EXISTS idx_calendar_objects_superseded;
ALTER TABLE calendar_objects DROP CONSTRAINT IF EXISTS calendar_objects_superseded_reason_check;
ALTER TABLE calendar_objects DROP COLUMN IF EXISTS superseded_reason;
ALTER TABLE calendar_objects DROP COLUMN IF EXISTS superseded_by;
ALTER TABLE calendar_objects DROP COLUMN IF EXISTS superseded_at;

-- 3. The guard (the trigger first: its function is what it runs).
DROP TRIGGER IF EXISTS bills_absorb_calendar_events ON bills;
DROP FUNCTION IF EXISTS bills_absorb_calendar_events();
DROP FUNCTION IF EXISTS calendar_absorb_bill_events(UUID);

-- 4. The matching helpers.
DROP FUNCTION IF EXISTS calendar_bill_name(TEXT);
DROP FUNCTION IF EXISTS calendar_bill_amounts(TEXT);
DROP FUNCTION IF EXISTS calendar_title_reads_as_bill(TEXT);

COMMIT;
