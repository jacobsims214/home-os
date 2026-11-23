-- 093_households_default_property_family_fk.up.sql (#5521, schema review F6)
--
-- THE DEFECT. 017 added (and 038 carried) the reference on the one table that
-- DEFINES tenancy as a plain single-column FK:
--
--   households_default_property_id_fkey
--     FOREIGN KEY (default_property_id) REFERENCES properties(id)
--     ON DELETE SET NULL
--
-- Measured on the catalogue at 092 (PG16), not assumed:
--
--   conname                              | contype | ncols_child | ncols_parent
--   -------------------------------------+---------+-------------+-------------
--   households_default_property_id_fkey  | f       |           1 |            1
--
-- So household A could set its default_property_id to household B's house and
-- the database accepted it. Reproduced on a scratch database at 092:
--
--   UPDATE households SET default_property_id = '<B property>'
--    WHERE id = '<household A>';          --> UPDATE 1        (WRONG)
--
-- Every other family-scoped reference either carries a composite same-family FK
-- (058, 091) or a trigger guard. This column had neither -- and unlike the
-- tables 058/091 converted, it is the tenant key itself, so a cross-family
-- value here makes "the house" mean someone else's house for every query that
-- resolves the default property.
--
-- WHY THIS TABLE NEEDS A DIFFERENT COLUMN PAIR. Everywhere else the child
-- table has a `household_id` column, so the composite is
-- (something_id, household_id) -> (id, household_id). `households` has NO
-- household_id column: the row IS the household, and its PRIMARY KEY `id` is
-- the tenant key. The composite therefore pairs the referencing column with
-- the row's own primary key:
--
--   FOREIGN KEY (default_property_id, id) REFERENCES properties (id, household_id)
--
--    default_property_id -> properties.id            (the property referenced)
--    households.id       -> properties.household_id  (the family it must belong to)
--
-- i.e. "the referenced property's household_id must equal this row's id". That
-- is the same-family check, expressed against the tenant key rather than a
-- duplicate of it.
--
-- COLUMN ORDER IS LOAD-BEARING and the intuitive order is wrong. Checked both
-- ways on PG16 before committing to this one:
--
--   (default_property_id, id) -> (id, household_id)   ACCEPTED  (this migration)
--     same-household UPDATE succeeds; cross-household UPDATE fails 23503.
--
--   (id, default_property_id) -> (id, household_id)   also created, but it
--     means households.id = properties.id, so it REJECTS a legitimate
--     same-household assignment:
--       ERROR: Key (id, default_property_id)=(<A>, <A property>) is not
--              present in table "properties"
--     A constraint that refuses the valid case is not a stricter guard, it is
--     a broken one -- it would have made default_property_id effectively
--     unusable for every household whose id is not also a property id, which
--     is all of them.
--
-- The parent side is available: `properties` carries
-- properties_id_household_id_key UNIQUE (id, household_id) -- added by 084
-- (#5512), read from pg_constraint at 092 before relying on it, so this
-- migration does not itself need to add a key.
--
-- ON DELETE BEHAVIOUR IS PRESERVED. confdeltype was 'n' (SET NULL) before and
-- is 'n' after; confupdtype 'a' (NO ACTION) and confmatchtype 's' (SIMPLE) are
-- likewise unchanged. The PG15+ column-list form is REQUIRED here and is not a
-- style choice: a bare `ON DELETE SET NULL` on a two-column key nulls BOTH
-- child columns, and `households.id` is the primary key. Measured with the
-- bare form:
--
--   ERROR: null value in column "id" of relation "households" violates
--          not-null constraint
--   CONTEXT: UPDATE ONLY "public"."households" SET "default_property_id" = NULL,
--            "id" = NULL WHERE ...
--
-- so deleting a property would have failed outright instead of clearing the
-- default. `ON DELETE SET NULL (default_property_id)` nulls only the reference.
-- This is the same form 043/058/091 use for the same reason.
--
-- A CHECK constraint could not express this: it cannot reference another table.
-- The declarative composite FK works, so no trigger is needed -- which matters,
-- because a trigger only guards writes it is attached to and is bypassed by
-- session_replication_role = replica, while an FK is enforced by the executor.
--
-- WHAT CHANGES FOR CALLERS. Moving a property to another household
-- (UPDATE properties SET household_id = ...) now fails 23503 if any household
-- still names it as default. That is the intended reading of the constraint
-- (a house cannot be someone's default and belong to someone else), and no
-- application code re-homes properties: the only `UPDATE properties` in the
-- tree sets presentation columns under `WHERE id = $1 AND household_id = $2`.
--
-- Constraint name is unchanged, so 017's down migration still finds it.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Repair existing cross-family references
-- ---------------------------------------------------------------------------
-- Such a row is corrupt data, not a workflow: default_property_id is written
-- only by admins, and nothing in the tree writes another household's property
-- id, so a non-zero count here means a COPY/pg_restore, a manual write, or a
-- session_replication_role = replica session. 058 and 091 both repair rather
-- than fail, and both report the count; the same is done here. The count is
-- reported even when zero, so a fresh database says so out loud instead of
-- being silent.
--
-- The repair is to NULL the reference, matching the ON DELETE SET NULL action
-- this column already has: a household pointing outside itself is treated as
-- having no default. It is never re-pointed at anything, because picking a
-- house for a family is a product decision, not a migration's.
DO $$
DECLARE
    n BIGINT;
    bad RECORD;
BEGIN
    SELECT count(*) INTO n
      FROM households h
      JOIN properties p ON p.id = h.default_property_id
     WHERE p.household_id <> h.id;

    RAISE NOTICE '093: % cross-family households.default_property_id row(s)', n;

    IF n > 0 THEN
        FOR bad IN
            SELECT h.id AS household_id, h.default_property_id, p.household_id AS actual_owner
              FROM households h
              JOIN properties p ON p.id = h.default_property_id
             WHERE p.household_id <> h.id
        LOOP
            RAISE NOTICE '093: household % default_property_id % belongs to household % -- nulling',
                bad.household_id, bad.default_property_id, bad.actual_owner;
        END LOOP;

        UPDATE households h
           SET default_property_id = NULL
          FROM properties p
         WHERE p.id = h.default_property_id
           AND p.household_id <> h.id;
    END IF;
END $$;

-- ---------------------------------------------------------------------------
-- 2. Single-column FK -> composite same-family FK
-- ---------------------------------------------------------------------------
-- Dropped and re-added under the SAME name so that
-- 017_calendar_property_event_type.down.sql (which drops the column, and with
-- it the constraint) and any operator script naming the constraint keep
-- working. 058 and 091 did the same deliberately.
--
-- No trigger-ordering hazard of the 091 kind here: this is the only constraint
-- on `households` taking an ON DELETE action, and the action touches a
-- nullable column of the row it already owns rather than a column another
-- constraint also watches. Both property-delete paths were measured at this
-- definition before shipping: deleting a property that is a default nulls the
-- column and leaves the household row intact, and deleting a whole household
-- (whose properties cascade) succeeds.
ALTER TABLE households
    DROP CONSTRAINT households_default_property_id_fkey;

ALTER TABLE households
    ADD CONSTRAINT households_default_property_id_fkey
    FOREIGN KEY (default_property_id, id)
    REFERENCES properties (id, household_id)
    ON DELETE SET NULL (default_property_id);

COMMIT;
