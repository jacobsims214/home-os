-- 065_vendor_address.up.sql (#4766)
--
-- vendors.address: a real column for a contact's street address. Before
-- this, the MCP contact tools (#4466) kept the address as the first notes
-- line "Address: <addr>" because 004 had no column. That line moves into
-- the column here and is removed from notes; the rest of the notes stays.
-- The move mirrors the old splitAddress: only the FIRST line counts, a
-- blank address after the prefix gives NULL, and the remaining notes are
-- trimmed (NULL when nothing is left). It is idempotent: a row that already
-- has an address, or whose notes no longer start with "Address: ", is left
-- alone, so re-running the migration changes nothing.
--
-- Indexes on bills(vendor_id) and maintenance_tasks(vendor_id): MCP
-- contact_find counts a contact's bills and open upkeep tasks per row, and
-- deleting a vendor runs the ON DELETE SET NULL FKs (058) on both tables.
-- Without these both were seq scans. Partial (vendor_id IS NOT NULL) like
-- 043's idx_pets_vet_vendor_id; "vendor_id = $x" implies NOT NULL, so the
-- planner uses them. Plain CREATE INDEX: the tables are family-sized.
--
-- Runs in one transaction.

BEGIN;

ALTER TABLE vendors ADD COLUMN IF NOT EXISTS address TEXT;

UPDATE vendors
   SET address = NULLIF(btrim(substr(split_part(notes, E'\n', 1), 10), E' \t\r\n'), ''),
       notes   = CASE WHEN strpos(notes, E'\n') = 0 THEN NULL
                      ELSE NULLIF(btrim(substr(notes, strpos(notes, E'\n') + 1), E' \t\r\n'), '')
                 END,
       updated_at = NOW()
 WHERE address IS NULL
   AND left(notes, 9) = 'Address: ';

COMMENT ON COLUMN vendors.address IS
    'Street address, one line. Before 065 the MCP contact tools kept it as the first notes line "Address: ..." (#4766).';

CREATE INDEX IF NOT EXISTS idx_bills_vendor_id
    ON bills (vendor_id) WHERE vendor_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_maintenance_tasks_vendor_id
    ON maintenance_tasks (vendor_id) WHERE vendor_id IS NOT NULL;

COMMIT;
