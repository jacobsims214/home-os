-- 070_entity_kind_notifications.down.sql (#4708)
--
-- Undoes 070: notifications.entity_type and calendar_objects.entity_type go
-- back to VARCHAR(100), the owner checks on them go, and entity_kind loses
-- 'calendar', 'calendar_event' and 'document_link'.
--
-- In order:
--   1. The 070 triggers and calendar_objects_enforce_entity_owner() are
--      dropped; 056's calendar_objects_clear_notifications comes back (056
--      down drops it).
--   2. Both columns become VARCHAR(100) again.
--   3. Rows of the other entity_kind tables about a calendar, an event or a
--      document link are handled as rehome_entity_refs() would: notes and
--      document links are deleted, files, secrets and lists become
--      family-level. The rehome triggers are already gone, so this is done
--      directly. After that the narrower (049) check can be added back.
--   4. entity_household() (049), rehome_entity_refs() (046),
--      delete_maintenance_on_calendar_delete() (038), enforce_entity_owner()
--      (038) and notify_entity_creator() (061, only if it still exists) get
--      their earlier bodies back.
--
-- Not restored: rows 070 up repaired. Notifications mapped from list_item,
-- meal_feedback and person_preference keep their parent link, and cleared
-- or renamed ('maintenance') references stay as up left them; the older code
-- reads both shapes.

BEGIN;

-- 1. Triggers.
DROP TRIGGER IF EXISTS notifications_enforce_entity_owner ON notifications;
DROP TRIGGER IF EXISTS calendar_objects_enforce_entity_owner ON calendar_objects;
DROP TRIGGER IF EXISTS calendars_rehome_entity_refs ON calendars;
DROP TRIGGER IF EXISTS calendar_objects_rehome_entity_refs ON calendar_objects;
DROP TRIGGER IF EXISTS document_links_rehome_entity_refs ON document_links;
DROP FUNCTION IF EXISTS calendar_objects_enforce_entity_owner();

-- 056's trigger: a deleted event leaves no dangling notification reference.
CREATE FUNCTION calendar_objects_clear_notifications()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    UPDATE notifications n SET entity_type = NULL, entity_id = NULL
     WHERE n.entity_type = 'calendar_event' AND n.entity_id = OLD.id
       AND EXISTS (SELECT 1 FROM households h WHERE h.id = n.household_id);
    RETURN NULL;
END;
$$;

CREATE TRIGGER calendar_objects_clear_notifications
    AFTER DELETE ON calendar_objects
    FOR EACH ROW EXECUTE FUNCTION calendar_objects_clear_notifications();

-- 2. Columns.
ALTER TABLE notifications    ALTER COLUMN entity_type TYPE VARCHAR(100);
ALTER TABLE calendar_objects ALTER COLUMN entity_type TYPE VARCHAR(100);

-- 3. Rows about the kinds that go away.
DELETE FROM notes WHERE entity_type IN ('calendar', 'calendar_event', 'document_link');
DELETE FROM document_links WHERE entity_type IN ('calendar', 'calendar_event', 'document_link');
UPDATE files SET entity_type = 'household', entity_id = household_id, updated_at = NOW()
 WHERE entity_type IN ('calendar', 'calendar_event', 'document_link');
UPDATE secrets SET entity_type = 'household', entity_id = household_id, updated_at = NOW()
 WHERE entity_type IN ('calendar', 'calendar_event', 'document_link');
UPDATE lists SET entity_type = 'household', entity_id = household_id
 WHERE entity_type IN ('calendar', 'calendar_event', 'document_link');

-- entity_kind as 049 left it.
ALTER DOMAIN entity_kind DROP CONSTRAINT entity_kind_check;
ALTER DOMAIN entity_kind ADD CONSTRAINT entity_kind_check CHECK (VALUE IN (
    'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
    'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
    'file', 'note', 'secret',
    -- 041
    'person',
    -- 044
    'budget',
    -- 046
    'list',
    -- 047
    'recipe', 'meal_plan',
    -- 049
    'guide'
));

-- 4. Earlier function bodies.

