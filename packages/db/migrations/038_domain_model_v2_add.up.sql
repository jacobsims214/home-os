-- 038_domain_model_v2_add.up.sql
--
-- Domain model v2, step 1 of 3 (KB decisions/domain-model-v2.md, approved
-- 2026-11-15). Purely additive: every query the current api/mcp/worker/
-- calendar code runs keeps working. 039 moves the data; 040 (#4601) drops the
-- legacy columns and adds the NOT NULL / CHECK constraints together with the
-- code that stops reading them.
--
-- What this adds:
--   1. entity_kind domain + entity_household(kind, id): one registry of the
--      things a note/file/secret/link can be about, and who owns each one.
--   2. enforce_entity_owner(): notes, files, secrets and document_links may
--      only point at a row of their own family.
--   3. rehome_entity_refs(): when an entity is deleted, files/secrets about it
--      move to the family, notes/links about it are deleted, calendar events
--      and notifications lose the reference.
--   4. Typed subject FKs on bills and loans (property/vehicle/asset) and
--      derive_subject_property(): property_id follows the asset.
--   5. Location hierarchy: rooms.household_id, assets.property_id follows the
--      room, property delete cascades to its assets.
--   6. FK delete rules from the doc's ownership table.
--
-- Column type changes to entity_kind are in 039, after the data is repaired,
-- so 038 can never fail on existing rows. calendar_objects and notifications
-- keep VARCHAR until 040 (code still writes 'maintenance' and
-- 'calendar_alarm' there).
--
-- Runs in one transaction so a failure leaves the database at 037.
--
-- Errors raised by the triggers use SQLSTATE 23503 (foreign_key_violation):
-- to a caller, a row of another family is the same as a row that is missing.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Entity registry
-- ---------------------------------------------------------------------------

CREATE DOMAIN entity_kind AS VARCHAR(50)
    CONSTRAINT entity_kind_check CHECK (VALUE IN (
        'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
        'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
        'file', 'note', 'secret'
    ));

-- entity_household returns the household that owns (kind, id), or NULL when
-- the kind is unknown or the row does not exist. Stage 2 migrations that add
-- an entity table add a branch here (and to entity_kind).
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

-- ---------------------------------------------------------------------------
-- 2. Ownership check for polymorphic "about" rows
-- ---------------------------------------------------------------------------

-- enforce_entity_owner: (entity_type, entity_id) must name a row owned by
-- NEW.household_id. Family-level rows use ('household', household_id).
--
-- Until 040 sets NOT NULL, files and secrets may still have a NULL entity
-- (the code writes NULL for "not attached"); those rows are allowed here.
--
-- document_links has no household_id in today's code paths, so for that table
-- a NULL household_id is filled in from the entity; links of type 'file' or
-- 'secret' must also point at a file/secret of the same family.
CREATE OR REPLACE FUNCTION enforce_entity_owner()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_owner UUID;
    v_link  UUID;
BEGIN
    IF NEW.entity_type IS NULL AND NEW.entity_id IS NULL AND TG_TABLE_NAME IN ('files', 'secrets') THEN
        RETURN NEW;
    END IF;

    v_owner := entity_household(NEW.entity_type, NEW.entity_id);

    IF TG_TABLE_NAME = 'document_links' AND NEW.household_id IS NULL THEN
        NEW.household_id := v_owner;
    END IF;

    IF v_owner IS NULL OR v_owner IS DISTINCT FROM NEW.household_id THEN
        RAISE EXCEPTION '%: % % does not exist in this family', TG_TABLE_NAME, NEW.entity_type, NEW.entity_id
            USING ERRCODE = 'foreign_key_violation',
                  HINT = 'entity_type must be a known kind and entity_id a row of the same household; use (''household'', household_id) for family-level rows';
    END IF;

    -- Nested IFs: NEW.link_type only exists on document_links, and SQL AND
    -- does not short-circuit.
    IF TG_TABLE_NAME <> 'document_links' THEN
        RETURN NEW;
    END IF;
    IF NEW.link_type IN ('file', 'secret') THEN
        IF NEW.link_id IS NULL OR NEW.link_id !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
            RAISE EXCEPTION 'document_links: link_id must be the id of a % in this family', NEW.link_type
                USING ERRCODE = 'foreign_key_violation';
        END IF;
        v_link := entity_household(NEW.link_type, NEW.link_id::uuid);
        IF v_link IS DISTINCT FROM NEW.household_id THEN
            RAISE EXCEPTION 'document_links: % % does not exist in this family', NEW.link_type, NEW.link_id
                USING ERRCODE = 'foreign_key_violation';
        END IF;
    END IF;

    RETURN NEW;
END;
$$;

-- document_links gets a tenant key; backfilled in 039, NOT NULL in 040.
ALTER TABLE document_links
    ADD COLUMN IF NOT EXISTS household_id UUID REFERENCES households(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_document_links_household ON document_links (household_id);

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

-- ---------------------------------------------------------------------------
-- 3. Clean up what pointed at a deleted entity
-- ---------------------------------------------------------------------------

-- rehome_entity_refs(kind): AFTER DELETE on every registered entity table.
--   files, secrets        -> re-pointed to ('household', household_id)
--   notes, document_links -> deleted
--   calendar_objects      -> entity reference cleared (the event stays),
--                            except a maintenance task's own event, deleted
--   notifications         -> entity reference cleared
-- When the whole family is being deleted the households row is already gone;
-- files/secrets are then left for the household cascade to delete.
CREATE OR REPLACE FUNCTION rehome_entity_refs()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_kind TEXT := TG_ARGV[0];
BEGIN
    UPDATE files f SET entity_type = 'household', entity_id = f.household_id, updated_at = NOW()
     WHERE f.entity_type = v_kind AND f.entity_id = OLD.id
       AND EXISTS (SELECT 1 FROM households h WHERE h.id = f.household_id);

    UPDATE secrets s SET entity_type = 'household', entity_id = s.household_id, updated_at = NOW()
     WHERE s.entity_type = v_kind AND s.entity_id = OLD.id
       AND EXISTS (SELECT 1 FROM households h WHERE h.id = s.household_id);

    DELETE FROM notes WHERE entity_type = v_kind AND entity_id = OLD.id;
    DELETE FROM document_links WHERE entity_type = v_kind AND entity_id = OLD.id;

    -- A maintenance task and its calendar event are one thing (027 deletes
    -- the task with its event; the api deletes the event with its task), so
    -- the event goes too. Until 040 calendar code writes 'maintenance'.
    IF v_kind = 'maintenance_task' THEN
        DELETE FROM calendar_objects
         WHERE entity_id = OLD.id AND entity_type IN ('maintenance', 'maintenance_task');
    ELSE
        UPDATE calendar_objects SET entity_type = NULL, entity_id = NULL, updated_at = NOW()
         WHERE entity_type = v_kind AND entity_id = OLD.id;
    END IF;

    UPDATE notifications SET entity_type = NULL, entity_id = NULL
     WHERE entity_type = v_kind AND entity_id = OLD.id;

    RETURN NULL;
END;
$$;

CREATE TRIGGER properties_rehome_entity_refs AFTER DELETE ON properties
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('property');
CREATE TRIGGER rooms_rehome_entity_refs AFTER DELETE ON rooms
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('room');
CREATE TRIGGER assets_rehome_entity_refs AFTER DELETE ON assets
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('asset');
CREATE TRIGGER vehicles_rehome_entity_refs AFTER DELETE ON vehicles
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('vehicle');
CREATE TRIGGER pets_rehome_entity_refs AFTER DELETE ON pets
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('pet');
CREATE TRIGGER vendors_rehome_entity_refs AFTER DELETE ON vendors
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('vendor');
CREATE TRIGGER bills_rehome_entity_refs AFTER DELETE ON bills
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('bill');
CREATE TRIGGER loans_rehome_entity_refs AFTER DELETE ON loans
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('loan');
CREATE TRIGGER maintenance_tasks_rehome_entity_refs AFTER DELETE ON maintenance_tasks
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('maintenance_task');
CREATE TRIGGER maintenance_schedules_rehome_entity_refs AFTER DELETE ON maintenance_schedules
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('maintenance_schedule');
CREATE TRIGGER files_rehome_entity_refs AFTER DELETE ON files
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('file');
CREATE TRIGGER notes_rehome_entity_refs AFTER DELETE ON notes
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('note');
CREATE TRIGGER secrets_rehome_entity_refs AFTER DELETE ON secrets
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('secret');

-- A file's blob dies with it (the FK points files -> file_blobs, so nothing
-- deleted blobs before). A blob still referenced by another file is kept.
CREATE OR REPLACE FUNCTION delete_file_blob()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    DELETE FROM file_blobs b
     WHERE b.id = OLD.blob_id
       AND NOT EXISTS (SELECT 1 FROM files f WHERE f.blob_id = OLD.blob_id);
    RETURN NULL;
END;
$$;

CREATE TRIGGER files_delete_blob AFTER DELETE ON files
    FOR EACH ROW EXECUTE FUNCTION delete_file_blob();

-- calendar 027: deleting a maintenance event deletes its task. Match the v2
-- kind as well as the legacy literal the calendar code still writes.
CREATE OR REPLACE FUNCTION delete_maintenance_on_calendar_delete()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.entity_type IN ('maintenance', 'maintenance_task') AND OLD.entity_id IS NOT NULL THEN
        DELETE FROM maintenance_tasks WHERE id = OLD.entity_id;
    END IF;
    RETURN OLD;
END;
$$;

-- 027 fired BEFORE DELETE. Deleting the task now runs rehome_entity_refs,
-- which updates calendar_objects; touching the row whose delete is in
-- progress fails with "tuple to be deleted was already modified". Fired
-- AFTER DELETE, the event row is already gone and the rehome finds nothing.
DROP TRIGGER IF EXISTS maintenance_calendar_sync_trigger ON calendar_objects;
CREATE TRIGGER maintenance_calendar_sync_trigger
    AFTER DELETE ON calendar_objects
    FOR EACH ROW EXECUTE FUNCTION delete_maintenance_on_calendar_delete();

-- ---------------------------------------------------------------------------
-- 4. Location hierarchy: property -> room -> asset
-- ---------------------------------------------------------------------------

ALTER TABLE rooms
    ADD COLUMN IF NOT EXISTS household_id UUID REFERENCES households(id) ON DELETE CASCADE;
ALTER TABLE rooms
    ADD CONSTRAINT rooms_id_property_id_key UNIQUE (id, property_id);
CREATE INDEX IF NOT EXISTS idx_rooms_household ON rooms (household_id);

-- rooms.household_id always equals its property's household. Today's code
-- does not write the column, so it is filled in here.
CREATE OR REPLACE FUNCTION rooms_set_household()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_household UUID;
BEGIN
    SELECT household_id INTO v_household FROM properties WHERE id = NEW.property_id;
    IF v_household IS NULL THEN
        RETURN NEW; -- the property FK reports the missing property
    END IF;
    IF NEW.household_id IS NULL THEN
        NEW.household_id := v_household;
    ELSIF NEW.household_id <> v_household THEN
        RAISE EXCEPTION 'rooms: property % does not exist in this family', NEW.property_id
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER rooms_set_household
    BEFORE INSERT OR UPDATE OF property_id, household_id ON rooms
    FOR EACH ROW EXECUTE FUNCTION rooms_set_household();

-- A property carries its assets; a deleted room leaves the asset in the property.
ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_property_id_fkey;
ALTER TABLE assets ADD CONSTRAINT assets_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id) ON DELETE CASCADE;
ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_room_id_fkey;
ALTER TABLE assets ADD CONSTRAINT assets_room_id_fkey
    FOREIGN KEY (room_id) REFERENCES rooms(id) ON DELETE SET NULL;

