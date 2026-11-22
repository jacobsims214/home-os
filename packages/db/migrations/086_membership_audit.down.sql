-- 086_membership_audit.down.sql (#5518)
--
-- Back to 085: memberships is silent again.
--
-- WHAT GOES
--   1. the log_activity('membership') trigger on memberships
--   2. membership_set_actor() and its trigger
--   3. memberships.updated_at / memberships.updated_by (and their comments)
--   4. the memberships_set_updated_at trigger (it names a column that is about
--      to disappear, so it must go first)
--   5. log_activity() back to 072's body (no membership label, no role suffix)
--   6. activity_change_values() back to 072's body (no memberships allowlist)
--   7. role_can_see_entity() back to 076's body (no membership branch)
--   8. 'membership' out of activity_entity_kind
--
-- WHAT STAYS, DELIBERATELY
--   * Every activity_log row 086 wrote. This migration removes the writer, not
--     the record. Deleting audit history because a migration was rolled back
--     would destroy exactly the thing the ticket exists to create -- and those
--     rows are the only record that a grant or a demotion ever happened. The
--     domain is narrowed back to 078's 26 members; 'membership' rows already
--     written are NOT deleted and NOT rewritten, so a rolled-back database can
--     hold rows the current domain does not name. That is the same choice 078
--     made (it refused to rewrite or drop unnameable history), and the
--     alternative -- DELETE FROM activity_log -- is worse.
--
--     Consequence to know: re-running 086 up after this down does NOT
--     re-validate those rows (ADD CONSTRAINT on the DOMAIN validates nothing
--     about existing activity_log rows unless ALTER COLUMN TYPE re-runs), so
--     the round trip is safe. Asserted in the test.
--   * The memberships themselves.
--
-- IF EXISTS throughout, like 077/078's down: this also runs inside the older
-- migrations' test chains, where 050 may already have dropped its functions.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. The writer, first: stop writing before taking the columns away
-- ---------------------------------------------------------------------------

DROP TRIGGER IF EXISTS memberships_activity ON memberships;
DROP TRIGGER IF EXISTS memberships_set_actor ON memberships;
DROP TRIGGER IF EXISTS memberships_set_updated_at ON memberships;

DROP FUNCTION IF EXISTS membership_set_actor();

-- ---------------------------------------------------------------------------
-- 2. The columns
-- ---------------------------------------------------------------------------

ALTER TABLE memberships DROP COLUMN IF EXISTS updated_by;
ALTER TABLE memberships DROP COLUMN IF EXISTS updated_at;

-- ---------------------------------------------------------------------------
-- 3. The writer back to 072's body
-- ---------------------------------------------------------------------------

-- 072's log_activity(): the memberships label and the role suffix are gone.
-- Everything else is 072's, so a rollback leaves the other 25 tables' rows
-- byte-identical. IF the function is gone entirely (an earlier down already
-- ran), skip: nothing to restore the body of.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'log_activity') THEN
        RETURN;
    END IF;

    EXECUTE $fn$