-- entity_household() as 049 left it.
CREATE OR REPLACE FUNCTION entity_household(p_kind TEXT, p_id UUID)
RETURNS UUID
LANGUAGE plpgsql STABLE
AS $$
DECLARE
    v_household UUID;
BEGIN
    IF p_kind IS NULL OR p_id IS NULL THEN
        RETURN NULL;
    END IF;
    CASE p_kind
        WHEN 'household'            THEN SELECT id INTO v_household FROM households WHERE id = p_id;
        WHEN 'property'             THEN SELECT household_id INTO v_household FROM properties WHERE id = p_id;
        -- rooms.household_id is nullable until 040; the property is authoritative.
        WHEN 'room'                 THEN SELECT p.household_id INTO v_household
                                         FROM rooms r JOIN properties p ON p.id = r.property_id
                                         WHERE r.id = p_id;
        WHEN 'asset'                THEN SELECT household_id INTO v_household FROM assets WHERE id = p_id;
        WHEN 'vehicle'              THEN SELECT household_id INTO v_household FROM vehicles WHERE id = p_id;
        WHEN 'pet'                  THEN SELECT household_id INTO v_household FROM pets WHERE id = p_id;
        WHEN 'vendor'               THEN SELECT household_id INTO v_household FROM vendors WHERE id = p_id;
        WHEN 'bill'                 THEN SELECT household_id INTO v_household FROM bills WHERE id = p_id;
        WHEN 'loan'                 THEN SELECT household_id INTO v_household FROM loans WHERE id = p_id;
        WHEN 'maintenance_task'     THEN SELECT household_id INTO v_household FROM maintenance_tasks WHERE id = p_id;
        WHEN 'maintenance_schedule' THEN SELECT household_id INTO v_household FROM maintenance_schedules WHERE id = p_id;
        WHEN 'file'                 THEN SELECT household_id INTO v_household FROM files WHERE id = p_id;
        WHEN 'note'                 THEN SELECT household_id INTO v_household FROM notes WHERE id = p_id;
        WHEN 'secret'               THEN SELECT household_id INTO v_household FROM secrets WHERE id = p_id;
        -- 041 people
        WHEN 'person'               THEN SELECT household_id INTO v_household FROM people WHERE id = p_id;
        -- end 041
        -- 044 budgeting
        WHEN 'budget'               THEN SELECT household_id INTO v_household FROM budget_categories WHERE id = p_id;
        -- end 044
        -- 046 lists
        WHEN 'list'                 THEN SELECT household_id INTO v_household FROM lists WHERE id = p_id;
        -- end 046
        -- 047 meals
        WHEN 'recipe'               THEN SELECT household_id INTO v_household FROM recipes WHERE id = p_id;
        WHEN 'meal_plan'            THEN SELECT household_id INTO v_household FROM meal_plans WHERE id = p_id;
        -- end 047
        -- 049 guides
        WHEN 'guide'                THEN SELECT household_id INTO v_household FROM guides WHERE id = p_id;
        -- end 049
        ELSE v_household := NULL;
    END CASE;
    RETURN v_household;
END;
$$;

-- rehome_entity_refs() as 046 left it.
CREATE OR REPLACE FUNCTION rehome_entity_refs()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_kind TEXT := TG_ARGV[0];
BEGIN
    UPDATE files f SET entity_type = 'household', entity_id = f.household_id, updated_at = NOW()
     WHERE f.entity_type = v_kind AND f.entity_id = OLD.id
       AND EXISTS (SELECT 1 FROM households h WHERE h.id = f.household_id);

    UPDATE secrets s SET entity_type = 'household', entity_id = s.household_id, updated_at = NOW()
     WHERE s.entity_type = v_kind AND s.entity_id = OLD.id
       AND EXISTS (SELECT 1 FROM households h WHERE h.id = s.household_id);

    -- 046 lists: a list about a deleted entity becomes family-level.
    UPDATE lists l SET entity_type = 'household', entity_id = l.household_id
     WHERE l.entity_type = v_kind AND l.entity_id = OLD.id
       AND EXISTS (SELECT 1 FROM households h WHERE h.id = l.household_id);
    -- end 046

    DELETE FROM notes WHERE entity_type = v_kind AND entity_id = OLD.id;
    DELETE FROM document_links WHERE entity_type = v_kind AND entity_id = OLD.id;

    -- A maintenance task and its calendar event are one thing (027 deletes
    -- the task with its event; the api deletes the event with its task), so
    -- the event goes too. Until 040 calendar code writes 'maintenance'.
    IF v_kind = 'maintenance_task' THEN
        DELETE FROM calendar_objects
         WHERE entity_id = OLD.id AND entity_type IN ('maintenance', 'maintenance_task');
    ELSE
        UPDATE calendar_objects SET entity_type = NULL, entity_id = NULL, updated_at = NOW()
         WHERE entity_type = v_kind AND entity_id = OLD.id;
    END IF;

    UPDATE notifications SET entity_type = NULL, entity_id = NULL
     WHERE entity_type = v_kind AND entity_id = OLD.id;

    RETURN NULL;
