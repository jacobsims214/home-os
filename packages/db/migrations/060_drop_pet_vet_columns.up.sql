-- 060_drop_pet_vet_columns.up.sql (#4705, leftover 1 of #4601)
--
-- A pet's vet is a family vendor (pets.vet_vendor_id, composite FK from 043).
-- The free-text pets.vet_name / vet_phone (006) stayed in the schema while
-- api, mcp and ui still wrote them. Nothing reads or writes them any more
-- (#4698 mcp, #4700 api, #4703 ui), so they are dropped here.
--
-- Before the drop, 043 section 4 runs again, unchanged: any pet that still
-- has vet_name / vet_phone but no vet_vendor_id (written by old code after
-- 043 ran) is linked to a same-family vendor with trade 'vet' -- an existing
-- vendor with the same name (case/space-insensitive) and a phone that doesn't
-- contradict, or with the same phone digits when there is no name; otherwise
-- a new vendor. These are the same match rules as apps/api/internal/pet
-- (KB apis/pets.md). It is idempotent: pets that already have
-- vet_vendor_id are not touched, so no vendor is duplicated and no existing
-- link is overwritten.
--
-- Pets that already have a vet_vendor_id lose their vet_name / vet_phone
-- text with the drop; the api has answered with the vendor's name and phone
-- since #4700, so that text is not shown anywhere.
--
-- Runs in one transaction (golang-migrate does not wrap files).

BEGIN;

-- 060 drop pet vet columns
-- ---------------------------------------------------------------------------
-- 1. vet_name / vet_phone -> a 'vet' vendor (043 section 4, verbatim)
-- ---------------------------------------------------------------------------
DO $$
DECLARE
    r        RECORD;
    v_vendor UUID;
    v_name   TEXT;
    v_digits TEXT;
    n_new    INT := 0;
    n_linked INT := 0;
BEGIN
    FOR r IN
        SELECT id, household_id,
               NULLIF(btrim(vet_name), '')  AS vname,
               NULLIF(btrim(vet_phone), '') AS vphone
          FROM pets
         WHERE vet_vendor_id IS NULL
           AND (NULLIF(btrim(vet_name), '') IS NOT NULL OR NULLIF(btrim(vet_phone), '') IS NOT NULL)
         ORDER BY created_at, id
    LOOP
        v_vendor := NULL;
        v_digits := NULLIF(regexp_replace(COALESCE(r.vphone, ''), '\D', '', 'g'), '');
        v_name   := COALESCE(r.vname, 'Vet');

        IF r.vname IS NOT NULL THEN
            -- Same name in the family, and phones that don't contradict.
            SELECT id INTO v_vendor
              FROM vendors
             WHERE household_id = r.household_id
               AND lower(btrim(name)) = lower(r.vname)
               AND (v_digits IS NULL OR phone IS NULL OR btrim(phone) = ''
                    OR regexp_replace(phone, '\D', '', 'g') = v_digits)
             ORDER BY ('vet' = ANY (trades)) DESC, created_at, id
             LIMIT 1;
        ELSE
            -- Phone only: a family vendor with the same number.
            SELECT id INTO v_vendor
              FROM vendors
             WHERE household_id = r.household_id
               AND regexp_replace(COALESCE(phone, ''), '\D', '', 'g') = v_digits
             ORDER BY ('vet' = ANY (trades)) DESC, created_at, id
             LIMIT 1;
        END IF;

        IF v_vendor IS NULL THEN
            INSERT INTO vendors (household_id, name, specialty, phone, trades)
            VALUES (r.household_id, v_name, 'Veterinarian', r.vphone, ARRAY['vet'])
            RETURNING id INTO v_vendor;
            n_new := n_new + 1;
        ELSE
            UPDATE vendors
               SET trades = CASE WHEN 'vet' = ANY (trades) THEN trades ELSE array_append(trades, 'vet') END,
                   phone  = COALESCE(NULLIF(btrim(phone), ''), r.vphone),
                   updated_at = NOW()
             WHERE id = v_vendor
               AND (NOT ('vet' = ANY (trades)) OR NULLIF(btrim(phone), '') IS NULL);
        END IF;

        UPDATE pets SET vet_vendor_id = v_vendor WHERE id = r.id;
        n_linked := n_linked + 1;
    END LOOP;

    IF n_linked > 0 THEN
        RAISE NOTICE '060: linked % pet(s) to a vet vendor (% new vendor(s))', n_linked, n_new;
    END IF;
END
$$;

-- ---------------------------------------------------------------------------
-- 2. drop the free-text columns
-- ---------------------------------------------------------------------------
ALTER TABLE pets DROP COLUMN IF EXISTS vet_name;
ALTER TABLE pets DROP COLUMN IF EXISTS vet_phone;
-- end 060

COMMIT;
