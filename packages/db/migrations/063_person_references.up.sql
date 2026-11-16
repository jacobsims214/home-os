-- 063_person_references.up.sql (#4743)
--
-- person_reference_counts(household, person): what points at a person
-- (people, migration 041) and is deleted or unlinked when the person is
-- deleted (rehome_entity_refs, 038, via people_rehome_entity_refs):
--
--   notes         notes about the person (entity_type 'person'); DELETED
--   documents     files with person_id = the person, or about the person
--   list_items    list items assigned to the person (assignee_person_id)
--   meal_ratings  meal_feedback rows by the person
--   events        calendar objects carrying X-HOMEOS-PERSON-ID=<id> in ical_raw
--
-- One definition for both deleters: the REST API (DELETE /api/v1/people/{id},
-- apps/api/internal/people/repo.go) and the MCP person_delete tool
-- (apps/mcp/internal/people/tools.go). Both refuse to delete a person with
-- any of these counts above zero unless the caller confirms. Preferences and
-- allergies go with the person and are not counted here.
--
-- Every count is filtered by household, so a caller can only learn about its
-- own family. STABLE, SECURITY INVOKER: it reads with the caller's rights.

BEGIN;

CREATE OR REPLACE FUNCTION person_reference_counts(p_household UUID, p_person UUID)
RETURNS TABLE (notes BIGINT, documents BIGINT, list_items BIGINT, meal_ratings BIGINT, events BIGINT)
LANGUAGE sql
STABLE
AS $$
SELECT
    (SELECT count(*) FROM notes n
      WHERE n.household_id = p_household AND n.entity_type = 'person' AND n.entity_id = p_person),
    (SELECT count(*) FROM files f
      WHERE f.household_id = p_household
        AND (f.person_id = p_person OR (f.entity_type = 'person' AND f.entity_id = p_person))),
    (SELECT count(*) FROM list_items li
      WHERE li.household_id = p_household AND li.assignee_person_id = p_person),
    (SELECT count(*) FROM meal_feedback mf
      WHERE mf.household_id = p_household AND mf.person_id = p_person),
    (SELECT count(*) FROM calendar_objects co JOIN calendars cal ON cal.id = co.calendar_id
      WHERE cal.household_id = p_household
        AND co.ical_raw LIKE '%X-HOMEOS-PERSON-ID=' || p_person::text || '%');
$$;

COMMENT ON FUNCTION person_reference_counts(UUID, UUID) IS
    'Records deleted (notes) or unlinked when a person is deleted; shared by the API and MCP delete confirm (#4743).';

COMMIT;
