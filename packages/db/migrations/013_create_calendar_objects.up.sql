CREATE TABLE calendar_objects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    calendar_id UUID NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid VARCHAR(500) NOT NULL,
    ical_data TEXT NOT NULL,
    etag VARCHAR(255) NOT NULL,
    entity_type VARCHAR(100),
    entity_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(calendar_id, uid)
);
