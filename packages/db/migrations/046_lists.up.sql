-- 046_lists.up.sql
--
-- Stage 2: shared lists (KB decisions/domain-model-v2.md, "Contracts for the
-- Stage 2 migrations" -> 046 lists; ticket #4454). Grocery, packing,
-- shopping, to-do and chore lists that the whole family edits at once.
--
-- What this adds:
--   1. lists: family-owned (household_id, CASCADE). A list may be about one
--      entity through (entity_type entity_kind, entity_id), checked by
--      enforce_entity_owner(), e.g. a packing list about a vehicle or a
--      shopping list about a room. A family-level list is
--      ('household', household_id); when the code leaves both columns NULL,
--      lists_default_entity fills that in. kind is one of grocery, packing,
--      shopping, todo, chores, other. is_template marks a list that is
--      copied to start a new one (the packing list for every trip);
--      archived hides a finished list without deleting it.
--   2. list_items: one row per item, never a JSON blob of the whole list.
--      Two people checking off different items update different rows, so
--      concurrent edits never overwrite or fork each other. Each item has
--      text, quantity + unit, section (aisle or room), checked/checked_at,
--      an optional assignee (a person, 041), due_date and position.
--        * household_id is copied from the list (same-family composite FK
--          to lists; filled in by trigger when the code leaves it NULL).
--        * assignee_person_id is a same-family composite FK to people,
--          ON DELETE SET NULL (assignee_person_id).
--        * position orders items within a list. When the code leaves it
--          NULL the item goes to the end (max + 1). It is not unique: two
--          concurrent appends may tie, and readers order by
--          (position, created_at, id). It is DOUBLE PRECISION so an item can
--          be moved between two others without renumbering the list.
--        * checked_at is set when checked becomes true and cleared when it
--          becomes false, by trigger, so the two never disagree.
--        * updated_at is set by trigger on every UPDATE of lists and
--          list_items, so a client can poll "items changed since T" per list
--          (idx_list_items_list_updated).
--   3. Kind 'list' joins the entity registry (entity_kind and
--      entity_household()), and lists get rehome_entity_refs('list'), so
--      notes, files, secrets and links can be about a list and are cleaned
--      up when it is deleted.
--   4. rehome_entity_refs() also re-points lists: when the entity a list is
--      about is deleted, the list becomes family-level instead of being
--      lost (the same rule as files and secrets). The function body is 038's
--      plus the lists UPDATE, marked -- 046.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Entity registry: kind 'list'
-- ---------------------------------------------------------------------------

-- Each Stage 2 migration re-declares the full list (038's kinds plus every
-- Stage 2 kind). Copied from 044, the latest definition.
ALTER DOMAIN entity_kind DROP CONSTRAINT entity_kind_check;
ALTER DOMAIN entity_kind ADD CONSTRAINT entity_kind_check CHECK (VALUE IN (
    'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
    'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
    'file', 'note', 'secret',
    -- 041
    'person',
    -- 044
    'budget',
    -- 046
    'list'
));

-- ---------------------------------------------------------------------------
-- 2. lists
-- ---------------------------------------------------------------------------

CREATE TABLE lists (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id  UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    name          VARCHAR(255) NOT NULL CHECK (btrim(name) <> ''),
    kind          VARCHAR(20) NOT NULL DEFAULT 'other',
    -- What the list is about; ('household', household_id) for family-level.
    entity_type   entity_kind NOT NULL,
    entity_id     UUID NOT NULL,
    is_template   BOOLEAN NOT NULL DEFAULT FALSE,
    archived      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT lists_kind_check
        CHECK (kind IN ('grocery', 'packing', 'shopping', 'todo', 'chores', 'other')),
    -- Target of list_items' same-family composite FK.
    CONSTRAINT lists_id_household_key UNIQUE (id, household_id)
);

-- The family's lists page: active lists by kind, templates separately.
CREATE INDEX idx_lists_household ON lists (household_id, archived, kind);
-- "Lists about this vehicle / room" and rehome_entity_refs().
CREATE INDEX idx_lists_entity ON lists (entity_type, entity_id);

-- A list the code creates without a subject is family-level. Named so it
-- sorts (and so fires) before lists_enforce_entity_owner.
CREATE FUNCTION lists_default_entity()
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

CREATE TRIGGER lists_default_entity
    BEFORE INSERT OR UPDATE OF entity_type, entity_id ON lists
    FOR EACH ROW EXECUTE FUNCTION lists_default_entity();

CREATE TRIGGER lists_enforce_entity_owner
    BEFORE INSERT OR UPDATE OF entity_type, entity_id, household_id ON lists
    FOR EACH ROW EXECUTE FUNCTION enforce_entity_owner();

-- updated_at on every UPDATE of lists and list_items (change feeds).
CREATE FUNCTION lists_touch_updated_at()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := NOW();
    RETURN NEW;
END;
$$;

CREATE TRIGGER lists_touch_updated_at
    BEFORE UPDATE ON lists
    FOR EACH ROW EXECUTE FUNCTION lists_touch_updated_at();

-- ---------------------------------------------------------------------------
-- 3. list_items
-- ---------------------------------------------------------------------------

CREATE TABLE list_items (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    list_id             UUID NOT NULL,
    household_id        UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    text                TEXT NOT NULL CHECK (btrim(text) <> ''),
    quantity            NUMERIC(12,3) CHECK (quantity IS NULL OR quantity > 0),
    unit                VARCHAR(50),
    -- Aisle (grocery) or room (packing, chores); free text.
    section             VARCHAR(100),
    checked             BOOLEAN NOT NULL DEFAULT FALSE,
    checked_at          TIMESTAMPTZ,
    assignee_person_id  UUID,
    due_date            DATE,
    position            DOUBLE PRECISION NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT list_items_list_fkey FOREIGN KEY (list_id, household_id)
        REFERENCES lists (id, household_id) ON DELETE CASCADE,
    CONSTRAINT list_items_assignee_fkey FOREIGN KEY (assignee_person_id, household_id)
        REFERENCES people (id, household_id) ON DELETE SET NULL (assignee_person_id),
    CONSTRAINT list_items_checked_at_check
        CHECK (checked = (checked_at IS NOT NULL))
);

-- Items of a list in display order.
CREATE INDEX idx_list_items_list_position ON list_items (list_id, position);
-- Items of a list changed since T (change feed / sync).
CREATE INDEX idx_list_items_list_updated ON list_items (list_id, updated_at);
-- "What's assigned to me" across the family's lists.
CREATE INDEX idx_list_items_assignee ON list_items (assignee_person_id)
    WHERE assignee_person_id IS NOT NULL;

-- Fills household_id from the list and position at the end of the list when
-- the code leaves them NULL; keeps checked_at in step with checked; sets
-- updated_at on UPDATE.
CREATE FUNCTION list_items_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.household_id IS NULL THEN
        SELECT l.household_id INTO NEW.household_id FROM lists l WHERE l.id = NEW.list_id;
    END IF;

    IF NEW.position IS NULL THEN
        SELECT COALESCE(MAX(i.position) + 1, 0) INTO NEW.position
          FROM list_items i WHERE i.list_id = NEW.list_id;
    END IF;

    -- A client may send its own checked_at (an offline check-off synced
    -- later); otherwise the time of the write is used.
    IF NOT NEW.checked THEN
        NEW.checked_at := NULL;
    ELSIF NEW.checked_at IS NULL THEN
        NEW.checked_at := NOW();
    END IF;

    IF TG_OP = 'UPDATE' THEN
        NEW.updated_at := NOW();
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER list_items_before_write
    BEFORE INSERT OR UPDATE ON list_items
    FOR EACH ROW EXECUTE FUNCTION list_items_before_write();

-- ---------------------------------------------------------------------------
-- 4. entity_household() and rehome_entity_refs()
-- ---------------------------------------------------------------------------

-- Same body as 044 plus the 046 branch. A later CREATE OR REPLACE must copy
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
        -- 044 budgeting
        WHEN 'budget'               THEN SELECT household_id INTO v_household FROM budget_categories WHERE id = p_id;
        -- end 044
        -- 046 lists
        WHEN 'list'                 THEN SELECT household_id INTO v_household FROM lists WHERE id = p_id;
        -- end 046
        ELSE v_household := NULL;
    END CASE;
    RETURN v_household;
END;
$$;

-- 038's body plus the 046 lists UPDATE. A later CREATE OR REPLACE must copy
-- this whole function, keeping every Stage 2 block.
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

    -- 046 lists: a list about a deleted entity becomes family-level.
    UPDATE lists l SET entity_type = 'household', entity_id = l.household_id
     WHERE l.entity_type = v_kind AND l.entity_id = OLD.id
       AND EXISTS (SELECT 1 FROM households h WHERE h.id = l.household_id);
    -- end 046

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

CREATE TRIGGER lists_rehome_entity_refs AFTER DELETE ON lists
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('list');

COMMIT;
