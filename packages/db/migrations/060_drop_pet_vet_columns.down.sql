-- 060_drop_pet_vet_columns.down.sql (#4705)
--
-- Re-adds pets.vet_name VARCHAR(255) / vet_phone VARCHAR(50) (006's types)
-- and fills them from the linked vet vendor's name and phone, so 043 down
-- (which drops vet_vendor_id) and pre-060 code still see each pet's vet.
--
-- Data loss on down (accepted): text that 060 up dropped is not recovered.
-- A pet with no vet_vendor_id gets NULLs; a pet whose old text differed from
-- its vendor (e.g. a different spelling) gets the vendor's name and phone.
-- Vendors are left as they are; a later 060 up only touches pets with no
-- vet_vendor_id, so up/down/up creates no duplicate vendors.

BEGIN;

-- 060 drop pet vet columns
ALTER TABLE pets ADD COLUMN IF NOT EXISTS vet_name VARCHAR(255);
ALTER TABLE pets ADD COLUMN IF NOT EXISTS vet_phone VARCHAR(50);

UPDATE pets p
   SET vet_name  = COALESCE(NULLIF(btrim(p.vet_name), ''), left(v.name, 255)),
       vet_phone = COALESCE(NULLIF(btrim(p.vet_phone), ''), left(v.phone, 50))
  FROM vendors v
 WHERE v.id = p.vet_vendor_id
   AND v.household_id = p.household_id
   AND (NULLIF(btrim(p.vet_name), '') IS NULL OR NULLIF(btrim(p.vet_phone), '') IS NULL);
-- end 060

COMMIT;
