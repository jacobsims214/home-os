-- verify_query_drift.sql (#5608) -- the OTHER half of the query proof, and the
-- reason the ticket exists. Run this against a database at 085 (no 087).
--
--     psql -d <db> -f verify_query_drift.sql
--
-- It shows the defect F2 describes, in numbers: on the old schema a row can be
-- about an asset at House B and about House A at the same time, and then
-- "what do we spend on House A?" counts a House B bill as House A's. Run
-- verify_query.sql on the same database after 087 and the same question gives
-- the right answer.
--
-- The row is built the way stale rows exist in the wild: the derive trigger is
-- what keeps property_id in step, so a row written while it is disabled (a bulk
-- load, a restore, a pre-038 dataset) is a stale row. That is not a contrived
-- state -- it is exactly the population 087's backfill repairs.
\set QUIET on

\echo '===== SETUP ====='
\i fixtures.sql

DELETE FROM bills WHERE name LIKE 'q087-%';

-- House A = 2222..., House B = 4444...; asset 3333... is at House A.
-- The bill is about asset 3333... (House A) but carries property_id House B.
ALTER TABLE bills DISABLE TRIGGER bills_derive_subject_property;
INSERT INTO bills (household_id, name, amount, asset_id, property_id)
  VALUES ('11111111-1111-1111-1111-111111111111','q087-drift-bill',100,
          '33333333-3333-3333-3333-333333333333','44444444-4444-4444-4444-444444444444');
ALTER TABLE bills ENABLE TRIGGER bills_derive_subject_property;

INSERT INTO bills (household_id, name, amount, property_id)
  VALUES ('11111111-1111-1111-1111-111111111111','q087-house-a-bill',20,'22222222-2222-2222-2222-222222222222');
INSERT INTO bills (household_id, name, amount, property_id)
  VALUES ('11111111-1111-1111-1111-111111111111','q087-house-b-bill',7,'44444444-4444-4444-4444-444444444444');

\echo ''
\echo '===== the row: about an asset at House A, but saying House B ====='
SELECT name, amount, asset_id, property_id,
       (SELECT property_id FROM assets WHERE id = b.asset_id) AS asset_is_at
  FROM bills b WHERE name LIKE 'q087-%' ORDER BY name;

\echo ''
\echo '===== "spend on House A" -- the wrong answer ====='
\echo '-- the bill is a House A bill ($100 about the House A furnace), so the'
\echo '-- right total is 120.00 (100 + the 20 house-a bill).'
\echo '-- (1) property_id = $1 alone: MISSES the asset bill entirely'
SELECT COALESCE(SUM(amount),0) AS total_property_only
  FROM bills WHERE property_id = '22222222-2222-2222-2222-222222222222';

\echo '-- (2) the OR form: finds it, but via the asset, so it counts a row'
\echo '--     whose property_id says House B'
SELECT COALESCE(SUM(amount),0) AS total_or_form
  FROM bills
 WHERE property_id = '22222222-2222-2222-2222-222222222222'
    OR asset_id IN (SELECT id FROM assets WHERE property_id = '22222222-2222-2222-2222-222222222222');

\echo ''
\echo '===== "spend on House B" -- the same row again ====='
\echo '-- the row is about a House A asset, so House B should NOT include it.'
\echo '-- Its property_id says House B, so this counts it.'
SELECT COALESCE(SUM(amount),0) AS total_house_b
  FROM bills WHERE property_id = '44444444-4444-4444-4444-444444444444';

\echo ''
\echo '===== THE DRIFT ====='
\echo '-- The 100.00 bill is counted under BOTH houses: the OR form gives House A'
\echo '-- 120.00 (it finds the row via the asset) and property_id = House B gives'
\echo '-- 107.00 (it finds the same row via property_id). 120 + 107 = 227, but the'
\echo '-- bills total 127. A row can be about an asset and a different property at'
\echo '-- once, and then NO single query is right: property_id alone under-counts'
\echo '-- House A, and the OR form double-counts. That is why the rule has to be'
\echo '-- enforced on write rather than repaired in every query.'
SELECT (SELECT COALESCE(SUM(amount),0) FROM bills
         WHERE property_id = '22222222-2222-2222-2222-222222222222'
            OR asset_id IN (SELECT id FROM assets WHERE property_id = '22222222-2222-2222-2222-222222222222'))
     + (SELECT COALESCE(SUM(amount),0) FROM bills WHERE property_id = '44444444-4444-4444-4444-444444444444')
       AS summed_over_both_houses,
       (SELECT COALESCE(SUM(amount),0) FROM bills WHERE name LIKE 'q087-%') AS actual_total;

DELETE FROM bills WHERE name LIKE 'q087-%';
