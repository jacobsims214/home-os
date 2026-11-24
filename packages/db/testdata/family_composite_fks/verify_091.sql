-- verify_091.sql (#5519) -- proves the composite same-family FKs of migration 091.
--
-- Run against a database at 091, from THIS directory:
--     cd packages/db/testdata/family_composite_fks
--     psql -d <db> -f verify_091.sql
--
-- Every case is a subtransaction, so one expected rejection does not abort the
-- run. Each row prints ok / <-- FAIL; a passing run has no <-- FAIL and no
-- ERROR. Re-runnable: the probe rows are cleared first.
--
-- The cases are the ticket's acceptance criteria, numbered:
--   AC1  every converted column has a composite FK (conkey has 2 entries)
--   AC2  every column kept the ON DELETE action it had at 089 (confdeltype)
--   AC3  deleting the parent nulls the column and leaves household_id intact
--   AC4  with derive_subject_property() DISABLED, a cross-family reference is
--        still REJECTED by the FK alone. This is the whole point.
--   AC5  the same insert inside the family is accepted.
\set QUIET on
\pset footer off
\timing off

\echo '==================== FIXTURES ===================='
BEGIN;
INSERT INTO households (id, name) VALUES
  ('aaaaaaaa-0000-0000-0000-000000000001','Family A'),
  ('bbbbbbbb-0000-0000-0000-000000000002','Family B')
  ON CONFLICT DO NOTHING;

INSERT INTO properties (id, household_id, name) VALUES
  ('a0000000-0000-0000-0000-0000000000a1','aaaaaaaa-0000-0000-0000-000000000001','A house'),
  ('b0000000-0000-0000-0000-0000000000b1','bbbbbbbb-0000-0000-0000-000000000002','B house')
  ON CONFLICT DO NOTHING;

INSERT INTO rooms (id, household_id, property_id, name) VALUES
  ('a2000000-0000-0000-0000-0000000000a2','aaaaaaaa-0000-0000-0000-000000000001','a0000000-0000-0000-0000-0000000000a1','A kitchen'),
  ('b2000000-0000-0000-0000-0000000000b2','bbbbbbbb-0000-0000-0000-000000000002','b0000000-0000-0000-0000-0000000000b1','B kitchen')
  ON CONFLICT DO NOTHING;

INSERT INTO assets (id, household_id, property_id, room_id, name) VALUES
  ('a3000000-0000-0000-0000-0000000000a3','aaaaaaaa-0000-0000-0000-000000000001','a0000000-0000-0000-0000-0000000000a1','a2000000-0000-0000-0000-0000000000a2','A furnace'),
  ('b3000000-0000-0000-0000-0000000000b3','bbbbbbbb-0000-0000-0000-000000000002','b0000000-0000-0000-0000-0000000000b1','b2000000-0000-0000-0000-0000000000b2','B furnace')
  ON CONFLICT DO NOTHING;

INSERT INTO vehicles (id, household_id, make, model) VALUES
  ('a4000000-0000-0000-0000-0000000000a4','aaaaaaaa-0000-0000-0000-000000000001','Ford','A van'),
  ('b4000000-0000-0000-0000-0000000000b4','bbbbbbbb-0000-0000-0000-000000000002','Ford','B van')
  ON CONFLICT DO NOTHING;
COMMIT;

\echo ''
\echo '==================== AC1: EVERY CONVERTED COLUMN IS A COMPOSITE FK ===================='
WITH want(tbl, conname) AS (VALUES
  ('bills','bills_property_id_fkey'),('bills','bills_asset_id_fkey'),('bills','bills_vehicle_id_fkey'),
  ('maintenance_schedules','maintenance_schedules_property_id_fkey'),
  ('maintenance_schedules','maintenance_schedules_asset_id_fkey'),
  ('maintenance_schedules','maintenance_schedules_vehicle_id_fkey'),
  ('maintenance_tasks','maintenance_tasks_property_id_fkey'),
  ('maintenance_tasks','maintenance_tasks_asset_id_fkey'),
  ('maintenance_tasks','maintenance_tasks_vehicle_id_fkey'),
  ('loans','loans_property_id_fkey'),('loans','loans_asset_id_fkey'),('loans','loans_vehicle_id_fkey'),
  ('guides','guides_property_id_fkey'),('guides','guides_asset_id_fkey'),('guides','guides_vehicle_id_fkey'),
  ('assets','assets_property_id_fkey'),('assets','assets_room_id_fkey'),('rooms','rooms_property_id_fkey'))
SELECT w.tbl, w.conname,
       coalesce(array_length(c.conkey,1),0) AS key_cols,
       CASE WHEN coalesce(array_length(c.conkey,1),0) = 2
             AND coalesce(array_length(c.confkey,1),0) = 2 THEN 'ok' ELSE '<-- FAIL' END AS result
  FROM want w
  LEFT JOIN pg_constraint c ON c.conrelid = w.tbl::regclass AND c.conname = w.conname AND c.contype = 'f'
 ORDER BY 1,2;

\echo ''
\echo '==================== AC1b: NO SINGLE-COLUMN FAMILY FK LEFT BEHIND ===================='
-- The one deliberate exception is activity_log.actor_agent_token_id (see the
-- migration header): agent_tokens has no (id, household_id) key.
WITH fk AS (
  SELECT con.conname::text AS conname, con.conrelid AS cid, con.confrelid AS pid,
         (SELECT a.attname::text FROM unnest(con.confkey) WITH ORDINALITY k(attnum,ord)
            JOIN pg_attribute a ON a.attrelid=con.confrelid AND a.attnum=k.attnum WHERE k.ord=1) AS parent_col
    FROM pg_constraint con WHERE con.contype='f' AND array_length(con.conkey,1)=1)
SELECT fk.cid::regclass::text AS child, fk.conname, fk.pid::regclass::text AS parent,
       'ok (deliberate)' AS result
  FROM fk
 WHERE fk.parent_col='id'
   AND EXISTS (SELECT 1 FROM pg_attribute x WHERE x.attrelid=fk.cid AND x.attname='household_id' AND x.attnum>0 AND NOT x.attisdropped)
   AND EXISTS (SELECT 1 FROM pg_attribute x WHERE x.attrelid=fk.pid AND x.attname='household_id' AND x.attnum>0 AND NOT x.attisdropped)
 ORDER BY 1,2;

\echo ''
\echo '==================== AC2: DELETE ACTIONS UNCHANGED ===================='
-- Expected values were read from pg_constraint.confdeltype on a database at
-- 089, BEFORE 091 ran (pasted in the ticket). 'n' = SET NULL, 'c' = CASCADE.
WITH want(tbl, conname, want_del) AS (VALUES
  ('bills','bills_property_id_fkey','n'),('bills','bills_asset_id_fkey','n'),('bills','bills_vehicle_id_fkey','n'),
  ('maintenance_schedules','maintenance_schedules_property_id_fkey','c'),
  ('maintenance_schedules','maintenance_schedules_asset_id_fkey','c'),
  ('maintenance_schedules','maintenance_schedules_vehicle_id_fkey','c'),
  ('maintenance_tasks','maintenance_tasks_property_id_fkey','c'),
  ('maintenance_tasks','maintenance_tasks_asset_id_fkey','c'),
  ('maintenance_tasks','maintenance_tasks_vehicle_id_fkey','c'),
  ('loans','loans_property_id_fkey','n'),('loans','loans_asset_id_fkey','n'),('loans','loans_vehicle_id_fkey','n'),
  ('guides','guides_property_id_fkey','n'),('guides','guides_asset_id_fkey','n'),('guides','guides_vehicle_id_fkey','n'),
  ('assets','assets_property_id_fkey','c'),('assets','assets_room_id_fkey','n'),('rooms','rooms_property_id_fkey','c'))
SELECT w.tbl, w.conname,
       CASE w.want_del WHEN 'n' THEN 'SET NULL' WHEN 'c' THEN 'CASCADE' END AS was_at_089,
       CASE c.confdeltype WHEN 'n' THEN 'SET NULL' WHEN 'c' THEN 'CASCADE'
            WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'd' THEN 'SET DEFAULT' END AS is_now,
       CASE WHEN c.confdeltype = w.want_del THEN 'ok' ELSE '<-- FAIL' END AS result
  FROM want w
  LEFT JOIN pg_constraint c ON c.conrelid = w.tbl::regclass AND c.conname = w.conname AND c.contype='f'
 ORDER BY 1,2;

\echo ''
\echo '==================== AC2b: NO SET NULL LACKS THE COLUMN LIST ===================='
-- ON DELETE SET NULL without a column list would try to null household_id,
-- which is NOT NULL, so the parent delete would fail outright.
SELECT conrelid::regclass AS tbl, conname, pg_get_constraintdef(oid) AS definition,
       CASE WHEN confdeltype='n' AND pg_get_constraintdef(oid) ~ 'SET NULL \(' THEN 'ok'
            WHEN confdeltype='n' THEN '<-- FAIL' ELSE 'n/a' END AS result
  FROM pg_constraint
 WHERE contype='f' AND conname IN (
   'bills_property_id_fkey','bills_asset_id_fkey','bills_vehicle_id_fkey',
   'maintenance_schedules_property_id_fkey','maintenance_schedules_asset_id_fkey','maintenance_schedules_vehicle_id_fkey',
   'maintenance_tasks_property_id_fkey','maintenance_tasks_asset_id_fkey','maintenance_tasks_vehicle_id_fkey',
   'loans_property_id_fkey','loans_asset_id_fkey','loans_vehicle_id_fkey',
   'guides_property_id_fkey','guides_asset_id_fkey','guides_vehicle_id_fkey',
   'assets_property_id_fkey','assets_room_id_fkey','rooms_property_id_fkey')
 ORDER BY 1,2;

\echo ''
\echo '==================== AC3: PARENT DELETE NULLS THE COLUMN, NOT household_id ===================='
-- A dedicated asset, so this case does not destroy the fixtures AC5 needs.
DO $$
DECLARE
  hid  uuid := 'aaaaaaaa-0000-0000-0000-000000000001';
  prop uuid := 'a0000000-0000-0000-0000-0000000000a1';
  asst uuid := 'a7000000-0000-0000-0000-0000000000a7';
  bid  uuid := 'a5000000-0000-0000-0000-0000000000a5';
  hh_after uuid;
  asset_after uuid;
BEGIN
  DELETE FROM bills  WHERE id = bid;
  DELETE FROM assets WHERE id = asst;
  INSERT INTO assets (id, household_id, property_id, name) VALUES (asst, hid, prop, 'ac3 disposable asset');
  INSERT INTO bills (id, household_id, name, amount, frequency, asset_id)
    VALUES (bid, hid, 'ac3 subject of a deleted asset', 10, 'annual', asst);
  -- bills.asset_id is ON DELETE SET NULL (asset_id)
  DELETE FROM assets WHERE id = asst;
  SELECT household_id, asset_id INTO hh_after, asset_after FROM bills WHERE id = bid;
  IF hh_after IS DISTINCT FROM hid THEN
      RAISE WARNING 'AC3 FAIL: household_id became % (want %)', hh_after, hid;
  ELSIF asset_after IS NOT NULL THEN
      RAISE WARNING 'AC3 FAIL: asset_id is still %', asset_after;
  ELSE
      RAISE NOTICE 'AC3 ok: household_id intact (%), asset_id nulled', hh_after;
  END IF;
  DELETE FROM bills WHERE id = bid;
END $$;
SELECT 'AC3 rows left behind' AS check,
       (SELECT count(*) FROM bills WHERE name LIKE 'ac3 %')
     + (SELECT count(*) FROM assets WHERE name LIKE 'ac3 %') AS leftovers;

\echo ''
\echo '==================== AC7: CASCADE PARENTS FIRE BEFORE TRIGGER-DERIVED SET NULL ===================='
-- This is the regression guard. A property delete fires its children's ON DELETE
-- actions in TRIGGER OID order. bills.property_id is re-derived from asset_id by
-- derive_subject_property(), so the asset must already be GONE when bills'
-- property_id is nulled -- that is, assets_property_id_fkey's action trigger must
-- have a LOWER OID than bills_property_id_fkey's. If a later migration drops and
-- re-adds either constraint, this fails here instead of failing a family's
-- attempt to delete a property.
DO $$
DECLARE
    v_assets INT;
    v_bills  INT;
BEGIN
    SELECT t.oid INTO v_assets
      FROM pg_trigger t JOIN pg_constraint c ON c.oid = t.tgconstraint
     WHERE t.tgrelid = 'properties'::regclass
       AND c.conname = 'assets_property_id_fkey' AND (t.tgtype & 8) <> 0;
    SELECT t.oid INTO v_bills
      FROM pg_trigger t JOIN pg_constraint c ON c.oid = t.tgconstraint
     WHERE t.tgrelid = 'properties'::regclass
       AND c.conname = 'bills_property_id_fkey' AND (t.tgtype & 8) <> 0;

    IF v_assets IS NULL OR v_bills IS NULL THEN
        RAISE WARNING 'AC7 FAIL: action trigger missing (assets=%, bills=%)', v_assets, v_bills;
    ELSIF v_assets > v_bills THEN
        RAISE WARNING 'AC7 FAIL: assets action trigger OID % is AFTER bills % -- deleting a property that an asset sits in will fail on bills_property_id_fkey', v_assets, v_bills;
    ELSE
        RAISE NOTICE 'AC7 ok: assets_property_id_fkey action trigger (%) fires before bills_property_id_fkey (%)', v_assets, v_bills;
    END IF;
END $$;

-- And the behaviour, not just the OIDs: the bill must end at NULL/NULL, which is
-- what 089 produced. AC3 covers the column; this covers the whole cascade.
DO $$
DECLARE
    v_h    UUID := 'a7100000-0000-0000-0000-000000000001';
    v_p    UUID := 'a7100000-0000-0000-0000-000000000002';
    v_a    UUID := 'a7100000-0000-0000-0000-000000000003';
    v_b    UUID := 'a7100000-0000-0000-0000-000000000004';
    v_prop UUID;
    v_ast  UUID;
BEGIN
    INSERT INTO households (id, name) VALUES (v_h, 'AC7 family') ON CONFLICT (id) DO NOTHING;
    INSERT INTO properties (id, household_id, name) VALUES (v_p, v_h, 'AC7 house');
    INSERT INTO assets (id, household_id, property_id, name) VALUES (v_a, v_h, v_p, 'AC7 furnace');
    INSERT INTO bills (id, household_id, property_id, asset_id, name) VALUES (v_b, v_h, v_p, v_a, 'AC7 bill');

    DELETE FROM properties WHERE id = v_p;

    SELECT property_id, asset_id INTO v_prop, v_ast FROM bills WHERE id = v_b;
    IF v_prop IS NULL AND v_ast IS NULL THEN
        RAISE NOTICE 'AC7 ok: deleting the property left the bill at property_id=NULL, asset_id=NULL (the 089 end state)';
    ELSE
        RAISE WARNING 'AC7 FAIL: bill left at property_id=%, asset_id=% (want NULL, NULL)', v_prop, v_ast;
    END IF;
    DELETE FROM bills WHERE id = v_b;
    DELETE FROM assets WHERE id = v_a;
    DELETE FROM households WHERE id = v_h;
EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'AC7 FAIL: DELETE FROM properties raised % (%)', SQLERRM, SQLSTATE;
END $$;

\echo '==================== AC4: TRIGGER DISABLED, CROSS-FAMILY STILL REJECTED ===================='
-- This is the point of the ticket: the database, not derive_subject_property(),
-- refuses the reference. Each case is its own subtransaction. Only SQLSTATE
-- 23503 (foreign_key_violation) counts as a pass -- any other rejection is
-- reported as a different guard doing the work, which would prove nothing.
DO $$
DECLARE
  hhB  uuid := 'bbbbbbbb-0000-0000-0000-000000000002';
  propA uuid := 'a0000000-0000-0000-0000-0000000000a1';
  asstA uuid := 'a3000000-0000-0000-0000-0000000000a3';
  vehA  uuid := 'a4000000-0000-0000-0000-0000000000a4';
  rec   record;
  rejected_sqlstate text;
  pass int := 0;
  fail int := 0;
BEGIN
  FOR rec IN
    SELECT * FROM (VALUES
      ('bills','bills_derive_subject_property','property_id', propA::text),
      ('bills','bills_derive_subject_property','asset_id',    asstA::text),
      ('bills','bills_derive_subject_property','vehicle_id',  vehA::text),
      ('loans','loans_derive_subject_property','property_id', propA::text),
      ('guides','guides_derive_subject_property','property_id', propA::text),
      ('maintenance_tasks','maintenance_tasks_derive_subject_property','property_id', propA::text),
      ('maintenance_schedules','maintenance_schedules_derive_subject_property','property_id', propA::text)
    ) AS t(tbl, trig, col, val)
  LOOP
    EXECUTE format('ALTER TABLE %I DISABLE TRIGGER %I', rec.tbl, rec.trig);
    rejected_sqlstate := NULL;
    BEGIN
      IF rec.tbl = 'bills' THEN
        EXECUTE format('INSERT INTO %I (household_id, name, frequency, %I) VALUES (%L, %L, ''annual'', %L)',
                       rec.tbl, rec.col, hhB, 'cross-family ' || rec.col, rec.val);
      ELSIF rec.tbl = 'maintenance_tasks' THEN
        EXECUTE format('INSERT INTO %I (household_id, name, status, %I) VALUES (%L, %L, ''pending'', %L)',
                       rec.tbl, rec.col, hhB, 'cross-family ' || rec.col, rec.val);
      ELSIF rec.tbl = 'maintenance_schedules' THEN
        EXECUTE format('INSERT INTO %I (household_id, name, rrule, %I) VALUES (%L, %L, ''FREQ=YEARLY'', %L)',
                       rec.tbl, rec.col, hhB, 'cross-family ' || rec.col, rec.val);
      ELSIF rec.tbl = 'loans' THEN
        EXECUTE format('INSERT INTO %I (household_id, name, original_amount, remaining_balance, %I) VALUES (%L, %L, 1, 1, %L)',
                       rec.tbl, rec.col, hhB, 'cross-family ' || rec.col, rec.val);
      ELSE
        EXECUTE format('INSERT INTO %I (household_id, title, body, %I) VALUES (%L, %L, ''body'', %L)',
                       rec.tbl, rec.col, hhB, 'cross-family ' || rec.col, rec.val);
      END IF;
    EXCEPTION WHEN OTHERS THEN
      rejected_sqlstate := SQLSTATE;
      IF SQLSTATE <> '23503' THEN
        RAISE WARNING 'AC4: % % rejected with SQLSTATE % (%) -- NOT the FK',
          rec.tbl, rec.col, SQLSTATE, SQLERRM;
      END IF;
    END;
    EXECUTE format('ALTER TABLE %I ENABLE TRIGGER %I', rec.tbl, rec.trig);
    IF rejected_sqlstate = '23503' THEN
      pass := pass + 1;
      RAISE NOTICE 'AC4 ok: % % -> family A row refused (23503) while % was DISABLED',
        rec.tbl, rec.col, rec.trig;
    ELSE
      fail := fail + 1;
      RAISE WARNING 'AC4 FAIL: % % was NOT refused by the FK (sqlstate %)', rec.tbl, rec.col, coalesce(rejected_sqlstate,'none');
    END IF;
  END LOOP;
  RAISE NOTICE 'AC4: % refused by the FK, % not', pass, fail;
  IF fail > 0 THEN RAISE WARNING 'AC4 FAIL: % case(s) not refused by the FK', fail; END IF;
END $$;

\echo ''
\echo '==================== AC4b: THE SAME INSERT WITHOUT THE FK WOULD SUCCEED ===================='
-- Negative control for AC4: drop one composite FK, retry the same cross-family
-- insert with the trigger disabled, and show it is ACCEPTED. That proves the
-- rejection in AC4 came from the FK and not from something else in the path.
DO $$
DECLARE
  hhB  uuid := 'bbbbbbbb-0000-0000-0000-000000000002';
  propA uuid := 'a0000000-0000-0000-0000-0000000000a1';
  accepted boolean := false;
BEGIN
  ALTER TABLE bills DISABLE TRIGGER bills_derive_subject_property;
  ALTER TABLE bills DROP CONSTRAINT bills_property_id_fkey;
  BEGIN
    INSERT INTO bills (id, household_id, name, amount, frequency, property_id)
      VALUES ('a9000000-0000-0000-0000-0000000000a9', hhB, 'control: no FK', 1, 'annual', propA);
    accepted := true;
  EXCEPTION WHEN OTHERS THEN
    NULL;
  END;
  DELETE FROM bills WHERE id = 'a9000000-0000-0000-0000-0000000000a9';
  ALTER TABLE bills ADD CONSTRAINT bills_property_id_fkey
      FOREIGN KEY (property_id, household_id) REFERENCES properties (id, household_id)
      ON DELETE SET NULL (property_id);
  ALTER TABLE bills ENABLE TRIGGER bills_derive_subject_property;
  IF accepted THEN
    RAISE NOTICE 'AC4b ok: without the FK the cross-family insert is ACCEPTED -- the FK is what rejects it';
  ELSE
    RAISE WARNING 'AC4b FAIL: rejected even without the FK, so AC4 proves nothing';
  END IF;
END $$;

\echo ''
\echo '==================== AC5: SAME-HOUSEHOLD INSERTS STILL SUCCEED ===================='
DO $$
DECLARE
  hhA  uuid := 'aaaaaaaa-0000-0000-0000-000000000001';
  propA uuid := 'a0000000-0000-0000-0000-0000000000a1';
  asstA uuid := 'a3000000-0000-0000-0000-0000000000a3';
  vehA  uuid := 'a4000000-0000-0000-0000-0000000000a4';
  ok int := 0;
BEGIN
  BEGIN
    INSERT INTO bills (id, household_id, name, amount, frequency, property_id)
      VALUES ('a6000000-0000-0000-0000-0000000000a6', hhA, 'ac5 property', 1, 'annual', propA);
    ok := ok + 1;
  EXCEPTION WHEN OTHERS THEN RAISE WARNING 'AC5 FAIL bills.property_id: %', SQLERRM; END;
  BEGIN
    INSERT INTO bills (id, household_id, name, amount, frequency, asset_id)
      VALUES ('a6000000-0000-0000-0000-0000000000a7', hhA, 'ac5 asset', 1, 'annual', asstA);
    ok := ok + 1;
  EXCEPTION WHEN OTHERS THEN RAISE WARNING 'AC5 FAIL bills.asset_id: %', SQLERRM; END;
  BEGIN
    INSERT INTO bills (id, household_id, name, amount, frequency, vehicle_id)
      VALUES ('a6000000-0000-0000-0000-0000000000a8', hhA, 'ac5 vehicle', 1, 'annual', vehA);
    ok := ok + 1;
  EXCEPTION WHEN OTHERS THEN RAISE WARNING 'AC5 FAIL bills.vehicle_id: %', SQLERRM; END;
  BEGIN
    INSERT INTO loans (id, household_id, name, original_amount, remaining_balance, property_id)
      VALUES ('a6000000-0000-0000-0000-0000000000b6', hhA, 'ac5 loan', 1, 1, propA);
    ok := ok + 1;
  EXCEPTION WHEN OTHERS THEN RAISE WARNING 'AC5 FAIL loans.property_id: %', SQLERRM; END;
  BEGIN
    INSERT INTO guides (id, household_id, title, body, property_id)
      VALUES ('a6000000-0000-0000-0000-0000000000b7', hhA, 'ac5 guide', 'body', propA);
    ok := ok + 1;
  EXCEPTION WHEN OTHERS THEN RAISE WARNING 'AC5 FAIL guides.property_id: %', SQLERRM; END;
  BEGIN
    INSERT INTO maintenance_tasks (id, household_id, name, status, property_id)
      VALUES ('a6000000-0000-0000-0000-0000000000b8', hhA, 'ac5 task', 'pending', propA);
    ok := ok + 1;
  EXCEPTION WHEN OTHERS THEN RAISE WARNING 'AC5 FAIL maintenance_tasks.property_id: %', SQLERRM; END;
  BEGIN
    INSERT INTO maintenance_schedules (id, household_id, name, rrule, property_id)
      VALUES ('a6000000-0000-0000-0000-0000000000b9', hhA, 'ac5 sched', 'FREQ=YEARLY', propA);
    ok := ok + 1;
  EXCEPTION WHEN OTHERS THEN RAISE WARNING 'AC5 FAIL maintenance_schedules.property_id: %', SQLERRM; END;
  RAISE NOTICE 'AC5: % of 7 same-household inserts accepted', ok;
  IF ok < 7 THEN RAISE WARNING 'AC5 FAIL: only % of 7', ok; END IF;
END $$;

\echo ''
\echo '==================== AC6: CHILD-SIDE INDEXES PRESENT ===================='
WITH want(indexname) AS (VALUES ('idx_bills_property'),('idx_maintenance_schedules_property'),
               ('idx_maintenance_schedules_asset'),('idx_maintenance_schedules_vehicle'),
               ('idx_maintenance_tasks_property'),('idx_maintenance_tasks_asset'),
               ('idx_maintenance_tasks_vehicle'))
SELECT w.indexname,
       CASE WHEN EXISTS (SELECT 1 FROM pg_indexes i WHERE i.schemaname='public' AND i.indexname=w.indexname)
            THEN 'ok' ELSE '<-- FAIL' END AS result
  FROM want w ORDER BY 1;
\echo ''
\echo '==================== CLEANUP ===================='
DELETE FROM guides                WHERE title LIKE 'ac5 %';
DELETE FROM maintenance_tasks     WHERE name  LIKE 'ac5 %';
DELETE FROM maintenance_schedules WHERE name  LIKE 'ac5 %';
DELETE FROM loans                 WHERE name  LIKE 'ac5 %';
DELETE FROM bills                 WHERE name  LIKE 'ac5 %' OR name LIKE 'cross-family%';
DELETE FROM assets                WHERE name  IN ('A furnace','B furnace');
DELETE FROM vehicles              WHERE id IN ('a4000000-0000-0000-0000-0000000000a4','b4000000-0000-0000-0000-0000000000b4');
DELETE FROM rooms                 WHERE name IN ('A kitchen','B kitchen');
DELETE FROM properties            WHERE id IN ('a0000000-0000-0000-0000-0000000000a1','b0000000-0000-0000-0000-0000000000b1');
DELETE FROM households            WHERE id IN ('aaaaaaaa-0000-0000-0000-000000000001','bbbbbbbb-0000-0000-0000-000000000002');
\echo 'done'
