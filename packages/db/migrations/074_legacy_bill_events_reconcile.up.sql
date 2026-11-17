-- 074_legacy_bill_events_reconcile.up.sql (#5140)
--
-- 073 (#4943) retired legacy "bill due" calendar entries in place, but its
-- matching predicate required `co.event_type IS DISTINCT FROM 'bill'`: a clause
-- written to skip a bill's own MIRROR rows, which are event_type 'bill'
-- (packages/billcal). The live round-1 entries were written on 2026-11-15,
-- before the bills module's mirrors existed, and are typed event_type 'bill'
-- with entity_type NULL. So the clause excluded exactly the rows 073 meant to
-- supersede: `superseded_total` was 0 and the Bills calendar still showed every
-- bill twice ('Electric bill due — $180' beside the module's 'Electric due').
--
-- WHAT TELLS A MIRROR FROM A LEGACY ENTRY
--
--   A bill mirror is identified by entity_type = 'bill' AND entity_id = the
--   bill (billcal writes both, and only mirror rows carry them). A legacy
--   round-1 entry mirrors no record at all: entity_type IS NULL.
--
--   entity_type, not event_type, is therefore what separates them. 073's
--   `co.event_type IS DISTINCT FROM 'bill'` is dropped here; the predicate is
--   `co.superseded_at IS NULL AND co.entity_type IS NULL`, unchanged in every
--   other respect. The rest of 073's rule still decides whether a match is
--   safe (same normalised name, agreeing money, no second bill of that name,
--   and the title reads as a bill statement or the calendar is a Bills one), so
--   an unrelated event can never be retired by the wider predicate.
--
-- This migration:
--   1. replaces calendar_absorb_bill_events() with the corrected predicate
--      (the Go twin is billLikeRowsSQL in apps/mcp/internal/calendar/
--      billlook.go — keep them in step), and
--   2. runs ONE reconcile pass over every bill, the same loop 073 ran, so the
--      entries 073 could not see are retired now.
--
-- It adds no column, drops nothing and edits no earlier migration: 073 already
-- ran live (schema_migrations version 73), so its failure is corrected forward.
-- The bills INSERT guard from 073 keeps working and now absorbs a legacy entry
-- of this shape too, because it calls this same function.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- A. The absorb function, with the mirror test on entity_type (#5140)
-- ---------------------------------------------------------------------------

-- calendar_absorb_bill_events(p_bill) supersedes every live bill-like entry of
-- the bill's family that this bill is unambiguously the record for, and returns
-- how many. See "THE MATCHING RULE" in 073_legacy_bill_events.up.sql: name and
-- money agree, and no other bill normalises to the same name.
--
-- The one change from 073 is the mirror test: `co.entity_type IS NULL` alone
-- says the row mirrors no record, and a bill mirror is entity_type 'bill'. A
-- legacy round-1 entry is event_type 'bill' with entity_type NULL, so 073's
-- `event_type IS DISTINCT FROM 'bill'` excluded it; there is no such clause
-- here.
--
-- It is idempotent: a superseded entry is not reconsidered, and a bill with
-- nothing to absorb writes nothing (no calendar_changes row, no ctag bump).
CREATE OR REPLACE FUNCTION calendar_absorb_bill_events(p_bill UUID) RETURNS INT
LANGUAGE plpgsql
AS $$
DECLARE
    v_bill     bills;
    v_name     TEXT;
    v_siblings INT;
    v_gone     INT;
BEGIN
    SELECT * INTO v_bill FROM bills WHERE id = p_bill;
    IF NOT FOUND THEN
        RETURN 0;
    END IF;
    v_name := calendar_bill_name(v_bill.name);
    IF v_name = '' THEN
        RETURN 0;
    END IF;

    -- Another bill of the family normalising to the same name makes this
    -- ambiguous: an entry saying "Electric" could be either one.
    SELECT count(*) INTO v_siblings
      FROM bills b
     WHERE b.household_id = v_bill.household_id
       AND calendar_bill_name(b.name) = v_name;
    IF v_siblings <> 1 THEN
        RETURN 0;
    END IF;

    WITH hit AS (
        SELECT co.id
          FROM calendar_objects co
          JOIN calendars c ON c.id = co.calendar_id
         WHERE c.household_id = v_bill.household_id
           AND co.superseded_at IS NULL
           -- Not a mirror. A bill's mirror is entity_type 'bill' with
           -- entity_id set (billcal, packages/billcal); a legacy round-1 entry
           -- mirrors nothing, whatever its event_type says. #5140: the mirror
           -- test used to be event_type, which is what excluded the legacy rows
           -- (event_type 'bill', entity_type NULL) from being retired at all.
           AND co.entity_type IS NULL
           AND calendar_bill_name(calendar_event_label(co.ical_data, co.ical_raw)) = v_name
           AND (calendar_title_reads_as_bill(calendar_event_label(co.ical_data, co.ical_raw))
                OR c.type = 'bills')
           -- The money has to agree too: the entry's amount, when it states
           -- one, is what somebody once read off a statement.
           AND (v_bill.amount IS NULL
                OR calendar_bill_amounts(calendar_event_label(co.ical_data, co.ical_raw)) = '{}'::numeric[]
                OR abs(v_bill.amount
                       - (calendar_bill_amounts(calendar_event_label(co.ical_data, co.ical_raw)))[1]) < 0.005)
    ), retired AS (
        UPDATE calendar_objects co
           SET superseded_at = NOW(),
               superseded_by = v_bill.id,
               superseded_reason = 'matched_bill',
               updated_at = NOW()
          FROM hit
         WHERE co.id = hit.id
        RETURNING co.calendar_id, co.uid
    ), changes AS (
        -- Clients drop it on their next sync, as billcal.Delete does.
        INSERT INTO calendar_changes (calendar_id, event_uid, action)
        SELECT calendar_id, uid, 'delete' FROM retired
    ), bumped AS (
        UPDATE calendars cal SET ctag = gen_random_uuid()::text, updated_at = NOW()
         WHERE cal.id IN (SELECT calendar_id FROM retired)
    )
    -- The count is the RETIRED ENTRIES, read from the CTE that retired them.
    -- 073 ended the block with `GET DIAGNOSTICS v_gone = ROW_COUNT` after the
    -- ctag UPDATE, which reports the CALENDARS it bumped: a bill retiring two
    -- entries on one calendar reported 1 (#5140, found while pinning this).
    SELECT count(*) INTO v_gone FROM retired;
    RETURN v_gone;
END;
$$;

COMMENT ON FUNCTION calendar_absorb_bill_events(UUID) IS
    'Supersede the family''s live bill-like calendar entries this bill is unambiguously the record for (same normalised name, agreeing amount, no second bill of that name). A bill mirror is excluded by entity_type = ''bill'', not by event_type, so a legacy entry typed event_type ''bill'' with entity_type NULL is absorbed too (#5140). Returns how many. #4943';

-- ---------------------------------------------------------------------------
-- B. The one-time reconcile again, now that the predicate can see the rows
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    r            RECORD;
    n_superseded INT := 0;
    n_one        INT;
    n_ambiguous  INT := 0;
    n_amount     INT := 0;
    n_unlinked   INT := 0;
    n_blank      INT := 0;
    n_mirrors    INT := 0;
BEGIN
    -- The legacy entries that exist now, with what they state. After the
    -- reconcile this is what is left live, so the NOTICE explains exactly the
    -- rows agenda_get will report as bill-like entries.
    CREATE TEMP TABLE legacy_bill_entries_074 ON COMMIT DROP AS
    SELECT co.id       AS event_id,
           c.household_id,
           calendar_bill_name(calendar_event_label(co.ical_data, co.ical_raw)) AS event_name,
           COALESCE((calendar_bill_amounts(calendar_event_label(co.ical_data, co.ical_raw)))[1], NULL) AS amount
      FROM calendar_objects co
      JOIN calendars c ON c.id = co.calendar_id
     WHERE co.superseded_at IS NULL
       AND co.entity_type IS NULL
       AND (calendar_title_reads_as_bill(calendar_event_label(co.ical_data, co.ical_raw))
            OR c.type = 'bills');

    -- The bills' own mirrors, counted so the NOTICE can say they were left
    -- alone: entity_type 'bill' is the one thing that marks a mirror.
    SELECT count(*) INTO n_mirrors
      FROM calendar_objects co
     WHERE co.superseded_at IS NULL
       AND co.entity_type = 'bill';

    FOR r IN SELECT id FROM bills ORDER BY created_at, id LOOP
        n_one := calendar_absorb_bill_events(r.id);
        n_superseded := n_superseded + n_one;
    END LOOP;

    -- What is still live, and why it was left.
    SELECT count(*) INTO n_blank FROM legacy_bill_entries_074 WHERE event_name = '';
    SELECT count(*) INTO n_ambiguous
      FROM legacy_bill_entries_074 e
     WHERE e.event_name <> ''
       AND (SELECT count(*) FROM bills b
             WHERE b.household_id = e.household_id
               AND calendar_bill_name(b.name) = e.event_name) > 1;
    SELECT count(*) INTO n_amount
      FROM legacy_bill_entries_074 e
     WHERE e.event_name <> ''
       AND e.amount IS NOT NULL
       AND (SELECT count(*) FROM bills b
             WHERE b.household_id = e.household_id
               AND calendar_bill_name(b.name) = e.event_name) = 1
       AND NOT EXISTS (SELECT 1 FROM bills b
                        WHERE b.household_id = e.household_id
                          AND calendar_bill_name(b.name) = e.event_name
                          AND (b.amount IS NULL OR abs(b.amount - e.amount) < 0.005));
    SELECT count(*) INTO n_unlinked
      FROM legacy_bill_entries_074 e
     WHERE e.event_name <> ''
       AND NOT EXISTS (SELECT 1 FROM bills b
                        WHERE b.household_id = e.household_id
                          AND calendar_bill_name(b.name) = e.event_name);

    RAISE NOTICE '074: superseded % legacy bill-like calendar entries (matched a bill by name and amount; entity_type IS NULL, event_type no longer decides)', n_superseded;
    RAISE NOTICE '074: left live: % ambiguous (several bills share the name), % with a disagreeing amount, % with no bill of that name, % unnamed (no name to match); agenda_get reports them as bill-like entries, never as bill state',
        n_ambiguous, n_amount, n_unlinked, n_blank;
    RAISE NOTICE '074: left % live bill mirror row(s) untouched (entity_type = ''bill''); a mirror is never superseded', n_mirrors;

    DROP TABLE legacy_bill_entries_074;
END $$;

COMMIT;
