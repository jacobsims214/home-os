-- 087_subject_contradiction.up.sql (#5608, schema review F2 — the real rule)
--
-- WHAT 083 COULD NOT DO, AND WHY
--
-- 083 restated the five *_subject_check constraints (bills, loans,
-- maintenance_schedules, maintenance_tasks, guides) and proved that no
-- row-local CHECK can express the real rule. The proof is short and it is
-- still true:
--
--   derive_subject_property() (038) is a BEFORE INSERT/UPDATE trigger that
--   copies the asset's property into NEW.property_id. So an asset-subject row
--   is SUPPOSED to carry both asset_id and property_id. A CHECK counting
--   non-null subject columns therefore cannot separate
--       "property_id is set because this row's asset_id is at that property"
--   from
--       "property_id is set to a property that disagrees with this row's
--        asset_id"
--   and the first is legal while the second is the defect.
--
-- Only a trigger sees the row before it lands, and only a trigger sees OLD,
-- so only a trigger can tell a value the caller presented from a value the row
-- already carried. That is what this migration uses.
--
-- THE DEFECT (F2). Until now a caller could write a row with asset_id = A and
-- property_id = P where A is not at P. The trigger silently OVERWROTE P with
-- A's property and the write succeeded. Two consequences:
--
--   * The caller's value was discarded without a word, so a caller could
--     believe it had filed something against property P when it had not.
--   * "What do we spend on this property?" could not be answered with
--     property_id = $1. The only safe query was
--         property_id = $1 OR asset_id IN (SELECT id FROM assets WHERE property_id = $1)
--     because property_id could not be trusted on an asset-subject row, and a
--     row whose property_id disagreed with its asset was double-counted. An
--     agent reading the schema could not learn the rule from it.
--
-- THE RULE, stated once, here and in the trigger
--
--   A row has at most one SUBJECT: asset_id or vehicle_id. property_id is not
--   a third subject; it is the property the row is about, and when the subject
--   is an asset it is DERIVED from that asset. Therefore:
--
--   (1) A vehicle excludes a property and an asset. A vehicle is not at a
--       property. (This is what the original CHECK said; 083's CHECKs still
--       say it and are kept unchanged as the backstop.)
--
--   (2) With asset_id set, a property_id the caller PRESENTS must be the
--       asset's property. A different value is a CONTRADICTION and is refused
--       with an error, not silently corrected. NULL means "not presented" and
--       is derived, as before.
--
-- WHAT "PRESENTED" MEANS, PRECISELY
--
--   INSERT  the row's own property_id, when NOT NULL. A caller inserting a row
--           is stating every value in it.
--   UPDATE  only when the statement changed it: NEW.property_id IS DISTINCT
--           FROM OLD.property_id. A value carried over from the stored row is
--           not a claim about this write — it is the previous derivation (or a
--           value from before this rule), and it is re-derived.
--   NULL    is never a claim, in either case. NULL means "derive it" (and the
--           asset FKs' ON DELETE SET NULL produces one when an asset leaves its
--           property), so a NULL property_id is always derived, never refused.
--
--   This is the crux of the ticket. An UPDATE that changes a bill's asset from
--   A to B and leaves property_id alone is the ordinary API path
--   (apps/api/internal/subject.Fields.Apply carries the stored subject
--   forward); it must keep working and it does: the carried value is not a
--   claim, so it is re-derived to B's property. An UPDATE that NAMES a
--   disagreeing property is refused. The trigger cannot see intent, only the
--   resulting row, so an UPDATE that writes the same value it already holds is
--   indistinguishable from one that carries it; that is stated here rather
--   than pretended away.
--
--   Derivation is kept for carried values as well, so a row that is stale for
--   any reason is corrected on its next subject write instead of persisting.
--
-- ERRCODES. A contradiction is check_violation (23514), like the CHECKs it
-- subsumes, and it names a constraint so a caller can tell the two shapes
-- apart:
--
--   <table>_subject_check              vehicle alongside a property or asset.
--                                      Same name as the 083 CHECK, so the
--                                      existing Go mappings keep working
--                                      (apps/mcp/internal/bill's
--                                      vehicleExclusive, loan's
--                                      loans_subject_check branch).
--   <table>_subject_property_conflict  asset_id plus a presented property_id
--                                      that is not the asset's property.
--
-- THE CHECKs FROM 083 ARE KEPT, UNTOUCHED. They are still correct and they are
-- now provably incomplete-but-safe: they cannot see the contradiction this
-- trigger refuses. They stay as the backstop for a writer that reaches the
-- table without the trigger — a bulk load with the trigger disabled, a
-- replication apply, or a future trigger list that loses a column. A CHECK
-- cannot be forgotten; a trigger can.
--
-- BACKFILL, BEFORE THE NEW RULE CAN BITE. Two shapes are counted per table:
--
--   A. vehicle_id together with property_id or asset_id. Unreachable under
--      083's CHECK; counted anyway. If one exists the migration REFUSES rather
--      than picking a winner, because a row with two subjects has no derivable
--      answer and guessing would be inventing data.
--   B. asset_id set and property_id NOT the asset's property (including NULL
--      where the asset has a property). Repaired by RE-DERIVING property_id
--      from the asset: the asset is the authority. Counted before and after.
--
-- Everything is measured and reported in NOTICEs; nothing is changed silently.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Measure first. Refuse a shape that has no derivable answer.
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    t          TEXT;
    n_two      BIGINT;
    n_stale    BIGINT;
    total_two  BIGINT := 0;
    total_stale BIGINT := 0;
