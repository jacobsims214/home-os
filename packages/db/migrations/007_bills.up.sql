CREATE TABLE bills (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    property_id UUID REFERENCES properties(id),
    name VARCHAR(255) NOT NULL,
    amount NUMERIC(12,2),
    due_day INT,
    category VARCHAR(100),
    vendor_id UUID REFERENCES vendors(id),
    rrule TEXT,
    notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
