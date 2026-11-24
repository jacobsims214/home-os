-- 091_family_composite_fks.down.sql (#5519)
--
-- Restores every FK the up replaced to its exact pre-091 definition, delete
-- action included, and drops the seven indexes the up added. Rows repaired by
-- the up stay repaired: the cross-family links were never valid and are not
-- reconstructible.
--
-- The 19 definitions below were read from pg_constraint
-- (pg_get_constraintdef + confdeltype) on a database at 089 before the up was
-- written, not retyped from memory:
--
--   bills.property_id                  -> properties  ON DELETE SET NULL
--   bills.asset_id                     -> assets      ON DELETE SET NULL
--   bills.vehicle_id                   -> vehicles    ON DELETE SET NULL
--   maintenance_schedules.property_id  -> properties  ON DELETE CASCADE
--   maintenance_schedules.asset_id     -> assets      ON DELETE CASCADE
--   maintenance_schedules.vehicle_id   -> vehicles    ON DELETE CASCADE
--   maintenance_tasks.property_id      -> properties  ON DELETE CASCADE
--   maintenance_tasks.asset_id         -> assets      ON DELETE CASCADE
--   maintenance_tasks.vehicle_id       -> vehicles    ON DELETE CASCADE
--   loans.property_id                  -> properties  ON DELETE SET NULL
--   loans.asset_id                     -> assets      ON DELETE SET NULL
--   loans.vehicle_id                   -> vehicles    ON DELETE SET NULL
--   guides.property_id                 -> properties  ON DELETE SET NULL
--   guides.asset_id                    -> assets      ON DELETE SET NULL
--   guides.vehicle_id                  -> vehicles    ON DELETE SET NULL
--   assets.property_id                 -> properties  ON DELETE CASCADE
--   assets.room_id                     -> rooms       ON DELETE SET NULL
--   rooms.property_id                  -> properties  ON DELETE CASCADE
--
-- 018 owns maintenance_tasks/maintenance_schedules .property_id (CASCADE) and
-- 038 owns the rest of the maintenance columns; the names are kept so those
-- down migrations still find them.

BEGIN;

DROP INDEX IF EXISTS idx_bills_property;
DROP INDEX IF EXISTS idx_maintenance_schedules_property;
DROP INDEX IF EXISTS idx_maintenance_schedules_asset;
DROP INDEX IF EXISTS idx_maintenance_schedules_vehicle;
DROP INDEX IF EXISTS idx_maintenance_tasks_property;
DROP INDEX IF EXISTS idx_maintenance_tasks_asset;
DROP INDEX IF EXISTS idx_maintenance_tasks_vehicle;

ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_property_id_fkey;
ALTER TABLE bills ADD CONSTRAINT bills_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id) ON DELETE SET NULL;

ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_asset_id_fkey;
ALTER TABLE bills ADD CONSTRAINT bills_asset_id_fkey
    FOREIGN KEY (asset_id) REFERENCES assets(id) ON DELETE SET NULL;

ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_vehicle_id_fkey;
ALTER TABLE bills ADD CONSTRAINT bills_vehicle_id_fkey
    FOREIGN KEY (vehicle_id) REFERENCES vehicles(id) ON DELETE SET NULL;

ALTER TABLE loans DROP CONSTRAINT IF EXISTS loans_property_id_fkey;
ALTER TABLE loans ADD CONSTRAINT loans_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id) ON DELETE SET NULL;

ALTER TABLE loans DROP CONSTRAINT IF EXISTS loans_asset_id_fkey;
ALTER TABLE loans ADD CONSTRAINT loans_asset_id_fkey
    FOREIGN KEY (asset_id) REFERENCES assets(id) ON DELETE SET NULL;

ALTER TABLE loans DROP CONSTRAINT IF EXISTS loans_vehicle_id_fkey;
ALTER TABLE loans ADD CONSTRAINT loans_vehicle_id_fkey
    FOREIGN KEY (vehicle_id) REFERENCES vehicles(id) ON DELETE SET NULL;

ALTER TABLE guides DROP CONSTRAINT IF EXISTS guides_property_id_fkey;
ALTER TABLE guides ADD CONSTRAINT guides_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id) ON DELETE SET NULL;

ALTER TABLE guides DROP CONSTRAINT IF EXISTS guides_asset_id_fkey;
ALTER TABLE guides ADD CONSTRAINT guides_asset_id_fkey
    FOREIGN KEY (asset_id) REFERENCES assets(id) ON DELETE SET NULL;

ALTER TABLE guides DROP CONSTRAINT IF EXISTS guides_vehicle_id_fkey;
ALTER TABLE guides ADD CONSTRAINT guides_vehicle_id_fkey
    FOREIGN KEY (vehicle_id) REFERENCES vehicles(id) ON DELETE SET NULL;

ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_property_id_fkey;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id) ON DELETE CASCADE;

ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_asset_id_fkey;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_asset_id_fkey
    FOREIGN KEY (asset_id) REFERENCES assets(id) ON DELETE CASCADE;

ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_vehicle_id_fkey;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_vehicle_id_fkey
    FOREIGN KEY (vehicle_id) REFERENCES vehicles(id) ON DELETE CASCADE;

ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_property_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id) ON DELETE CASCADE;

ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_asset_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_asset_id_fkey
    FOREIGN KEY (asset_id) REFERENCES assets(id) ON DELETE CASCADE;

ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_vehicle_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_vehicle_id_fkey
    FOREIGN KEY (vehicle_id) REFERENCES vehicles(id) ON DELETE CASCADE;

ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_property_id_fkey;
ALTER TABLE assets ADD CONSTRAINT assets_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id) ON DELETE CASCADE;

ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_room_id_fkey;
ALTER TABLE assets ADD CONSTRAINT assets_room_id_fkey
    FOREIGN KEY (room_id) REFERENCES rooms(id) ON DELETE SET NULL;

ALTER TABLE rooms DROP CONSTRAINT IF EXISTS rooms_property_id_fkey;
ALTER TABLE rooms ADD CONSTRAINT rooms_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id) ON DELETE CASCADE;

COMMIT;
