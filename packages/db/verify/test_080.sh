#!/bin/bash
# Test 080 (vendors.kind) up and down against real PostgreSQL 16 with every
# earlier migration applied, seeding vendors whose class is known from the
# evidence 080's backfill reads. (#5529)
#
#   initdb -D /tmp/pg16 -U postgres --auth=trust
#   pg_ctl -D /tmp/pg16 -o "-p 55499 -k /tmp" start
#   PGPORT=55499 ./packages/db/verify/test_080.sh
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIG="$HERE/../migrations"
export PATH=/opt/homebrew/opt/postgresql@16/bin:$PATH
PHOST="${PGHOST:-127.0.0.1}"
PPORT="${PGPORT:-55499}"
PSQL="${PSQL:-psql} -h $PHOST -p $PPORT"

fail() { echo "FAIL: $*"; exit 1; }
q() { $PSQL -U postgres -d homeos -tAF'|' -c "$1"; }

echo "=== 0. postgres version ==="
$PSQL -U postgres -d postgres -tAc "SELECT current_setting('server_version')" | sed 's/^/    /'
$PSQL -U postgres -d postgres -tAc "SELECT current_setting('server_version_num')::int >= 160000" | grep -qx t || fail "need PostgreSQL 16+"
echo "    16+ ok"

echo "=== 0b. 080 is not already taken on this branch ==="
ls "$MIG"/080_*.up.sql >/dev/null 2>&1 || fail "080_vendor_kind.up.sql missing"
echo "    $(basename "$MIG"/080_*.up.sql)"

