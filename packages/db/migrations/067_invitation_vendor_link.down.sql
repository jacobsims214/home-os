-- 067_invitation_vendor_link.down.sql (#4799)
--
-- Drops invitations.vendor_id. Memberships already linked keep their
-- memberships.vendor_id (064). Run the API build from before #4799 with it:
-- this build reads invitations.vendor_id.

BEGIN;

DROP INDEX IF EXISTS idx_invitations_vendor_id;
ALTER TABLE invitations DROP CONSTRAINT IF EXISTS invitations_vendor_household_fkey;
ALTER TABLE invitations DROP COLUMN IF EXISTS vendor_id;

COMMIT;
