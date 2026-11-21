-- 075_bill_autopay_payment_account.down.sql (#5497)
--
-- Undoes 075 and nothing else, leaving the schema at 074:
--
--   1. vendors.portal_secret_id (FK, index, column)
--   2. bills.payee_name (CHECK, column)
--   3. bills.autopay_account_id (FK, CHECK, index, column)
--   4. payment_accounts (its secret FK, indexes, table) and its enum
--   5. secrets_id_household_id_key -- 075 added it for the two composite FKs
--      above; with both gone nothing references it
--   6. bills.is_autopay back to 020's declaration: NULLABLE, DEFAULT false.
--      The default stays, because 020 set it and 074 has it.
--
-- LOST ON DOWN, unavoidably, because there is no earlier column to hold them:
--   * every payment_accounts row (names, institutions, last-4, notes)
--   * bills.autopay_account_id, bills.payee_name, vendors.portal_secret_id
--   * the is_autopay values that were NULL before 075 backfilled them to false.
--     Up again cannot tell a deliberate false from a backfilled one, so a row
--     that said nothing before 075 says false after an up/down/up. Nothing
--     reads the difference (every reader already COALESCEd to false), so this
--     is a note, not a hazard.
-- None of this touches `secrets`: 075 only ever added a REFERENCES to it, so
-- the family's ciphertext, keys and salts survive the down untouched. The down
-- does not decrypt anything and cannot -- the server never could.
--
-- Safe to run twice: every drop is IF EXISTS and the is_autopay ALTERs are
-- idempotent.
--
-- Runs in one transaction.

BEGIN;

-- 1. vendors: the payee portal login link.
DROP INDEX IF EXISTS idx_vendors_portal_secret;
ALTER TABLE vendors DROP CONSTRAINT IF EXISTS vendors_portal_secret_fkey;
ALTER TABLE vendors DROP COLUMN IF EXISTS portal_secret_id;

-- 2. bills: the free-text payee.
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_payee_name_check;
ALTER TABLE bills DROP COLUMN IF EXISTS payee_name;

-- 3. bills: the autopay account. The CHECK goes before the column so no window
--    exists in which the column is gone and the constraint still names it.
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_autopay_account_check;
DROP INDEX IF EXISTS idx_bills_autopay_account;
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_autopay_account_fkey;
ALTER TABLE bills DROP COLUMN IF EXISTS autopay_account_id;

-- 4. payment_accounts and its type. The table drop takes its own indexes and
--    constraints with it.
DROP INDEX IF EXISTS idx_payment_accounts_secret;
DROP INDEX IF EXISTS idx_payment_accounts_household;
DROP TABLE IF EXISTS payment_accounts;
DROP TYPE IF EXISTS payment_account_type;

-- 5. The secrets key 075 added for the composite FKs.
ALTER TABLE secrets DROP CONSTRAINT IF EXISTS secrets_id_household_id_key;

-- 6. is_autopay back to 020's nullable declaration. The DEFAULT false stays:
--    020 set it, so removing it would leave the schema somewhere neither 074
--    nor 075 ever was.
ALTER TABLE bills ALTER COLUMN is_autopay DROP NOT NULL;

COMMENT ON COLUMN bills.is_autopay IS
    'Does this bill pay itself? NULL means "not recorded" (pre-075 rows may still say nothing); every reader COALESCEs to false, so an unset value asks for a person rather than staying silent. #5497 removed the NULL possibility going forward, in 075.';

COMMIT;
