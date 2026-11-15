-- 050_authorship.up.sql
--
-- Stage 2: authorship and the activity log (KB decisions/domain-model-v2.md,
-- "Contracts for the Stage 2 migrations" -> 050 authorship; ticket #4481).
--
-- Who did it is answered by the database, from two per-transaction settings
-- the API, MCP server and worker set before they write:
--
--     SELECT set_config('app.user_id',        '<users.id>',        true);
--     SELECT set_config('app.agent_token_id', '<agent_tokens.id>', true); -- only when an agent acts
--     SELECT set_config('app.household_id',   '<households.id>',   true); -- optional fallback
--
-- Use is_local = true (or SET LOCAL) inside the write's transaction: pooled
-- connections are reused, and a session-level setting would leak into the
-- next caller's writes. A setting that is unset, blank, not a UUID, or names
-- a row that doesn't exist counts as "no actor": the write still succeeds and
-- the actor columns are NULL. Nothing here ever makes a write fail.
--
-- What this adds:
--   1. created_by / updated_by (UUID REFERENCES users ON DELETE SET NULL) on
--      every family-owned table (list below). Set by the BEFORE trigger
--      set_authorship():
--        * INSERT: created_by = the value written, else the actor.
--                  updated_by = the value written, else the actor, else
--                  created_by.
--        * UPDATE: updated_by = the actor (NULL when there is none: "last
--                  changed by the system or an unknown caller"), unless the
--                  statement sets updated_by itself. created_by is never
--                  changed by the trigger.
--                  An UPDATE that changes nothing but authorship columns (the
--                  ON DELETE SET NULL cascade when a user is deleted) leaves
--                  updated_by alone, so it is not blamed on whoever deleted
--                  the user.
--   2. notes.author_id is folded into created_by. created_by is backfilled
--      from author_id, and author_id is KEPT and kept in step both ways
--      (writing either one sets the other), because api/internal/note and
--      mcp/internal/note still write author_id. #4491/#4492 move those
--      writes to the actor setting; a later migration then drops author_id.
--      guide_revisions.edited_by gets the same treatment (it is the
--      revision's author).
--   3. activity_log: one row per create, update or delete of a family-owned
--      record, written by the AFTER trigger log_activity(kind). It feeds the
--      change feed (#4505 API, #4498 MCP what-changed) and creator
--      notifications (#4493, migration 052). Each row says:
--        * who:   actor_user_id, and actor_agent_token_id when an agent acted;
--        * what:  entity_type (the entity_kind name for registered kinds,
--                 e.g. 'bill'; otherwise 'calendar', 'calendar_event',
--                 'document_link', 'list_item', 'meal_feedback',
--                 'person_preference') + entity_id; parent_type/parent_id is
--                 the record it sits in or is about (the list of a list item,
--                 the calendar of an event, the asset a note is about);
--                 entity_created_by is the record's creator at the time, so
--                 052 can notify a creator even after the record is deleted;
--        * how:   action create/update/delete, and changed_fields (column
--                 NAMES only) for an update;
--        * when:  created_at (transaction time) and id (strict order; the
--                 feed's cursor);
--        * summary: short text such as `updated bill "Netflix" (amount)`.
--      Values are never copied into the log. The summary uses only a
--      record's label (title / name / display_name, a list item's text, an
--      event's SUMMARY line, a credential's name), never note bodies,
--      document text, URLs or secret material.
--      Not logged, to keep the feed readable:
--        * updates that change only bookkeeping columns (updated_at, the
--          authorship columns, calendars.ctag, calendar_objects.etag);
--        * deletes caused by another delete (a trigger, or an FK cascade): the
--          items of a deleted list, the notes about a deleted asset, the
--          rows of a deleted household. The delete the caller asked for is
--          logged;
--        * anything while the record's household is being deleted;
--        * guide_revisions (the guide's own update is logged).
--
-- Family-owned tables covered (authorship on all; activity on all but
-- guide_revisions):
--   properties, rooms, assets, vehicles, pets, vendors, bills, loans,
--   maintenance_schedules, maintenance_tasks, calendars, calendar_objects,
--   notes, files, secrets, document_links, people, person_preferences,
--   budget_categories, lists, list_items, recipes, meal_plans, meal_feedback,
--   guides, guide_revisions.
-- Not covered: households, users, memberships, invitations (has invited_by),
-- agent_tokens, notifications, outbox_events, secret_keys, integrations
-- (dropped by 040), calendar_changes (CalDAV sync log), file_blobs (1:1 with
-- files), password_reset_tokens.
--
-- 050 does not change entity_kind, entity_household() or
-- rehome_entity_refs(): the next migration that touches them still copies
-- entity_household() from 049 and rehome_entity_refs() from 046.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Actor lookup
-- ---------------------------------------------------------------------------

-- The acting user from app.user_id, or NULL. Never raises.
CREATE FUNCTION current_actor_user_id() RETURNS UUID
LANGUAGE plpgsql STABLE
AS $$
DECLARE
    v_raw TEXT := NULLIF(btrim(current_setting('app.user_id', true)), '');
    v_id  UUID;
BEGIN
    IF v_raw IS NULL THEN
        RETURN NULL;
    END IF;
    BEGIN
        v_id := v_raw::uuid;
    EXCEPTION WHEN invalid_text_representation THEN
        RETURN NULL;
    END;
    -- A deleted (or never existing) user would make the FK fail.
    IF NOT EXISTS (SELECT 1 FROM users WHERE id = v_id) THEN
        RETURN NULL;
    END IF;
    RETURN v_id;
END;
$$;

-- The acting agent token from app.agent_token_id, or NULL. Never raises.
CREATE FUNCTION current_actor_agent_token_id() RETURNS UUID
LANGUAGE plpgsql STABLE
AS $$
DECLARE
    v_raw TEXT := NULLIF(btrim(current_setting('app.agent_token_id', true)), '');
    v_id  UUID;
BEGIN
    IF v_raw IS NULL THEN
        RETURN NULL;
    END IF;
    BEGIN
        v_id := v_raw::uuid;
    EXCEPTION WHEN invalid_text_representation THEN
        RETURN NULL;
    END;
    IF NOT EXISTS (SELECT 1 FROM agent_tokens WHERE id = v_id) THEN
        RETURN NULL;
    END IF;
    RETURN v_id;
END;
$$;

-- ---------------------------------------------------------------------------
-- 2. created_by / updated_by columns
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'properties', 'rooms', 'assets', 'vehicles', 'pets', 'vendors',
        'bills', 'loans', 'maintenance_schedules', 'maintenance_tasks',
        'calendars', 'calendar_objects', 'notes', 'files', 'secrets',
        'document_links', 'people', 'person_preferences', 'budget_categories',
        'lists', 'list_items', 'recipes', 'meal_plans', 'meal_feedback',
        'guides', 'guide_revisions'
    ] LOOP
        EXECUTE format(
            'ALTER TABLE %I
                 ADD COLUMN created_by UUID REFERENCES users(id) ON DELETE SET NULL,
                 ADD COLUMN updated_by UUID REFERENCES users(id) ON DELETE SET NULL',
            t);
    END LOOP;
END
$$;

-- Backfill from the only authorship data that exists today. Runs before the
-- triggers are attached, so it writes no activity.
UPDATE notes SET created_by = author_id, updated_by = author_id
 WHERE author_id IS NOT NULL;
UPDATE guide_revisions SET created_by = edited_by, updated_by = edited_by
 WHERE edited_by IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 3. activity_log
-- ---------------------------------------------------------------------------

CREATE TABLE activity_log (
    -- Strictly increasing: the change feed pages with "id > cursor".
    id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    household_id         UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    actor_user_id        UUID REFERENCES users(id) ON DELETE SET NULL,
    actor_agent_token_id UUID REFERENCES agent_tokens(id) ON DELETE SET NULL,
    entity_type          VARCHAR(50) NOT NULL CHECK (btrim(entity_type) <> ''),
    entity_id            UUID NOT NULL,
    parent_type          VARCHAR(50),
    parent_id            UUID,
    entity_created_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    action               VARCHAR(10) NOT NULL CHECK (action IN ('create', 'update', 'delete')),
    changed_fields       TEXT[] NOT NULL DEFAULT '{}',
    summary              TEXT NOT NULL CHECK (char_length(summary) <= 300),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT activity_log_parent_check CHECK ((parent_type IS NULL) = (parent_id IS NULL)),
    CONSTRAINT activity_log_changed_fields_check CHECK (action = 'update' OR changed_fields = '{}')
);

-- The family feed, newest first.
CREATE INDEX idx_activity_log_household_created ON activity_log (household_id, created_at DESC, id DESC);
-- History of one record, and of the records in one parent.
CREATE INDEX idx_activity_log_entity ON activity_log (entity_type, entity_id, id DESC);
CREATE INDEX idx_activity_log_parent ON activity_log (parent_type, parent_id, id DESC)
    WHERE parent_id IS NOT NULL;
-- "What did I / this agent change", and the users/agent_tokens ON DELETE SET NULL.
CREATE INDEX idx_activity_log_actor_user ON activity_log (actor_user_id, id DESC)
    WHERE actor_user_id IS NOT NULL;
CREATE INDEX idx_activity_log_actor_agent ON activity_log (actor_agent_token_id, id DESC)
    WHERE actor_agent_token_id IS NOT NULL;
-- Creator notifications (052): changes to records a user created.
CREATE INDEX idx_activity_log_entity_created_by ON activity_log (entity_created_by, id DESC)
    WHERE entity_created_by IS NOT NULL;

-- Grant to the app role when it exists (see 032_loans, 034_agent_tokens).
-- The log is append-only for the app: rows change only through the FK
-- cascades above.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app') THEN
        GRANT SELECT, INSERT ON TABLE activity_log TO app;
    END IF;
END
$$;

-- ---------------------------------------------------------------------------
-- 4. Trigger functions
-- ---------------------------------------------------------------------------

-- True when a row being deleted points at a container that no longer
-- exists: the row is going by FK cascade (a deleted list's items, a deleted
-- property's assets and rooms, a deleted asset's maintenance). Every such
-- column is a real FK, so a missing target can only mean it was deleted in
-- this statement. Only columns on family-owned tables are listed.
CREATE FUNCTION activity_container_gone(p_row JSONB) RETURNS BOOLEAN
LANGUAGE plpgsql STABLE
AS $$
BEGIN
    IF p_row ->> 'property_id' IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM properties WHERE id = (p_row ->> 'property_id')::uuid) THEN
        RETURN TRUE;
    END IF;
    IF p_row ->> 'asset_id' IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM assets WHERE id = (p_row ->> 'asset_id')::uuid) THEN
        RETURN TRUE;
    END IF;
    IF p_row ->> 'vehicle_id' IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM vehicles WHERE id = (p_row ->> 'vehicle_id')::uuid) THEN
        RETURN TRUE;
    END IF;
    IF p_row ->> 'list_id' IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM lists WHERE id = (p_row ->> 'list_id')::uuid) THEN
        RETURN TRUE;
    END IF;
    IF p_row ->> 'calendar_id' IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM calendars WHERE id = (p_row ->> 'calendar_id')::uuid) THEN
        RETURN TRUE;
    END IF;
    IF p_row ->> 'meal_plan_id' IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM meal_plans WHERE id = (p_row ->> 'meal_plan_id')::uuid) THEN
        RETURN TRUE;
    END IF;
    IF p_row ->> 'person_id' IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM people WHERE id = (p_row ->> 'person_id')::uuid) THEN
        RETURN TRUE;
    END IF;
    RETURN FALSE;
