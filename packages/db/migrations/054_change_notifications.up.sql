-- 054_change_notifications.up.sql (#4493)
--
-- Tell members when someone else changes what they created. When a member
-- updates or deletes a family record that a different member created, the
-- creator gets a personal in-app notification:
--
--     Amber deleted event "Date night" on Sat Nov 28
--
-- How it works: migration 050 logs every create, update and delete in
-- activity_log, with the actor (app.user_id, plus app.agent_token_id when an
-- agent acts) and a snapshot of the record's creator (entity_created_by), so
-- the creator is known even after a delete. An AFTER INSERT trigger on
-- activity_log (notify_entity_creator) turns the qualifying rows into
-- notifications. Every writer (api, mcp, worker, caldav) gets this without
-- code of its own.
--
-- A notification is written only when all of these hold:
--   * action is update or delete (creates never notify);
--   * the actor is known (actor_user_id NOT NULL). Writes by the system or an
--     unknown caller never notify;
--   * the creator is known (entity_created_by NOT NULL) and is not the actor.
--     An agent acting for its own member counts as that member, so it is a
--     self-edit;
--   * the creator is still a member of the family.
-- Cascades are already kept out of activity_log by 050: deleting a list logs
-- the list, not its items.
--
-- Rows: type 'item_changed', user_id = the creator (personal, so only the
-- creator sees it in the MCP notification tools), channel 'in_app'.
--   * title: "<actor> [(via <agent>)] <verb> <kind> "<label>"", plus
--     " on <Dy Mon DD>" for calendar events. It is built from the actor's
--     family display name (people.display_name, else users.name), the agent
--     token's name and activity_log.summary, which 050 builds from label
--     columns only. Note bodies, document text and secret values never reach
--     it.
--   * body: "You created this <kind>." and, for updates, the changed column
--     names ("Changed: amount, notes."). Names only, never values.
--   * entity_type/entity_id: what to open.
--       - Updates point at the record itself.
--       - Deletes point at its parent when there is one (an event's calendar,
--         the bill a note was on), because the record itself is gone.
--       - A list item always points at its list.
--     A delete of a top-level record points at the deleted record. For the
--     registered entity kinds, rehome_entity_refs (046) then clears that link,
--     since <table>_rehome_entity_refs fires after <table>_activity. The
--     title still says what was deleted.
--
-- Dedupe: one notification per creator per link target per 10 minutes, for
-- updates and for list items, so an editing burst or someone working through
-- your shopping list sends one notification, not dozens. Deletes of other
-- records always notify. There is no dedupe_key column (#4601 may add one);
-- the window is checked against (user_id, entity_type, entity_id, created_at),
-- served by the partial index below.
--
-- Event names and dates: an event's date sits in calendar_objects.ical_data,
-- which is gone by the time a delete reaches activity_log. ical_data is
-- usually JSON ({"title", "start", "all_day", ...}, written by the api, mcp
-- and calendar app; ical_raw holds any raw VCALENDAR), so 050's iCal SUMMARY
-- label is often missing too. notify_entity_creator leaves the new
-- notification's id in the transaction-local setting home_os.creator_notice.
-- calendar_objects_notify_when is an AFTER row trigger that sorts after
-- calendar_objects_activity. It reads OLD or NEW and appends the event's
-- title when the summary had none, then " on Sat Nov 28". The title and start
-- come from the JSON, else from the VEVENT's SUMMARY/DTSTART in ical_data or
-- ical_raw. A timed start is shown in the family's timezone; an all-day
-- start keeps its date. If nothing can be parsed, the title is left as is.
--
-- Notifications never make a write fail: if a name lookup finds nothing, the
-- text says "Someone". The date step swallows its own errors.
--
-- Runs in one transaction.

BEGIN;

-- Dedupe lookups (and "my change notifications").
CREATE INDEX idx_notifications_item_changed
    ON notifications (user_id, entity_type, entity_id, created_at DESC)
    WHERE type = 'item_changed';

CREATE OR REPLACE FUNCTION notify_entity_creator() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    v_creator   UUID := NEW.entity_created_by;
    v_actor     TEXT;
    v_agent     TEXT;
    v_kind      TEXT;
    v_core      TEXT;
    v_suffix    TEXT;
    v_title     TEXT;
    v_body      TEXT;
    v_link_type TEXT;
    v_link_id   UUID;
    v_id        UUID;
BEGIN
    IF NEW.action NOT IN ('update', 'delete')
       OR NEW.actor_user_id IS NULL
       OR v_creator IS NULL
       OR NEW.actor_user_id = v_creator THEN
        RETURN NULL;
    END IF;
    -- Only members of the family see its notifications.
    IF NOT EXISTS (SELECT 1 FROM memberships
                    WHERE household_id = NEW.household_id AND user_id = v_creator) THEN
        RETURN NULL;
    END IF;

    -- What the notification opens: see the header.
    IF NEW.parent_id IS NOT NULL
       AND (NEW.entity_type = 'list_item' OR NEW.action = 'delete') THEN
        v_link_type := NEW.parent_type;
        v_link_id := NEW.parent_id;
    ELSE
        v_link_type := NEW.entity_type;
        v_link_id := NEW.entity_id;
    END IF;

    IF (NEW.action = 'update' OR NEW.entity_type = 'list_item')
       AND EXISTS (SELECT 1 FROM notifications
                    WHERE user_id = v_creator
                      AND type = 'item_changed'
                      AND entity_type = v_link_type
                      AND entity_id = v_link_id
                      AND created_at > now() - interval '10 minutes') THEN
        RETURN NULL;
    END IF;

    -- Who: the actor's name in this family, else their account name.
    SELECT NULLIF(btrim(display_name), '') INTO v_actor
      FROM people
     WHERE household_id = NEW.household_id AND user_id = NEW.actor_user_id
     ORDER BY created_at
     LIMIT 1;
    IF v_actor IS NULL THEN
        SELECT NULLIF(btrim(name), '') INTO v_actor FROM users WHERE id = NEW.actor_user_id;
    END IF;
    v_actor := left(COALESCE(v_actor, 'Someone'), 80);
    IF NEW.actor_agent_token_id IS NOT NULL THEN
        SELECT NULLIF(btrim(name), '') INTO v_agent
          FROM agent_tokens WHERE id = NEW.actor_agent_token_id;
        v_actor := v_actor || ' (via ' || left(COALESCE(v_agent, 'an agent'), 60) || ')';
    END IF;

    -- What: the summary without 050's " (field, ...)" suffix, which goes in
    -- the body instead. This rebuilds the suffix exactly as log_activity()
    -- does.
    v_kind := CASE NEW.entity_type
                  WHEN 'calendar_event' THEN 'event'
                  WHEN 'budget' THEN 'budget category'
                  ELSE replace(NEW.entity_type, '_', ' ')
              END;
    v_core := NEW.summary;
    IF NEW.action = 'update' AND cardinality(NEW.changed_fields) > 0 THEN
        v_suffix := ' (' || array_to_string(NEW.changed_fields[1:5], ', ')
                 || CASE WHEN cardinality(NEW.changed_fields) > 5
                         THEN ', +' || (cardinality(NEW.changed_fields) - 5) || ' more' ELSE '' END
                 || ')';
        IF right(v_core, length(v_suffix)) = v_suffix THEN
            v_core := left(v_core, length(v_core) - length(v_suffix));
        END IF;
    END IF;
    -- "deleted calendar event ..." -> "deleted event ...".
    v_core := regexp_replace(v_core,
                             '^(\S+) ' || replace(NEW.entity_type, '_', ' '),
                             '\1 ' || v_kind);
    v_title := left(v_actor || ' ' || v_core, 480);

    v_body := 'You created this ' || v_kind || '.';
    IF NEW.action = 'update' AND cardinality(NEW.changed_fields) > 0 THEN
        v_body := v_body || ' Changed: '
               || replace(array_to_string(NEW.changed_fields, ', '), '_', ' ') || '.';
    END IF;

    INSERT INTO notifications (household_id, user_id, type, title, body, channel,
                               entity_type, entity_id)
    VALUES (NEW.household_id, v_creator, 'item_changed', v_title, v_body, 'in_app',
            v_link_type, v_link_id)
    RETURNING id INTO v_id;

    IF NEW.entity_type = 'calendar_event' THEN
        -- 050 reads an event's label from an iCal SUMMARY line, but the api,
        -- mcp and calendar app store ical_data as JSON, so the summary often
        -- has no label; notify_event_when fills it in.
        PERFORM set_config('home_os.creator_notice',
                           NEW.entity_id || ':' || v_id || ':'
                           || CASE WHEN v_core ~ '"' THEN 'label' ELSE 'nolabel' END,
                           true);
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER activity_log_notify_creator AFTER INSERT ON activity_log
    FOR EACH ROW EXECUTE FUNCTION notify_entity_creator();

-- Completes the notification notify_entity_creator just wrote for this event
-- with the event's name (when 050's summary had none) and date (see the
-- header).
CREATE OR REPLACE FUNCTION notify_event_when() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    v_pending TEXT := current_setting('home_os.creator_notice', true);
    v_row     calendar_objects;
    v_json    JSONB;
    v_ical    TEXT;
    v_label   TEXT;
    v_m       TEXT[];
    v_tz      TEXT;
    v_date    DATE;
    v_extra   TEXT := '';
BEGIN
    IF v_pending IS NULL OR v_pending = '' THEN
        RETURN NULL;
    END IF;
    IF TG_OP = 'DELETE' THEN v_row := OLD; ELSE v_row := NEW; END IF;
    IF split_part(v_pending, ':', 1) <> v_row.id::text THEN
        RETURN NULL;
    END IF;
    PERFORM set_config('home_os.creator_notice', '', true);

    BEGIN
        SELECT h.timezone INTO v_tz
          FROM calendars c JOIN households h ON h.id = c.household_id
         WHERE c.id = v_row.calendar_id;
        v_tz := COALESCE(v_tz, 'UTC');

        IF left(ltrim(v_row.ical_data), 1) = '{' THEN
            -- The api/mcp/calendar shape: {"title", "start", "all_day", ...}.
            v_json := v_row.ical_data::jsonb;
            v_label := v_json ->> 'title';
            IF v_json ->> 'start' ~ '^\d{4}-\d{2}-\d{2}' THEN
                IF (v_json ->> 'all_day') = 'true' OR v_json ->> 'start' !~ 'T' THEN
                    v_date := left(v_json ->> 'start', 10)::date;
                ELSE
                    v_date := ((v_json ->> 'start')::timestamptz AT TIME ZONE v_tz)::date;
                END IF;
            END IF;
        ELSE
            -- iCal: the VEVENT's SUMMARY and DTSTART, not a VTIMEZONE's.
            v_ical := v_row.ical_data;
            IF position('BEGIN:VEVENT' IN v_ical) = 0 THEN
                v_ical := COALESCE(v_row.ical_raw, '');
            END IF;
            v_ical := substring(v_ical FROM position('BEGIN:VEVENT' IN v_ical));
            v_label := substring(v_ical FROM '\nSUMMARY[^:\r\n]*:([^\r\n]*)');
            v_m := regexp_match(v_ical, '\nDTSTART[^:\r\n]*:(\d{4})(\d{2})(\d{2})(T(\d{2})(\d{2})(\d{2})(Z)?)?');
            IF v_m IS NOT NULL THEN
                IF v_m[8] = 'Z' THEN
                    v_date := ((make_timestamp(v_m[1]::int, v_m[2]::int, v_m[3]::int,
                                               v_m[5]::int, v_m[6]::int, v_m[7]::int)
                                AT TIME ZONE 'UTC') AT TIME ZONE v_tz)::date;
                ELSE
                    -- A floating or TZID time is already local.
                    v_date := make_date(v_m[1]::int, v_m[2]::int, v_m[3]::int);
                END IF;
            END IF;
        END IF;

        -- Names only: the event's title, never its description or location.
        v_label := NULLIF(left(regexp_replace(btrim(v_label), '\s+', ' ', 'g'), 80), '');
        IF split_part(v_pending, ':', 3) = 'nolabel' AND v_label IS NOT NULL THEN
            v_extra := ' "' || v_label || '"';
        END IF;
        IF v_date IS NOT NULL THEN
            v_extra := v_extra || ' on ' || to_char(v_date, 'Dy Mon FMDD');
        END IF;
        IF v_extra <> '' THEN
            UPDATE notifications SET title = left(title || v_extra, 500)
             WHERE id = split_part(v_pending, ':', 2)::uuid;
        END IF;
    EXCEPTION WHEN OTHERS THEN
        -- Bad JSON, DTSTART or timezone: keep the title as it is.
        NULL;
    END;
    RETURN NULL;
END;
$$;

CREATE TRIGGER calendar_objects_notify_when AFTER UPDATE OR DELETE ON calendar_objects
    FOR EACH ROW EXECUTE FUNCTION notify_event_when();

COMMIT;
