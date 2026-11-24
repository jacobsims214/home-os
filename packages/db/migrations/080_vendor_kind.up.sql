-- 080_vendor_kind.up.sql  (#5529, decision #5517 option 0)
--
-- ONE table, two meanings. `vendors` holds both "who does work for us" (the
-- plumber, the gardener, the vet -- a contact with trades, 043) and "who we
-- pay" (the electric utility, the mortgage servicer -- a row that exists to
-- be the payee on a bill, 058/075). Until now nothing in the schema said
-- which, so every consumer of the list had to guess, and the contacts list
-- filled up with utilities while the bill payee picker offered the gardener.
--
-- The decision (#5517, option 0) is a discriminator on the existing table,
-- not a separate payees table and not a second contact block on bills:
-- "who do we pay?" stays one hop from a bill (bills.vendor_id -> vendors),
-- it reuses the four migrations that already extend vendors (043 trades,
-- 064 membership link, 065 address, 058 same-family composite FK), and one
-- row can be both things without being duplicated (Electric Co that also
-- fixes the panel is `both`).
--
-- THE ENUM IS THE CHECK. `vendor_kind` is a native type, so a write with any
-- other value is refused by the database on every path (API, MCP, psql, a
-- migration) -- the same guarantee a TEXT + CHECK would give, enforced by the
-- type instead of a constraint expression, and it cannot be bypassed by a
-- later ALTER that forgets the constraint. Verified in test_080.sh: an INSERT
-- with kind='utility' is refused.
--
-- DEFAULT 'service_provider' IS DELIBERATE. Every path that can create a
-- vendors row today is a contacts path (POST /api/v1/vendors, MCP
-- contact_create, seed data, tests), and a contacts row is someone who does
-- work. A row inserted by a caller that predates this migration therefore
-- stays visible in the list it was created in, which is exactly where it is
-- today. It also fails in the cheap direction: a wrongly-typed
-- service_provider is one UPDATE away from correct and is visible to the
-- person who can fix it, whereas defaulting to 'payee' would silently hide a
-- contact the person just created. Payee-only rows are created by a payee
-- path that sets kind explicitly (the bill payee picker), not by the default.
--
-- Runs in one transaction (golang-migrate does not wrap files).

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. the discriminator
-- ---------------------------------------------------------------------------

CREATE TYPE vendor_kind AS ENUM ('service_provider', 'payee', 'both');

COMMENT ON TYPE vendor_kind IS
    'What a vendors row is for: service_provider does work for the family (plumber, vet -- the contacts list), payee is paid by the family (utility, mortgage servicer -- the bill payee picker), both is both. #5529';

-- Nullable for exactly one statement so every row can be classified by
-- evidence first; section 3 makes it NOT NULL.
ALTER TABLE vendors ADD COLUMN kind vendor_kind;

-- ---------------------------------------------------------------------------
-- 2. backfill BY EVIDENCE, not by blanket default
-- ---------------------------------------------------------------------------
--
-- "Does work" evidence, in the order the schema accumulated it:
--   * trades <> '{}'                043 -- a trade is what a service provider has;
--   * maintenance_tasks.vendor_id   009/058 -- the family assigned them work;
--   * pets.vet_vendor_id            043 -- they are a pet's vet;
--   * memberships.vendor_id         064 -- a vendor login exists for them
--                                     (invite or member edit, #4718/#4799),
--                                     which only happens for a company that
--                                     does work for this family.
--
-- "Is paid" evidence:
--   * bills.vendor_id               058 -- a bill names them as its payee (075
--                                     keeps payee_name for payees not in
--                                     vendors, so a row is only evidence when
--                                     a bill actually links it).
--
-- Anything with both kinds of evidence is 'both'. A row with NEITHER is
-- 'service_provider': it was created from the contacts list (the only
-- creation path before this migration), it has not been used as a payee, and
-- leaving it visible in that list is what the person who created it expects.
-- Classifying it 'payee' would delete it from the only screen that can edit
-- it -- silently, and with no evidence to justify it.
--
-- Not evidence, deliberately: specialty, notes, website, portal_secret_id.
-- "Utility" in a free-text specialty is a guess; 043 already shows how
-- unreliable free text is (it maps a closed list and gives up on the rest).
-- portal_secret_id is a login, not a payment: two migrations set it for
-- service providers too (#5497/#5529), and a login is not a bill.
UPDATE vendors v
   SET kind = CASE
                WHEN work.has_work AND paid.has_bill THEN 'both'::vendor_kind
                WHEN work.has_work                   THEN 'service_provider'::vendor_kind
                WHEN paid.has_bill                   THEN 'payee'::vendor_kind
                ELSE 'service_provider'::vendor_kind
              END
  FROM (SELECT v2.id,
               (v2.trades <> '{}'
                OR EXISTS (SELECT 1 FROM maintenance_tasks m WHERE m.vendor_id = v2.id)
                OR EXISTS (SELECT 1 FROM pets p             WHERE p.vet_vendor_id = v2.id)
                OR EXISTS (SELECT 1 FROM memberships mb     WHERE mb.vendor_id = v2.id)) AS has_work
          FROM vendors v2) work
  JOIN (SELECT v3.id,
               EXISTS (SELECT 1 FROM bills b WHERE b.vendor_id = v3.id) AS has_bill
          FROM vendors v3) paid ON paid.id = work.id
 WHERE v.id = work.id;

-- A row inserted by a concurrent writer between the UPDATE and the NOT NULL
-- below would otherwise abort the whole migration. The default is the same
-- 'service_provider' the CASE above gives an evidence-free row, so this
-- cannot disagree with the backfill.
UPDATE vendors SET kind = 'service_provider' WHERE kind IS NULL;

ALTER TABLE vendors ALTER COLUMN kind SET DEFAULT 'service_provider';
ALTER TABLE vendors ALTER COLUMN kind SET NOT NULL;

COMMENT ON COLUMN vendors.kind IS
    'What this row is for. service_provider: the contacts list (does work). payee: the bill payee picker (is paid). both: both. NOT NULL DEFAULT service_provider -- an evidence-free row came from a contacts path and stays visible there. #5529';

-- ---------------------------------------------------------------------------
-- 2b. evidence added later keeps kind true
-- ---------------------------------------------------------------------------
--
-- The backfill above classifies the rows that exist now. This trigger keeps the
-- answer right when a PAYEE row later gains work: the vet picker tags a vendor
-- with the 'vet' trade (043 section 4, 060), contact_update writes trades, and
-- a payee-only utility that becomes the family's vet must stop being invisible
-- in the contacts list. Promotion is one-way and only on evidence: a row is
-- never demoted to 'payee' by losing its last trade, because a person who
-- cleared a trade is editing a contact, not asking to hide it from the list
-- they are standing in.
--
-- Deliberately NOT covered by a trigger, and stated rather than hidden:
-- evidence that lives in ANOTHER table (bills.vendor_id, maintenance_tasks.
-- vendor_id, pets.vet_vendor_id, memberships.vendor_id). Covering those needs
-- a trigger on each of four tables, and a bill link must NOT promote a row --
-- bills.vendor_id is what makes it a payee in the first place. A row that is
-- linked as a payee and also does work is set to 'both' by the caller that
-- adds the work (API PUT kind, MCP contact_update), which is one write and
-- says what the person meant.
CREATE OR REPLACE FUNCTION vendors_promote_on_trades() RETURNS trigger AS $$
BEGIN
    IF NEW.kind = 'payee' AND NEW.trades <> '{}' THEN
        NEW.kind := 'both';
    END IF;
    RETURN NEW;
END
$$ LANGUAGE plpgsql;

CREATE TRIGGER vendors_promote_on_trades
    BEFORE INSERT OR UPDATE OF kind, trades ON vendors
    FOR EACH ROW EXECUTE FUNCTION vendors_promote_on_trades();

COMMENT ON FUNCTION vendors_promote_on_trades() IS
    'A payee that gains a trade does work too: kind becomes both. One-way -- losing the last trade never demotes. #5529';

-- ---------------------------------------------------------------------------
-- 3. index what the two lists filter on
-- ---------------------------------------------------------------------------
--
-- Both lists are one household's rows narrowed by kind:
--   contacts  WHERE household_id = $1 AND kind <> 'payee'
--   payees    WHERE household_id = $1 AND kind <> 'service_provider'
-- so the index leads with household_id and carries kind, which lets either
-- list be answered from the index (including the ORDER BY created_at path
-- when the planner prefers it, and the kind-only count in the overview).
-- One index, not two: they filter the same two columns in the same order.
CREATE INDEX idx_vendors_household_kind ON vendors (household_id, kind);

-- end 080

COMMIT;
