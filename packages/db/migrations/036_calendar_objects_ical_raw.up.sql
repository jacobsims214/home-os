-- Keep the full iCalendar text for CalDAV round trips. CalDAV PUT previously
-- stored only a JSON subset, dropping RRULE, EXDATE and VALARM.
--   ical_raw: the complete VCALENDAR as received from a client or generated
--             by the server (NULL for rows written before this migration).
--   rrule:    the event's RRULE value, kept queryable on its own.
-- Both are nullable with no default, so existing rows are untouched.
ALTER TABLE calendar_objects ADD COLUMN IF NOT EXISTS ical_raw TEXT;
ALTER TABLE calendar_objects ADD COLUMN IF NOT EXISTS rrule TEXT;
