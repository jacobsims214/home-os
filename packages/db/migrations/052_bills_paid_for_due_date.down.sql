-- 052_bills_paid_for_due_date down (#4655).
--
-- Drops bills.paid_for_due_date. Pre-052 code infers coverage from paid_date
-- alone, so a payment recorded with an explicit for_due_date more than 7 days
-- after that due date is read as paying the next one again (the bug 052
-- fixes). paid_date itself is untouched.
--
-- Runs in one transaction.

BEGIN;

ALTER TABLE bills DROP COLUMN IF EXISTS paid_for_due_date;

COMMIT;
