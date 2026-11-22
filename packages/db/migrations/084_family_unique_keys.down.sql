-- 084_family_unique_keys.down.sql (#5512)
--
-- Reverses 084_family_unique_keys.up.sql: drops the 12 UNIQUE (id,
-- household_id) keys it added. Nothing else changes — no data was touched, no
-- constraint outside this migration is affected — so the down is exactly the
-- inverse and leaves the schema as 082 had it.
--
-- #5519's composite FKs depend on these keys. Its own down (which runs first in
-- a full chain walk, newest first) puts the single-column FKs back, so by the
-- time this runs nothing references the keys being dropped. IF EXISTS keeps a
-- re-run idempotent, as 077/082 do.

BEGIN;

-- Reverse order of the up migration. Order does not matter (nothing in this
-- migration depends on another of its own constraints) but reversing it makes
-- the pair readable side by side.
ALTER TABLE document_links    DROP CONSTRAINT IF EXISTS document_links_id_household_id_key;
ALTER TABLE guide_revisions   DROP CONSTRAINT IF EXISTS guide_revisions_id_household_id_key;
ALTER TABLE memberships       DROP CONSTRAINT IF EXISTS memberships_id_household_id_key;
ALTER TABLE rooms             DROP CONSTRAINT IF EXISTS rooms_id_household_id_key;
ALTER TABLE vehicles          DROP CONSTRAINT IF EXISTS vehicles_id_household_id_key;
ALTER TABLE pets              DROP CONSTRAINT IF EXISTS pets_id_household_id_key;
ALTER TABLE notes             DROP CONSTRAINT IF EXISTS notes_id_household_id_key;
ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_id_household_id_key;
ALTER TABLE loans             DROP CONSTRAINT IF EXISTS loans_id_household_id_key;
ALTER TABLE calendars         DROP CONSTRAINT IF EXISTS calendars_id_household_id_key;
ALTER TABLE bills             DROP CONSTRAINT IF EXISTS bills_id_household_id_key;
ALTER TABLE assets            DROP CONSTRAINT IF EXISTS assets_id_household_id_key;

COMMIT;
