-- 075_kb_docs.up.sql (#5502)
--
-- The family's own knowledge base: durable, household-scoped knowledge that a
-- person or an agent can write once and anyone can find later.
--
-- "How to reset the router", "our pediatrician's after-hours number and when
-- to use it", "what the babysitter needs to know". It is neither a note
-- (transient, attached to one record) nor a file (an uploaded document): a KB
-- doc has a PATH that is its stable address in the family's own namespace.
--
-- 1. kb_docs: household-owned (CASCADE). path is a namespaced slug
--    ('how-to/networking/router-reset'); UNIQUE (household_id, path) gives a
--    doc exactly ONE home, so a write replaces instead of forking. deleted_at
--    is a soft delete: a doc's path is never reused by a different doc, and
--    the reconciler stops indexing it while its history stays.
--    created_by / updated_by reference users ON DELETE SET NULL, so the
--    doc survives a removed login.
-- 2. role_can_see_entity() gains 'kb_doc' -> 'kb' (the ResKB resource). Every
--    household role reads the KB, a housesitter included: the babysitter
--    needs the router doc. Keep in step with apps/api/internal/auth/roles.go.
--
-- No entity_kind change: a kb doc is not a note target, and nothing attaches
-- to it.
--
-- Runs in one transaction.

BEGIN;

CREATE TABLE kb_docs (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id  UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    -- Namespaced slug: lower-case segments separated by '/'. No leading,
    -- trailing or doubled slash, no '..' (checked), at most 512 characters.
    path          VARCHAR(512) NOT NULL
                  CHECK (path ~ '^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)*$'),
    title         VARCHAR(255) NOT NULL CHECK (btrim(title) <> ''),
    -- Markdown.
    body          TEXT NOT NULL DEFAULT '',
    tags          TEXT[] NOT NULL DEFAULT '{}',
    created_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Soft delete: NULL means live.
    deleted_at    TIMESTAMPTZ,
    CONSTRAINT kb_docs_household_path_key UNIQUE (household_id, path)
);

-- "The family's docs, by path" (the unique constraint above indexes the same
-- columns; this partial index is what list/get scans, live docs only).
CREATE INDEX idx_kb_docs_live ON kb_docs (household_id, path) WHERE deleted_at IS NULL;
-- "Docs tagged veterinary", and the GIN index the intent filter would use.
CREATE INDEX idx_kb_docs_tags ON kb_docs USING GIN (tags);

-- updated_at on every UPDATE of kb_docs.
CREATE FUNCTION kb_docs_touch_updated_at()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := NOW();
    RETURN NEW;
END;
$$;

CREATE TRIGGER kb_docs_touch_updated_at
    BEFORE UPDATE ON kb_docs
    FOR EACH ROW EXECUTE FUNCTION kb_docs_touch_updated_at();

-- The SQL copy of auth.CanSeeEntity (061) gains the kb_doc branch. A doc is
-- readable by every role: the KB exists so that a housesitter can read the
-- router doc. Copy of 061's body plus the two new lines.
CREATE OR REPLACE FUNCTION role_can_see_entity(p_role TEXT, p_kind TEXT) RETURNS BOOLEAN
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
    SELECT COALESCE(p_role = ANY (
        CASE (CASE p_kind                              -- entityResources
                  WHEN 'household'            THEN 'family_notes'
                  WHEN 'property'             THEN 'properties'
                  WHEN 'room'                 THEN 'properties'
                  WHEN 'asset'                THEN 'assets'
                  WHEN 'vehicle'              THEN 'vehicles'
                  WHEN 'pet'                  THEN 'pets'
                  WHEN 'vendor'               THEN 'vendors'
                  WHEN 'bill'                 THEN 'bills'
                  WHEN 'loan'                 THEN 'loans'
                  WHEN 'maintenance'          THEN 'maintenance'
                  WHEN 'maintenance_task'     THEN 'maintenance'
                  WHEN 'maintenance_schedule' THEN 'maintenance'
                  WHEN 'calendar'             THEN 'calendar'
                  WHEN 'calendar_event'       THEN 'calendar'
                  WHEN 'note'                 THEN 'notes'
                  WHEN 'file'                 THEN 'files'
                  WHEN 'document'             THEN 'files'
                  WHEN 'document_link'        THEN 'files'
                  WHEN 'secret'               THEN 'secrets'
                  WHEN 'person'               THEN 'people'
                  WHEN 'person_preference'    THEN 'preferences'
                  WHEN 'budget'               THEN 'budget'
                  WHEN 'list'                 THEN 'lists'
                  WHEN 'list_item'            THEN 'lists'
                  WHEN 'recipe'               THEN 'meals'
                  WHEN 'meal_plan'            THEN 'meals'
                  WHEN 'meal_feedback'        THEN 'meals'
                  WHEN 'guide'                THEN 'guides'
                  -- 075
                  WHEN 'kb_doc'               THEN 'kb'
              END)
            -- policy[resource][ActRead]
            WHEN 'family_notes' THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'properties'   THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter', 'vendor'] -- rolesEveryone
            WHEN 'assets'       THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter', 'vendor'] -- rolesEveryone
            WHEN 'vehicles'     THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'pets'         THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']   -- rolesSitter
            WHEN 'vendors'      THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']   -- rolesSitter
            WHEN 'calendar'     THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']   -- rolesSitter
            WHEN 'maintenance'  THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'vendor']        -- rolesFabric
            WHEN 'bills'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'loans'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'budget'       THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'notes'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']   -- rolesSitter
            WHEN 'files'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'secrets'      THEN ARRAY['owner', 'family_manager', 'family_member']                            -- rolesFamily
            WHEN 'people'       THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'preferences'  THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer']                  -- rolesReaders
            WHEN 'lists'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']   -- rolesSitter
            WHEN 'meals'        THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']   -- rolesSitter
            WHEN 'guides'       THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter']   -- rolesSitter
            -- 075: the KB is everyone's, a housesitter's included.
            WHEN 'kb'           THEN ARRAY['owner', 'family_manager', 'family_member', 'viewer', 'housesitter', 'vendor'] -- rolesEveryone
            -- Unknown kind: family roles only (fails closed).
            ELSE ARRAY['owner', 'family_manager', 'family_member']
        END), false)
$$;

COMMENT ON FUNCTION role_can_see_entity(TEXT, TEXT) IS
    'SQL copy of auth.CanSeeEntity (apps/api/internal/auth/roles.go); keep in step. Unknown kinds: family roles only.';

COMMIT;