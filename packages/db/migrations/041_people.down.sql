-- 041_people.down.sql
--
-- Removes people and person_preferences and kind 'person'.
--
-- People are deleted row by row first so rehome_entity_refs('person') runs:
-- files and secrets about a person move to the family, notes and links about
-- a person are deleted, calendar events and notifications lose the reference.
-- After that no row uses kind 'person' and the narrower entity_kind check can
-- be added back. Person data (birthdates, schools, preferences) is lost.

BEGIN;

DELETE FROM people;

DROP TRIGGER IF EXISTS people_rehome_entity_refs ON people;

-- entity_household() as 038 created it (no 041 branch).
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
        ELSE v_household := NULL;
    END CASE;
    RETURN v_household;
END;
$$;

ALTER DOMAIN entity_kind DROP CONSTRAINT entity_kind_check;
ALTER DOMAIN entity_kind ADD CONSTRAINT entity_kind_check CHECK (VALUE IN (
    'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
    'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
    'file', 'note', 'secret'
));

DROP TRIGGER IF EXISTS memberships_unlink_person ON memberships;
DROP FUNCTION IF EXISTS membership_unlink_person();
DROP TRIGGER IF EXISTS memberships_ensure_person ON memberships;
DROP FUNCTION IF EXISTS membership_ensure_person();

DROP TABLE IF EXISTS person_preferences;
DROP TYPE IF EXISTS person_preference_kind;
DROP TABLE IF EXISTS people;

COMMIT;
