-- 064_membership_vendor_link.up.sql (#4718)
--
-- Links a vendor's login (their membership) to the family's vendors row for
-- that business, so a vendor can be shown only THEIR OWN maintenance jobs
-- (role policy, KB decisions/2026-11-15-role-policy.md; mockups #87/#89).
--
-- memberships.vendor_id: nullable. The composite FK (vendor_id,
-- household_id) -> vendors(id, household_id) (the UNIQUE key added in 043)
-- keeps the vendor in the membership's own family. Deleting the vendors row
-- clears only vendor_id (PG15+ column list on SET NULL, as in 043).
--
-- Readers today: apps/calendar (CalDAV). A member with role 'vendor' and a
-- vendor_id sees only maintenance task events whose task has that
-- vendor_id; a vendor without one still sees every maintenance task event.
-- The column is ignored for other roles, so a promotion needs no cleanup.
-- Setting it (invitations / member edit) and the API/MCP filtering are a
-- follow-up.
--
-- Runs in one transaction.

BEGIN;

ALTER TABLE memberships ADD COLUMN IF NOT EXISTS vendor_id UUID;

ALTER TABLE memberships DROP CONSTRAINT IF EXISTS memberships_vendor_household_fkey;
ALTER TABLE memberships ADD CONSTRAINT memberships_vendor_household_fkey
    FOREIGN KEY (vendor_id, household_id) REFERENCES vendors (id, household_id)
    ON DELETE SET NULL (vendor_id);

CREATE INDEX IF NOT EXISTS idx_memberships_vendor_id
    ON memberships (vendor_id) WHERE vendor_id IS NOT NULL;

COMMENT ON COLUMN memberships.vendor_id IS
    'For role vendor: the family vendors row this login works for. CalDAV shows such a vendor only maintenance tasks with this vendor_id (#4718).';

COMMIT;
