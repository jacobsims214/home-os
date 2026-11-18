-- 069_drop_finance_columns.up.sql
--
-- Drops the legacy finance columns on properties and vehicles (#4707,
-- leftover of #4601), after copying every value still in them into the
-- subject's loan or bill:
--   properties: mortgage_amount, mortgage_rate, mortgage_term_months,
--     mortgage_start_date, mortgage_lender, mortgage_account_number,
--     property_tax_annual, property_tax_due_months, insurance_annual,
--     insurance_provider, hoa_fee_monthly
--   vehicles: lender, loan_amount, loan_term_months, monthly_payment,
--     registration_cost, registration_renewal_month, insurance_cost,
--     insurance_provider
--
-- Why a second copy: 044 section 4 copied the columns once, but until #4704
-- the API kept writing the property and vehicle forms into the columns, so a
-- value typed after 044 ran exists only there. 044's section cannot simply be
-- re-run: its has_loan/has_bill helpers and INSERTs use bills/loans
-- entity_type/entity_id, which 056 dropped.
--
-- Which row is "the" loan or bill: the rules of apps/api/internal/legacyfinance
-- (KB apis/property-vehicle-finance.md), i.e. 044's rules on the typed FK
-- only. Same family, property_id/vehicle_id = the subject, asset_id IS NULL;
-- the oldest row (created_at, id) wins, and for a property a loan named like
-- '%mortgage%' wins over other loans. Bills match by name or category:
--   property tax %tax%, insurance %insur%, HOA %hoa% / %association%,
--   vehicle registration %regist%, vehicle insurance %insur%.
-- Keep these in step with legacyfinance's LoanJoin/BillJoin.
--
-- For each subject and kind:
--   * No matching row: one is created when any value is recorded (044 needed
--     an amount, a lender or an insurance provider; here a lone rate, due
--     month, term, ... also counts, so no value is dropped). New rows look
--     like 044's copies, without entity_type/entity_id: annual bills (HOA
--     monthly), due_date = the first of the due month today or later, other
--     due-month text in a "Due months: ..." notes line, the insurance provider
--     as a same-family vendor (bills.vendor_id; found by name, trade
--     insurance first, else created), as legacyfinance.ApplyBill does. A bill
--     gets budget_category_id only when the family already has a budget
--     category named like the bill's category (the MCP bill_create default);
--     none is created. Loans get no budget category.
--   * A matching row: each recorded column value is compared with what the
--     API shows for that row (amounts converted between the column's period
--     and the bill's frequency, rounded to cents). Equal: nothing changes, so
--     what 044 copied is not copied again. Different:
--       - the row's field is empty (NULL, or 0 for an amount) -> filled;
--       - the row is an unedited 044 copy (044's COPY_MARKER notes and
--         updated_at = created_at): the column is at least as new as the
--         copy, so the column value is written;
--       - otherwise the row was edited and either side may be newer: the row
--         is kept and the column value is appended to its notes as
--         "Migration 069: the property's details also said ...", so it is
--         not lost.
--   * A bill whose amount or due date this migration changes loses its
--     Bills-calendar mirror event (deleted with billcal.Delete's SQL: a
--     calendar_changes 'delete' row and a ctag bump), and the worker's
--     billcal backfill (#4716) creates a fresh one. New bills get their event
--     from the same backfill. Events people made are never touched.
--
-- Rows created here carry notes starting with
--   'Copied from the property''s details by migration 069.' or
--   'Copied from the vehicle''s details by migration 069.'
-- Re-running the copy changes nothing: every value then equals its row.
--
-- One transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- Helpers (pg_temp, dropped at the end)
-- ---------------------------------------------------------------------------

-- The first day of month p_month that is today or later (044's helper).
CREATE FUNCTION pg_temp.next_month_start(p_month INT)
RETURNS DATE
LANGUAGE sql STABLE
AS $$
    SELECT CASE
        WHEN p_month IS NULL OR p_month NOT BETWEEN 1 AND 12 THEN NULL
        WHEN make_date(EXTRACT(YEAR FROM CURRENT_DATE)::int, p_month, 1) >= CURRENT_DATE
            THEN make_date(EXTRACT(YEAR FROM CURRENT_DATE)::int, p_month, 1)
        ELSE make_date(EXTRACT(YEAR FROM CURRENT_DATE)::int + 1, p_month, 1)
    END
$$;

-- Payments per year of a bill frequency (legacyfinance periodsSQL).
CREATE FUNCTION pg_temp.periods(p_frequency TEXT)
RETURNS INT
LANGUAGE sql IMMUTABLE
AS $$
    SELECT CASE p_frequency WHEN 'weekly' THEN 52 WHEN 'monthly' THEN 12 WHEN 'quarterly' THEN 4
                            WHEN 'semiannual' THEN 2 ELSE 1 END
$$;

-- p_notes with p_line appended on its own line, unless it is already there.
CREATE FUNCTION pg_temp.with_line(p_notes TEXT, p_line TEXT)
RETURNS TEXT
LANGUAGE sql IMMUTABLE
AS $$
    SELECT CASE
        WHEN p_line IS NULL OR p_line = '' THEN p_notes
        WHEN p_notes IS NULL OR btrim(p_notes) = '' THEN p_line
        WHEN position(p_line IN p_notes) > 0 THEN p_notes
        ELSE rtrim(p_notes, E'\n') || E'\n' || p_line
    END
$$;

-- p_notes with its "Due months: ..." line set to p_value (legacyfinance
-- withDueMonths), or removed when p_value is NULL.
CREATE FUNCTION pg_temp.with_due_months(p_notes TEXT, p_value TEXT)
RETURNS TEXT
LANGUAGE sql IMMUTABLE
AS $$
    SELECT NULLIF(pg_temp.with_line(
               NULLIF(rtrim(regexp_replace(COALESCE(p_notes, ''), '(^|\n)Due months: [^\n]*', '', 'g'), E'\n'), ''),
               'Due months: ' || p_value), '')
$$;

-- Is this row a copy 044 made that nobody edited since? (044 COPY_MARKER.)
CREATE FUNCTION pg_temp.unedited_044_copy(p_notes TEXT, p_created TIMESTAMPTZ, p_updated TIMESTAMPTZ)
RETURNS BOOLEAN
LANGUAGE sql IMMUTABLE
AS $$
    SELECT p_updated = p_created
       AND (p_notes LIKE 'Copied from the property''s details by migration 044.%'
         OR p_notes LIKE 'Copied from the vehicle''s details by migration 044.%')
$$;

-- Delete a bill's Bills-calendar mirror event(s) the way billcal.Delete does,
-- so the worker's backfill rebuilds it from the bill as it is now.
CREATE FUNCTION pg_temp.drop_bill_event(p_household UUID, p_bill UUID)
RETURNS VOID
LANGUAGE sql
AS $$
    WITH gone AS (
        DELETE FROM calendar_objects co USING calendars c
         WHERE c.id = co.calendar_id AND c.household_id = p_household
           AND co.entity_type = 'bill' AND co.entity_id = p_bill AND co.event_type = 'bill'
     RETURNING co.calendar_id, co.uid
    ), changes AS (
        INSERT INTO calendar_changes (calendar_id, event_uid, action)
        SELECT calendar_id, uid, 'delete' FROM gone
    )
    UPDATE calendars SET ctag = gen_random_uuid()::text, updated_at = NOW()
     WHERE id IN (SELECT calendar_id FROM gone)
$$;

-- The family's vendor for an insurance provider name (legacyfinance
-- resolveProvider): same name trimmed and case-insensitive, trade insurance
-- first, then the oldest; else a new {name, Insurance, {insurance}} vendor.
CREATE FUNCTION pg_temp.provider_vendor(p_household UUID, p_name TEXT)
RETURNS UUID
LANGUAGE plpgsql
AS $$
DECLARE
    v_id UUID;
BEGIN
    SELECT id INTO v_id FROM vendors
     WHERE household_id = p_household AND lower(btrim(name)) = lower(btrim(p_name))
     ORDER BY ('insurance' = ANY (trades)) DESC, created_at, id
     LIMIT 1;
    IF v_id IS NULL THEN
        INSERT INTO vendors (household_id, name, specialty, trades)
        VALUES (p_household, btrim(p_name), 'Insurance', ARRAY['insurance'])
        RETURNING id INTO v_id;
    END IF;
    RETURN v_id;
END;
$$;

-- Copy one subject's loan fields into its loan (see the header).
-- p_source is 'property' or 'vehicle'; p_label prefixes field names in
-- notes ('mortgage' / 'loan').
CREATE FUNCTION pg_temp.apply_loan(
    p_household UUID, p_property UUID, p_vehicle UUID, p_prefer TEXT[],
    p_name TEXT, p_source TEXT, p_label TEXT,
    p_amount NUMERIC, p_rate NUMERIC, p_term INT, p_start DATE,
    p_lender TEXT, p_account TEXT, p_payment NUMERIC)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    v_amount  NUMERIC := CASE WHEN p_amount > 0 THEN p_amount END;
    v_rate    NUMERIC := CASE WHEN p_rate >= 0 AND p_rate < 100 THEN p_rate END;
    v_term    INT     := CASE WHEN p_term > 0 THEN p_term END;
    v_lender  TEXT    := NULLIF(btrim(p_lender), '');
    v_account TEXT    := NULLIF(btrim(p_account), '');
    v_payment NUMERIC := CASE WHEN p_payment > 0 THEN p_payment END;
    -- A rate loans.interest_rate cannot hold (044 dropped it) is kept as a note.
    v_bad_rate TEXT   := CASE WHEN p_rate IS NOT NULL AND v_rate IS NULL
                              THEN format('%s rate %s', p_label, p_rate) END;
    l RECORD;
    v_overwrite BOOLEAN;
    v_conflicts TEXT[] := ARRAY[]::TEXT[];
    n_amount NUMERIC; n_rate NUMERIC; n_term INT; n_start DATE;
    n_lender TEXT; n_account TEXT; n_payment NUMERIC;
BEGIN
    IF v_amount IS NULL AND v_rate IS NULL AND v_term IS NULL AND p_start IS NULL
       AND v_lender IS NULL AND v_account IS NULL AND v_payment IS NULL AND v_bad_rate IS NULL THEN
        RETURN;
    END IF;

    SELECT * INTO l FROM loans
     WHERE household_id = p_household AND asset_id IS NULL
       AND ((p_property IS NOT NULL AND property_id = p_property)
         OR (p_vehicle  IS NOT NULL AND vehicle_id  = p_vehicle))
     ORDER BY (name ILIKE ANY (p_prefer)) DESC, created_at, id
     LIMIT 1
       FOR UPDATE;

    IF NOT FOUND THEN
        INSERT INTO loans (household_id, name, property_id, vehicle_id, lender, original_amount,
                           remaining_balance, interest_rate, term_months, monthly_payment,
                           start_date, term_end, account_number, notes)
        VALUES (p_household, p_name, p_property, p_vehicle, v_lender, COALESCE(v_amount, 0),
                COALESCE(v_amount, 0), v_rate, v_term, v_payment, p_start,
                CASE WHEN p_start IS NOT NULL AND v_term IS NOT NULL
                     THEN (p_start + make_interval(months => v_term))::date END,
                v_account,
                pg_temp.with_line(
                    format('Copied from the %s''s details by migration 069. ', p_source)
                    || 'The remaining balance is the original amount; update it.',
                    CASE WHEN v_bad_rate IS NOT NULL
                         THEN format('Migration 069: the %s''s details also said %s.', p_source, v_bad_rate) END));
        RETURN;
    END IF;

    v_overwrite := pg_temp.unedited_044_copy(l.notes, l.created_at, l.updated_at);
    n_amount := l.original_amount; n_rate := l.interest_rate; n_term := l.term_months;
    n_start := l.start_date; n_lender := l.lender; n_account := l.account_number;
    n_payment := l.monthly_payment;

    IF v_amount IS NOT NULL AND v_amount IS DISTINCT FROM l.original_amount THEN
        IF COALESCE(l.original_amount, 0) = 0 OR v_overwrite THEN n_amount := v_amount;
        ELSE v_conflicts := v_conflicts || format('%s amount %s', p_label, v_amount); END IF;
    END IF;
    IF v_rate IS NOT NULL AND v_rate IS DISTINCT FROM l.interest_rate THEN
        IF l.interest_rate IS NULL OR v_overwrite THEN n_rate := v_rate;
        ELSE v_conflicts := v_conflicts || format('%s rate %s', p_label, v_rate); END IF;
    END IF;
    IF v_bad_rate IS NOT NULL THEN
        v_conflicts := v_conflicts || v_bad_rate;
    END IF;
    IF v_term IS NOT NULL AND v_term IS DISTINCT FROM l.term_months THEN
        IF l.term_months IS NULL OR v_overwrite THEN n_term := v_term;
        ELSE v_conflicts := v_conflicts || format('%s term %s months', p_label, v_term); END IF;
    END IF;
    IF p_start IS NOT NULL AND p_start IS DISTINCT FROM l.start_date THEN
        IF l.start_date IS NULL OR v_overwrite THEN n_start := p_start;
        ELSE v_conflicts := v_conflicts || format('%s start date %s', p_label, p_start); END IF;
    END IF;
    IF v_lender IS NOT NULL AND v_lender IS DISTINCT FROM btrim(l.lender) THEN
        IF NULLIF(btrim(l.lender), '') IS NULL OR v_overwrite THEN n_lender := v_lender;
        ELSE v_conflicts := v_conflicts || format('lender %s', v_lender); END IF;
    END IF;
    IF v_account IS NOT NULL AND v_account IS DISTINCT FROM btrim(l.account_number) THEN
        IF NULLIF(btrim(l.account_number), '') IS NULL OR v_overwrite THEN n_account := v_account;
        ELSE v_conflicts := v_conflicts || format('account number %s', v_account); END IF;
    END IF;
    IF v_payment IS NOT NULL AND v_payment IS DISTINCT FROM l.monthly_payment THEN
        IF COALESCE(l.monthly_payment, 0) = 0 OR v_overwrite THEN n_payment := v_payment;
        ELSE v_conflicts := v_conflicts || format('monthly payment %s', v_payment); END IF;
    END IF;

    IF n_amount IS NOT DISTINCT FROM l.original_amount AND n_rate IS NOT DISTINCT FROM l.interest_rate
       AND n_term IS NOT DISTINCT FROM l.term_months AND n_start IS NOT DISTINCT FROM l.start_date
       AND n_lender IS NOT DISTINCT FROM l.lender AND n_account IS NOT DISTINCT FROM l.account_number
       AND n_payment IS NOT DISTINCT FROM l.monthly_payment
       AND (cardinality(v_conflicts) = 0
            OR position(format('Migration 069: the %s''s details also said %s.', p_source,
                               array_to_string(v_conflicts, ', ')) IN COALESCE(l.notes, '')) > 0) THEN
        RETURN;
    END IF;

    UPDATE loans
       SET original_amount = n_amount,
           interest_rate   = n_rate,
           term_months     = n_term,
           start_date      = n_start,
           lender          = n_lender,
           account_number  = n_account,
           monthly_payment = n_payment,
           -- As legacyfinance.ApplyLoan: recomputed when term or start changes.
           term_end = CASE
               WHEN (n_term IS DISTINCT FROM l.term_months OR n_start IS DISTINCT FROM l.start_date)
                AND n_start IS NOT NULL AND n_term > 0
               THEN (n_start + make_interval(months => n_term))::date
               ELSE term_end END,
           notes = CASE WHEN cardinality(v_conflicts) = 0 THEN notes
                        ELSE pg_temp.with_line(notes, format('Migration 069: the %s''s details also said %s.',
                                                             p_source, array_to_string(v_conflicts, ', '))) END,
           updated_at = NOW()
     WHERE id = l.id;
END;
$$;

-- Copy one subject's bill fields into its bill of one kind (see the header).
-- p_input_periods: how often a year the column's amount is paid (1 for an
-- annual figure, 12 for hoa_fee_monthly). p_due is a due month as text
-- (property_tax_due_months, registration_renewal_month). p_provider only for
-- insurance kinds. p_amount_label / p_due_label name the fields in notes.
CREATE FUNCTION pg_temp.apply_bill(
    p_household UUID, p_property UUID, p_vehicle UUID, p_patterns TEXT[],
    p_name TEXT, p_source TEXT, p_category TEXT, p_frequency TEXT, p_input_periods INT,
    p_amount NUMERIC, p_due TEXT, p_provider TEXT,
    p_amount_label TEXT, p_due_label TEXT)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    v_amount   NUMERIC := CASE WHEN p_amount > 0 THEN p_amount END;
    v_due      TEXT    := NULLIF(btrim(p_due), '');
    v_provider TEXT    := NULLIF(btrim(p_provider), '');
    v_month    INT;
    b RECORD;
    v_overwrite BOOLEAN;
    v_conflicts TEXT[] := ARRAY[]::TEXT[];
    v_shown    TEXT;
    n_amount NUMERIC; n_due_date DATE; n_paid DATE; n_notes TEXT; n_vendor UUID;
    v_bill UUID;
BEGIN
    IF v_amount IS NULL AND v_due IS NULL AND v_provider IS NULL THEN
        RETURN;
    END IF;
    IF v_due ~ '^[0-9]{1,2}$' AND v_due::int BETWEEN 1 AND 12 THEN
        v_month := v_due::int;
    END IF;

    SELECT bl.*, bv.name AS vendor_name INTO b
      FROM bills bl
      LEFT JOIN vendors bv ON bv.id = bl.vendor_id AND bv.household_id = bl.household_id
     WHERE bl.household_id = p_household AND bl.asset_id IS NULL
       AND ((p_property IS NOT NULL AND bl.property_id = p_property)
         OR (p_vehicle  IS NOT NULL AND bl.vehicle_id  = p_vehicle))
       AND (bl.name ILIKE ANY (p_patterns) OR bl.category ILIKE ANY (p_patterns))
     ORDER BY bl.created_at, bl.id
     LIMIT 1
       FOR UPDATE OF bl;

    IF NOT FOUND THEN
        INSERT INTO bills (household_id, name, amount, category, frequency, due_date,
                           property_id, vehicle_id, vendor_id, budget_category_id, notes)
        VALUES (p_household, p_name,
                round(v_amount * p_input_periods / pg_temp.periods(p_frequency), 2),
                p_category, p_frequency,
                CASE WHEN p_frequency <> 'monthly' THEN pg_temp.next_month_start(v_month) END,
                p_property, p_vehicle,
                CASE WHEN v_provider IS NOT NULL THEN pg_temp.provider_vendor(p_household, v_provider) END,
                (SELECT bc.id FROM budget_categories bc
                  WHERE bc.household_id = p_household AND lower(btrim(bc.name)) = lower(btrim(p_category))
                  ORDER BY bc.created_at, bc.id LIMIT 1),
                format('Copied from the %s''s details by migration 069.', p_source)
                || CASE WHEN v_due IS NOT NULL AND (v_month IS NULL OR p_frequency = 'monthly')
                        THEN E'\nDue months: ' || v_due ELSE '' END)
        RETURNING id INTO v_bill;
        RETURN;
    END IF;

    v_overwrite := pg_temp.unedited_044_copy(b.notes, b.created_at, b.updated_at);
    n_amount := b.amount; n_due_date := b.due_date; n_paid := b.paid_for_due_date;
    n_notes := b.notes; n_vendor := b.vendor_id;

    -- Amount, compared in the column's period as the API shows it.
    IF v_amount IS NOT NULL
       AND (b.amount IS NULL
            OR round(b.amount * pg_temp.periods(b.frequency) / p_input_periods, 2) <> round(v_amount, 2)) THEN
        IF COALESCE(b.amount, 0) = 0 OR v_overwrite THEN
            n_amount := round(v_amount * p_input_periods / pg_temp.periods(b.frequency), 2);
        ELSE
            v_conflicts := v_conflicts || format('%s %s', p_amount_label, v_amount);
        END IF;
    END IF;

    -- Due month: a single month on a bill that is not monthly is its
    -- due_date; anything else is the "Due months:" notes line.
    IF v_due IS NOT NULL THEN
        IF v_month IS NOT NULL AND b.frequency <> 'monthly' THEN
            IF b.due_date IS NULL OR EXTRACT(MONTH FROM b.due_date)::int <> v_month THEN
                IF b.due_date IS NULL OR v_overwrite THEN
                    n_due_date := pg_temp.next_month_start(v_month);
                    n_paid := NULL;
                    n_notes := pg_temp.with_due_months(n_notes, NULL);
                ELSE
                    v_conflicts := v_conflicts || format('%s %s', p_due_label, v_due);
                END IF;
            END IF;
        ELSE
            v_shown := COALESCE(EXTRACT(MONTH FROM b.due_date)::int::text,
                                substring(b.notes from 'Due months: ([^\n]*)'));
            IF v_shown IS DISTINCT FROM v_due THEN
                IF v_shown IS NULL OR v_overwrite THEN
                    n_notes := pg_temp.with_due_months(n_notes, v_due);
                ELSE
                    v_conflicts := v_conflicts || format('%s %s', p_due_label, v_due);
                END IF;
            END IF;
        END IF;
    END IF;

    -- Insurance provider: the bill's vendor, else 044's "(Provider)" suffix.
    IF v_provider IS NOT NULL THEN
        v_shown := COALESCE(b.vendor_name, substring(b.name from '\(([^()]+)\)\s*$'));
        IF v_shown IS NULL OR lower(btrim(v_shown)) <> lower(v_provider) THEN
            IF v_shown IS NULL OR v_overwrite THEN
                n_vendor := pg_temp.provider_vendor(p_household, v_provider);
            ELSE
                v_conflicts := v_conflicts || format('insurance provider %s', v_provider);
            END IF;
        END IF;
    END IF;

    IF cardinality(v_conflicts) > 0 THEN
        n_notes := pg_temp.with_line(n_notes, format('Migration 069: the %s''s details also said %s.',
                                                     p_source, array_to_string(v_conflicts, ', ')));
    END IF;

    IF n_amount IS NOT DISTINCT FROM b.amount AND n_due_date IS NOT DISTINCT FROM b.due_date
       AND n_notes IS NOT DISTINCT FROM b.notes AND n_vendor IS NOT DISTINCT FROM b.vendor_id THEN
        RETURN;
    END IF;

    UPDATE bills
       SET amount = n_amount,
           due_date = n_due_date,
           paid_for_due_date = n_paid,
           notes = n_notes,
           vendor_id = n_vendor,
           updated_at = NOW()
     WHERE id = b.id;

    -- The mirror event shows the amount and the schedule.
    IF n_amount IS DISTINCT FROM b.amount OR n_due_date IS DISTINCT FROM b.due_date THEN
        PERFORM pg_temp.drop_bill_event(p_household, b.id);
    END IF;
END;
$$;

-- ---------------------------------------------------------------------------
-- 1. Copy
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    p RECORD;
    v RECORD;
    v_name TEXT;
BEGIN
    FOR p IN SELECT * FROM properties ORDER BY created_at, id LOOP
        PERFORM pg_temp.apply_loan(p.household_id, p.id, NULL, ARRAY['%mortgage%'],
            p.name || ' mortgage', 'property', 'mortgage',
            p.mortgage_amount, p.mortgage_rate, p.mortgage_term_months, p.mortgage_start_date,
            p.mortgage_lender, p.mortgage_account_number, NULL);
        PERFORM pg_temp.apply_bill(p.household_id, p.id, NULL, ARRAY['%tax%'],
            p.name || ' property tax', 'property', 'Taxes', 'annual', 1,
            p.property_tax_annual, p.property_tax_due_months, NULL,
            'property tax (annual)', 'property tax due months');
        PERFORM pg_temp.apply_bill(p.household_id, p.id, NULL, ARRAY['%insur%'],
            p.name || ' insurance', 'property', 'Insurance', 'annual', 1,
            p.insurance_annual, NULL, p.insurance_provider,
            'insurance (annual)', NULL);
        PERFORM pg_temp.apply_bill(p.household_id, p.id, NULL, ARRAY['%hoa%', '%association%'],
            p.name || ' HOA fee', 'property', 'Housing', 'monthly', 12,
            p.hoa_fee_monthly, NULL, NULL,
            'HOA fee (monthly)', NULL);
    END LOOP;

    FOR v IN SELECT * FROM vehicles ORDER BY created_at, id LOOP
        v_name := concat_ws(' ', v.year::text, NULLIF(btrim(v.make), ''), NULLIF(btrim(v.model), ''));
        PERFORM pg_temp.apply_loan(v.household_id, NULL, v.id, ARRAY[]::TEXT[],
            concat_ws(' ', NULLIF(v_name, ''), 'loan'), 'vehicle', 'loan',
            v.loan_amount, NULL, v.loan_term_months, NULL,
            v.lender, NULL, v.monthly_payment);
        PERFORM pg_temp.apply_bill(v.household_id, NULL, v.id, ARRAY['%regist%'],
            concat_ws(' ', NULLIF(v_name, ''), 'registration'), 'vehicle', 'Vehicle', 'annual', 1,
            v.registration_cost, v.registration_renewal_month::text, NULL,
            'registration (annual)', 'registration renewal month');
        PERFORM pg_temp.apply_bill(v.household_id, NULL, v.id, ARRAY['%insur%'],
            concat_ws(' ', NULLIF(v_name, ''), 'insurance'), 'vehicle', 'Insurance', 'annual', 1,
            v.insurance_cost, NULL, v.insurance_provider,
            'insurance (annual)', NULL);
    END LOOP;
END;
$$;

DROP FUNCTION pg_temp.apply_bill(UUID, UUID, UUID, TEXT[], TEXT, TEXT, TEXT, TEXT, INT, NUMERIC, TEXT, TEXT, TEXT, TEXT);
DROP FUNCTION pg_temp.apply_loan(UUID, UUID, UUID, TEXT[], TEXT, TEXT, TEXT, NUMERIC, NUMERIC, INT, DATE, TEXT, TEXT, NUMERIC);
DROP FUNCTION pg_temp.provider_vendor(UUID, TEXT);
DROP FUNCTION pg_temp.drop_bill_event(UUID, UUID);
DROP FUNCTION pg_temp.unedited_044_copy(TEXT, TIMESTAMPTZ, TIMESTAMPTZ);
DROP FUNCTION pg_temp.with_due_months(TEXT, TEXT);
DROP FUNCTION pg_temp.with_line(TEXT, TEXT);
DROP FUNCTION pg_temp.periods(TEXT);
DROP FUNCTION pg_temp.next_month_start(INT);

-- ---------------------------------------------------------------------------
-- 2. Drop the columns (added by 020)
-- ---------------------------------------------------------------------------

ALTER TABLE properties
    DROP COLUMN mortgage_amount,
    DROP COLUMN mortgage_rate,
    DROP COLUMN mortgage_term_months,
    DROP COLUMN mortgage_start_date,
    DROP COLUMN mortgage_lender,
    DROP COLUMN mortgage_account_number,
    DROP COLUMN property_tax_annual,
    DROP COLUMN property_tax_due_months,
    DROP COLUMN insurance_annual,
    DROP COLUMN insurance_provider,
    DROP COLUMN hoa_fee_monthly;

ALTER TABLE vehicles
    DROP COLUMN lender,
    DROP COLUMN loan_amount,
    DROP COLUMN loan_term_months,
    DROP COLUMN monthly_payment,
    DROP COLUMN registration_cost,
    DROP COLUMN registration_renewal_month,
    DROP COLUMN insurance_cost,
    DROP COLUMN insurance_provider;

COMMIT;
