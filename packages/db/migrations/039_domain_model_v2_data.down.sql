-- 039_domain_model_v2_data.down.sql
--
-- Undoes what 039 can undo: column types, family-level files/secrets back to
-- a NULL entity, the calendar literal, and the notes 039 wrote. The typed
-- subject FKs and rooms.household_id are dropped by 038's down; the legacy
-- columns were never changed, so pre-v2 code reads the same data as before.
--
-- Not undone (see 039 up): re-homed rows that failed the owner check,
-- converted or deleted integration links, cleared cross-family references,
-- cleared extra maintenance subjects.

BEGIN;

ALTER TABLE invitations ALTER COLUMN role DROP DEFAULT;
ALTER TABLE invitations ALTER COLUMN role TYPE TEXT USING role::text;
ALTER TABLE invitations ALTER COLUMN role SET DEFAULT 'family_member';

DROP TRIGGER notes_enforce_entity_owner ON notes;
DROP TRIGGER files_enforce_entity_owner ON files;
DROP TRIGGER secrets_enforce_entity_owner ON secrets;
DROP TRIGGER document_links_enforce_entity_owner ON document_links;
ALTER TABLE document_links ALTER COLUMN entity_type TYPE VARCHAR(100);
ALTER TABLE secrets        ALTER COLUMN entity_type TYPE VARCHAR(50);
ALTER TABLE files          ALTER COLUMN entity_type TYPE VARCHAR(50);
ALTER TABLE notes          ALTER COLUMN entity_type TYPE VARCHAR(50);
CREATE TRIGGER notes_enforce_entity_owner
    BEFORE INSERT OR UPDATE OF entity_type, entity_id, household_id ON notes
    FOR EACH ROW EXECUTE FUNCTION enforce_entity_owner();
CREATE TRIGGER files_enforce_entity_owner
    BEFORE INSERT OR UPDATE OF entity_type, entity_id, household_id ON files
    FOR EACH ROW EXECUTE FUNCTION enforce_entity_owner();
CREATE TRIGGER secrets_enforce_entity_owner
    BEFORE INSERT OR UPDATE OF entity_type, entity_id, household_id ON secrets
    FOR EACH ROW EXECUTE FUNCTION enforce_entity_owner();
CREATE TRIGGER document_links_enforce_entity_owner
    BEFORE INSERT OR UPDATE OF entity_type, entity_id, household_id, link_type, link_id ON document_links
    FOR EACH ROW EXECUTE FUNCTION enforce_entity_owner();

UPDATE files   SET entity_type = NULL, entity_id = NULL WHERE entity_type = 'household';
UPDATE secrets SET entity_type = NULL, entity_id = NULL WHERE entity_type = 'household';

UPDATE calendar_objects SET entity_type = 'maintenance' WHERE entity_type = 'maintenance_task';

DELETE FROM notes WHERE entity_type IN ('bill', 'loan') AND title = 'Earlier link (migration 039)';
DELETE FROM notes WHERE entity_type = 'vendor' AND title = 'Serves property';

COMMIT;
