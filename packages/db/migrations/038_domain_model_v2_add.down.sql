-- 038_domain_model_v2_add.down.sql
--
-- Restores the 037 schema. Run after 039's down (golang-migrate does), which
-- turns the entity_kind columns back into VARCHAR so the domain can be dropped.
-- Data written into the new columns (rooms.household_id, document_links.
-- household_id, bills.vehicle_id/asset_id, loans.property_id/vehicle_id/
-- asset_id) is dropped with them; the legacy columns still hold what 039 read.

BEGIN;

-- 6. Remaining delete rules
-- Invitations whose inviter was deleted cannot satisfy NOT NULL again.
DELETE FROM invitations WHERE invited_by IS NULL;
ALTER TABLE invitations DROP CONSTRAINT IF EXISTS invitations_invited_by_fkey;
ALTER TABLE invitations ADD CONSTRAINT invitations_invited_by_fkey
    FOREIGN KEY (invited_by) REFERENCES users(id);
ALTER TABLE invitations ALTER COLUMN invited_by SET NOT NULL;

ALTER TABLE notifications DROP CONSTRAINT IF EXISTS notifications_user_id_fkey;
ALTER TABLE notifications ADD CONSTRAINT notifications_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id);

ALTER TABLE notes DROP CONSTRAINT IF EXISTS notes_author_id_fkey;
ALTER TABLE notes ADD CONSTRAINT notes_author_id_fkey
    FOREIGN KEY (author_id) REFERENCES users(id);

ALTER TABLE vendors DROP CONSTRAINT IF EXISTS vendors_property_id_fkey;
ALTER TABLE vendors ADD CONSTRAINT vendors_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id);

-- 5. Subject columns
DROP TRIGGER IF EXISTS assets_propagate_property ON assets;
DROP FUNCTION IF EXISTS assets_propagate_property();
DROP TRIGGER IF EXISTS bills_derive_subject_property ON bills;
DROP TRIGGER IF EXISTS loans_derive_subject_property ON loans;
DROP TRIGGER IF EXISTS maintenance_schedules_derive_subject_property ON maintenance_schedules;
DROP TRIGGER IF EXISTS maintenance_tasks_derive_subject_property ON maintenance_tasks;
DROP FUNCTION IF EXISTS derive_subject_property();

ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_schedule_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_schedule_id_fkey
    FOREIGN KEY (schedule_id) REFERENCES maintenance_schedules(id);
ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_vendor_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_vendor_id_fkey
    FOREIGN KEY (vendor_id) REFERENCES vendors(id);
ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_vehicle_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_vehicle_id_fkey
    FOREIGN KEY (vehicle_id) REFERENCES vehicles(id) ON DELETE SET NULL;
ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_asset_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_asset_id_fkey
    FOREIGN KEY (asset_id) REFERENCES assets(id);

ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_vendor_id_fkey;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_vendor_id_fkey
    FOREIGN KEY (vendor_id) REFERENCES vendors(id);
ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_vehicle_id_fkey;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_vehicle_id_fkey
    FOREIGN KEY (vehicle_id) REFERENCES vehicles(id) ON DELETE SET NULL;
ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_asset_id_fkey;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_asset_id_fkey
    FOREIGN KEY (asset_id) REFERENCES assets(id);

DROP INDEX IF EXISTS idx_loans_property;
DROP INDEX IF EXISTS idx_loans_vehicle;
DROP INDEX IF EXISTS idx_loans_asset;
ALTER TABLE loans DROP CONSTRAINT IF EXISTS loans_subject_check;
ALTER TABLE loans DROP COLUMN IF EXISTS asset_id;
ALTER TABLE loans DROP COLUMN IF EXISTS vehicle_id;
ALTER TABLE loans DROP COLUMN IF EXISTS property_id;

