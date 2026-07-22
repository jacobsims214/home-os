-- Migration 021: Down — removes caldav_password_hash column
ALTER TABLE users DROP COLUMN IF EXISTS caldav_password_hash;