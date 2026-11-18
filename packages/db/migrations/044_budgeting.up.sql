-- 044_budgeting.up.sql
--
-- Stage 2: budgeting metadata (KB decisions/domain-model-v2.md, "Contracts
-- for the Stage 2 migrations" -> 044 budgeting; ticket #4453). It answers
-- "when is the mortgage due, how much, what rate?". It stores metadata only:
-- no transactions and no ledger (KB decisions/2026-11-15-home-os-product-direction.md).
--
-- What this adds:
--   1. bills.frequency ('monthly' | 'quarterly' | 'semiannual' | 'annual' |
--      'once', default 'monthly', so every existing bill becomes monthly) and
--      bills.due_date, the next or only due date of a bill that is not monthly.
--      A monthly bill keeps using due_day. A bill that is not monthly has no
--      due_day: the worker's bill_due job treats every due_day as monthly.
--   2. Loans get due_day (day of the month the payment is due), term_end
--      (backfilled from start_date + term_months) and account_number.
--      interest_rate, lender and monthly_payment already exist (032). The
--      current balance is the existing remaining_balance (NOT NULL), so no
--      second column that could disagree with it is added.
--   3. budget_categories: family-owned, one row per category with a monthly
--      amount. bills and loans can point at one (budget_category_id,
--      same-family composite FK, ON DELETE SET NULL). Kind 'budget' joins
--      the entity registry, so notes, files, secrets and links can be about
--      a budget category and are cleaned up when it is deleted.
--   4. Copies the finance columns on properties and vehicles into subject
--      loans and bills. A column is skipped when the same subject already
--      has an equivalent row, so re-running creates nothing new.
--        properties.mortgage_*            -> loans(property_id)
--        properties.property_tax_annual   -> bills(property_id), annual
--        properties.insurance_annual/_provider -> bills(property_id), annual
--        properties.hoa_fee_monthly       -> bills(property_id), monthly
--        vehicles.lender/loan_amount/monthly_payment/loan_term_months -> loans(vehicle_id)
--        vehicles.registration_cost/_renewal_month -> bills(vehicle_id), annual
--        vehicles.insurance_cost/_provider -> bills(vehicle_id), annual
--      The copies also fill the legacy entity_type/entity_id, because code
--      written before #4449 still reads those columns.
--
-- The property and vehicle finance columns are NOT dropped here. The api
-- (property, vehicle, seed/demo.go), mcp (property, vehicle, finance) and ui
-- still read and write them. 040 (#4601) drops them, after re-running
-- section 4 so that values written in the meantime are copied too.
--
-- Copied rows carry a notes line that starts with COPY_MARKER (below). 044
-- down deletes copies that were never edited (updated_at = created_at).
--
-- Runs in one transaction. Section 4 is idempotent.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. bills: frequency and due_date
-- ---------------------------------------------------------------------------

ALTER TABLE bills ADD COLUMN frequency VARCHAR(20) NOT NULL DEFAULT 'monthly';
ALTER TABLE bills ADD COLUMN due_date DATE;
ALTER TABLE bills ADD CONSTRAINT bills_frequency_check
    CHECK (frequency IN ('monthly', 'quarterly', 'semiannual', 'annual', 'once'));
-- due_day means "every month on this day". A bill that is not monthly is due
-- on due_date instead.
ALTER TABLE bills ADD CONSTRAINT bills_due_day_monthly_check
    CHECK (frequency = 'monthly' OR due_day IS NULL);

CREATE INDEX idx_bills_due_date ON bills (household_id, due_date) WHERE due_date IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 2. loans: due_day, term_end, account_number
-- ---------------------------------------------------------------------------

ALTER TABLE loans ADD COLUMN due_day INT;
ALTER TABLE loans ADD COLUMN term_end DATE;
ALTER TABLE loans ADD COLUMN account_number TEXT;
ALTER TABLE loans ADD CONSTRAINT loans_due_day_check
    CHECK (due_day IS NULL OR due_day BETWEEN 1 AND 31);
ALTER TABLE loans ADD CONSTRAINT loans_term_end_check
    CHECK (term_end IS NULL OR start_date IS NULL OR term_end >= start_date);

UPDATE loans
   SET term_end = (start_date + make_interval(months => term_months))::date
 WHERE term_end IS NULL AND start_date IS NOT NULL AND term_months > 0;

-- ---------------------------------------------------------------------------
-- 3. budget_categories
-- ---------------------------------------------------------------------------

CREATE TABLE budget_categories (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id    UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    name            VARCHAR(255) NOT NULL CHECK (btrim(name) <> ''),
    monthly_amount  NUMERIC(12,2) CHECK (monthly_amount IS NULL OR monthly_amount >= 0),
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Target of the same-family composite FKs from bills and loans.
    CONSTRAINT budget_categories_id_household_key UNIQUE (id, household_id)
);

-- One "Housing" per family, whatever the case or spacing.
CREATE UNIQUE INDEX idx_budget_categories_household_name
    ON budget_categories (household_id, lower(btrim(name)));

ALTER TABLE bills ADD COLUMN budget_category_id UUID;
ALTER TABLE bills ADD CONSTRAINT bills_budget_category_fkey
    FOREIGN KEY (budget_category_id, household_id)
    REFERENCES budget_categories (id, household_id) ON DELETE SET NULL (budget_category_id);
CREATE INDEX idx_bills_budget_category ON bills (budget_category_id) WHERE budget_category_id IS NOT NULL;

ALTER TABLE loans ADD COLUMN budget_category_id UUID;
ALTER TABLE loans ADD CONSTRAINT loans_budget_category_fkey
    FOREIGN KEY (budget_category_id, household_id)
    REFERENCES budget_categories (id, household_id) ON DELETE SET NULL (budget_category_id);
CREATE INDEX idx_loans_budget_category ON loans (budget_category_id) WHERE budget_category_id IS NOT NULL;

-- Entity registry: kind 'budget' (a budget_categories row).
-- Each Stage 2 migration re-declares the full list (038's kinds plus its own
-- plus every earlier Stage 2 kind). Copied from 041, the latest definition.
ALTER DOMAIN entity_kind DROP CONSTRAINT entity_kind_check;
ALTER DOMAIN entity_kind ADD CONSTRAINT entity_kind_check CHECK (VALUE IN (
    'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
    'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
    'file', 'note', 'secret',
    -- 041
    'person',
    -- 044
    'budget'
));

-- Same body as 041 plus the 044 branch. A later CREATE OR REPLACE must copy
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
        ELSE v_household := NULL;
    END CASE;
    RETURN v_household;
END;
$$;

CREATE TRIGGER budget_categories_rehome_entity_refs AFTER DELETE ON budget_categories
    FOR EACH ROW EXECUTE FUNCTION rehome_entity_refs('budget');

-- ---------------------------------------------------------------------------
-- 4. Copy property and vehicle finance columns into subject loans and bills
-- ---------------------------------------------------------------------------
-- COPY_MARKER: every copied row's notes start with
--   'Copied from the property''s details by migration 044.' or
--   'Copied from the vehicle''s details by migration 044.'
-- 044 down and the tests find copies by this prefix. Keep it in step with
-- 044_budgeting.down.sql.

-- The first day of month p_month that is today or later. Used for annual
-- bills whose source only says which month they are due.
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

-- Does this family already have a loan about this property or vehicle?
-- Both the typed FK and the legacy polymorphic columns count, because code
-- written before #4449 only writes entity_type/entity_id.
CREATE FUNCTION pg_temp.has_loan(p_household UUID, p_kind TEXT, p_id UUID)
RETURNS BOOLEAN
LANGUAGE sql STABLE
AS $$
    SELECT EXISTS (
        SELECT 1 FROM loans l
         WHERE l.household_id = p_household
           AND ((p_kind = 'property' AND l.property_id = p_id)
             OR (p_kind = 'vehicle'  AND l.vehicle_id  = p_id)
             OR (l.entity_type = p_kind AND l.entity_id = p_id)))
$$;

-- Does this family already have a bill about this property or vehicle whose
-- name or category matches p_pattern (an ILIKE pattern)?
CREATE FUNCTION pg_temp.has_bill(p_household UUID, p_kind TEXT, p_id UUID, p_pattern TEXT)
RETURNS BOOLEAN
LANGUAGE sql STABLE
AS $$
    SELECT EXISTS (
        SELECT 1 FROM bills b
         WHERE b.household_id = p_household
           AND ((p_kind = 'property' AND b.property_id = p_id)
             OR (p_kind = 'vehicle'  AND b.vehicle_id  = p_id)
             OR (b.entity_type = p_kind AND b.entity_id = p_id))
           AND (b.name ILIKE p_pattern OR b.category ILIKE p_pattern))
$$;

-- 4a. Property mortgage -> loan about the property.
INSERT INTO loans (household_id, name, entity_type, entity_id, property_id,
                   lender, original_amount, remaining_balance, interest_rate,
                   term_months, start_date, term_end, account_number, notes)
SELECT p.household_id,
       p.name || ' mortgage',
       'property', p.id, p.id,
       NULLIF(btrim(p.mortgage_lender), ''),
       COALESCE(p.mortgage_amount, 0),
       -- The property only records the original amount.
       COALESCE(p.mortgage_amount, 0),
       -- loans.interest_rate is NUMERIC(5,3); a rate of 100 or more is a typo.
       CASE WHEN p.mortgage_rate >= 0 AND p.mortgage_rate < 100 THEN p.mortgage_rate END,
       CASE WHEN p.mortgage_term_months > 0 THEN p.mortgage_term_months END,
       p.mortgage_start_date,
       CASE WHEN p.mortgage_start_date IS NOT NULL AND p.mortgage_term_months > 0
            THEN (p.mortgage_start_date + make_interval(months => p.mortgage_term_months))::date END,
       NULLIF(btrim(p.mortgage_account_number), ''),
       'Copied from the property''s details by migration 044. '
       || 'The remaining balance is the original amount; update it.'
  FROM properties p
 WHERE (COALESCE(p.mortgage_amount, 0) > 0 OR NULLIF(btrim(p.mortgage_lender), '') IS NOT NULL)
   AND NOT pg_temp.has_loan(p.household_id, 'property', p.id);

-- 4b. Property tax -> annual bill about the property. A single due month
-- (e.g. '4') becomes due_date; anything else is kept in the notes.
INSERT INTO bills (household_id, name, amount, category, frequency, due_date,
                   entity_type, entity_id, property_id, notes)
SELECT p.household_id,
       p.name || ' property tax',
       p.property_tax_annual,
       'Taxes',
       'annual',
       CASE WHEN btrim(p.property_tax_due_months) ~ '^[0-9]{1,2}$'
            THEN pg_temp.next_month_start(btrim(p.property_tax_due_months)::int) END,
       'property', p.id, p.id,
       'Copied from the property''s details by migration 044.'
       || COALESCE(E'\nDue months: ' || NULLIF(btrim(p.property_tax_due_months), ''), '')
  FROM properties p
 WHERE p.property_tax_annual > 0
   AND NOT pg_temp.has_bill(p.household_id, 'property', p.id, '%tax%');

-- 4c. Home insurance -> annual bill about the property.
INSERT INTO bills (household_id, name, amount, category, frequency,
                   entity_type, entity_id, property_id, notes)
SELECT p.household_id,
       p.name || ' insurance' || COALESCE(' (' || NULLIF(btrim(p.insurance_provider), '') || ')', ''),
       CASE WHEN p.insurance_annual > 0 THEN p.insurance_annual END,
       'Insurance',
       'annual',
       'property', p.id, p.id,
       'Copied from the property''s details by migration 044.'
  FROM properties p
 WHERE (p.insurance_annual > 0 OR NULLIF(btrim(p.insurance_provider), '') IS NOT NULL)
   AND NOT pg_temp.has_bill(p.household_id, 'property', p.id, '%insur%');

-- 4d. HOA fee -> monthly bill about the property (no due day is known).
INSERT INTO bills (household_id, name, amount, category, frequency,
                   entity_type, entity_id, property_id, notes)
SELECT p.household_id,
       p.name || ' HOA fee',
       p.hoa_fee_monthly,
       'Housing',
       'monthly',
       'property', p.id, p.id,
       'Copied from the property''s details by migration 044.'
  FROM properties p
 WHERE p.hoa_fee_monthly > 0
   AND NOT (pg_temp.has_bill(p.household_id, 'property', p.id, '%hoa%')
         OR pg_temp.has_bill(p.household_id, 'property', p.id, '%association%'));

-- 4e. Vehicle financing -> loan about the vehicle.
INSERT INTO loans (household_id, name, entity_type, entity_id, vehicle_id,
                   lender, original_amount, remaining_balance, term_months,
                   monthly_payment, notes)
SELECT v.household_id,
       concat_ws(' ', v.year::text, NULLIF(btrim(v.make), ''), NULLIF(btrim(v.model), ''), 'loan'),
       'vehicle', v.id, v.id,
       NULLIF(btrim(v.lender), ''),
       COALESCE(v.loan_amount, 0),
       COALESCE(v.loan_amount, 0),
       CASE WHEN v.loan_term_months > 0 THEN v.loan_term_months END,
       CASE WHEN v.monthly_payment > 0 THEN v.monthly_payment END,
       'Copied from the vehicle''s details by migration 044. '
       || 'The remaining balance is the original amount; update it.'
  FROM vehicles v
 WHERE (COALESCE(v.loan_amount, 0) > 0 OR COALESCE(v.monthly_payment, 0) > 0
        OR NULLIF(btrim(v.lender), '') IS NOT NULL)
   AND NOT pg_temp.has_loan(v.household_id, 'vehicle', v.id);

-- 4f. Vehicle registration -> annual bill about the vehicle, due on the 1st
-- of the renewal month.
INSERT INTO bills (household_id, name, amount, category, frequency, due_date,
                   entity_type, entity_id, vehicle_id, notes)
SELECT v.household_id,
       concat_ws(' ', v.year::text, NULLIF(btrim(v.make), ''), NULLIF(btrim(v.model), ''), 'registration'),
       v.registration_cost,
       'Vehicle',
       'annual',
       pg_temp.next_month_start(v.registration_renewal_month),
       'vehicle', v.id, v.id,
       'Copied from the vehicle''s details by migration 044.'
  FROM vehicles v
 WHERE v.registration_cost > 0
   AND NOT pg_temp.has_bill(v.household_id, 'vehicle', v.id, '%regist%');

-- 4g. Vehicle insurance -> annual bill about the vehicle.
INSERT INTO bills (household_id, name, amount, category, frequency,
                   entity_type, entity_id, vehicle_id, notes)
SELECT v.household_id,
       concat_ws(' ', v.year::text, NULLIF(btrim(v.make), ''), NULLIF(btrim(v.model), ''), 'insurance')
       || COALESCE(' (' || NULLIF(btrim(v.insurance_provider), '') || ')', ''),
       CASE WHEN v.insurance_cost > 0 THEN v.insurance_cost END,
       'Insurance',
       'annual',
       'vehicle', v.id, v.id,
       'Copied from the vehicle''s details by migration 044.'
  FROM vehicles v
 WHERE (v.insurance_cost > 0 OR NULLIF(btrim(v.insurance_provider), '') IS NOT NULL)
   AND NOT pg_temp.has_bill(v.household_id, 'vehicle', v.id, '%insur%');

DROP FUNCTION pg_temp.has_bill(UUID, TEXT, UUID, TEXT);
DROP FUNCTION pg_temp.has_loan(UUID, TEXT, UUID);
DROP FUNCTION pg_temp.next_month_start(INT);

COMMIT;
