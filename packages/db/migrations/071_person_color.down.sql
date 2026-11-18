-- 071_person_color.down.sql (#4821)
--
-- Undoes 071: the default-colour trigger, the palette CHECK, people.color and
-- person_default_color() go.
--
-- Lost on down: the colour of every person (there is no earlier column to
-- restore it to). Up again re-derives each person's default from their id, so
-- a person who chose a colour goes back to their derived one. Safe to run
-- twice.

BEGIN;

DROP TRIGGER IF EXISTS people_default_color ON people;
DROP FUNCTION IF EXISTS people_set_default_color();

ALTER TABLE IF EXISTS people DROP CONSTRAINT IF EXISTS people_color_check;
ALTER TABLE IF EXISTS people DROP COLUMN IF EXISTS color;

DROP FUNCTION IF EXISTS person_default_color(UUID);

COMMIT;