DROP INDEX IF EXISTS idx_bills_vehicle;
DROP INDEX IF EXISTS idx_bills_asset;
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_subject_check;
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_vendor_id_fkey;
ALTER TABLE bills ADD CONSTRAINT bills_vendor_id_fkey
    FOREIGN KEY (vendor_id) REFERENCES vendors(id);
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_property_id_fkey;
ALTER TABLE bills ADD CONSTRAINT bills_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id);
ALTER TABLE bills DROP COLUMN IF EXISTS asset_id;
ALTER TABLE bills DROP COLUMN IF EXISTS vehicle_id;

-- 4. Location hierarchy
DROP TRIGGER IF EXISTS assets_derive_property ON assets;
DROP FUNCTION IF EXISTS assets_derive_property();
ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_room_id_fkey;
ALTER TABLE assets ADD CONSTRAINT assets_room_id_fkey
    FOREIGN KEY (room_id) REFERENCES rooms(id);
ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_property_id_fkey;
ALTER TABLE assets ADD CONSTRAINT assets_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id);

DROP TRIGGER IF EXISTS rooms_set_household ON rooms;
DROP FUNCTION IF EXISTS rooms_set_household();
DROP INDEX IF EXISTS idx_rooms_household;
ALTER TABLE rooms DROP CONSTRAINT IF EXISTS rooms_id_property_id_key;
ALTER TABLE rooms DROP COLUMN IF EXISTS household_id;

-- 3. Rehome on delete, blob cleanup, calendar 027
CREATE OR REPLACE FUNCTION delete_maintenance_on_calendar_delete()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.entity_type = 'maintenance' AND OLD.entity_id IS NOT NULL THEN
        DELETE FROM maintenance_tasks WHERE id = OLD.entity_id;
    END IF;
    RETURN OLD;
END;
$$;
DROP TRIGGER IF EXISTS maintenance_calendar_sync_trigger ON calendar_objects;
CREATE TRIGGER maintenance_calendar_sync_trigger
    BEFORE DELETE ON calendar_objects
    FOR EACH ROW EXECUTE FUNCTION delete_maintenance_on_calendar_delete();

DROP TRIGGER IF EXISTS files_delete_blob ON files;
DROP FUNCTION IF EXISTS delete_file_blob();

DROP TRIGGER IF EXISTS properties_rehome_entity_refs ON properties;
DROP TRIGGER IF EXISTS rooms_rehome_entity_refs ON rooms;
DROP TRIGGER IF EXISTS assets_rehome_entity_refs ON assets;
DROP TRIGGER IF EXISTS vehicles_rehome_entity_refs ON vehicles;
DROP TRIGGER IF EXISTS pets_rehome_entity_refs ON pets;
DROP TRIGGER IF EXISTS vendors_rehome_entity_refs ON vendors;
DROP TRIGGER IF EXISTS bills_rehome_entity_refs ON bills;
DROP TRIGGER IF EXISTS loans_rehome_entity_refs ON loans;
DROP TRIGGER IF EXISTS maintenance_tasks_rehome_entity_refs ON maintenance_tasks;
DROP TRIGGER IF EXISTS maintenance_schedules_rehome_entity_refs ON maintenance_schedules;
DROP TRIGGER IF EXISTS files_rehome_entity_refs ON files;
DROP TRIGGER IF EXISTS notes_rehome_entity_refs ON notes;
DROP TRIGGER IF EXISTS secrets_rehome_entity_refs ON secrets;
DROP FUNCTION IF EXISTS rehome_entity_refs();

-- 2. Ownership check
DROP TRIGGER IF EXISTS notes_enforce_entity_owner ON notes;
DROP TRIGGER IF EXISTS files_enforce_entity_owner ON files;
DROP TRIGGER IF EXISTS secrets_enforce_entity_owner ON secrets;
DROP TRIGGER IF EXISTS document_links_enforce_entity_owner ON document_links;
DROP FUNCTION IF EXISTS enforce_entity_owner();
DROP INDEX IF EXISTS idx_document_links_household;
ALTER TABLE document_links DROP COLUMN IF EXISTS household_id;

-- 1. Registry
DROP FUNCTION IF EXISTS entity_household(TEXT, UUID);
DROP DOMAIN IF EXISTS entity_kind;

COMMIT;
