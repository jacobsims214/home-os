-- 076_search_index_backfill.down.sql (#5314)
--
-- The backfill only adds queue rows; there is nothing to undo that 075's
-- down migration does not already drop. Deliberately empty so a down to 074
-- runs 076 down, then 075 down.

SELECT 1;