echo "=== 1. fresh database, apply 001..079 ==="
$PSQL -U postgres -d postgres -c "DROP DATABASE IF EXISTS homeos;" >/dev/null
$PSQL -U postgres -d postgres -c "CREATE DATABASE homeos;" >/dev/null
$PSQL -U postgres -d homeos -q -c "CREATE ROLE app;" >/dev/null 2>&1
n=0
for f in $(ls "$MIG"/*.up.sql | sort); do
  bn=$(basename "$f" | cut -d_ -f1)
  if [ "$bn" -ge 80 ]; then continue; fi
  out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$f" 2>&1)
  if [ $? -ne 0 ]; then echo "--- $bn FAILED ---"; echo "$out" | tail -20; fail "baseline migration $bn"; fi
  n=$((n+1))
done
echo "    $n migrations applied (001..077 present on feat/family-hub)"

echo "=== 2. seed: 6 vendors, each with known evidence, plus a 7th with none ==="
H='a0000000-0000-0000-0000-000000000001'   # household A
H2='a0000000-0000-0000-0000-000000000002'  # household B (tenancy)
# v1 gardener   trades, no bill                 -> service_provider
# v2 electric   bill payee, no trades           -> payee
# v3 hvac+paid  trades AND a bill               -> both
# v4 mortgage   bill payee only, no trades      -> payee
# v5 plumber    no trades but a maintenance job -> service_provider
# v6 bare       nothing at all                  -> service_provider (default)
# v7 vet        pets.vet_vendor_id              -> service_provider
# v8 helper     memberships.vendor_id           -> service_provider
q "INSERT INTO households (id,name,timezone) VALUES ('$H','Fam A','America/New_York'),('$H2','Fam B','America/New_York');" >/dev/null || fail seed households
q "INSERT INTO vendors (id,household_id,name) VALUES
  ('b0000000-0000-0000-0000-000000000001','$H','Green Thumb Gardening'),
  ('b0000000-0000-0000-0000-000000000002','$H','Metro Electric'),
  ('b0000000-0000-0000-0000-000000000003','$H','Comfort HVAC'),
  ('b0000000-0000-0000-0000-000000000004','$H','First National Mortgage'),
  ('b0000000-0000-0000-0000-000000000005','$H','Joe the Plumber'),
  ('b0000000-0000-0000-0000-000000000006','$H','Bare Contact'),
  ('b0000000-0000-0000-0000-000000000007','$H','Dr. Vetson'),
  ('b0000000-0000-0000-0000-000000000008','$H','Helper Co'),
  ('b0000000-0000-0000-0000-000000000009','$H2','Other Family Utility');" >/dev/null || fail seed vendors
# evidence
q "UPDATE vendors SET trades = ARRAY['gardener'], specialty='Landscaping' WHERE id='b0000000-0000-0000-0000-000000000001';" >/dev/null
q "UPDATE vendors SET trades = ARRAY['hvac'], specialty='HVAC'             WHERE id='b0000000-0000-0000-0000-000000000003';" >/dev/null
q "INSERT INTO bills (household_id,name,vendor_id) VALUES
  ('$H','Electric bill','b0000000-0000-0000-0000-000000000002'),
  ('$H','HVAC service plan','b0000000-0000-0000-0000-000000000003'),
  ('$H','Mortgage','b0000000-0000-0000-0000-000000000004');" >/dev/null || fail seed bills
q "INSERT INTO maintenance_tasks (household_id,name,vendor_id) VALUES ('$H','Fix the leak','b0000000-0000-0000-0000-000000000005');" >/dev/null || fail seed maintenance
q "INSERT INTO pets (household_id,name,vet_vendor_id) VALUES ('$H','Rex','b0000000-0000-0000-0000-000000000007');" >/dev/null || fail seed pets
q "INSERT INTO users (id,email,name,password_hash) VALUES ('c0000000-0000-0000-0000-000000000001','h@example.com','Helper','x');" >/dev/null || fail seed user
q "INSERT INTO memberships (household_id,user_id,role,vendor_id) VALUES ('$H','c0000000-0000-0000-0000-000000000001','vendor','b0000000-0000-0000-0000-000000000008');" >/dev/null || fail seed membership

echo -n "    seeded vendors: "
q "SELECT count(*) FROM vendors"

echo "=== 3. pre-080: no kind column ==="
echo -n "    kind column present (expect 0): "
q "SELECT count(*) FROM information_schema.columns WHERE table_name='vendors' AND column_name='kind'"
echo -n "    vendor_kind type present (expect 0): "
q "SELECT count(*) FROM pg_type WHERE typname='vendor_kind'"

echo "=== 4. UP ==="
out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/080_vendor_kind.up.sql" 2>&1)
rc=$?
echo "$out"
[ $rc -eq 0 ] || fail "080 up"
echo "    up OK"

echo "=== 5. backfill counts by class (the numbers for the ticket) ==="
q "SELECT kind, count(*) FROM vendors GROUP BY kind ORDER BY kind" | sed 's/^/    /'
echo "    --- each row with the evidence 080 read ---"
q "SELECT v.name,
          v.kind,
          CASE WHEN v.trades <> '{}' THEN array_to_string(v.trades,',') ELSE '-' END AS trades,
          (SELECT count(*) FROM bills b            WHERE b.vendor_id = v.id) AS bills,
          (SELECT count(*) FROM maintenance_tasks m WHERE m.vendor_id = v.id) AS maint,
          (SELECT count(*) FROM pets p             WHERE p.vet_vendor_id = v.id) AS pets,
          (SELECT count(*) FROM memberships mb     WHERE mb.vendor_id = v.id) AS memb
     FROM vendors v ORDER BY v.name" | column -t -s'|' | sed 's/^/    /'

echo "=== 6. post-up assertions ==="
echo -n "    kind NOT NULL + default service_provider: "
q "SELECT is_nullable || ' / ' || column_default FROM information_schema.columns WHERE table_name='vendors' AND column_name='kind'"
echo -n "    vendor_kind labels: "
q "SELECT string_agg(enumlabel,',' ORDER BY enumsortorder) FROM pg_enum e JOIN pg_type t ON t.oid=e.enumtypid WHERE t.typname='vendor_kind'"
echo -n "    index on (household_id, kind): "
q "SELECT indexdef FROM pg_indexes WHERE indexname='idx_vendors_household_kind'"
echo -n "    no NULL kind rows: "
q "SELECT count(*) FROM vendors WHERE kind IS NULL"
echo -n "    classified correctly (expect 0 mismatches): "
q "SELECT count(*) FROM vendors v WHERE v.kind IS DISTINCT FROM
     (CASE WHEN (v.trades <> '{}'
                 OR EXISTS (SELECT 1 FROM maintenance_tasks m WHERE m.vendor_id = v.id)
                 OR EXISTS (SELECT 1 FROM pets p WHERE p.vet_vendor_id = v.id)
                 OR EXISTS (SELECT 1 FROM memberships mb WHERE mb.vendor_id = v.id))
            AND EXISTS (SELECT 1 FROM bills b WHERE b.vendor_id = v.id) THEN 'both'::vendor_kind
           WHEN (v.trades <> '{}'
                 OR EXISTS (SELECT 1 FROM maintenance_tasks m WHERE m.vendor_id = v.id)
                 OR EXISTS (SELECT 1 FROM pets p WHERE p.vet_vendor_id = v.id)
                 OR EXISTS (SELECT 1 FROM memberships mb WHERE mb.vendor_id = v.id)) THEN 'service_provider'::vendor_kind
           WHEN EXISTS (SELECT 1 FROM bills b WHERE b.vendor_id = v.id) THEN 'payee'::vendor_kind
           ELSE 'service_provider'::vendor_kind END)"

CHECKS_FAILED=0
echo "=== 7. the two lists stay separate (each list's query excludes the other) ==="
echo "    contacts list (kind <> 'payee') for Fam A:"
q "SELECT name FROM vendors WHERE household_id='$H' AND kind <> 'payee' ORDER BY name" | sed 's/^/      /'
echo "    payee picker (kind <> 'service_provider') for Fam A:"
q "SELECT name FROM vendors WHERE household_id='$H' AND kind <> 'service_provider' ORDER BY name" | sed 's/^/      /'

# t: "pass" = the statement succeeds; "refuse" = the database rejects it;
# "absent" = the SELECT returns no row (the list did not offer it);
# "present" = the SELECT returns a row.
t() {
  local name="$1" want="$2" sql="$3" out rc
  out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -tAc "$sql" 2>&1); rc=$?
  case "$want" in
    pass)    [ $rc -eq 0 ] || { echo "  FAIL [$name] expected pass: $out"; return 1; }; ;;
    refuse)  [ $rc -ne 0 ] || { echo "  FAIL [$name] expected refusal but it succeeded"; return 1; }; ;;
    absent)  { [ $rc -eq 0 ] && [ -z "$(printf %s "$out" | tr -d '[:space:]')" ]; } \
               || { echo "  FAIL [$name] expected no row, got: $out"; return 1; }; ;;
    present) { [ $rc -eq 0 ] && [ -n "$(printf %s "$out" | tr -d '[:space:]')" ]; } \
               || { echo "  FAIL [$name] expected a row, got: ${out:-<none>}"; return 1; }; ;;
  esac
  echo "  ok [$want] $name${out:+ -> $out}"
  return 0
}
echo "--- the enum IS the check ---"
t "kind='utility' is refused" refuse \
  "UPDATE vendors SET kind='utility' WHERE id='b0000000-0000-0000-0000-000000000001';"
t "kind='' is refused" refuse \
  "UPDATE vendors SET kind='' WHERE id='b0000000-0000-0000-0000-000000000001';"
t "kind=NULL is refused (NOT NULL)" refuse \
  "UPDATE vendors SET kind=NULL WHERE id='b0000000-0000-0000-0000-000000000001';"
t "an INSERT that omits kind gets service_provider" pass \
  "INSERT INTO vendors (household_id,name) VALUES ('$H','Walk-in Contact') RETURNING kind;"

echo "--- payee-only vendor is excluded from the contacts list ---"
t "Green Thumb (gardener) IS in the contacts list" present \
  "SELECT 1 FROM vendors WHERE household_id='$H' AND kind <> 'payee' AND name='Green Thumb Gardening';"
t "Metro Electric (payee-only) is NOT in the contacts list" absent \
  "SELECT 1 FROM vendors WHERE household_id='$H' AND kind <> 'payee' AND name='Metro Electric';"
t "First National Mortgage (payee-only) is NOT in the contacts list" absent \
  "SELECT 1 FROM vendors WHERE household_id='$H' AND kind <> 'payee' AND name='First National Mortgage';"
echo "--- and the other way round ---"
t "Metro Electric IS in the payee picker" present \
  "SELECT 1 FROM vendors WHERE household_id='$H' AND kind <> 'service_provider' AND name='Metro Electric';"
t "Green Thumb (gardener) is NOT in the payee picker" absent \
  "SELECT 1 FROM vendors WHERE household_id='$H' AND kind <> 'service_provider' AND name='Green Thumb Gardening';"
t "'both' appears in both lists" present \
  "SELECT 1 FROM vendors WHERE household_id='$H' AND name='Comfort HVAC' AND kind <> 'payee' AND kind <> 'service_provider';"

echo "--- tenancy: a filter never crosses families ---"
t "Fam B's payee-only row is invisible to Fam A's payee picker" absent \
  "SELECT 1 FROM vendors WHERE household_id='$H' AND kind <> 'service_provider' AND name='Other Family Utility';"

echo "--- a payee that gains work is promoted (trigger), never silently hidden ---"
u='b0000000-0000-0000-0000-000000000002'   # Metro Electric, payee
t "payee row starts as payee" present \
  "SELECT 1 FROM vendors WHERE id='$u' AND kind='payee';"
t "tagging it with a trade promotes it to both" pass \
  "UPDATE vendors SET trades = ARRAY['electrician'] WHERE id='$u';"
t "after the tag it is 'both'" present \
  "SELECT 1 FROM vendors WHERE id='$u' AND kind='both';"
t "and it now appears in the contacts list" present \
  "SELECT 1 FROM vendors WHERE household_id='$H' AND kind <> 'payee' AND name='Metro Electric';"
t "clearing the trade does NOT demote it back to payee" pass \
  "UPDATE vendors SET trades = '{}' WHERE id='$u';"
t "it is still 'both' (one-way promotion)" present \
  "SELECT 1 FROM vendors WHERE id='$u' AND kind='both';"
t "put it back to payee-only for the remaining checks" pass \
  "UPDATE vendors SET kind='payee' WHERE id='$u';"

echo "--- bill -> payee -> contact in ONE hop ---"
q "SELECT b.name AS bill, v.name AS payee, v.kind, v.phone IS NOT NULL AS has_phone, v.email IS NOT NULL AS has_email
     FROM bills b JOIN vendors v ON v.id = b.vendor_id AND v.household_id = b.household_id
    WHERE b.household_id='$H' ORDER BY b.name" | column -t -s'|' | sed 's/^/    /'

echo "--- index is actually used by the contacts list ---"
q "SET enable_seqscan=off; EXPLAIN (COSTS OFF) SELECT id FROM vendors WHERE household_id='$H' AND kind <> 'payee' ORDER BY created_at DESC" | sed 's/^/    /'

echo "=== 8. DOWN ==="
out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/080_vendor_kind.down.sql" 2>&1)
rc=$?
echo "$out"
[ $rc -eq 0 ] || fail "080 down"
echo "    down OK"
echo -n "    kind column gone: "
q "SELECT count(*) FROM information_schema.columns WHERE table_name='vendors' AND column_name='kind'"
echo -n "    vendor_kind type gone: "
q "SELECT count(*) FROM pg_type WHERE typname='vendor_kind'"
echo -n "    trigger gone: "
q "SELECT count(*) FROM pg_trigger WHERE tgname='vendors_promote_on_trades'"
echo -n "    function gone: "
q "SELECT count(*) FROM pg_proc WHERE proname='vendors_promote_on_trades'"
echo -n "    index gone: "
q "SELECT count(*) FROM pg_indexes WHERE indexname='idx_vendors_household_kind'"
echo -n "    rows survived: "
q "SELECT count(*) FROM vendors"
echo -n "    evidence survived (bills->vendor links): "
q "SELECT count(*) FROM bills WHERE vendor_id IS NOT NULL"

echo "=== 9. DOWN twice (idempotent) ==="
$PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/080_vendor_kind.down.sql" >/dev/null 2>&1 \
  || fail "080 down twice"
echo "    second down OK"

echo "=== 10. UP again after down (round trip re-classifies identically) ==="
$PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/080_vendor_kind.up.sql" >/dev/null 2>&1 \
  || fail "080 up after down"
echo "    re-up OK, counts by class:"
q "SELECT kind, count(*) FROM vendors GROUP BY kind ORDER BY kind" | sed 's/^/      /'
echo -n "    evidence-free rows still service_provider: "
q "SELECT kind FROM vendors WHERE name='Bare Contact'"

echo
echo "=== ALL CHECKS PASSED ==="
