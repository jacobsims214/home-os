-- 087_subject_contradiction.down.sql (#5608)
--
-- Reverses 087_subject_contradiction.up.sql: derive_subject_property() goes
-- back to its 038 body, byte for byte, so the function in the catalogue is
-- what 038 created. The trigger comments 087 added are dropped (they did not
-- exist before).
--
-- The five *_subject_check constraints are NOT touched — 087 never changed
-- them. They stay as 083 left them.
--
-- What cannot come back, and is not pretended to come back:
--
--   * The backfill. Where 087 found a row whose property_id disagreed with its
--     asset, it re-derived property_id from the asset. The old value was not
--     recorded and is not restorable. Restoring it would only put the
--     contradiction back, and the derivation 038 already runs would overwrite
--     it on the row's next subject write anyway. The down migration prints how
--     many rows now disagree with their asset, so the operator knows the size of
--     what it is walking away from.
--
--   * The refusal itself. Rolling back means a caller can again present a
--     contradicting property_id and have it silently overwritten. That is the
--     behaviour 087 exists to remove; the down migration exists for a real
--     rollback, not as a way to keep the new rule's safety while dropping its
--     error.
--
-- Runs in one transaction.

BEGIN;

CREATE OR REPLACE FUNCTION derive_subject_property()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_household UUID;
    v_property  UUID;
    v_found     BOOLEAN;
BEGIN
    IF NEW.asset_id IS NOT NULL THEN
        SELECT household_id, property_id, TRUE INTO v_household, v_property, v_found
          FROM assets WHERE id = NEW.asset_id;
        IF v_found THEN
            IF v_household <> NEW.household_id THEN
                RAISE EXCEPTION '%: asset % does not exist in this family', TG_TABLE_NAME, NEW.asset_id
                    USING ERRCODE = 'foreign_key_violation';
            END IF;
            NEW.property_id := v_property;
        END IF;
    END IF;

    IF NEW.property_id IS NOT NULL THEN
        SELECT household_id INTO v_household FROM properties WHERE id = NEW.property_id;
        IF v_household IS NOT NULL AND v_household <> NEW.household_id THEN
            RAISE EXCEPTION '%: property % does not exist in this family', TG_TABLE_NAME, NEW.property_id
                USING ERRCODE = 'foreign_key_violation';
        END IF;
    END IF;

    IF NEW.vehicle_id IS NOT NULL THEN
        SELECT household_id INTO v_household FROM vehicles WHERE id = NEW.vehicle_id;
        IF v_household IS NOT NULL AND v_household <> NEW.household_id THEN
            RAISE EXCEPTION '%: vehicle % does not exist in this family', TG_TABLE_NAME, NEW.vehicle_id
                USING ERRCODE = 'foreign_key_violation';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

COMMENT ON FUNCTION derive_subject_property() IS NULL;
COMMENT ON TRIGGER bills_derive_subject_property ON bills IS NULL;
COMMENT ON TRIGGER loans_derive_subject_property ON loans IS NULL;
COMMENT ON TRIGGER maintenance_schedules_derive_subject_property ON maintenance_schedules IS NULL;
COMMENT ON TRIGGER maintenance_tasks_derive_subject_property ON maintenance_tasks IS NULL;
COMMENT ON TRIGGER guides_derive_subject_property ON guides IS NULL;

-- Report, without changing, how many rows now disagree with their asset. After
-- a rollback the trigger derives on the next subject write, so these are not
-- stuck; they are simply what the database now holds.
DO $$
DECLARE
    t TEXT;
    n BIGINT;
BEGIN
    FOREACH t IN ARRAY ARRAY['bills', 'loans', 'maintenance_schedules',
                             'maintenance_tasks', 'guides'] LOOP
        EXECUTE format(
            'SELECT count(*) FROM %I r JOIN assets a ON a.id = r.asset_id
              WHERE r.property_id IS DISTINCT FROM a.property_id',
            t) INTO n;
        RAISE NOTICE '087 down: % row(s) in % disagree with their asset (left as is)', n, t;
    END LOOP;
END $$;

COMMIT;
