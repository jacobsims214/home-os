-- Migration 021: CalDAV app password support
-- Adds a dedicated caldav_password_hash column to users table for
-- CalDAV Basic Auth. Apple Calendar and other CalDAV clients use
-- this password instead of the user's main account password.

ALTER TABLE users ADD COLUMN IF NOT EXISTS caldav_password_hash TEXT;