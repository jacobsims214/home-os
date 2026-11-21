-- 078_activity_entity_kind.up.sql (#5509)
--
-- Finding F1 in architecture/schema-review-2026-11.md.
--
-- THE DEFECT. activity_log.entity_type and activity_log.parent_type (050)
-- were plain VARCHAR(50). The only constraint on entity_type was
-- activity_log_entity_type_check CHECK (btrim(entity_type) <> '') -- "not
-- blank" -- and parent_type had none at all (activity_log_parent_check only
-- enforces that parent_type and parent_id are NULL together). Proven on a
-- live cluster: entity_type = 'not_a_real_table' was ACCEPTED. They were the
-- only two entity_type columns in the database not governed by a domain;
-- notes, files, secrets, document_links, lists, notifications and
-- calendar_objects all use entity_kind.
--
-- WHY IT MATTERS. activity_log.entity_type is what the activity feed, the
-- entity detail pages and the MCP `activity` tools resolve against. A row
-- whose entity_type is a typo or a renamed kind is INVISIBLE: no join finds
-- it, no page lists it, no agent can fetch it. The record that a change
-- happened exists and cannot be read -- and this is the one table where a
-- wrong write is permanent.
--
-- THE DRIFT, MEASURED (not guessed). Three vocabularies answer "what kind of
-- thing is this", and before this migration they disagreed:
--
--   entity_kind domain (070)                 23 members
--   log_activity('...') trigger arguments    25 -- the 23 minus 'household',
--                                            plus list_item, meal_feedback,
--                                            person_preference
--   role_can_see_entity(kind) branches       29 -- the 23, plus list_item,
--                                            meal_feedback,
--                                            person_preference, and the three
--                                            legacy/alias kinds document,
--                                            maintenance, kb_doc
--
-- So exactly three kinds were WRITTEN but not in the domain: list_item,
-- meal_feedback, person_preference. They are real records with real tables,
-- real triggers and real readers; they were simply never added to
-- entity_kind, because entity_kind also governs what a note or a file may
-- ATTACH TO and those three are not note targets.
--
-- THE FIX: A SEPARATE DOMAIN, NOT A WIDER entity_kind. Widening entity_kind
-- would silently let a note attach to a person_preference, which is wrong.
-- activity_entity_kind is the audit feed's own vocabulary: every entity_kind
-- member (so the domain stays a superset of the thing it mirrors) plus the
-- three kinds that only the feed writes. 'household' is a member because it
-- is a valid entity_kind and role_can_see_entity has its branch, even though
-- no trigger logs the household row itself -- see the test, which names that
-- one exception rather than leaving it implied.
--
-- 'membership' is deliberately NOT here: that is the #audit-memberships
-- ticket's job.
--
-- REPAIR POLICY (do not silently rewrite history). Before constraining, this
-- migration reports every out-of-domain value with its row count, then
-- repairs only the values that have a NAMED, LOSSLESS target:
--
--   document    -> file             019/024 folded the documents table into
--                                   files; role_can_see_entity maps both to
--                                   the 'files' resource, so a reader already
--                                   treats them as the same kind.
--   maintenance -> maintenance_task 039 performed this exact rename on
--                                   calendar_objects.entity_type.
--
-- Neither can exist in a database migrated to 050 (activity_log was created
-- in 050, after 024 dropped documents and after 039 renamed maintenance), so
-- in practice these two branches repair nothing -- they are here because a
-- database restored from an older dump, or written by an older build, is
-- exactly the case a migration has to survive.
--
-- Anything else is NOT repaired and NOT dropped: the migration stops with the
-- offending values and counts in the message. A kind nobody can name is a bug
-- in a writer, and rewriting it to something plausible would put a false
-- statement into the audit log -- the one table where a wrong value is
-- permanent. Fix the writer, then run this migration again.
--
-- LOCKING. activity_log is append-only and grows; the validation scan must
-- not hold ACCESS EXCLUSIVE. Each column gets an equivalent CHECK added
-- NOT VALID, then VALIDATE CONSTRAINT (SHARE UPDATE EXCLUSIVE, concurrent
-- reads and writes), and only then ALTER COLUMN TYPE. PostgreSQL 16 proves
-- the domain's constraint from the validated check and skips its own scan,
-- so the ACCESS EXCLUSIVE step is a catalogue change (measured on 400k rows:
-- 254ms with the pre-check vs 783ms without).
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. The audit feed's vocabulary
-- ---------------------------------------------------------------------------

