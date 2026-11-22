-- 086_membership_audit.up.sql (#5518)
--
-- Finding F24 in architecture/schema-review-2026-11.md.
--
-- THE DEFECT, CONFIRMED ON A LIVE PG16 CLUSTER AT 084. `memberships` carries
-- two AFTER triggers -- membership_ensure_person (INSERT) and
-- membership_unlink_person (DELETE), both from 041 -- and NO log_activity
-- trigger:
--
--   SELECT tgname FROM pg_trigger
--    WHERE tgrelid = 'public.memberships'::regclass AND NOT tgisinternal;
--     memberships_ensure_person     AFTER INSERT ... membership_ensure_person()
--     memberships_unlink_person     AFTER DELETE ... membership_unlink_person()
--
-- No Go handler compensates. The only hand-written INSERT INTO activity_log in
-- the product is apps/mcp/internal/credential/reveal.go (a credential reveal,
-- correctly not a trigger), and no household/invite/admin handler writes an
-- activity row. So GRANTING a person access to a family, CHANGING their role
-- from viewer to owner, or REMOVING them writes nothing at all:
--
--   INSERT INTO memberships ...   -> 1 row, entity_type 'person' (the 041
--                                    side effect, a person being created)
--   UPDATE memberships SET role   -> 0 rows
--   DELETE FROM memberships ...   -> 1 row, entity_type 'person' (the 041
--                                    unlink side effect)
--
-- The role change -- the single most security-significant write in a product
-- where a role decides whether a person can see money, credentials and
-- documents -- was the ONE operation that left no trace anywhere. The row
-- itself could not answer it either: memberships had no updated_at and no
-- updated_by (F18), so a promoted membership and an untouched one looked
-- identical.
--
-- WHY A TRIGGER AND NOT HANDLER CODE. Both write paths exist -- REST
-- (apps/api/internal/household/repo.go: AddMember, UpdateMemberRole,
-- RemoveMember) and MCP -- and a handler-code audit site is a site that a
-- third path, a backfill or a psql session silently bypasses. Every other
-- audited table in this schema is audited by log_activity() at the table, so
-- REST and MCP produce identical rows BY CONSTRUCTION rather than by two
-- implementations agreeing. See architecture/write-paths-and-side-effects.md.
--
-- WHAT THIS DOES
--   1. activity_entity_kind gains 'membership' (the F1 domain, 078). 078
--      deliberately left it out and named this ticket as the reason.
--   2. memberships gains updated_at / updated_by, with the same
--      set_authorship()+set_updated_at() treatment as every other mutable
--      table. Set-authoritative: the trigger fills them, so a writer that
--      forgets cannot make the row lie.
--   3. log_activity('membership') is attached AFTER INSERT OR UPDATE OR
--      DELETE, via 050's (table, kind) block, so the writer is attached the
--      same way 25 other tables are.
--   4. log_activity() gains two #5518 additions: a label for a membership
--      (the person's name and role, read from users) and a summary suffix that
--      spells out "role viewer -> owner". activity_change_values() gains a
--      memberships allowlist so `changes` carries {"role":{"from":..,"to":..}}
--      for an API client. Both are additions to an existing function, in the
--      same shape as 072's #4941 addition; nothing else in the body changes.
--   5. role_can_see_entity() gains a members branch (ticket F24 step 3).
--
-- THE VISIBILITY DECISION (F24 step 3). activity_log has NO per-row visibility
-- mechanism: the feed is filtered only by household_id, and per-role hiding is
-- decided at read time by role_can_see_entity(role, entity_type) through
-- auth.CanSeeEntity (apps/api/internal/auth/roles.go) and the activity
-- handler's HiddenEntityKinds. Adding a visibility COLUMN would need every
-- reader to honour it -- a new invariant across the API, MCP, search and every
-- future reader -- and a column a reader can forget is a leak. So the decision
-- is made in the mechanism that already exists and that every reader already
-- asks: 'membership' maps to a NEW resource, member_audit, granted to
-- rolesManagers (owner, family_manager) ONLY.
--
-- Consequence, stated plainly: a demotion is NOT visible to the person
-- demoted, nor to family_member/viewer/housesitter/vendor. A person CAN still
-- discover their own demotion the moment it bites -- their permissions change
-- -- they simply do not read it in the shared feed. This is option (a) from
-- the finding, and it is the safe direction: the audit trail exists for the
-- people accountable for access, and a role change is management data.
--
-- The cost, also stated: this is a NEW resource, not an existing one. It is
-- 'member_audit', not 'members', because members read is rolesSitter -- a
-- housesitter may see who is in the family, which is right for a members list
-- and wrong for the record of who demoted whom. Reusing ResMembers would have
-- silently made the audit housesitter-readable. members stays as it is.
--
-- WHY THE PEOPLE SIDE EFFECT IS NOT DOUBLE-LOGGED (F24 step 4, criterion 4).
-- 041's triggers make a membership write touch people. Measured on PG16 at
-- 084, what that produces is:
--   INSERT membership -> people INSERT (a new person)  -> 1 'person' create row
--   UPDATE membership -> people UNTOUCHED              -> 0 rows (before this)
--   DELETE membership -> people UPDATE (user_id=NULL)  -> 1 'person' update row
-- So a delete now writes TWO rows: the membership delete this migration adds
-- and the pre-existing person update. That is not double-logging of one event;
-- it is two different facts (the membership ended; the person record outlived
-- the login), and the person row is the pre-existing, correct behaviour of
-- 041 that this migration neither causes nor changes. Suppressing it would
-- require log_activity() to special-case memberships in its DELETE branch --
-- MORE coupling to the people triggers, not less. Left alone, and asserted by
-- count in the test so it stays two, not three.
--
-- updated_at / updated_by vs auth.uid() (F24 step 4). Postgres has no
-- auth.uid(); this schema uses current_actor_user_id() (050), which reads the
-- app.user_id GUC the API and MCP both set. That is what fills
-- activity_log.actor_user_id -- so "who did it" is already answered there.
-- The columns are added anyway because the ROW must be able to answer "who
-- last changed this, and when" WITHOUT reading the log: F18's complaint is
-- precisely that the row records nothing, and a memberships row is what an
-- authorization decision reads. updated_by is filled by set_authorship() --
-- the same mechanism as the other 25 tables, not a new one -- and updated_at
-- by set_updated_at() (077).
--
-- No BACKFILL of synthetic history. Existing memberships get updated_at =
-- created_at and updated_by NULL (see below), and NO activity_log row is
-- invented for them. There is no evidence of who granted what, and a
-- fabricated "created membership" row dated today would state something that
-- did not happen in the one table where a wrong write is permanent. The
-- audit trail starts at the moment it can tell the truth. Say so in the
-- completion comment; do not add a baseline entry later without evidence
-- (an invitation row, a deploy log) to date it from.
--
-- LOCKING. memberships is small (one row per person per family). The
-- ADD COLUMN updated_at with a DEFAULT NOW() would rewrite every row and set
-- updated_at to the migration time, which is a lie; instead the column is
-- added nullable, backfilled from created_at, then SET NOT NULL -- a
-- catalogue change plus one small UPDATE. set_updated_at() makes it
-- self-maintaining from then on.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. The audit feed's vocabulary: 'membership'
-- ---------------------------------------------------------------------------

-- 078's 26 members plus this one. Written out in full, like every earlier
-- domain re-declaration (041, 070, 078): a domain constraint is replaced, not
-- extended, so a copy that omits a member silently deletes it.
ALTER DOMAIN activity_entity_kind DROP CONSTRAINT activity_entity_kind_check;
ALTER DOMAIN activity_entity_kind ADD CONSTRAINT activity_entity_kind_check CHECK (VALUE IN (
        'household',
        'property',
        'room',
        'asset',
        'vehicle',
        'pet',
        'vendor',
        'bill',
        'loan',
        'maintenance_task',
        'maintenance_schedule',
        'file',
        'note',
        'secret',
        'person',
        'budget',
        'list',
        'recipe',
        'meal_plan',
        'guide',
        'calendar',
        'calendar_event',
        'document_link',
        'list_item',
        'meal_feedback',
        'person_preference',
        'membership'
    ));

COMMENT ON DOMAIN activity_entity_kind IS
    'What kind of thing an activity_log row is about (#5509, #5518). The 23 entity_kind members plus list_item, meal_feedback, person_preference and membership, which only the audit feed writes. Separate from entity_kind on purpose: entity_kind also governs what a note or file may attach to, and a membership is not a note target.';

-- ---------------------------------------------------------------------------
-- 2. memberships: the row records its own last change
-- ---------------------------------------------------------------------------

ALTER TABLE memberships ADD COLUMN updated_at TIMESTAMPTZ;
ALTER TABLE memberships ADD COLUMN updated_by UUID REFERENCES users(id) ON DELETE SET NULL;

-- Existing rows: the only honest timestamp is when the row was created. Nobody
-- knows when it was last touched -- that is the gap -- so updated_at starts
-- equal to created_at and updated_by stays NULL ("unknown"), not a guess.
UPDATE memberships SET updated_at = created_at WHERE updated_at IS NULL;
ALTER TABLE memberships ALTER COLUMN updated_at SET NOT NULL;
ALTER TABLE memberships ALTER COLUMN updated_at SET DEFAULT NOW();

COMMENT ON COLUMN memberships.updated_at IS
    'When this membership last changed. Filled by set_updated_at() (077), not by writers. Backfilled to created_at: the real time of the last change was never recorded. #5518';
COMMENT ON COLUMN memberships.updated_by IS
    'Who last changed this membership, from current_actor_user_id() via set_authorship(). NULL for rows that existed before this column: unknown, not invented. #5518';

-- The 077 mechanism, on the same function as the other 25 tables. Name sorts
-- after memberships_ensure_person/unlink_person so it runs last among the
-- BEFORE triggers; see 077's header on why that ordering is load-bearing.
CREATE TRIGGER memberships_set_updated_at BEFORE UPDATE ON memberships
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- set_authorship() is already attached to 26 tables and fills updated_by from
-- current_actor_user_id(), keeping created_by/updated_by out of changed_fields.
-- It must NOT be attached to memberships: memberships has no created_by column
-- and set_authorship()'s INSERT branch writes one unconditionally. updated_by
-- is therefore filled here, by a memberships-specific trigger that does
-- exactly one thing -- the same "one small function, table-specific" shape as
-- 041's own membership_ensure_person().
CREATE OR REPLACE FUNCTION membership_set_actor() RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_actor UUID := current_actor_user_id();
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.updated_by := COALESCE(NEW.updated_by, v_actor);
        RETURN NEW;
    END IF;
    -- Only actorship changed (e.g. users ON DELETE SET NULL), or nothing
    -- changed: not an edit by anyone. Mirrors set_authorship() (050).
    IF (to_jsonb(NEW) - ARRAY['updated_at', 'updated_by'])
       = (to_jsonb(OLD) - ARRAY['updated_at', 'updated_by']) THEN
        RETURN NEW;
    END IF;
    IF NEW.updated_by IS NOT DISTINCT FROM OLD.updated_by THEN
        NEW.updated_by := v_actor;
    END IF;
    RETURN NEW;
END;
$$;

COMMENT ON FUNCTION membership_set_actor() IS
    'BEFORE INSERT OR UPDATE on memberships: fills updated_by from the acting user. memberships has no created_by column, so 050''s set_authorship() cannot be attached to it. #5518';

-- 'm' < 's': this runs BEFORE memberships_set_updated_at, so updated_at is
-- still the old value when the no-op comparison above runs. See 077's header.
CREATE TRIGGER memberships_set_actor BEFORE INSERT OR UPDATE ON memberships
    FOR EACH ROW EXECUTE FUNCTION membership_set_actor();

-- ---------------------------------------------------------------------------
-- 3. log_activity('membership')
-- ---------------------------------------------------------------------------

-- Attached from 050's (table, kind) VALUES block so the writer is declared the
-- same way as the other 25. The test that reads that block
-- (TestActivityEntityKind078_EveryMigrationArgumentIsAMember) requires the
-- kind here to be an activity_entity_kind member, which section 1 made it.
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN SELECT * FROM (VALUES
        ('memberships', 'membership')
    ) AS v(tbl, kind) LOOP
        EXECUTE format(
            'CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I
                 FOR EACH ROW EXECUTE FUNCTION log_activity(%L)',
            r.tbl || '_activity', r.tbl, r.kind);
    END LOOP;
END
$$;

-- ---------------------------------------------------------------------------
-- 4. The writer: label, before/after role, and the policy branch
-- ---------------------------------------------------------------------------

-- 072's body plus the two #5518 additions marked below. Everything else --
-- summary, parent, actor, creator, the delete-depth rule -- is byte-for-byte
-- 072's, so the other 25 tables' rows read exactly as they did.
CREATE OR REPLACE FUNCTION log_activity() RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
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
        -- A delete caused by another delete is covered by the delete that
        -- caused it: one made by a trigger (rehome, the 027 maintenance
        -- sync) runs at trigger depth > 1; an FK cascade runs at depth 1
        -- (Postgres fires it from the outer statement's trigger queue), so
        -- it is recognised by its container being gone.
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
        -- #4941: the values, for the allowlisted money/date columns only.
        v_changes := activity_change_values(TG_TABLE_NAME, v_old, v_row, v_changed);
    END IF;

    -- Household: the row's own, else (calendar_objects) its calendar's, else
    -- the app.household_id setting.
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
    -- Unknown household, or the household is being deleted (its rows go by
    -- cascade): nothing to log, and an insert would fail the FK.
    IF v_household IS NULL OR NOT EXISTS (SELECT 1 FROM households WHERE id = v_household) THEN
        RETURN NULL;
    END IF;

    -- Parent: the record this one sits in, else the record it is about.
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

    -- Label: a short human name for the record. Only label columns are
    -- read, so no body, document text, URL or secret value reaches the log.
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
        -- 086: a membership is about a PERSON and a ROLE, and neither is a
        -- column on the row: the name has to be read from users. Only the
        -- name and the role are read -- no email, no hash, no token.
        WHEN 'memberships' THEN
            v_label := concat_ws(' as ', (SELECT COALESCE(NULLIF(btrim(u.name), ''), u.email)
                                            FROM users u WHERE u.id = (v_row ->> 'user_id')::uuid),
                                          v_row ->> 'role');
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
    -- 086: a role change names BOTH values where a person reads it. The
    -- machine-readable copy is in `changes` (activity_change_values); this is
    -- the same fact in the one line the feed shows.
    IF TG_TABLE_NAME = 'memberships' AND v_action = 'update'
       AND 'role' = ANY (v_changed) THEN
        v_summary := v_summary || ' -- role ' || COALESCE(v_old ->> 'role', '?')
                  || ' -> ' || COALESCE(v_row ->> 'role', '?');
    END IF;
    v_summary := left(v_summary, 300);

    -- The creator, only while that user still exists (it may be mid-delete).
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
$$;

-- 072's function plus the memberships allowlist (marked #5518). Only `role`
-- (an enum) and `vendor_id` (a reference) are kept, so 050's invariant -- the
-- log never holds a record's contents -- still holds here.
CREATE OR REPLACE FUNCTION activity_change_values(p_table TEXT, p_old JSONB, p_new JSONB, p_changed TEXT[])
RETURNS JSONB
LANGUAGE plpgsql IMMUTABLE
AS $$
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
            -- source_document_id is a reference (a UUID), never a body: it
            -- says which document the bill came from, which is the thing a
            -- correction has to be traceable to.
            ARRAY['amount', 'frequency', 'due_day', 'due_date', 'paid_date', 'paid_for_due_date',
                  'source_document_id']
        WHEN 'loans' THEN
            ARRAY['monthly_payment', 'remaining_balance', 'interest_rate', 'term_months', 'due_day', 'term_end']
        WHEN 'maintenance_tasks' THEN
            ARRAY['cost', 'status', 'due_date']
        WHEN 'budget_categories' THEN
            ARRAY['monthly_amount']
        -- 086. A role is an allowlisted enum, and vendor_id a reference: both
        -- are safe to keep and are the whole point of auditing a membership.
        -- A role change must record BOTH values, so `role` is here even
        -- though "which field changed" is already in changed_fields.
        WHEN 'memberships' THEN
            ARRAY['role', 'vendor_id']
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
$$;

-- 076's body plus the membership branch (marked #086). A membership is
-- management data: the record of who granted, demoted or removed whom. Read by
-- owner and family_manager only. NOT the members resource -- a housesitter may
-- list the family's members (members.read is rolesSitter) and must not read
-- the audit of how they got their roles.
CREATE OR REPLACE FUNCTION role_can_see_entity(p_role TEXT, p_kind TEXT) RETURNS BOOLEAN
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
    SELECT COALESCE(p_role = ANY (
        CASE (CASE p_kind                              -- entityResources
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
                  -- 076
                  WHEN 'kb_doc'               THEN 'kb'
                  -- 086
                  WHEN 'membership'           THEN 'member_audit'
              END)
            -- policy[resource][ActRead]
            WHEN 'family_notes' THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'properties'   THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter', 'vendor'] -- rolesEveryone
            WHEN 'assets'       THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter', 'vendor'] -- rolesEveryone
            WHEN 'vehicles'     THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'pets'         THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']   -- rolesSitter
            WHEN 'vendors'      THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']   -- rolesSitter
            WHEN 'calendar'     THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']   -- rolesSitter
            WHEN 'maintenance'  THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'vendor']        -- rolesFabric
            WHEN 'bills'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'loans'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'budget'       THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'notes'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']   -- rolesSitter
            WHEN 'files'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'secrets'      THEN ARRAY['owner', 'family_manager', 'family_member']                            -- rolesFamily
            WHEN 'people'       THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'preferences'  THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'lists'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']   -- rolesSitter
            WHEN 'meals'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']   -- rolesSitter
            WHEN 'guides'       THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']   -- rolesSitter
            -- 076: the KB is everyone's, a housesitter's included.
            WHEN 'kb'           THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter', 'vendor'] -- rolesEveryone
            -- 086: memberships.rolesManagers. The membership audit is a
            -- management record: who granted, demoted or removed whom. See
            -- the header for why it is NOT family-readable.
            WHEN 'member_audit'  THEN ARRAY['owner', 'family_manager'] -- rolesManagers
            -- Unknown kind: family roles only (fails closed).
            ELSE ARRAY['owner', 'family_manager', 'family_member']
        END), false)
$$;

COMMIT;
