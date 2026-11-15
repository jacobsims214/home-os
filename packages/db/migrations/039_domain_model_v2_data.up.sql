-- 039_domain_model_v2_data.up.sql
--
-- Domain model v2, step 2 of 3 (KB decisions/domain-model-v2.md). Moves
-- existing rows onto the columns and rules 038 added. Idempotent: every
-- statement only touches rows that are not migrated yet, so it is safe to run
-- again (040/#4601 may re-run it to catch rows written by pre-v2 code).
--
-- Legacy columns are left in place and still populated (bills/loans
-- entity_type/entity_id, vendors.property_id); 040 drops them.
--
-- After the data is repaired, the polymorphic "about" columns of notes,
-- files, secrets and document_links become entity_kind, and invitations.role
-- becomes membership_role.
--
-- Not reversible by 039's down (by design): repairs of rows that pointed at
-- another family or at nothing, legacy integration links converted to 'url'
-- or deleted, maintenance rows whose extra subject was cleared.

BEGIN;

-- ---------------------------------------------------------------------------
-- rooms: tenant key from the property
-- ---------------------------------------------------------------------------
UPDATE rooms r SET household_id = p.household_id
  FROM properties p
 WHERE p.id = r.property_id AND r.household_id IS DISTINCT FROM p.household_id;

-- ---------------------------------------------------------------------------
-- assets: drop references to another family, then follow the room
-- ---------------------------------------------------------------------------
UPDATE assets a
   SET room_id = CASE WHEN EXISTS (SELECT 1 FROM rooms r JOIN properties p ON p.id = r.property_id
                                    WHERE r.id = a.room_id AND p.household_id = a.household_id)
                      THEN a.room_id END,
       property_id = CASE WHEN EXISTS (SELECT 1 FROM properties p
                                        WHERE p.id = a.property_id AND p.household_id = a.household_id)
                          THEN a.property_id END
 WHERE (a.room_id IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM rooms r JOIN properties p ON p.id = r.property_id
             WHERE r.id = a.room_id AND p.household_id = a.household_id))
    OR (a.property_id IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM properties p WHERE p.id = a.property_id AND p.household_id = a.household_id));

UPDATE assets a SET property_id = r.property_id
  FROM rooms r
 WHERE r.id = a.room_id AND a.property_id IS DISTINCT FROM r.property_id;

-- ---------------------------------------------------------------------------
-- Assets without a property, where a maintenance row (or a legacy asset bill/
-- loan) about them names one: the asset gets that property, so deriving the
-- property from the asset below loses nothing.
-- ---------------------------------------------------------------------------
UPDATE assets a SET property_id = src.property_id
  FROM (
    SELECT DISTINCT ON (asset_id) asset_id, property_id, household_id FROM (
        SELECT asset_id, property_id, household_id FROM maintenance_tasks
         WHERE asset_id IS NOT NULL AND property_id IS NOT NULL AND vehicle_id IS NULL
        UNION ALL
        SELECT asset_id, property_id, household_id FROM maintenance_schedules
         WHERE asset_id IS NOT NULL AND property_id IS NOT NULL AND vehicle_id IS NULL
        UNION ALL
        SELECT entity_id, property_id, household_id FROM bills
         WHERE entity_type = 'asset' AND entity_id IS NOT NULL AND property_id IS NOT NULL
        UNION ALL
        SELECT entity_id, property_id, household_id FROM loans
         WHERE entity_type = 'asset' AND entity_id IS NOT NULL AND property_id IS NOT NULL
    ) c
    ORDER BY asset_id, property_id
  ) src
 WHERE a.id = src.asset_id
   AND a.property_id IS NULL AND a.room_id IS NULL
   AND a.household_id = src.household_id
   AND EXISTS (SELECT 1 FROM properties p WHERE p.id = src.property_id AND p.household_id = a.household_id);

-- ---------------------------------------------------------------------------
-- maintenance: drop references to another family; a vehicle excludes the
-- other subjects; property follows the asset.
-- ---------------------------------------------------------------------------
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

-- ---------------------------------------------------------------------------
-- bills and loans: polymorphic entity_type/entity_id -> typed subject FKs.
-- A column is set only when the target exists in the same family. Rows with
-- the 020 default ('property', NULL) are ignored.
-- ---------------------------------------------------------------------------
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

-- Links that could not become a typed subject (a pet, another family's row,
-- a deleted row) are kept as a note on the bill/loan, so nothing is lost.
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

-- Property follows the asset for bills and loans too.
UPDATE bills b SET property_id = a.property_id
  FROM assets a WHERE a.id = b.asset_id AND b.property_id IS DISTINCT FROM a.property_id;
UPDATE loans l SET property_id = a.property_id
  FROM assets a WHERE a.id = l.asset_id AND l.property_id IS DISTINCT FROM a.property_id;

