-- 052_bills_paid_for_due_date.up.sql (#4655)
--
-- bills.paid_for_due_date: the due date the last payment (bills.paid_date)
-- covers. bill_pay stores it, from the caller's for_due_date or, when that is
-- left out, the date inferred from paid_date (paidForDueDate: up to 7 days
-- late pays the past due date, otherwise the next one). Without it a payment
-- more than 7 days late was read as paying NEXT month, so the overdue bill
-- vanished and next month's reminder was never sent.
--
-- NULL means "not stored": the bill tools and the worker then infer coverage
-- from paid_date as before, so rows written before 052 behave exactly as
-- they did. bill_update (and the API's bill update) reset it to NULL when
-- due_day changes, so coverage is recomputed for the new day.
--
-- Runs in one transaction.

BEGIN;

ALTER TABLE bills ADD COLUMN IF NOT EXISTS paid_for_due_date DATE;

COMMENT ON COLUMN bills.paid_for_due_date IS
    'Due date the payment on paid_date covers (set by bill_pay). NULL: infer it from paid_date.';

COMMIT;
