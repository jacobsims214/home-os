-- 047_meals.up.sql
--
-- Stage 2: meals (KB decisions/domain-model-v2.md, "Contracts for the Stage 2
-- migrations" -> 047 meals; ticket #4455). The family's recipe box, its meal
-- plan, and what everyone thought of each meal.
--
-- What this adds:
--   1. recipes: family-owned (household_id, CASCADE). title, description,
--      servings, prep/cook minutes, ingredients, steps, tags, source_url.
--        * ingredients is a JSONB array of objects
--          {name, quantity, unit, section}. Only name is required (a
--          non-blank string); quantity is a number or a string ("1/2",
--          "a pinch"); unit and section are strings. recipe_ingredients_valid()
--          checks the shape. A recipe is edited as a whole by one person, so
--          a blob is fine here (unlike list_items, which many people tick
--          off at once). "Add this recipe to the grocery list" copies
--          ingredients into list_items (046) rows.
--        * tags is text[] with a GIN index ("vegetarian", "weeknight").
--   2. meal_plans: one row per meal on a date and slot. A planned meal whose
--      date has passed is the meal history: there is no separate history
--      table, so "what did we eat last month" and "when did we last have
--      tacos" are range queries on (household_id, date).
--        * slot is breakfast, lunch, dinner or snack (text + CHECK, like
--          044/046, not a Postgres enum).
--        * UNIQUE (household_id, date, slot): one meal per slot per day.
--          That index also serves the history queries (household, date).
--        * recipe_id is optional (a same-family composite FK to recipes,
--          ON DELETE SET NULL (recipe_id)): "leftovers" or "eat out" need no
--          recipe.
--        * title is NOT NULL. When the code leaves it NULL it is copied from
--          the recipe, so the history still names the meal after the recipe
--          is deleted.
--   3. meal_feedback: what one person thought of one meal (rating 1-5 and/or
--      a comment). household_id is copied from the meal plan (same-family
--      composite FK, CASCADE with the meal plan).
--        * person_id is a same-family composite FK to people, ON DELETE SET
--          NULL (person_id): when a person is removed their ratings stay as
--          anonymous family ratings, so a recipe's history and average rating
--          don't change. (person_preferences, 041, is the per-person data
--          that CASCADEs with the person.)
--        * One feedback row per person per meal (UNIQUE meal_plan_id,
--          person_id). person_id may be NULL for an unattributed rating.
--   4. Kinds 'recipe' and 'meal_plan' join the entity registry (entity_kind
--      and entity_household()); both tables get rehome_entity_refs(), so
--      notes, files (a photo of grandma's recipe card), secrets and links can
--      be about a recipe or a meal and are cleaned up when it is deleted.
--      rehome_entity_refs() itself is unchanged (046's body stays): nothing
--      in 047 is polymorphic, meal_plans point at recipes by a real FK.
--
-- Preferences and allergies: 041 already has people.allergies and
-- person_preferences (kind like/dislike/dietary/other, category 'food');
-- 047 reuses them and adds no preference table.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Entity registry: kinds 'recipe' and 'meal_plan'
-- ---------------------------------------------------------------------------

-- Each Stage 2 migration re-declares the full list (038's kinds plus every
-- Stage 2 kind). Copied from 046, the latest definition.
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

-- ---------------------------------------------------------------------------
-- 2. recipes
-- ---------------------------------------------------------------------------

-- TRUE when p is a JSON array of ingredient objects with a non-blank string
-- name. quantity may be a number, a string or null; unit and section a
-- string or null. Other keys are allowed (forward compatible).
CREATE FUNCTION recipe_ingredients_valid(p JSONB)
RETURNS BOOLEAN
LANGUAGE sql IMMUTABLE
AS $$
    SELECT CASE
        WHEN p IS NULL OR jsonb_typeof(p) <> 'array' THEN FALSE
        ELSE NOT EXISTS (
            SELECT 1
              FROM jsonb_array_elements(p) AS e(v)
             WHERE jsonb_typeof(e.v) <> 'object'
                OR jsonb_typeof(e.v -> 'name') IS DISTINCT FROM 'string'
                OR btrim(e.v ->> 'name') = ''
                OR COALESCE(jsonb_typeof(e.v -> 'quantity'), 'null') NOT IN ('number', 'string', 'null')
                OR COALESCE(jsonb_typeof(e.v -> 'unit'), 'null') NOT IN ('string', 'null')
                OR COALESCE(jsonb_typeof(e.v -> 'section'), 'null') NOT IN ('string', 'null')
        )
    END
$$;

CREATE TABLE recipes (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id  UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    title         VARCHAR(255) NOT NULL CHECK (btrim(title) <> ''),
    description   TEXT,
    servings      INTEGER CHECK (servings IS NULL OR servings > 0),
    prep_minutes  INTEGER CHECK (prep_minutes IS NULL OR prep_minutes >= 0),
    cook_minutes  INTEGER CHECK (cook_minutes IS NULL OR cook_minutes >= 0),
    ingredients   JSONB NOT NULL DEFAULT '[]'::jsonb,
    steps         TEXT,
    tags          TEXT[] NOT NULL DEFAULT '{}',
    source_url    TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT recipes_ingredients_check CHECK (recipe_ingredients_valid(ingredients)),
    -- Target of meal_plans' same-family composite FK.
    CONSTRAINT recipes_id_household_key UNIQUE (id, household_id)
);

-- The family's recipe box, alphabetical.
CREATE INDEX idx_recipes_household_title ON recipes (household_id, lower(title));
-- "Vegetarian weeknight recipes".
CREATE INDEX idx_recipes_tags ON recipes USING GIN (tags);

-- ---------------------------------------------------------------------------
-- 3. meal_plans (the plan, and the history once the date has passed)
-- ---------------------------------------------------------------------------

CREATE TABLE meal_plans (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id  UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    date          DATE NOT NULL,
    slot          VARCHAR(20) NOT NULL,
    recipe_id     UUID,
    -- Copied from the recipe when the code leaves it NULL (trigger below).
    title         VARCHAR(255) NOT NULL CHECK (btrim(title) <> ''),
    notes         TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT meal_plans_slot_check
        CHECK (slot IN ('breakfast', 'lunch', 'dinner', 'snack')),
    CONSTRAINT meal_plans_recipe_fkey FOREIGN KEY (recipe_id, household_id)
        REFERENCES recipes (id, household_id) ON DELETE SET NULL (recipe_id),
    -- One meal per slot per day. Also the index for the week view and the
    -- history queries: WHERE household_id = $1 AND date BETWEEN $2 AND $3.
    CONSTRAINT meal_plans_household_date_slot_key UNIQUE (household_id, date, slot),
    -- Target of meal_feedback's same-family composite FK.
    CONSTRAINT meal_plans_id_household_key UNIQUE (id, household_id)
);

-- "When did we last have this recipe / how often": newest first per recipe.
CREATE INDEX idx_meal_plans_recipe_date ON meal_plans (recipe_id, date DESC)
    WHERE recipe_id IS NOT NULL;

-- Fills title from the recipe when the code leaves it NULL; sets updated_at
-- on UPDATE.
CREATE FUNCTION meal_plans_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    -- Looked up by id alone: a recipe from another family still fails the
    -- composite FK (23503) instead of surfacing as a NULL title (23502).
    IF NEW.title IS NULL AND NEW.recipe_id IS NOT NULL THEN
        SELECT r.title INTO NEW.title FROM recipes r WHERE r.id = NEW.recipe_id;
    END IF;

    IF TG_OP = 'UPDATE' THEN
        NEW.updated_at := NOW();
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER meal_plans_before_write
    BEFORE INSERT OR UPDATE ON meal_plans
    FOR EACH ROW EXECUTE FUNCTION meal_plans_before_write();

-- ---------------------------------------------------------------------------
-- 4. meal_feedback
-- ---------------------------------------------------------------------------

CREATE TABLE meal_feedback (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meal_plan_id  UUID NOT NULL,
    household_id  UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    person_id     UUID,
    rating        SMALLINT CHECK (rating IS NULL OR rating BETWEEN 1 AND 5),
    comment       TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT meal_feedback_meal_plan_fkey FOREIGN KEY (meal_plan_id, household_id)
        REFERENCES meal_plans (id, household_id) ON DELETE CASCADE,
    CONSTRAINT meal_feedback_person_fkey FOREIGN KEY (person_id, household_id)
        REFERENCES people (id, household_id) ON DELETE SET NULL (person_id),
    -- Feedback says something: a rating, a comment, or both.
    CONSTRAINT meal_feedback_content_check
        CHECK (rating IS NOT NULL OR btrim(COALESCE(comment, '')) <> ''),
    -- One row per person per meal (NULL person_id rows are not limited).
    CONSTRAINT meal_feedback_meal_person_key UNIQUE (meal_plan_id, person_id)
);

-- "Everything Sam rated" / "what does Sam like".
CREATE INDEX idx_meal_feedback_person ON meal_feedback (person_id)
    WHERE person_id IS NOT NULL;

-- Fills household_id from the meal plan when the code leaves it NULL; sets
-- updated_at on UPDATE.
CREATE FUNCTION meal_feedback_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.household_id IS NULL THEN
        SELECT m.household_id INTO NEW.household_id FROM meal_plans m WHERE m.id = NEW.meal_plan_id;
    END IF;

    IF TG_OP = 'UPDATE' THEN
        NEW.updated_at := NOW();
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER meal_feedback_before_write
    BEFORE INSERT OR UPDATE ON meal_feedback
    FOR EACH ROW EXECUTE FUNCTION meal_feedback_before_write();

-- updated_at on every UPDATE of recipes.
CREATE FUNCTION recipes_touch_updated_at()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := NOW();
    RETURN NEW;
END;
$$;

CREATE TRIGGER recipes_touch_updated_at
    BEFORE UPDATE ON recipes
    FOR EACH ROW EXECUTE FUNCTION recipes_touch_updated_at();

-- ---------------------------------------------------------------------------
-- 5. entity_household() and the rehome triggers
-- ---------------------------------------------------------------------------

-- Same body as 046 plus the 047 branches. A later CREATE OR REPLACE must copy
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
        -- 047 meals
        WHEN 'recipe'               THEN SELECT household_id INTO v_household FROM recipes WHERE id = p_id;
        WHEN 'meal_plan'            THEN SELECT household_id INTO v_household FROM meal_plans WHERE id = p_id;
        -- end 047
        ELSE v_household := NULL;
    END CASE;
    RETURN v_household;
END;
$$;

-- rehome_entity_refs() keeps 046's body (files, secrets and lists re-homed;
-- notes and links deleted). Only the triggers are new.
CREATE TRIGGER recipes_rehome_entity_refs AFTER DELETE ON recipes
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('recipe');

CREATE TRIGGER meal_plans_rehome_entity_refs AFTER DELETE ON meal_plans
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('meal_plan');

COMMIT;
