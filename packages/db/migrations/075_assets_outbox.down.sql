-- 075_assets_outbox down (#5320).
--
-- Drops the trigger, so asset writes stop enqueuing outbox events again. The
-- rows already written are kept (they are a log). Reverting also means
-- apps/api/internal/asset/repo.go must put its own INSERTs back, otherwise
-- REST writes enqueue nothing either; this down migration cannot do that.
--
-- Runs in one transaction.

BEGIN;

DROP TRIGGER IF EXISTS assets_enqueue_outbox ON assets;
DROP FUNCTION IF EXISTS assets_enqueue_outbox();

COMMIT;
