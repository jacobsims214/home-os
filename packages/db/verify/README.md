# Verifying migration 075

Three scripts, one real PostgreSQL 15+ server (16 used). They are here, not in
`packages/db/migrations`, because they test the SQL rather than the app: the
normal Go test harness clones a fully-migrated template and cannot apply a
migration in the middle of a run.

Start a throwaway cluster first:

```sh
initdb -D /tmp/pg075 -U postgres --auth=trust
pg_ctl -D /tmp/pg075 -o "-p 55497 -k /tmp -c listen_addresses=127.0.0.1" start
export PATH="/opt/homebrew/opt/postgresql@16/bin:$PATH"   # or any PG15+ bin
```

Then, from this directory:

| script | what it proves |
| --- | --- |
| `test_075.sh` | applies 001..074, seeds rows the way the old schema allowed (including `is_autopay` NULL), applies 075, asserts every constraint by behaviour, then down, down again, up again |
| `test_chain.sh` | the full 035..075 down/up walk, twice — what `TestHotPathIndexes035_FullChainReversible` does now that `migrationsFrom` globs the directory |
| `test_subset.sh` | the hardcoded newest-first down list the existing `testutil` tests use (written when 074 was head, so it omits 075), with 075 applied underneath, and back up — proves 075 does not block them |

`verification.log` is the output of all three at commit time: 19 behavioural
assertions (10 refusals, 9 accepts) and three suite pass markers, no failures.

The scripts create and drop the `homeos`, `chain` and `subset` databases on the
server they point at.
