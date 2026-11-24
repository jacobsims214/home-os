#!/bin/bash
# Existing tests (apps/mcp/internal/testutil/*_test.go) go "down to the head" by
# hardcoding a newest-first list of downs that was written when 074 was the head.
# They do NOT include 075 — 075 is applied by the template and stays applied
# underneath. This replays that exact sequence with 075 in place, then re-applies
# the same ups, to prove 075 does not block them.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIG="$HERE/../migrations"
# Any empty PostgreSQL 15+ cluster works, e.g.
#   initdb -D /tmp/pg && pg_ctl -D /tmp/pg -o "-p 55497 -k /tmp" start
PSQL="${PSQL:-psql} -h ${PGHOST:-127.0.0.1} -p ${PGPORT:-55497}"
fail() { echo "FAIL: $*"; exit 1; }

# The exact newest-first list vendor_trades_test.go uses to get down to 041.
SUBSET_DOWN=(
  074_legacy_bill_events_reconcile 073_legacy_bill_events 071_person_color
  070_entity_kind_notifications 069_drop_finance_columns 068_guide_revision_change_note
  067_invitation_vendor_link 066_list_item_checked_by 065_vendor_address
  064_membership_vendor_link 061_notify_creator_role 060_drop_pet_vet_columns
  058_same_family_fks 057_secret_agent_share 056_domain_v2_cleanup
  055_activity_log 054_notify_creator 052_bills_paid_for_due_date 051_files_person
  050_authorship 049_guides 048_files_person 047_meals 046_lists 044_budgeting
  043_vendor_trades
)

$PSQL -U postgres -d postgres -q -c "DROP DATABASE IF EXISTS subset;" >/dev/null
$PSQL -U postgres -d postgres -q -c "CREATE DATABASE subset;" >/dev/null
$PSQL -U postgres -d subset -q -c "CREATE ROLE app;" >/dev/null 2>&1

echo "=== up to 075 (what the test template does today) ==="
for f in $(ls "$MIG"/*.up.sql | sort); do
  $PSQL -U postgres -d subset -v ON_ERROR_STOP=1 -q -f - < "$f" >/dev/null 2>&1 || fail "up $(basename $f)"
done

echo "=== seed a bill, a vendor and an account (a real family's data) ==="
$PSQL -U postgres -d subset -q -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO households (id,name,timezone) VALUES ('11111111-1111-1111-1111-111111111111','S','America/New_York');
INSERT INTO bills (household_id,name,amount,is_autopay,payee_name) VALUES ('11111111-1111-1111-1111-111111111111','Copay',35.00,false,'Dr. Patel office');
INSERT INTO payment_accounts (household_id,name,last_four) VALUES ('11111111-1111-1111-1111-111111111111','Joint checking','4821');
SQL

echo "=== DOWN: the hardcoded subset (075 stays applied underneath) ==="
for m in "${SUBSET_DOWN[@]}"; do
  f="$MIG/$m.down.sql"
  [ -f "$f" ] || { echo "    (no $m, skipped)"; continue; }
  out=$($PSQL -U postgres -d subset -v ON_ERROR_STOP=1 -q -f - < "$f" 2>&1) || {
    echo "--- $m.down FAILED ---"; echo "$out" | grep -E "ERROR|DETAIL" | head -4; fail "subset down $m"; }
done
echo "    subset downs OK with 075 in place"

echo "=== UP: the same migrations again, back to the head ==="
for (( i=${#SUBSET_DOWN[@]}-1; i>=0; i-- )); do
  m="${SUBSET_DOWN[$i]}"
  f="$MIG/$m.up.sql"
  [ -f "$f" ] || continue
  out=$($PSQL -U postgres -d subset -v ON_ERROR_STOP=1 -q -f - < "$f" 2>&1) || {
    echo "--- $m.up FAILED ---"; echo "$out" | grep -E "ERROR|DETAIL" | head -4; fail "subset up $m"; }
done
echo "    subset ups OK"

echo "=== 075 still intact after the round trip ==="
$PSQL -U postgres -d subset -tAc "SELECT 'autopay NOT NULL='||(is_nullable='NO')::text FROM information_schema.columns WHERE table_name='bills' AND column_name='is_autopay';"
$PSQL -U postgres -d subset -tAc "SELECT 'account rows='||count(*) FROM payment_accounts;"
$PSQL -U postgres -d subset -tAc "SELECT 'bill payee='||COALESCE(payee_name,'NULL') FROM bills;"
$PSQL -U postgres -d subset -tAc "SELECT 'portal FK='||count(*) FROM pg_constraint WHERE conname='vendors_portal_secret_fkey';"

echo
echo "=== SUBSET TEST PASSED ==="
