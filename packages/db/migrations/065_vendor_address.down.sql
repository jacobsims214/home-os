-- 065_vendor_address.down.sql (#4766)
--
-- Moves vendors.address back into notes as the first line
-- "Address: <addr>" (what the MCP contact tools before #4766 read), then
-- drops the column and the two vendor_id indexes. Whitespace runs in the
-- address (an API client may have sent a newline) become one space, so the
-- address stays on the first line. Safe to run twice.

BEGIN;

DROP INDEX IF EXISTS idx_maintenance_tasks_vendor_id;
DROP INDEX IF EXISTS idx_bills_vendor_id;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = current_schema() AND table_name = 'vendors' AND column_name = 'address') THEN
        UPDATE vendors
           SET notes = 'Address: ' || regexp_replace(btrim(address), '\s+', ' ', 'g')
                       || COALESCE(E'\n' || notes, '')
         WHERE NULLIF(btrim(address), '') IS NOT NULL;
        ALTER TABLE vendors DROP COLUMN address;
    END IF;
END $$;

COMMIT;
