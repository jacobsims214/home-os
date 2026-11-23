-- verify_094.sql (HOME-OS-5626) -- proves an explicit property_id clear is no
-- longer overwritten by derive_subject_property(), on all five tables that
-- carry the subject pattern: bills, loans, maintenance_schedules,
-- maintenance_tasks and guides.
--
-- Run against a database at 094, from THIS directory (fixtures first):
--     psql -d <db> -f fixtures.sql
--     psql -d <db> -f verify_094.sql
--
-- REVERT-CHECK. This script asserts 094's rule in every run; the only thing
-- that changes between the two runs is the schema:
--
--   at 094 (094 applied)             -> every case prints ok, no '<-- FAIL'
--   at 091 (094's down applied)      -> AC1 and AC1b print '<-- FAIL': the
--                                       clear is re-derived, the pre-094 bug.
--
-- Every case runs inside its own savepoint (a DO block's EXCEPTION handler, or
-- BEGIN/ROLLBACK), so an expected rejection never aborts the run.
--
-- The cases are the ticket's acceptance criteria, numbered:
--   AC1  an explicit property_id := NULL on a row with asset_id set PERSISTS
--        (the ticket's own requirement; FAILS at 091)
--   AC1b the same clear on each of the five subject tables
--   AC2  repeating the clear is idempotent: a retry does not resurrect it
--   AC3  an ASSET CHANGE still re-derives, even with a carried NULL -- that is
--        the shape the API/MCP write path sends (all subject columns every time)
--   AC3b a CARRIED stale value is still corrected (087's rule)
--   AC4  an INSERT of an asset-subject row with no property still derives
--   AC5  a contradicting property presented against an asset is still refused
--        with SQLSTATE 23514 (087's rule is intact)
--   AC7  the property delete cascade still ends at asset_id=NULL,
--        property_id=NULL
--   AC8  a vehicle alongside a subject is still refused

\set QUIET on
\pset footer off

-- One helper so each case is one line and the pattern cannot drift between
-- tables: create a probe row with an asset, clear its property, report.
CREATE OR REPLACE FUNCTION pg_temp.probe_clear(p_tbl TEXT, p_household UUID, p_asset UUID)
RETURNS UUID LANGUAGE plpgsql AS $$
DECLARE v_id UUID; v_prop UUID; v_derived UUID; v_namecol TEXT; v_name TEXT;
BEGIN
    v_namecol := CASE p_tbl WHEN 'guides' THEN 'title' ELSE 'name' END;
    v_name    := CASE p_tbl WHEN 'guides' THEN '094 probe guide' ELSE '094 probe ' || p_tbl END;
    EXECUTE format('DELETE FROM %I WHERE household_id = %L AND %I LIKE %L',
                   p_tbl, p_household, v_namecol, '094 probe%');

    IF p_tbl = 'loans' THEN
        EXECUTE format('INSERT INTO %I (household_id,name,original_amount,remaining_balance,asset_id)
                        VALUES (%L,%L,100,100,%L) RETURNING id', p_tbl, p_household,v_name,p_asset) INTO v_id;
    ELSIF p_tbl = 'maintenance_schedules' THEN
        EXECUTE format('INSERT INTO %I (household_id,name,rrule,asset_id)
                        VALUES (%L,%L,%L,%L) RETURNING id', p_tbl, p_household,v_name,'FREQ=MONTHLY',p_asset) INTO v_id;
    ELSIF p_tbl = 'guides' THEN
        EXECUTE format('INSERT INTO %I (household_id,title,body,tags,asset_id)
                        VALUES (%L,%L,%L,%L,%L) RETURNING id', p_tbl, p_household,v_name,'body','{}',p_asset) INTO v_id;
    ELSE
        EXECUTE format('INSERT INTO %I (household_id,name,asset_id)
                        VALUES (%L,%L,%L) RETURNING id', p_tbl, p_household,v_name,p_asset) INTO v_id;
    END IF;

    EXECUTE format('SELECT property_id FROM %I WHERE id = %L', p_tbl, v_id) INTO v_derived;

    EXECUTE format('UPDATE %I SET property_id = NULL WHERE id = %L', p_tbl, v_id);
    EXECUTE format('SELECT property_id FROM %I WHERE id = %L', p_tbl, v_id) INTO v_prop;

    IF v_prop IS NULL THEN
        RAISE NOTICE 'ok  % : insert derived %, explicit clear PERSISTED', rpad(p_tbl,22), v_derived;
    ELSE
        RAISE NOTICE '<-- FAIL % : insert derived %, the clear was OVERWRITTEN back to %',
            rpad(p_tbl,22), v_derived, v_prop;
    END IF;
    RETURN v_id;
END $$;

\echo '==================== AC1/AC1b: the clear persists, on all five tables ===================='
SELECT pg_temp.probe_clear('bills','11111111-1111-1111-1111-111111111111','33333333-3333-3333-3333-333333333333') IS NOT NULL \g /dev/null
SELECT pg_temp.probe_clear('loans','11111111-1111-1111-1111-111111111111','33333333-3333-3333-3333-333333333333') IS NOT NULL \g /dev/null
SELECT pg_temp.probe_clear('maintenance_schedules','11111111-1111-1111-1111-111111111111','33333333-3333-3333-3333-333333333333') IS NOT NULL \g /dev/null
SELECT pg_temp.probe_clear('maintenance_tasks','11111111-1111-1111-1111-111111111111','33333333-3333-3333-3333-333333333333') IS NOT NULL \g /dev/null
SELECT pg_temp.probe_clear('guides','11111111-1111-1111-1111-111111111111','33333333-3333-3333-3333-333333333333') IS NOT NULL \g /dev/null

\echo ''
\echo '==================== AC2: repeating the clear is idempotent ===================='
DO $$
DECLARE v_id UUID; v_prop UUID;
BEGIN
    EXECUTE 'DELETE FROM bills WHERE name LIKE ''094 probe%''';
    INSERT INTO bills (household_id,name,amount,asset_id)
    VALUES ('11111111-1111-1111-1111-111111111111','094 probe bill',10.00,'33333333-3333-3333-3333-333333333333')
    RETURNING id INTO v_id;
    UPDATE bills SET property_id = NULL WHERE id = v_id;
    UPDATE bills SET property_id = NULL WHERE id = v_id;   -- the retry
    SELECT property_id INTO v_prop FROM bills WHERE id = v_id;
    IF v_prop IS NULL THEN
        RAISE NOTICE 'ok  AC2 a retried clear is still NULL';
    ELSE
        RAISE NOTICE '<-- FAIL AC2 the retry resurrected %', v_prop;
    END IF;
END $$;

\echo ''
\echo '==================== AC3/AC3b: an asset change STILL re-derives ===================='
DO $$
DECLARE v_id UUID; v_prop UUID;
BEGIN
    EXECUTE 'DELETE FROM bills WHERE name LIKE ''094 probe%''';
    INSERT INTO bills (household_id,name,amount,asset_id)
    VALUES ('11111111-1111-1111-1111-111111111111','094 probe bill',10.00,'33333333-3333-3333-3333-333333333333')
    RETURNING id INTO v_id;

    -- the API/MCP shape: switch the subject, carry property_id (NULL here)
    UPDATE bills SET asset_id='77777777-7777-7777-7777-777777777777', property_id=NULL WHERE id=v_id;
    SELECT property_id INTO v_prop FROM bills WHERE id=v_id;
    IF v_prop = '44444444-4444-4444-4444-444444444444' THEN
        RAISE NOTICE 'ok  AC3 asset change with a carried NULL re-derived to the new asset''s property';
    ELSE
        RAISE NOTICE '<-- FAIL AC3 expected 44444444, got %', coalesce(v_prop::text,'NULL');
    END IF;

    -- a CARRIED stale value (unchanged by the statement) is still corrected
    UPDATE bills SET asset_id='33333333-3333-3333-3333-333333333333',
                     property_id='44444444-4444-4444-4444-444444444444' WHERE id=v_id;
    SELECT property_id INTO v_prop FROM bills WHERE id=v_id;
    IF v_prop = '22222222-2222-2222-2222-222222222222' THEN
        RAISE NOTICE 'ok  AC3b a carried stale value is still corrected';
    ELSE
        RAISE NOTICE '<-- FAIL AC3b expected 22222222, got %', coalesce(v_prop::text,'NULL');
    END IF;
END $$;

\echo ''
\echo '==================== AC4: INSERT still derives ===================='
DO $$
DECLARE v_prop UUID;
BEGIN
    EXECUTE 'DELETE FROM bills WHERE name LIKE ''094 probe%''';
    INSERT INTO bills (household_id,name,amount,asset_id)
    VALUES ('11111111-1111-1111-1111-111111111111','094 probe bill',10.00,'33333333-3333-3333-3333-333333333333');
    SELECT property_id INTO v_prop FROM bills WHERE name LIKE '094 probe%';
    IF v_prop = '22222222-2222-2222-2222-222222222222' THEN
        RAISE NOTICE 'ok  AC4 insert derived property_id from the asset';
    ELSE
        RAISE NOTICE '<-- FAIL AC4 got %', coalesce(v_prop::text,'NULL');
    END IF;
END $$;

\echo ''
\echo '==================== AC5: 087''s contradiction rule is intact ===================='
DO $$
DECLARE v_state TEXT;
BEGIN
    EXECUTE 'DELETE FROM bills WHERE name LIKE ''094 probe%''';
    INSERT INTO bills (household_id,name,amount,asset_id)
    VALUES ('11111111-1111-1111-1111-111111111111','094 probe bill',10.00,'33333333-3333-3333-3333-333333333333');
    BEGIN
        UPDATE bills SET property_id='44444444-4444-4444-4444-444444444444' WHERE name LIKE '094 probe%';
        RAISE NOTICE '<-- FAIL AC5 a contradicting property was ACCEPTED';
    EXCEPTION WHEN check_violation THEN
        GET STACKED DIAGNOSTICS v_state = RETURNED_SQLSTATE;
        RAISE NOTICE 'ok  AC5 a contradicting property is still refused with % (087''s rule intact)', v_state;
    END;
END $$;

\echo ''
\echo '==================== AC7: the property delete cascade is unharmed ===================='
DO $$
DECLARE v_asset UUID; v_prop UUID;
BEGIN
    EXECUTE 'DELETE FROM bills WHERE name LIKE ''094 probe%''';
    INSERT INTO bills (household_id,name,amount,asset_id)
    VALUES ('11111111-1111-1111-1111-111111111111','094 probe bill',10.00,'33333333-3333-3333-3333-333333333333');
    BEGIN
        DELETE FROM properties WHERE id='22222222-2222-2222-2222-222222222222';
        SELECT asset_id, property_id INTO v_asset, v_prop FROM bills WHERE name LIKE '094 probe%';
        IF v_asset IS NULL AND v_prop IS NULL THEN
            RAISE NOTICE 'ok  AC7 deleting the property left the bill at asset_id=NULL, property_id=NULL';
        ELSE
            RAISE NOTICE '<-- FAIL AC7 ended at asset_id=%, property_id=%',
                coalesce(v_asset::text,'NULL'), coalesce(v_prop::text,'NULL');
        END IF;
        RAISE EXCEPTION 'roll back the delete';
    EXCEPTION WHEN OTHERS THEN
        NULL;  -- undo the delete; the assertion above already reported
    END;
END $$;

\echo ''
\echo '==================== AC8: a vehicle alongside a subject is still refused ===================='
DO $$
BEGIN
    EXECUTE 'DELETE FROM bills WHERE name LIKE ''094 probe%''';
    INSERT INTO bills (household_id,name,amount,asset_id)
    VALUES ('11111111-1111-1111-1111-111111111111','094 probe bill',10.00,'33333333-3333-3333-3333-333333333333');
    BEGIN
        UPDATE bills SET vehicle_id='55555555-5555-5555-5555-555555555555' WHERE name LIKE '094 probe%';
        RAISE NOTICE '<-- FAIL AC8 a vehicle was accepted alongside an asset';
    EXCEPTION WHEN check_violation THEN
        RAISE NOTICE 'ok  AC8 a vehicle alongside an asset is still refused';
    END;
END $$;

\echo ''
\echo '==================== clearing probe rows ===================='
DELETE FROM bills WHERE name LIKE '094 probe%';
DELETE FROM loans WHERE name LIKE '094 probe%';
DELETE FROM maintenance_schedules WHERE name LIKE '094 probe%';
DELETE FROM maintenance_tasks WHERE name LIKE '094 probe%';
DELETE FROM guides WHERE title LIKE '094 probe%';
\echo 'done: a passing run has no <-- FAIL above'
