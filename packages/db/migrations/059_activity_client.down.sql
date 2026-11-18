-- 059_activity_client.down.sql (#4491)
--
-- Drops activity_log.actor_client_id. Lost on down: the recorded OAuth
-- client ids. Tolerates 050 down having run first (activity_log gone).

BEGIN;

ALTER TABLE IF EXISTS activity_log DROP COLUMN IF EXISTS actor_client_id;

COMMIT;
