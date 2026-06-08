-- Migration 003: Create assets table
-- Assets belong to households and optionally to a property and room.

CREATE TABLE assets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    property_id UUID REFERENCES properties(id),
    room_id UUID REFERENCES rooms(id),
    name VARCHAR(255) NOT NULL,
    category VARCHAR(100),
    manufacturer VARCHAR(255),
    model VARCHAR(255),
    serial_number VARCHAR(255),
    purchase_date DATE,
    purchase_price NUMERIC(12,2),
    warranty_expiry DATE,
    notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_assets_household ON assets(household_id);
CREATE INDEX idx_assets_property ON assets(property_id);
