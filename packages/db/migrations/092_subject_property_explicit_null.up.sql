-- 092_subject_property_explicit_null.up.sql (HOME-OS-5626)
--
-- An explicit "clear the property off this record" is no longer overwritten.
--
-- THE DEFECT. derive_subject_property() (038, restated by 087) re-derives
-- NEW.property_id from the asset on every write where asset_id is set. That is
-- right for an INSERT, and right when the asset changes. It also fired for an
-- UPDATE whose whole point was to REMOVE the property:
--
--     UPDATE bills SET property_id = NULL WHERE asset_id IS NOT NULL;
--     UPDATE 1
--     -- and property_id comes back with the asset's property
--
-- The caller asked to clear it and the trigger put it back, silently.
-- Measured on a database at 090 before this migration (PG16, pasted in the
-- ticket and in verify_092.sql):
--
--   * trigger enabled  -> property_id is re-derived; the NULL is lost
--   * the same statement with bills_derive_subject_property DISABLED
--                      -> the NULL sticks, so the trigger is the cause
--
-- #5519 made this unreachable on a normal property delete by restoring the FK
-- action-trigger order, and recorded the behaviour as a known gap: it was a
-- real bug on its own terms, still reachable by any direct UPDATE. Fixed here.
--
-- WHY DEFERRABLE CONSTRAINTS ARE *NOT* USED (measured, not assumed)
--
-- #5519 expected the complete fix to need DEFERRABLE INITIALLY DEFERRED on the
-- composite FKs so that RI checks run at COMMIT, after every cascade action.
-- That was tried on PG16 against the 091 composite FKs and it does NOT fix this
-- defect, because the rewrite happens in a BEFORE trigger, before any RI check
-- of any timing runs:
--
--     both bills FKs DEFERRABLE INITIALLY DEFERRED, then the same UPDATE
--     -> property_id still comes back with the asset's property
--
-- What deferring DOES fix is the firing-order fragility 091 documents (with an
-- inverted bills_property_id_fkey OID, a property delete succeeded instead of
-- raising 23503). Measured cost, same cluster, 3000 single-row INSERTs on
-- bills, three alternating rounds, identical table, trigger disabled so only
-- the FK work differs:
--
--     round   immediate   deferred
--     1        21803 ms   25161 ms
--     2        26137 ms   29707 ms
--     3        28565 ms   34567 ms
--
-- i.e. +15 % to +20 % on every write to the table, and a violation surfaces at
-- COMMIT instead of at the statement that caused it. Since the trigger change
-- below removes the ordering dependence anyway (verified: with it in place a
-- property delete succeeds even at the deliberately INVERTED constraint order
-- 091 warns about), paying 15-20 % on every bill write for an ordering
-- guarantee that is no longer needed is a bad trade. Deferrable is therefore
-- NOT shipped. 091's stated creation order stays as the belt to this braces and
-- verify_092.sql asserts both.
--
-- THE RULE, precisely
--
-- A NULL property_id on an INSERT, or on an UPDATE that also changes the
-- subject, still means "derive it". That is 087's rule, unchanged: an
-- asset-subject row still carries both columns, so "everything at this
-- property" is still property_id = $1 alone.
--
-- A NULL property_id is treated as a CLEAR -- the caller's instruction rather
-- than a missing value -- when, and only when, all of these hold:
--
--   * it is an UPDATE, and
--   * NEW.property_id IS NULL, and
--   * the subject did not move on this write: asset_id, vehicle_id and
--     household_id are each IS NOT DISTINCT FROM their OLD value.
--
-- Then the NULL is left alone. Every other 087 behaviour is kept: a PRESENTED
-- property_id that is not the asset's property is still refused with
-- <table>_subject_property_conflict (23514); a vehicle alongside a property or
-- asset is still refused with <table>_subject_check (23514); both family checks
-- are untouched; the CHECKs from 083 stay as the backstop.
--
--   INSERT                 -> derive, as before (a new row's NULL is "no claim")
--   asset changes          -> derive from the NEW asset, even with a carried
--                             NULL, which is exactly what the API/MCP write
--                             path sends (both subject columns every time)
--   asset unchanged,
--   property cleared       -> the NULL PERSISTS (this migration)
--   property re-cleared    -> still NULL: idempotent, because the predicate
--                             does not require property_id to have changed
--   property presented     -> must equal the asset's property, else 23514
--   vehicle + subject      -> refused, 23514
--
-- CONSEQUENCE TO KNOW ABOUT (documented in architecture/database-schema.md)
--
-- After an explicit clear, property_id is NULL on a row whose asset_id is set,
-- so property_id no longer strictly equals the asset's property for that row.
-- It is the caller's stated choice: the bill is about the asset, and
-- deliberately not rolled up into the property's totals. Nothing re-derives it
-- until the subject next changes. This does not affect assets_propagate_property
-- (it only updates rows whose property_id is DISTINCT FROM the asset's, so a
-- deliberately cleared row is skipped rather than resurrected), and it does not
-- affect the property delete cascade (the row is already at NULL).
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. The rule, in the one place that can express it.
-- ---------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION derive_subject_property()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_household UUID;
    v_property  UUID;
    v_found     BOOLEAN;
    v_presented BOOLEAN;
    v_cleared   BOOLEAN;
BEGIN
    -- Did THIS write present a property_id? See "THE RULE" above.
    IF TG_OP = 'INSERT' THEN
        v_presented := NEW.property_id IS NOT NULL;
        v_cleared   := FALSE;
    ELSE
        v_presented := NEW.property_id IS DISTINCT FROM OLD.property_id;
        -- 092: the caller asked for no property, and nothing about the subject
        -- moved, so there is nothing to derive. A repeated clear lands here too.
        v_cleared := NEW.property_id IS NULL
                     AND NEW.asset_id     IS NOT DISTINCT FROM OLD.asset_id
                     AND NEW.vehicle_id   IS NOT DISTINCT FROM OLD.vehicle_id
                     AND NEW.household_id IS NOT DISTINCT FROM OLD.household_id;
    END IF;

    -- An asset decides the property. A presented value that disagrees is the
    -- contradiction 087 refuses.
    IF NEW.asset_id IS NOT NULL THEN
        SELECT household_id, property_id, TRUE INTO v_household, v_property, v_found
          FROM assets WHERE id = NEW.asset_id;
        IF v_found THEN
            IF v_household <> NEW.household_id THEN
                RAISE EXCEPTION '%: asset % does not exist in this family', TG_TABLE_NAME, NEW.asset_id
                    USING ERRCODE = 'foreign_key_violation';
            END IF;
            IF v_cleared THEN
                -- 092: the caller cleared it; this is not a missing value.
                NULL;
            ELSIF v_presented AND NEW.property_id IS NOT NULL AND NEW.property_id IS DISTINCT FROM v_property THEN
                RAISE EXCEPTION
                    '%: property_id % contradicts asset % , which is at property %',
                    TG_TABLE_NAME, NEW.property_id, NEW.asset_id, COALESCE(v_property::text, 'none')
                    USING ERRCODE = 'check_violation',
                          CONSTRAINT = TG_TABLE_NAME || '_subject_property_conflict',
                          HINT = 'A record about an asset is about that asset''s property. Leave property_id out (or NULL) with asset_id and it is derived from the asset. To file this against a different property, name that property and not the asset.';
            ELSE
                -- Derivation, unchanged from 038/087: the asset is the
                -- authority, and a carried value that is stale is corrected.
                NEW.property_id := v_property;
            END IF;
        END IF;
        -- Asset not found: leave the subject to the FK, as in 038.
    END IF;

    -- A subject must belong to the row's family. A subject that is not found
    -- is left to its FK (it may be mid-cascade).
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

        -- (1) A vehicle is not at a property. Checked AFTER derivation, so an
        -- asset-subject row that also names a vehicle is caught too.
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
    '(<table>_subject_check, 23514). Since 092, an UPDATE that sets property_id to NULL while the '
    'subject stays put is a CLEAR and is honoured, not re-derived.';

-- The triggers already exist from 038 (bills, loans, maintenance_schedules,
-- maintenance_tasks) and 049 (guides) and call this function by name, so the
-- CREATE OR REPLACE above is the whole change to them.

-- ---------------------------------------------------------------------------
-- 2. Report rows that already carry a deliberately-cleared property.
--
-- Nothing to repair: 092 changes what FUTURE writes do, and a row whose
-- property_id is NULL with an asset set is exactly the shape this migration now
-- allows on purpose. Counted so the number is visible rather than assumed.
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    t     TEXT;
    n     BIGINT;
    total BIGINT := 0;
BEGIN
    FOREACH t IN ARRAY ARRAY['bills', 'loans', 'maintenance_schedules',
                             'maintenance_tasks', 'guides'] LOOP
        EXECUTE format('SELECT count(*) FROM %I WHERE asset_id IS NOT NULL AND property_id IS NULL', t) INTO n;
        total := total + n;
        RAISE NOTICE '092 %: % row(s) have an asset and no property_id (a clear, allowed from this migration)', rpad(t, 22), n;
    END LOOP;
    RAISE NOTICE '092 total: % row(s)', total;
END $$;

COMMIT;
