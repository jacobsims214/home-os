ALTER TABLE calendars ADD COLUMN IF NOT EXISTS property_id UUID REFERENCES properties(id) ON DELETE CASCADE;
ALTER TABLE calendar_objects ADD COLUMN IF NOT EXISTS event_type VARCHAR(50) NOT NULL DEFAULT 'custom';
ALTER TABLE households ADD COLUMN IF NOT EXISTS default_property_id UUID REFERENCES properties(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_calendars_property ON calendars(property_id);