-- assets.property_id follows the room, and room/property must be the family's own.
CREATE OR REPLACE FUNCTION assets_derive_property()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_property  UUID;
    v_household UUID;
BEGIN
    IF NEW.room_id IS NOT NULL THEN
        SELECT r.property_id, p.household_id INTO v_property, v_household
          FROM rooms r JOIN properties p ON p.id = r.property_id
         WHERE r.id = NEW.room_id;
        IF v_household IS NOT NULL AND v_household <> NEW.household_id THEN
            RAISE EXCEPTION 'assets: room % does not exist in this family', NEW.room_id
                USING ERRCODE = 'foreign_key_violation';
        END IF;
        IF v_property IS NOT NULL THEN
            NEW.property_id := v_property;
        END IF;
    END IF;

    IF NEW.property_id IS NOT NULL THEN
        SELECT household_id INTO v_household FROM properties WHERE id = NEW.property_id;
        IF v_household IS NOT NULL AND v_household <> NEW.household_id THEN
            RAISE EXCEPTION 'assets: property % does not exist in this family', NEW.property_id
                USING ERRCODE = 'foreign_key_violation';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER assets_derive_property
    BEFORE INSERT OR UPDATE OF room_id, property_id, household_id ON assets
    FOR EACH ROW EXECUTE FUNCTION assets_derive_property();

