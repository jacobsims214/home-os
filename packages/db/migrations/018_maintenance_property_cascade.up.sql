-- Migration 018: Add ON DELETE CASCADE to maintenance FK references to properties
-- This ensures deleting a property cascades to its maintenance tasks and schedules,
-- matching the pattern already used by rooms (002), calendars (017), and households (017).

ALTER TABLE maintenance_tasks
    DROP CONSTRAINT maintenance_tasks_property_id_fkey,
    ADD CONSTRAINT maintenance_tasks_property_id_fkey
        FOREIGN KEY (property_id) REFERENCES properties(id) ON DELETE CASCADE;

ALTER TABLE maintenance_schedules
    DROP CONSTRAINT maintenance_schedules_property_id_fkey,
    ADD CONSTRAINT maintenance_schedules_property_id_fkey
        FOREIGN KEY (property_id) REFERENCES properties(id) ON DELETE CASCADE;