-- 090_bill_payments down (#5514).
--
-- Drops the ledger. Nothing else changes: bills.paid_date and
-- bills.paid_for_due_date were never touched by 090 (they stay the derived
-- cache of the latest payment, and every reader still reads them), so after
-- this the schema and every read path are exactly as they were before 090.
-- That is what "additive and reversible" has to mean here — a down that had to
-- rebuild payment state from the ledger would be a migration nobody could run.
--
-- The backfill goes with the table. A reconstruction that outlived the table it
-- lived in would have nowhere to be, and bills.paid_date still holds the same
-- fact for every reconstructed row.
--
-- No FK elsewhere points at bill_payments (the ticket that adds one adds its own
-- column and constraint), so DROP TABLE needs no CASCADE, and a CASCADE here
-- would be a promise that some other object is allowed to vanish silently.
--
-- Runs in one transaction.

BEGIN;

DROP TABLE IF EXISTS bill_payments;

COMMIT;
