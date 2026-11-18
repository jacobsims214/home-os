-- 073_legacy_bill_events.up.sql (#4943)
--
-- Some calendar entries are bill statements nobody keeps up to date: they were
-- made before the bills module existed ("Electric bill due — $180",
-- "Internet bill due — $75", "Mortgage due — $2,340" on the Bills calendar),
-- and paying a bill never touched them. So the Bills calendar showed each bill
-- twice with disagreeing amounts, and a briefing read the stale entry instead
-- of the bill record and told a parent a bill he had paid that morning was
-- "still unpaid" (war game R1: discoveries/war-game-r1-jacob.md G2 + G5,
-- discoveries/war-game-r1-amber.md G3).
--
-- THE MATCHING RULE, and never anything wider
--
--   A calendar entry is a BILL-LIKE entry when it is live
--   (superseded_at IS NULL), mirrors no record at all (entity_type IS NULL)
--   and is not a bill mirror (event_type <> 'bill'), and either its title
--   reads as a bill statement — it states a money amount, or uses a word only
--   a bill would use (bill, payment, invoice, autopay) — or it sits on a Bills
--   calendar.
--
--   A bill absorbs such an entry when they name the same thing AND agree on
--   the money:
--     * name: calendar_bill_name() of both titles is equal. It lowercases,
--       drops money amounts, drops the words that only say "this is a bill
--       due" (bill, bills, payment, payments, due, autopay, invoice,
--       invoices, reminder, notice, monthly) and treats everything that is
--       not a letter or digit as a separator — so "Electric bill due — $180",
--       "Electric bill", "Electric due" and "ELECTRIC — $180.00" all
--       normalise to "electric", and match the bill named Electric.
--     * money: the entry states no amount, or the bill has none, or they are
--       equal to the cent.
--     * and no OTHER bill of the family normalises to the same name: if two
--       do, there is no way to tell which one the entry is about.
--
--   Everything else is left exactly as it is: a disagreeing amount, a second
--   bill of the same name, a bill the module does not hold yet, a title with
--   no name in it ("Bill due — $80"). The entry stays live and agenda_get
--   reports it as a bill-like calendar entry, never as bill state
--   (apps/mcp/internal/calendar/billlook.go), so the family can decide.
--
-- WHAT SUPERSEDING MEANS (never a delete: a family may have real notes, people
-- or reminders on the entry, and a delete cannot be undone)
--
--   1. calendar_objects.superseded_at / superseded_by / superseded_reason
--      retire the row in place. Its ical_data, ical_raw, notes and people stay,
--      so event_find still reads it by id and an undo is one UPDATE.
--   2. A calendar_changes 'delete' row and a ctag bump make CalDAV clients
--      drop it on their next sync, exactly as billcal.Delete and migration 069
--      do. Every live read (MCP, API, CalDAV) also skips superseded_at IS NOT
--      NULL, so it is gone even for a client that never syncs.
--   3. The bill's own mirror (entity_type 'bill', event_type 'bill',
--      packages/billcal) is the entry that survives; where a bill has none yet
--      the worker's hourly billcal.Backfill creates one.
--
-- THE GUARD, so this cannot recur: bills_absorb_calendar_events runs
-- calendar_absorb_bill_events() after every bills INSERT, so a bill made by any
-- writer (MCP bill_create, the API, legacyfinance, a seed, raw SQL) absorbs its
-- own stale entries instead of leaving two sources of truth. The one-time
-- reconcile below is the same function over the bills that already exist.
--
-- calendar_bill_name(), calendar_bill_amounts() and
-- calendar_title_reads_as_bill() are the SQL copy of billName(), billAmounts()
-- and titleReadsAsBill() in apps/mcp/internal/calendar/billlook.go. Keep them
-- in step; apps/mcp/internal/testutil/legacy_bill_events_test.go compares the
-- two on the same titles.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- A. calendar_objects.superseded_at / superseded_by / superseded_reason
-- ---------------------------------------------------------------------------

ALTER TABLE calendar_objects
    ADD COLUMN superseded_at TIMESTAMPTZ,
    ADD COLUMN superseded_by UUID REFERENCES bills(id) ON DELETE SET NULL,
    ADD COLUMN superseded_reason VARCHAR(40);

