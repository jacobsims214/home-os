#!/bin/bash
# Test 079 (notification vocabularies) up and down against real PostgreSQL 16
# with every earlier migration applied. (#5516, schema review F10 + F3)
#
#   initdb -D /tmp/pg16 -U postgres --auth=trust
#   pg_ctl -D /tmp/pg16 -o "-p 55499 -k /tmp" start
#   PGPORT=55499 ./packages/db/verify/test_079.sh
#
# What it proves, in order:
#   1. the baseline 001..078 applies and the defect is REAL on it (a bogus type,
#      a bogus channel and a url link with no url are all accepted);
#   2. 079 up applies onto that, and refuses every bogus write afterwards while
#      every valid one still inserts;
#   3. rows that cannot satisfy the vocabulary stop the up and are named, with
#      nothing changed;
#   4. down restores the VARCHARs, keeps every row, and the defect comes back;
#   5. down twice, up again.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIG="$HERE/../migrations"
export PATH=/opt/homebrew/opt/postgresql@16/bin:$PATH
PHOST="${PGHOST:-127.0.0.1}"
PPORT="${PGPORT:-55499}"
PSQL="${PSQL:-psql} -h $PHOST -p $PPORT"

fail() { echo "FAIL: $*"; exit 1; }
q() { $PSQL -U postgres -d homeos -tAF'|' -c "$1"; }
# accepted/rejected helpers: echo the outcome and fail the script on surprise.
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

echo "=== 0. postgres version ==="
$PSQL -U postgres -d postgres -tAc "SELECT current_setting('server_version')" | sed 's/^/    /'
$PSQL -U postgres -d postgres -tAc "SELECT current_setting('server_version_num')::int >= 160000" | grep -qx t || fail "need PostgreSQL 16+"
echo "    16+ ok"

echo "=== 0b. 079 is the file this branch carries ==="
ls "$MIG"/079_*.up.sql >/dev/null 2>&1 || fail "079_notification_vocabularies.up.sql missing"
echo "    $(basename "$MIG"/079_*.up.sql)"