-- ---------------------------------------------------------------------------
-- 5. Subject columns for bills, loans and maintenance
-- ---------------------------------------------------------------------------

-- bills: typed vehicle/asset subjects; money outlives its subject (SET NULL).
ALTER TABLE bills ADD COLUMN IF NOT EXISTS vehicle_id UUID REFERENCES vehicles(id) ON DELETE SET NULL;
ALTER TABLE bills ADD COLUMN IF NOT EXISTS asset_id   UUID REFERENCES assets(id)   ON DELETE SET NULL;
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_property_id_fkey;
ALTER TABLE bills ADD CONSTRAINT bills_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id) ON DELETE SET NULL;
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_vendor_id_fkey;
ALTER TABLE bills ADD CONSTRAINT bills_vendor_id_fkey
    FOREIGN KEY (vendor_id) REFERENCES vendors(id) ON DELETE SET NULL;
-- vehicle_id is new and nothing writes it yet, so the subject CHECK holds
-- for every existing row and can be enforced now.
ALTER TABLE bills ADD CONSTRAINT bills_subject_check
    CHECK (vehicle_id IS NULL OR (property_id IS NULL AND asset_id IS NULL));
CREATE INDEX IF NOT EXISTS idx_bills_vehicle ON bills (vehicle_id);
CREATE INDEX IF NOT EXISTS idx_bills_asset   ON bills (asset_id);

