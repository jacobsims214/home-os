# Standing instructions for every Home OS worker

> **This is the repo copy.** The canonical version lives in the AMP project KB
> (`how-to/worker-standing-instructions.md`, project 7, Home OS); the KB copy is authoritative and
> this file follows it. This copy exists so workers can read the instructions from their own
> checkout. Changes belong in the KB first, then here — never the other way round.
>
> **Repo-copy notes, verified against `origin/feat/family-hub` @ `d8f8d59` on 2026-11-23.** These
> correct only what was untrue *of this checkout*; the body below is otherwise the KB text verbatim.
>
> - Several documents named in the body are **project-KB paths, not repo files**: `gotchas/…`,
>   `ops/…`, `process/…`, and `architecture/write-paths-and-side-effects.md`. Read them with
>   `amp_kb_get`, not `cat`. (In the tree, `architecture/` holds only `database-schema.md` and
>   `schema-review-2026-11.md`.)
> - §8's kubeconfig path is relative to the homelab tree (`.../talos/pve-1`), not to the repo.
>   Confirmed present on 2026-11-23: `amp-work/5319/kube/admin.kubeconfig`, mode `0600`, cert
>   `notAfter=Nov 18 22:20:47 2027 GMT`, server `https://192.168.8.226:6443`.
> - §3 deliberately states **no current migration ceiling**. Re-derive it every time (§3a). For
>   orientation only: the tip was migration **094** on 2026-11-23 — that number is history the
>   moment you read it, not a ceiling.

**Read this before starting any ticket in this project.** It exists so dispatches do not have to
repeat it, and so it cannot drift between tickets.

---

## 1. The repo, and how to trust it

**Repo:** `/Users/jacobsims/development/simstech/amp-repos/home-os`

**Integration branch of record: `feat/family-hub`.**

> **ALWAYS establish truth from the remote first:**
> ```
> git ls-remote --heads origin | grep family-hub
> git fetch origin --prune
> git rev-parse origin/feat/family-hub
> ```

**Do not trust the working tree or a local ref.** On 2026-11-21 the shared checkout held *three
different values* for "one branch": detached HEAD at `dc01ab9` (ceiling 074), a stale local
`refs/heads/feat/family-hub` at `970ee24` (ceiling **070**, no `packages/billdue`), and the true
`origin/feat/family-hub` at `2db4833` (ceiling 077). A worker read the local ref, reported it as
origin, and built a migration on a base 158 commits behind.

Full write-up: `gotchas/stale-refs-and-detached-checkout.md`.

`feat/family-hub-int` is a **strict ancestor**, not a parallel branch. Never base work on it.

---

## 2. Merging: how to prove work actually landed

A completion comment is a claim, not a fact. Eight tickets in November 2026 reported success with
their work on an unmerged branch.

For each item:
1. Check absence with **both** `git merge-base --is-ancestor` **and** `git cherry` (patch-level).
   Ancestry alone has been wrong repeatedly; `git cherry` survives shallow-clone boundaries.
2. Land it, one commit/merge per ticket, message naming the ticket.
3. **Prove by content, read back from origin:** `git show origin/feat/family-hub:<path>`.
   Name the file and the line/string that proves the change. "Merged cleanly" is not proof.

Never force-push shared history. Never rebase someone else's branch as a side effect.

**Before deleting a merge ticket, prove the branch landed.** A merge ticket was once deleted as a
"stale duplicate" without checking; the branch had never been merged, and the fix sat undeployed
for a day. `git cherry` answers it in seconds.

**Completed ≠ merged.** AMP tracks them separately. A ticket with `merge_state: needed` still
blocks its dependents even when completed.

---

## 3. Migrations

**The ceiling moves while tickets are in flight. Three rules, all mandatory.**

### 3a. Claim a number from the base branch's own tree, at the moment you claim it
Never trust a number quoted in your brief or from an earlier ticket.

```
git fetch origin --prune
git ls-remote --heads origin | grep family-hub
```
Then read the base's `packages/db/migrations/` for the real ceiling and take the next free number
**above** it. Report the number and how you established it was free.

### 3b. Push the empty migration file IMMEDIATELY after choosing the number
**This is the rule that prevents the collisions.** Sweeping remote branches cannot see a claim that
exists only in someone else's worktree. Three collisions happened in one day (090 twice, 092
twice, 078 twice) because two workers each swept correctly, each saw the same ceiling, and neither
had pushed yet.

