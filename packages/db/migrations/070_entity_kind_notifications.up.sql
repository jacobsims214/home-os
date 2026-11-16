-- 070_entity_kind_notifications.up.sql (#4708)
--
-- notifications.entity_type and calendar_objects.entity_type become the
-- entity_kind domain, and a notification can no longer point at another
-- family's record (#4459: a family-A notification about family B's property
-- was accepted).
--
-- Values written before this migration, and what happens to them
-- (KB architecture/domain-model-v2-cleanup-056.md, "070"):
--   registered as entity kinds (entity_household() branch and
--   rehome_entity_refs(kind) on their table):
--     'calendar'       calendars        (item_changed for a calendar, and the
--                                        parent an event delete links to)
--     'calendar_event' calendar_objects (worker alarms, item_changed)
--     'document_link'  document_links   (item_changed; kept as itself so the
--                                        notification read filter (#4794)
--                                        still sees a secret link)
--   mapped to the parent kind (same role resource, NOT NULL parent FK):
--     'list_item'         -> ('list', list_id)
--     'meal_feedback'     -> ('meal_plan', meal_plan_id)
--     'person_preference' -> ('person', person_id)
--   'maintenance' (pre-039 calendar code, the generic create-event API)
--     -> 'maintenance_task' (#4701).
--   Anything else, a half reference (type without id or the reverse), a
--   record that no longer exists or one of another family -> no reference
--   (NULL, NULL). A NOTICE reports the counts.
--
-- In order:
--   1. entity_kind gains the three kinds; entity_household() (049's body)
--      gets their branches.
--   2. Functions that compare these columns with literals stop using
--      'maintenance' (an unknown literal is coerced to the domain and fails
--      its CHECK at run time): 038's delete_maintenance_on_calendar_delete()
--      (027's trigger) and 046's rehome_entity_refs() (lists block kept;
--      its notification clear now skips households being deleted, as 056's
--      calendar_objects_clear_notifications did).
--   3. notify_entity_creator(), copied from 061: list items, meal ratings
--      and person preferences link to their parent; a link the owner check
--      would refuse (a deleted record with no parent, an unregistered kind)
--      becomes no link instead of failing the user's write.
--   4. enforce_entity_owner() (038) also allows a NULL entity on
--      notifications; calendar_objects_enforce_entity_owner() checks an
--      event's entity against its calendar's family.
--   5. Existing rows are repaired, then both columns are ALTERed to
--      entity_kind.
--   6. Triggers: notifications_enforce_entity_owner,
--      calendar_objects_enforce_entity_owner, and rehome_entity_refs on
--      calendars, calendar_objects and document_links. 056's
--      calendar_objects_clear_notifications is dropped: rehome_entity_refs
--      ('calendar_event') does the same and more.
--
-- Registering the kinds also lets notes, files, secrets, lists and document
-- links be about a calendar, an event or a document link at the database
-- level; deleting one re-homes or deletes them like any other kind.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Entity registry: 'calendar', 'calendar_event', 'document_link'
-- ---------------------------------------------------------------------------

-- Each migration that adds kinds re-declares the full list. Copied from 049,
-- the latest definition.
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
    'guide',
    -- 070
    'calendar', 'calendar_event', 'document_link'
));

-- entity_household(): 049's body plus the 070 branches.
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
        -- 070 calendars, events and document links
        WHEN 'calendar'             THEN SELECT household_id INTO v_household FROM calendars WHERE id = p_id;
        WHEN 'calendar_event'       THEN SELECT c.household_id INTO v_household
                                         FROM calendar_objects o JOIN calendars c ON c.id = o.calendar_id
                                         WHERE o.id = p_id;
        WHEN 'document_link'        THEN SELECT household_id INTO v_household FROM document_links WHERE id = p_id;
        -- end 070
        ELSE v_household := NULL;
    END CASE;
    RETURN v_household;
END;
$$;

-- ---------------------------------------------------------------------------
-- 2. No 'maintenance' literal
-- ---------------------------------------------------------------------------

-- 038's body (027's trigger, fired AFTER DELETE since 038).
CREATE OR REPLACE FUNCTION delete_maintenance_on_calendar_delete()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.entity_type = 'maintenance_task' AND OLD.entity_id IS NOT NULL THEN
        DELETE FROM maintenance_tasks WHERE id = OLD.entity_id;
    END IF;
    RETURN OLD;
END;
$$;

-- 046's body, with the maintenance branch and the notification clear changed.
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
    -- the event goes too. calendar_objects.entity_type is entity_kind since
    -- 070, so the old 'maintenance' literal is gone (it would fail the
    -- domain check).
    IF v_kind = 'maintenance_task' THEN
        DELETE FROM calendar_objects
         WHERE entity_id = OLD.id AND entity_type = 'maintenance_task';
    ELSE
        UPDATE calendar_objects SET entity_type = NULL, entity_id = NULL, updated_at = NOW()
         WHERE entity_type = v_kind AND entity_id = OLD.id;
    END IF;

    -- Skipped while the household itself is being deleted (as 056's
    -- calendar_objects_clear_notifications did): its notifications go by
    -- cascade.
    UPDATE notifications n SET entity_type = NULL, entity_id = NULL
     WHERE n.entity_type = v_kind AND n.entity_id = OLD.id
       AND EXISTS (SELECT 1 FROM households h WHERE h.id = n.household_id);

    RETURN NULL;
END;
$$;

-- ---------------------------------------------------------------------------
-- 3. Creator change notifications link only to registered kinds
-- ---------------------------------------------------------------------------

-- 061's notify_entity_creator(), changed only where it picks the link.
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

    -- What the notification opens: see the header. A list item, meal rating
    -- or person preference is not an entity kind (070): it always opens its
    -- list, meal plan or person.
    IF NEW.parent_id IS NOT NULL
       AND (NEW.entity_type IN ('list_item', 'meal_feedback', 'person_preference')
            OR NEW.action = 'delete') THEN
        v_link_type := NEW.parent_type;
        v_link_id := NEW.parent_id;
    ELSE
        v_link_type := NEW.entity_type;
        v_link_id := NEW.entity_id;
    END IF;
    -- notifications.entity_type is entity_kind with an owner check (070).
    -- A link the check would refuse is written as no link instead of failing
    -- the user's write: a deleted record without a parent (the row is
    -- already gone; before 070 rehome_entity_refs nulled that link right
    -- after), or a kind the registry does not know (entity_household() is
    -- NULL for both).
    IF v_link_type IS NULL OR v_link_id IS NULL
       OR entity_household(v_link_type, v_link_id) IS DISTINCT FROM NEW.household_id THEN
        v_link_type := NULL;
        v_link_id := NULL;
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

