-- verify_087.sql (#5608) -- proves the subject rule migration 087 installs in
-- derive_subject_property(), on all five tables that carry the subject pattern:
-- bills, loans, maintenance_schedules, maintenance_tasks, guides.
--
-- Run against a database that is at 087, from THIS directory:
--     cd packages/db/testdata/subject_contradiction
--     psql -d <db> -f verify_087.sql
--
-- REVERT-CHECK. This script ALWAYS asserts 087's rule. It is the same file in
-- both runs, so the only thing that changes is the schema:
--
--   against a database at 087          -> 80/80 ok, no <-- FAIL
--   against a database at 085 (no 087) -> the contradiction cases print
--                                         <-- FAIL, because the old trigger
--                                         accepted them and overwrote the
--                                         caller's property_id without a word
--
-- The vehicle cases are CHECK cases, refused by 083's *_subject_check in BOTH
-- runs; they are here as regression guards (087 must not lose them).
--
-- Every case is its own subtransaction, so an expected rejection does not abort
-- the run. Re-runnable: probe rows are cleared first and last.
\set QUIET on
\pset footer off

\echo '==================== FIXTURES ===================='
\i fixtures.sql

\echo ''
\echo '==================== CASES ===================='

CREATE TEMP TABLE probe_results (
  seq        serial,
  tbl        text,
  case_name  text,
  expected   text,
  got        text,
  detail     text,
  ok         boolean
);

DO $$
DECLARE
  hh       uuid := '11111111-1111-1111-1111-111111111111';
  propA    uuid := '22222222-2222-2222-2222-222222222222';
  propB    uuid := '44444444-4444-4444-4444-444444444444';
  assetA   uuid := '33333333-3333-3333-3333-333333333333';  -- at House A
  assetB   uuid := '77777777-7777-7777-7777-777777777777';  -- at House B
  assetN   uuid := '88888888-8888-8888-8888-888888888888';  -- at no property
  veh      uuid := '55555555-5555-5555-5555-555555555555';

  t        text;
  key      text;
  ins      text;   -- INSERT template with subject columns: %I table, %s columns, %s values
  insNone  text;   -- INSERT template with NO subject columns: %I table
  stmt     text;
  nm       text;
  got      text;
  cname    text;
  detail   text;
  want     text;
  want_ok  boolean;
  rid      uuid;
  derived  uuid;
  n_ok     int := 0;
  n_fail   int := 0;
BEGIN
  FOREACH t IN ARRAY ARRAY['bills','loans','maintenance_schedules',
                           'maintenance_tasks','guides'] LOOP
    key := CASE t WHEN 'guides' THEN 'title' ELSE 'name' END;
    ins := CASE t
      WHEN 'bills' THEN
        'INSERT INTO %I (household_id,name,frequency,due_date,%s) VALUES ($1,$2,''annual'',CURRENT_DATE,%s)'
      WHEN 'loans' THEN
        'INSERT INTO %I (household_id,name,original_amount,remaining_balance,%s) VALUES ($1,$2,1000,1000,%s)'
      WHEN 'maintenance_schedules' THEN
        'INSERT INTO %I (household_id,name,rrule,%s) VALUES ($1,$2,''FREQ=YEARLY'',%s)'
      WHEN 'maintenance_tasks' THEN
        'INSERT INTO %I (household_id,name,%s) VALUES ($1,$2,%s)'
      ELSE
        'INSERT INTO %I (household_id,title,%s) VALUES ($1,$2,%s)'
    END;
    -- The same tables with no subject column at all, for the "family row gets
    -- a subject" cases. A separate template, so a bug in one cannot hide in
    -- the other.
    insNone := CASE t
      WHEN 'bills' THEN
        'INSERT INTO %I (household_id,name,frequency,due_date) VALUES ($1,$2,''annual'',CURRENT_DATE)'
      WHEN 'loans' THEN
        'INSERT INTO %I (household_id,name,original_amount,remaining_balance) VALUES ($1,$2,1000,1000)'
      WHEN 'maintenance_schedules' THEN
        'INSERT INTO %I (household_id,name,rrule) VALUES ($1,$2,''annual'')'
      WHEN 'maintenance_tasks' THEN
        'INSERT INTO %I (household_id,name) VALUES ($1,$2)'
      ELSE
        'INSERT INTO %I (household_id,title) VALUES ($1,$2)'
    END;

    -- Clean slate for this table.
    EXECUTE format('DELETE FROM %I WHERE %I LIKE ''p087-%%''', t, key);

    -- ------------------------------------------------------------------
    -- INSERT cases
    -- ------------------------------------------------------------------

    -- 1. asset only -> ACCEPTED, property derived from the asset.
    nm := 'p087-ins-asset-only';
    BEGIN
      EXECUTE format(ins, t, 'asset_id', '$3') USING hh, nm, assetA;
      EXECUTE format('SELECT property_id FROM %I WHERE %I = $1', t, key) INTO derived USING nm;
      IF derived = propA THEN
        INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
          VALUES (t,'insert: asset only','ACCEPT + derive House A','ACCEPT','property_id='||derived,TRUE);
      ELSE
        INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
          VALUES (t,'insert: asset only','ACCEPT + derive House A','ACCEPT','property_id='||COALESCE(derived::text,'NULL'),FALSE);
      END IF;
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: asset only','ACCEPT + derive House A','REJECTED',SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM,FALSE);
    END;

    -- 2. asset + its own property -> ACCEPTED (the normal derived pair).
    nm := 'p087-ins-asset-match';
    BEGIN
      EXECUTE format(ins, t, 'asset_id,property_id', '$3,$4') USING hh, nm, assetA, propA;
      EXECUTE format('SELECT property_id FROM %I WHERE %I = $1', t, key) INTO derived USING nm;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: asset + its own property','ACCEPT','ACCEPT','property_id='||COALESCE(derived::text,'NULL'),derived = propA);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: asset + its own property','ACCEPT','REJECTED',SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM,FALSE);
    END;

    -- 3. CONTRADICTION via INSERT: asset at House A, property House B.
    want := 'REJECT';
    nm := 'p087-ins-asset-conflict';
    BEGIN
      EXECUTE format(ins, t, 'asset_id,property_id', '$3,$4') USING hh, nm, assetA, propB;
      EXECUTE format('SELECT property_id FROM %I WHERE %I = $1', t, key) INTO derived USING nm;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: asset + another property (contradiction)',want,'ACCEPT',
                'ACCEPTED and the caller''s property_id was overwritten to '||COALESCE(derived::text,'NULL'), FALSE);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: asset + another property (contradiction)',want,'REJECTED',
                SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM, want = 'REJECT');
    END;

    -- 4. CONTRADICTION via INSERT: an asset at no property, plus a property.
    want := 'REJECT';
    nm := 'p087-ins-asset-noprop-conflict';
    BEGIN
      EXECUTE format(ins, t, 'asset_id,property_id', '$3,$4') USING hh, nm, assetN, propA;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: asset at no property + a property (contradiction)',want,'ACCEPT','ACCEPTED',FALSE);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: asset at no property + a property (contradiction)',want,'REJECTED',
                SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM, want = 'REJECT');
    END;

    -- 5. asset at no property, alone -> ACCEPTED, property NULL.
    nm := 'p087-ins-asset-noprop';
    BEGIN
      EXECUTE format(ins, t, 'asset_id', '$3') USING hh, nm, assetN;
      EXECUTE format('SELECT property_id FROM %I WHERE %I = $1', t, key) INTO derived USING nm;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: asset at no property','ACCEPT + NULL property','ACCEPT',
                'property_id='||COALESCE(derived::text,'NULL'), derived IS NULL);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: asset at no property','ACCEPT + NULL property','REJECTED',SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM,FALSE);
    END;

    -- 6. property only -> ACCEPTED.
    nm := 'p087-ins-property';
    BEGIN
      EXECUTE format(ins, t, 'property_id', '$3') USING hh, nm, propA;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: property only','ACCEPT','ACCEPT','',TRUE);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: property only','ACCEPT','REJECTED',SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM,FALSE);
    END;

    -- 7. vehicle only -> ACCEPTED.
    nm := 'p087-ins-vehicle';
    BEGIN
      EXECUTE format(ins, t, 'vehicle_id', '$3') USING hh, nm, veh;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: vehicle only','ACCEPT','ACCEPT','',TRUE);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: vehicle only','ACCEPT','REJECTED',SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM,FALSE);
    END;

    -- 8. vehicle + property -> REJECTED (CHECK case, refused before and after 087).
    nm := 'p087-ins-veh-prop';
    BEGIN
      EXECUTE format(ins, t, 'vehicle_id,property_id', '$3,$4') USING hh, nm, veh, propA;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: vehicle + property','REJECT','ACCEPT','',FALSE);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: vehicle + property','REJECT','REJECTED',SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM,TRUE);
    END;

    -- 9. vehicle + asset -> REJECTED (CHECK case, refused before and after 087).
    nm := 'p087-ins-veh-asset';
    BEGIN
      EXECUTE format(ins, t, 'vehicle_id,asset_id', '$3,$4') USING hh, nm, veh, assetA;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: vehicle + asset','REJECT','ACCEPT','',FALSE);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'insert: vehicle + asset','REJECT','REJECTED',SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM,TRUE);
    END;

    -- ------------------------------------------------------------------
    -- UPDATE cases (one fresh row per case, so each is independent)
    -- ------------------------------------------------------------------

    -- 10. CONTRADICTION via UPDATE: an asset row at House A, property set to B.
    want := 'REJECT';
    nm := 'p087-upd-prop-conflict';
    BEGIN
      EXECUTE format(ins, t, 'asset_id', '$3') USING hh, nm, assetA;
      EXECUTE format('UPDATE %I SET property_id = $1 WHERE %I = $2', t, key) USING propB, nm;
      EXECUTE format('SELECT property_id FROM %I WHERE %I = $1', t, key) INTO derived USING nm;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: asset row, property set to another property',want,'ACCEPT',
                'ACCEPTED and the caller''s property_id was overwritten to '||COALESCE(derived::text,'NULL'), FALSE);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: asset row, property set to another property',want,'REJECTED',
                SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM, want = 'REJECT');
    END;

    -- 11. CONTRADICTION via UPDATE: asset + property moved together to B.
    want := 'REJECT';
    nm := 'p087-upd-both-conflict';
    BEGIN
      EXECUTE format(ins, t, 'asset_id', '$3') USING hh, nm, assetA;
      EXECUTE format('UPDATE %I SET asset_id = $1, property_id = $2 WHERE %I = $3', t, key)
        USING assetA, propB, nm;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: asset row, asset+property moved to another property',want,'ACCEPT','ACCEPTED',FALSE);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: asset row, asset+property moved to another property',want,'REJECTED',
                SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM, want = 'REJECT');
    END;

    -- 12. CONTRADICTION via UPDATE: a family row given an asset and another property.
    want := 'REJECT';
    nm := 'p087-upd-family-to-conflict';
    BEGIN
      EXECUTE format(insNone, t) USING hh, nm;
      EXECUTE format('UPDATE %I SET asset_id = $1, property_id = $2 WHERE %I = $3', t, key)
        USING assetA, propB, nm;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: family row given an asset and another property',want,'ACCEPT','ACCEPTED',FALSE);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: family row given an asset and another property',want,'REJECTED',
                SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM, want = 'REJECT');
    END;

    -- 13. vehicle on an asset row -> REJECTED (CHECK case, refused before and after 087).
    nm := 'p087-upd-vehicle-on-asset';
    BEGIN
      EXECUTE format(ins, t, 'asset_id', '$3') USING hh, nm, assetA;
      EXECUTE format('UPDATE %I SET vehicle_id = $1 WHERE %I = $2', t, key) USING veh, nm;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: vehicle on an asset row','REJECT','ACCEPT','',FALSE);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: vehicle on an asset row','REJECT','REJECTED',SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM,TRUE);
    END;

    -- 14. vehicle + property on a family row -> REJECTED (CHECK case, refused before and after 087).
    nm := 'p087-upd-veh-prop';
    BEGIN
      EXECUTE format(insNone, t) USING hh, nm;
      EXECUTE format('UPDATE %I SET vehicle_id = $1, property_id = $2 WHERE %I = $3', t, key)
        USING veh, propA, nm;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: vehicle + property','REJECT','ACCEPT','',FALSE);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: vehicle + property','REJECT','REJECTED',SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM,TRUE);
    END;

    -- 15. LEGITIMATE: move the asset to the House B asset, property NOT named.
    -- The stored property_id is carried, not presented, so it is re-derived.
    nm := 'p087-upd-asset-carry';
    BEGIN
      EXECUTE format(ins, t, 'asset_id', '$3') USING hh, nm, assetA;
      EXECUTE format('UPDATE %I SET asset_id = $1 WHERE %I = $2', t, key) USING assetB, nm;
      EXECUTE format('SELECT property_id FROM %I WHERE %I = $1', t, key) INTO derived USING nm;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: asset moved to another asset, property carried','ACCEPT + re-derive House B','ACCEPT',
                'property_id='||COALESCE(derived::text,'NULL'), derived = propB);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: asset moved to another asset, property carried','ACCEPT + re-derive House B','REJECTED',
                SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM,FALSE);
    END;

    -- 15b. LEGITIMATE: clearing property_id on an asset row (NULL means
    -- "derive it", and an asset leaving its property does exactly this).
    nm := 'p087-upd-null-property';
    BEGIN
      EXECUTE format(ins, t, 'asset_id', '$3') USING hh, nm, assetA;
      EXECUTE format('UPDATE %I SET property_id = NULL WHERE %I = $1', t, key) USING nm;
      EXECUTE format('SELECT property_id FROM %I WHERE %I = $1', t, key) INTO derived USING nm;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: property_id cleared to NULL on an asset row','ACCEPT + re-derive House A','ACCEPT',
                'property_id='||COALESCE(derived::text,'NULL'), derived = propA);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: property_id cleared to NULL on an asset row','ACCEPT + re-derive House A','REJECTED',
                SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM,FALSE);
    END;

    -- 15c. LEGITIMATE: the asset MOVES property and the row follows, through
    -- the database's own path (assets_propagate_property for four tables,
    -- guides_follow_asset_property for guides). That trigger PRESENTS the
    -- asset's new property, and it agrees by construction -- so this is the
    -- proof that the rule leaves the maintenance path working. The asset is
    -- moved back at the end, so no other case sees it.
    nm := 'p087-upd-asset-moves';
    BEGIN
      EXECUTE format(ins, t, 'asset_id', '$3') USING hh, nm, assetA;
      UPDATE assets SET property_id = propB WHERE id = assetA;
      EXECUTE format('SELECT property_id FROM %I WHERE %I = $1', t, key) INTO derived USING nm;
      UPDATE assets SET property_id = propA WHERE id = assetA;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: asset moves property, row follows','ACCEPT + follow House B','ACCEPT',
                'property_id='||COALESCE(derived::text,'NULL'), derived = propB);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      UPDATE assets SET property_id = propA WHERE id = assetA;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: asset moves property, row follows','ACCEPT + follow House B','REJECTED',
                SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM,FALSE);
    END;

    -- 16. LEGITIMATE: an unrelated column update on an asset row.
    nm := 'p087-upd-name-only';
    BEGIN
      EXECUTE format(ins, t, 'asset_id', '$3') USING hh, nm, assetA;
      EXECUTE format('UPDATE %I SET %I = %I || ''-renamed'' WHERE %I = $1', t, key, key, key) USING nm;
      EXECUTE format('SELECT property_id FROM %I WHERE %I = $1', t, key) INTO derived USING nm || '-renamed';
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: name only on an asset row','ACCEPT','ACCEPT',
                'property_id='||COALESCE(derived::text,'NULL'), derived = propA);
    EXCEPTION WHEN OTHERS THEN
      GET STACKED DIAGNOSTICS cname = CONSTRAINT_NAME;
      INSERT INTO probe_results(tbl,case_name,expected,got,detail,ok)
        VALUES (t,'update: name only on an asset row','ACCEPT','REJECTED',SQLSTATE||' '||COALESCE(cname,'')||' '||SQLERRM,FALSE);
    END;

    EXECUTE format('DELETE FROM %I WHERE %I LIKE ''p087-%%''', t, key);
  END LOOP;
