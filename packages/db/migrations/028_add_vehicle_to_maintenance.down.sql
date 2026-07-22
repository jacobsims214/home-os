-- 028_add_vehicle_to_maintenance.down.sql
ALTER TABLE maintenance_tasks DROP COLUMN IF EXISTS vehicle_id;
ALTER TABLE maintenance_schedules DROP COLUMN IF EXISTS vehicle_id;