-- loans: typed subjects replace entity_type/entity_id (dropped in 040).
ALTER TABLE loans ADD COLUMN IF NOT EXISTS property_id UUID REFERENCES properties(id) ON DELETE SET NULL;
ALTER TABLE loans ADD COLUMN IF NOT EXISTS vehicle_id  UUID REFERENCES vehicles(id)   ON DELETE SET NULL;
ALTER TABLE loans ADD COLUMN IF NOT EXISTS asset_id    UUID REFERENCES assets(id)     ON DELETE SET NULL;
ALTER TABLE loans ADD CONSTRAINT loans_subject_check
    CHECK (vehicle_id IS NULL OR (property_id IS NULL AND asset_id IS NULL));
CREATE INDEX IF NOT EXISTS idx_loans_property ON loans (property_id);
CREATE INDEX IF NOT EXISTS idx_loans_vehicle  ON loans (vehicle_id);
CREATE INDEX IF NOT EXISTS idx_loans_asset    ON loans (asset_id);

-- maintenance: upkeep dies with its subject (CASCADE); vendor SET NULL;
-- deleting a schedule keeps its tasks. The subject CHECK for maintenance is
-- added in 040, after 039 has cleaned rows that set a vehicle and a property.
ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_asset_id_fkey;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_asset_id_fkey
    FOREIGN KEY (asset_id) REFERENCES assets(id) ON DELETE CASCADE;
ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_vehicle_id_fkey;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_vehicle_id_fkey
    FOREIGN KEY (vehicle_id) REFERENCES vehicles(id) ON DELETE CASCADE;
ALTER TABLE maintenance_schedules DROP CONSTRAINT IF EXISTS maintenance_schedules_vendor_id_fkey;
ALTER TABLE maintenance_schedules ADD CONSTRAINT maintenance_schedules_vendor_id_fkey
    FOREIGN KEY (vendor_id) REFERENCES vendors(id) ON DELETE SET NULL;

ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_asset_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_asset_id_fkey
    FOREIGN KEY (asset_id) REFERENCES assets(id) ON DELETE CASCADE;
ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_vehicle_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_vehicle_id_fkey
    FOREIGN KEY (vehicle_id) REFERENCES vehicles(id) ON DELETE CASCADE;
ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_vendor_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_vendor_id_fkey
    FOREIGN KEY (vendor_id) REFERENCES vendors(id) ON DELETE SET NULL;
ALTER TABLE maintenance_tasks DROP CONSTRAINT IF EXISTS maintenance_tasks_schedule_id_fkey;
ALTER TABLE maintenance_tasks ADD CONSTRAINT maintenance_tasks_schedule_id_fkey
    FOREIGN KEY (schedule_id) REFERENCES maintenance_schedules(id) ON DELETE SET NULL;

-- derive_subject_property: when asset_id is set, property_id is the asset's
-- property (possibly NULL), so "everything at this property" is one indexed
-- WHERE property_id = $1. Subjects must belong to the row's family. A subject
-- that is not found is left to its FK (it may be mid-cascade).
CREATE OR REPLACE FUNCTION derive_subject_property()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_household UUID;
    v_property  UUID;
    v_found     BOOLEAN;