END $$;

\echo ''
\echo '==================== RESULTS ===================='
SELECT rpad(tbl,22) AS table, rpad(case_name,58) AS case, rpad(expected,32) AS expected,
       rpad(got,9) AS got, ok, detail,
       CASE WHEN ok THEN '' ELSE '<-- FAIL' END AS verdict
FROM probe_results
ORDER BY seq;

\echo ''
\echo '==================== SUMMARY ===================='
SELECT count(*) FILTER (WHERE ok)     AS ok,
       count(*) FILTER (WHERE NOT ok) AS failed,
       count(*)                       AS total,
       CASE WHEN count(*) FILTER (WHERE NOT ok) = 0 THEN 'PASS' ELSE 'FAIL' END AS verdict
FROM probe_results;

\echo ''
\echo '==================== TRIGGERS AND FUNCTION IN THE CATALOGUE ===================='
SELECT c.relname AS table, t.tgname AS trigger, p.proname AS function
FROM pg_trigger t
JOIN pg_class c ON c.oid = t.tgrelid
JOIN pg_proc  p ON p.oid = t.tgfoid
WHERE t.tgname LIKE '%derive_subject_property'
ORDER BY 1;

\echo ''
\echo '==================== CONSTRAINT DEFINITIONS (still 083''s) ===================='
SELECT conrelid::regclass AS table, conname, pg_get_constraintdef(oid) AS definition
FROM pg_constraint
WHERE conname IN ('bills_subject_check','loans_subject_check',
                  'maintenance_schedules_subject_check','maintenance_tasks_subject_check',
                  'guides_subject_check')
ORDER BY 1;