-- ---------------------------------------------------------------------------
-- 4. Owner checks
-- ---------------------------------------------------------------------------

-- 038's body; notifications may have no entity.
CREATE OR REPLACE FUNCTION enforce_entity_owner()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_owner UUID;
    v_link  UUID;
BEGIN
    -- 070: a notification may be about nothing (or about a deleted record,
    -- see rehome_entity_refs()).
    IF NEW.entity_type IS NULL AND NEW.entity_id IS NULL AND TG_TABLE_NAME IN ('files', 'secrets', 'notifications') THEN
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

-- An event's entity must be a record of the calendar's family. calendar_objects
-- has no household_id; the calendar's is used.
CREATE FUNCTION calendar_objects_enforce_entity_owner()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_household UUID;
    v_owner     UUID;
BEGIN
    IF NEW.entity_type IS NULL AND NEW.entity_id IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT household_id INTO v_household FROM calendars WHERE id = NEW.calendar_id;
    v_owner := entity_household(NEW.entity_type, NEW.entity_id);
    IF v_owner IS NULL OR v_owner IS DISTINCT FROM v_household THEN
        RAISE EXCEPTION 'calendar_objects: % % does not exist in this family', NEW.entity_type, NEW.entity_id
            USING ERRCODE = 'foreign_key_violation',
                  HINT = 'entity_type must be a known kind and entity_id a row of the calendar''s household, or both NULL';
    END IF;
    RETURN NEW;
