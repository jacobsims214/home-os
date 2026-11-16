-- 063_person_references.down.sql (#4743)
--
-- Drops person_reference_counts(). Run the api and mcp builds from before
-- #4743 with it: both call the function on person delete.

BEGIN;

DROP FUNCTION IF EXISTS person_reference_counts(UUID, UUID);

COMMIT;