-- Keep in step with entity_kind (070) and with role_can_see_entity (061/076).
-- A kind added to any of the three has to be added to the others: the test in
-- apps/mcp/internal/testutil/activity_entity_kind_test.go fails otherwise.
CREATE DOMAIN activity_entity_kind AS VARCHAR(50)
    CONSTRAINT activity_entity_kind_check CHECK (VALUE IN (
        -- entity_kind, 070's full list
        'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
        'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
        'file', 'note', 'secret',
        'person',
        'budget',
        'list',
        'recipe', 'meal_plan',
        'guide',
        'calendar', 'calendar_event', 'document_link',
        -- logged by 050's triggers, never entity_kind members (not note targets)
        'list_item', 'meal_feedback', 'person_preference'
    ));

COMMENT ON DOMAIN activity_entity_kind IS
    'What kind of thing an activity_log row is about (#5509). The 23 entity_kind members plus list_item, meal_feedback and person_preference, which only the audit feed writes. Separate from entity_kind on purpose: entity_kind also governs what a note or file may attach to.';

-- ---------------------------------------------------------------------------
-- 2. Report, then repair, before anything is constrained
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    r         RECORD;
    n_bad     INTEGER := 0;
    n_entity  INTEGER := 0;
    n_parent  INTEGER := 0;
    v_others  TEXT;
BEGIN
    -- The pre-migration picture, one line per offending value. This runs
    -- before any repair so the operator sees what was actually there.
    FOR r IN
        SELECT 'entity_type' AS col, entity_type::text AS val, count(*) AS n
          FROM activity_log
         WHERE entity_type::text NOT IN (
                   'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
                   'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
                   'file', 'note', 'secret', 'person', 'budget', 'list',
                   'recipe', 'meal_plan', 'guide', 'calendar', 'calendar_event',
                   'document_link', 'list_item', 'meal_feedback', 'person_preference')
         GROUP BY entity_type
        UNION ALL
        SELECT 'parent_type', parent_type::text, count(*)
          FROM activity_log
         WHERE parent_type IS NOT NULL
           AND parent_type::text NOT IN (
                   'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
                   'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
                   'file', 'note', 'secret', 'person', 'budget', 'list',
                   'recipe', 'meal_plan', 'guide', 'calendar', 'calendar_event',
                   'document_link', 'list_item', 'meal_feedback', 'person_preference')
         GROUP BY parent_type
         ORDER BY 1, 3 DESC
    LOOP
        RAISE NOTICE '078: activity_log.% has % row(s) with out-of-domain value %',
            r.col, r.n, quote_literal(r.val);
    END LOOP;

    SELECT count(*) INTO n_bad FROM activity_log
     WHERE entity_type::text NOT IN (
               'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
               'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
               'file', 'note', 'secret', 'person', 'budget', 'list',
               'recipe', 'meal_plan', 'guide', 'calendar', 'calendar_event',
               'document_link', 'list_item', 'meal_feedback', 'person_preference');

    IF n_bad = 0 THEN
        RAISE NOTICE '078: activity_log.entity_type -- no out-of-domain rows to repair';
    END IF;

    -- Named, lossless renames only. See the header for why each is safe.
    UPDATE activity_log SET entity_type = 'file'
     WHERE entity_type::text = 'document';
    GET DIAGNOSTICS n_entity = ROW_COUNT;
    IF n_entity > 0 THEN
        RAISE NOTICE '078: repaired % activity_log.entity_type row(s): document -> file', n_entity;
    END IF;

    UPDATE activity_log SET entity_type = 'maintenance_task'
     WHERE entity_type::text = 'maintenance';
    GET DIAGNOSTICS n_entity = ROW_COUNT;
    IF n_entity > 0 THEN
        RAISE NOTICE '078: repaired % activity_log.entity_type row(s): maintenance -> maintenance_task', n_entity;
    END IF;

    UPDATE activity_log SET parent_type = 'file'
     WHERE parent_type::text = 'document';
    GET DIAGNOSTICS n_parent = ROW_COUNT;
    IF n_parent > 0 THEN
        RAISE NOTICE '078: repaired % activity_log.parent_type row(s): document -> file', n_parent;
    END IF;

    UPDATE activity_log SET parent_type = 'maintenance_task'
     WHERE parent_type::text = 'maintenance';
    GET DIAGNOSTICS n_parent = ROW_COUNT;
    IF n_parent > 0 THEN
        RAISE NOTICE '078: repaired % activity_log.parent_type row(s): maintenance -> maintenance_task', n_parent;
    END IF;

    IF n_entity + n_parent > 0 THEN
        RAISE NOTICE '078: % activity_log row(s) repaired in total', n_entity + n_parent;
    END IF;

    -- Whatever is left has no named target. Stop, and say exactly what and
    -- where, rather than guessing or deleting an audit row.
    -- Built with quote_literal() so the message carries a statement that can
    -- be pasted as-is. RAISE only knows %, not %L.
    SELECT string_agg(DISTINCT quote_literal(v), ', ' ORDER BY quote_literal(v)) INTO v_others
      FROM (
            SELECT entity_type::text AS v FROM activity_log
             WHERE entity_type::text NOT IN (
                       'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
                       'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
                       'file', 'note', 'secret', 'person', 'budget', 'list',
                       'recipe', 'meal_plan', 'guide', 'calendar', 'calendar_event',
                       'document_link', 'list_item', 'meal_feedback', 'person_preference')
            UNION
            SELECT parent_type::text FROM activity_log
             WHERE parent_type IS NOT NULL
               AND parent_type::text NOT IN (
                       'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
                       'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
                       'file', 'note', 'secret', 'person', 'budget', 'list',
                       'recipe', 'meal_plan', 'guide', 'calendar', 'calendar_event',
                       'document_link', 'list_item', 'meal_feedback', 'person_preference')
           ) s;

    IF v_others IS NOT NULL THEN
        RAISE EXCEPTION
            '078: activity_log still has out-of-domain kind(s) with no named repair: %. Nothing was changed. Inspect with: SELECT entity_type, parent_type, count(*) FROM activity_log WHERE entity_type::text IN (%) OR parent_type::text IN (%) GROUP BY 1, 2; fix the writer that produced them, then re-run.',
            v_others, v_others, v_others;
    END IF;
