-- 043_vendor_trades.up.sql
--
-- Stage 2: vendor trades (KB decisions/domain-model-v2.md, "Contracts for the
-- Stage 2 migrations" -> 043 vendor trades; ticket #4452).
--
-- A vendor (the plumber, the vet, the pediatrician) belongs to the family and
-- may have several trades. "Who's our plumber?" becomes
--   SELECT ... FROM vendors WHERE household_id = $1 AND trades @> ARRAY['plumber']
-- served by a GIN index.
--
-- What this adds:
--   1. vendors.trades TEXT[] NOT NULL DEFAULT '{}', no NULL or blank
--      elements, GIN index. The vocabulary is open (lower-case by convention:
--      plumber, electrician, hvac, vet, pediatrician, ...).
--   2. Backfill trades from vendors.specialty where it names a known trade
--      (HVAC -> hvac, Plumbing -> plumber, ...). specialty is kept: api, mcp
--      and ui still read it.
--   3. pets.vet_vendor_id: the pet's vet is a family vendor. The composite FK
--      (vet_vendor_id, household_id) -> vendors(id, household_id) keeps the
--      vet in the pet's family; deleting the vendor clears only vet_vendor_id
--      (PG15+ column list on SET NULL).
--   4. Backfill: each pet with vet_name/vet_phone gets a vendor with trade
--      'vet' -- an existing family vendor with the same name (and a
--      compatible phone), or the same phone when there is no name; otherwise a
--      new vendor. Pets of one family that share a vet share the vendor.
--
-- Contact fields: vendors already has phone, email, website and notes (004),
-- so none are added.
--
-- pets.vet_name / vet_phone are NOT dropped here: apps/api/internal/pet,
-- apps/mcp/internal/pet, seed/demo.go and the ui still read and write them.
-- 040 (#4601) drops them together with the code change, and must re-run
-- section 4 first (it is idempotent: only pets with vet_vendor_id IS NULL are
-- touched) so pets written by old code in the meantime get their vendor.
--
-- 043 adds no entity kind: 'vendor' is already registered (038), so
-- entity_household() and the entity_kind domain are untouched. Their latest
-- definitions are in 041_people.up.sql.
--
-- Runs in one transaction (golang-migrate does not wrap files).

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. vendors.trades
-- ---------------------------------------------------------------------------
-- 043 vendor trades
ALTER TABLE vendors ADD COLUMN IF NOT EXISTS trades TEXT[] NOT NULL DEFAULT '{}';

ALTER TABLE vendors DROP CONSTRAINT IF EXISTS vendors_trades_check;
ALTER TABLE vendors ADD CONSTRAINT vendors_trades_check
    CHECK (array_position(trades, NULL) IS NULL AND array_position(trades, '') IS NULL);

CREATE INDEX IF NOT EXISTS idx_vendors_trades ON vendors USING GIN (trades);

-- ---------------------------------------------------------------------------
-- 2. trades from specialty (only vendors with no trades yet)
-- ---------------------------------------------------------------------------
UPDATE vendors v
   SET trades = ARRAY[m.trade]
  FROM (VALUES
        ('hvac', 'hvac'), ('heating', 'hvac'), ('heating & cooling', 'hvac'), ('heating and cooling', 'hvac'),
        ('plumbing', 'plumber'), ('plumber', 'plumber'),
        ('electrical', 'electrician'), ('electrician', 'electrician'), ('electric', 'electrician'),
        ('landscaping', 'landscaper'), ('landscaper', 'landscaper'), ('lawn care', 'landscaper'),
        ('roofing', 'roofer'), ('roofer', 'roofer'),
        ('pest control', 'pest_control'), ('exterminator', 'pest_control'),
        ('cleaning', 'cleaner'), ('cleaner', 'cleaner'), ('house cleaning', 'cleaner'),
        ('handyman', 'handyman'),
        ('pool', 'pool'), ('pool service', 'pool'),
        ('mechanic', 'mechanic'), ('auto repair', 'mechanic'),
        ('vet', 'vet'), ('veterinarian', 'vet'), ('veterinary', 'vet'), ('animal hospital', 'vet'),
        ('pediatrician', 'pediatrician'), ('pediatrics', 'pediatrician'),
        ('doctor', 'doctor'), ('physician', 'doctor'),
        ('dentist', 'dentist'), ('dental', 'dentist')
       ) AS m(specialty, trade)
 WHERE v.trades = '{}'
   AND lower(btrim(v.specialty)) = m.specialty;

-- ---------------------------------------------------------------------------
-- 3. pets.vet_vendor_id
-- ---------------------------------------------------------------------------
ALTER TABLE vendors DROP CONSTRAINT IF EXISTS vendors_id_household_id_key;
ALTER TABLE vendors ADD CONSTRAINT vendors_id_household_id_key UNIQUE (id, household_id);

ALTER TABLE pets ADD COLUMN IF NOT EXISTS vet_vendor_id UUID;

ALTER TABLE pets DROP CONSTRAINT IF EXISTS pets_vet_vendor_fkey;
ALTER TABLE pets ADD CONSTRAINT pets_vet_vendor_fkey
    FOREIGN KEY (vet_vendor_id, household_id) REFERENCES vendors (id, household_id)
    ON DELETE SET NULL (vet_vendor_id);

CREATE INDEX IF NOT EXISTS idx_pets_vet_vendor_id ON pets (vet_vendor_id) WHERE vet_vendor_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 4. vet_name / vet_phone -> a 'vet' vendor (idempotent; 040 re-runs it)
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
        RAISE NOTICE '043: linked % pet(s) to a vet vendor (% new vendor(s))', n_linked, n_new;
    END IF;
END
$$;
-- end 043

COMMIT;
