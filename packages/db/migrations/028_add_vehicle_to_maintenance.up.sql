-- 028_add_vehicle_to_maintenance.up.sql
-- Add vehicle_id column to maintenance_tasks so maintenance tasks
-- can be associated with vehicles (oil changes, tire rotations, etc.)
ALTER TABLE maintenance_tasks ADD COLUMN IF NOT EXISTS vehicle_id UUID REFERENCES vehicles(id) ON DELETE SET NULL;

-- Also add to maintenance_schedules for recurring vehicle maintenance
ALTER TABLE maintenance_schedules ADD COLUMN IF NOT EXISTS vehicle_id UUID REFERENCES vehicles(id) ON DELETE SET NULL;