END;
$$;

-- BEFORE INSERT OR UPDATE on every family-owned table.
-- Nested IFs on TG_TABLE_NAME: PL/pgSQL does not short-circuit AND, and
-- notes.author_id / guide_revisions.edited_by exist only on those tables.
CREATE FUNCTION set_authorship() RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_actor  UUID := current_actor_user_id();
    v_ignore TEXT[] := ARRAY['created_by', 'updated_by', 'author_id', 'edited_by'];
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF TG_TABLE_NAME = 'notes' THEN
            NEW.created_by := COALESCE(NEW.created_by, NEW.author_id, v_actor);
            NEW.author_id  := COALESCE(NEW.author_id, NEW.created_by);
        ELSIF TG_TABLE_NAME = 'guide_revisions' THEN
            NEW.created_by := COALESCE(NEW.created_by, NEW.edited_by, v_actor);
            NEW.edited_by  := COALESCE(NEW.edited_by, NEW.created_by);
        ELSE
            NEW.created_by := COALESCE(NEW.created_by, v_actor);
        END IF;
        NEW.updated_by := COALESCE(NEW.updated_by, v_actor, NEW.created_by);
        RETURN NEW;
    END IF;

    -- UPDATE: keep the legacy author column and created_by in step.
    IF TG_TABLE_NAME = 'notes' THEN
        IF NEW.author_id IS DISTINCT FROM OLD.author_id
           AND NEW.created_by IS NOT DISTINCT FROM OLD.created_by THEN
            NEW.created_by := NEW.author_id;
        ELSIF NEW.created_by IS DISTINCT FROM OLD.created_by
           AND NEW.author_id IS NOT DISTINCT FROM OLD.author_id THEN
            NEW.author_id := NEW.created_by;
        END IF;
    ELSIF TG_TABLE_NAME = 'guide_revisions' THEN
        IF NEW.edited_by IS DISTINCT FROM OLD.edited_by
           AND NEW.created_by IS NOT DISTINCT FROM OLD.created_by THEN
            NEW.created_by := NEW.edited_by;
        ELSIF NEW.created_by IS DISTINCT FROM OLD.created_by
           AND NEW.edited_by IS NOT DISTINCT FROM OLD.edited_by THEN
            NEW.edited_by := NEW.created_by;
        END IF;
    END IF;

    -- Only authorship columns changed (e.g. users ON DELETE SET NULL), or
    -- nothing changed: not an edit by anyone.
    IF (to_jsonb(NEW) - v_ignore) = (to_jsonb(OLD) - v_ignore) THEN
        RETURN NEW;
    END IF;

    IF NEW.updated_by IS NOT DISTINCT FROM OLD.updated_by THEN
        NEW.updated_by := v_actor;
    END IF;
    RETURN NEW;
