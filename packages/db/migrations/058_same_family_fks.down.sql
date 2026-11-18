-- 058_same_family_fks.down.sql (#4697)
--
-- Restores the single-column FKs (038's definitions for the vendor and
-- schedule columns, 017's for calendars.property_id) with the same delete
-- actions, and drops the (id, household_id) keys 058 added on properties and
-- maintenance_schedules. vendors_id_household_id_key belongs to 043 and stays.
-- Rows repaired by 058 up stay repaired (the cross-family links are not
-- recoverable, and were never valid).

BEGIN;

ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_vendor_id_fkey;
ALTER TABLE bills ADD CONSTRAINT bills_vendor_id_fkey
    FOREIGN KEY (vendor_id) REFERENCES vendors(id) ON DELETE SET NULL;

ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_vendor_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_vendor_id_fkey
    FOREIGN KEY (vendor_id) REFERENCES vendors(id) ON DELETE SET NULL;

ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_vendor_id_fkey;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_vendor_id_fkey
    FOREIGN KEY (vendor_id) REFERENCES vendors(id) ON DELETE SET NULL;

ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_schedule_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_schedule_id_fkey
    FOREIGN KEY (schedule_id) REFERENCES maintenance_schedules(id) ON DELETE SET NULL;

ALTER TABLE calendars DROP CONSTRAINT IF EXISTS calendars_property_id_fkey;
ALTER TABLE calendars ADD CONSTRAINT calendars_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id) ON DELETE CASCADE;

ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_id_household_id_key;
ALTER TABLE properties DROP CONSTRAINT IF EXISTS properties_id_household_id_key;

COMMIT;
