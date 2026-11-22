-- 050_authorship.down.sql
--
-- Removes authorship columns, the activity log and their triggers (ticket
-- #4481).
--
-- Order:
--   1. Triggers first, so the clean-up writes below log nothing.
--   2. notes.author_id and guide_revisions.edited_by take created_by where
--      they are empty (they are kept in step, so normally nothing changes).
--   3. Columns, the activity_log table and the functions.
--
-- Every step tolerates a table that is already gone, so tests may undo
-- earlier migrations (e.g. 049 down drops guides) before this one.
--
-- Lost on down: the activity log, and created_by/updated_by on every table
-- except notes (author_id) and guide_revisions (edited_by).

BEGIN;

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'properties', 'rooms', 'assets', 'vehicles', 'pets', 'vendors',
        'bills', 'loans', 'maintenance_schedules', 'maintenance_tasks',
        'calendars', 'calendar_objects', 'notes', 'files', 'secrets',
        'document_links', 'people', 'person_preferences', 'budget_categories',
        'lists', 'list_items', 'recipes', 'meal_plans', 'meal_feedback',
        'guides', 'guide_revisions'
    ] LOOP
        IF to_regclass(format('public.%I', t)) IS NOT NULL THEN
            EXECUTE format('DROP TRIGGER IF EXISTS %I ON %I', t || '_activity', t);
            EXECUTE format('DROP TRIGGER IF EXISTS %I ON %I', t || '_authorship', t);
        END IF;
    END LOOP;

    -- 086 (#5518) attached log_activity('membership') and its authorship
    -- column ABOVE this migration. Dropping the function below would fail
    -- while that trigger depends on it, so any trigger a later migration
    -- attached has to go first -- this file already tolerates objects that a
    -- later down removed (see the header), and this is the same rule in the
    -- other direction.
    IF to_regclass('public.memberships') IS NOT NULL THEN
        EXECUTE 'DROP TRIGGER IF EXISTS memberships_activity ON memberships';
        EXECUTE 'DROP TRIGGER IF EXISTS memberships_set_actor ON memberships';
        EXECUTE 'DROP TRIGGER IF EXISTS memberships_set_updated_at ON memberships';
        EXECUTE 'ALTER TABLE memberships DROP COLUMN IF EXISTS updated_by, DROP COLUMN IF EXISTS updated_at';
    END IF;

    IF to_regclass('public.notes') IS NOT NULL THEN
        UPDATE notes SET author_id = created_by
         WHERE author_id IS NULL AND created_by IS NOT NULL;
    END IF;
    IF to_regclass('public.guide_revisions') IS NOT NULL THEN
        UPDATE guide_revisions SET edited_by = created_by
         WHERE edited_by IS NULL AND created_by IS NOT NULL;
    END IF;

    FOREACH t IN ARRAY ARRAY[
        'properties', 'rooms', 'assets', 'vehicles', 'pets', 'vendors',
        'bills', 'loans', 'maintenance_schedules', 'maintenance_tasks',
        'calendars', 'calendar_objects', 'notes', 'files', 'secrets',
        'document_links', 'people', 'person_preferences', 'budget_categories',
        'lists', 'list_items', 'recipes', 'meal_plans', 'meal_feedback',
        'guides', 'guide_revisions'
    ] LOOP
        IF to_regclass(format('public.%I', t)) IS NOT NULL THEN
            EXECUTE format(
                'ALTER TABLE %I DROP COLUMN IF EXISTS created_by, DROP COLUMN IF EXISTS updated_by',
                t);
        END IF;
    END LOOP;
END
$$;

DROP TABLE IF EXISTS activity_log;
DROP FUNCTION IF EXISTS log_activity();
DROP FUNCTION IF EXISTS set_authorship();
DROP FUNCTION IF EXISTS activity_container_gone(JSONB);
DROP FUNCTION IF EXISTS current_actor_agent_token_id();
DROP FUNCTION IF EXISTS current_actor_user_id();

COMMIT;
