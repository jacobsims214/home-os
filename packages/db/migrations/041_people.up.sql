-- 041_people.up.sql
--
-- Stage 2: the people of a family (KB decisions/domain-model-v2.md,
-- "Contracts for the Stage 2 migrations" -> 041 people; ticket #4451).
--
-- A person is anyone the family keeps records about: members who log in
-- (user_id set) and kids, grandparents, nannies who don't (user_id NULL).
-- Logins stay in users/memberships; a person is the family's record of
-- someone, and outlives a deleted login (user_id ON DELETE SET NULL).
--
-- What this adds:
--   1. people: family-owned, at most one person per (family, user).
--   2. person_preferences: likes, dislikes, dietary needs per person. The
--      composite FK keeps a preference in its person's family.
--   3. Backfill: one person per existing membership, named from users.name.
--   4. memberships triggers: a new membership gets a person; a removed
--      membership unlinks the login from the person but keeps the person.
--   5. Entity registry: kind 'person' in entity_kind, a branch in
--      entity_household(), and rehome_entity_refs('person') on people, so
--      notes/files/secrets/links can be about a person and are cleaned up
--      (files/secrets re-homed to the family) when the person is deleted.
--
-- Independent of 040: touches no column 040 drops or constrains.
-- Runs in one transaction; idempotent backfill (ON CONFLICT DO NOTHING).

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. people
-- ---------------------------------------------------------------------------

CREATE TABLE people (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id  UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    -- Set for members who log in; NULL for kids and others without a login.
    user_id       UUID REFERENCES users(id) ON DELETE SET NULL,
    display_name  VARCHAR(255) NOT NULL CHECK (btrim(display_name) <> ''),
    -- Free text as the family describes it: 'parent', 'child', 'grandparent'.
    relationship  VARCHAR(50),
    birthdate     DATE,
    school        VARCHAR(255),
    grade         VARCHAR(50),
    allergies     TEXT[] NOT NULL DEFAULT '{}',
    notes         TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- NULLs are distinct: any number of people without a login.
    CONSTRAINT people_household_user_key UNIQUE (household_id, user_id),
    -- Target of person_preferences' same-family composite FK.
    CONSTRAINT people_id_household_key UNIQUE (id, household_id)
);

CREATE INDEX idx_people_user ON people (user_id) WHERE user_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 2. person_preferences
-- ---------------------------------------------------------------------------

CREATE TYPE person_preference_kind AS ENUM ('like', 'dislike', 'dietary', 'other');

CREATE TABLE person_preferences (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id  UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    person_id     UUID NOT NULL,
    kind          person_preference_kind NOT NULL,
    -- What the preference is about: 'food', 'activity', 'gift', ...
    category      VARCHAR(50) NOT NULL DEFAULT 'general' CHECK (btrim(category) <> ''),
    value         TEXT NOT NULL CHECK (btrim(value) <> ''),
    notes         TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT person_preferences_person_fkey FOREIGN KEY (person_id, household_id)
        REFERENCES people (id, household_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_person_preferences_unique
    ON person_preferences (person_id, kind, lower(category), lower(value));
CREATE INDEX idx_person_preferences_household ON person_preferences (household_id);

-- ---------------------------------------------------------------------------
-- 3. Backfill: one person per existing membership
-- ---------------------------------------------------------------------------

INSERT INTO people (household_id, user_id, display_name, created_at)
SELECT m.household_id, m.user_id,
       COALESCE(NULLIF(btrim(u.name), ''), u.email),
       m.created_at
  FROM memberships m
  JOIN users u ON u.id = m.user_id
ON CONFLICT (household_id, user_id) DO NOTHING;

-- ---------------------------------------------------------------------------
-- 4. Keep memberships and people in step
-- ---------------------------------------------------------------------------

-- Every member has a person. Code that creates the person first (e.g. an
-- invite for an existing kid) is fine: the conflict is ignored.
CREATE OR REPLACE FUNCTION membership_ensure_person()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO people (household_id, user_id, display_name)
    SELECT NEW.household_id, NEW.user_id, COALESCE(NULLIF(btrim(u.name), ''), u.email)
      FROM users u WHERE u.id = NEW.user_id
    ON CONFLICT (household_id, user_id) DO NOTHING;
    RETURN NULL;
END;
$$;

CREATE TRIGGER memberships_ensure_person AFTER INSERT ON memberships
    FOR EACH ROW EXECUTE FUNCTION membership_ensure_person();

-- Leaving a family unlinks the login; the person and their records stay.
CREATE OR REPLACE FUNCTION membership_unlink_person()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    UPDATE people SET user_id = NULL, updated_at = NOW()
     WHERE household_id = OLD.household_id AND user_id = OLD.user_id;
    RETURN NULL;
END;
$$;

CREATE TRIGGER memberships_unlink_person AFTER DELETE ON memberships
    FOR EACH ROW EXECUTE FUNCTION membership_unlink_person();

-- ---------------------------------------------------------------------------
-- 5. Entity registry: kind 'person'
-- ---------------------------------------------------------------------------

-- Each Stage 2 migration re-declares the full list (038's kinds plus its own
-- plus every earlier Stage 2 kind).
ALTER DOMAIN entity_kind DROP CONSTRAINT entity_kind_check;
ALTER DOMAIN entity_kind ADD CONSTRAINT entity_kind_check CHECK (VALUE IN (
    'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
    'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
    'file', 'note', 'secret',
    -- 041
    'person'
));

-- Same body as 038 plus the 041 branch. A later CREATE OR REPLACE must copy
-- this whole function, keeping every Stage 2 branch.
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
        ELSE v_household := NULL;
    END CASE;
    RETURN v_household;
END;
$$;

CREATE TRIGGER people_rehome_entity_refs AFTER DELETE ON people
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('person');

COMMIT;
