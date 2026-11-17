-- 074_legacy_bill_events_reconcile.down.sql (#5140)
--
-- Puts back exactly what 074 changed, and nothing else:
--
--   1. calendar_absorb_bill_events() returns to 073's body, whose mirror test
--      is `co.event_type IS DISTINCT FROM 'bill'` (migration 073's definition,
--      copied here verbatim so the down does not depend on reading 073).
--   2. Every entry 074 retired is live again. 074's predicate differs from
--      073's in ONE way — it also matches an entry that is entity_type NULL
--      and event_type 'bill' — so that is the set to put back: those rows, and
--      only those. What 073 retired (entity_type NULL, event_type <> 'bill')
--      stays retired, because 073's own row is the schema this migration
--      returns to.
--
-- Nothing is deleted, so putting an entry back is dropping its supersede marks.
-- CalDAV clients were told 'delete' on the way up (as they were for 073), so
-- the way down tells them 'create' for the same uids and bumps the ctag, while
-- the columns still exist. The activity_log rows 074 wrote stay: they are the
-- family's history of what happened, not schema.
--
-- Safe to run twice: the second run finds no entry to put back and replaces the
-- function with an identical body.
--
-- Runs in one transaction.

BEGIN;

-- 1. CalDAV clients get the entries back before the marks go away.
WITH back AS (
    SELECT co.calendar_id, co.uid
      FROM calendar_objects co
     WHERE co.superseded_at IS NOT NULL
       AND co.superseded_reason = 'matched_bill'
       AND co.entity_type IS NULL
       AND co.event_type = 'bill'
), changes AS (
    INSERT INTO calendar_changes (calendar_id, event_uid, action)
    SELECT calendar_id, uid, 'create' FROM back
)
UPDATE calendars cal SET ctag = gen_random_uuid()::text, updated_at = NOW()
 WHERE cal.id IN (SELECT calendar_id FROM back);

-- 2. Live again: the entries only 074's predicate could match.
UPDATE calendar_objects
   SET superseded_at = NULL,
       superseded_by = NULL,
       superseded_reason = NULL,
       updated_at = NOW()
 WHERE superseded_at IS NOT NULL
   AND superseded_reason = 'matched_bill'
   AND entity_type IS NULL
   AND event_type = 'bill';

-- 3. Back to 073's function: the mirror test is event_type again.
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
           AND co.entity_type IS NULL
           AND co.event_type IS DISTINCT FROM 'bill'
           AND calendar_bill_name(calendar_event_label(co.ical_data, co.ical_raw)) = v_name
           AND (calendar_title_reads_as_bill(calendar_event_label(co.ical_data, co.ical_raw))
                OR c.type = 'bills')
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
        INSERT INTO calendar_changes (calendar_id, event_uid, action)
        SELECT calendar_id, uid, 'delete' FROM retired
    )
    UPDATE calendars cal SET ctag = gen_random_uuid()::text, updated_at = NOW()
     WHERE cal.id IN (SELECT calendar_id FROM retired);

    GET DIAGNOSTICS v_gone = ROW_COUNT;
    RETURN v_gone;
END;
$$;

COMMENT ON FUNCTION calendar_absorb_bill_events(UUID) IS
    'Supersede the family''s live bill-like calendar entries this bill is unambiguously the record for (same normalised name, agreeing amount, no second bill of that name). Returns how many. #4943';

COMMIT;
