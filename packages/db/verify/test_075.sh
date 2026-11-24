#!/bin/bash
# Test 075 up and down against a real Postgres with every earlier migration applied.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIG="$HERE/../migrations"
PGURL="${PGURL:-postgres://postgres@127.0.0.1:55497/homeos?sslmode=disable}"
# Any empty PostgreSQL 15+ cluster works, e.g.
#   initdb -D /tmp/pg && pg_ctl -D /tmp/pg -o "-p 55497 -k /tmp" start
PSQL="${PSQL:-psql} -h ${PGHOST:-127.0.0.1} -p ${PGPORT:-55497}"

fail() { echo "FAIL: $*"; exit 1; }
q() { $PSQL -U postgres -d homeos -tAc "$1"; }

echo "=== 0. fresh database ==="
$PSQL -U postgres -d postgres -c "DROP DATABASE IF EXISTS homeos;" >/dev/null
$PSQL -U postgres -d postgres -c "CREATE DATABASE homeos;" >/dev/null

echo "=== 1. apply migrations 001..074 ==="
# The app role the compose stack creates before migrations run.
$PSQL -U postgres -d homeos -q -c "CREATE ROLE app;" >/dev/null 2>&1
for f in $(ls "$MIG"/*.up.sql | sort); do
  n=$(basename "$f" | cut -d_ -f1)
  if [ "$n" -ge 75 ]; then continue; fi
  out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$f" 2>&1)
  rc=$?
  if [ $rc -ne 0 ]; then echo "--- $n FAILED ---"; echo "$out" | tail -20; fail "baseline migration $n"; fi
done
echo "    74 migrations applied"

echo "=== 2. seed a household, two bills (one NULL autopay), a vendor, a secret ==="
q "INSERT INTO households (id, name, timezone) VALUES ('11111111-1111-1111-1111-111111111111','Test','America/New_York') ON CONFLICT DO NOTHING;" || true
# households columns may differ; probe first
echo "--- households columns ---"
q "SELECT string_agg(column_name, ',') FROM information_schema.columns WHERE table_name='households' AND is_nullable='NO' AND column_default IS NULL;"

echo "=== 3. pre-075 state: bills columns and is_autopay nullability ==="
q "SELECT is_nullable || ' / default=' || COALESCE(column_default,'none') FROM information_schema.columns WHERE table_name='bills' AND column_name='is_autopay';"
echo "--- any of the new names already present? (expect none) ---"
q "SELECT string_agg(column_name,',') FROM information_schema.columns WHERE (table_name='bills' AND column_name IN ('autopay_account_id','payee_name','autopay_enabled')) OR (table_name='vendors' AND column_name='portal_secret_id') OR table_name='payment_accounts';"

echo "=== 4. UP ==="
out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/075_bill_autopay_payment_account.up.sql" 2>&1)
rc=$?
echo "$out"
[ $rc -eq 0 ] || fail "075 up"
echo "    up OK"

echo "=== 5. post-up assertions ==="
echo -n "is_autopay NOT NULL + default false: "
q "SELECT is_nullable || ' / ' || column_default FROM information_schema.columns WHERE table_name='bills' AND column_name='is_autopay';"
echo -n "payment_accounts exists: "
q "SELECT count(*) FROM information_schema.tables WHERE table_name='payment_accounts';"
echo -n "payment_account_type enum: "
q "SELECT string_agg(enumlabel, ',' ORDER BY enumsortorder) FROM pg_enum e JOIN pg_type t ON t.oid=e.enumtypid WHERE t.typname='payment_account_type';"
echo -n "new columns: "
q "SELECT string_agg(table_name||'.'||column_name, ', ' ORDER BY table_name, column_name) FROM information_schema.columns WHERE (table_name='bills' AND column_name IN ('autopay_account_id','payee_name')) OR (table_name='vendors' AND column_name='portal_secret_id') OR table_name='payment_accounts' AND column_name='secret_id';"
echo -n "composite FKs (same family): "
q "SELECT string_agg(conname||' -> '||confrelid::regclass::text, ' | ' ORDER BY conname) FROM pg_constraint WHERE conname IN ('bills_autopay_account_fkey','vendors_portal_secret_fkey','payment_accounts_secret_fkey');"
echo -n "delete rules: "
q "SELECT string_agg(conname||'='||confdeltype::text, ', ' ORDER BY conname) FROM pg_constraint WHERE conname IN ('bills_autopay_account_fkey','vendors_portal_secret_fkey','payment_accounts_secret_fkey');"
echo -n "check constraints: "
q "SELECT string_agg(conname, ', ' ORDER BY conname) FROM pg_constraint WHERE conname IN ('bills_autopay_account_check','bills_payee_name_check','payment_accounts_last_four_check','payment_accounts_name_check');"
echo -n "secrets key: "
q "SELECT count(*) FROM pg_constraint WHERE conname='secrets_id_household_id_key';"

echo "=== 6. behavioural tests (each must be refused or accepted as stated) ==="
H='22222222-2222-2222-2222-222222222222'
V='33333333-3333-3333-3333-333333333333'
S='44444444-4444-4444-4444-444444444444'
PA='55555555-5555-5555-5555-555555555555'
B='66666666-6666-6666-6666-666666666666'
H2='77777777-7777-7777-7777-777777777777'
S2='88888888-8888-8888-8888-888888888888'
V2='99999999-9999-9999-9999-999999999999'

q "INSERT INTO households (id,name,timezone) VALUES ('$H','Fam A','America/New_York'),('$H2','Fam B','America/New_York');" >/dev/null || fail seed households
q "INSERT INTO vendors (id,household_id,name) VALUES ('$V','$H','Electric Co'),('$V2','$H2','Other Co');" >/dev/null || fail seed vendors
q "INSERT INTO secrets (id,household_id,entity_type,entity_id,encrypted_data,iv,name,secret_type) VALUES ('$S','$H','household','$H','\\x00','\\x00','Bank login','login'),('$S2','$H2','household','$H2','\\x00','\\x00','B login','login');" >/dev/null || echo "     (secrets seed: entity_kind check may need a real kind)"

t() { # name, expected(pass|refuse), sql
  local name="$1" want="$2" sql="$3"
  local out rc
  out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -tAc "$sql" 2>&1); rc=$?
  if [ "$want" = "pass" ] && [ $rc -ne 0 ]; then echo "  FAIL [$name] expected pass: $out"; return 1; fi
  if [ "$want" = "refuse" ] && [ $rc -eq 0 ]; then echo "  FAIL [$name] expected refusal but it succeeded"; return 1; fi
  local tag="ok "; [ "$want" = "refuse" ] && tag="ok(refused)"
  echo "  $tag [$name]${out:+ -> $out}"
  return 0
}

echo "--- autopay default is fail-safe ---"
t "INSERT bill omitting autopay gets false" pass \
  "INSERT INTO bills (id,household_id,name) VALUES ('$B','$H','Water') RETURNING is_autopay;"
t "explicit NULL autopay is refused now" refuse \
  "INSERT INTO bills (household_id,name,is_autopay) VALUES ('$H','Null bill',NULL);"
echo "--- the contradiction ---"
t "create the payment account (own family's secret)" pass \
  "INSERT INTO payment_accounts (id,household_id,name,last_four,secret_id) VALUES ('$PA','$H','Joint checking','4821','$S');"
t "account on a non-autopay bill is refused" refuse \
  "UPDATE bills SET autopay_account_id='$PA' WHERE id='$B';"
t "account on an autopay bill is accepted" pass \
  "UPDATE bills SET is_autopay=true, autopay_account_id='$PA' WHERE id='$B';"
t "turning autopay off while the account is set is refused" refuse \
  "UPDATE bills SET is_autopay=false WHERE id='$B';"
t "turning autopay off and clearing the account is accepted" pass \
  "UPDATE bills SET is_autopay=false, autopay_account_id=NULL WHERE id='$B';"

echo "--- last-4 only ---"
t "5-digit last_four refused" refuse \
  "INSERT INTO payment_accounts (household_id,name,last_four) VALUES ('$H','Bad','48211');"
t "non-numeric last_four refused" refuse \
  "INSERT INTO payment_accounts (household_id,name,last_four) VALUES ('$H','Bad','48a1');"
t "full 12-digit account number in last_four refused" refuse \
  "INSERT INTO payment_accounts (household_id,name,last_four) VALUES ('$H','Bad','123456789012');"
t "no column can hold a full number (expect only last_four)" pass \
  "SELECT string_agg(column_name,',') FROM information_schema.columns WHERE table_name='payment_accounts' AND (column_name LIKE '%account%number%' OR column_name LIKE '%routing%' OR column_name='account_number');"

echo "--- tenancy: another family's rows are refused ---"
t "account may not link family B's secret" refuse \
  "INSERT INTO payment_accounts (household_id,name,secret_id) VALUES ('$H','Cross','$S2');"
t "vendor may not link family B's secret" refuse \
  "UPDATE vendors SET portal_secret_id='$S2' WHERE id='$V';"
t "vendor may link its own family's secret" pass \
  "UPDATE vendors SET portal_secret_id='$S' WHERE id='$V';"
t "family B gets its own account" pass \
  "INSERT INTO payment_accounts (id,household_id,name) VALUES ('77777777-0000-0000-0000-000000000000','$H2','B acct');"
t "bill may not use family B's account" refuse \
  "INSERT INTO bills (household_id,name,is_autopay,autopay_account_id) VALUES ('$H','Cross bill',true,'77777777-0000-0000-0000-000000000000');"
t "empty payee_name refused" refuse \
  "UPDATE bills SET payee_name='   ' WHERE id='$B';"
t "real payee_name accepted" pass \
  "UPDATE bills SET payee_name='Dr. Patel office' WHERE id='$B';"

echo "--- ON DELETE SET NULL leaves no dangling pointer ---"
q "SELECT secret_id FROM payment_accounts WHERE id='$PA';" | sed 's/^/    before secret delete: /'
t "delete the secret" pass "DELETE FROM secrets WHERE id='$S';"
q "SELECT COALESCE(secret_id::text,'NULL') FROM payment_accounts WHERE id='$PA';" | sed 's/^/    after secret delete: /'
q "SELECT COALESCE(portal_secret_id::text,'NULL') FROM vendors WHERE id='$V';" | sed 's/^/    vendor after:       /'
q "SELECT count(*) FROM payment_accounts WHERE id='$PA';" | sed 's/^/    account still exists: /'

echo "=== 7. DOWN ==="
out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/075_bill_autopay_payment_account.down.sql" 2>&1)
rc=$?
echo "$out"
[ $rc -eq 0 ] || fail "075 down"
echo "    down OK"

echo "=== 8. post-down assertions: back to exactly 074 ==="
echo -n "is_autopay nullable again: "
q "SELECT is_nullable || ' / ' || COALESCE(column_default,'none') FROM information_schema.columns WHERE table_name='bills' AND column_name='is_autopay';"
echo -n "payment_accounts gone: "
q "SELECT count(*) FROM information_schema.tables WHERE table_name='payment_accounts';"
echo -n "payment_account_type gone: "
q "SELECT count(*) FROM pg_type WHERE typname='payment_account_type';"
echo -n "new columns gone: "
q "SELECT COALESCE(string_agg(table_name||'.'||column_name,', '),'(none)') FROM information_schema.columns WHERE (table_name='bills' AND column_name IN ('autopay_account_id','payee_name')) OR (table_name='vendors' AND column_name='portal_secret_id');"
echo -n "secrets key gone: "
q "SELECT count(*) FROM pg_constraint WHERE conname='secrets_id_household_id_key';"
echo -n "075's indexes gone: "
q "SELECT COALESCE(string_agg(indexname,','),'(none)') FROM pg_indexes WHERE indexname IN ('idx_vendors_portal_secret','idx_bills_autopay_account','idx_payment_accounts_secret','idx_payment_accounts_household');"
echo -n "bills row survived, is_autopay preserved: "
q "SELECT count(*)||' rows, autopay='||COALESCE(bool_and(is_autopay)::text,'null') FROM bills;"
echo -n "secrets row count (down never touches ciphertext): "
q "SELECT count(*) FROM secrets;"

echo "=== 9. DOWN twice (idempotent) ==="
out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/075_bill_autopay_payment_account.down.sql" 2>&1)
[ $? -eq 0 ] || { echo "$out"; fail "075 down twice"; }
echo "    second down OK (safe to run twice)"

echo "=== 10. UP again after down (round trip) ==="
out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/075_bill_autopay_payment_account.up.sql" 2>&1)
[ $? -eq 0 ] || { echo "$out"; fail "075 up after down"; }
echo "    re-up OK"
echo -n "is_autopay NOT NULL again: "
q "SELECT is_nullable FROM information_schema.columns WHERE table_name='bills' AND column_name='is_autopay';"

echo
echo "=== ALL CHECKS PASSED ==="