-- ---------------------------------------------------------------------------
-- vendors: the property a vendor served survives as a note (column dropped in 040)
-- ---------------------------------------------------------------------------
INSERT INTO notes (household_id, entity_type, entity_id, title, body)
SELECT v.household_id, 'vendor', v.id, 'Serves property', p.name
  FROM vendors v
  JOIN properties p ON p.id = v.property_id AND p.household_id = v.household_id
 WHERE NOT EXISTS (SELECT 1 FROM notes n WHERE n.entity_type = 'vendor' AND n.entity_id = v.id
                                         AND n.title = 'Serves property');

-- ---------------------------------------------------------------------------
-- calendar_objects: one name for maintenance tasks. (The column becomes
-- entity_kind in 040, with the calendar repo.go fix.)
-- ---------------------------------------------------------------------------
UPDATE calendar_objects SET entity_type = 'maintenance_task' WHERE entity_type = 'maintenance';

-- ---------------------------------------------------------------------------
-- notes, files, secrets: family-level rows point at the family; rows that
-- point at another family or at nothing are re-homed to their own family.
-- ---------------------------------------------------------------------------
DO $$
DECLARE
    n_notes   BIGINT;
    n_files   BIGINT;
    n_secrets BIGINT;
BEGIN
    UPDATE notes SET entity_type = 'household', entity_id = household_id, updated_at = NOW()
     WHERE entity_household(entity_type, entity_id) IS DISTINCT FROM household_id;
    GET DIAGNOSTICS n_notes = ROW_COUNT;

    UPDATE files SET entity_type = 'household', entity_id = household_id
     WHERE entity_type IS NULL OR entity_id IS NULL;
    UPDATE files SET entity_type = 'household', entity_id = household_id, updated_at = NOW()
     WHERE entity_household(entity_type, entity_id) IS DISTINCT FROM household_id;
    GET DIAGNOSTICS n_files = ROW_COUNT;

    UPDATE secrets SET entity_type = 'household', entity_id = household_id
     WHERE entity_type IS NULL OR entity_id IS NULL;
    UPDATE secrets SET entity_type = 'household', entity_id = household_id, updated_at = NOW()
     WHERE entity_household(entity_type, entity_id) IS DISTINCT FROM household_id;
    GET DIAGNOSTICS n_secrets = ROW_COUNT;

    RAISE NOTICE '039: re-homed to family level after a failed owner check: % notes, % files, % secrets',
        n_notes, n_files, n_secrets;
END
$$;

-- ---------------------------------------------------------------------------
-- document_links: tenant key, orphans removed, legacy integration links
-- ---------------------------------------------------------------------------
DELETE FROM document_links WHERE entity_household(entity_type, entity_id) IS NULL;

-- Paperless, Vaultwarden and MinIO are gone: keep the URL, else drop the row.
DELETE FROM document_links
 WHERE link_type IN ('paperless', 'vaultwarden', 'minio') AND url IS NULL;
UPDATE document_links SET link_type = 'url'
 WHERE link_type IN ('paperless', 'vaultwarden', 'minio');

-- file/secret links must name a file/secret of the same family.
DELETE FROM document_links d
 WHERE d.link_type IN ('file', 'secret')
   AND (CASE WHEN d.link_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
             THEN entity_household(d.link_type, d.link_id::uuid) END)
       IS DISTINCT FROM entity_household(d.entity_type, d.entity_id);

UPDATE document_links SET household_id = entity_household(entity_type, entity_id)
 WHERE household_id IS DISTINCT FROM entity_household(entity_type, entity_id);

-- ---------------------------------------------------------------------------
-- Types: every remaining value is a registered kind.
-- ---------------------------------------------------------------------------
-- A trigger with UPDATE OF entity_type pins the column type, so the owner
-- triggers (same definitions as 038) are dropped and recreated around it.
DROP TRIGGER notes_enforce_entity_owner ON notes;
DROP TRIGGER files_enforce_entity_owner ON files;
DROP TRIGGER secrets_enforce_entity_owner ON secrets;
DROP TRIGGER document_links_enforce_entity_owner ON document_links;

ALTER TABLE notes          ALTER COLUMN entity_type TYPE entity_kind;
ALTER TABLE files          ALTER COLUMN entity_type TYPE entity_kind;
ALTER TABLE secrets        ALTER COLUMN entity_type TYPE entity_kind;
ALTER TABLE document_links ALTER COLUMN entity_type TYPE entity_kind;

-- invitations.role: free text -> membership_role; anything else is a family member.
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

ALTER TABLE invitations ALTER COLUMN role DROP DEFAULT;
ALTER TABLE invitations ALTER COLUMN role TYPE membership_role USING (
    CASE WHEN role IN ('owner', 'family_manager', 'family_member', 'viewer', 'housesitter', 'vendor')
         THEN role::membership_role
         ELSE 'family_member'::membership_role END);
ALTER TABLE invitations ALTER COLUMN role SET DEFAULT 'family_member';

COMMIT;
