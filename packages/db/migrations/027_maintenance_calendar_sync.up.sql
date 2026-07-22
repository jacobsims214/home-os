-- 027_maintenance_calendar_sync.up.sql
-- Bidirectional sync between maintenance_tasks and calendar_objects.
-- When a calendar_object with entity_type='maintenance' is deleted
-- (via API, CalDAV, or direct SQL), automatically delete the linked
-- maintenance task. This makes calendar deletions propagate to maintenance.

CREATE OR REPLACE FUNCTION delete_maintenance_on_calendar_delete()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.entity_type = 'maintenance' AND OLD.entity_id IS NOT NULL THEN
        DELETE FROM maintenance_tasks WHERE id = OLD.entity_id;
    END IF;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER maintenance_calendar_sync_trigger
    BEFORE DELETE ON calendar_objects
    FOR EACH ROW
    EXECUTE FUNCTION delete_maintenance_on_calendar_delete();