-- A retired entry says why, and a live one says nothing. Written so that a
-- NULL reason cannot slip through: `NULL IN (...)` is NULL, which a CHECK
-- accepts, so the reason is compared only after the two columns are known to
-- be set (or unset) together.
ALTER TABLE calendar_objects
    ADD CONSTRAINT calendar_objects_superseded_reason_check CHECK (
        (superseded_at IS NULL) = (superseded_reason IS NULL)
        AND (superseded_reason IS NULL
             OR superseded_reason IN ('matched_bill', 'duplicate'))
    );

COMMENT ON COLUMN calendar_objects.superseded_at IS
    'When Home OS retired this entry in place because it duplicated a record that is kept up to date (a bill). Superseded entries are skipped by every live read and are not deleted, so their notes, people and text survive. #4943';
COMMENT ON COLUMN calendar_objects.superseded_by IS
    'The bill whose record replaced this entry; NULL when it was retired for another reason. #4943';
COMMENT ON COLUMN calendar_objects.superseded_reason IS
    'Why the entry was retired: matched_bill (a bill with the same name and amount is the record) or duplicate. #4943';

-- Live reads ask for superseded_at IS NULL, which nearly every row satisfies;
-- this answers the exceptions instead (the reconcile below, an undo, a count).
CREATE INDEX idx_calendar_objects_superseded
    ON calendar_objects (calendar_id) WHERE superseded_at IS NOT NULL;

-- ---------------------------------------------------------------------------
-- B. The matching rule, as SQL (the Go twin is calendar/billlook.go)
-- ---------------------------------------------------------------------------

-- calendar_bill_name(title) is the name a bill statement is about, with the
-- amount and the words that only say "this is a bill due" taken out:
-- 'Electric bill due — $180' -> 'electric', 'Mortgage due — $2,340' ->
-- 'mortgage', 'Bill due — $80' -> ''.
CREATE FUNCTION calendar_bill_name(p_title TEXT) RETURNS TEXT
LANGUAGE plpgsql IMMUTABLE
AS $$
DECLARE
    t TEXT := lower(COALESCE(p_title, ''));
BEGIN
    -- A money amount is not part of a bill's name.
    t := regexp_replace(t, '\$[0-9][0-9,]*(\.[0-9]+)?', ' ', 'g');
    t := regexp_replace(t, '\y[0-9][0-9,]*(\.[0-9]{2})?\y\s*(usd|dollars?)\y', ' ', 'g');
    -- Words that say what kind of record it is, never which bill it is.
    t := regexp_replace(t,
        '\y(bill|bills|payment|payments|due|autopay|invoice|invoices|reminder|notice|monthly)\y',
        ' ', 'g');
    -- Anything that is not a letter or a digit separates names.
    t := regexp_replace(t, '[^a-z0-9]+', ' ', 'g');
    RETURN btrim(regexp_replace(t, '\s+', ' ', 'g'));
END;
$$;

-- calendar_bill_amounts(title) is every money amount the title states, as
-- numbers: '$1,234.56' -> {1234.56}. Empty when it states none.
CREATE FUNCTION calendar_bill_amounts(p_title TEXT) RETURNS NUMERIC[]
LANGUAGE sql IMMUTABLE
AS $$
    SELECT COALESCE(array_agg(replace(m[1], ',', '')::numeric), '{}'::numeric[])
      FROM regexp_matches(lower(COALESCE(p_title, '')), '\$([0-9][0-9,]*(\.[0-9]+)?)', 'g') AS m
$$;

-- calendar_title_reads_as_bill(title) says whether a title reads as a bill
-- statement rather than an appointment: it states an amount, or uses a word
-- only a bill would use. Deliberately narrower than calendar_bill_name()'s word
-- list, so a title that merely says "due" ("Library books due") is not a bill.
CREATE FUNCTION calendar_title_reads_as_bill(p_title TEXT) RETURNS BOOLEAN
LANGUAGE sql IMMUTABLE
AS $$
    SELECT lower(COALESCE(p_title, '')) ~ '\$[0-9]'
        OR lower(COALESCE(p_title, '')) ~ '\y(bill|bills|payment|payments|invoice|invoices|autopay)\y'
$$;

-- ---------------------------------------------------------------------------
-- C. Absorb one bill's stale calendar entries
-- ---------------------------------------------------------------------------

