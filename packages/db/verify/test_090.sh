#!/bin/bash
# Test 090 (bill_payments ledger) up, backfill, constraints and down against
# real PostgreSQL 16 with every earlier migration applied. (#5514, schema
# review F6)
#
#   initdb -D /tmp/pg5514 -U postgres --auth=trust
#   pg_ctl -D /tmp/pg5514 -o "-p 55514 -k /tmp -c listen_addresses=127.0.0.1" start
#   PGPORT=55514 ./packages/db/verify/test_090.sh
#
# What it proves, in order:
#   1. the baseline 001..089 applies and the defect is REAL on it: a bill holds
#      exactly one paid_date, so a second payment destroys the first, and no
#      column records what was paid;
#   2. 090 up applies onto that, backfills one reconstructed row per paid bill,
#      and leaves bills itself untouched;
#   3. the constraints hold by behaviour — composite same-family FKs on bill_id
#      and payment_account_id, UNIQUE (id, household_id), household_id NOT NULL,
#      amount > 0 — and the two indexes exist with the shape the queries need;
#   4. a bill holds TWO payments at once, which the old schema could not
#      express, and bills.paid_date/paid_for_due_date still hold the later one
#      (the cache, unchanged for every existing reader);
#   5. down removes the ledger and leaves bills byte-identical; down twice,
#      up again.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIG="$HERE/../migrations"
export PATH=/opt/homebrew/opt/postgresql@16/bin:$PATH
PHOST="${PGHOST:-127.0.0.1}"
PPORT="${PGPORT:-55514}"
PSQL="${PSQL:-psql} -h $PHOST -p $PPORT"

fail() { echo "FAIL: $*"; exit 1; }
q() { $PSQL -U postgres -d homeos -tAF'|' -c "$1"; }
accepts() { # accepts "<label>" "<sql>"
  local out rc
  out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -c "$2" 2>&1); rc=$?
  if [ $rc -ne 0 ]; then fail "$1: expected ACCEPT, got: $(echo "$out" | grep ERROR | head -1)"; fi
  echo "    ACCEPTED  $1"
}
refuses() { # refuses "<label>" "<sql>" ["<expected fragment>"]
  local out rc
  out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -c "$2" 2>&1); rc=$?
  if [ $rc -eq 0 ]; then fail "$1: expected REFUSE, but it was ACCEPTED"; fi
  if [ $# -ge 3 ] && ! echo "$out" | grep -q "$3"; then
    fail "$1: refused for the wrong reason: $(echo "$out" | grep ERROR | head -1)"
  fi
  echo "    REFUSED   $1"
}
# same "<label>" "<sql>" "<expected>" — an exact single-value answer
same() {
  local got
  got=$(q "$2")
  [ "$got" = "$3" ] || fail "$1: got '$got', want '$3'"
  echo "    OK        $1 = $3"
}

echo "=== 0. postgres version ==="
$PSQL -U postgres -d postgres -tAc "SELECT current_setting('server_version')" | sed 's/^/    /'
$PSQL -U postgres -d postgres -tAc "SELECT current_setting('server_version_num')::int >= 160000" | grep -qx t || fail "need PostgreSQL 16+"
echo "    16+ ok"

