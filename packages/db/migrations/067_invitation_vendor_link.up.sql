-- 067_invitation_vendor_link.up.sql (#4799)
--
-- An invitation with role 'vendor' may name the family's vendors row the
-- invitee works for. Accepting the invitation copies it to
-- memberships.vendor_id (064), so the new login sees only that vendor's
-- maintenance jobs in the API, MCP and CalDAV.
--
-- invitations.vendor_id: nullable. The composite FK (vendor_id,
-- household_id) -> vendors(id, household_id) (UNIQUE key from 043) keeps the
-- vendor in the invitation's own family. Deleting the vendors row clears
-- only vendor_id (PG15+ column list on SET NULL, as in 043/064): the invite
-- stays valid and then makes an unlinked vendor.
--
-- Runs in one transaction.

BEGIN;

ALTER TABLE invitations ADD COLUMN IF NOT EXISTS vendor_id UUID;

ALTER TABLE invitations DROP CONSTRAINT IF EXISTS invitations_vendor_household_fkey;
ALTER TABLE invitations ADD CONSTRAINT invitations_vendor_household_fkey
    FOREIGN KEY (vendor_id, household_id) REFERENCES vendors (id, household_id)
    ON DELETE SET NULL (vendor_id);

CREATE INDEX IF NOT EXISTS idx_invitations_vendor_id
    ON invitations (vendor_id) WHERE vendor_id IS NOT NULL;

COMMENT ON COLUMN invitations.vendor_id IS
    'For role vendor: the family vendors row the invitee works for; copied to memberships.vendor_id on accept (#4799).';

COMMIT;
