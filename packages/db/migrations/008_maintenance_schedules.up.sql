CREATE TABLE maintenance_schedules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    property_id UUID REFERENCES properties(id),
    asset_id UUID REFERENCES assets(id),
    name VARCHAR(255) NOT NULL,
    description TEXT,
    rrule TEXT NOT NULL,
    estimated_cost NUMERIC(12,2),
    vendor_id UUID REFERENCES vendors(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
