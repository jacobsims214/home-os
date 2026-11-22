-- 089_subject_check_num_nonnulls.down.sql (#5515)
--
-- Reverses 089_subject_check_num_nonnulls.up.sql. The five constraints go back
-- to the EXACT expressions they had, verbatim, so the catalogue is byte-for-byte
-- what it was before 089:
--
--     CHECK (vehicle_id IS NULL OR (property_id IS NULL AND asset_id IS NULL))
--
-- which is the form 038 (bills, loans), 056 (maintenance_schedules,
-- maintenance_tasks) and 049 (guides) created.
--
-- The five COMMENT ON CONSTRAINT texts are dropped, not kept: before 089 there
-- were none, so there is nothing to restore.
--
-- What cannot come back, and is not pretended to:
--
--   * The stale-property_id backfill is not reversed. 089 rewrote property_id
--     from the asset where the two disagreed. Those rows had a property_id that
--     contradicted their own asset -- the state that made "spend on this
--     property" double-count or miss -- so restoring it would only put the
--     disagreement back. The old value was not recorded, and re-deriving it is
--     what derive_subject_property() does on the next write anyway.
--
-- Runs in one transaction.

BEGIN;

ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_subject_check;
ALTER TABLE bills ADD CONSTRAINT bills_subject_check
    CHECK (vehicle_id IS NULL OR (property_id IS NULL AND asset_id IS NULL));

ALTER TABLE loans DROP CONSTRAINT IF EXISTS loans_subject_check;
ALTER TABLE loans ADD CONSTRAINT loans_subject_check
    CHECK (vehicle_id IS NULL OR (property_id IS NULL AND asset_id IS NULL));

ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_subject_check;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_subject_check
    CHECK (vehicle_id IS NULL OR (property_id IS NULL AND asset_id IS NULL));

ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_subject_check;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_subject_check
    CHECK (vehicle_id IS NULL OR (property_id IS NULL AND asset_id IS NULL));

ALTER TABLE guides DROP CONSTRAINT IF EXISTS guides_subject_check;
ALTER TABLE guides ADD CONSTRAINT guides_subject_check
    CHECK (vehicle_id IS NULL OR (property_id IS NULL AND asset_id IS NULL));

COMMIT;