END;
$$;

-- AFTER INSERT OR UPDATE OR DELETE on every family-owned table except
-- guide_revisions. TG_ARGV[0] is the entity type written to the log.
CREATE FUNCTION log_activity() RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_kind        TEXT := TG_ARGV[0];
    v_row         JSONB;
    v_old         JSONB;
    v_action      TEXT;
    v_changed     TEXT[] := '{}';
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
            v_label := substring(v_row ->> 'ical_data' FROM 'SUMMARY[^:\r\n]*:([^\r\n]*)');
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

    -- The creator, only while that user still exists (it may be mid-delete).
    v_creator := (v_row ->> 'created_by')::uuid;
    IF v_creator IS NOT NULL AND NOT EXISTS (SELECT 1 FROM users WHERE id = v_creator) THEN
        v_creator := NULL;
    END IF;

    INSERT INTO activity_log (household_id, actor_user_id, actor_agent_token_id,
                              entity_type, entity_id, parent_type, parent_id,
                              entity_created_by, action, changed_fields, summary)
    VALUES (v_household, current_actor_user_id(), current_actor_agent_token_id(),
            v_kind, (v_row ->> 'id')::uuid, v_parent_type, v_parent_id,
            v_creator, v_action, v_changed, v_summary);
    RETURN NULL;
