#!/bin/bash
# Reproduces TestHotPathIndexes035_FullChainReversible's walk now that 075 is the
# newest migration: migrationsFrom(t, 35, "down") globs the directory, so 075's
# down is applied FIRST (newest first) and 075's up LAST (oldest first).
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIG="$HERE/../migrations"
# Any empty PostgreSQL 15+ cluster works, e.g.
#   initdb -D /tmp/pg && pg_ctl -D /tmp/pg -o "-p 55497 -k /tmp" start
PSQL="${PSQL:-psql} -h ${PGHOST:-127.0.0.1} -p ${PGPORT:-55497}"
fail() { echo "FAIL: $*"; exit 1; }

$PSQL -U postgres -d postgres -q -c "DROP DATABASE IF EXISTS chain;" >/dev/null
$PSQL -U postgres -d postgres -q -c "CREATE DATABASE chain;" >/dev/null
$PSQL -U postgres -d chain -q -c "CREATE ROLE app;" >/dev/null 2>&1

echo "=== apply 001..075 up ==="
for f in $(ls "$MIG"/*.up.sql | sort); do
  out=$($PSQL -U postgres -d chain -v ON_ERROR_STOP=1 -q -f - < "$f" 2>&1) || { echo "--- $(basename $f) ---"; echo "$out"|tail -8; fail "up $(basename $f)"; }
done
echo "    head at 075"

echo "=== seed data the way seedV2Family does: bills with autopay values ==="
$PSQL -U postgres -d chain -q -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO households (id,name,timezone) VALUES ('11111111-1111-1111-1111-111111111111','Chain','America/New_York');
INSERT INTO bills (household_id,name,amount,is_autopay) VALUES
  ('11111111-1111-1111-1111-111111111111','Kept autopay',100.00,true),
  ('11111111-1111-1111-1111-111111111111','Kept manual',50.00,false);
INSERT INTO payment_accounts (household_id,name,last_four) VALUES ('11111111-1111-1111-1111-111111111111','Joint checking','4821');
SQL
echo "    seeded 2 bills + 1 account"

downs=$(ls "$MIG"/*.down.sql | sort -r)
echo "=== walk DOWN 075 -> 035 (newest first, as the test does) ==="
n=0
for f in $downs; do
  num=$(basename "$f" | cut -d_ -f1)
  [ "$num" -lt 35 ] && continue
  out=$($PSQL -U postgres -d chain -v ON_ERROR_STOP=1 -q -f - < "$f" 2>&1) || { echo "--- $(basename $f) ---"; echo "$out"|tail -10; fail "down $(basename $f)"; }
  n=$((n+1))
done
echo "    $n downs applied, at 034"

echo "=== walk UP 035 -> 075 (oldest first) ==="
ups=$(ls "$MIG"/*.up.sql | sort)
n=0
for f in $ups; do
  num=$(basename "$f" | cut -d_ -f1)
  [ "$num" -lt 35 ] && continue
  out=$($PSQL -U postgres -d chain -v ON_ERROR_STOP=1 -q -f - < "$f" 2>&1) || { echo "--- $(basename $f) ---"; echo "$out"|tail -10; fail "up $(basename $f)"; }
  n=$((n+1))
done
echo "    $n ups applied, back at 075"

echo "=== 075's objects are back ==="
$PSQL -U postgres -d chain -tAc "SELECT 'payment_accounts='||count(*) FROM information_schema.tables WHERE table_name='payment_accounts';"
$PSQL -U postgres -d chain -tAc "SELECT 'is_autopay nullable='||is_nullable FROM information_schema.columns WHERE table_name='bills' AND column_name='is_autopay';"
$PSQL -U postgres -d chain -tAc "SELECT 'secrets key='||count(*) FROM pg_constraint WHERE conname='secrets_id_household_id_key';"

echo "=== second cycle (the test does two) ==="
for f in $downs; do
  num=$(basename "$f" | cut -d_ -f1); [ "$num" -lt 35 ] && continue
  $PSQL -U postgres -d chain -v ON_ERROR_STOP=1 -q -f - < "$f" >/dev/null 2>&1 || fail "cycle2 down $(basename $f)"
done
for f in $ups; do
  num=$(basename "$f" | cut -d_ -f1); [ "$num" -lt 35 ] && continue
  $PSQL -U postgres -d chain -v ON_ERROR_STOP=1 -q -f - < "$f" >/dev/null 2>&1 || fail "cycle2 up $(basename $f)"
done
echo "    second up/down/up cycle OK"

echo "=== bills survived both cycles ==="
$PSQL -U postgres -d chain -tAc "SELECT name||' autopay='||is_autopay FROM bills ORDER BY name;"

echo
echo "=== CHAIN TEST PASSED ==="
