-- 078_activity_entity_kind.down.sql (#5509)
--
-- Back to 077: activity_log.entity_type and parent_type are plain
-- VARCHAR(50) again, with only 050's "not blank" check on entity_type, and
-- the activity_entity_kind domain is gone.
--
-- Both columns go back to VARCHAR(50) BEFORE the domain is dropped: a column
-- cannot outlive the type it uses.
--
-- Nothing is deleted and no value is rewritten. Every value in the table is a
-- domain member by construction (078 up refused to finish otherwise), so the
-- narrowing to VARCHAR(50) accepts all of them. The repair 078 up performed
-- (document -> file, maintenance -> maintenance_task) is NOT undone: it was a
-- fix to history, not a schema change, and putting a dead kind back into the
-- audit log would recreate exactly the unreadable rows this migration exists
-- to prevent.
--
-- IF EXISTS throughout, like 077's down: this also runs inside the older
-- migrations' test chains, where 050 may already have dropped the table.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Both columns off the domain
-- ---------------------------------------------------------------------------

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_name = 'activity_log'
                  AND column_name = 'entity_type'
                  AND domain_name = 'activity_entity_kind') THEN
        ALTER TABLE activity_log
            ALTER COLUMN entity_type TYPE VARCHAR(50)
            USING entity_type::text;
    END IF;

    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_name = 'activity_log'
                  AND column_name = 'parent_type'
                  AND domain_name = 'activity_entity_kind') THEN
        ALTER TABLE activity_log
            ALTER COLUMN parent_type TYPE VARCHAR(50)
            USING parent_type::text;
    END IF;
END
$$;

-- ---------------------------------------------------------------------------
-- 2. The vocabulary itself
-- ---------------------------------------------------------------------------

DROP DOMAIN IF EXISTS activity_entity_kind;

COMMIT;
