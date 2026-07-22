-- Migration 020: Down — revert family model extensions.
-- Reverse order of up migration.

-- 5. Revert bills polymorphic columns
ALTER TABLE bills ALTER COLUMN property_id SET NOT NULL;
ALTER TABLE bills DROP COLUMN IF EXISTS payment_url;
ALTER TABLE bills DROP COLUMN IF EXISTS account_number;
ALTER TABLE bills DROP COLUMN IF EXISTS is_autopay;
ALTER TABLE bills DROP COLUMN IF EXISTS paid_date;
ALTER TABLE bills DROP COLUMN IF EXISTS entity_id;
ALTER TABLE bills DROP COLUMN IF EXISTS entity_type;

-- 4. Revert pet fields
ALTER TABLE pets DROP COLUMN IF EXISTS registration_id;
ALTER TABLE pets DROP COLUMN IF EXISTS insurance_policy;
ALTER TABLE pets DROP COLUMN IF EXISTS insurance_provider;
ALTER TABLE pets DROP COLUMN IF EXISTS microchip_id;

-- 3. Revert vehicle financial columns
ALTER TABLE vehicles DROP COLUMN IF EXISTS insurance_cost;
ALTER TABLE vehicles DROP COLUMN IF EXISTS insurance_provider;
ALTER TABLE vehicles DROP COLUMN IF EXISTS registration_cost;
ALTER TABLE vehicles DROP COLUMN IF EXISTS registration_renewal_month;
ALTER TABLE vehicles DROP COLUMN IF EXISTS monthly_payment;
ALTER TABLE vehicles DROP COLUMN IF EXISTS loan_term_months;
ALTER TABLE vehicles DROP COLUMN IF EXISTS loan_amount;
ALTER TABLE vehicles DROP COLUMN IF EXISTS lender;
ALTER TABLE vehicles DROP COLUMN IF EXISTS purchase_date;
ALTER TABLE vehicles DROP COLUMN IF EXISTS purchase_price;

-- 2. Revert property financial columns
ALTER TABLE properties DROP COLUMN IF EXISTS hoa_fee_monthly;
ALTER TABLE properties DROP COLUMN IF EXISTS insurance_provider;
ALTER TABLE properties DROP COLUMN IF EXISTS insurance_annual;
ALTER TABLE properties DROP COLUMN IF EXISTS property_tax_due_months;
ALTER TABLE properties DROP COLUMN IF EXISTS property_tax_annual;
ALTER TABLE properties DROP COLUMN IF EXISTS mortgage_account_number;
ALTER TABLE properties DROP COLUMN IF EXISTS mortgage_lender;
ALTER TABLE properties DROP COLUMN IF EXISTS mortgage_start_date;
ALTER TABLE properties DROP COLUMN IF EXISTS mortgage_term_months;
ALTER TABLE properties DROP COLUMN IF EXISTS mortgage_rate;
ALTER TABLE properties DROP COLUMN IF EXISTS mortgage_amount;
ALTER TABLE properties DROP COLUMN IF EXISTS down_payment;
ALTER TABLE properties DROP COLUMN IF EXISTS current_value;
ALTER TABLE properties DROP COLUMN IF EXISTS purchase_date;
ALTER TABLE properties DROP COLUMN IF EXISTS purchase_price;

-- 1. Drop notes table
DROP TABLE IF EXISTS notes;