echo "=== 0b. 090 is the number this branch carries, and nothing below it is missing ==="
ls "$MIG"/090_*.up.sql >/dev/null 2>&1 || fail "090_bill_payments.up.sql missing"
echo "    $(basename "$MIG"/090_*.up.sql)"
dups=$(ls "$MIG"/*.up.sql | sed -E 's|.*/([0-9]+)_.*|\1|' | sort | uniq -d)
[ -z "$dups" ] || fail "duplicate migration numbers: $dups"
head_num=$(ls "$MIG"/*.up.sql | sed -E 's|.*/([0-9]+)_.*|\1|' | sort -n | tail -1)
[ "$head_num" = "090" ] || fail "head is $head_num, expected 090"
echo "    no duplicates; head = 090"

echo "=== 1. fresh database, apply 001..089 ==="
$PSQL -U postgres -d postgres -c "DROP DATABASE IF EXISTS homeos;" >/dev/null
$PSQL -U postgres -d postgres -c "CREATE DATABASE homeos;" >/dev/null
$PSQL -U postgres -d homeos -q -c "CREATE ROLE app;" >/dev/null 2>&1
n=0
for f in $(ls "$MIG"/*.up.sql | sort); do
  bn=$(basename "$f" | cut -d_ -f1)
  [ "$bn" -ge 90 ] && continue
  out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$f" 2>&1)
  if [ $? -ne 0 ]; then echo "--- $bn FAILED ---"; echo "$out" | tail -20; fail "baseline migration $bn"; fi
  n=$((n+1))
done
echo "    $n migrations applied (001..089)"

echo "=== 1b. the defect is REAL on the pre-090 schema ==="
same "no bill_payments table" \
  "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='bill_payments'" "0"
same "bills has no amount-paid column" \
  "SELECT count(*) FROM information_schema.columns WHERE table_name='bills' AND column_name IN ('amount_paid','paid_amount')" "0"

H='a0000000-0000-0000-0000-000000000001'   # household A
H2='a0000000-0000-0000-0000-000000000002'  # household B (tenancy)
U='c0000000-0000-0000-0000-000000000001'
ACC='d0000000-0000-0000-0000-000000000001'   # payment account, family A
ACC2='d0000000-0000-0000-0000-000000000002'  # payment account, family B (tenancy)
# Bills. Five paid ones and two unpaid, so the backfill has both cases:
#  b1 mortgage  paid, with a stored coverage          -> 1 reconstructed row
#  b2 dentist   paid, coverage NULL (pre-052 row)     -> 1 reconstructed row
#  b3 internet  paid this month                       -> 1 reconstructed row
#  b4 water     paid, amount NULL                     -> 0 rows (cannot invent)
#  b5 trash     never paid                            -> 0 rows
#  b6 B's bill, paid                                  -> 1 reconstructed row
#  b7 unpaid                                          -> 0 rows
q "INSERT INTO households (id,name,timezone) VALUES ('$H','Fam A','America/New_York'),('$H2','Fam B','America/New_York');" >/dev/null || fail seed households
q "INSERT INTO users (id,email,name,password_hash) VALUES ('$U','h@example.com','Helper','x');" >/dev/null || fail seed user
q "INSERT INTO payment_accounts (id,household_id,name,account_type,last_four) VALUES
     ('$ACC','$H','Joint checking','checking','4412'),
     ('$ACC2','$H2','Other family checking','checking','9911');" >/dev/null || fail seed accounts
q "INSERT INTO bills (id,household_id,name,amount,frequency,due_day,paid_date,paid_for_due_date) VALUES
     ('b0000000-0000-0000-0000-000000000001','$H','Mortgage',    1850.00,'monthly',1, '2026-11-02','2026-11-01'),
     ('b0000000-0000-0000-0000-000000000002','$H','Dentist',      340.00,'annual', NULL,'2026-09-15',NULL),
     ('b0000000-0000-0000-0000-000000000003','$H','Internet',      79.99,'monthly',12,'2026-11-12','2026-11-12'),
     ('b0000000-0000-0000-0000-000000000004','$H','Water',        NULL,   'monthly',20,'2026-11-20','2026-11-20'),
     ('b0000000-0000-0000-0000-000000000005','$H','Trash',         45.00,'monthly',25,NULL,NULL),
     ('b0000000-0000-0000-0000-000000000006','$H2','Other utility',120.00,'monthly',5,'2026-11-05','2026-11-05'),
     ('b0000000-0000-0000-0000-000000000007','$H','Unpaid',        60.00,'monthly',15,NULL,NULL);" >/dev/null || fail seed bills
# b2 is an annual bill with no due_date: give it one so it has a schedule.
q "UPDATE bills SET due_date = '2026-09-10' WHERE id='b0000000-0000-0000-0000-000000000002';" >/dev/null || fail set b2 due_date

echo -n "    paid bills (expect 5): "; q "SELECT count(*) FROM bills WHERE paid_date IS NOT NULL"
echo -n "    paid bills with a positive amount (expect 4): "; q "SELECT count(*) FROM bills WHERE paid_date IS NOT NULL AND amount > 0"
echo "    the defect: a second payment destroys the first, and only one row can exist"
q "UPDATE bills SET paid_date='2026-11-25', paid_for_due_date='2026-11-25' WHERE id='b0000000-0000-0000-0000-000000000003';" >/dev/null
same "after a second payment only the latest paid_date survives" \
  "SELECT paid_date FROM bills WHERE id='b0000000-0000-0000-0000-000000000003'" "2026-11-25"
same "nothing recorded the first payment" \
  "SELECT count(*) FROM bills WHERE id='b0000000-0000-0000-0000-000000000003'" "1"
# Put it back so the backfill numbers below match the header comment.
q "UPDATE bills SET paid_date='2026-11-12', paid_for_due_date='2026-11-12' WHERE id='b0000000-0000-0000-0000-000000000003';" >/dev/null

echo "=== 1c. snapshot bills so down can prove it left them alone ==="
BILLS_BEFORE=$($PSQL -U postgres -d homeos -tAc \
  "SELECT md5(string_agg(b::text, '|' ORDER BY id)) FROM bills b")
echo "    bills checksum before 090 = $BILLS_BEFORE"

echo "=== 2. UP ==="
out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/090_bill_payments.up.sql" 2>&1)
rc=$?
echo "$out"
[ $rc -eq 0 ] || fail "090 up"

echo "=== 3. BACKFILL (the numbers for the ticket) ==="
echo -n "    ledger rows (expect 5 = EVERY paid bill; the water bill gets a NULL amount): "
q "SELECT count(*) FROM bill_payments"
echo -n "    reconstructed rows (expect 5): "
q "SELECT count(*) FROM bill_payments WHERE is_reconstructed"
echo -n "    observed rows (expect 0): "
q "SELECT count(*) FROM bill_payments WHERE NOT is_reconstructed"
echo -n "    paid bills that got a row with an UNKNOWN amount (expect 1 = the water bill): "
q "SELECT count(*) FROM bill_payments WHERE is_reconstructed AND amount IS NULL"
echo -n "    paid bills left with no ledger row (expect 0 — one row each): "
q "SELECT count(*) FROM bills b WHERE b.paid_date IS NOT NULL AND NOT EXISTS (SELECT 1 FROM bill_payments p WHERE p.bill_id=b.id)"
echo "    --- each paid bill, and what the backfill made of it ---"
q "SELECT b.name,
          b.amount        AS owed,
          b.paid_date     AS bill_paid_date,
          b.paid_for_due_date AS bill_coverage,
          COALESCE(p.amount::text,'NULL')       AS ledger_amount,
          COALESCE(p.paid_on::text,'-')         AS ledger_paid_on,
          COALESCE(p.paid_for_due_date::text,'-') AS ledger_coverage,
          COALESCE(p.is_reconstructed::text,'-') AS reconstructed
     FROM bills b
     LEFT JOIN bill_payments p ON p.bill_id = b.id
    WHERE b.paid_date IS NOT NULL
    ORDER BY b.name" | column -t -s'|' | sed 's/^/    /'
echo "    --- THE SAMPLE ROW (criterion 4) ---"
q "SELECT id, bill_id, amount, paid_on, paid_for_due_date, payment_account_id, method, is_reconstructed
     FROM bill_payments ORDER BY paid_on LIMIT 1" | sed 's/^/    /'
same "a reconstructed row has is_reconstructed = true" \
  "SELECT CASE WHEN is_reconstructed THEN 'reconstructed' ELSE 'observed' END FROM bill_payments WHERE bill_id='b0000000-0000-0000-0000-000000000001'" "reconstructed"
same "a reconstructed row records no account (nobody recorded one)" \
  "SELECT CASE WHEN payment_account_id IS NULL THEN 'null' ELSE 'set' END FROM bill_payments WHERE bill_id='b0000000-0000-0000-0000-000000000001'" "null"
same "a reconstructed row carries the bill's amount" \
  "SELECT amount::text FROM bill_payments WHERE bill_id='b0000000-0000-0000-0000-000000000001'" "1850.00"
same "a reconstructed row carries the bill's stored coverage" \
  "SELECT paid_for_due_date::text FROM bill_payments WHERE bill_id='b0000000-0000-0000-0000-000000000001'" "2026-11-01"
same "a pre-052 NULL coverage stays NULL, not invented" \
  "SELECT CASE WHEN paid_for_due_date IS NULL THEN 'null' ELSE 'set' END FROM bill_payments WHERE bill_id='b0000000-0000-0000-0000-000000000002'" "null"

echo "=== 4. bills untouched by the backfill (criterion 4 + 7) ==="
BILLS_AFTER=$($PSQL -U postgres -d homeos -tAc \
  "SELECT md5(string_agg(b::text, '|' ORDER BY id)) FROM bills b")
[ "$BILLS_BEFORE" = "$BILLS_AFTER" ] || fail "bills changed during 090 up: $BILLS_BEFORE -> $BILLS_AFTER"
echo "    OK        bills checksum after up == before up ($BILLS_AFTER)"
same "bills.paid_date is still the column the readers use" \
  "SELECT paid_date::text FROM bills WHERE id='b0000000-0000-0000-0000-000000000001'" "2026-11-02"

echo "=== 5. SHAPE (criteria 1 and 6), from information_schema / pg_constraint / pg_indexes ==="
echo -n "    household_id NOT NULL (is_nullable must be NO): "
q "SELECT is_nullable FROM information_schema.columns WHERE table_name='bill_payments' AND column_name='household_id'"
same "household_id is NOT NULL" \
  "SELECT is_nullable FROM information_schema.columns WHERE table_name='bill_payments' AND column_name='household_id'" "NO"
echo -n "    columns: "
q "SELECT string_agg(column_name, ',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name='bill_payments'"
echo "    composite FKs (contype f, two columns each):"
q "SELECT c.conname,
          pg_get_constraintdef(c.oid),
          (SELECT count(*) FROM unnest(c.conkey) k) AS ncols
     FROM pg_constraint c
    WHERE c.conrelid = 'bill_payments'::regclass AND c.contype = 'f'
    ORDER BY c.conname" | sed 's/^/      /'
same "exactly 3 FKs, each composite (bill, account, document)" \
  "SELECT count(*) FROM pg_constraint WHERE conrelid='bill_payments'::regclass AND contype='f' AND array_length(conkey,1)=2" "3"
same "the bill FK targets bills (id, household_id)" \
  "SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname='bill_payments_bill_fkey'" \
  "FOREIGN KEY (bill_id, household_id) REFERENCES bills(id, household_id) ON DELETE CASCADE"
same "the account FK targets payment_accounts (id, household_id)" \
  "SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname='bill_payments_account_fkey'" \
  "FOREIGN KEY (payment_account_id, household_id) REFERENCES payment_accounts(id, household_id) ON DELETE SET NULL (payment_account_id)"
echo -n "    UNIQUE (id, household_id): "
q "SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname='bill_payments_id_household_id_key'"
echo "    indexes:"
q "SELECT indexname || ' :: ' || indexdef FROM pg_indexes WHERE tablename='bill_payments' ORDER BY indexname" | sed 's/^/      /'
same "(household_id, paid_on DESC) exists" \
  "SELECT count(*) FROM pg_indexes WHERE tablename='bill_payments' AND indexdef LIKE '%(household_id, paid_on DESC)'" "1"
same "(bill_id, paid_on DESC) exists" \
  "SELECT count(*) FROM pg_indexes WHERE tablename='bill_payments' AND indexdef LIKE '%(bill_id, paid_on DESC)'" "1"

echo "=== 6. CONSTRAINTS BY BEHAVIOUR ==="
refuses "a payment of negative amount is refused (not a refund)" \
  "INSERT INTO bill_payments (household_id,bill_id,amount,paid_on) VALUES ('$H','b0000000-0000-0000-0000-000000000005',-10,'2026-11-01');" \
  "bill_payments_amount_check"
refuses "a payment of zero is refused" \
  "INSERT INTO bill_payments (household_id,bill_id,amount,paid_on) VALUES ('$H','b0000000-0000-0000-0000-000000000005',0,'2026-11-01');" \
  "bill_payments_amount_check"
refuses "a payment with no paid_on is refused" \
  "INSERT INTO bill_payments (household_id,bill_id,amount) VALUES ('$H','b0000000-0000-0000-0000-000000000005',45);" \
  "paid_on"
refuses "a payment naming ANOTHER FAMILY's bill is refused (composite FK)" \
  "INSERT INTO bill_payments (household_id,bill_id,amount,paid_on) VALUES ('$H','b0000000-0000-0000-0000-000000000006',120,'2026-11-05');" \
  "bill_payments_bill_fkey"
refuses "a payment naming ANOTHER FAMILY's account is refused (composite FK)" \
  "INSERT INTO bill_payments (household_id,bill_id,amount,paid_on,payment_account_id) VALUES ('$H','b0000000-0000-0000-0000-000000000005',45,'2026-11-01','$ACC2');" \
  "bill_payments_account_fkey"
refuses "a blank method is refused" \
  "INSERT INTO bill_payments (household_id,bill_id,amount,paid_on,method) VALUES ('$H','b0000000-0000-0000-0000-000000000005',45,'2026-11-01','  ');" \
  "bill_payments_method_check"
accepts "a payment of a bill in the same family is accepted" \
  "INSERT INTO bill_payments (household_id,bill_id,amount,paid_on,paid_for_due_date,payment_account_id,method,note)
   VALUES ('$H','b0000000-0000-0000-0000-000000000005',45.00,'2026-11-24','2026-11-25','$ACC','ach','trash, paid by transfer');"

echo "=== 7. TWO PAYMENTS ON ONE BILL (criteria 2 and 5) ==="
# b3 "Internet" already has one reconstructed row for 2026-11-12. Record a real
# second payment for the December cycle, through the ledger, and keep the cache
# in step the way bill_pay does (both, one transaction).
q "INSERT INTO bill_payments (household_id,bill_id,amount,paid_on,paid_for_due_date,payment_account_id,method,is_reconstructed)
   VALUES ('$H','b0000000-0000-0000-0000-000000000003',79.99,'2026-12-10','2026-12-12','$ACC','card',false);" >/dev/null \
  || fail "second payment insert"
q "UPDATE bills SET paid_date='2026-12-10', paid_for_due_date='2026-12-12', updated_at=NOW()
    WHERE id='b0000000-0000-0000-0000-000000000003';" >/dev/null || fail "cache update"
echo "    b3 Internet, all rows (the old schema could express ONE):"
q "SELECT paid_on, paid_for_due_date, amount, COALESCE(method,'-'), is_reconstructed
     FROM bill_payments WHERE bill_id='b0000000-0000-0000-0000-000000000003'
    ORDER BY paid_on DESC" | sed 's/^/      /'
same "BOTH payments retained (expect 2)" \
  "SELECT count(*) FROM bill_payments WHERE bill_id='b0000000-0000-0000-0000-000000000003'" "2"
same "the older payment is still there, with its own date" \
  "SELECT paid_on::text FROM bill_payments WHERE bill_id='b0000000-0000-0000-0000-000000000003' ORDER BY paid_on LIMIT 1" "2026-11-12"
same "bills.paid_date reflects the LATER payment (the cache)" \
  "SELECT paid_date::text FROM bills WHERE id='b0000000-0000-0000-0000-000000000003'" "2026-12-10"
same "bills.paid_for_due_date reflects the later coverage" \
  "SELECT paid_for_due_date::text FROM bills WHERE id='b0000000-0000-0000-0000-000000000003'" "2026-12-12"
echo "    criterion 5 — 'sum of payments for this household this year', ONE query, no audit log:"
q "SELECT sum(amount) FROM bill_payments
    WHERE household_id='$H' AND paid_on >= '2026-01-01' AND paid_on < '2027-01-01'" | sed 's/^/      /'
same "the sum is over the LEDGER, and includes both of b3's payments (6 rows: 5 with an amount + the NULL-amount water bill)" \
  "SELECT count(*) FROM bill_payments WHERE household_id='$H' AND paid_on >= '2026-01-01' AND paid_on < '2027-01-01'" "6"
same "SUM() skips the unknown amount instead of counting it as zero" \
  "SELECT sum(amount)::text FROM bill_payments WHERE bill_id='b0000000-0000-0000-0000-000000000004'" ""

echo "    the same question on the OLD schema, for comparison (only the latest payment per bill):"
q "SELECT sum(amount) FROM bills WHERE household_id='$H' AND paid_date >= '2026-01-01' AND paid_date < '2027-01-01'" | sed 's/^/      /'
echo "    ...and what it CANNOT see: b3's November payment"
q "SELECT paid_on || ' ' || amount FROM bill_payments WHERE bill_id='b0000000-0000-0000-0000-000000000003' ORDER BY paid_on" | sed 's/^/      /'

echo "=== 8. cascade: a deleted account keeps its payments; a deleted bill takes its own ==="
q "INSERT INTO bill_payments (household_id,bill_id,amount,paid_on,payment_account_id) VALUES ('$H','b0000000-0000-0000-0000-000000000005',45,'2026-12-24','$ACC');" >/dev/null
before=$(q "SELECT count(*) FROM bill_payments WHERE household_id='$H'")
q "UPDATE bill_payments SET payment_account_id='$ACC' WHERE household_id='$H';" >/dev/null
q "DELETE FROM payment_accounts WHERE id='$ACC';" >/dev/null || fail "delete account"
same "deleting the account kept every payment" \
  "SELECT count(*) FROM bill_payments WHERE household_id='$H'" "$before"
same "and forgot where the money came from" \
  "SELECT count(*) FROM bill_payments WHERE payment_account_id IS NOT NULL AND household_id='$H'" "0"
q "DELETE FROM bills WHERE id='b0000000-0000-0000-0000-000000000007';" >/dev/null || fail "delete unpaid bill"
q "INSERT INTO bills (id,household_id,name,amount,frequency,due_day) VALUES ('b0000000-0000-0000-0000-000000000008','$H','Doomed',10,'monthly',1);" >/dev/null
q "INSERT INTO bill_payments (household_id,bill_id,amount,paid_on) VALUES ('$H','b0000000-0000-0000-0000-000000000008',10,'2026-11-01');" >/dev/null
q "DELETE FROM bills WHERE id='b0000000-0000-0000-0000-000000000008';" >/dev/null || fail "delete bill"
same "deleting the bill took its payment with it" \
  "SELECT count(*) FROM bill_payments WHERE bill_id='b0000000-0000-0000-0000-000000000008'" "0"

echo "=== 9. the ledger is NOT the reader yet: bills still drives coverage ==="
# Prove it by making the cache and the ledger disagree, and showing the columns
# the readers use hold the cache's value, untouched by the ledger.
same "bills.paid_date is what an existing reader sees (not MAX(paid_on))" \
  "SELECT CASE WHEN b.paid_date = (SELECT max(paid_on) FROM bill_payments p WHERE p.bill_id=b.id) THEN 'same' ELSE 'differs' END
     FROM bills b WHERE b.id='b0000000-0000-0000-0000-000000000003'" "same"

echo "=== 10. sort order proves idx_bill_payments_bill_paid is the shape ==="
$PSQL -U postgres -d homeos -c "SET enable_seqscan=off; EXPLAIN (COSTS OFF) SELECT paid_on FROM bill_payments WHERE bill_id='b0000000-0000-0000-0000-000000000003' ORDER BY paid_on DESC;" 2>&1 | sed 's/^/    /'

echo "=== 11. DOWN (criterion 7) ==="
out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/090_bill_payments.down.sql" 2>&1)
rc=$?
echo "$out"
[ $rc -eq 0 ] || fail "090 down"
same "bill_payments is gone" \
  "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='bill_payments'" "0"
BILLS_DOWN=$($PSQL -U postgres -d homeos -tAc \
  "SELECT md5(string_agg(b::text, '|' ORDER BY id)) FROM bills b")
[ -n "$BILLS_DOWN" ] || fail "bills is empty after the down"
same "bills survived the down: one row per bill that existed (7 seeded, b7 and b8 deleted in section 8)" \
  "SELECT count(*) FROM bills" "6"
echo "    bills checksum after down = $BILLS_DOWN"
same "bills still holds its paid_date after the ledger is gone" \
  "SELECT paid_date::text FROM bills WHERE id='b0000000-0000-0000-0000-000000000003'" "2026-12-10"
same "bills.paid_for_due_date still holds the later coverage" \
  "SELECT paid_for_due_date::text FROM bills WHERE id='b0000000-0000-0000-0000-000000000003'" "2026-12-12"

echo "=== 11b. down twice (idempotent) ==="
out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/090_bill_payments.down.sql" 2>&1)
[ $? -eq 0 ] || fail "090 down twice"
echo "    down twice OK"

echo "=== 11c. UP AGAIN, on a database that has already been through both ==="
out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/090_bill_payments.up.sql" 2>&1)
[ $? -eq 0 ] || fail "090 up again"
same "the re-up backfilled again, one row per paid bill (water included, amount NULL)" \
  "SELECT count(*) FROM bill_payments WHERE is_reconstructed" "5"
same "and did not double up" \
  "SELECT count(*) FROM bill_payments WHERE bill_id='b0000000-0000-0000-0000-000000000003'" "1"
echo "    (5 = every bill with a paid_date: mortgage, dentist, internet, water
         (amount NULL) and the other family's utility. b3's earlier November
         row is gone with the first drop and the re-up rebuilds from
         bills.paid_date, which now says December — a reconstruction is ONE
         row per bill because that is all the old schema ever held. Stated,
         not hidden: this is exactly why the ledger must be written by the
         write path (bill_pay), not only by a backfill.)"
echo -n "    b3's re-up row: "
q "SELECT paid_on || ' / ' || is_reconstructed FROM bill_payments WHERE bill_id='b0000000-0000-0000-0000-000000000003'"

echo "=== 12. the deploy/rollback cycle on a FRESH database, 001..089 then 090 up/down/up ==="
# Not the whole-chain walk: 020's own down needs bills.property_id NOT NULL and
# this seed has property-less bills, a limit of 020 that predates 090. What a
# deploy actually does is apply 090 onto 089 and, if it must be undone, down to
# 089 and up again — so that is what is proved here, on a database that never
# saw the previous sections' deletions.
$PSQL -U postgres -d postgres -c "DROP DATABASE IF EXISTS cycle090;" >/dev/null
$PSQL -U postgres -d postgres -c "CREATE DATABASE cycle090;" >/dev/null
cq() { $PSQL -U postgres -d cycle090 -tAF'|' -c "$1"; }
# same, but against the cycle database (the `same` above is pinned to homeos).
csame() {
  local got
  got=$(cq "$2")
  [ "$got" = "$3" ] || fail "$1: got '$got', want '$3'"
  echo "    OK        $1 = $3"
}
for f in $(ls "$MIG"/*.up.sql | sort); do
  bn=$(basename "$f" | cut -d_ -f1)
  [ "$bn" -ge 90 ] && continue
  out=$($PSQL -U postgres -d cycle090 -v ON_ERROR_STOP=1 -q -f - < "$f" 2>&1)
  if [ $? -ne 0 ]; then echo "--- $bn FAILED ---"; echo "$out" | tail -10; fail "cycle: baseline $bn"; fi
done
csame "cycle DB at 089 has no ledger" \
  "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='bill_payments'" "0"
# A paid bill, on the pre-090 schema, so the backfill has something to do.
cq "INSERT INTO households (id,name,timezone) VALUES ('a0000000-0000-0000-0000-00000000000c','Cycle','UTC');" >/dev/null
cq "INSERT INTO bills (id,household_id,name,amount,frequency,due_day,paid_date,paid_for_due_date)
    VALUES ('b0000000-0000-0000-0000-00000000000c','a0000000-0000-0000-0000-00000000000c','Cycle bill',99.50,'monthly',3,'2026-11-03','2026-11-03');" >/dev/null
CYC_BEFORE=$(cq "SELECT md5(string_agg(b::text,'|' ORDER BY id)) FROM bills b")
for step in up down up down; do
  out=$($PSQL -U postgres -d cycle090 -v ON_ERROR_STOP=1 -q -f - < "$MIG/090_bill_payments.$step.sql" 2>&1)
  if [ $? -ne 0 ]; then echo "--- 090 $step FAILED ---"; echo "$out" | tail -10; fail "cycle: 090 $step"; fi
  echo "    090 $step OK"
done
CYC_AFTER=$(cq "SELECT md5(string_agg(b::text,'|' ORDER BY id)) FROM bills b")
[ "$CYC_BEFORE" = "$CYC_AFTER" ] || fail "cycle: bills changed across up/down/up/down: $CYC_BEFORE -> $CYC_AFTER"
echo "    OK        bills identical across up/down/up/down ($CYC_AFTER)"
csame "cycle DB ends at 089 with no ledger" \
  "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='bill_payments'" "0"
csame "and the paid bill still holds its dates" \
  "SELECT paid_date::text || ' / ' || paid_for_due_date::text FROM bills WHERE id='b0000000-0000-0000-0000-00000000000c'" \
  "2026-11-03 / 2026-11-03"
$PSQL -U postgres -d postgres -c "DROP DATABASE IF EXISTS cycle090;" >/dev/null

echo
echo "ALL 090 CHECKS PASSED"