END;
$$;

-- ---------------------------------------------------------------------------
-- 5. Triggers. <table>_authorship sorts before the tables' other BEFORE
-- triggers (they fire alphabetically); none of those depend on it.
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN SELECT * FROM (VALUES
        ('properties',            'property'),
        ('rooms',                 'room'),
        ('assets',                'asset'),
        ('vehicles',              'vehicle'),
        ('pets',                  'pet'),
        ('vendors',               'vendor'),
        ('bills',                 'bill'),
        ('loans',                 'loan'),
        ('maintenance_schedules', 'maintenance_schedule'),
        ('maintenance_tasks',     'maintenance_task'),
        ('calendars',             'calendar'),
        ('calendar_objects',      'calendar_event'),
        ('notes',                 'note'),
        ('files',                 'file'),
        ('secrets',               'secret'),
        ('document_links',        'document_link'),
        ('people',                'person'),
        ('person_preferences',    'person_preference'),
        ('budget_categories',     'budget'),
        ('lists',                 'list'),
        ('list_items',            'list_item'),
        ('recipes',               'recipe'),
        ('meal_plans',            'meal_plan'),
        ('meal_feedback',         'meal_feedback'),
        ('guides',                'guide'),
        ('guide_revisions',       NULL)
    ) AS v(tbl, kind) LOOP
        EXECUTE format(
            'CREATE TRIGGER %I BEFORE INSERT OR UPDATE ON %I
                 FOR EACH ROW EXECUTE FUNCTION set_authorship()',
            r.tbl || '_authorship', r.tbl);
        IF r.kind IS NOT NULL THEN
            EXECUTE format(
                'CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I
                     FOR EACH ROW EXECUTE FUNCTION log_activity(%L)',
                r.tbl || '_activity', r.tbl, r.kind);
        END IF;
    END LOOP;
END
$$;

COMMIT;