CREATE OR REPLACE FUNCTION log_activity() RETURNS TRIGGER
LANGUAGE plpgsql
AS $body$
DECLARE
    v_kind        TEXT := TG_ARGV[0];
    v_row         JSONB;
    v_old         JSONB;
    v_action      TEXT;
    v_changed     TEXT[] := '{}';
    v_changes     JSONB;                                    -- #4941
    v_household   UUID;
    v_parent_type TEXT;
    v_parent_id   UUID;
    v_label       TEXT;
    v_creator     UUID;
    v_summary     TEXT;
    v_raw         TEXT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        v_row := to_jsonb(OLD);
        IF pg_trigger_depth() > 1 OR activity_container_gone(v_row) THEN
            RETURN NULL;
        END IF;
        v_action := 'delete';
    ELSIF TG_OP = 'INSERT' THEN
        v_action := 'create';
        v_row := to_jsonb(NEW);
    ELSE
        v_action := 'update';
        v_row := to_jsonb(NEW);
        v_old := to_jsonb(OLD);
        SELECT COALESCE(array_agg(k ORDER BY k), '{}') INTO v_changed
          FROM jsonb_object_keys(v_row) AS k
         WHERE k NOT IN ('updated_at', 'updated_by', 'created_by', 'author_id',
                         'edited_by', 'ctag', 'etag')
           AND v_row -> k IS DISTINCT FROM v_old -> k;
        IF cardinality(v_changed) = 0 THEN
            RETURN NULL;
        END IF;
        v_changes := activity_change_values(TG_TABLE_NAME, v_old, v_row, v_changed);
    END IF;

    v_household := (v_row ->> 'household_id')::uuid;
    IF v_household IS NULL AND v_row ? 'calendar_id' THEN
        SELECT household_id INTO v_household
          FROM calendars WHERE id = (v_row ->> 'calendar_id')::uuid;
    END IF;
    IF v_household IS NULL THEN
        v_raw := NULLIF(btrim(current_setting('app.household_id', true)), '');
        IF v_raw IS NOT NULL THEN
            BEGIN
                v_household := v_raw::uuid;
            EXCEPTION WHEN invalid_text_representation THEN
                v_household := NULL;
            END;
        END IF;
    END IF;
    IF v_household IS NULL OR NOT EXISTS (SELECT 1 FROM households WHERE id = v_household) THEN
        RETURN NULL;
    END IF;

    CASE TG_TABLE_NAME
        WHEN 'rooms'              THEN v_parent_type := 'property';  v_parent_id := (v_row ->> 'property_id')::uuid;
        WHEN 'calendar_objects'   THEN v_parent_type := 'calendar';  v_parent_id := (v_row ->> 'calendar_id')::uuid;
        WHEN 'list_items'         THEN v_parent_type := 'list';      v_parent_id := (v_row ->> 'list_id')::uuid;
        WHEN 'meal_feedback'      THEN v_parent_type := 'meal_plan'; v_parent_id := (v_row ->> 'meal_plan_id')::uuid;
        WHEN 'person_preferences' THEN v_parent_type := 'person';    v_parent_id := (v_row ->> 'person_id')::uuid;
        ELSE
            IF v_row ? 'entity_type' AND (v_row ->> 'entity_type') <> 'household' THEN
                v_parent_type := v_row ->> 'entity_type';
                v_parent_id := (v_row ->> 'entity_id')::uuid;
            END IF;
    END CASE;
    IF v_parent_type IS NULL OR v_parent_id IS NULL THEN
        v_parent_type := NULL;
        v_parent_id := NULL;
    END IF;

    CASE TG_TABLE_NAME
        WHEN 'vehicles' THEN
            v_label := NULLIF(concat_ws(' ', v_row ->> 'year', v_row ->> 'make', v_row ->> 'model'), '');
        WHEN 'calendar_objects' THEN
            v_label := calendar_event_label(v_row ->> 'ical_data', v_row ->> 'ical_raw');
        WHEN 'person_preferences' THEN
            v_label := concat_ws(': ', v_row ->> 'category', v_row ->> 'value');
        WHEN 'meal_feedback' THEN
            v_label := CASE WHEN v_row ->> 'rating' IS NOT NULL
                            THEN 'rating ' || (v_row ->> 'rating') ELSE 'comment' END;
        WHEN 'document_links' THEN
            v_label := COALESCE(v_row ->> 'title', v_row ->> 'link_type');
        WHEN 'secrets' THEN
            v_label := v_row ->> 'name';
        WHEN 'notes' THEN
            v_label := v_row ->> 'title';
        WHEN 'list_items' THEN
            v_label := v_row ->> 'text';
        ELSE
            v_label := COALESCE(v_row ->> 'title', v_row ->> 'name', v_row ->> 'display_name');
    END CASE;
    v_label := NULLIF(left(regexp_replace(btrim(v_label), '\s+', ' ', 'g'), 80), '');

    v_summary := CASE v_action WHEN 'create' THEN 'created ' WHEN 'update' THEN 'updated ' ELSE 'deleted ' END
              || replace(v_kind, '_', ' ')
              || COALESCE(' "' || v_label || '"', '');
    IF v_action = 'update' THEN
        v_summary := v_summary || ' (' || array_to_string(v_changed[1:5], ', ')
                  || CASE WHEN cardinality(v_changed) > 5
                          THEN ', +' || (cardinality(v_changed) - 5) || ' more' ELSE '' END
                  || ')';
    END IF;
    v_summary := left(v_summary, 300);

    v_creator := (v_row ->> 'created_by')::uuid;
    IF v_creator IS NOT NULL AND NOT EXISTS (SELECT 1 FROM users WHERE id = v_creator) THEN
        v_creator := NULL;
    END IF;

    INSERT INTO activity_log (household_id, actor_user_id, actor_agent_token_id,
                              entity_type, entity_id, parent_type, parent_id,
                              entity_created_by, action, changed_fields, summary,
                              changes)
    VALUES (v_household, current_actor_user_id(), current_actor_agent_token_id(),
            v_kind, (v_row ->> 'id')::uuid, v_parent_type, v_parent_id,
            v_creator, v_action, v_changed, v_summary,
            v_changes);
    RETURN NULL;
