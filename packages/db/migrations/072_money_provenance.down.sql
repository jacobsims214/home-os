-- 072_money_provenance.down.sql (#4941)
--
-- Undoes 072: activity_log.changes and activity_change_values(),
-- bills.source_document_id and files.text_origin/text_sha256 (with the
-- (id, household_id) key the composite FK targeted).
--
-- log_activity() goes back to 055's body (a copy of what 055 up defines), so
-- nothing tries to write a column that no longer exists. Rows already
-- written keep their changes value until the column is dropped with the
-- table's data; nothing else reads it.
--
-- Runs in one transaction.

BEGIN;

-- 1. log_activity(): 055's body, without the #4941 additions.
CREATE OR REPLACE FUNCTION log_activity() RETURNS TRIGGER
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
                              entity_created_by, action, changed_fields, summary)
    VALUES (v_household, current_actor_user_id(), current_actor_agent_token_id(),
            v_kind, (v_row ->> 'id')::uuid, v_parent_type, v_parent_id,
            v_creator, v_action, v_changed, v_summary);
    RETURN NULL;
END;
$$;

-- 2. activity_log.changes (the CHECK goes with the column).
ALTER TABLE activity_log DROP COLUMN IF EXISTS changes;
DROP FUNCTION IF EXISTS activity_change_values(TEXT, JSONB, JSONB, TEXT[]);

-- 3. bills.source_document_id.
DROP INDEX IF EXISTS idx_bills_source_document;
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_source_document_fkey;
ALTER TABLE bills DROP COLUMN IF EXISTS source_document_id;

-- 4. files.text_origin / text_sha256 and the composite key they came with.
ALTER TABLE files DROP CONSTRAINT IF EXISTS files_id_household_id_key;
ALTER TABLE files DROP CONSTRAINT IF EXISTS files_text_origin_check;
ALTER TABLE files DROP COLUMN IF EXISTS text_sha256;
ALTER TABLE files DROP COLUMN IF EXISTS text_origin;

COMMIT;