BEGIN
    FOREACH t IN ARRAY ARRAY['bills', 'loans', 'maintenance_schedules',
                             'maintenance_tasks', 'guides'] LOOP
        EXECUTE format(
            'SELECT count(*) FROM %I WHERE vehicle_id IS NOT NULL AND (property_id IS NOT NULL OR asset_id IS NOT NULL)',
            t) INTO n_two;

        EXECUTE format(
            'SELECT count(*) FROM %I r JOIN assets a ON a.id = r.asset_id
              WHERE r.property_id IS DISTINCT FROM a.property_id',
            t) INTO n_stale;

        total_two   := total_two   + n_two;
        total_stale := total_stale + n_stale;

        RAISE NOTICE '087 pre-backfill %: % row(s) with a vehicle plus another subject, % row(s) whose property_id is not their asset''s property',
            rpad(t, 22), n_two, n_stale;
    END LOOP;

    RAISE NOTICE '087 pre-backfill total: % two-subject row(s), % stale property_id row(s)', total_two, total_stale;

    IF total_two > 0 THEN
        RAISE EXCEPTION '087: % row(s) have a vehicle plus a property or asset. There is no derivable answer for those: split them by hand (decide which subject the row is about) and re-run.', total_two;
    END IF;
END $$;

-- ---------------------------------------------------------------------------
-- 2. The rule, in the one place that can express it.
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
BEGIN
    -- Did THIS write present a property_id? See "WHAT PRESENTED MEANS" above.
    IF TG_OP = 'INSERT' THEN
        v_presented := NEW.property_id IS NOT NULL;
    ELSE
        v_presented := NEW.property_id IS DISTINCT FROM OLD.property_id;
    END IF;

    -- An asset decides the property. A presented value that disagrees is the
    -- contradiction this trigger exists to refuse.
    IF NEW.asset_id IS NOT NULL THEN
        SELECT household_id, property_id, TRUE INTO v_household, v_property, v_found
          FROM assets WHERE id = NEW.asset_id;
        IF v_found THEN
            IF v_household <> NEW.household_id THEN
                RAISE EXCEPTION '%: asset % does not exist in this family', TG_TABLE_NAME, NEW.asset_id
                    USING ERRCODE = 'foreign_key_violation';
            END IF;
            -- A NULL property_id is never a contradiction: it is the caller
            -- saying "derive it" (and the asset FKs' ON DELETE SET NULL
            -- produces one when an asset leaves its property). Only a value the
            -- caller actually presented, and that differs, is refused.
            IF v_presented AND NEW.property_id IS NOT NULL AND NEW.property_id IS DISTINCT FROM v_property THEN
                RAISE EXCEPTION
                    '%: property_id % contradicts asset % , which is at property %',
                    TG_TABLE_NAME, NEW.property_id, NEW.asset_id, COALESCE(v_property::text, 'none')
                    USING ERRCODE = 'check_violation',
                          CONSTRAINT = TG_TABLE_NAME || '_subject_property_conflict',
                          HINT = 'A record about an asset is about that asset''s property. Leave property_id out (or NULL) with asset_id and it is derived from the asset. To file this against a different property, name that property and not the asset.';
            END IF;
            -- Derivation, unchanged from 038: the asset is the authority. This
            -- also corrects a carried value that is stale, so the invariant
            -- holds after every write that fires this trigger.
            NEW.property_id := v_property;
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
        -- asset-subject row that also names a vehicle is caught too (its
        -- property_id is now set from the asset). The CONSTRAINT name is the
        -- table's *_subject_check, so callers that already map that name keep
        -- working; the CHECK from 083 remains as the backstop.
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

