-- 055_activity_event_titles.up.sql (#4694)
--
-- Name calendar events in activity_log summaries.
--
-- 050's log_activity() labels a calendar_objects row from an iCal SUMMARY
-- line in ical_data. The api, mcp and calendar app store ical_data as JSON
-- ({"title", "start", "all_day", ...}; any raw VCALENDAR goes in ical_raw),
-- so most event rows in the feed read `deleted calendar event` with no name.
-- 054's notify_event_when() already works around this for notifications; this
-- fixes the summary at the source.
--
-- What this does:
--   1. calendar_event_label(ical_data, ical_raw): the event's title, or NULL.
--      JSON ical_data -> its "title"; iCal ical_data -> the VEVENT's SUMMARY;
--      if neither gives a title, the VEVENT SUMMARY in ical_raw. Whitespace
--      is collapsed and the result cut to 80 characters, as 050 does for
--      every label. Names only: never the description, location or dates.
--      Never raises (bad JSON -> falls through to ical_raw, then NULL).
--   2. log_activity() is replaced with 050's body, changed in ONE line: the
--      calendar_objects label now comes from calendar_event_label(). 050 is
--      the only migration that defines log_activity() (051-057 don't touch
--      it), so this body is 050's verbatim apart from that line.
--   3. Backfill: existing calendar_event rows whose summary has no name get
--      the event's CURRENT title, where the event still exists. Rows of
--      deleted events keep their nameless summary (the title is gone).
--      Only the summary changes; no trigger fires on activity_log UPDATE.
--
-- 054's notify_entity_creator() sees the quoted name in the summary and tags
-- the notice 'label', so notify_event_when() only appends the date; the
-- notification text is unchanged ("Amber deleted event "X" on Sat Nov 28").
--
-- entity_kind, entity_household() and rehome_entity_refs() are not changed.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Event title
-- ---------------------------------------------------------------------------

CREATE FUNCTION calendar_event_label(p_ical_data TEXT, p_ical_raw TEXT) RETURNS TEXT
LANGUAGE plpgsql IMMUTABLE
AS $$
DECLARE
    v_label TEXT;
    v_ical  TEXT;
BEGIN
    IF left(ltrim(COALESCE(p_ical_data, '')), 1) = '{' THEN
        -- The api/mcp/calendar shape: {"title", ...}.
        BEGIN
            v_label := p_ical_data::jsonb ->> 'title';
        EXCEPTION WHEN OTHERS THEN
            v_label := NULL;
        END;
    ELSIF position('BEGIN:VEVENT' IN COALESCE(p_ical_data, '')) > 0 THEN
        -- iCal: the VEVENT's SUMMARY, not a VTIMEZONE's or VALARM's.
        v_ical := substring(p_ical_data FROM position('BEGIN:VEVENT' IN p_ical_data));
        v_label := substring(v_ical FROM '\nSUMMARY[^:\r\n]*:([^\r\n]*)');
    END IF;

    IF NULLIF(btrim(v_label), '') IS NULL
       AND position('BEGIN:VEVENT' IN COALESCE(p_ical_raw, '')) > 0 THEN
        v_ical := substring(p_ical_raw FROM position('BEGIN:VEVENT' IN p_ical_raw));
        v_label := substring(v_ical FROM '\nSUMMARY[^:\r\n]*:([^\r\n]*)');
    END IF;

    RETURN NULLIF(left(regexp_replace(btrim(v_label), '\s+', ' ', 'g'), 80), '');
END;
$$;

-- ---------------------------------------------------------------------------
-- 2. log_activity(): 050's body; only the calendar_objects label differs.
-- ---------------------------------------------------------------------------

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
-- 3. Backfill names into existing nameless event rows.
-- "<verb> calendar event[ (fields)]" -> "<verb> calendar event "<title>"[ (fields)]".
-- Built with string operations, not regexp_replace, so a backslash or & in a
-- title is copied literally.
-- ---------------------------------------------------------------------------

UPDATE activity_log a
   SET summary = left(split_part(a.summary, ' ', 1) || ' calendar event "' || e.label || '"'
                      || substr(a.summary, length(split_part(a.summary, ' ', 1)) + 16),
                      300)
  FROM (SELECT id, calendar_event_label(ical_data, ical_raw) AS label
          FROM calendar_objects) e
 WHERE a.entity_type = 'calendar_event'
   AND a.entity_id = e.id
   AND e.label IS NOT NULL
   AND a.summary ~ '^(created|updated|deleted) calendar event( \(|$)';

COMMIT;
