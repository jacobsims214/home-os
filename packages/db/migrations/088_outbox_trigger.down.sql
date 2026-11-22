-- 088_outbox_trigger.down.sql (#5513)
--
-- Reverses 088. Drops the two triggers and the one function it created, so
-- the schema is exactly as 084 left it. The hand-written INSERTs 088 removed
-- from apps/ are NOT restored: a down migration rolls back schema, and
-- putting the handler-code side-effect back would reintroduce the bug 088
-- exists to fix. Rolling 088 back therefore means bills and assets enqueue
-- nothing -- honest for a database-only rollback, and no worse than the gap
-- 088 closed.
--
-- Idempotent: each DROP uses IF EXISTS, so running it twice is a no-op
-- (packages/db/verify's walk runs the newest downs first, and a hand re-run
-- must not fail).

DROP TRIGGER IF EXISTS bills_outbox ON bills;
DROP TRIGGER IF EXISTS assets_outbox ON assets;
DROP FUNCTION IF EXISTS log_outbox_event();

-- Rows 088 enqueued are left in place: outbox_events is an append-only log,
-- and deleting history is a worse outcome than holding a few rows no trigger
-- would have written. Truncate it explicitly if a clean queue is wanted.
