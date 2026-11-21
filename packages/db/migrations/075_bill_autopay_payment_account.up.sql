-- 075_bill_autopay_payment_account.up.sql (#5497)
--
-- A bill could not say three things a household needs to know:
--
--   1. Does it pay itself?      bills.is_autopay (020) held the answer but was
--                               NULLABLE, so a row could say nothing at all.
--                               This migration makes "no answer" impossible:
--                               NULLs are backfilled to false and the column
--                               becomes NOT NULL DEFAULT false.
--   2. From which account?      payment_accounts (new) + bills.autopay_account_id.
--   3. Who is being paid?       bills.vendor_id already is the payee when the
--                               payee is a known vendor (058, same-family FK;
--                               MCP calls the parameter contact_id).
--                               bills.payee_name covers the payee that is not
--                               in vendors yet -- a one-off medical bill.
--
-- WHY DEFAULT FALSE IS THE FAIL-SAFE CHOICE. A bill inserted without an explicit
-- autopay value is a bill we know nothing about. Assuming it pays itself turns
-- a forgotten bill into silence: no reminder, no attention flag, and the money
-- leaves the account only if someone happened to set it up. Assuming it needs a
-- human turns the same row into a reminder that costs one tap to dismiss. Wrong
-- in the cheap direction, never the expensive one. The NOT NULL is the other
-- half of that: DEFAULT only fills a column the INSERT omits, and an explicit
-- NULL used to defeat it.
--
-- WHY THE COLUMN IS NOT RENAMED TO autopay_enabled. #5498 exposes
-- `autopay_enabled` in the API response; that is the name the UI and the agent
-- read. The physical column stays `is_autopay` because it is already read by
-- billdue, billcal, the API repo, the MCP bill tools, the worker's bill
-- mirror and the UI model -- a rename is a ten-file refactor across five apps
-- with no change in behaviour, and the point of this ticket is that there is
-- ONE answer to "does it pay itself", not that the answer is spelled one way in
-- the database and another in JSON. No second autopay column is added: two
-- columns for one fact is precisely how they drift apart.
--
-- SECRETS ARE LINKED, NEVER COPIED. payment_accounts holds last-4 and a name;
-- the bank login lives in `secrets` (026), which the server stores as an
-- AES-256-GCM blob it cannot read. The link is payment_accounts.secret_id and
-- vendors.portal_secret_id -- a UUID and a foreign key, nothing else. See the
-- block above each column for why writing one never needs the plaintext.
--
-- Runs in one transaction so a failure leaves the database at 074.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Nowhere left to hide: an unanswered autopay is not an option
-- ---------------------------------------------------------------------------

-- A row may be NULL today (020 declared the column nullable, and an INSERT that
-- names it explicitly writes NULL). Those rows say nothing, which is the state
-- this migration removes. NULL meant "unknown" and unknown meant silence, so it
-- becomes false -- the value that asks a person to look.
UPDATE bills SET is_autopay = false WHERE is_autopay IS NULL;

ALTER TABLE bills ALTER COLUMN is_autopay SET DEFAULT false;
ALTER TABLE bills ALTER COLUMN is_autopay SET NOT NULL;

COMMENT ON COLUMN bills.is_autopay IS
    'Does this bill pay itself? NOT NULL DEFAULT false: false is "a person must pay this", so an unset value produces a reminder rather than silence. Exposed to the API and the agent as autopay_enabled. #5497';

-- ---------------------------------------------------------------------------
-- 2. payment_accounts -- where the money comes from
-- ---------------------------------------------------------------------------
-- Household-scoped like every other record. LAST 4 ONLY: there is no column for
-- a full account number and no column for a routing number, and last_four is
-- CHECKed to exactly four digits so a longer string cannot be smuggled in
-- through it. A full number is not a fact this table is allowed to hold; the
-- banking credential belongs in `secrets`, linked, not copied.

CREATE TYPE payment_account_type AS ENUM ('checking', 'savings', 'credit_card', 'other');

CREATE TABLE payment_accounts (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id  UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,

    -- What the family calls it: "Joint checking" . last_four renders the rest.
    name          VARCHAR(255) NOT NULL,
    institution   VARCHAR(255),
    account_type  payment_account_type NOT NULL DEFAULT 'checking',
    last_four     CHAR(4),
    notes         TEXT,

    -- The bank login for this account. A link: the ciphertext stays in
    -- `secrets`, and only a client holding the household key can decrypt it.
    -- ON DELETE SET NULL so deleting the secret leaves "no login stored"
    -- rather than a pointer to a row that is gone. Composite with
    -- household_id (section 4) so an account cannot be linked to another
    -- family's secret.
    secret_id     UUID,

    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT payment_accounts_last_four_check
        CHECK (last_four IS NULL OR last_four ~ '^[0-9]{4}$'),
    CONSTRAINT payment_accounts_name_check
        CHECK (btrim(name) <> ''),

    -- The composite FK in section 5 targets this key; section 4's FK targets
    -- secrets (id, household_id).
    CONSTRAINT payment_accounts_id_household_key UNIQUE (id, household_id)
);

COMMENT ON TABLE payment_accounts IS
    'An account a household pays bills from. Holds a display name, an institution and the LAST FOUR DIGITS only -- never a full account or routing number. The banking login is secrets(ciphertext), linked by secret_id. #5497';

COMMENT ON COLUMN payment_accounts.last_four IS
    'Last four digits of the account number, for telling two accounts at the same bank apart. A full account number must never be stored here: the CHECK keeps this column to exactly four digits. #5497';

COMMENT ON COLUMN payment_accounts.secret_id IS
    'The bank login for this account, as a row in `secrets`. METADATA ONLY: the ciphertext and the household key never leave `secrets`, and the server can answer "a login is stored" without decrypting anything. ON DELETE SET NULL. #5497';

CREATE INDEX idx_payment_accounts_household ON payment_accounts (household_id);
CREATE INDEX idx_payment_accounts_secret ON payment_accounts (secret_id)
    WHERE secret_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 3. secrets gets the key the composite FKs need
-- ---------------------------------------------------------------------------
-- secrets has never had (id, household_id) as a key, because nothing referenced
-- it by household. Now something does: a link to a secret must be a link to a
-- secret OF THIS FAMILY, otherwise family A's bill could name family B's secret
-- and the read path would have to remember to check. The key makes the database
-- check. Its index also serves the FK, so no second index is created (same
-- shape as 072 for files).

ALTER TABLE secrets
    ADD CONSTRAINT secrets_id_household_id_key UNIQUE (id, household_id);

-- ---------------------------------------------------------------------------
-- 4. The account's bank login is a secret OF THIS FAMILY
-- ---------------------------------------------------------------------------
-- Declared here rather than beside the column because it needs the key above.
-- Without it, payment_accounts.secret_id would happily name another family's
-- secret: the read path would have to remember to check, and any path that
-- forgot would answer "a login is stored" for a secret the caller can never
-- open. With it, the database refuses the pair, and ON DELETE SET NULL can
-- only ever clear a link that was valid.

ALTER TABLE payment_accounts
    ADD CONSTRAINT payment_accounts_secret_fkey
        FOREIGN KEY (secret_id, household_id)
        REFERENCES secrets (id, household_id)
        ON DELETE SET NULL (secret_id);

-- ---------------------------------------------------------------------------
-- 5. bills: which account pays it, and who is being paid
-- ---------------------------------------------------------------------------

-- The account the bill is paid from when it pays itself. Composite FK: same
-- family, ON DELETE SET NULL on this column only (PG15+ column-list form, as
-- 043 and 058 do), so deleting the account does not delete the bill.
--
-- DELIBERATELY NOT the same thing as "the account this bill is normally paid
-- from". It is the AUTOPAY account, which is why the CHECK below holds it to
-- autopay bills. A manual bill has no autopay account by definition.
ALTER TABLE bills ADD COLUMN autopay_account_id UUID;

ALTER TABLE bills
    ADD CONSTRAINT bills_autopay_account_fkey
        FOREIGN KEY (autopay_account_id, household_id)
        REFERENCES payment_accounts (id, household_id)
        ON DELETE SET NULL (autopay_account_id);

-- The contradiction, refused by the database: a bill cannot say "a human pays
-- this" and "this account pays it" at once. Without this, a client that turns
-- autopay off but leaves the account behind produces a row that reads as
-- automatic in one code path and as manual in another -- and the notification
-- policy (#5500) keys on autopay to decide whether to stay quiet. A CHECK
-- rather than a trigger: it holds on every write path (API, MCP, psql, a
-- migration), it reports the offending row, and it costs nothing.
ALTER TABLE bills
    ADD CONSTRAINT bills_autopay_account_check
        CHECK (autopay_account_id IS NULL OR is_autopay);

COMMENT ON COLUMN bills.autopay_account_id IS
    'The payment_accounts row that pays this bill when it pays itself. Only set on an autopay bill (bills_autopay_account_check), same family as the bill, ON DELETE SET NULL. #5497';

-- Who is being paid, when they are not in vendors: a one-off medical bill, a
-- school trip, a plumber paid once. When the payee IS a known vendor,
-- bills.vendor_id is the payee and this stays NULL -- one fact, one place.
-- vendor_id keeps the meaning it already had (058: same-family FK, ON DELETE
-- SET NULL), so nothing that reads it has to change.
ALTER TABLE bills ADD COLUMN payee_name TEXT;

ALTER TABLE bills
    ADD CONSTRAINT bills_payee_name_check
        CHECK (payee_name IS NULL OR btrim(payee_name) <> '');

COMMENT ON COLUMN bills.payee_name IS
    'Free-text payee for a bill whose payee is not a vendors row (a one-off medical bill). NULL when bills.vendor_id names the payee. Never a duplicate of the vendor name. #5497';

CREATE INDEX idx_bills_autopay_account ON bills (autopay_account_id)
    WHERE autopay_account_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 6. vendors: the portal login for a payee
-- ---------------------------------------------------------------------------

-- The utility or portal login for this vendor, as a row in `secrets`. Linked,
-- not copied: the username, password and 2FA seed are inside the encrypted
-- blob, and this column is the only thing the server ever handles. Writing it
-- takes a UUID, so storing the link never requires the plaintext -- and reading
-- it back says "a portal login is stored" without decrypting. Same-family
-- composite FK as payment_accounts.secret_id, so both links obey one rule.
ALTER TABLE vendors ADD COLUMN portal_secret_id UUID;

ALTER TABLE vendors
    ADD CONSTRAINT vendors_portal_secret_fkey
        FOREIGN KEY (portal_secret_id, household_id)
        REFERENCES secrets (id, household_id)
        ON DELETE SET NULL (portal_secret_id);

COMMENT ON COLUMN vendors.portal_secret_id IS
    'The payee portal login for this vendor, as a row in `secrets`. METADATA ONLY: the server stores a UUID and can answer "a login is stored" without decrypting anything. Same family as the vendor, ON DELETE SET NULL. #5497';

CREATE INDEX idx_vendors_portal_secret ON vendors (portal_secret_id)
    WHERE portal_secret_id IS NOT NULL;

COMMIT;
