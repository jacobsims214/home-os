-- 078_bill_reminder_dedupe.up.sql (#5500)
--
-- The notification policy keys a bill reminder on (bill, due date, attention
-- state), and stores that key in notifications.dedupe_key (056). 056's
-- idx_notifications_dedupe is UNIQUE (user_id, dedupe_key) WHERE dedupe_key IS
-- NOT NULL, which is exactly right for the calendar alarms it was added for:
-- one row per member per firing.
--
-- It does not cover this job's rows. A bill reminder is FAMILY-WIDE, so its
-- user_id is NULL (the read paths already treat NULL as "every member", see
-- visibleSQL in apps/api/internal/notification and apps/mcp/internal/...:
-- `n.user_id IS NULL OR n.user_id = $2`). NULL never conflicts in a unique
-- index, so the constraint would not stop two replicas, or a restart mid-run,
-- from writing the same reminder twice -- which is the one thing this ticket
-- asks for and cannot get from the existing index.
--
-- So: a second partial unique index on (household_id, dedupe_key) for the
-- family-wide rows. The worker inserts with ON CONFLICT DO NOTHING (no target
-- list: the columns differ per row shape, and DO NOTHING without a target is
-- valid for any conflict), so a re-run is a no-op whichever index refuses it.
--
-- Uniqueness per FAMILY, not per user, is the right grain: two members must not
-- each get their own copy of "Power overdue", or the two of them compare phones
-- and conclude the family is being told twice.
--
-- Runs in one transaction.

BEGIN;

CREATE UNIQUE INDEX idx_notifications_dedupe_family
    ON notifications (household_id, dedupe_key)
    WHERE dedupe_key IS NOT NULL AND user_id IS NULL;

COMMENT ON INDEX idx_notifications_dedupe_family IS
    'One family-wide notification per dedupe_key per household: bill_due keyed on (bill, date, attention) and bill_digest keyed on the set it stands in for. NULL user_id rows only; 056''s idx_notifications_dedupe covers the per-member rows. #5500';

COMMIT;