So: commit and push a stub migration (filename + header comment) as your **first** action, before
writing the SQL. Then the next worker's sweep sees it.

### 3c. The collision gate, which must print NOTHING
```
ls packages/db/migrations/*.up.sql | sed -E 's|.*/([0-9]+)_.*|\1|' | sort | uniq -d
```
A duplicate makes `golang-migrate` refuse the **entire** tree with `duplicate migration file`.

### 3d. A LOW number on a stale branch is worse than a duplicate
If your branch was cut before recent migrations merged, your new migration can be numbered
*beneath* existing ones. That leaves a **hole**, and merging it can silently revert everything
above. Four branches had to be rebased and renumbered for exactly this (083→089, 085→088, 075→090,
075/076→abandoned).

**If your base is behind, rebase and renumber to the next free HIGH number.**

### 3e. Every migration needs
An up, a **down**, a backfill **before** any constraint, and proof of up → down → up on a real
database.

### 3f. Constraint creation order can be load-bearing
Postgres fires a parent's child `ON DELETE` actions in **internal trigger OID order = the order the
constraints were created**. Dropping and re-adding a constraint assigns a new, higher OID and can
silently invert a cascade. Migration 091 has a section whose whole purpose is creating CASCADE
parents *before* trigger-derived `SET NULL` columns, and a harness assertion (AC7) that fails if
anyone reorders them. See `gotchas/fk-action-trigger-ordering.md`.

---

## 4. Running Postgres (Docker is broken on this host)

`overlay2` I/O errors mean **containers cannot be created** — testcontainers suites fail for
reasons unrelated to your change.

- Use **Homebrew `postgresql@16`** on a scratch cluster.
- **PG14 will not work**: migrations 043 and 058 use PG15+ `ON DELETE SET NULL (column)`.
- `TESTCONTAINERS`-based Go suites cannot run locally. Prove behaviour in SQL and say so plainly
  rather than claiming a test passed.
- In the cluster: **`home-os-postgres-1` is a read-only replica; the primary is `-2`, DB `app`.**
  Full detail in `ops/cluster-admin-access.md`.

---

## 5. Tests that cannot fail are worse than no tests

This project has shipped at least three: #4875 (compound matching), #5140 (wrong fixture type),
and a grep-only envoy check that passed the exact config which took the site down.

**Every test you write must be revert-checked**: put the defect back, watch the test go red,
restore the fix. Paste that evidence.

**Revert-checking is necessary but not sufficient.** A test can fail on its defect and still be
green on a broken product if it does not *start where the person starts*. For each test state:
"reproduced via <exact starting point>; fails on regression because <what it would catch>".

---

## 6. One rule, one home

The single most repeated defect in this codebase. Each was fixed by consolidating, not patching:

