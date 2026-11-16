-- 069_drop_finance_columns.down.sql
--
-- Adds the legacy property and vehicle finance columns back (020's types)
-- and fills them from the loan and bills each subject has now, with the
-- selection rules and conversions the API uses to show them
-- (apps/api/internal/legacyfinance LoanJoin/BillJoin, AmountSQL,
-- DueMonthsTextSQL, ProviderSQL): the oldest same-family row about the
-- property/vehicle (not an asset's), a '%mortgage%' loan first for a
-- property, bills by name or category pattern. A column whose subject has no
-- such row stays NULL, as does a loan amount or payment of 0.
--
-- Loans, bills and vendors created by 069 up are KEPT: the code before 069
-- (#4704 on) reads only loans and bills, so deleting them would hide values
-- from it. 069 up run again finds every column equal to its row and changes
-- nothing.
--
-- Not restored (data loss on down):
--   * column values 069 up could not place because the row had been edited
--     to a different value: the columns get the row's value; the old column
--     value stays only in the row's notes ("Migration 069: ... also said").
--   * a value whose row has since been deleted, and any other loan or bill
--     beyond the one the API shows (e.g. a second tax bill).
--   * a registration_renewal_month outside 1-12: 069 up kept it only as a
--     "Due months:" line in the bill's notes; the column comes back from
--     the bill's due_date.
--   * the exact text of property_tax_due_months when it was a single month
--     with a leading zero ('04' comes back as '4'), and a rate of 100 or more
--     (only in notes).
--   * the Bills-calendar events 069 up deleted are not put back; the worker's
--     backfill had already rebuilt them from the bills.

BEGIN;

ALTER TABLE properties ADD COLUMN IF NOT EXISTS mortgage_amount NUMERIC(12,2);
ALTER TABLE properties ADD COLUMN IF NOT EXISTS mortgage_rate NUMERIC(6,3);
ALTER TABLE properties ADD COLUMN IF NOT EXISTS mortgage_term_months INT;
ALTER TABLE properties ADD COLUMN IF NOT EXISTS mortgage_start_date DATE;
ALTER TABLE properties ADD COLUMN IF NOT EXISTS mortgage_lender TEXT;
ALTER TABLE properties ADD COLUMN IF NOT EXISTS mortgage_account_number TEXT;
ALTER TABLE properties ADD COLUMN IF NOT EXISTS property_tax_annual NUMERIC(12,2);
ALTER TABLE properties ADD COLUMN IF NOT EXISTS property_tax_due_months TEXT;
ALTER TABLE properties ADD COLUMN IF NOT EXISTS insurance_annual NUMERIC(12,2);
ALTER TABLE properties ADD COLUMN IF NOT EXISTS insurance_provider TEXT;
ALTER TABLE properties ADD COLUMN IF NOT EXISTS hoa_fee_monthly NUMERIC(12,2);

ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS lender TEXT;
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS loan_amount NUMERIC(12,2);
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS loan_term_months INT;
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS monthly_payment NUMERIC(12,2);
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS registration_renewal_month INT;
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS registration_cost NUMERIC(12,2);
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS insurance_provider TEXT;
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS insurance_cost NUMERIC(12,2);

-- Payments per year (legacyfinance periodsSQL).
CREATE FUNCTION pg_temp.periods(p_frequency TEXT)
RETURNS INT
LANGUAGE sql IMMUTABLE
AS $$
    SELECT CASE p_frequency WHEN 'weekly' THEN 52 WHEN 'monthly' THEN 12 WHEN 'quarterly' THEN 4
                            WHEN 'semiannual' THEN 2 ELSE 1 END
$$;

UPDATE properties p
   SET mortgage_amount         = NULLIF(s.l_amount, 0),
       mortgage_rate           = s.l_rate,
       mortgage_term_months    = s.l_term,
       mortgage_start_date     = s.l_start,
       mortgage_lender         = s.l_lender,
       mortgage_account_number = s.l_account,
       property_tax_annual     = round(s.t_amount * pg_temp.periods(s.t_frequency) / 1.0, 2),
       property_tax_due_months = COALESCE(EXTRACT(MONTH FROM s.t_due_date)::int::text,
                                          substring(s.t_notes from 'Due months: ([^\n]*)')),
       insurance_annual        = round(s.i_amount * pg_temp.periods(s.i_frequency) / 1.0, 2),
       insurance_provider      = COALESCE(s.i_vendor, substring(s.i_name from '\(([^()]+)\)\s*$')),
       hoa_fee_monthly         = round(s.h_amount * pg_temp.periods(s.h_frequency) / 12.0, 2)
  FROM (
       SELECT pp.id,
              l.original_amount AS l_amount, l.interest_rate AS l_rate, l.term_months AS l_term,
              l.start_date AS l_start, l.lender AS l_lender, l.account_number AS l_account,
              t.amount AS t_amount, t.frequency AS t_frequency, t.due_date AS t_due_date, t.notes AS t_notes,
              i.amount AS i_amount, i.frequency AS i_frequency, i.name AS i_name, i.vendor_name AS i_vendor,
              h.amount AS h_amount, h.frequency AS h_frequency
         FROM properties pp
         LEFT JOIN LATERAL (
              SELECT x.* FROM loans x
               WHERE x.household_id = pp.household_id AND x.property_id = pp.id AND x.asset_id IS NULL
               ORDER BY (x.name ILIKE '%mortgage%') DESC, x.created_at, x.id
               LIMIT 1) l ON true
         LEFT JOIN LATERAL (
              SELECT x.* FROM bills x
               WHERE x.household_id = pp.household_id AND x.property_id = pp.id AND x.asset_id IS NULL
                 AND (x.name ILIKE '%tax%' OR x.category ILIKE '%tax%')
               ORDER BY x.created_at, x.id
               LIMIT 1) t ON true
         LEFT JOIN LATERAL (
              SELECT x.amount, x.frequency, x.name, xv.name AS vendor_name FROM bills x
                LEFT JOIN vendors xv ON xv.id = x.vendor_id AND xv.household_id = x.household_id
               WHERE x.household_id = pp.household_id AND x.property_id = pp.id AND x.asset_id IS NULL
                 AND (x.name ILIKE '%insur%' OR x.category ILIKE '%insur%')
               ORDER BY x.created_at, x.id
               LIMIT 1) i ON true
         LEFT JOIN LATERAL (
              SELECT x.* FROM bills x
               WHERE x.household_id = pp.household_id AND x.property_id = pp.id AND x.asset_id IS NULL
                 AND (x.name ILIKE ANY (ARRAY['%hoa%', '%association%'])
                   OR x.category ILIKE ANY (ARRAY['%hoa%', '%association%']))
               ORDER BY x.created_at, x.id
               LIMIT 1) h ON true
       ) s
 WHERE s.id = p.id
   AND (s.l_amount IS NOT NULL OR s.t_amount IS NOT NULL OR s.t_due_date IS NOT NULL OR s.t_notes IS NOT NULL
        OR s.i_amount IS NOT NULL OR s.i_name IS NOT NULL OR s.h_amount IS NOT NULL);

UPDATE vehicles v
   SET lender                     = s.l_lender,
       loan_amount                = NULLIF(s.l_amount, 0),
       loan_term_months           = s.l_term,
       monthly_payment            = NULLIF(s.l_payment, 0),
       registration_cost          = round(s.r_amount * pg_temp.periods(s.r_frequency) / 1.0, 2),
       registration_renewal_month = EXTRACT(MONTH FROM s.r_due_date)::int,
       insurance_cost             = round(s.i_amount * pg_temp.periods(s.i_frequency) / 1.0, 2),
       insurance_provider         = COALESCE(s.i_vendor, substring(s.i_name from '\(([^()]+)\)\s*$'))
  FROM (
       SELECT vv.id,
              l.original_amount AS l_amount, l.term_months AS l_term, l.lender AS l_lender,
              l.monthly_payment AS l_payment,
              r.amount AS r_amount, r.frequency AS r_frequency, r.due_date AS r_due_date,
              i.amount AS i_amount, i.frequency AS i_frequency, i.name AS i_name, i.vendor_name AS i_vendor
         FROM vehicles vv
         LEFT JOIN LATERAL (
              SELECT x.* FROM loans x
               WHERE x.household_id = vv.household_id AND x.vehicle_id = vv.id AND x.asset_id IS NULL
               ORDER BY x.created_at, x.id
               LIMIT 1) l ON true
         LEFT JOIN LATERAL (
              SELECT x.* FROM bills x
               WHERE x.household_id = vv.household_id AND x.vehicle_id = vv.id AND x.asset_id IS NULL
                 AND (x.name ILIKE '%regist%' OR x.category ILIKE '%regist%')
               ORDER BY x.created_at, x.id
               LIMIT 1) r ON true
         LEFT JOIN LATERAL (
              SELECT x.amount, x.frequency, x.name, xv.name AS vendor_name FROM bills x
                LEFT JOIN vendors xv ON xv.id = x.vendor_id AND xv.household_id = x.household_id
               WHERE x.household_id = vv.household_id AND x.vehicle_id = vv.id AND x.asset_id IS NULL
                 AND (x.name ILIKE '%insur%' OR x.category ILIKE '%insur%')
               ORDER BY x.created_at, x.id
               LIMIT 1) i ON true
       ) s
 WHERE s.id = v.id
   AND (s.l_amount IS NOT NULL OR s.r_amount IS NOT NULL OR s.r_due_date IS NOT NULL
        OR s.i_amount IS NOT NULL OR s.i_name IS NOT NULL);

DROP FUNCTION pg_temp.periods(TEXT);

COMMIT;
