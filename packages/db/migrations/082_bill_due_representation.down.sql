-- 082_bill_due_representation.down.sql (#5511)
--
-- Reverses 082_bill_due_representation.up.sql: restores the due_date values
-- the up migration cleared on monthly bills (from the notes lines it wrote),
-- drops the two constraints, and clears the three column comments.
--
-- What cannot come back, and is not pretended to:
--
--   * A monthly bill that carried BOTH a due_day and a due_date gets its
--     due_date back, so pre-082 code sees what it saw before. A monthly bill
--     that had a due_date but NO due_day has no schedule; the up migration
--     recorded "not set, so the bill has no schedule" rather than a date, so
--     there is no value to restore and none is invented.
--   * A due_day outside 1-31 that the up migration cleared is not restored:
--     no reader could resolve it, and bringing the impossible value back
--     would only restore a state where the bill has no schedule anyway. It
--     stays in the notes.
--   * The comments go back to being absent: before 082 there were none, so
--     there is nothing to restore.

BEGIN;

-- The constraints go FIRST: restoring a monthly bill's due_date is exactly
-- the state bills_schedule_one_representation_check forbids, so the restore
-- below cannot run while it exists.
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_schedule_one_representation_check;
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_due_day_range_check;

-- Restore each monthly bill's cleared due_date from the line the up
-- migration appended. Only rows whose line recorded a real date are touched.
UPDATE bills
   SET due_date = (regexp_match(notes,
           'Migration 082: this bill is monthly, so its due day is day [0-9]+; '
           'the due date ([0-9]{4}-[0-9]{2}-[0-9]{2}) it also carried'))[1]::date
 WHERE frequency = 'monthly'
   AND due_date IS NULL
   AND (regexp_match(notes,
           'Migration 082: this bill is monthly, so its due day is day [0-9]+; '
           'the due date ([0-9]{4}-[0-9]{2}-[0-9]{2}) it also carried'))[1] IS NOT NULL;

-- Drop the lines the up migration appended, so a re-run of up does not stack
-- a second copy on top of the first (the 077 precedent: the down of an
-- append-to-notes migration takes its own lines back out).
UPDATE bills
   SET notes = nullif(btrim(
           regexp_replace(notes, '(^|\n)Migration 082: [^\n]*', '', 'g'),
           E' \t\r\n'), '')
 WHERE notes LIKE '%Migration 082: %';

COMMENT ON COLUMN bills.due_day IS NULL;
COMMENT ON COLUMN bills.due_date IS NULL;
COMMENT ON COLUMN bills.rrule IS NULL;

COMMIT;
