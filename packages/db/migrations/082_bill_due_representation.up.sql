-- 082_bill_due_representation.up.sql (#5511, schema review F12 of #5507)
--
-- "When is this bill due" was stored three ways on bills with nothing tying
-- them together:
--
--   due_day   INTEGER      (007)      day of the month, monthly bills
--   rrule     TEXT         (020)      a recurrence rule nothing reads
--   due_date  DATE         (038/069)  the anchor date of every other frequency
--
-- The only rule was 044's bills_due_day_monthly_check (frequency = 'monthly'
-- OR due_day IS NULL), which constrains due_day alone. A monthly bill could
-- hold due_day = 5 AND due_date = '2026-12-28' and be accepted.
--
-- The consumer inventory behind this migration (on the ticket) found that
-- every reader of a bill's schedule branches on frequency FIRST, and only
-- then reads exactly one column:
--
--   frequency = 'monthly'  -> due_day   (due_date is never looked at)
--   any other frequency    -> due_date  (due_day is never looked at)
--
-- Consumers, all of them agreeing: billdue.Schedule.cadence (the one due
-- rule, behind bill_find, bill_pay, the calendar agenda, family_overview,
-- budget_summary and GET /api/v1/budget/due-soon), billcal.Rule (the Bills
-- calendar mirror event), apps/worker's bill_due notification job, and the
-- UI. A monthly bill's due_date was therefore not a second answer -- it was
-- a value nothing had ever read.
--
-- `rrule` is a different case: it is not a third answer, it is a dead
-- column. The API accepts it on create/update and echoes it back, and NO
-- SELECT anywhere computes anything from bills.rrule. The rrule consumers
-- read is calendar_objects.rrule -- another table, which billcal derives
-- from due_day/due_date and which TestBillRuleMatchesDueRule pins to the due
-- rule. So this migration cannot make rrule authoritative (nothing would
-- read it); it documents it as strictly non-authoritative, and the follow-up
-- ticket removes the column once the API's rrule request fields are gone.
--
-- What this migration makes true, so the schema says what the code means:
--
--   1. frequency SELECTS the authoritative column; the other must be NULL.
--      A monthly bill cannot carry a due_date. (due_day on a non-monthly
--      bill was already refused by 044.)
--   2. due_day, being authoritative for monthly bills, must be a real day
--      of the month (1-31). 044 gave loans that check and never gave bills
--      one.
--   3. All three columns say which one wins, in COMMENT ON COLUMN, so a
--      reader of the catalogue cannot get two answers.
--
-- Why not the other shapes the ticket allowed:
--
--   * "rrule wins when set" cannot be documented, because nothing reads it.
--     Making it authoritative would mean a fourth implementation of the due
--     rule, over a text column, that no code currently has.
--   * Dropping due_day and backfilling due_date (the ticket's preferred #2)
--     is a change to the due rule, not to a column: a monthly bill's series
--     is anchored on created_at's month (billcal.Rule), and due_day is
--     threaded through billdue.Schedule into four modules plus a deliberate
--     second copy in apps/worker. That is its own ticket. Nothing is lost by
--     leaving it: for a monthly bill due_date is unread, so requiring the
--     two to be mutually exclusive is a strict superset of what "generate
--     due_day from due_date" could enforce.
--
-- Backfill before constraining: the violating rows are fixed deliberately,
-- with the discarded value kept in notes, rather than by the ALTER failing
-- or by a silent coercion. The counts are reported to the migrate job's log.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Report what is about to be fixed (visible in the migrate job's log)
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    n_monthly_due_date INT;
    n_bad_due_day      INT;
    n_rrule            INT;
BEGIN
    SELECT count(*) INTO n_monthly_due_date
      FROM bills WHERE frequency = 'monthly' AND due_date IS NOT NULL;
    SELECT count(*) INTO n_bad_due_day
      FROM bills WHERE due_day IS NOT NULL AND (due_day < 1 OR due_day > 31);
    SELECT count(*) INTO n_rrule
      FROM bills WHERE rrule IS NOT NULL;

    RAISE NOTICE
        '082: monthly bills carrying an unread due_date (cleared): %; '
        'bills with a due_day outside 1-31 (cleared): %; '
        'bills with rrule set (now documented as non-authoritative, not dropped): %',
        n_monthly_due_date, n_bad_due_day, n_rrule;
END $$;

-- ---------------------------------------------------------------------------
-- 2. Backfill: monthly bills keep due_day and lose the unread due_date
-- ---------------------------------------------------------------------------

