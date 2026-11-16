-- 062_activity_log_xact.up.sql (#4741)
--
-- Make "poll for new activity" safe against transactions that commit out of
-- id order.
--
-- activity_log.id is taken at INSERT, but transactions commit in any order.
-- A poller that read id 101 and then asks for "id > 101" never sees id 100
-- if 100's transaction committed later. Nor does holding back rows whose
-- transaction is still running fix it on its own: a transaction can take its
-- xid before another one but log after it, so xid order and id order differ.
--
-- xact_id records the writing (top-level) transaction's id. The api polls in
-- (xact_id, id) order and only hands out rows whose xact_id is below the
-- reading snapshot's xmin. Every transaction below xmin has finished, and
-- every row that is not yet visible belongs to a transaction at or above
-- xmin, so the rows handed out are a prefix that never grows behind the
-- poller. See KB apis/activity-feed.md.
--
-- Existing rows are all committed: they get xact_id 0, which sorts first.
-- ADD COLUMN with a constant default does not rewrite the table; the
-- volatile default for new rows is set afterwards.

BEGIN;

ALTER TABLE activity_log ADD COLUMN xact_id xid8 NOT NULL DEFAULT '0'::xid8;
ALTER TABLE activity_log ALTER COLUMN xact_id SET DEFAULT pg_current_xact_id();

COMMENT ON COLUMN activity_log.xact_id IS
    'Top-level transaction that wrote the row (0 for rows logged before 062). The change feed polls in (xact_id, id) order below the snapshot xmin, so rows that commit out of id order are never skipped.';

-- The poll scan and its anchor: one household, (xact_id, id) order.
CREATE INDEX idx_activity_log_household_xact ON activity_log (household_id, xact_id, id);

COMMIT;
