-- 089_subject_check_num_nonnulls.up.sql (#5515)
--
-- Finding F2 in architecture/schema-review-2026-11.md: five tables carry the
-- same three-column "subject" pattern (property_id, asset_id, vehicle_id) and
-- the same CHECK, which the review read as asymmetric and permissive:
--
--     CHECK (vehicle_id IS NULL OR (property_id IS NULL AND asset_id IS NULL))
--
-- on bills (038), loans (038), maintenance_schedules (056),
-- maintenance_tasks (056) and guides (049). Five tables, one rule, copied.
--
-- WHAT THIS MIGRATION DOES, AND WHAT IT DELIBERATELY DOES NOT
--
-- The review asked for `num_nonnulls(property_id, asset_id, vehicle_id) <= 1`
-- on all five. That expression cannot be shipped: it rejects every row whose
-- subject is an asset. derive_subject_property() (038) is a BEFORE INSERT OR
-- UPDATE trigger on all five of these tables and it sets
--     NEW.property_id := (SELECT property_id FROM assets WHERE id = NEW.asset_id)
-- so an asset-subject row is *supposed* to have BOTH asset_id and property_id
-- set. Measured on PG16 after the trigger has run:
--
--     property set = t, asset set = t, vehicle set = f
--     num_nonnulls(property_id, asset_id, vehicle_id) = 2
--
-- i.e. "a bill about the furnace" would violate the review's expression. The
-- same is true of loans, maintenance_schedules, maintenance_tasks and guides.
--
-- The real intent is therefore not "at most one of three columns". It is:
--
--     At most one SUBJECT. The subject is vehicle_id or asset_id.
--     property_id is not a third subject: it is the property the record is
--     about, and when the subject is an asset it is a DENORMALISATION of that
--     asset's property (derive_subject_property, maintained on asset move by
--     assets_propagate_property, both 038).
--
-- Stated as two num_nonnulls clauses, which is the same rule the old CHECK
-- enforces and is what this migration installs on all five tables:
--
--     num_nonnulls(asset_id, vehicle_id)  <= 1   -- at most one real subject
--  AND num_nonnulls(property_id, vehicle_id) <= 1 -- a vehicle is not "at" a property
--
-- This is EXACTLY equivalent to the old expression. Proof over all 8
-- null/not-null combinations (measured in the verification harness):
--
--     prop  asset  veh  | old  new | equivalent
--      f      f     f   |  t    t  |     t
--      f      f     t   |  t    t  |     t
--      f      t     f   |  t    t  |     t
--      f      t     t   |  f    f  |     t
--      t      f     f   |  t    t  |     t
--      t      f     t   |  f    f  |     t
--      t      t     f   |  t    t  |     t
--      t      t     t   |  f    f  |     t
--
-- So this migration changes NO row's accept/reject outcome. That is the point:
-- it is a restatement for legibility, not a behaviour change. It is also not
-- possible to go further at row level. The obvious "tightening" that excludes
-- the derived column --
--     num_nonnulls(CASE WHEN asset_id IS NULL THEN property_id END, asset_id, vehicle_id) <= 1
-- -- is provably identical to the old check (same 8-row table), and that is
-- forced: "at most one subject" plus "asset and property may coexist" means
-- property cannot count when an asset is set. No row-local counting expression
-- is tighter than what is already here.
--
-- The hazard the review actually named -- a row with asset_id plus a STALE
-- property_id, double-counted by
--     property_id = $1 OR asset_id IN (SELECT id FROM assets WHERE property_id = $1)
-- -- is not reachable. derive_subject_property overwrites property_id from the
-- asset on any write of the subject columns, so a direct INSERT or UPDATE that
-- tries to set a disagreeing property is corrected before the row lands
-- (verified on PG16), and assets_propagate_property realigns all five tables
-- when an asset moves to another property. A composite FK
-- (asset_id, property_id) -> assets(id, property_id) was evaluated as the
-- declarative version of that invariant and rejected as redundant with the
-- trigger: it can never fire.
--
-- What this migration DOES add beyond the restatement is the rule itself, on
-- each constraint, as COMMENT ON CONSTRAINT -- so a schema reader learns
-- "property rides along with asset" from the catalogue rather than from Go.
-- That is the "one rule, one home" fix the review was reaching for.
--
-- Runs in one transaction. Constraint replacement is DROP + ADD NOT VALID +
-- VALIDATE so the five tables are not scanned under an ACCESS EXCLUSIVE lock
-- while validating (the review's step 2).

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Backfill BEFORE the constraint, so the ALTER cannot fail on old rows.
--
-- Two shapes are checked, both reported. Neither is expected to find anything:
-- the first was already impossible under the old CHECK, and the second is
-- prevented by derive_subject_property.
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    t             text;
    n_two_subject bigint;
    n_stale_prop  bigint;
    total_stale   bigint := 0;
    total_two     bigint := 0;
BEGIN
    FOREACH t IN ARRAY ARRAY['bills', 'loans', 'maintenance_schedules',
                             'maintenance_tasks', 'guides'] LOOP
        -- Shape 1: more than one real subject (vehicle together with a
        -- property or an asset). Impossible under the old CHECK; counted so
        -- the report is complete rather than assumed.
        EXECUTE format(
            'SELECT count(*) FROM %I
              WHERE vehicle_id IS NOT NULL
                AND (property_id IS NOT NULL OR asset_id IS NOT NULL)', t)
        INTO n_two_subject;

        -- Shape 2: an asset subject whose property_id is not that asset''s
        -- property -- the "stale property" the review names. Repaired by
        -- RE-DERIVING property_id from the asset, never by nulling it: the
        -- asset is the authority, and property_id exists to be filterable, so
        -- re-deriving keeps every "spend on this property" query correct.
        EXECUTE format(
            'SELECT count(*) FROM %I r
              JOIN assets a ON a.id = r.asset_id
              WHERE r.property_id IS DISTINCT FROM a.property_id', t)
        INTO n_stale_prop;

        total_two   := total_two   + n_two_subject;
        total_stale := total_stale + n_stale_prop;

        RAISE NOTICE '089 %: % rows with two real subjects, % rows with a stale property_id',
            t, n_two_subject, n_stale_prop;

        IF n_stale_prop > 0 THEN
            EXECUTE format(
                'UPDATE %I r
                    SET property_id = a.property_id
                   FROM assets a
                  WHERE a.id = r.asset_id
                    AND r.property_id IS DISTINCT FROM a.property_id', t);
        END IF;
    END LOOP;

    RAISE NOTICE '089 backfill total: % two-subject rows, % stale property_id rows repaired',
        total_two, total_stale;

    -- Refuse to proceed on a shape-1 row rather than invent a "winner": with
    -- two real subjects there is no rule that says which one the row was
    -- about. Zero such rows exist (the old CHECK forbade them), so this is a
    -- guard, not an expected path.
    IF total_two > 0 THEN
        RAISE EXCEPTION
            '089: % rows carry two real subjects (vehicle plus property/asset); '
            'they must be split by hand before this constraint can be added', total_two;
    END IF;
END $$;

-- ---------------------------------------------------------------------------
-- 2. Replace the five constraints with the restated rule.
--
-- Same names, so nothing that names them (docs, the down migration) changes.
-- NOT VALID + VALIDATE: ADD ... NOT VALID does not scan the table, and the
-- separate VALIDATE takes only a SHARE UPDATE EXCLUSIVE lock.
-- ---------------------------------------------------------------------------

ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_subject_check;
ALTER TABLE bills ADD CONSTRAINT bills_subject_check
    CHECK (num_nonnulls(asset_id, vehicle_id) <= 1
       AND num_nonnulls(property_id, vehicle_id) <= 1) NOT VALID;
ALTER TABLE bills VALIDATE CONSTRAINT bills_subject_check;

ALTER TABLE loans DROP CONSTRAINT IF EXISTS loans_subject_check;
ALTER TABLE loans ADD CONSTRAINT loans_subject_check
    CHECK (num_nonnulls(asset_id, vehicle_id) <= 1
       AND num_nonnulls(property_id, vehicle_id) <= 1) NOT VALID;
ALTER TABLE loans VALIDATE CONSTRAINT loans_subject_check;

ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_subject_check;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_subject_check
    CHECK (num_nonnulls(asset_id, vehicle_id) <= 1
       AND num_nonnulls(property_id, vehicle_id) <= 1) NOT VALID;
ALTER TABLE maintenance_schedules VALIDATE CONSTRAINT maintenance_schedules_subject_check;

ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_subject_check;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_subject_check
    CHECK (num_nonnulls(asset_id, vehicle_id) <= 1
       AND num_nonnulls(property_id, vehicle_id) <= 1) NOT VALID;
ALTER TABLE maintenance_tasks VALIDATE CONSTRAINT maintenance_tasks_subject_check;

ALTER TABLE guides DROP CONSTRAINT IF EXISTS guides_subject_check;
ALTER TABLE guides ADD CONSTRAINT guides_subject_check
    CHECK (num_nonnulls(asset_id, vehicle_id) <= 1
       AND num_nonnulls(property_id, vehicle_id) <= 1) NOT VALID;
ALTER TABLE guides VALIDATE CONSTRAINT guides_subject_check;

-- ---------------------------------------------------------------------------
-- 3. State the rule once, on each constraint.
--
-- The review's real complaint was that the invariant lived only in
-- derive_subject_property() and in Go, so every consumer had to rediscover it.
-- A constraint comment is read by anyone with \d+ and by the catalogue, which
-- is where a schema reader looks.
-- ---------------------------------------------------------------------------

COMMENT ON CONSTRAINT bills_subject_check ON bills IS
    'At most one SUBJECT per row: the subject is asset_id or vehicle_id. '
    'property_id is not a third subject -- it is the property the record is about, '
    'and when the subject is an asset it is DERIVED from that asset by '
    'derive_subject_property() (038) and realigned by assets_propagate_property '
    'when the asset moves. So asset_id and property_id are set together by design, '
    'and "everything about this property (incl. its assets)" is the single '
    'predicate property_id = $1 -- never an OR over asset_id. A vehicle is never '
    'at a property: vehicle_id excludes both.';

COMMENT ON CONSTRAINT loans_subject_check ON loans IS
    'At most one SUBJECT per row: the subject is asset_id or vehicle_id. '
    'property_id is not a third subject -- it is the property the record is about, '
    'and when the subject is an asset it is DERIVED from that asset by '
    'derive_subject_property() (038) and realigned by assets_propagate_property '
    'when the asset moves. So asset_id and property_id are set together by design, '
    'and "everything about this property (incl. its assets)" is the single '
    'predicate property_id = $1 -- never an OR over asset_id. A vehicle is never '
    'at a property: vehicle_id excludes both.';

COMMENT ON CONSTRAINT maintenance_schedules_subject_check ON maintenance_schedules IS
    'At most one SUBJECT per row: the subject is asset_id or vehicle_id. '
    'property_id is not a third subject -- it is the property the record is about, '
    'and when the subject is an asset it is DERIVED from that asset by '
    'derive_subject_property() (038) and realigned by assets_propagate_property '
    'when the asset moves. So asset_id and property_id are set together by design, '
    'and "everything about this property (incl. its assets)" is the single '
    'predicate property_id = $1 -- never an OR over asset_id. A vehicle is never '
    'at a property: vehicle_id excludes both.';

COMMENT ON CONSTRAINT maintenance_tasks_subject_check ON maintenance_tasks IS
    'At most one SUBJECT per row: the subject is asset_id or vehicle_id. '
    'property_id is not a third subject -- it is the property the record is about, '
    'and when the subject is an asset it is DERIVED from that asset by '
    'derive_subject_property() (038) and realigned by assets_propagate_property '
    'when the asset moves. So asset_id and property_id are set together by design, '
    'and "everything about this property (incl. its assets)" is the single '
    'predicate property_id = $1 -- never an OR over asset_id. A vehicle is never '
    'at a property: vehicle_id excludes both.';

COMMENT ON CONSTRAINT guides_subject_check ON guides IS
    'At most one SUBJECT per row: the subject is asset_id or vehicle_id. '
    'property_id is not a third subject -- it is the property the record is about, '
    'and when the subject is an asset it is DERIVED from that asset by '
    'derive_subject_property() (038) and realigned by assets_propagate_property '
    'when the asset moves. So asset_id and property_id are set together by design, '
    'and "everything about this property (incl. its assets)" is the single '
    'predicate property_id = $1 -- never an OR over asset_id. A vehicle is never '
    'at a property: vehicle_id excludes both.';

COMMIT;
