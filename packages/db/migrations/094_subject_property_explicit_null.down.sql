-- 094_subject_property_explicit_null.down.sql (HOME-OS-5626)
--
-- Reverses 094_subject_property_explicit_null.up.sql: derive_subject_property()
-- goes back to its 087 body, byte for byte, so the function in the catalogue is
-- what 087 left. 087's own down migration still restores 038's body in turn, so
-- the whole chain rolls back.
--
-- What cannot come back, and is not pretended to come back: a property_id that
-- a caller deliberately cleared under 094 has since been re-derived by nothing,
-- so it stays NULL. Re-deriving it here would silently put back a value a
-- person asked to remove, which is the defect 094 removes rather than a repair
-- -- and it would also have to invent a value for a row whose asset has since
-- moved. Nothing is changed; the down migration counts those rows and says so.
--
-- What DOES come back: an explicit UPDATE ... SET property_id = NULL on a row
-- with asset_id set is silently overwritten again. That is the pre-094
-- behaviour and the down migration exists for a real rollback, not as a way to
-- keep the new rule while dropping it.
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
    v_presented BOOLEAN;
BEGIN
    -- Did THIS write present a property_id? See 087's "WHAT PRESENTED MEANS".
    IF TG_OP = 'INSERT' THEN
        v_presented := NEW.property_id IS NOT NULL;
    ELSE
        v_presented := NEW.property_id IS DISTINCT FROM OLD.property_id;
    END IF;

    IF NEW.asset_id IS NOT NULL THEN
        SELECT household_id, property_id, TRUE INTO v_household, v_property, v_found
          FROM assets WHERE id = NEW.asset_id;
        IF v_found THEN
            IF v_household <> NEW.household_id THEN
                RAISE EXCEPTION '%: asset % does not exist in this family', TG_TABLE_NAME, NEW.asset_id
                    USING ERRCODE = 'foreign_key_violation';
            END IF;
            IF v_presented AND NEW.property_id IS NOT NULL AND NEW.property_id IS DISTINCT FROM v_property THEN
                RAISE EXCEPTION
                    '%: property_id % contradicts asset % , which is at property %',
                    TG_TABLE_NAME, NEW.property_id, NEW.asset_id, COALESCE(v_property::text, 'none')
                    USING ERRCODE = 'check_violation',
                          CONSTRAINT = TG_TABLE_NAME || '_subject_property_conflict',
                          HINT = 'A record about an asset is about that asset''s property. Leave property_id out (or NULL) with asset_id and it is derived from the asset. To file this against a different property, name that property and not the asset.';
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
        IF NEW.property_id IS NOT NULL OR NEW.asset_id IS NOT NULL THEN
            RAISE EXCEPTION '%: vehicle_id % cannot be combined with property_id or asset_id', TG_TABLE_NAME, NEW.vehicle_id
                USING ERRCODE = 'check_violation',
                      CONSTRAINT = TG_TABLE_NAME || '_subject_check',
                      HINT = 'A vehicle is not at a property. Pass only vehicle_id, or only property_id and/or asset_id.';
        END IF;
    END IF;

    RETURN NEW;
END;
$$;

COMMENT ON FUNCTION derive_subject_property() IS
    'The subject rule for bills, loans, maintenance_schedules, maintenance_tasks and guides. '
    'One subject per row: asset_id or vehicle_id. property_id is derived from the asset, so an '
    'asset-subject row carries both. A property_id the caller PRESENTS (on INSERT when not NULL; '
    'on UPDATE when it changed) that is not the asset''s property is refused '
    '(<table>_subject_property_conflict, 23514); a vehicle alongside a property or asset is refused '
    '(<table>_subject_check, 23514). Because property_id can never disagree with its asset, '
    '"everything about this property" is property_id = $1 alone.';

-- Report, without changing, the rows an explicit clear left at NULL. Under 087
-- they would have been re-derived; here they stay as the person left them.
DO $$
DECLARE
    t TEXT;
    n BIGINT;
BEGIN
    FOREACH t IN ARRAY ARRAY['bills', 'loans', 'maintenance_schedules',
                             'maintenance_tasks', 'guides'] LOOP
        EXECUTE format('SELECT count(*) FROM %I WHERE asset_id IS NOT NULL AND property_id IS NULL', t) INTO n;
        RAISE NOTICE '094 down: % row(s) in % have an asset and no property_id (left as the caller cleared them)', n, t;
    END LOOP;
END $$;

COMMIT;
