-- 046_lists.down.sql
--
-- Removes lists, list_items, kind 'list' and the lists block of
-- rehome_entity_refs() (ticket #4454).
--
-- Order matters:
--   1. lists rows are deleted row by row so that rehome_entity_refs('list')
--      runs: files and secrets about a list move to the family, notes and
--      links about it are deleted. DROP TABLE fires no delete triggers.
--      After that no row uses kind 'list' and the narrower entity_kind check
--      (044's) can be added back.
--   2. The tables and their trigger functions are dropped.
--   3. rehome_entity_refs() goes back to its 038 body and entity_household()
--      to its 044 body (keeping 'person' and 'budget').
--
-- Lost on down: every list and list item.

BEGIN;

-- 1. Lists, through the rehome trigger (items go by FK cascade).
DELETE FROM lists;

-- 2. Tables and functions.
DROP TABLE IF EXISTS list_items;
DROP TABLE IF EXISTS lists;
DROP FUNCTION IF EXISTS list_items_before_write();
DROP FUNCTION IF EXISTS lists_touch_updated_at();
DROP FUNCTION IF EXISTS lists_default_entity();

-- 3a. rehome_entity_refs() as 038 left it.
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

-- 3b. entity_household() as 044 left it (038 body plus the 041 and 044 branches).
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
        ELSE v_household := NULL;
    END CASE;
    RETURN v_household;
END;
$$;

-- entity_kind as 044 left it.
ALTER DOMAIN entity_kind DROP CONSTRAINT entity_kind_check;
ALTER DOMAIN entity_kind ADD CONSTRAINT entity_kind_check CHECK (VALUE IN (
    'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
    'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
    'file', 'note', 'secret',
    -- 041
    'person',
    -- 044
    'budget'
));

COMMIT;