END;
$body$;
$fn$;
END
$$;

-- 072's activity_change_values(): the memberships allowlist is gone, so no
-- `changes` values are recorded for that table again.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'activity_change_values') THEN
        RETURN;
    END IF;

    EXECUTE $fn$
CREATE OR REPLACE FUNCTION activity_change_values(p_table TEXT, p_old JSONB, p_new JSONB, p_changed TEXT[])
RETURNS JSONB
LANGUAGE plpgsql IMMUTABLE
AS $body$
DECLARE
    v_cols TEXT[];
    v_col  TEXT;
    v_out  JSONB := '{}'::jsonb;
    v_n    INT := 0;
BEGIN
    IF p_changed IS NULL OR p_old IS NULL OR p_new IS NULL THEN
        RETURN NULL;
    END IF;
    v_cols := CASE p_table
        WHEN 'bills' THEN
            ARRAY['amount', 'frequency', 'due_day', 'due_date', 'paid_date', 'paid_for_due_date',
                  'source_document_id']
        WHEN 'loans' THEN
            ARRAY['monthly_payment', 'remaining_balance', 'interest_rate', 'term_months', 'due_day', 'term_end']
        WHEN 'maintenance_tasks' THEN
            ARRAY['cost', 'status', 'due_date']
        WHEN 'budget_categories' THEN
            ARRAY['monthly_amount']
        ELSE
            NULL
    END;
    IF v_cols IS NULL THEN
        RETURN NULL;
    END IF;
    FOREACH v_col IN ARRAY v_cols LOOP
        CONTINUE WHEN NOT (v_col = ANY (p_changed));
        v_out := v_out || jsonb_build_object(v_col,
                     jsonb_build_object('from', p_old -> v_col, 'to', p_new -> v_col));
        v_n := v_n + 1;
    END LOOP;
    IF v_n = 0 THEN
        RETURN NULL;
    END IF;
    RETURN v_out;
END;
$body$;
$fn$;
END
$$;

-- 076's role_can_see_entity(): no membership branch, no member_audit resource.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'role_can_see_entity') THEN
        RETURN;
    END IF;

    EXECUTE $fn$
