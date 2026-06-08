CREATE TYPE calendar_type AS ENUM ('family', 'bills', 'maintenance', 'vehicles', 'properties', 'custom');

CREATE TABLE calendars (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    type calendar_type NOT NULL,
    color VARCHAR(20),
    caldav_uid VARCHAR(255) NOT NULL UNIQUE,
    ctag VARCHAR(255) NOT NULL DEFAULT gen_random_uuid()::text,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
