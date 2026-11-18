-- 061_notify_creator_role.down.sql (#4740)
--
-- Restores 054's notify_entity_creator() (membership check only, whatever
-- the creator's role) and drops role_can_see_entity(). Notifications already
-- written are kept. If 054 down has already dropped notify_entity_creator(),
-- it is not recreated.

BEGIN;

DO $do$
BEGIN
    IF to_regprocedure('notify_entity_creator()') IS NOT NULL THEN
        EXECUTE $sql$
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
        $sql$;
    END IF;
END
$do$;

DROP FUNCTION IF EXISTS role_can_see_entity(TEXT, TEXT);

COMMIT;
