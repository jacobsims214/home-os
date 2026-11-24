# Subject-contradiction harness (migration 087)

Proves the rule `derive_subject_property()` now enforces on all five tables that
carry the subject pattern (`bills`, `loans`, `maintenance_schedules`,
`maintenance_tasks`, `guides`). Run against a PostgreSQL 16 database, from this
directory.

Start a throwaway cluster (Docker's overlay2 store is broken on this host; use
Homebrew's `postgresql@16`):

```sh
export PATH="/opt/homebrew/opt/postgresql@16/bin:$PATH"
initdb -D /tmp/pg087 -U postgres --auth=trust
pg_ctl -D /tmp/pg087 -o "-p 55451 -k /tmp -c listen_addresses=127.0.0.1" start
psql -h 127.0.0.1 -p 55451 -U postgres -c 'CREATE ROLE app'   # 032 references it
```

| file | what it proves |
| --- | --- |
| `verify_087.sql` | 90 cases: the answer to "does this rule hold?" — 18 per table, INSERT and UPDATE, plus every legitimate shape. Prints `PASS` or `<-- FAIL` per case. |
| `verify_query.sql` | on a database **at 087**: `property_id = $1` alone answers "what do we spend on this property", the OR form adds 0 rows, and a contradicting UPDATE is refused |
| `verify_query_drift.sql` | on a database **at 086**: the drift 087 removes — one row counted under two houses (227.00 against 127.00 of bills) |
| `fixtures.sql` | one family, two properties, one vehicle, an asset at each property and one at none |

## The revert-check

`verify_087.sql` always asserts 087's rule, so the script is identical in both
runs and only the schema changes:

```sh
# at 087 -> 90/90, verdict PASS
psql -d <db> -f verify_087.sql

# at 086 -> 65/90, verdict FAIL, 25 cases print <-- FAIL
#           (5 contradiction shapes x 5 tables: accepted before, refused now)
psql -d <db> -f verify_087.sql
```

To produce the second database, apply `packages/db/migrations/*.up.sql` in order
skipping `087_*`. The 25 failures are listed by name in the ticket.

## Why the backfill needs a database with stale rows in it

A fresh migration run has no stale rows, so the backfill reports 0 and proves
nothing. Build them the way they exist in the wild — the derive trigger is what
keeps `property_id` in step, so a row written while it is disabled (a bulk load,
a restore, a pre-038 dataset) is a stale row:

```sql
ALTER TABLE bills DISABLE TRIGGER bills_derive_subject_property;
INSERT INTO bills (household_id, name, asset_id, property_id) VALUES (...);
ALTER TABLE bills ENABLE TRIGGER bills_derive_subject_property;
```

Applying 087 to that database prints, per table, how many rows contradicted
their asset and how many it re-derived; on the verification run it was 1 per
table, 5 total, 0 still disagreeing afterwards.

## Notes

- Every case is its own subtransaction, so an expected rejection does not abort
  the run. Both scripts are re-runnable.
- Port 55432 is claimed by other Home OS workers' scratch clusters on this host.
  Use another `-p` (087 used 55451).
- Build the **whole** tree, not just the tables you care about: later migrations
  add CHECKs on the same tables (082's `bills_schedule_one_representation_check`
  rejects a naive monthly bill with a `due_date`).
