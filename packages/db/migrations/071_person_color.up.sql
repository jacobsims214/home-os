-- 071_person_color.up.sql (#4821)
--
-- people.color: the colour a person is drawn in across the UI (the Today and
-- Calendar screens colour a family event by the first involved person, and
-- Settings has "Change colour").
--
-- Why a fixed palette of names and not a hex value: this string is
-- interpolated into a style/class by the UI, so anything a client could send
-- would be an injection surface, and a raw hex value cannot follow the light
-- and dark schemes. The eight names are Mantine palette colours, and
-- apps/ui/app/providers.tsx already carries per-colour contrast fixes for
-- every one of them in both schemes (#4754), so the UI can render
-- var(--mantine-color-<name>-6) or <Badge color={person.color}> and stay
-- readable. KB architecture/theme-tokens.md, api/person-color.md.
--
-- What this adds:
--   1. people.color VARCHAR(20) + a CHECK naming the palette.
--   2. Backfill: every existing person gets a colour derived from their id.
--   3. NOT NULL, and a BEFORE INSERT trigger that fills the colour the same
--      way, so the memberships trigger (041) and every other INSERT get one
--      without naming the column.
--
-- The default is a pure function of the person's id (person_default_color),
-- not a random draw: it is stable for the life of the row, the backfill and
-- new inserts cannot disagree, and re-running this migration lands on the
-- same colours. Two people in one family may collide, which is fine - the
-- colour is cosmetic and settable (PATCH /api/v1/people/{id}).
--
-- Runs in one transaction. Safe to run twice.

BEGIN;

-- 1. The palette and the deterministic default.

-- person_default_color maps a person id onto the palette below. It reads the
-- first byte of the uuid (16 bytes of hex after the dashes) modulo the
-- palette size: no hash-function dependency, no overflow, and a v4 uuid's
-- first byte is random.
CREATE OR REPLACE FUNCTION person_default_color(p_id UUID)
RETURNS TEXT
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT (ARRAY['red', 'orange', 'yellow', 'green',
                  'teal', 'cyan', 'blue', 'violet'])
           [1 + get_byte(decode(replace(p_id::text, '-', ''), 'hex'), 0) % 8];
$$;

COMMENT ON FUNCTION person_default_color(UUID) IS
    'The default people.color for a person: a palette name derived from the id, so it is stable per person (#4821).';

ALTER TABLE people ADD COLUMN IF NOT EXISTS color VARCHAR(20);

-- 2. Backfill, then require a colour.

UPDATE people SET color = person_default_color(id) WHERE color IS NULL;

ALTER TABLE people ALTER COLUMN color SET NOT NULL;

-- 3. The palette is the database's rule too, so nothing but a palette name
-- can be stored even by a writer that bypasses the API (MCP, a script, a
-- future migration). Keep this list in step with people.PaletteColours.
ALTER TABLE people DROP CONSTRAINT IF EXISTS people_color_check;
ALTER TABLE people ADD CONSTRAINT people_color_check CHECK (color IN (
    'red', 'orange', 'yellow', 'green', 'teal', 'cyan', 'blue', 'violet'
));

COMMENT ON COLUMN people.color IS
    'Palette name the person is drawn in (one of the eight in people_color_check); defaulted from the id, settable by the person or a manager (#4821).';

-- A new person (the API, the memberships trigger from 041, MCP person_create,
-- a test fixture) gets a default colour without naming the column.
CREATE OR REPLACE FUNCTION people_set_default_color()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.color IS NULL THEN
        NEW.color := person_default_color(NEW.id);
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS people_default_color ON people;
CREATE TRIGGER people_default_color BEFORE INSERT ON people
    FOR EACH ROW EXECUTE FUNCTION people_set_default_color();

COMMIT;
