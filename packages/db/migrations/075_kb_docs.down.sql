-- 075_kb_docs.down.sql (#5502)

BEGIN;

-- Back to 061's body (no 'kb_doc' branch).
CREATE OR REPLACE FUNCTION role_can_see_entity(p_role TEXT, p_kind TEXT) RETURNS BOOLEAN
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
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
            ELSE ARRAY['owner', 'family_manager', 'family_member']
        END), false)
$$;

DROP TRIGGER IF EXISTS kb_docs_touch_updated_at ON kb_docs;
DROP FUNCTION IF EXISTS kb_docs_touch_updated_at();
DROP TABLE IF EXISTS kb_docs;

COMMIT;