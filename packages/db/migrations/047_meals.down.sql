-- 047_meals.down.sql
--
-- Removes recipes, meal_plans, meal_feedback and kinds 'recipe' and
-- 'meal_plan' (ticket #4455).
--
-- Order matters:
--   1. meal_plans and recipes rows are deleted row by row so that
--      rehome_entity_refs() runs: files and secrets about a recipe or meal
--      move to the family, lists about one become family-level, notes and
--      links about one are deleted. DROP TABLE fires no delete triggers.
--      After that no row uses the 047 kinds and the narrower entity_kind
--      check (046's) can be added back.
--   2. The tables and their functions are dropped.
--   3. entity_household() goes back to its 046 body (keeping 'person',
--      'budget' and 'list'). rehome_entity_refs() was not changed by 047 and
--      is left alone.
--
-- Lost on down: every recipe, meal plan and meal feedback row.
-- person_preferences (041) is untouched.

BEGIN;

-- 1. Rows, through the rehome triggers (feedback goes by FK cascade).
DELETE FROM meal_plans;
DELETE FROM recipes;

-- 2. Tables and functions.
DROP TABLE IF EXISTS meal_feedback;
DROP TABLE IF EXISTS meal_plans;
DROP TABLE IF EXISTS recipes;
DROP FUNCTION IF EXISTS meal_feedback_before_write();
DROP FUNCTION IF EXISTS meal_plans_before_write();
DROP FUNCTION IF EXISTS recipes_touch_updated_at();
DROP FUNCTION IF EXISTS recipe_ingredients_valid(JSONB);

-- 3. entity_household() as 046 left it (038 body plus the 041, 044 and 046 branches).
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

-- entity_kind as 046 left it.
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

COMMIT;