-- calendar_absorb_bill_events(p_bill) supersedes every live bill-like entry of
-- the bill's family that this bill is unambiguously the record for, and returns
-- how many. See "THE MATCHING RULE" above: name and money agree, and no other
-- bill normalises to the same name.
--
-- It is idempotent: a superseded entry is not reconsidered, and a bill with
-- nothing to absorb writes nothing (no calendar_changes row, no ctag bump).
CREATE FUNCTION calendar_absorb_bill_events(p_bill UUID) RETURNS INT
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
           AND co.entity_type IS NULL
           AND co.event_type IS DISTINCT FROM 'bill'
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
    )
    UPDATE calendars cal SET ctag = gen_random_uuid()::text, updated_at = NOW()
     WHERE cal.id IN (SELECT calendar_id FROM retired);

    GET DIAGNOSTICS v_gone = ROW_COUNT;
    RETURN v_gone;
END;
$$;

COMMENT ON FUNCTION calendar_absorb_bill_events(UUID) IS
    'Supersede the family''s live bill-like calendar entries this bill is unambiguously the record for (same normalised name, agreeing amount, no second bill of that name). Returns how many. #4943';

-- ---------------------------------------------------------------------------
-- D. The one-time reconcile, over the bills that already exist
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
BEGIN
    -- The bill-like entries that exist now, with what they state. After the
    -- reconcile this is what is left live, so the NOTICE explains exactly the
    -- rows agenda_get will report as bill-like entries.
    CREATE TEMP TABLE legacy_bill_entries ON COMMIT DROP AS
    SELECT co.id       AS event_id,
           c.household_id,
           calendar_bill_name(calendar_event_label(co.ical_data, co.ical_raw)) AS event_name,
           COALESCE((calendar_bill_amounts(calendar_event_label(co.ical_data, co.ical_raw)))[1], NULL) AS amount
      FROM calendar_objects co
      JOIN calendars c ON c.id = co.calendar_id
     WHERE co.superseded_at IS NULL
       AND co.entity_type IS NULL
       AND co.event_type IS DISTINCT FROM 'bill'
       AND (calendar_title_reads_as_bill(calendar_event_label(co.ical_data, co.ical_raw))
            OR c.type = 'bills');

    FOR r IN SELECT id FROM bills ORDER BY created_at, id LOOP
        n_one := calendar_absorb_bill_events(r.id);
        n_superseded := n_superseded + n_one;
    END LOOP;

    -- What is still live, and why it was left.
    SELECT count(*) INTO n_blank FROM legacy_bill_entries WHERE event_name = '';
    SELECT count(*) INTO n_ambiguous
      FROM legacy_bill_entries e
     WHERE e.event_name <> ''
       AND (SELECT count(*) FROM bills b
             WHERE b.household_id = e.household_id
               AND calendar_bill_name(b.name) = e.event_name) > 1;
    SELECT count(*) INTO n_amount
      FROM legacy_bill_entries e
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
      FROM legacy_bill_entries e
     WHERE e.event_name <> ''
       AND NOT EXISTS (SELECT 1 FROM bills b
                        WHERE b.household_id = e.household_id
                          AND calendar_bill_name(b.name) = e.event_name);

    RAISE NOTICE '073: superseded % bill-like calendar entries (matched a bill by name and amount)', n_superseded;
    RAISE NOTICE '073: left live: % ambiguous (several bills share the name), % with a disagreeing amount, % with no bill of that name, % unnamed (no name to match); agenda_get reports them as bill-like entries, never as bill state',
        n_ambiguous, n_amount, n_unlinked, n_blank;

    DROP TABLE legacy_bill_entries;
END $$;

-- ---------------------------------------------------------------------------
-- E. The guard: a new bill absorbs its own stale entries
-- ---------------------------------------------------------------------------

CREATE FUNCTION bills_absorb_calendar_events() RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM calendar_absorb_bill_events(NEW.id);
    RETURN NULL;
END;
$$;

COMMENT ON FUNCTION bills_absorb_calendar_events() IS
    'AFTER INSERT on bills (#4943): one bill, one record. A bill made by any writer retires the stale bill-like calendar entries it is the record for, so the two sources of truth cannot come back.';

CREATE TRIGGER bills_absorb_calendar_events AFTER INSERT ON bills
    FOR EACH ROW EXECUTE FUNCTION bills_absorb_calendar_events();

COMMIT;
