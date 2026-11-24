-- verify_089.sql (#5515) -- proves the 089 subject CHECK on all five tables.
--
-- Run against a database that is at 089, from THIS directory:
--     cd packages/db/testdata/subject_check
--     psql -d <db> -f verify_089.sql
--
-- Every case is a subtransaction, so one expected rejection does not abort the
-- run. Each line prints ok / <-- FAIL; a passing run has no <-- FAIL and no
-- ERROR. Re-runnable: the probe rows are cleared first.
\set QUIET on
\pset footer off
\echo '==================== FIXTURES ===================='
\i fixtures.sql

\echo ''
\echo '==================== CONSTRAINT DEFINITIONS (pg_get_constraintdef) ===================='
SELECT conrelid::regclass AS table, conname, pg_get_constraintdef(oid) AS definition
FROM pg_constraint
WHERE conname IN ('bills_subject_check','loans_subject_check',
                  'maintenance_schedules_subject_check','maintenance_tasks_subject_check',
                  'guides_subject_check')
ORDER BY 1;

\echo ''
\echo '==================== AC2: TWO SUBJECTS MUST BE REJECTED ===================='
DO $$
DECLARE
  hh   uuid := '11111111-1111-1111-1111-111111111111';
  prop uuid := '22222222-2222-2222-2222-222222222222';
  asst uuid := '33333333-3333-3333-3333-333333333333';
  veh  uuid := '55555555-5555-5555-5555-555555555555';
  t    text;
  ins  text;
  got  text;
  nbig int := 0;
BEGIN
  -- clean slate so the matrix is re-runnable
  DELETE FROM bills                 WHERE name  LIKE '%-%' AND name IN ('one-asset','one-property','one-vehicle','no-subject');
  DELETE FROM loans                 WHERE name  IN ('one-asset','one-property','one-vehicle','no-subject');
  DELETE FROM maintenance_schedules WHERE name  IN ('one-asset','one-property','one-vehicle','no-subject');
  DELETE FROM maintenance_tasks     WHERE name  IN ('one-asset','one-property','one-vehicle','no-subject');
  DELETE FROM guides                WHERE title IN ('one-asset','one-property','one-vehicle','no-subject');

  FOREACH t IN ARRAY ARRAY['bills','loans','maintenance_schedules','maintenance_tasks','guides'] LOOP
    -- per-table minimal insert; every one gets the same 4 subject combinations
    ins := CASE t
      WHEN 'bills'                 THEN 'INSERT INTO %I (household_id,name,frequency,due_date,%s) VALUES ($1,$2,''annual'',CURRENT_DATE,%s)'
      WHEN 'loans'                 THEN 'INSERT INTO %I (household_id,name,original_amount,remaining_balance,%s) VALUES ($1,$2,1000,1000,%s)'
      WHEN 'maintenance_schedules' THEN 'INSERT INTO %I (household_id,name,rrule,%s) VALUES ($1,$2,''FREQ=YEARLY'',%s)'
      WHEN 'maintenance_tasks'     THEN 'INSERT INTO %I (household_id,name,%s) VALUES ($1,$2,%s)'
      ELSE                              'INSERT INTO %I (household_id,title,%s) VALUES ($1,$2,%s)'
    END;

    -- (a) vehicle + asset      -> must be REJECTED
    BEGIN
      EXECUTE format(ins, t, 'vehicle_id,asset_id', '$3,$4') USING hh, 'two-veh-asset', veh, asst;
      RAISE NOTICE '%  vehicle+asset      : ACCEPTED  <-- FAIL', rpad(t,22);
    EXCEPTION WHEN check_violation THEN
      RAISE NOTICE '%  vehicle+asset      : REJECTED  (ok)', rpad(t,22);
    END;

    -- (b) vehicle + property    -> must be REJECTED
    BEGIN
      EXECUTE format(ins, t, 'vehicle_id,property_id', '$3,$4') USING hh, 'two-veh-prop', veh, prop;
      RAISE NOTICE '%  vehicle+property    : ACCEPTED  <-- FAIL', rpad(t,22);
    EXCEPTION WHEN check_violation THEN
      RAISE NOTICE '%  vehicle+property    : REJECTED  (ok)', rpad(t,22);
    END;

    -- (c) vehicle + asset + property (all three) -> must be REJECTED
    BEGIN
      EXECUTE format(ins, t, 'vehicle_id,asset_id,property_id', '$3,$4,$5') USING hh, 'two-all', veh, asst, prop;
      RAISE NOTICE '%  vehicle+asset+prop  : ACCEPTED  <-- FAIL', rpad(t,22);
    EXCEPTION WHEN check_violation THEN
      RAISE NOTICE '%  vehicle+asset+prop  : REJECTED  (ok)', rpad(t,22);
    END;

    -- (d) asset alone  -> must SUCCEED, and property_id must be DERIVED
    BEGIN
      EXECUTE format(ins, t, 'asset_id', '$3') USING hh, 'one-asset', asst;
      EXECUTE format('SELECT property_id::text FROM %I WHERE %s = $1',
                     t, CASE t WHEN 'guides' THEN 'title' ELSE 'name' END)
        INTO got USING 'one-asset';
      IF got = prop::text THEN
        RAISE NOTICE '%  asset alone        : ACCEPTED, property derived (ok)', rpad(t,22);
      ELSE
        RAISE NOTICE '%  asset alone        : ACCEPTED but property_id=% (expected %) <-- FAIL', rpad(t,22), got, prop;
      END IF;
    EXCEPTION WHEN check_violation THEN
      RAISE NOTICE '%  asset alone        : REJECTED  <-- FAIL (breaks every asset subject)', rpad(t,22);
    END;

    -- (e) property alone -> must SUCCEED
    BEGIN
      EXECUTE format(ins, t, 'property_id', '$3') USING hh, 'one-property', prop;
      RAISE NOTICE '%  property alone     : ACCEPTED  (ok)', rpad(t,22);
    EXCEPTION WHEN check_violation THEN
      RAISE NOTICE '%  property alone     : REJECTED  <-- FAIL', rpad(t,22);
    END;

    -- (f) vehicle alone -> must SUCCEED
    BEGIN
      EXECUTE format(ins, t, 'vehicle_id', '$3') USING hh, 'one-vehicle', veh;
      RAISE NOTICE '%  vehicle alone      : ACCEPTED  (ok)', rpad(t,22);
    EXCEPTION WHEN check_violation THEN
      RAISE NOTICE '%  vehicle alone      : REJECTED  <-- FAIL', rpad(t,22);
    END;

    -- (g) no subject at all -> must SUCCEED
    BEGIN
      EXECUTE CASE t
        WHEN 'bills'                 THEN 'INSERT INTO bills (household_id,name,frequency,due_date) VALUES ($1,''no-subject'',''annual'',CURRENT_DATE)'
        WHEN 'loans'                 THEN 'INSERT INTO loans (household_id,name,original_amount,remaining_balance) VALUES ($1,''no-subject'',1000,1000)'
        WHEN 'maintenance_schedules' THEN 'INSERT INTO maintenance_schedules (household_id,name,rrule) VALUES ($1,''no-subject'',''FREQ=YEARLY'')'
        WHEN 'maintenance_tasks'     THEN 'INSERT INTO maintenance_tasks (household_id,name) VALUES ($1,''no-subject'')'
        ELSE                              'INSERT INTO guides (household_id,title) VALUES ($1,''no-subject'')'
      END USING hh;
      RAISE NOTICE '%  none               : ACCEPTED  (ok)', rpad(t,22);
    EXCEPTION WHEN check_violation THEN
      RAISE NOTICE '%  none               : REJECTED  <-- FAIL', rpad(t,22);
    END;
  END LOOP;
END $$;

\echo ''
\echo '==================== AC4: NOT VALID must be gone (constraints validated) ===================='
SELECT conrelid::regclass AS table, conname, convalidated
FROM pg_constraint WHERE conname LIKE '%_subject_check' ORDER BY 1;
