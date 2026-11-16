-- 061_notify_creator_role.up.sql (#4740)
--
-- Creator change notifications (054) follow the creator's CURRENT role.
--
-- 054's notify_entity_creator() only checked that the creator was still a
-- member of the family. A member who created a credential and was then
-- demoted to viewer kept getting `Dana updated secret "Bank of America
-- login"`, although a viewer may never see credential records (role policy,
-- KB decisions/2026-11-15-role-policy.md). A member demoted to housesitter or
-- vendor likewise kept hearing the names of bills, loans, vehicles, documents
-- and so on.
--
-- 1. role_can_see_entity(role, kind): SQL copy of auth.CanSeeEntity in
--    apps/api/internal/auth/roles.go (entityResources + the ActRead grants of
--    policy). It MUST stay in step with roles.go;
--    apps/api/internal/auth/roles_sql_test.go compares the two for every role
--    and every kind (roles.go's, the entity_kind domain's and every
--    log_activity trigger's) and fails when they differ. Fail closed: an
--    unknown kind is visible only to owner/family_manager/family_member, and
--    an unknown or NULL role sees nothing.
-- 2. notify_entity_creator(): 054's body (the only migration that defines
--    it), with ONE change: the membership check reads the creator's role and
--    returns unless the role may read the record's kind and, when it has
--    one, its parent's kind (a note on a secret, a note on a bill).
--
-- Notifications already written are not touched. The MCP notification tools
-- and the API notifications handler do not filter by role on read; they show
-- what was written while the member had the older role.
--
-- Runs in one transaction.

BEGIN;

-- Keep in step with apps/api/internal/auth/roles.go (CanSeeEntity).
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
            -- Unknown kind: family roles only (fails closed).
            ELSE ARRAY['owner', 'family_manager', 'family_member']
        END), false)
$$;

COMMENT ON FUNCTION role_can_see_entity(TEXT, TEXT) IS
    'SQL copy of auth.CanSeeEntity (apps/api/internal/auth/roles.go); keep in step. Unknown kinds: family roles only.';

-- 054's notify_entity_creator(), changed only in the membership check.
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

COMMIT;