END
$$;

-- ---------------------------------------------------------------------------
-- 3. Constrain entity_type, without a long ACCESS EXCLUSIVE scan
-- ---------------------------------------------------------------------------

-- Same member list as the domain: with it validated, PostgreSQL 16 proves the
-- domain's constraint and ALTER COLUMN TYPE does not re-scan the table.
ALTER TABLE activity_log
    ADD CONSTRAINT activity_log_entity_type_kind
    CHECK (entity_type IN (
        'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
        'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
        'file', 'note', 'secret', 'person', 'budget', 'list',
        'recipe', 'meal_plan', 'guide', 'calendar', 'calendar_event',
        'document_link', 'list_item', 'meal_feedback', 'person_preference'
    )) NOT VALID;

ALTER TABLE activity_log VALIDATE CONSTRAINT activity_log_entity_type_kind;

ALTER TABLE activity_log
    ALTER COLUMN entity_type TYPE activity_entity_kind
    USING entity_type::activity_entity_kind;

-- The pre-check has done its job; the domain is the one source of truth now.
ALTER TABLE activity_log DROP CONSTRAINT activity_log_entity_type_kind;

-- ---------------------------------------------------------------------------
-- 4. The same for parent_type, which is nullable (NULL for top-level entries)
-- ---------------------------------------------------------------------------

-- A domain constraint is not evaluated for NULL, so a top-level entry stays
-- legal; activity_log_parent_check still ties parent_type to parent_id.
ALTER TABLE activity_log
    ADD CONSTRAINT activity_log_parent_type_kind
    CHECK (parent_type IN (
        'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
        'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
        'file', 'note', 'secret', 'person', 'budget', 'list',
        'recipe', 'meal_plan', 'guide', 'calendar', 'calendar_event',
        'document_link', 'list_item', 'meal_feedback', 'person_preference'
    )) NOT VALID;

ALTER TABLE activity_log VALIDATE CONSTRAINT activity_log_parent_type_kind;

ALTER TABLE activity_log
    ALTER COLUMN parent_type TYPE activity_entity_kind
    USING parent_type::activity_entity_kind;

ALTER TABLE activity_log DROP CONSTRAINT activity_log_parent_type_kind;

COMMIT;
