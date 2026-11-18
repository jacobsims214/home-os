-- 035_mcp_hot_path_indexes.up.sql (#4387)
--
-- Indexes for the MCP tools' hot paths, found by the latency benchmark
-- (#4386, apps/mcp/cmd/mcp/bench_test.go).
--
-- 1. calendar_objects.starts_at / ends_at
--    Every event range query (event_find, agenda_get, family_overview,
--    calendar_find_free_time, event_create's conflict check) filtered on
--    (ical_data::jsonb->>'start')::timestamptz, which parses the JSON of every
--    calendar_objects row in the instance (Seq Scan). A text->timestamptz cast
--    is STABLE, so no expression index can serve it. The two columns hold the
--    same instants, kept by a trigger so every writer is covered (MCP, the
--    API, CalDAV PUT, seeds, plain SQL) without code changes:
--      * starts_at: ical_data's "start" as an instant. For a recurring series
--        (rrule set) that is the series start (the master's DTSTART), which
--        is what CalDAV's ParseEvent stores in "start".
--      * ends_at: ical_data's "end" (for a series, the first occurrence's
--        end). Queries that must keep recurring series in range add
--        "OR co.rrule IS NOT NULL" on the ends_at side.
--      * All-day events are stored as written in the JSON: the date at
--        00:00 UTC with an exclusive end at 00:00 UTC of the following day
--        (#4340). The columns hold those raw instants; queries widen their
--        window (the MCP's allDaySlack, 48h) and apply the household time
--        zone in Go, exactly as before.
--      * NULL when ical_data is not a JSON object or a value does not parse.
--        A malformed row never fails a write, and range queries skip it
--        (the old ::jsonb cast failed the whole query instead).
--    The trigger re-derives both columns on INSERT and whenever an UPDATE
--    changes ical_data or touches the columns themselves, so they cannot
--    drift. Offset-less strings are read as UTC (the function pins the time
--    zone), so the result never depends on the session's TimeZone.
--
-- 2. Household lookups that had no index at 034: calendars (joined by every
--    event query), bills, maintenance_tasks, maintenance_schedules, vendors
--    and notifications.
--
-- 3. pg_trgm GIN index on calendar_objects.ical_data for search_all's
--    "ical_data ILIKE '%term%'" prefilter. pg_trgm is a trusted contrib
--    extension (Postgres 13+), so the database owner (CNPG bootstrap owner
--    "app", which runs migrations) may create it. If the extension is not
--    available or not permitted (an external database), the index is skipped
--    with a NOTICE and search keeps working with a sequential scan. The index
--    is built with fastupdate = off (see below).
--
-- Only references schema that exists at 034: on a fresh database this runs
-- before 036 (which adds calendar_objects.rrule / ical_raw) through 052.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Stored event start/end
-- ---------------------------------------------------------------------------

-- One key of an ical_data JSON document as an instant, or NULL. Never raises.
CREATE FUNCTION calendar_event_instant(p_ical_data TEXT, p_key TEXT)
RETURNS TIMESTAMPTZ
LANGUAGE plpgsql STABLE
SET timezone = 'UTC'
AS $$
DECLARE
    v_doc JSONB;
BEGIN
    IF p_ical_data IS NULL THEN
        RETURN NULL;
    END IF;
    BEGIN
        v_doc := p_ical_data::jsonb;
        IF jsonb_typeof(v_doc) <> 'object' THEN
            RETURN NULL;
        END IF;
        RETURN NULLIF(btrim(v_doc ->> p_key), '')::timestamptz;
    EXCEPTION WHEN data_exception THEN
        -- invalid JSON, or a value that is not a timestamp
        RETURN NULL;
    END;
END;
$$;

ALTER TABLE calendar_objects ADD COLUMN starts_at TIMESTAMPTZ;
ALTER TABLE calendar_objects ADD COLUMN ends_at TIMESTAMPTZ;

COMMENT ON COLUMN calendar_objects.starts_at IS
    'ical_data "start" as an instant (series start for a recurring event; all-day: 00:00 UTC of the date). Trigger-maintained; NULL if unparseable.';
COMMENT ON COLUMN calendar_objects.ends_at IS
    'ical_data "end" as an instant (all-day: exclusive, 00:00 UTC of the day after). Trigger-maintained; NULL if unparseable.';

-- Backfill before the trigger exists. No updated_at trigger exists on
-- calendar_objects at 034, so etags and DTSTAMPs are untouched.
UPDATE calendar_objects
   SET starts_at = calendar_event_instant(ical_data, 'start'),
       ends_at   = calendar_event_instant(ical_data, 'end');

CREATE FUNCTION calendar_objects_set_event_times()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT'
       OR NEW.ical_data IS DISTINCT FROM OLD.ical_data
       OR NEW.starts_at IS DISTINCT FROM OLD.starts_at
       OR NEW.ends_at IS DISTINCT FROM OLD.ends_at THEN
        NEW.starts_at := calendar_event_instant(NEW.ical_data, 'start');
        NEW.ends_at   := calendar_event_instant(NEW.ical_data, 'end');
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER calendar_objects_event_times
    BEFORE INSERT OR UPDATE ON calendar_objects
    FOR EACH ROW EXECUTE FUNCTION calendar_objects_set_event_times();

-- Range queries: per calendar (the household's calendars come from
-- idx_calendars_household), events starting before the range end.
CREATE INDEX idx_calendar_objects_calendar_starts
    ON calendar_objects (calendar_id, starts_at);

-- ---------------------------------------------------------------------------
-- 2. Household lookups
-- ---------------------------------------------------------------------------

CREATE INDEX idx_calendars_household ON calendars (household_id);
CREATE INDEX idx_bills_household ON bills (household_id);
CREATE INDEX idx_maintenance_tasks_household ON maintenance_tasks (household_id, due_date);
CREATE INDEX idx_maintenance_schedules_household ON maintenance_schedules (household_id);
CREATE INDEX idx_vendors_household ON vendors (household_id);
CREATE INDEX idx_notifications_household ON notifications (household_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- 3. Trigram index for event text search (optional)
-- ---------------------------------------------------------------------------

DO $$
BEGIN
    BEGIN
        CREATE EXTENSION IF NOT EXISTS pg_trgm;
    EXCEPTION
        WHEN insufficient_privilege OR undefined_file OR feature_not_supported THEN
            RAISE NOTICE '035: pg_trgm unavailable (%), skipping idx_calendar_objects_ical_trgm', SQLERRM;
            RETURN;
    END;
    -- fastupdate off: calendar writes are rare, and a GIN pending list
    -- (flushed only by VACUUM / autoanalyze) makes the planner cost the
    -- index as expensive and fall back to a sequential scan after a burst of
    -- inserts (seen in the benchmark after seeding).
    EXECUTE 'CREATE INDEX idx_calendar_objects_ical_trgm ON calendar_objects '
         || 'USING gin (ical_data gin_trgm_ops) WITH (fastupdate = off)';
END;
$$;

COMMIT;
