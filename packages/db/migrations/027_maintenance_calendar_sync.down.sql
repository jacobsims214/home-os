-- 027_maintenance_calendar_sync.down.sql
DROP TRIGGER IF EXISTS maintenance_calendar_sync_trigger ON calendar_objects;
DROP FUNCTION IF EXISTS delete_maintenance_on_calendar_delete();
