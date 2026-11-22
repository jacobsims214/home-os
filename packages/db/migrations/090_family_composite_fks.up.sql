-- 090_family_composite_fks.up.sql (#5519, schema review F4 step 2 of 2)
--
-- 084 (#5512) added the missing UNIQUE (id, household_id) keys; this migration
-- spends them. Schema review F4 said "15 family-scoped FKs are single-column
-- where 20 equivalents are composite". The live catalogue says 19 single-column
-- FKs whose parent is family-scoped, so the inventory below is taken from
-- pg_constraint, not from the review's list:
--
--   child table            column                parent           ON DELETE
--   bills                  property_id           properties       SET NULL
--   bills                  asset_id              assets           SET NULL
--   bills                  vehicle_id            vehicles         SET NULL
--   maintenance_schedules  property_id           properties       CASCADE
--   maintenance_schedules  asset_id              assets           CASCADE
--   maintenance_schedules  vehicle_id            vehicles         CASCADE
--   maintenance_tasks      property_id           properties       CASCADE
--   maintenance_tasks      asset_id              assets           CASCADE
--   maintenance_tasks      vehicle_id            vehicles         CASCADE
--   loans                  property_id           properties       SET NULL
--   loans                  asset_id              assets           SET NULL
--   loans                  vehicle_id            vehicles         SET NULL
--   guides                 property_id           properties       SET NULL
--   guides                 asset_id              assets           SET NULL
--   guides                 vehicle_id            vehicles         SET NULL
--   assets                 property_id           properties       CASCADE
--   assets                 room_id               rooms            SET NULL
--   rooms                  property_id           properties       CASCADE
--
-- The first 15 are the review's list, verbatim. The next three are the same
-- defect the review's query missed: assets and rooms also point at a family
-- parent by id alone, are also guarded only by a trigger
-- (assets_derive_property(), rooms_set_household()), and their parents carry
-- the 084 key too — so they are converted here rather than left as the last
-- three single-column family FKs in the schema.
--
-- The 19th, activity_log.actor_agent_token_id -> agent_tokens, is deliberately
-- NOT converted. agent_tokens has no (id, household_id) key, and adding one
-- changes behaviour in a way this ticket must not decide quietly: an audit row
-- naming a token from another household would start being REJECTED. For an
-- audit trail a refused write is worse than the missing guard, so that is a
-- product decision, not a constraint swap. Flagged on the ticket.
--
-- Delete actions are preserved column for column, from
-- pg_constraint.confdeltype read before the change. Every CASCADE stays
-- CASCADE; every SET NULL uses the PG15+ column-list form ON DELETE SET NULL
-- (col) exactly as 043/058 do, so a cascade nulls the reference column and
-- never household_id (which is NOT NULL, so the wrong form would fail the
-- delete outright rather than quietly re-home the row).
--
-- Constraint names are unchanged, so 018's and 038's down migrations still find
-- them (058 did the same deliberately).

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Repair cross-family references
-- ---------------------------------------------------------------------------
-- A cross-family row here is corrupt data, not a workflow: the triggers exist
-- to prevent it, so such a row can only come from a trigger-disabled write, a
-- COPY/pg_restore, or session_replication_role = replica. 058 set such
-- references to NULL and reported the count; the same is done here so the
-- migration repairs data instead of failing on it. Every count is reported,
-- including zeros.
--
-- rooms.property_id is the one column that is NOT NULL, so it cannot be
-- nulled. A room's family comes from its property (rooms_set_household() sets
-- household_id from the property on insert), so the property is the authority:
-- the room's household_id is aligned to it. That runs first, before anything
-- references rooms.
DO $$
DECLARE
    n INT;
BEGIN
    UPDATE rooms r SET household_id = p.household_id
      FROM properties p
     WHERE p.id = r.property_id AND p.household_id <> r.household_id;
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: re-homed % cross-family rooms.property_id', n;

    UPDATE assets c SET property_id = NULL
     WHERE c.property_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM properties p WHERE p.id = c.property_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family assets.property_id', n;

    UPDATE assets c SET room_id = NULL
     WHERE c.room_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM rooms p WHERE p.id = c.room_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family assets.room_id', n;

    UPDATE bills c SET property_id = NULL
     WHERE c.property_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM properties p WHERE p.id = c.property_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family bills.property_id', n;

    UPDATE bills c SET asset_id = NULL
     WHERE c.asset_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM assets p WHERE p.id = c.asset_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family bills.asset_id', n;

    UPDATE bills c SET vehicle_id = NULL
     WHERE c.vehicle_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM vehicles p WHERE p.id = c.vehicle_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family bills.vehicle_id', n;

    UPDATE maintenance_schedules c SET property_id = NULL
     WHERE c.property_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM properties p WHERE p.id = c.property_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family maintenance_schedules.property_id', n;

    UPDATE maintenance_schedules c SET asset_id = NULL
     WHERE c.asset_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM assets p WHERE p.id = c.asset_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family maintenance_schedules.asset_id', n;

    UPDATE maintenance_schedules c SET vehicle_id = NULL
     WHERE c.vehicle_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM vehicles p WHERE p.id = c.vehicle_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family maintenance_schedules.vehicle_id', n;

    UPDATE maintenance_tasks c SET property_id = NULL
     WHERE c.property_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM properties p WHERE p.id = c.property_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family maintenance_tasks.property_id', n;

    UPDATE maintenance_tasks c SET asset_id = NULL
     WHERE c.asset_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM assets p WHERE p.id = c.asset_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family maintenance_tasks.asset_id', n;

    UPDATE maintenance_tasks c SET vehicle_id = NULL
     WHERE c.vehicle_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM vehicles p WHERE p.id = c.vehicle_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family maintenance_tasks.vehicle_id', n;

    UPDATE loans c SET property_id = NULL
     WHERE c.property_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM properties p WHERE p.id = c.property_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family loans.property_id', n;

    UPDATE loans c SET asset_id = NULL
     WHERE c.asset_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM assets p WHERE p.id = c.asset_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family loans.asset_id', n;

    UPDATE loans c SET vehicle_id = NULL
     WHERE c.vehicle_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM vehicles p WHERE p.id = c.vehicle_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family loans.vehicle_id', n;

    UPDATE guides c SET property_id = NULL
     WHERE c.property_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM properties p WHERE p.id = c.property_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family guides.property_id', n;

    UPDATE guides c SET asset_id = NULL
     WHERE c.asset_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM assets p WHERE p.id = c.asset_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family guides.asset_id', n;

    UPDATE guides c SET vehicle_id = NULL
     WHERE c.vehicle_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM vehicles p WHERE p.id = c.vehicle_id AND p.household_id = c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '090: cleared % cross-family guides.vehicle_id', n;
END $$;

-- ---------------------------------------------------------------------------
-- 2. Same-family composite FKs, delete action preserved per column
-- ---------------------------------------------------------------------------

-- SET NULL columns (column-list form, so household_id is never nulled) --------
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_property_id_fkey;
ALTER TABLE bills ADD CONSTRAINT bills_property_id_fkey
    FOREIGN KEY (property_id, household_id) REFERENCES properties (id, household_id)
    ON DELETE SET NULL (property_id);

ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_asset_id_fkey;
ALTER TABLE bills ADD CONSTRAINT bills_asset_id_fkey
    FOREIGN KEY (asset_id, household_id) REFERENCES assets (id, household_id)
    ON DELETE SET NULL (asset_id);

ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_vehicle_id_fkey;
ALTER TABLE bills ADD CONSTRAINT bills_vehicle_id_fkey
    FOREIGN KEY (vehicle_id, household_id) REFERENCES vehicles (id, household_id)
    ON DELETE SET NULL (vehicle_id);

ALTER TABLE loans DROP CONSTRAINT IF EXISTS loans_property_id_fkey;
ALTER TABLE loans ADD CONSTRAINT loans_property_id_fkey
    FOREIGN KEY (property_id, household_id) REFERENCES properties (id, household_id)
    ON DELETE SET NULL (property_id);

ALTER TABLE loans DROP CONSTRAINT IF EXISTS loans_asset_id_fkey;
ALTER TABLE loans ADD CONSTRAINT loans_asset_id_fkey
    FOREIGN KEY (asset_id, household_id) REFERENCES assets (id, household_id)
    ON DELETE SET NULL (asset_id);

ALTER TABLE loans DROP CONSTRAINT IF EXISTS loans_vehicle_id_fkey;
ALTER TABLE loans ADD CONSTRAINT loans_vehicle_id_fkey
    FOREIGN KEY (vehicle_id, household_id) REFERENCES vehicles (id, household_id)
    ON DELETE SET NULL (vehicle_id);

ALTER TABLE guides DROP CONSTRAINT IF EXISTS guides_property_id_fkey;
ALTER TABLE guides ADD CONSTRAINT guides_property_id_fkey
    FOREIGN KEY (property_id, household_id) REFERENCES properties (id, household_id)
    ON DELETE SET NULL (property_id);

ALTER TABLE guides DROP CONSTRAINT IF EXISTS guides_asset_id_fkey;
ALTER TABLE guides ADD CONSTRAINT guides_asset_id_fkey
    FOREIGN KEY (asset_id, household_id) REFERENCES assets (id, household_id)
    ON DELETE SET NULL (asset_id);

ALTER TABLE guides DROP CONSTRAINT IF EXISTS guides_vehicle_id_fkey;
ALTER TABLE guides ADD CONSTRAINT guides_vehicle_id_fkey
    FOREIGN KEY (vehicle_id, household_id) REFERENCES vehicles (id, household_id)
    ON DELETE SET NULL (vehicle_id);

ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_room_id_fkey;
ALTER TABLE assets ADD CONSTRAINT assets_room_id_fkey
    FOREIGN KEY (room_id, household_id) REFERENCES rooms (id, household_id)
    ON DELETE SET NULL (room_id);

-- CASCADE columns ------------------------------------------------------------
ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_property_id_fkey;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_property_id_fkey
    FOREIGN KEY (property_id, household_id) REFERENCES properties (id, household_id)
    ON DELETE CASCADE;

ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_asset_id_fkey;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_asset_id_fkey
    FOREIGN KEY (asset_id, household_id) REFERENCES assets (id, household_id)
    ON DELETE CASCADE;

ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_vehicle_id_fkey;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_vehicle_id_fkey
    FOREIGN KEY (vehicle_id, household_id) REFERENCES vehicles (id, household_id)
    ON DELETE CASCADE;

ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_property_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_property_id_fkey
    FOREIGN KEY (property_id, household_id) REFERENCES properties (id, household_id)
    ON DELETE CASCADE;

ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_asset_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_asset_id_fkey
    FOREIGN KEY (asset_id, household_id) REFERENCES assets (id, household_id)
    ON DELETE CASCADE;

ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_vehicle_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_vehicle_id_fkey
    FOREIGN KEY (vehicle_id, household_id) REFERENCES vehicles (id, household_id)
    ON DELETE CASCADE;

ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_property_id_fkey;
ALTER TABLE assets ADD CONSTRAINT assets_property_id_fkey
    FOREIGN KEY (property_id, household_id) REFERENCES properties (id, household_id)
    ON DELETE CASCADE;

ALTER TABLE rooms DROP CONSTRAINT IF EXISTS rooms_property_id_fkey;
ALTER TABLE rooms ADD CONSTRAINT rooms_property_id_fkey
    FOREIGN KEY (property_id, household_id) REFERENCES properties (id, household_id)
    ON DELETE CASCADE;

-- ---------------------------------------------------------------------------
-- 3. Child-side indexes (F21, free with the composite FKs)
-- ---------------------------------------------------------------------------
-- PostgreSQL gives the referencing side of a FK no index, and the delete side
-- of every CASCADE/SET NULL above scans the child by the reference column. Only
-- the columns with no usable leading-column index get one; bills.asset_id,
-- bills.vehicle_id and all nine guides/loans columns already have one.
CREATE INDEX IF NOT EXISTS idx_bills_property ON bills (property_id);
CREATE INDEX IF NOT EXISTS idx_maintenance_schedules_property ON maintenance_schedules (property_id);
CREATE INDEX IF NOT EXISTS idx_maintenance_schedules_asset ON maintenance_schedules (asset_id);
CREATE INDEX IF NOT EXISTS idx_maintenance_schedules_vehicle ON maintenance_schedules (vehicle_id);
CREATE INDEX IF NOT EXISTS idx_maintenance_tasks_property ON maintenance_tasks (property_id);
CREATE INDEX IF NOT EXISTS idx_maintenance_tasks_asset ON maintenance_tasks (asset_id);
CREATE INDEX IF NOT EXISTS idx_maintenance_tasks_vehicle ON maintenance_tasks (vehicle_id);

COMMIT;
