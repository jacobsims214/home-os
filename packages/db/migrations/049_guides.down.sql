-- 049_guides.down.sql
--
-- Removes guides, guide_revisions and kind 'guide' (ticket #4457).
--
-- Order matters:
--   1. guides rows are deleted row by row so that rehome_entity_refs() runs:
--      files and secrets about a guide move to the family, lists about one
--      become family-level, notes and links about one are deleted. DROP
--      TABLE fires no delete triggers. After that no row uses kind 'guide'
--      and the narrower entity_kind check (047's) can be added back.
--      Revisions go by FK cascade.
--   2. The trigger on assets, the tables and their functions are dropped.
--      derive_subject_property() and rehome_entity_refs() belong to 038 and
--      stay.
--   3. entity_household() goes back to its 047 body (keeping 'person',
--      'budget', 'list', 'recipe' and 'meal_plan'). rehome_entity_refs() was
--      not changed by 049 and is left alone.
--
-- Lost on down: every guide and guide revision.

BEGIN;

-- 1. Rows, through the rehome trigger.
DELETE FROM guides;

-- 2. Triggers, tables and functions.
DROP TRIGGER IF EXISTS assets_guides_follow_property ON assets;
DROP TABLE IF EXISTS guide_revisions;
DROP TABLE IF EXISTS guides;
DROP FUNCTION IF EXISTS guides_follow_asset_property();
DROP FUNCTION IF EXISTS guide_revisions_before_write();
DROP FUNCTION IF EXISTS guides_touch_updated_at();

-- 3. entity_household() as 047 left it (038 body plus the 041, 044, 046 and
-- 047 branches).
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
        -- 047 meals
        WHEN 'recipe'               THEN SELECT household_id INTO v_household FROM recipes WHERE id = p_id;
        WHEN 'meal_plan'            THEN SELECT household_id INTO v_household FROM meal_plans WHERE id = p_id;
        -- end 047
        ELSE v_household := NULL;
    END CASE;
    RETURN v_household;
END;
$$;

-- entity_kind as 047 left it.
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
    'list',
    -- 047
    'recipe', 'meal_plan'
));

COMMIT;
