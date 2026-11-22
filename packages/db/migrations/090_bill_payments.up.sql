-- 090_bill_payments.up.sql (#5514, schema review F6)
--
-- THE DEFECT. A bill's whole payment history is two nullable dates:
-- bills.paid_date (020) and bills.paid_for_due_date (052). Every payment but
-- the most recent is destroyed by the next one. There is no amount-paid column
-- at all — bills.amount is the amount OWED — so these questions cannot be
-- asked, and each is a question this product exists to answer:
--
--   "Did we pay the October mortgage, and when?"
--   "How much did we pay the dentist last year?"
--   "Show me the payment that cleared at $341 when the bill says $314."
--
-- 052's own header is the evidence that two inferred dates are not enough: a
-- payment more than 7 days late was read as paying NEXT month, so the overdue
-- bill vanished and next month's reminder was never sent. It patched that
-- symptom with a SECOND inferred date. The next bug of that family wants a
-- third column. A payment is an EVENT; this migration stores events.
--
-- WHAT THIS MIGRATION DOES
--   1. Creates bill_payments: one row per payment, with the amount, the date
--      it was paid, and the due cycle it settles.
--   2. Backfills one row per bill that has a paid_date, flagged
--      is_reconstructed = true (section 3).
--   3. Adds the two indexes the query shapes need.
--
-- WHAT IT DELIBERATELY DOES NOT DO
--   * It does NOT switch any reader over to the ledger. Every existing read
--     keeps using bills.paid_date / bills.paid_for_due_date exactly as it does
--     today: billdue.coveredDueDate, BillDueFor, AttentionFor, bill_find, the
--     API's bill responses, the agenda, the worker's reminders and the
--     notification policy. Making the ledger authoritative is a follow-up once
--     it is populated and proven, and it is a change to that ONE rule in
--     packages/billdue — not to its callers.
--   * It does NOT drop or change bills.paid_date / paid_for_due_date. They
--     stay as the derived cache, which is what makes this additive and
--     reversible: down drops the ledger and every reader is exactly where it
--     was.
--   * It does NOT add a trigger keeping that cache in step with the ledger.
--     A trigger now would be a SECOND writer of the cache running beside the
--     MCP bill_pay path, and until something reads the ledger the two can only
--     disagree silently. The cache writers stay what they are: bill_pay (which
--     also writes the ledger row, #5514) and the API's bill update (which only
--     resets paid_for_due_date when the schedule changes).
--
-- NAMING: paid_for_due_date, not covers_due_date. The ticket's original draft
-- called the column covers_due_date; bills already calls this same fact
-- paid_for_due_date (052) and one fact gets one name. The ledger column keeps
-- that meaning — the due cycle the payment settles — which is what makes 052's
-- coverage question answerable from the ledger rather than inferred from one
-- date.
--
-- Runs in one transaction.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. The ledger
-- ---------------------------------------------------------------------------

CREATE TABLE bill_payments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id    UUID NOT NULL REFERENCES households(id) ON DELETE CASCADE,

    -- The bill this payment settles. NOT a plain REFERENCES bills(id): the
    -- composite FK below is what makes "same family" checkable in the database
    -- instead of in every call site (058's pattern, and the reason 084 had to
    -- add bills_id_household_id_key before this ticket could start).
    bill_id         UUID NOT NULL,

    -- What was actually paid, when it was paid. NOT bills.amount: that is what
    -- is owed, and the two differ exactly when it matters (a partial payment, a
    -- late fee, a corrected statement). NUMERIC(12,2) matches bills.amount so a
    -- payment reconciles against a statement without rounding.
    --
    -- NULLABLE, deliberately, and it is the honest option rather than a
    -- convenience. bills.amount has been nullable since 007, so a bill can
    -- exist with no amount at all (a legacy row, one entered before the family
    -- knew the figure). Requiring an amount here would either refuse to record
    -- that the payment happened, or force an invented 0.00 that reads as "paid,
    -- and it cost nothing" — and a 0.00 is wrong for every spend total in the
    -- quiet direction. NULL means "a payment happened; nobody recorded how
    -- much", which is exactly the fact. SUM() skips it, which is the correct
    -- arithmetic: unknown is not zero. The CHECK still refuses a non-payment.
    amount          NUMERIC(12,2),

    -- When the money moved (the event's date — what "when?" asks), and which
    -- due cycle it settles (what 052 asks). Two columns on purpose: a late
    -- payment is one event covering an earlier cycle, and collapsing the two is
    -- exactly the bug 052 fixed.
    --
    -- paid_for_due_date is nullable because a bill with no usable schedule has
    -- no cycle to name, and the write path only infers coverage when the bill
    -- has one. NULL means "this bill has no due cycle", not "unknown".
    paid_on         DATE NOT NULL,
    paid_for_due_date DATE,

    -- Where the money came from. Nullable: a cash payment, or a reconstruction
    -- of a payment nobody recorded, has no account. It lives here rather than
    -- only on bills because a PAYMENT comes out of an account; 075's
    -- bills.autopay_account_id is the standing configuration, this is the
    -- transaction (#5497).
    payment_account_id UUID,

    -- How it was paid ("card", "ach", "check", "cash"). Free text rather than
    -- an enum: the vocabulary is not settled, and an enum on a table that must
    -- also hold reconstructions would freeze it too early.
    method          VARCHAR(50),
    note            TEXT,

    -- The receipt this payment was reconciled against, as bills.source_document_id
    -- is the bill's (072): same-family, ON DELETE SET NULL on this column only,
    -- so deleting the document keeps the payment and forgets its evidence.
    source_document_id UUID,

    -- TRUE for a row this migration invented from a single paid_date, FALSE for
    -- a payment somebody actually recorded. This is the column that stops an
    -- inferred history reading as a record: after 090 a family can ask "what do
    -- we actually know", and for every pre-090 bill the honest answer is "the
    -- last payment, reconstructed".
    is_reconstructed BOOLEAN NOT NULL DEFAULT false,

    -- created_at only, no updated_at: a payment is an event, and events are not
    -- edited. A correction is a new row with its own meaning, so a ledger row
    -- that silently changes is a bug this column deliberately cannot express.
    -- (It also keeps the table out of 077's updated_at contract, where a new
    -- table would need its own set_updated_at trigger to be consistent.)
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Who recorded it, from app.user_id (050's actor), so no write path has to
    -- pass it and none can forget. current_actor_user_id() never raises and
    -- returns NULL when there is no actor or the value is not a user, which is
    -- what keeps a migration, a psql session and a dev token from failing the
    -- FK. Left NULL by the backfill below, which is correct: nobody recorded
    -- those rows.
    created_by      UUID DEFAULT current_actor_user_id() REFERENCES users(id) ON DELETE SET NULL,

    -- The composite same-family key. A payment cannot point at another
    -- family's bill, and it dies with its bill (a payment of a bill that no
    -- longer exists is an orphan, not history) — both facts from one
    -- constraint, so no write path can get them wrong.
    -- bills_id_household_id_key (084) is the target, and its own
    -- (id, household_id) btree serves this FK, so bills needs no extra index.
    CONSTRAINT bill_payments_bill_fkey
        FOREIGN KEY (bill_id, household_id)
        REFERENCES bills (id, household_id)
        ON DELETE CASCADE,

    -- Same-family account, ON DELETE SET NULL on this column only (the PG15+
    -- column-list form 043/058/075 use), so deleting an account keeps the
    -- payment and forgets where it came from rather than destroying history.
    CONSTRAINT bill_payments_account_fkey
        FOREIGN KEY (payment_account_id, household_id)
        REFERENCES payment_accounts (id, household_id)
        ON DELETE SET NULL (payment_account_id),

    -- Same-family receipt, same terms (072's files_id_household_id_key).
    CONSTRAINT bill_payments_source_document_fkey
        FOREIGN KEY (source_document_id, household_id)
        REFERENCES files (id, household_id)
        ON DELETE SET NULL (source_document_id),

    -- So a later table can point at one payment with the same composite-FK
    -- pattern (a reconciliation, an allocation across two cycles, a refund).
    -- Same shape as bills_id_household_id_key (084) and for the same reason:
    -- id alone is already unique, so this cannot reject a row that was
    -- previously valid; it exists so (id, household_id) is a referenceable
    -- target.
    CONSTRAINT bill_payments_id_household_id_key UNIQUE (id, household_id),

    -- A payment of zero or less is not a payment (an unknown amount is another
    -- matter, see the column). A negative amount would read as a refund, which
    -- this ledger does not model — a refund is its own event with its own
    -- meaning, not a negative payment, and a negative row would make "sum of
    -- payments this year" wrong for every reader that filters amount > 0 and
    -- right for nobody. Cheap to refuse while the table is empty; expensive
    -- once a report depends on the sign.
    CONSTRAINT bill_payments_amount_check CHECK (amount IS NULL OR amount > 0),
    CONSTRAINT bill_payments_method_check
        CHECK (method IS NULL OR btrim(method) <> ''),
    CONSTRAINT bill_payments_note_check
        CHECK (note IS NULL OR btrim(note) <> '')
);

COMMENT ON TABLE bill_payments IS
    'One row per payment of a bill: what was paid, when, and which due cycle it settles. bills.paid_date/paid_for_due_date remain the derived cache of the LATEST payment until a follow-up points packages/billdue at this table. #5514';

COMMENT ON COLUMN bill_payments.household_id IS
    'The family. NOT NULL from day one (schema review F20''s lesson), and half of every composite FK here, so a row cannot join a bill, an account and a document from three different families. #5514';

COMMENT ON COLUMN bill_payments.amount IS
    'What was actually paid, not what was owed (bills.amount). NUMERIC(12,2) so it reconciles against a statement without rounding. NULL: the payment happened and nobody recorded how much (bills.amount has been nullable since 007); SUM() skips it, because unknown is not zero. #5514';

COMMENT ON COLUMN bill_payments.paid_on IS
    'The date the money moved. The event''s date; paid_for_due_date is the cycle it settles. #5514';

COMMENT ON COLUMN bill_payments.paid_for_due_date IS
    'The due cycle this payment settles (052''s fact, and its name). NULL when the bill has no usable schedule. Stored as recorded — re-inferring it from paid_on is the bug 052 fixed. #5514';

COMMENT ON COLUMN bill_payments.is_reconstructed IS
    'TRUE: rebuilt from bills.paid_date by migration 090, not observed. FALSE: a payment somebody recorded. Keeps inferred history from reading as a record. #5514';

-- ---------------------------------------------------------------------------
-- 2. Indexes: the query shapes
-- ---------------------------------------------------------------------------

-- "This family's payments, newest first" and "sum of payments this household
-- this year": filter household_id, order or range on paid_on, read backwards
-- from today. household_id leads, which is also the index F20's lesson asks
-- for. A date range needs no ORDER BY to use it.
CREATE INDEX idx_bill_payments_household_paid
    ON bill_payments (household_id, paid_on DESC);

-- "This bill's payment history, newest first" — the question that could not be
-- asked at all before this table, and the one the two-payments-per-bill test
-- reads. No household_id prefix: bill_id is bills.id, the primary key, so a
-- bill-scoped scan is already family-scoped and a third leading column would
-- only widen every entry in this index. (The ticket's brief phrased this as
-- (household_id, bill_id, paid_on DESC); the same query is answered here with
-- household_id as a filter on a handful of rows per bill.)
CREATE INDEX idx_bill_payments_bill_paid
    ON bill_payments (bill_id, paid_on DESC);

-- The two nullable FKs need no index: idx_bill_payments_household_paid covers
-- household_id, and neither "payments from this account" nor "payments from
-- this document" is a query anyone makes yet. An index per nullable column is
-- a cost on every write for a question nothing asks; add each with the reader
-- that asks it.

-- ---------------------------------------------------------------------------
-- 3. Backfill: one reconstructed row per paid bill
-- ---------------------------------------------------------------------------

-- Every bill with a paid_date has exactly ONE payment on record, because the
-- schema could only ever hold the latest one. The row says that and no more:
--
--   amount             = bills.amount             what was OWED. Nothing
--                                                   recorded what was paid, so
--                                                   this is not an observation
--                                                   and is_reconstructed says so.
--                                                   NULL for a bill with no
--                                                   amount, which stays NULL
--   paid_on            = bills.paid_date
--   paid_for_due_date  = bills.paid_for_due_date  NULL stays NULL: 052's NULL
--                                                   means "infer it", and
--                                                   inventing a coverage here
--                                                   would freeze an inference
--                                                   billdue still makes from the
--                                                   bill's CURRENT schedule
--   payment_account_id, method, note, source_document_id = NULL (unrecorded)
--   is_reconstructed   = TRUE
--
-- EVERY bill with a paid_date gets exactly one row — the criterion is "one row
-- each", and a bill whose amount nobody recorded still had a payment. Its
-- amount is NULL, not 0.00: an invented zero reads as "paid, and it cost
-- nothing" and would make every spend total wrong in the quiet direction.
-- A bill whose amount is 0 or negative cannot satisfy the CHECK, and such a
-- row is not a payment anyone made; there are none in the tree today (007 made
-- the column nullable, and no path writes <= 0), and rather than invent a
-- number for one the INSERT reports the conflict loudly. The verification
-- script counts both cases explicitly.
--
-- created_by is written NULL explicitly, and that is the point: the column has
-- a DEFAULT current_actor_user_id(), so a migration run by an authenticated
-- session would otherwise stamp ITS user on rows nobody observed. A
-- reconstruction is not authored. (There is no authorship trigger on this
-- table: a payment is an event, and 050's set_authorship has nothing to
-- maintain on an append-only row.)
INSERT INTO bill_payments
    (household_id, bill_id, amount, paid_on, paid_for_due_date, is_reconstructed, created_by)
SELECT b.household_id,
       b.id,
       b.amount,
       b.paid_date,
       b.paid_for_due_date,
       TRUE,
       NULL::uuid   -- explicit: nobody recorded these, whatever session runs the migration
  FROM bills b
 WHERE b.paid_date IS NOT NULL
   AND (b.amount IS NULL OR b.amount > 0);

COMMIT;
