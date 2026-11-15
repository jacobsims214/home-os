-- 035_mcp_hot_path_indexes down (#4387).
--
-- Drops what 035 up added: the trigram index (and the pg_trgm extension when
-- nothing else uses it), the household indexes, and calendar_objects'
-- trigger-maintained starts_at / ends_at with their index, trigger and
-- functions. ical_data is untouched, so no event data is lost; pre-035 code
-- reads start/end from the JSON.
--
-- Runs in one transaction.

BEGIN;

DROP INDEX IF EXISTS idx_calendar_objects_ical_trgm;
DO $$
BEGIN
    DROP EXTENSION IF EXISTS pg_trgm RESTRICT;
EXCEPTION
    WHEN dependent_objects_still_exist OR insufficient_privilege THEN
        RAISE NOTICE '035 down: keeping pg_trgm (%)', SQLERRM;
END;
$$;

DROP INDEX IF EXISTS idx_notifications_household;
DROP INDEX IF EXISTS idx_vendors_household;
DROP INDEX IF EXISTS idx_maintenance_schedules_household;
DROP INDEX IF EXISTS idx_maintenance_tasks_household;
DROP INDEX IF EXISTS idx_bills_household;
DROP INDEX IF EXISTS idx_calendars_household;

DROP INDEX IF EXISTS idx_calendar_objects_calendar_starts;
DROP TRIGGER IF EXISTS calendar_objects_event_times ON calendar_objects;
DROP FUNCTION IF EXISTS calendar_objects_set_event_times();
ALTER TABLE calendar_objects DROP COLUMN IF EXISTS ends_at;
ALTER TABLE calendar_objects DROP COLUMN IF EXISTS starts_at;
DROP FUNCTION IF EXISTS calendar_event_instant(TEXT, TEXT);

COMMIT;
