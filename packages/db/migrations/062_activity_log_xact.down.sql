-- 062_activity_log_xact.down.sql (#4741)
--
-- Drops activity_log.xact_id and its index. Lost on down: the recorded
-- transaction ids (polling falls back to plain id order). Tolerates 050 down
-- having run first (activity_log gone).

BEGIN;

DROP INDEX IF EXISTS idx_activity_log_household_xact;
ALTER TABLE IF EXISTS activity_log DROP COLUMN IF EXISTS xact_id;

COMMIT;
