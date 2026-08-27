CREATE TABLE loans (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    entity_type VARCHAR(50),
    entity_id UUID,
    lender VARCHAR(255),
    original_amount NUMERIC(12,2) NOT NULL,
    interest_rate NUMERIC(5,3),
    term_months INTEGER,
    monthly_payment NUMERIC(12,2),
    remaining_balance NUMERIC(12,2) NOT NULL,
    start_date DATE,
    notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_loans_household ON loans(household_id);
CREATE INDEX idx_loans_entity ON loans(entity_type, entity_id);

-- Ensure the app user has access to the loans table
GRANT ALL ON TABLE loans TO app;