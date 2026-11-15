-- 049_guides.up.sql
--
-- Stage 2: guides (KB decisions/domain-model-v2.md, "Contracts for the Stage 2
-- migrations" -> 049 guides; ticket #4457). The family's how-tos and runbooks:
-- "replace the air filters", "reset the router", "winterize the lake house".
--
-- What this adds:
--   1. guides: family-owned (household_id, CASCADE). title, summary, body
--      (markdown), tags (text[], GIN index), timestamps.
--        * A guide may be about one property, asset or vehicle. That uses the
--          same typed subject columns as bills and loans (038 mechanism 3),
--          not a polymorphic (entity_type, entity_id) pair:
--            - property_id, asset_id, vehicle_id are nullable FKs, all
--              ON DELETE SET NULL. A guide outlives its subject: when the
--              HVAC is thrown out, "replace the air filters" stays (about the
--              asset's property, or about the family once nothing is left).
--            - guides_subject_check: a vehicle excludes property and asset.
--            - derive_subject_property() (038) sets property_id from the
--              asset and refuses another family's property, asset or
--              vehicle (23503). So "everything at the lake house" is
--              WHERE property_id = $1 and includes guides about its assets.
--            - guides_follow_asset_property(): when an asset moves to
--              another property, guides about it follow. It is a trigger of
--              its own on assets, so 038's assets_propagate_property() is not
--              redefined.
--            - No subject at all means a family-level guide.
--   2. guide_revisions: the body as it was saved, one row per save
--      (guide_id, body, edited_by, created_at). household_id is copied from
--      the guide by trigger (same-family composite FK, CASCADE with the
--      guide). edited_by references users ON DELETE SET NULL, so history
--      survives a removed login. The code that saves a guide inserts the
--      revision in the same transaction: the database cannot know which user
--      is editing (there is no per-session user setting), so there is no
--      automatic snapshot trigger. The newest revision equals guides.body.
--   3. Kind 'guide' joins the entity registry (entity_kind and
--      entity_household()), and guides get rehome_entity_refs('guide'), so
--      notes, files (a photo of the filter size), secrets, lists and links
--      can be about a guide and are cleaned up when it is deleted.
--      rehome_entity_refs() itself is unchanged (046's body stays): guides
--      point at their subject by real FKs.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Entity registry: kind 'guide'
-- ---------------------------------------------------------------------------

-- Each Stage 2 migration re-declares the full list (038's kinds plus every
-- Stage 2 kind). Copied from 047, the latest definition.
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
    'recipe', 'meal_plan',
    -- 049
    'guide'
));

-- ---------------------------------------------------------------------------
-- 2. guides
-- ---------------------------------------------------------------------------

CREATE TABLE guides (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id  UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    title         VARCHAR(255) NOT NULL CHECK (btrim(title) <> ''),
    summary       TEXT,
    -- Markdown.
    body          TEXT NOT NULL DEFAULT '',
    tags          TEXT[] NOT NULL DEFAULT '{}',
    -- Optional subject (038 mechanism 3). property_id is derived from the
    -- asset when asset_id is set.
    property_id   UUID REFERENCES properties(id) ON DELETE SET NULL,
    asset_id      UUID REFERENCES assets(id) ON DELETE SET NULL,
    vehicle_id    UUID REFERENCES vehicles(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT guides_subject_check
        CHECK (vehicle_id IS NULL OR (property_id IS NULL AND asset_id IS NULL)),
    -- Target of guide_revisions' same-family composite FK.
    CONSTRAINT guides_id_household_key UNIQUE (id, household_id)
);

-- The family's guides, alphabetical.
CREATE INDEX idx_guides_household_title ON guides (household_id, lower(title));
-- "Guides tagged hvac".
CREATE INDEX idx_guides_tags ON guides USING GIN (tags);
-- Guides about one property (including its assets), asset or vehicle.
CREATE INDEX idx_guides_property ON guides (property_id) WHERE property_id IS NOT NULL;
CREATE INDEX idx_guides_asset ON guides (asset_id) WHERE asset_id IS NOT NULL;
CREATE INDEX idx_guides_vehicle ON guides (vehicle_id) WHERE vehicle_id IS NOT NULL;

-- Same trigger and column list as bills and loans (038).
CREATE TRIGGER guides_derive_subject_property
    BEFORE INSERT OR UPDATE OF asset_id, property_id, vehicle_id, household_id ON guides
    FOR EACH ROW EXECUTE FUNCTION derive_subject_property();

-- updated_at on every UPDATE of guides.
CREATE FUNCTION guides_touch_updated_at()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := NOW();
    RETURN NEW;
END;
$$;

CREATE TRIGGER guides_touch_updated_at
    BEFORE UPDATE ON guides
    FOR EACH ROW EXECUTE FUNCTION guides_touch_updated_at();

-- When an asset moves to another property (or out of one), guides about it
-- follow, as bills, loans and maintenance do through 038's
-- assets_propagate_property().
CREATE FUNCTION guides_follow_asset_property()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    UPDATE guides SET property_id = NEW.property_id
     WHERE asset_id = NEW.id AND property_id IS DISTINCT FROM NEW.property_id;
    RETURN NULL;
END;
$$;

CREATE TRIGGER assets_guides_follow_property
    AFTER UPDATE OF property_id ON assets
    FOR EACH ROW WHEN (OLD.property_id IS DISTINCT FROM NEW.property_id)
    EXECUTE FUNCTION guides_follow_asset_property();

-- ---------------------------------------------------------------------------
-- 3. guide_revisions
-- ---------------------------------------------------------------------------

CREATE TABLE guide_revisions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    guide_id      UUID NOT NULL,
    household_id  UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    -- Markdown, as saved.
    body          TEXT NOT NULL,
    edited_by     UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT guide_revisions_guide_fkey FOREIGN KEY (guide_id, household_id)
        REFERENCES guides (id, household_id) ON DELETE CASCADE
);

-- A guide's history, newest first.
CREATE INDEX idx_guide_revisions_guide_created ON guide_revisions (guide_id, created_at DESC);
-- "What has Sam edited" (and keeps the edited_by SET NULL cheap).
CREATE INDEX idx_guide_revisions_edited_by ON guide_revisions (edited_by)
    WHERE edited_by IS NOT NULL;

-- Fills household_id from the guide when the code leaves it NULL.
CREATE FUNCTION guide_revisions_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.household_id IS NULL THEN
        SELECT g.household_id INTO NEW.household_id FROM guides g WHERE g.id = NEW.guide_id;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER guide_revisions_before_write
    BEFORE INSERT OR UPDATE ON guide_revisions
    FOR EACH ROW EXECUTE FUNCTION guide_revisions_before_write();

-- ---------------------------------------------------------------------------
-- 4. entity_household() and the rehome trigger
-- ---------------------------------------------------------------------------

-- Same body as 047 plus the 049 branch. A later CREATE OR REPLACE must copy
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
        -- 049 guides
        WHEN 'guide'                THEN SELECT household_id INTO v_household FROM guides WHERE id = p_id;
        -- end 049
        ELSE v_household := NULL;
    END CASE;
    RETURN v_household;
END;
$$;

-- rehome_entity_refs() keeps 046's body (files, secrets and lists re-homed;
-- notes and links deleted). Only the trigger is new.
CREATE TRIGGER guides_rehome_entity_refs AFTER DELETE ON guides
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('guide');

COMMIT;
