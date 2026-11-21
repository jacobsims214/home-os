-- 077_set_updated_at.up.sql (#5508)
--
-- THE DEFECT. Every one of the 25 tables with an updated_at column declares
-- it TIMESTAMPTZ NOT NULL DEFAULT NOW(), but only three had a trigger that
-- moves it on UPDATE: lists (046), recipes (047) and guides (049). On the
-- other 22 it moved only when the writer remembered to write
-- `updated_at = NOW()` into its own statement. The API and MCP bill-update
-- paths already disagree: one bumps it, the other does not.
--
-- WHY IT MATTERS. updated_at is the sort key for "what changed recently",
-- and 050's log_activity() deliberately EXCLUDES updated_at from
-- changed_fields precisely BECAUSE it is supposed to be bumped by the
-- database. So today the audit log records "updated bill (amount)" while the
-- row itself says nothing happened -- the two records of the same edit
-- disagree.
--
-- THE FIX. One function, set_updated_at(), attached by BEFORE UPDATE trigger
-- to every table that has the column. The three bespoke copies of the same
-- body (lists_touch_updated_at, recipes_touch_updated_at,
-- guides_touch_updated_at) are dropped, so there is one implementation of
-- "when did this row last change".
--
-- FOUND BY CATALOGUE, NOT BY GREPPING MIGRATIONS. The loop below asks
-- information_schema which tables have an updated_at column, so a table
-- added between 074 and here is covered without editing this file. It also
-- means the migration cannot silently miss a table whose name nobody
-- remembered to add to a hand-written list.
--
-- WHAT IT DELIBERATELY DOES NOT TOUCH.
--   * The 14 tables with no updated_at column, which stay as they are:
--     activity_log, agent_tokens, calendar_changes, document_links,
--     file_blobs, file_search_deletions, guide_revisions, invitations,
--     memberships, notifications, outbox_events, password_reset_tokens,
--     rooms, secret_keys. (rooms is the odd one out -- it is not append-only,
--     it simply never got the column; giving it one is a different change.)
--   * The redundant `NEW.updated_at := NOW()` inside meal_plans_before_write
--     (047) and list_items_before_write (046). NOW() is the transaction
--     timestamp, so the trigger here writes the identical value; removing
--     the dead line is a cleanup, not part of this fix.
--
-- TRIGGER ORDER IS LOAD-BEARING, AND THIS NAME IS WHAT MAKES IT WORK.
-- Postgres fires BEFORE row triggers in name order. <table>_set_updated_at is
-- the last of each table's BEFORE triggers: 's' sorts after every
-- authorship/derive/enforce/default/before_write/checked_by trigger name in
-- this schema, and after calendar_objects_set_event_times because 'e' < 'u'.
--
-- Why it matters: set_authorship() decides "only bookkeeping columns changed,
-- this is not an edit by anyone" by comparing the whole row with the
-- authorship columns removed. If updated_at moved BEFORE it ran, that
-- comparison would see a change on every no-op UPDATE, and updated_by would
-- be rewritten to the actor (or to NULL when no actor is set) -- silently
-- blaming whoever ran a query that changed nothing.
--
-- Proved both ways on a live cluster: with this name, a no-op UPDATE leaves
-- bills.updated_by exactly as it was; renaming the trigger to fire before
-- bills_authorship makes the same statement set updated_by to NULL. Do not
-- rename this trigger to something that sorts earlier.
--
-- NO-OP UPDATES STAY SILENT. log_activity() ignores updated_at when it
-- builds changed_fields, so the unconditional bump still leaves a no-op
-- UPDATE's changed_fields empty and the early RETURN NULL intact. Verified
-- in updated_at_test.go (a spurious activity row on every no-op would be a
-- regression).
--
-- Runs in one transaction so a failure leaves the database at 074.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. One implementation
-- ---------------------------------------------------------------------------

CREATE FUNCTION set_updated_at() RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := NOW();
    RETURN NEW;
END;
$$;

COMMENT ON FUNCTION set_updated_at() IS
    'BEFORE UPDATE: moves updated_at to the transaction timestamp. Attached to every table with an updated_at column (077, #5508); replaces the per-table copies in 046/047/049.';

-- ---------------------------------------------------------------------------
-- 2. Every table that has the column, found in the catalogue
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    v_table TEXT;
    v_count INT := 0;
BEGIN
    FOR v_table IN
        SELECT c.table_name
          FROM information_schema.columns c
          JOIN information_schema.tables t
            ON t.table_schema = c.table_schema
           AND t.table_name   = c.table_name
           AND t.table_type   = 'BASE TABLE'
         WHERE c.table_schema = 'public'
           AND c.column_name  = 'updated_at'
         ORDER BY c.table_name
    LOOP
        -- The three bespoke triggers (046/047/049) are replaced, not doubled:
        -- two BEFORE triggers both setting NEW.updated_at is harmless today
        -- but leaves two explanations for one column. Checked in pg_trigger
        -- rather than DROP TRIGGER IF EXISTS, which would print a NOTICE for
        -- each of the other 22 tables that never had one.
        IF EXISTS (SELECT 1 FROM pg_trigger
                    WHERE tgrelid = format('public.%I', v_table)::regclass
                      AND tgname  = v_table || '_touch_updated_at') THEN
            EXECUTE format('DROP TRIGGER %I ON %I',
                           v_table || '_touch_updated_at', v_table);
        END IF;

        EXECUTE format(
            'CREATE TRIGGER %I BEFORE UPDATE ON %I
                 FOR EACH ROW EXECUTE FUNCTION set_updated_at()',
            v_table || '_set_updated_at', v_table);

        v_count := v_count + 1;
    END LOOP;

    -- The catalogue at 074 has 25 such tables, and the migration must cover
    -- all of them -- none may be left without a trigger. The check is written
    -- as "every table that has the column ends up covered", NOT "v_count = 25":
    -- migrations 075+ are in flight on other branches and at least two of them
    -- add a table with an updated_at column (payment_accounts in #5497,
    -- kb_docs in #5502). A hardcoded 25 would abort this migration for
    -- whoever merges it after one of those, for no reason -- the loop above
    -- already picks a new table up by itself. 25 is the floor: fewer means the
    -- loop did not see the schema it was written for.
    IF v_count < 25 THEN
        RAISE EXCEPTION '077: expected at least 25 tables with updated_at, found %', v_count;
    END IF;

    IF EXISTS (
        SELECT 1
          FROM information_schema.columns c
         WHERE c.table_schema = 'public'
           AND c.column_name  = 'updated_at'
           AND NOT EXISTS (
               SELECT 1 FROM pg_trigger t
                 JOIN pg_class cl ON cl.oid = t.tgrelid
                 JOIN pg_proc p   ON p.oid = t.tgfoid
                WHERE cl.relname = c.table_name
                  AND p.proname  = 'set_updated_at')
    ) THEN
        RAISE EXCEPTION '077: a table with updated_at has no set_updated_at trigger';
    END IF;
END
$$;

-- ---------------------------------------------------------------------------
-- 3. The bespoke bodies are gone, not just unused
-- ---------------------------------------------------------------------------

DROP FUNCTION IF EXISTS lists_touch_updated_at();
DROP FUNCTION IF EXISTS recipes_touch_updated_at();
DROP FUNCTION IF EXISTS guides_touch_updated_at();

COMMIT;