BEGIN
    IF NEW.asset_id IS NOT NULL THEN
        SELECT household_id, property_id, TRUE INTO v_household, v_property, v_found
          FROM assets WHERE id = NEW.asset_id;
        IF v_found THEN
            IF v_household <> NEW.household_id THEN
                RAISE EXCEPTION '%: asset % does not exist in this family', TG_TABLE_NAME, NEW.asset_id
                    USING ERRCODE = 'foreign_key_violation';
            END IF;
            NEW.property_id := v_property;
        END IF;
    END IF;

    IF NEW.property_id IS NOT NULL THEN
        SELECT household_id INTO v_household FROM properties WHERE id = NEW.property_id;
        IF v_household IS NOT NULL AND v_household <> NEW.household_id THEN
            RAISE EXCEPTION '%: property % does not exist in this family', TG_TABLE_NAME, NEW.property_id
                USING ERRCODE = 'foreign_key_violation';
        END IF;
    END IF;

    IF NEW.vehicle_id IS NOT NULL THEN
        SELECT household_id INTO v_household FROM vehicles WHERE id = NEW.vehicle_id;
        IF v_household IS NOT NULL AND v_household <> NEW.household_id THEN
            RAISE EXCEPTION '%: vehicle % does not exist in this family', TG_TABLE_NAME, NEW.vehicle_id
                USING ERRCODE = 'foreign_key_violation';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER bills_derive_subject_property
    BEFORE INSERT OR UPDATE OF asset_id, property_id, vehicle_id, household_id ON bills
    FOR EACH ROW EXECUTE FUNCTION derive_subject_property();
CREATE TRIGGER loans_derive_subject_property
    BEFORE INSERT OR UPDATE OF asset_id, property_id, vehicle_id, household_id ON loans
    FOR EACH ROW EXECUTE FUNCTION derive_subject_property();
CREATE TRIGGER maintenance_schedules_derive_subject_property
    BEFORE INSERT OR UPDATE OF asset_id, property_id, vehicle_id, household_id ON maintenance_schedules
    FOR EACH ROW EXECUTE FUNCTION derive_subject_property();
CREATE TRIGGER maintenance_tasks_derive_subject_property
    BEFORE INSERT OR UPDATE OF asset_id, property_id, vehicle_id, household_id ON maintenance_tasks
    FOR EACH ROW EXECUTE FUNCTION derive_subject_property();

-- When an asset moves to another property (or out of one), records about it follow.
CREATE OR REPLACE FUNCTION assets_propagate_property()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    UPDATE bills SET property_id = NEW.property_id
     WHERE asset_id = NEW.id AND property_id IS DISTINCT FROM NEW.property_id;
    UPDATE loans SET property_id = NEW.property_id
     WHERE asset_id = NEW.id AND property_id IS DISTINCT FROM NEW.property_id;
    UPDATE maintenance_schedules SET property_id = NEW.property_id
     WHERE asset_id = NEW.id AND property_id IS DISTINCT FROM NEW.property_id;
    UPDATE maintenance_tasks SET property_id = NEW.property_id
     WHERE asset_id = NEW.id AND property_id IS DISTINCT FROM NEW.property_id;
    RETURN NULL;
END;
$$;

CREATE TRIGGER assets_propagate_property
    AFTER UPDATE OF property_id ON assets
    FOR EACH ROW WHEN (OLD.property_id IS DISTINCT FROM NEW.property_id)
    EXECUTE FUNCTION assets_propagate_property();

-- ---------------------------------------------------------------------------
-- 6. Remaining delete rules
-- ---------------------------------------------------------------------------

-- vendors.property_id is dropped in 040 (kept as a note by 039). Until then
-- it must not block deleting a property.
ALTER TABLE vendors DROP CONSTRAINT IF EXISTS vendors_property_id_fkey;
ALTER TABLE vendors ADD CONSTRAINT vendors_property_id_fkey
    FOREIGN KEY (property_id) REFERENCES properties(id) ON DELETE SET NULL;

ALTER TABLE notes DROP CONSTRAINT IF EXISTS notes_author_id_fkey;
ALTER TABLE notes ADD CONSTRAINT notes_author_id_fkey
    FOREIGN KEY (author_id) REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE notifications DROP CONSTRAINT IF EXISTS notifications_user_id_fkey;
ALTER TABLE notifications ADD CONSTRAINT notifications_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE invitations ALTER COLUMN invited_by DROP NOT NULL;
ALTER TABLE invitations DROP CONSTRAINT IF EXISTS invitations_invited_by_fkey;
ALTER TABLE invitations ADD CONSTRAINT invitations_invited_by_fkey
    FOREIGN KEY (invited_by) REFERENCES users(id) ON DELETE SET NULL;

COMMIT;