-- The triggers already exist from 038 (bills, loans, maintenance_schedules,
-- maintenance_tasks) and 049 (guides) and call this function by name, so
-- CREATE OR REPLACE above is the whole change to them. Restated here so the
-- trigger list is readable in one place and a future reader does not have to
-- guess which tables the rule covers.
COMMENT ON TRIGGER bills_derive_subject_property ON bills IS 'Migration 087: refuses a contradictory subject (see derive_subject_property).';
COMMENT ON TRIGGER loans_derive_subject_property ON loans IS 'Migration 087: refuses a contradictory subject (see derive_subject_property).';
COMMENT ON TRIGGER maintenance_schedules_derive_subject_property ON maintenance_schedules IS 'Migration 087: refuses a contradictory subject (see derive_subject_property).';
COMMENT ON TRIGGER maintenance_tasks_derive_subject_property ON maintenance_tasks IS 'Migration 087: refuses a contradictory subject (see derive_subject_property).';
COMMENT ON TRIGGER guides_derive_subject_property ON guides IS 'Migration 087: refuses a contradictory subject (see derive_subject_property).';

-- ---------------------------------------------------------------------------
-- 3. Backfill: re-derive every stale property_id from its asset.
--
-- The UPDATE presents the asset's own property, so the trigger accepts it —
-- the repair goes through the same rule as every other write, which is the
-- proof that the rule and its maintenance path agree.
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    t        TEXT;
    n_before BIGINT;
    n_after  BIGINT;
    n_fixed  BIGINT;
    total    BIGINT := 0;
BEGIN
    FOREACH t IN ARRAY ARRAY['bills', 'loans', 'maintenance_schedules',
                             'maintenance_tasks', 'guides'] LOOP
        EXECUTE format(
            'SELECT count(*) FROM %I r JOIN assets a ON a.id = r.asset_id
              WHERE r.property_id IS DISTINCT FROM a.property_id',
            t) INTO n_before;

        IF n_before > 0 THEN
            EXECUTE format(
                'UPDATE %I r SET property_id = a.property_id
                   FROM assets a
                  WHERE a.id = r.asset_id
                    AND r.property_id IS DISTINCT FROM a.property_id',
                t);
            GET DIAGNOSTICS n_fixed = ROW_COUNT;
        ELSE
            n_fixed := 0;
        END IF;

        EXECUTE format(
            'SELECT count(*) FROM %I r JOIN assets a ON a.id = r.asset_id
              WHERE r.property_id IS DISTINCT FROM a.property_id',
            t) INTO n_after;

        total := total + n_fixed;
        RAISE NOTICE '087 backfill %: % row(s) contradicted their asset, % changed, % still disagree',
            rpad(t, 22), n_before, n_fixed, n_after;
    END LOOP;

    RAISE NOTICE '087 backfill total: % row(s) re-derived from their asset', total;
END $$;

COMMIT;
