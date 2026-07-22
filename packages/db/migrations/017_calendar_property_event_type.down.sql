DROP INDEX IF EXISTS idx_calendars_property;
ALTER TABLE calendars DROP COLUMN IF EXISTS property_id;
ALTER TABLE calendar_objects DROP COLUMN IF EXISTS event_type;
ALTER TABLE households DROP COLUMN IF EXISTS default_property_id;