| Rule | Duplicated as | Fixed by |
|---|---|---|
| Money visibility | per-handler checks | serialization boundary (#4796/#4806/#4807) |
| Mortgage math | inline in 3 components | `components/property/property-finance.ts` |
| "Is this bill on me?" | UI, MCP, worker | `packages/billdue/attention.go` |
| `"Not filed yet"` vs `"Other"` | two lookup tables | one `isFiled()` predicate |
| Household timezone | browser clock per page | one helper + an enforcement test |
| `updated_at` | 3 bespoke triggers + 72 Go writes | one `set_updated_at()` (077) |
| Typesense env | pasted into 2 of 3 deployments | one `_helpers.tpl` definition |

If you are about to write `if autopay {...} else {...}`, or compute a total, or decide what "today"
is — **find the existing function and call it.** If none exists, create one and say where.

Side-effects written as **Postgres triggers** are caller-independent; side-effects in **handler
code** leak (REST fires them, MCP does not). Prefer triggers. See
`architecture/write-paths-and-side-effects.md`.

---

## 7. Never fail silently

The most dangerous output in this codebase is a plausible wrong answer:

- A **grep-only envoy test** passed the config that took the site down.
- **Bills serialized 100× too large** because `pgtype.Numeric.Int.String()` dropped the exponent — a valid-looking number, no error.
- **`kb_search` degraded to literal word matching** when the MCP pod lacked `TYPESENSE_*` env vars, returning a normal empty result. An agent would confidently tell a family the document does not exist.
- **A Dockerfile `COPY` of a `.dockerignore`-excluded path** fails at build time with a cache-key error, after the change has already merged.

**If a capability is missing or a dependency is unreachable, say so in the output or fail.** Do not
return an empty result that looks like "nothing matched".

---

## 8. Secrets and credentials

**Never print a secret** — not to stdout, not into a ticket comment, not into a KB doc.

- **Cluster:** kubeconfig at `amp-work/5319/kube/admin.kubeconfig`, valid to 2027-11-18.
  Control plane `https://192.168.8.226:6443`. Runbook: `ops/cluster-admin-access.md`.
- **War-game Dex passwords:** namespace **`moltis`**, NOT `home-os`.
  `kubectl -n moltis get secret home-os-amber-wargame-password`.
- Read to a `0600` file, use it, scrub it.
- **Never** use or modify the `avware-prod` context. **Never** touch the GPU box's SSH or passwords.
- **Never** `helm upgrade`/`helm uninstall` moltis — it is Argo-managed.

Three separate "credentials expired" reports were all "credential exists, location undocumented".

---

## 9. The ticket may be wrong — verify, then act

Six times in November 2026 a worker caught the manager's ticket stating something false:

- `autopay_enabled` — `bills.is_autopay` already existed, read by six packages
- `payee_vendor_id` — `bills.vendor_id` was already the payee FK
- `num_nonnulls(property_id, asset_id, vehicle_id) <= 1` — impossible; a trigger derives `property_id` from `asset_id`
- "document that `rrule` wins when set" — `bills.rrule` is never read by anything
- "a composite FK gives a free child-side index" — **false**; Postgres indexes the *referenced* side only
- "`DEFERRABLE INITIALLY DEFERRED` fixes the trigger overwrite" — **false**; the rewrite is in a BEFORE trigger, before any RI check, and it costs +15–20% per write

**The ticket's stated cause is a hypothesis.** Trust the symptom, verify the cause against the live
system (query the catalogue, grep the callers, run the probe). If they differ, fix what is actually
there and say so with evidence — including when that means **not doing what you were told**.

**Be equally willing to conclude an area is FINE.** A review that finds a problem in everything is
not credible and will not be acted on.

---

## 10. Working style

- **Time box is real.** A written finding with no fix beats a dead run. If the run is going long,
  complete the ticket with what you achieved and what remains.
- **Commit incrementally.** Several tickets were rescued by a later run finding committed work in
  `.worktrees/<id>`. Uncommitted work is lost work.
- **Check `.worktrees/<ticket>` first** — a previous run may have left good work there.
- **If two consecutive tool calls fail, stop and report.** Do not retry a third time.
- **Do not re-read your own ticket more than once.** A worker was killed for calling
  `amp_get_task` on its own ticket five times in a row.
- **Declined indexes, declined constraints, and "already covered" are valuable outputs.** Saying
  what you did *not* do, and why, is as useful as what you did.

---

## 11. Before you complete

1. Is the work **on the integration branch** or on a branch this ticket cannot merge? Say which.
2. Does every claim have **evidence read back from origin**, not from your working tree?
3. Did you **revert-check** any test you added?
4. Did you name the **file and line** for each fix, so the next reader can verify in seconds?
5. If a capability silently degraded instead of failing, did you fix the silence as well as the
   capability?
6. If you changed a Dockerfile or the build context, **did you actually build**? A merged change
   that breaks the build blocks every deploy.

---

## 12. A deploy is not complete until its smoke slice is green

Every production deploy plays the war-game **smoke slice** against the sha it just rolled out, and
every release to `main` plays the **full family year** first. The trigger, the fixed slice, the
scorecard location and what "green" means are in `how-to/war-game-cadence.md`; the rule in one
line:

> **A deploy ticket is not complete until its smoke slice is green** — `run.sh` exited `0`, the
> scorecard's delta line reads `No new findings, no regressions`, and no scenario in the slice is
> `BLOCKED`. A failed round blocks the deploy; the ticket stays open.

A release ticket's definition of done is the same with the full family year instead of the slice.
The deploy ticket's owner runs the round and pastes the scorecard path and delta line into the
ticket; there is no separate war-game owner. §11 applies to a war-game check as much as to a Go
test: revert-check it.
