-- 078_bill_reminder_dedupe.down.sql (#5500)
--
-- Drops the family-wide dedupe index; 056's per-member index is untouched.
-- Bill rows already written keep their dedupe_key, which is inert without the
-- index (and would be re-unique if this migration is re-applied).

BEGIN;

DROP INDEX IF EXISTS idx_notifications_dedupe_family;

COMMIT;