echo "=== 1. fresh database, apply 001..078 ==="
$PSQL -U postgres -d postgres -c "DROP DATABASE IF EXISTS homeos;" >/dev/null
$PSQL -U postgres -d postgres -c "CREATE DATABASE homeos;" >/dev/null
$PSQL -U postgres -d homeos -q -c "CREATE ROLE app;" >/dev/null 2>&1
n=0
for f in $(ls "$MIG"/*.up.sql | sort); do
  bn=$(basename "$f" | cut -d_ -f1)
  if [ "$bn" -ge 79 ]; then continue; fi
  out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$f" 2>&1)
  if [ $? -ne 0 ]; then echo "--- $bn FAILED ---"; echo "$out" | tail -20; fail "baseline migration $bn"; fi
  n=$((n+1))
done
echo "    $n migrations applied (001..078)"

H='a0000000-0000-0000-0000-000000000001'
q "INSERT INTO households (id,name,timezone) VALUES ('$H','Fam','America/New_York');" >/dev/null || fail seed household

echo "=== 2. the defect is REAL at 078 ==="
accepts "type='bill_du' before 079"  "INSERT INTO notifications (household_id,type,title) VALUES ('$H','bill_du','t')"
accepts "channel='carrier_pigeon' before 079" "INSERT INTO notifications (household_id,type,title,channel) VALUES ('$H','bill_due','t','carrier_pigeon')"
accepts "type='' before 079" "INSERT INTO notifications (household_id,type,title) VALUES ('$H','','t')"
accepts "url link with no url before 079" "INSERT INTO document_links (household_id,entity_type,entity_id,link_type) VALUES ('$H','household','$H','url')"
# document_links already had a vocabulary check from 056, and the trigger has
# refused a bad file link_id since 038. State that, so the fix below is not
# credited with work 056/038 did.
refuses "link_type='bogus' (056's check, already there)" "INSERT INTO document_links (household_id,entity_type,entity_id,link_type,url) VALUES ('$H','household','$H','bogus','https://x.y')" "document_links_link_type_check"
refuses "file link with a non-UUID link_id (038's trigger, already there)" "INSERT INTO document_links (household_id,entity_type,entity_id,link_type,link_id) VALUES ('$H','household','$H','file','not-a-uuid')" "link_id must be the id"

echo "=== 3. clean up the deliberate bad rows, then 079 up ==="
q "DELETE FROM notifications WHERE type IN ('bill_du','');" >/dev/null
q "DELETE FROM notifications WHERE channel = 'carrier_pigeon';" >/dev/null
q "DELETE FROM document_links;" >/dev/null
out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/079_notification_vocabularies.up.sql" 2>&1)
if [ $? -ne 0 ]; then echo "--- 079 up FAILED ---"; echo "$out" | tail -20; fail "079 up"; fi
echo "$out" | grep NOTICE | sed 's/^/    /'

echo "=== 3a. notifications.type is constrained ==="
refuses "type='bill_du'"  "INSERT INTO notifications (household_id,type,title) VALUES ('$H','bill_du','t')" "notification_type"
refuses "type=''"         "INSERT INTO notifications (household_id,type,title) VALUES ('$H','','t')" "notification_type"
refuses "type='BILL_DUE'" "INSERT INTO notifications (household_id,type,title) VALUES ('$H','BILL_DUE','t')" "notification_type"
refuses "type='reminder'" "INSERT INTO notifications (household_id,type,title) VALUES ('$H','reminder','t')" "notification_type"
for t in bill_due bill_digest maintenance_due calendar_alarm item_changed; do
  accepts "type='$t'" "INSERT INTO notifications (household_id,type,title) VALUES ('$H','$t','t')"
done

echo "=== 3b. notifications.channel is constrained ==="
refuses "channel='carrier_pigeon'" "INSERT INTO notifications (household_id,type,title,channel) VALUES ('$H','bill_due','t','carrier_pigeon')" "notification_channel"
refuses "channel='email' (nothing writes it)" "INSERT INTO notifications (household_id,type,title,channel) VALUES ('$H','bill_due','t','email')" "notification_channel"
accepts "channel='in_app'" "INSERT INTO notifications (household_id,type,title,channel) VALUES ('$H','bill_due','t','in_app')"
accepts "channel omitted (015's DEFAULT)" "INSERT INTO notifications (household_id,type,title) VALUES ('$H','bill_due','default')"

echo "=== 3c. document_links link target ==="
refuses "url type, url NULL"  "INSERT INTO document_links (household_id,entity_type,entity_id,link_type) VALUES ('$H','household','$H','url')" "document_links_link_target_check"
refuses "url type, url=''"    "INSERT INTO document_links (household_id,entity_type,entity_id,link_type,url) VALUES ('$H','household','$H','url','')" "document_links_link_target_check"
refuses "file type, bad link_id" "INSERT INTO document_links (household_id,entity_type,entity_id,link_type,link_id) VALUES ('$H','household','$H','file','not-a-uuid')"
refuses "file type, link_id NULL" "INSERT INTO document_links (household_id,entity_type,entity_id,link_type) VALUES ('$H','household','$H','file')"
accepts "url type with a url" "INSERT INTO document_links (household_id,entity_type,entity_id,link_type,url) VALUES ('$H','household','$H','url','https://example.com/x')"
# -tA still prints psql's command tag after RETURNING, so take the first line.
blob=$(q "INSERT INTO file_blobs (data,size) VALUES ('x',1) RETURNING id;" | head -1)
file=$(q "INSERT INTO files (household_id,blob_id,name,size) VALUES ('$H','$blob','w.pdf',1) RETURNING id;" | head -1)
accepts "file type at a real file" "INSERT INTO document_links (household_id,entity_type,entity_id,link_type,link_id) VALUES ('$H','household','$H','file','$file')"

echo "=== 4. a row that cannot satisfy the vocabulary stops the up, named ==="
before=$(q "SELECT count(*) FROM notifications;")
q "INSERT INTO notifications (household_id,type,title) VALUES ('$H','bill_du','typo');" >/dev/null 2>&1 \
  && fail "the domain did not refuse bill_du"
# Down, plant the row the old schema allowed, and try to come back up.
$PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/079_notification_vocabularies.down.sql" >/dev/null 2>&1 || fail "079 down"
q "INSERT INTO notifications (household_id,type,title) VALUES ('$H','bill_du','typo');" >/dev/null || fail "plant typo at 078"
out=$($PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/079_notification_vocabularies.up.sql" 2>&1)
if [ $? -eq 0 ]; then fail "079 up accepted a row with type='bill_du'"; fi
echo "$out" | grep -E "ERROR|HINT" | head -3 | sed 's/^/    /'
echo "$out" | grep -q "bill_du" || fail "the refusal did not name the offending value"
after=$(q "SELECT count(*) FROM notifications;")
[ "$after" -gt "$before" ] || fail "the unconstrainable row was DELETED, not reported"
echo "    row count $before -> $after: the row was reported, not rewritten or dropped"
# The column is still VARCHAR: nothing half-applied.
dom=$(q "SELECT COALESCE(domain_name,'(none)') FROM information_schema.columns WHERE table_name='notifications' AND column_name='type';")
[ "$dom" = "(none)" ] || fail "079 half-applied: type is $dom after a refused up"

echo "=== 5. repair, up, then down keeps every row and the defect returns ==="
q "UPDATE notifications SET type='bill_due' WHERE type='bill_du';" >/dev/null || fail repair
$PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/079_notification_vocabularies.up.sql" >/dev/null 2>&1 || fail "079 up after repair"
rowsUp=$(q "SELECT count(*) FROM notifications;")
$PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/079_notification_vocabularies.down.sql" >/dev/null 2>&1 || fail "079 down"
rowsDown=$(q "SELECT count(*) FROM notifications;")
[ "$rowsUp" = "$rowsDown" ] || fail "down changed the row count: $rowsUp -> $rowsDown"
for c in type channel; do
  own=$(q "SELECT COALESCE(domain_name,'(none)') FROM information_schema.columns WHERE table_name='notifications' AND column_name='$c';")
  [ "$own" = "(none)" ] || fail "after down, notifications.$c is still domain $own"
done
nulls=$(q "SELECT count(*) FROM information_schema.columns WHERE table_name='notifications' AND column_name IN ('type','channel') AND is_nullable='NO';")
[ "$nulls" = "2" ] || fail "after down, a column lost NOT NULL"
accepts "type='bill_du' after down (the defect is back)" "INSERT INTO notifications (household_id,type,title) VALUES ('$H','bill_du','back')"
q "DELETE FROM notifications WHERE type='bill_du';" >/dev/null

echo "=== 6. down is idempotent, up works again ==="
$PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/079_notification_vocabularies.down.sql" >/dev/null 2>&1 || fail "079 down twice"
$PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/079_notification_vocabularies.down.sql" >/dev/null 2>&1 || fail "079 down three times"
$PSQL -U postgres -d homeos -v ON_ERROR_STOP=1 -q -f - < "$MIG/079_notification_vocabularies.up.sql" >/dev/null 2>&1 || fail "079 up again"
[ "$(q "SELECT count(*) FROM pg_constraint WHERE conname IN ('notification_type_check','notification_channel_check','document_links_link_target_check');")" = "3" ] \
  || fail "the three constraints are not all present after the round trip"

echo
echo "=== ALL 079 CHECKS PASSED ==="