-- Migration 018: Down — revert ON DELETE CASCADE to simple REFERENCES

ALTER TABLE maintenance_tasks
    DROP CONSTRAINT maintenance_tasks_property_id_fkey,
    ADD CONSTRAINT maintenance_tasks_property_id_fkey
        FOREIGN KEY (property_id) REFERENCES properties(id);

ALTER TABLE maintenance_schedules
    DROP CONSTRAINT maintenance_schedules_property_id_fkey,
    ADD CONSTRAINT maintenance_schedules_property_id_fkey
        FOREIGN KEY (property_id) REFERENCES properties(id);