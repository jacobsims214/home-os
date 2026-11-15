-- 056_domain_v2_cleanup.down.sql (#4601)
--
-- Recreates what 056 up dropped and removes what it added. Dropped columns
-- are refilled from the typed FKs and notes:
--   * bills/loans entity_type/entity_id from vehicle_id, else asset_id, else
--     property_id (a bill with no subject gets 020's default 'property' with
--     a NULL entity_id; a loan gets NULLs). Links 039 turned into
--     "Earlier link" notes (a pet, another family's row) stay notes.
--   * vendors.property_id from the vendor's "Serves property" note, matched
--     to a property of the same family by name.
--   * document_links.document_id comes back empty (dead since 024).
--   * integrations comes back empty: the rows (encrypted config) are gone.
--   * calendar_alarm notifications get their dedupe key back in entity_id
--     with entity_type 'calendar_alarm'; the event link is lost.
--
-- Indexes that 056 only repeated with IF NOT EXISTS (idx_loans_property,
-- idx_loans_vehicle, idx_bills_vehicle, idx_bills_asset, idx_rooms_household,
-- idx_document_links_household) belong to 038 and are left in place.

BEGIN;

-- 5. notifications
DROP TRIGGER calendar_objects_clear_notifications ON calendar_objects;
DROP FUNCTION calendar_objects_clear_notifications();

UPDATE notifications
   SET entity_type = 'calendar_alarm', entity_id = dedupe_key::uuid
 WHERE type = 'calendar_alarm'
   AND dedupe_key ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';
DROP INDEX IF EXISTS idx_notifications_dedupe;
ALTER TABLE notifications DROP COLUMN dedupe_key;

-- 4. constraints
ALTER TABLE maintenance_schedules DROP CONSTRAINT maintenance_schedules_subject_check;
ALTER TABLE maintenance_tasks DROP CONSTRAINT maintenance_tasks_subject_check;

ALTER TABLE document_links DROP CONSTRAINT document_links_link_type_check;
ALTER TABLE document_links ALTER COLUMN link_type SET DEFAULT 'paperless';
ALTER TABLE document_links ALTER COLUMN household_id DROP NOT NULL;

ALTER TABLE secrets ALTER COLUMN entity_type DROP NOT NULL, ALTER COLUMN entity_id DROP NOT NULL;
ALTER TABLE files   ALTER COLUMN entity_type DROP NOT NULL, ALTER COLUMN entity_id DROP NOT NULL;
DROP TRIGGER secrets_default_entity ON secrets;
DROP TRIGGER files_default_entity ON files;
DROP FUNCTION default_entity_to_household();

ALTER TABLE rooms ALTER COLUMN household_id DROP NOT NULL;

-- 3. integrations (as 014 created it; empty)
CREATE TYPE integration_type AS ENUM ('paperless', 'vaultwarden', 'homeassistant', 'minio');
CREATE TYPE integration_status AS ENUM ('connected', 'disconnected', 'error');

CREATE TABLE integrations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    type integration_type NOT NULL,
    config BYTEA,
    status integration_status NOT NULL DEFAULT 'disconnected',
    last_health_check TIMESTAMPTZ,
    last_sync TIMESTAMPTZ,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(household_id, type)
);

-- 2. dropped columns
ALTER TABLE document_links ADD COLUMN document_id UUID;

ALTER TABLE vendors ADD COLUMN property_id UUID;
ALTER TABLE vendors ADD CONSTRAINT vendors_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id) ON DELETE SET NULL;
UPDATE vendors v SET property_id = (
    SELECT p.id
      FROM notes n
      JOIN properties p ON p.household_id = v.household_id AND p.name = n.body
     WHERE n.entity_type = 'vendor' AND n.entity_id = v.id AND n.title = 'Serves property'
     ORDER BY p.created_at, p.id
     LIMIT 1)
 WHERE EXISTS (SELECT 1 FROM notes n
                WHERE n.entity_type = 'vendor' AND n.entity_id = v.id AND n.title = 'Serves property');

ALTER TABLE loans ADD COLUMN entity_type VARCHAR(50), ADD COLUMN entity_id UUID;
UPDATE loans
   SET entity_type = CASE WHEN vehicle_id IS NOT NULL THEN 'vehicle'
                          WHEN asset_id   IS NOT NULL THEN 'asset'
                          ELSE 'property' END,
       entity_id = COALESCE(vehicle_id, asset_id, property_id)
 WHERE COALESCE(vehicle_id, asset_id, property_id) IS NOT NULL;
CREATE INDEX idx_loans_entity ON loans (entity_type, entity_id);

ALTER TABLE bills ADD COLUMN entity_type VARCHAR(50) DEFAULT 'property', ADD COLUMN entity_id UUID;
UPDATE bills
   SET entity_type = CASE WHEN vehicle_id IS NOT NULL THEN 'vehicle'
                          WHEN asset_id   IS NOT NULL THEN 'asset'
                          ELSE 'property' END,
       entity_id = COALESCE(vehicle_id, asset_id, property_id);

COMMIT;