CREATE OR REPLACE FUNCTION role_can_see_entity(p_role TEXT, p_kind TEXT) RETURNS BOOLEAN
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $body$
    SELECT COALESCE(p_role = ANY (
        CASE (CASE p_kind
                  WHEN 'household'            THEN 'family_notes'
                  WHEN 'property'             THEN 'properties'
                  WHEN 'room'                 THEN 'properties'
                  WHEN 'asset'                THEN 'assets'
                  WHEN 'vehicle'              THEN 'vehicles'
                  WHEN 'pet'                  THEN 'pets'
                  WHEN 'vendor'               THEN 'vendors'
                  WHEN 'bill'                 THEN 'bills'
                  WHEN 'loan'                 THEN 'loans'
                  WHEN 'maintenance'          THEN 'maintenance'
                  WHEN 'maintenance_task'     THEN 'maintenance'
                  WHEN 'maintenance_schedule' THEN 'maintenance'
                  WHEN 'calendar'             THEN 'calendar'
                  WHEN 'calendar_event'       THEN 'calendar'
                  WHEN 'note'                 THEN 'notes'
                  WHEN 'file'                 THEN 'files'
                  WHEN 'document'             THEN 'files'
                  WHEN 'document_link'        THEN 'files'
                  WHEN 'secret'               THEN 'secrets'
                  WHEN 'person'               THEN 'people'
                  WHEN 'person_preference'    THEN 'preferences'
                  WHEN 'budget'               THEN 'budget'
                  WHEN 'list'                 THEN 'lists'
                  WHEN 'list_item'            THEN 'lists'
                  WHEN 'recipe'               THEN 'meals'
                  WHEN 'meal_plan'            THEN 'meals'
                  WHEN 'meal_feedback'        THEN 'meals'
                  WHEN 'guide'                THEN 'guides'
                  WHEN 'kb_doc'               THEN 'kb'
              END)
            WHEN 'family_notes' THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']
            WHEN 'properties'   THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter', 'vendor']
            WHEN 'assets'       THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter', 'vendor']
            WHEN 'vehicles'     THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']
            WHEN 'pets'         THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']
            WHEN 'vendors'      THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']
            WHEN 'calendar'     THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']
            WHEN 'maintenance'  THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'vendor']
            WHEN 'bills'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']
            WHEN 'loans'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']
            WHEN 'budget'       THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']
            WHEN 'notes'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']
            WHEN 'files'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']
            WHEN 'secrets'      THEN ARRAY['owner', 'family_manager', 'family_member']
            WHEN 'people'       THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']
            WHEN 'preferences'  THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']
            WHEN 'lists'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']
            WHEN 'meals'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']
            WHEN 'guides'       THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']
            WHEN 'kb'           THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter', 'vendor']
            ELSE ARRAY['owner', 'family_manager', 'family_member']
        END), false)
$body$;
$fn$;
END
$$;

-- ---------------------------------------------------------------------------
-- 4. The vocabulary: 'membership' STAYS a domain member
-- ---------------------------------------------------------------------------

-- This is the one part of 086 that a rollback cannot undo, and the reason is a
-- property of domains rather than a preference: activity_log.entity_type is of
-- type activity_entity_kind, so ALTER DOMAIN ... ADD CONSTRAINT VALIDATES EVERY
-- EXISTING activity_log ROW. Removing 'membership' from the domain therefore
-- FAILS while any row 086 wrote still exists -- measured, on PG16:
--
--   ERROR: column "entity_type" of table "activity_log" contains values that
--          violate the new constraint
--
-- The only ways to make that succeed are to DELETE audit history or to REWRITE
-- it to a kind that did not happen. Both are worse than the inconsistency:
-- these rows are the only evidence that a grant or a demotion ever occurred,
-- and 078 already set the precedent that this schema does not rewrite history
-- to fit a new constraint.
--
-- So 'membership' outlives its writer. The consequence, stated: after this
-- down there is a domain member no trigger writes. That is the mirror image of
-- 'kb_doc' (a policy branch nothing logs), which 078 documented as acceptable,
-- and it is the state a future 086 up returns to normal by re-attaching the
-- writer. A database with NO membership rows could be narrowed by hand; this
-- migration does not guess which kind of database it is running on.

COMMENT ON DOMAIN activity_entity_kind IS
    'What kind of thing an activity_log row is about (#5509, #5518). The 23 entity_kind members plus list_item, meal_feedback, person_preference and membership, which only the audit feed writes. Separated from entity_kind on purpose: entity_kind also governs what a note or file may attach to.';

COMMIT;
