-- 054_change_notifications.down.sql (#4493)
--
-- Stops creator notifications. Notifications already written (type
-- 'item_changed') are kept: they are ordinary in-app notifications. IF EXISTS
-- everywhere so this also runs after 050 down has dropped activity_log.

BEGIN;

DROP TRIGGER IF EXISTS calendar_objects_notify_when ON calendar_objects;
DROP FUNCTION IF EXISTS notify_event_when();

DO $$
BEGIN
    IF to_regclass('activity_log') IS NOT NULL THEN
        DROP TRIGGER IF EXISTS activity_log_notify_creator ON activity_log;
    END IF;
END
$$;
DROP FUNCTION IF EXISTS notify_entity_creator();

DROP INDEX IF EXISTS idx_notifications_item_changed;

COMMIT;
