-- 064_membership_vendor_link.down.sql (#4718)
--
-- Drops memberships.vendor_id. Run the calendar build from before #4718 with
-- it: that build reads memberships.vendor_id.

BEGIN;

DROP INDEX IF EXISTS idx_memberships_vendor_id;
ALTER TABLE memberships DROP CONSTRAINT IF EXISTS memberships_vendor_household_fkey;
ALTER TABLE memberships DROP COLUMN IF EXISTS vendor_id;

COMMIT;