END;
$$;

-- ---------------------------------------------------------------------------
-- 5. Repair rows, then convert the columns
-- ---------------------------------------------------------------------------
DO $$
DECLARE
    n INT;
BEGIN
    UPDATE calendar_objects SET entity_type = 'maintenance_task', updated_at = NOW()
     WHERE entity_type = 'maintenance';
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '070: % calendar_objects maintenance -> maintenance_task', n;

    UPDATE calendar_objects o SET entity_type = NULL, entity_id = NULL, updated_at = NOW()
      FROM calendars c
     WHERE c.id = o.calendar_id
       AND (o.entity_type IS NOT NULL OR o.entity_id IS NOT NULL)
       AND (o.entity_type IS NULL OR o.entity_id IS NULL
            OR entity_household(o.entity_type, o.entity_id) IS DISTINCT FROM c.household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '070: cleared % unknown, missing or cross-family calendar_objects entity references', n;

    UPDATE notifications SET entity_type = 'maintenance_task' WHERE entity_type = 'maintenance';
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '070: % notifications maintenance -> maintenance_task', n;

    UPDATE notifications n SET entity_type = 'list', entity_id = li.list_id
      FROM list_items li
     WHERE n.entity_type = 'list_item' AND li.id = n.entity_id;
    UPDATE notifications n SET entity_type = 'meal_plan', entity_id = mf.meal_plan_id
      FROM meal_feedback mf
     WHERE n.entity_type = 'meal_feedback' AND mf.id = n.entity_id;
    UPDATE notifications n SET entity_type = 'person', entity_id = pp.person_id
      FROM person_preferences pp
     WHERE n.entity_type = 'person_preference' AND pp.id = n.entity_id;

    -- entity_household() is NULL for an unknown kind and for a missing row.
    UPDATE notifications SET entity_type = NULL, entity_id = NULL
     WHERE (entity_type IS NOT NULL OR entity_id IS NOT NULL)
       AND (entity_type IS NULL OR entity_id IS NULL
            OR entity_household(entity_type, entity_id) IS DISTINCT FROM household_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE '070: cleared % unknown, missing or cross-family notification entity references', n;
END $$;

ALTER TABLE notifications    ALTER COLUMN entity_type TYPE entity_kind;
ALTER TABLE calendar_objects ALTER COLUMN entity_type TYPE entity_kind;

-- ---------------------------------------------------------------------------
-- 6. Triggers
-- ---------------------------------------------------------------------------

CREATE TRIGGER notifications_enforce_entity_owner
    BEFORE INSERT OR UPDATE OF entity_type, entity_id, household_id ON notifications
    FOR EACH ROW EXECUTE FUNCTION enforce_entity_owner();
CREATE TRIGGER calendar_objects_enforce_entity_owner
    BEFORE INSERT OR UPDATE OF entity_type, entity_id, calendar_id ON calendar_objects
    FOR EACH ROW EXECUTE FUNCTION calendar_objects_enforce_entity_owner();

-- rehome_entity_refs on the new kinds' tables. On calendar_objects it sorts
-- after calendar_objects_activity and calendar_objects_notify_when, as on
-- every other table (054 relies on that order).
DROP TRIGGER calendar_objects_clear_notifications ON calendar_objects;
DROP FUNCTION calendar_objects_clear_notifications();
CREATE TRIGGER calendars_rehome_entity_refs AFTER DELETE ON calendars
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('calendar');
CREATE TRIGGER calendar_objects_rehome_entity_refs AFTER DELETE ON calendar_objects
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('calendar_event');
CREATE TRIGGER document_links_rehome_entity_refs AFTER DELETE ON document_links
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('document_link');

COMMIT;
