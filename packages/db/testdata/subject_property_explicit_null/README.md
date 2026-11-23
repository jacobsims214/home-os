# Verifying migration 094 (an explicit property_id clear)

`verify_094.sql` proves the one behaviour 094 changes, and that it changed
nothing else. It is a plain SQL harness rather than a Go test because it needs a
database at 091 and then at 094 on the same data — the Go harness clones a
fully-migrated template and cannot apply a migration mid-run (same reason
`packages/db/verify/` exists).

```sh
# a throwaway PG16 (Docker cannot run on some of our hosts)
initdb -D /tmp/pg094 -U postgres --auth=trust
pg_ctl -D /tmp/pg094 -o "-p 55499 -k /tmp -c listen_addresses=127.0.0.1" start

psql -h 127.0.0.1 -p 55499 -U postgres -d postgres -c 'CREATE DATABASE v92'
for f in packages/db/migrations/*.up.sql; do psql -h 127.0.0.1 -p 55499 -U postgres -d v92 -v ON_ERROR_STOP=1 -f "$f"; done

cd packages/db/testdata/subject_property_explicit_null
psql -h 127.0.0.1 -p 55499 -U postgres -d v92 -f fixtures.sql
psql -h 127.0.0.1 -p 55499 -U postgres -d v92 -f verify_094.sql
```

## What it asserts

| case | what it proves |
| --- | --- |
| AC1 / AC1b | an explicit `property_id := NULL` on a row with `asset_id` set **persists**, on all five subject tables (bills, loans, maintenance_schedules, maintenance_tasks, guides) |
| AC2 | repeating the clear is idempotent — a retry does not resurrect the value |
| AC3 | an **asset change** still re-derives, even with a carried NULL (the shape the API/MCP write path sends) |
| AC3b | a carried stale value is still corrected (087's rule) |
| AC4 | an INSERT of an asset-subject row with no property still derives |
| AC5 | a contradicting `property_id` presented against an asset is still refused, SQLSTATE 23514 |
| AC7 | the property delete cascade still ends at `asset_id=NULL, property_id=NULL` |
| AC8 | a vehicle alongside a subject is still refused |

## Revert-check (done at commit time, and how to redo it)

The script asserts 094's rule in **both** runs; only the schema differs.

```
at 094     -> 12 ok, 0 '<-- FAIL'
094's down -> 6 '<-- FAIL': AC1 and AC1b on all five tables ("the clear was
              OVERWRITTEN back to 2222…") plus AC2 ("the retry resurrected")
```

Run:

```sh
psql -d v92 -f ../../migrations/094_subject_property_explicit_null.down.sql
psql -d v92 -f verify_094.sql      # expect 6 FAILs
psql -d v92 -f ../../migrations/094_subject_property_explicit_null.up.sql
psql -d v92 -f verify_094.sql      # expect 0 FAILs
```

**Starting point of the test, stated as §5 requires:** it reproduces via the
exact statement the ticket reports — `UPDATE <table> SET property_id = NULL` on
a row whose `asset_id` is set — on a database built from `packages/db/migrations`
in order, and it fails on regression because the pre-094 trigger rewrites
`NEW.property_id` from the asset in a BEFORE trigger, which the assertion reads
back from the row.
