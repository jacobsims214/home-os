-- Migration 020: Family model extensions
-- Adds notes table, property/vehicle/pet financial fields, polymorphic bills.
-- Every entity can now have notes, financial tracking, and bills.

-- 1. Notes table — polymorphic notes for any entity
CREATE TABLE notes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    entity_type VARCHAR(50) NOT NULL,
    entity_id UUID NOT NULL,
    title VARCHAR(500),
    body TEXT NOT NULL,
    author_id UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_notes_entity ON notes(entity_type, entity_id);
CREATE INDEX idx_notes_household ON notes(household_id);

-- 2. Property financial columns
ALTER TABLE properties ADD COLUMN IF NOT EXISTS purchase_price NUMERIC(12,2);
ALTER TABLE properties ADD COLUMN IF NOT EXISTS purchase_date DATE;
ALTER TABLE properties ADD COLUMN IF NOT EXISTS current_value NUMERIC(12,2);
ALTER TABLE properties ADD COLUMN IF NOT EXISTS down_payment NUMERIC(12,2);
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

-- 3. Vehicle financial columns
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS purchase_price NUMERIC(12,2);
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS purchase_date DATE;
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS lender TEXT;
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS loan_amount NUMERIC(12,2);
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS loan_term_months INT;
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS monthly_payment NUMERIC(12,2);
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS registration_renewal_month INT;
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS registration_cost NUMERIC(12,2);
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS insurance_provider TEXT;
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS insurance_cost NUMERIC(12,2);

-- 4. Pet fields
ALTER TABLE pets ADD COLUMN IF NOT EXISTS microchip_id TEXT;
ALTER TABLE pets ADD COLUMN IF NOT EXISTS insurance_provider TEXT;
ALTER TABLE pets ADD COLUMN IF NOT EXISTS insurance_policy TEXT;
ALTER TABLE pets ADD COLUMN IF NOT EXISTS registration_id TEXT;

-- 5. Restructure bills for polymorphic support
ALTER TABLE bills ADD COLUMN IF NOT EXISTS entity_type VARCHAR(50) DEFAULT 'property';
ALTER TABLE bills ADD COLUMN IF NOT EXISTS entity_id UUID;
ALTER TABLE bills ADD COLUMN IF NOT EXISTS paid_date DATE;
ALTER TABLE bills ADD COLUMN IF NOT EXISTS is_autopay BOOLEAN DEFAULT false;
ALTER TABLE bills ADD COLUMN IF NOT EXISTS account_number TEXT;
ALTER TABLE bills ADD COLUMN IF NOT EXISTS payment_url TEXT;

-- Migrate existing: set entity_id = property_id for all bills that have one
UPDATE bills SET entity_id = property_id, entity_type = 'property' WHERE property_id IS NOT NULL AND entity_id IS NULL;

-- Make property_id nullable (old column, kept for backward compat)
ALTER TABLE bills ALTER COLUMN property_id DROP NOT NULL;