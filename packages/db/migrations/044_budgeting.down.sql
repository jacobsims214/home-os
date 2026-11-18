-- 044_budgeting.down.sql
--
-- Removes budget_categories, kind 'budget', bills.frequency/due_date/
-- budget_category_id and loans.due_day/term_end/account_number/
-- budget_category_id (ticket #4453).
--
-- Order matters:
--   1. Loans and bills that 044 up copied from property/vehicle columns and
--      that nobody edited since (updated_at = created_at) are deleted. Their
--      source columns still exist (044 drops nothing), so no data is lost,
--      and pre-044 code does not read an annual bill as a monthly one.
--      Edited copies are kept; a later 044 up sees them as equivalents.
--   2. budget_categories rows are deleted row by row so that
--      rehome_entity_refs('budget') runs: files and secrets about a category
--      move to the family, notes and links about it are deleted. After that
--      no row uses kind 'budget' and the narrower entity_kind check (041's)
--      can be added back.
--   3. entity_household() goes back to its 041 body (keeping 'person').
--
-- Lost on down: frequencies and due dates of bills that are not monthly,
-- loan due days, term ends and account numbers, and budget categories.

BEGIN;

-- 1. Unedited copies (COPY_MARKER, see 044_budgeting.up.sql section 4).
DELETE FROM loans
 WHERE updated_at = created_at
   AND (notes LIKE 'Copied from the property''s details by migration 044.%'
     OR notes LIKE 'Copied from the vehicle''s details by migration 044.%');
DELETE FROM bills
 WHERE updated_at = created_at
   AND (notes LIKE 'Copied from the property''s details by migration 044.%'
     OR notes LIKE 'Copied from the vehicle''s details by migration 044.%');

-- 2. Budget categories, through the rehome trigger.
DELETE FROM budget_categories;
DROP TRIGGER IF EXISTS budget_categories_rehome_entity_refs ON budget_categories;

-- 3. entity_household() as 041 left it (038 body plus the 041 branch).
CREATE OR REPLACE FUNCTION entity_household(p_kind TEXT, p_id UUID)
RETURNS UUID
LANGUAGE plpgsql STABLE
AS $$
DECLARE
    v_household UUID;
BEGIN
    IF p_kind IS NULL OR p_id IS NULL THEN
        RETURN NULL;
    END IF;
    CASE p_kind
        WHEN 'household'            THEN SELECT id INTO v_household FROM households WHERE id = p_id;
        WHEN 'property'             THEN SELECT household_id INTO v_household FROM properties WHERE id = p_id;
        -- rooms.household_id is nullable until 040; the property is authoritative.
        WHEN 'room'                 THEN SELECT p.household_id INTO v_household
                                         FROM rooms r JOIN properties p ON p.id = r.property_id
                                         WHERE r.id = p_id;
        WHEN 'asset'                THEN SELECT household_id INTO v_household FROM assets WHERE id = p_id;
        WHEN 'vehicle'              THEN SELECT household_id INTO v_household FROM vehicles WHERE id = p_id;
        WHEN 'pet'                  THEN SELECT household_id INTO v_household FROM pets WHERE id = p_id;
        WHEN 'vendor'               THEN SELECT household_id INTO v_household FROM vendors WHERE id = p_id;
        WHEN 'bill'                 THEN SELECT household_id INTO v_household FROM bills WHERE id = p_id;
        WHEN 'loan'                 THEN SELECT household_id INTO v_household FROM loans WHERE id = p_id;
        WHEN 'maintenance_task'     THEN SELECT household_id INTO v_household FROM maintenance_tasks WHERE id = p_id;
        WHEN 'maintenance_schedule' THEN SELECT household_id INTO v_household FROM maintenance_schedules WHERE id = p_id;
        WHEN 'file'                 THEN SELECT household_id INTO v_household FROM files WHERE id = p_id;
        WHEN 'note'                 THEN SELECT household_id INTO v_household FROM notes WHERE id = p_id;
        WHEN 'secret'               THEN SELECT household_id INTO v_household FROM secrets WHERE id = p_id;
        -- 041 people
        WHEN 'person'               THEN SELECT household_id INTO v_household FROM people WHERE id = p_id;
        -- end 041
        ELSE v_household := NULL;
    END CASE;
    RETURN v_household;
END;
$$;

-- entity_kind as 041 left it.
ALTER DOMAIN entity_kind DROP CONSTRAINT entity_kind_check;
ALTER DOMAIN entity_kind ADD CONSTRAINT entity_kind_check CHECK (VALUE IN (
    'household', 'property', 'room', 'asset', 'vehicle', 'pet', 'vendor',
    'bill', 'loan', 'maintenance_task', 'maintenance_schedule',
    'file', 'note', 'secret',
    -- 041
    'person'
));

-- 4. Columns and the table.
DROP INDEX IF EXISTS idx_loans_budget_category;
ALTER TABLE loans DROP CONSTRAINT IF EXISTS loans_budget_category_fkey;
ALTER TABLE loans DROP COLUMN IF EXISTS budget_category_id;
DROP INDEX IF EXISTS idx_bills_budget_category;
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_budget_category_fkey;
ALTER TABLE bills DROP COLUMN IF EXISTS budget_category_id;
DROP TABLE IF EXISTS budget_categories;

ALTER TABLE loans DROP CONSTRAINT IF EXISTS loans_term_end_check;
ALTER TABLE loans DROP CONSTRAINT IF EXISTS loans_due_day_check;
ALTER TABLE loans DROP COLUMN IF EXISTS account_number;
ALTER TABLE loans DROP COLUMN IF EXISTS term_end;
ALTER TABLE loans DROP COLUMN IF EXISTS due_day;

DROP INDEX IF EXISTS idx_bills_due_date;
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_due_day_monthly_check;
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_frequency_check;
ALTER TABLE bills DROP COLUMN IF EXISTS due_date;
ALTER TABLE bills DROP COLUMN IF EXISTS frequency;

COMMIT;
