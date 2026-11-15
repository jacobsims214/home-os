-- 043_vendor_trades.down.sql
--
-- Removes vendors.trades and pets.vet_vendor_id (ticket #4452).
--
-- Before the link is dropped, a pet with an empty vet_name / vet_phone takes
-- them from its vet vendor, so a vet chosen only through vet_vendor_id is not
-- lost. Vendors that 043 up created for vets are kept (they are ordinary
-- family vendors now, and may have been edited); a later 043 up finds them
-- again by name, so up/down/up creates no duplicates. Trades are lost.
--
-- 043 touched neither entity_household() nor the entity_kind domain, so
-- nothing to restore there.

BEGIN;

-- 043 vendor trades
UPDATE pets p
   SET vet_name  = COALESCE(NULLIF(btrim(p.vet_name), ''), v.name),
       vet_phone = COALESCE(NULLIF(btrim(p.vet_phone), ''), v.phone)
  FROM vendors v
 WHERE v.id = p.vet_vendor_id
   AND (NULLIF(btrim(p.vet_name), '') IS NULL OR NULLIF(btrim(p.vet_phone), '') IS NULL);

DROP INDEX IF EXISTS idx_pets_vet_vendor_id;
ALTER TABLE pets DROP CONSTRAINT IF EXISTS pets_vet_vendor_fkey;
ALTER TABLE pets DROP COLUMN IF EXISTS vet_vendor_id;
ALTER TABLE vendors DROP CONSTRAINT IF EXISTS vendors_id_household_id_key;

DROP INDEX IF EXISTS idx_vendors_trades;
ALTER TABLE vendors DROP CONSTRAINT IF EXISTS vendors_trades_check;
ALTER TABLE vendors DROP COLUMN IF EXISTS trades;
-- end 043

COMMIT;
