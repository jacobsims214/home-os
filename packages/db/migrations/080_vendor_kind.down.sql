-- 080_vendor_kind.down.sql  (#5529)
--
-- Reverses 080 exactly: the index, the column, the type. There is nothing to
-- restore from elsewhere -- kind was DERIVED by 080's backfill (trades,
-- maintenance_tasks.vendor_id, pets.vet_vendor_id, memberships.vendor_id,
-- bills.vendor_id all still hold the evidence), so a later re-up classifies
-- every row the same way it did the first time. What is lost is a change a
-- PERSON made to kind after 080 ran (a contact reclassified by hand); it has
-- no other home, and that is stated here rather than papered over.
--
-- IF EXISTS throughout: the down is safe to run twice and safe to run against
-- a database where the up failed part-way (golang-migrate can leave the
-- transaction undone but a retried down should never fail on absence).
--
-- Runs in one transaction.

BEGIN;

DROP TRIGGER IF EXISTS vendors_promote_on_trades ON vendors;

DROP FUNCTION IF EXISTS vendors_promote_on_trades();

DROP INDEX IF EXISTS idx_vendors_household_kind;

ALTER TABLE vendors DROP COLUMN IF EXISTS kind;

DROP TYPE IF EXISTS vendor_kind;

COMMIT;
