-- verify_query.sql (#5608) -- proves the "spend on this property" query the
-- ticket is about: property_id = $1 alone is now correct, and the OR form the
-- schema review called "the only safe query" is now redundant.
--
-- Run against a database at 087 (after verify_087.sql, which leaves it clean):
--     psql -d <db> -f verify_query.sql
--
-- The scenario is the one F2 describes: an asset at House A, a bill about that
-- asset, a bill about House A directly, a bill about House B, and a bill about
-- a vehicle. "What do we spend on House A?" must count the asset bill exactly
-- once, and the asset bill must be reachable by property_id = House A.
\set QUIET on

\echo '===== SETUP ====='
\i fixtures.sql

DELETE FROM bills WHERE name LIKE 'q087-%';

-- A bill about the furnace at House A (asset subject), a bill about House A
-- directly, a bill about House B, and a vehicle bill.
INSERT INTO bills (household_id, name, amount, asset_id)
  VALUES ('11111111-1111-1111-1111-111111111111','q087-asset-bill',100,'33333333-3333-3333-3333-333333333333');
INSERT INTO bills (household_id, name, amount, property_id)
  VALUES ('11111111-1111-1111-1111-111111111111','q087-property-bill',20,'22222222-2222-2222-2222-222222222222');
INSERT INTO bills (household_id, name, amount, property_id)
  VALUES ('11111111-1111-1111-1111-111111111111','q087-other-property-bill',7,'44444444-4444-4444-4444-444444444444');
INSERT INTO bills (household_id, name, amount, vehicle_id)
  VALUES ('11111111-1111-1111-1111-111111111111','q087-vehicle-bill',5,'55555555-5555-5555-5555-555555555555');

\echo ''
\echo '===== the asset bill: asset at House A, property_id derived ====='
SELECT name, asset_id, property_id FROM bills WHERE name LIKE 'q087-%' ORDER BY name;

\echo ''
\echo '===== "spend on House A" three ways. All three must equal 120.00 ====='
\echo '-- (1) property_id = $1 alone -- the query the ticket says is now correct'
SELECT COALESCE(SUM(amount),0) AS total_property_only
  FROM bills WHERE property_id = '22222222-2222-2222-2222-222222222222';

\echo '-- (2) the OR form the review called "the only safe query"'
SELECT COALESCE(SUM(amount),0) AS total_or_form
  FROM bills
 WHERE property_id = '22222222-2222-2222-2222-222222222222'
    OR asset_id IN (SELECT id FROM assets WHERE property_id = '22222222-2222-2222-2222-222222222222');

\echo '-- (3) the asset bill on its own, to show it is counted once and reachable'
SELECT COUNT(*) AS asset_bill_rows,
       COALESCE(SUM(amount),0) AS asset_bill_total
  FROM bills WHERE name = 'q087-asset-bill';

\echo ''
\echo '===== the OR form is redundant, not merely equal: it adds no row ====='
\echo '-- rows the OR form finds that property_id = $1 does not'
SELECT COUNT(*) AS extra_rows_from_the_or
  FROM bills
 WHERE NOT (property_id = '22222222-2222-2222-2222-222222222222')
   AND asset_id IN (SELECT id FROM assets WHERE property_id = '22222222-2222-2222-2222-222222222222');

\echo ''
\echo '===== a contradiction cannot be created, so the total cannot drift ====='
\echo '-- attempting to file the asset bill against House B (expect an ERROR)'
DO $$
BEGIN
  UPDATE bills SET property_id = '44444444-4444-4444-4444-444444444444' WHERE name = 'q087-asset-bill';
  RAISE NOTICE 'UPDATE SUCCEEDED -- the contradiction is reachable <-- FAIL';
EXCEPTION WHEN check_violation THEN
  RAISE NOTICE 'refused: % (%)', SQLERRM, SQLSTATE;
END $$;

\echo ''
\echo '-- and the total is still 120.00 after the refused write'
SELECT COALESCE(SUM(amount),0) AS total_property_only_after_refusal
  FROM bills WHERE property_id = '22222222-2222-2222-2222-222222222222';

DELETE FROM bills WHERE name LIKE 'q087-%';