-- The discarded value goes into notes first, so it is not lost and an
-- operator can see what changed (the 069 precedent). A monthly bill with no
-- due_day has no schedule at all -- every consumer already reports "no due
-- date" for it -- so clearing its due_date changes nothing observable
-- either; this is the reviewer's contradictory row, which was never
-- contradictory to any consumer.
UPDATE bills
   SET notes = COALESCE(NULLIF(notes, '') || E'\n', '')
             || format(
                  'Migration 082: this bill is monthly, so its due day is %s; '
                  'the due date %s it also carried was never read by any code '
                  '(both the next due date and the Bills calendar event come '
                  'from the monthly day) and was removed.',
                  COALESCE('day ' || due_day, 'not set, so the bill has no schedule'), due_date),
       due_date = NULL
 WHERE frequency = 'monthly'
   AND due_date IS NOT NULL;

-- A due_day outside 1-31 is not a day of any month, so no reader can resolve
-- it (billdue.Schedule.cadence returns ok = false and the bill shows as
-- having no schedule). NULL is exactly what those bills already behaved as;
-- note the value so it is not lost.
UPDATE bills
   SET notes = COALESCE(NULLIF(notes, '') || E'\n', '')
             || format('Migration 082: due_day %s was not a day of any month '
                       '(1-31) and was cleared; set a real due day to give '
                       'this bill a schedule.', due_day),
       due_day = NULL
 WHERE due_day IS NOT NULL
   AND (due_day < 1 OR due_day > 31);

-- A non-monthly bill with a due_day cannot exist: 044's
-- bills_due_day_monthly_check has refused it since it was added, and this
-- migration does not drop that constraint. Asserted rather than fixed, so a
-- cluster that somehow has one stops here instead of silently losing it.
DO $$
DECLARE
    n INT;
BEGIN
    SELECT count(*) INTO n FROM bills WHERE frequency <> 'monthly' AND due_day IS NOT NULL;
    IF n > 0 THEN
        RAISE EXCEPTION '082: % non-monthly bill(s) hold a due_day, which 044 should have refused. Fix them before migrating.', n;
    END IF;
END $$;

-- ---------------------------------------------------------------------------
-- 3. The invariant: frequency selects the authoritative column
-- ---------------------------------------------------------------------------

-- The due_day half already holds via 044's bills_due_day_monthly_check, which
-- stays as it is (other code and docs name it, and it is the same rule). This
-- adds the half that was missing, in the same shape, so the catalogue states
-- the whole rule.
ALTER TABLE bills ADD CONSTRAINT bills_schedule_one_representation_check
    CHECK (frequency <> 'monthly' OR due_date IS NULL);

-- due_day is authoritative for monthly bills, so it must be a real day of
-- the month. This is what makes "due_day = 32" as impossible as 044 already
-- made it on loans.
ALTER TABLE bills ADD CONSTRAINT bills_due_day_range_check
    CHECK (due_day IS NULL OR due_day BETWEEN 1 AND 31);

-- ---------------------------------------------------------------------------
-- 4. Say which column wins, where a schema reader will look
-- ---------------------------------------------------------------------------

COMMENT ON COLUMN bills.due_day IS
    'AUTHORITATIVE for frequency = ''monthly'', and due_date is NULL on those rows. '
    'The whole schedule of a monthly bill: it falls due on this day (1-31) of every '
    'month, a short month using its last day (due_day 31 is due 30 April). NULL means '
    'the bill has no schedule yet: it is then due nowhere, and every consumer reports '
    '"no due date" for it. Ignored by every reader for any other frequency, which 044 '
    'refuses anyway. The one rule that reads it is packages/billdue (Schedule.cadence); '
    'billcal turns it into the Bills calendar event''s RRULE.';

COMMENT ON COLUMN bills.due_date IS
    'AUTHORITATIVE for frequency <> ''monthly'', and due_day is NULL on those rows. '
    'The anchor date: quarterly, semiannual and annual bills fall due on its day of the '
    'month every 3, 6 or 12 months (clamped to short months), and a once bill falls due '
    'on it and never again. Must be NULL for a monthly bill '
    '(bills_schedule_one_representation_check): a monthly bill''s schedule is due_day, '
    'not a date, and the date was never read as one. NULL for a non-monthly bill means '
    'it has no schedule. The one rule that reads it is packages/billdue '
    '(Schedule.cadence); billcal turns it into the Bills calendar event''s DTSTART.';

COMMENT ON COLUMN bills.rrule IS
    'NOT AUTHORITATIVE, NOT READ. Nothing in the codebase computes a due date from '
    'bills.rrule: this is a write-only legacy column from migration 020, accepted and '
    'echoed by the API since nothing told the API to stop. "When is this bill due" is '
    'due_day (monthly) or due_date (every other frequency) -- never this. The RRULE that '
    'apps actually read is calendar_objects.rrule, a different table, which '
    'packages/billcal derives from due_day/due_date and which TestBillRuleMatchesDueRule '
    'pins to the due rule. Do not read this column; it is scheduled for removal once the '
    'API''s rrule request fields are gone (#5511 follow-up).';

COMMIT;
