-- 056_domain_v2_cleanup.up.sql (#4601)
--
-- Domain model v2, step 3 of 3 (KB decisions/domain-model-v2.md). The spec
-- calls this "040_domain_v2_cleanup". It is numbered 056 because
-- golang-migrate applies files in version order on a fresh database, and a
-- new 040 would run before 041-054, which it depends on (041/044/046/049/050
-- extend the tables and functions this file relies on).
--
-- What it does, in order:
--   1. Re-runs the parts of 039's backfill that feed a dropped column or a
--      new constraint, for rows written by pre-v2 code since 039 ran:
--      bills/loans typed subjects (plus the "Earlier link" notes), the
--      vendor "Serves property" notes, maintenance subject repair,
--      document_links repair, family-level files/secrets.
--   2. Drops the superseded columns:
--        bills.entity_type, bills.entity_id
--        loans.entity_type, loans.entity_id (and idx_loans_entity)
--        vendors.property_id
--        document_links.document_id (dead since 024)
--   3. Drops the integrations table and its two enum types (open question 3,
--      approved on #4441). The api package and the UI panel go in the same
--      commit.
--   4. Adds the constraints 038 deferred:
--        rooms.household_id NOT NULL
--        files/secrets entity_type + entity_id NOT NULL. A BEFORE trigger
--          fills ('household', household_id) when both are NULL, the same
--          rule as lists (046), so a caller that leaves them out gets a
--          family-level row instead of an error
--        document_links.household_id NOT NULL; link_type limited to file,
--          secret or url (the 'paperless' default is dropped)
--        maintenance_tasks/schedules subject CHECK: a vehicle excludes
--          property and asset (api #4449 and mcp already enforce it)
--   5. notifications.dedupe_key. The worker's calendar_alarm rows stored a
--      per-firing dedupe UUID in entity_id with entity_type 'calendar_alarm'.
--      The key moves to dedupe_key, unique per (user_id, dedupe_key), and
--      the entity becomes the event: ('calendar_event', calendar object id),
--      the same reference 054's item_changed rows use. Existing alarm rows
--      keep their key; their event cannot be recovered from the hash, so
--      their entity becomes NULL. A calendar object's delete now clears
--      notification references to it, the same as rehome_entity_refs()
--      does for registered kinds.
--
-- NOT done here, on purpose. Code still reads these, so they are left for a
-- later ticket:
--   * pets.vet_name / vet_phone: api pet, mcp pet (#4378 fallback),
--     seed/demo.go and the ui still read and write them. 043's vet backfill
--     is therefore not re-run.
--   * properties mortgage_ / property_tax_ / insurance_ / hoa_fee_ columns
--     and vehicles lender / loan_ / registration_ / insurance_ columns: api
--     property/vehicle/seed, mcp property/vehicle/finance/loan/search and
--     the ui still use them. 044 section 4 is therefore not re-run. A later
--     drop must copy with the typed FKs only, because 044's helpers read the
--     bills/loans entity_type columns dropped here.
--   * notifications.entity_type and calendar_objects.entity_type stay
--     VARCHAR, not entity_kind. Notifications use the activity vocabulary
--     (054 writes 'calendar_event', 'calendar', 'list_item',
--     'document_link', 'meal_feedback', 'person_preference'). The api
--     calendar repo and 038's functions still compare calendar_objects with
--     the legacy 'maintenance' literal, which an entity_kind column would
--     reject at runtime.
--
-- Down (056_domain_v2_cleanup.down.sql) recreates every dropped column and
-- table and refills the columns from the typed FKs and notes.
--
-- Runs in one transaction (golang-migrate does not wrap files).

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Re-run 039's backfill for rows written since (idempotent)
-- ---------------------------------------------------------------------------

UPDATE rooms r SET household_id = p.household_id
  FROM properties p
 WHERE p.id = r.property_id AND r.household_id IS DISTINCT FROM p.household_id;

-- bills / loans: polymorphic entity_type/entity_id -> typed subject FKs.
UPDATE bills b SET property_id = NULL
 WHERE b.property_id IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM properties p WHERE p.id = b.property_id AND p.household_id = b.household_id);

UPDATE bills b SET property_id = b.entity_id
 WHERE b.entity_type = 'property' AND b.entity_id IS NOT NULL
   AND b.property_id IS NULL AND b.vehicle_id IS NULL AND b.asset_id IS NULL
   AND EXISTS (SELECT 1 FROM properties p WHERE p.id = b.entity_id AND p.household_id = b.household_id);

UPDATE bills b SET vehicle_id = b.entity_id, property_id = NULL, asset_id = NULL
 WHERE b.entity_type = 'vehicle' AND b.entity_id IS NOT NULL AND b.vehicle_id IS NULL
   AND EXISTS (SELECT 1 FROM vehicles v WHERE v.id = b.entity_id AND v.household_id = b.household_id);

UPDATE bills b SET asset_id = b.entity_id
 WHERE b.entity_type = 'asset' AND b.entity_id IS NOT NULL
   AND b.asset_id IS NULL AND b.vehicle_id IS NULL
   AND EXISTS (SELECT 1 FROM assets a WHERE a.id = b.entity_id AND a.household_id = b.household_id);

UPDATE loans l SET property_id = l.entity_id
 WHERE l.entity_type = 'property' AND l.entity_id IS NOT NULL
   AND l.property_id IS NULL AND l.vehicle_id IS NULL AND l.asset_id IS NULL
   AND EXISTS (SELECT 1 FROM properties p WHERE p.id = l.entity_id AND p.household_id = l.household_id);

UPDATE loans l SET vehicle_id = l.entity_id, property_id = NULL, asset_id = NULL
 WHERE l.entity_type = 'vehicle' AND l.entity_id IS NOT NULL AND l.vehicle_id IS NULL
   AND EXISTS (SELECT 1 FROM vehicles v WHERE v.id = l.entity_id AND v.household_id = l.household_id);

UPDATE loans l SET asset_id = l.entity_id
 WHERE l.entity_type = 'asset' AND l.entity_id IS NOT NULL
   AND l.asset_id IS NULL AND l.vehicle_id IS NULL
   AND EXISTS (SELECT 1 FROM assets a WHERE a.id = l.entity_id AND a.household_id = l.household_id);

-- Links that could not become a typed subject survive as a note.
INSERT INTO notes (household_id, entity_type, entity_id, title, body)
SELECT b.household_id, 'bill', b.id, 'Earlier link (migration 039)',
       format('This bill was linked to %s %s. Domain model v2 keeps only property, vehicle or asset subjects of the same family, so the link was kept as this note.',
              b.entity_type, b.entity_id)
  FROM bills b
 WHERE b.entity_id IS NOT NULL
   AND NOT ((b.entity_type = 'property' AND b.property_id IS NOT DISTINCT FROM b.entity_id)
         OR (b.entity_type = 'vehicle'  AND b.vehicle_id  IS NOT DISTINCT FROM b.entity_id)
         OR (b.entity_type = 'asset'    AND b.asset_id    IS NOT DISTINCT FROM b.entity_id))
   AND NOT EXISTS (SELECT 1 FROM notes n WHERE n.entity_type = 'bill' AND n.entity_id = b.id
                                           AND n.title = 'Earlier link (migration 039)');

INSERT INTO notes (household_id, entity_type, entity_id, title, body)
SELECT l.household_id, 'loan', l.id, 'Earlier link (migration 039)',
       format('This loan was linked to %s %s. Domain model v2 keeps only property, vehicle or asset subjects of the same family, so the link was kept as this note.',
              l.entity_type, l.entity_id)
  FROM loans l
 WHERE l.entity_id IS NOT NULL
   AND NOT ((l.entity_type = 'property' AND l.property_id IS NOT DISTINCT FROM l.entity_id)
         OR (l.entity_type = 'vehicle'  AND l.vehicle_id  IS NOT DISTINCT FROM l.entity_id)
         OR (l.entity_type = 'asset'    AND l.asset_id    IS NOT DISTINCT FROM l.entity_id))
   AND NOT EXISTS (SELECT 1 FROM notes n WHERE n.entity_type = 'loan' AND n.entity_id = l.id
                                           AND n.title = 'Earlier link (migration 039)');

UPDATE bills b SET property_id = a.property_id
  FROM assets a WHERE a.id = b.asset_id AND b.property_id IS DISTINCT FROM a.property_id;
UPDATE loans l SET property_id = a.property_id
  FROM assets a WHERE a.id = l.asset_id AND l.property_id IS DISTINCT FROM a.property_id;

-- vendors: the property a vendor served survives as a note.
INSERT INTO notes (household_id, entity_type, entity_id, title, body)
SELECT v.household_id, 'vendor', v.id, 'Serves property', p.name
  FROM vendors v
  JOIN properties p ON p.id = v.property_id AND p.household_id = v.household_id
 WHERE NOT EXISTS (SELECT 1 FROM notes n WHERE n.entity_type = 'vendor' AND n.entity_id = v.id
                                         AND n.title = 'Serves property');

-- maintenance: no other family's subject; a vehicle excludes the others;
-- property follows the asset.
UPDATE maintenance_tasks m
   SET property_id = CASE WHEN EXISTS (SELECT 1 FROM properties x WHERE x.id = m.property_id AND x.household_id = m.household_id) THEN m.property_id END,
       asset_id    = CASE WHEN EXISTS (SELECT 1 FROM assets x     WHERE x.id = m.asset_id    AND x.household_id = m.household_id) THEN m.asset_id END,
       vehicle_id  = CASE WHEN EXISTS (SELECT 1 FROM vehicles x   WHERE x.id = m.vehicle_id  AND x.household_id = m.household_id) THEN m.vehicle_id END
 WHERE (m.property_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM properties x WHERE x.id = m.property_id AND x.household_id = m.household_id))
    OR (m.asset_id    IS NOT NULL AND NOT EXISTS (SELECT 1 FROM assets x     WHERE x.id = m.asset_id    AND x.household_id = m.household_id))
    OR (m.vehicle_id  IS NOT NULL AND NOT EXISTS (SELECT 1 FROM vehicles x   WHERE x.id = m.vehicle_id  AND x.household_id = m.household_id));

UPDATE maintenance_schedules m
   SET property_id = CASE WHEN EXISTS (SELECT 1 FROM properties x WHERE x.id = m.property_id AND x.household_id = m.household_id) THEN m.property_id END,
       asset_id    = CASE WHEN EXISTS (SELECT 1 FROM assets x     WHERE x.id = m.asset_id    AND x.household_id = m.household_id) THEN m.asset_id END,
       vehicle_id  = CASE WHEN EXISTS (SELECT 1 FROM vehicles x   WHERE x.id = m.vehicle_id  AND x.household_id = m.household_id) THEN m.vehicle_id END
 WHERE (m.property_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM properties x WHERE x.id = m.property_id AND x.household_id = m.household_id))
    OR (m.asset_id    IS NOT NULL AND NOT EXISTS (SELECT 1 FROM assets x     WHERE x.id = m.asset_id    AND x.household_id = m.household_id))
    OR (m.vehicle_id  IS NOT NULL AND NOT EXISTS (SELECT 1 FROM vehicles x   WHERE x.id = m.vehicle_id  AND x.household_id = m.household_id));

UPDATE maintenance_tasks SET property_id = NULL, asset_id = NULL
 WHERE vehicle_id IS NOT NULL AND (property_id IS NOT NULL OR asset_id IS NOT NULL);
UPDATE maintenance_schedules SET property_id = NULL, asset_id = NULL
 WHERE vehicle_id IS NOT NULL AND (property_id IS NOT NULL OR asset_id IS NOT NULL);

UPDATE maintenance_tasks m SET property_id = a.property_id
  FROM assets a WHERE a.id = m.asset_id AND m.property_id IS DISTINCT FROM a.property_id;
UPDATE maintenance_schedules m SET property_id = a.property_id
  FROM assets a WHERE a.id = m.asset_id AND m.property_id IS DISTINCT FROM a.property_id;

-- files / secrets: a NULL entity means the family.
UPDATE files   SET entity_type = 'household', entity_id = household_id
 WHERE entity_type IS NULL OR entity_id IS NULL;
UPDATE secrets SET entity_type = 'household', entity_id = household_id
 WHERE entity_type IS NULL OR entity_id IS NULL;

-- document_links: orphans, legacy integration links, bad file/secret
-- targets, tenant key.
DELETE FROM document_links WHERE entity_household(entity_type, entity_id) IS NULL;
DELETE FROM document_links
 WHERE link_type IN ('paperless', 'vaultwarden', 'minio') AND url IS NULL;
UPDATE document_links SET link_type = 'url'
 WHERE link_type IN ('paperless', 'vaultwarden', 'minio');
-- Anything else outside the new set keeps its URL as a url link, else goes.
DELETE FROM document_links
 WHERE link_type NOT IN ('file', 'secret', 'url') AND url IS NULL;
UPDATE document_links SET link_type = 'url'
 WHERE link_type NOT IN ('file', 'secret', 'url');
DELETE FROM document_links d
 WHERE d.link_type IN ('file', 'secret')
   AND (CASE WHEN d.link_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
             THEN entity_household(d.link_type, d.link_id::uuid) END)
       IS DISTINCT FROM entity_household(d.entity_type, d.entity_id);
UPDATE document_links SET household_id = entity_household(entity_type, entity_id)
 WHERE household_id IS DISTINCT FROM entity_household(entity_type, entity_id);

-- ---------------------------------------------------------------------------
-- 2. Drop the superseded columns
-- ---------------------------------------------------------------------------

ALTER TABLE bills DROP COLUMN entity_type, DROP COLUMN entity_id;

DROP INDEX IF EXISTS idx_loans_entity;
ALTER TABLE loans DROP COLUMN entity_type, DROP COLUMN entity_id;
-- 038 created these; repeated so the subject queries are served either way.
CREATE INDEX IF NOT EXISTS idx_loans_property ON loans (property_id);
CREATE INDEX IF NOT EXISTS idx_loans_vehicle  ON loans (vehicle_id);
CREATE INDEX IF NOT EXISTS idx_bills_vehicle  ON bills (vehicle_id);
CREATE INDEX IF NOT EXISTS idx_bills_asset    ON bills (asset_id);

ALTER TABLE vendors DROP COLUMN property_id;

ALTER TABLE document_links DROP COLUMN document_id;

-- ---------------------------------------------------------------------------
-- 3. integrations (external integrations are gone; #4441)
-- ---------------------------------------------------------------------------

DROP TABLE integrations;
DROP TYPE integration_status;
DROP TYPE integration_type;

-- ---------------------------------------------------------------------------
-- 4. Constraints 038 deferred
-- ---------------------------------------------------------------------------

ALTER TABLE rooms ALTER COLUMN household_id SET NOT NULL;
CREATE INDEX IF NOT EXISTS idx_rooms_household ON rooms (household_id);

-- files / secrets: every row is about exactly one thing. Named so they sort
-- (and so fire) before <table>_enforce_entity_owner.
CREATE FUNCTION default_entity_to_household()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.entity_type IS NULL AND NEW.entity_id IS NULL THEN
        NEW.entity_type := 'household';
        NEW.entity_id := NEW.household_id;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER files_default_entity
    BEFORE INSERT OR UPDATE OF entity_type, entity_id ON files
    FOR EACH ROW EXECUTE FUNCTION default_entity_to_household();
CREATE TRIGGER secrets_default_entity
    BEFORE INSERT OR UPDATE OF entity_type, entity_id ON secrets
    FOR EACH ROW EXECUTE FUNCTION default_entity_to_household();

ALTER TABLE files   ALTER COLUMN entity_type SET NOT NULL, ALTER COLUMN entity_id SET NOT NULL;
ALTER TABLE secrets ALTER COLUMN entity_type SET NOT NULL, ALTER COLUMN entity_id SET NOT NULL;

ALTER TABLE document_links ALTER COLUMN household_id SET NOT NULL;
ALTER TABLE document_links ALTER COLUMN link_type DROP DEFAULT;
ALTER TABLE document_links ADD CONSTRAINT document_links_link_type_check
    CHECK (link_type IN ('file', 'secret', 'url'));
CREATE INDEX IF NOT EXISTS idx_document_links_household ON document_links (household_id);

ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_subject_check
    CHECK (vehicle_id IS NULL OR (property_id IS NULL AND asset_id IS NULL));
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_subject_check
    CHECK (vehicle_id IS NULL OR (property_id IS NULL AND asset_id IS NULL));

-- ---------------------------------------------------------------------------
-- 5. notifications.dedupe_key; alarm rows point at their event
-- ---------------------------------------------------------------------------

ALTER TABLE notifications ADD COLUMN dedupe_key TEXT
    CONSTRAINT notifications_dedupe_key_check CHECK (btrim(dedupe_key) <> '');

UPDATE notifications
   SET dedupe_key = entity_id::text, entity_type = NULL, entity_id = NULL
 WHERE entity_type = 'calendar_alarm' AND entity_id IS NOT NULL;
UPDATE notifications SET entity_type = NULL
 WHERE entity_type = 'calendar_alarm';

-- One notification per member per firing. The worker inserts with
-- ON CONFLICT (user_id, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING.
CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_dedupe
    ON notifications (user_id, dedupe_key) WHERE dedupe_key IS NOT NULL;

-- A deleted event leaves no dangling notification reference (calendar
-- objects are not a registered entity kind, so rehome_entity_refs() does not
-- cover them). Skipped while the household itself is being deleted.
CREATE FUNCTION calendar_objects_clear_notifications()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    UPDATE notifications n SET entity_type = NULL, entity_id = NULL
     WHERE n.entity_type = 'calendar_event' AND n.entity_id = OLD.id
       AND EXISTS (SELECT 1 FROM households h WHERE h.id = n.household_id);
    RETURN NULL;
END;
$$;

CREATE TRIGGER calendar_objects_clear_notifications
    AFTER DELETE ON calendar_objects
    FOR EACH ROW EXECUTE FUNCTION calendar_objects_clear_notifications();

COMMIT;
