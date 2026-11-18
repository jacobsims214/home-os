-- 066_list_item_checked_by.down.sql (#4800)
--
-- Drops list_items.checked_by with its trigger, function, index and FK.
-- Lost on down: who checked each item off. Safe to run twice.

BEGIN;

-- CASCADE drops the trigger with the function.
DROP FUNCTION IF EXISTS list_items_checked_by() CASCADE;
DROP INDEX IF EXISTS idx_list_items_checked_by;
ALTER TABLE IF EXISTS list_items DROP CONSTRAINT IF EXISTS list_items_checked_by_fkey;
ALTER TABLE IF EXISTS list_items DROP COLUMN IF EXISTS checked_by;

COMMIT;
