-- 058_same_family_fks.up.sql (#4697)
--
-- Domain model v2's rule is that the database rejects a reference to another
-- family's row. Five single-column FKs still pointed at family-owned tables
-- without checking the family:
--
--   bills.vendor_id                  -> vendors               ON DELETE SET NULL
--   maintenance_tasks.vendor_id      -> vendors               ON DELETE SET NULL
--   maintenance_schedules.vendor_id  -> vendors               ON DELETE SET NULL
--   maintenance_tasks.schedule_id    -> maintenance_schedules ON DELETE SET NULL
--   calendars.property_id            -> properties            ON DELETE CASCADE
--
-- The last one let family B's property delete cascade into family A's
-- calendar and all its events.
--
-- This migration:
--   1. Repairs existing rows: any of the five columns whose target is in
--      another household is set to NULL (a NOTICE reports the counts). A
--      calendar keeps its events; it only loses the foreign property link.
--   2. Adds UNIQUE (id, household_id) on properties and maintenance_schedules
--      (vendors has had it since 043).
--   3. Replaces the five FKs with composite (col, household_id) FKs that keep
--      today's delete actions. SET NULL uses the PG15+ column-list form, as
--      043 does for pets.vet_vendor_id, so household_id is never nulled.
--      Constraint names are unchanged so 038's down still finds them.
--
-- Every referencing table has household_id NOT NULL, so MATCH SIMPLE checks
-- the pair whenever the referencing column is set.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Repair cross-family references
-- ---------------------------------------------------------------------------
DO $$
DECLARE
    n INT;
BEGIN
    UPDATE bills b SET vendor_id = NULL
     WHERE b.vendor_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM vendors v WHERE v.id = b.vendor_id AND v.household_id = b.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '058: cleared % cross-family bills.vendor_id', n;

    UPDATE maintenance_tasks t SET vendor_id = NULL
     WHERE t.vendor_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM vendors v WHERE v.id = t.vendor_id AND v.household_id = t.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '058: cleared % cross-family maintenance_tasks.vendor_id', n;

    UPDATE maintenance_schedules s SET vendor_id = NULL
     WHERE s.vendor_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM vendors v WHERE v.id = s.vendor_id AND v.household_id = s.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '058: cleared % cross-family maintenance_schedules.vendor_id', n;

    UPDATE maintenance_tasks t SET schedule_id = NULL
     WHERE t.schedule_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM maintenance_schedules s WHERE s.id = t.schedule_id AND s.household_id = t.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '058: cleared % cross-family maintenance_tasks.schedule_id', n;

    UPDATE calendars c SET property_id = NULL
     WHERE c.property_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM properties p WHERE p.id = c.property_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '058: cleared % cross-family calendars.property_id', n;
END $$;

-- ---------------------------------------------------------------------------
-- 2. (id, household_id) keys for the composite FKs to reference
-- ---------------------------------------------------------------------------
-- Added only when missing, so a re-run never has to drop a key that FKs
-- already depend on. vendors_id_household_id_key exists since 043.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'properties'::regclass AND conname = 'properties_id_household_id_key') THEN
        ALTER TABLE properties ADD CONSTRAINT properties_id_household_id_key UNIQUE (id, household_id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'maintenance_schedules'::regclass AND conname = 'maintenance_schedules_id_household_id_key') THEN
        ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_id_household_id_key UNIQUE (id, household_id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                    WHERE conrelid = 'vendors'::regclass AND conname = 'vendors_id_household_id_key') THEN
        ALTER TABLE vendors ADD CONSTRAINT vendors_id_household_id_key UNIQUE (id, household_id);
    END IF;
END $$;

-- ---------------------------------------------------------------------------
-- 3. Same-family composite FKs
-- ---------------------------------------------------------------------------
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_vendor_id_fkey;
ALTER TABLE bills ADD CONSTRAINT bills_vendor_id_fkey
    FOREIGN KEY (vendor_id, household_id) REFERENCES vendors (id, household_id)
    ON DELETE SET NULL (vendor_id);

ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_vendor_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_vendor_id_fkey
    FOREIGN KEY (vendor_id, household_id) REFERENCES vendors (id, household_id)
    ON DELETE SET NULL (vendor_id);

ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_vendor_id_fkey;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_vendor_id_fkey
    FOREIGN KEY (vendor_id, household_id) REFERENCES vendors (id, household_id)
    ON DELETE SET NULL (vendor_id);

ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_schedule_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_schedule_id_fkey
    FOREIGN KEY (schedule_id, household_id) REFERENCES maintenance_schedules (id, household_id)
    ON DELETE SET NULL (schedule_id);

ALTER TABLE calendars DROP CONSTRAINT IF EXISTS calendars_property_id_fkey;
ALTER TABLE calendars ADD CONSTRAINT calendars_property_id_fkey
    FOREIGN KEY (property_id, household_id) REFERENCES properties (id, household_id)
    ON DELETE CASCADE;

COMMIT;