END;
$$;

-- delete_maintenance_on_calendar_delete() as 038 left it.
CREATE OR REPLACE FUNCTION delete_maintenance_on_calendar_delete()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.entity_type IN ('maintenance', 'maintenance_task') AND OLD.entity_id IS NOT NULL THEN
        DELETE FROM maintenance_tasks WHERE id = OLD.entity_id;
    END IF;
    RETURN OLD;
END;
$$;

-- enforce_entity_owner() as 038 left it.
CREATE OR REPLACE FUNCTION enforce_entity_owner()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_owner UUID;
    v_link  UUID;
BEGIN
    IF NEW.entity_type IS NULL AND NEW.entity_id IS NULL AND TG_TABLE_NAME IN ('files', 'secrets') THEN
        RETURN NEW;
    END IF;

    v_owner := entity_household(NEW.entity_type, NEW.entity_id);

    IF TG_TABLE_NAME = 'document_links' AND NEW.household_id IS NULL THEN
        NEW.household_id := v_owner;
    END IF;

    IF v_owner IS NULL OR v_owner IS DISTINCT FROM NEW.household_id THEN
        RAISE EXCEPTION '%: % % does not exist in this family', TG_TABLE_NAME, NEW.entity_type, NEW.entity_id
            USING ERRCODE = 'foreign_key_violation',
                  HINT = 'entity_type must be a known kind and entity_id a row of the same household; use (''household'', household_id) for family-level rows';
    END IF;

    -- Nested IFs: NEW.link_type only exists on document_links, and SQL AND
    -- does not short-circuit.
    IF TG_TABLE_NAME <> 'document_links' THEN
        RETURN NEW;
    END IF;
    IF NEW.link_type IN ('file', 'secret') THEN
        IF NEW.link_id IS NULL OR NEW.link_id !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
            RAISE EXCEPTION 'document_links: link_id must be the id of a % in this family', NEW.link_type
                USING ERRCODE = 'foreign_key_violation';
        END IF;
        v_link := entity_household(NEW.link_type, NEW.link_id::uuid);
        IF v_link IS DISTINCT FROM NEW.household_id THEN
            RAISE EXCEPTION 'document_links: % % does not exist in this family', NEW.link_type, NEW.link_id
                USING ERRCODE = 'foreign_key_violation';
        END IF;
    END IF;

    RETURN NEW;
END;
$$;

-- notify_entity_creator() as 061 left it, unless 054 down already dropped it.
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
    v_role      TEXT;
BEGIN
    IF NEW.action NOT IN ('update', 'delete')
       OR NEW.actor_user_id IS NULL
       OR v_creator IS NULL
       OR NEW.actor_user_id = v_creator THEN
        RETURN NULL;
    END IF;
    -- Only members of the family see its notifications, and only for kinds
    -- their CURRENT role may read (061): the record's kind and, when it has
    -- one, its parent's kind (a note on a secret is a credential record for
    -- a viewer). A demoted creator stops hearing about what they can no
    -- longer see.
    SELECT role::text INTO v_role FROM memberships
     WHERE household_id = NEW.household_id AND user_id = v_creator;
    IF v_role IS NULL
       OR NOT role_can_see_entity(v_role, NEW.entity_type)
       OR (NEW.parent_type IS NOT NULL AND NOT role_can_see_entity(v_role, NEW.parent_type)) THEN
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

COMMIT;
