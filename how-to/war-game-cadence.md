# War-game cadence — when a round runs, who triggers it, and what it blocks

The capability costs one command (`tools/wargame/run.sh`). This file says **when** it runs, so
nobody has to decide that again. The rule is stated as a trigger, not as a habit: a round happens
because a ticket type says it does, not because someone remembered.

If you are reading this in three months to find out whether you need to run a round: read §1, find
the ticket type you are working, and do what it says. You do not decide.

Related: `tools/wargame/README.md` (the harness), `how-to/wargame-round-runner.md` (run.sh, exit
codes, the delta), `how-to/wargame-scenario-corpus.md` (the corpus and how to add a scenario),
`how-to/worker-standing-instructions.md` (§12 carries the deploy rule).

---

## 1. When a round runs — the rule

| Round | Trigger | Scope | Command |
|---|---|---|---|
| **Smoke slice** | After **every** production deploy | A few scenarios, one per surface (the fixed list in §1a) | `./run.sh --ids <list>` |
| **Full family year** | Before **any** release to `main` | The whole corpus, unfiltered | `./run.sh` |

There is no third cadence. "Run one when we feel like it" is not a cadence, and a round played on
a whim is not comparable to anything, so it counts as neither.

### 1a. The smoke slice is a fixed list, not a sample

The slice is named, versioned in this file, and played as the same list every time — otherwise the
delta compares nothing. It is one scenario per surface, chosen for being the one that would notice
a broken deploy on that surface:

| Surface | Scenario |
|---|---|
| `money` | `amber-missed-setup-bill` |
| `money` | `amber-payee-contact` |
| `today` | `amber-today-page` |
| `lists` | `amber-lists-route` |
| `meals` | `amber-meal-plan-and-grocery` |
| `documents` | `amber-agent-find-filed-documents` |
| `search` | `amber-drawer-search` |
| `settings` | `amber-account-menu` |
| `activity` | `amber-activity-attribution` |
| `notifications` | `amber-notifications-sheet` |
| `auth` | `amber-back-after-login` |
| `app-shell` | `designer-320-overflow` |

```bash
cd tools/wargame
./run.sh --ids amber-missed-setup-bill,amber-payee-contact,amber-today-page,amber-lists-route,\
amber-meal-plan-and-grocery,amber-agent-find-filed-documents,amber-drawer-search,\
amber-account-menu,amber-activity-attribution,amber-notifications-sheet,\
amber-back-after-login,designer-320-overflow \
  --label "deploy-<sha>" --compare rounds/<previous-deploy-label>
```

`--ids` is validated against the corpus: a typo in an id makes `run.sh` exit `2` rather than
silently play a smaller slice. Never switch the slice to `--limit N`: the first N scenarios in
corpus order are not a slice, and the next round would compare against a different set.

**The slice grows as checks land.** A scenario only reaches PASS when it has a
`checks/<scenario-id>.py`, so a surface whose scenarios have no check is **BLOCKED**, not green
(see §4). The list above is the target. Today only `money` has checks
(`amber-missed-setup-bill`, `amber-payee-contact`), so the honest slice today is those two; a
surface joins the slice in the same ticket that writes its check. Do not remove a surface from the
list to make a round green — write the check, or record the surface as BLOCKED in the deploy
ticket (§4).

### 1b. The full family year

Before a release to `main`, play the whole corpus:

```bash
cd tools/wargame
./run.sh --label "year-<sha>" --compare rounds/<previous-year-label>
```

Unfiltered means every scenario in `tools/wargame/scenarios/` — the "family year" is the year of a
household's life the corpus encodes. `--round r4` is a legitimate narrower variant only when the
release ticket says so; an unfiltered run is the default and the only one that counts as the year.

### 1c. The environment gate is part of the round

`run.sh` runs `up.sh` first and refuses to start if it fails (exit `2`, no scorecard). A round
played against a half-up environment is not a round. There is no `--force`; repair the environment
and run again. `--skip-up` results are recorded as not comparable and **do not satisfy this
cadence**.

---

## 2. Who triggers it — the ticket types, in their definition of done

A round is triggered by the ticket, not by a person's memory. These lines belong in the ticket's
definition of done, and a ticket is not done until they are met:

| Ticket type | Name it as it appears in the plan | Definition of done includes |
|---|---|---|
| **Deploy ticket** | "Deploy `<sha>` to production" / "Deploy #<n> to production" — the ticket that rolls a `feat/family-hub` tip (or a release build) onto the homelab | The **smoke slice** (§1a) has been played against the deployed sha and is **green** (§3), with the scorecard path and its delta line in the ticket. |
| **Release ticket** | "Release `feat/family-hub` → `main`" — the final integration PR, the one that opens the PR to `main` | The **full family year** (§1b) has been played against the release candidate and is **green**, with the scorecard path and its delta line in the ticket. |

The owner is the person or agent who owns that ticket: the deploy ticket's owner plays the slice
(or dispatches a worker to) and records the result. There is no separate "war-game owner" — the
ticket that ships the code carries the round. A deploy ticket's owner who cannot run the round
because the environment is down has not completed the deploy; that is a blocker, not a waiver.

**Where the rule is inherited from:** the standing instructions
(`how-to/worker-standing-instructions.md` §12) carry the one-line deploy rule, so a brief written
from them inherits it. A deploy ticket whose brief does not mention the slice is an incomplete
brief, not an exemption.

---

## 3. Where the scorecard lives, and what the delta is compared against

**The scorecard** is written by `run.sh` to `$WARGAME_ROUNDS` (default
`tools/wargame/rounds/<label>/scorecard.md`), beside `evidence/<scenario-id>/` and the raw
`results.jsonl` / `verdicts.jsonl`. `rounds/` is **git-ignored**, so a scorecard is evidence in a
checkout, not a durable record — it disappears with the pod or the clean-up.

**The durable record** is therefore two things, both required:

1. **On the ticket** — the scorecard's path, its **delta line**, and the exit code. The delta line
   is one line at the top of the scorecard: `No new findings, no regressions`, or the list of
   scenarios whose verdict changed. Paste it; do not summarise it as "looks fine".
2. **In the KB deploy write-up** — `ops/deploy-<date>-sha-<sha>.md` (deploys) or the release
   write-up (releases) carries the final verdict and the scorecard path. That is the record that
   survives.

**What the delta compares against:** the previous round **of the same kind**, played on the same
slice.

| Round | Compare against |
|---|---|
| Smoke slice | The previous deploy's smoke slice — pass `--compare rounds/<previous-deploy-label>` explicitly; never let it fall back to "the newest round in the directory", which may be a year or a narrow filtered round |
| Full family year | The previous full family year — `--compare rounds/<previous-year-label>` |

Pass `--compare` explicitly for both. The default ("newest earlier round in `rounds/`") is fine for
exploratory rounds and wrong for a gate: a deploy that followed a year would otherwise compare its
12-scenario slice against 124 scenarios and report every unplayed scenario as absent.

Only scenarios this round played are compared — that is what makes a filtered slice's delta
meaningful. And a scenario played for the **first time** cannot be "unchanged": it is reported as
played for the first time and the round is explicitly **not** clean. So "no new findings" is a
checkable claim, not an assertion:

> **Green** = `run.sh` exited `0` **and** the scorecard's delta line reads
> `No new findings, no regressions` **and** no scenario in the slice is `BLOCKED`.

All three. Exit `0` alone is not green: a slice of scenarios with no checks exits `0` while
judging nothing.

---

## 4. What happens when a round fails — it blocks the deploy

**A failed round blocks the deploy. Plainly.** The deploy does not go out, the release does not go
to `main`, and the ticket stays open.

| Round result | What it means | What happens |
|---|---|---|
| exit `1`, a scenario `FAIL` | The product is broken on a surface the corpus covers | **Deploy blocked.** File a fix ticket for the failure; the deploy ticket waits on it. Re-run the slice after the fix. |
| exit `0`, all `PASS` | Green | Deploy proceeds. |
| exit `0`, some `BLOCKED` | The round could not judge those scenarios — no check, no driver, or "could not tell" (a `429`, a dead port-forward) | **Not green.** The deploy proceeds only if the deploy ticket records each `BLOCKED` scenario and its reason, and a ticket exists to give it a check or a driver. A `BLOCKED` reported as "green" is the failure this cadence exists to prevent. |
| exit `2` | The round never started: the environment gate failed, or the selection matched nothing | **Not a round.** The deploy is blocked until the environment is repaired and the slice is played. There is no scorecard to point at, so there is no evidence. |

A `BLOCKED` from "could not tell" is not a product failure and must not be written up as one — but
it is also not a pass. Say which it was.

A failure found by a round is **not** a reason to edit the assertion to match the behaviour. The
scenario's `intent` prose stays; the assertion changes only when the intended behaviour itself
changes, and that is a decision recorded in the ticket. The corpus's own README says the same:
"do not edit the prose of `intent` to match new behavior: change the assertion."

---

## 5. How the corpus grows — from real defects, not imagination

**Every real defect a family reports becomes a scenario.** A round is worth running because the
corpus is made of things that actually went wrong, not things someone imagined might.

The path from a bug report to a scenario, in order:

1. **The bug report arrives** — a family member reports it, or a round finds it. It is a ticket.
2. **The fix ticket writes the scenario**, in the same ticket as the fix (or in a follow-up named in
   its definition of done). The scenario is added to `tools/wargame/scenarios/<id>.yaml` with:
   - `origin.source` = the `discoveries/` write-up, or the ticket as `#<n>` — a ticket source is
     allowed only while `origin.verdict` is `untested`;
   - `origin.ref` = the row, turn or finding in that write-up;
   - `origin.ticket` = the bug's ticket, `origin.date`, `origin.verdict` = `untested` until played;
   - `assertion` = what the fixed behaviour must do, written so a `checks/<id>.py` can fail on it.
3. **The check** — `checks/<id>.py` — is written with the scenario. A scenario without a check is
   BLOCKED forever and protects nothing; the check is what turns the report into a regression
   guard. Revert-check it (standing instructions §5): put the defect back, watch it go red.
4. **It joins the slice** if it is on a surface that has no scenario in the smoke slice yet. That
   is the point: the slice grows out of lived experience, so the next deploy is tested against the
   last thing that broke.
5. **It stays.** A passing scenario is not deleted; it is a regression guard. A later round that
   re-runs it records a `reruns` entry on the same file — never a second file.

The corpus's own README (`tools/wargame/scenarios/README.md`) has the field-by-field format and
the loader's validation rules. Add the file, run `./scenarios_load.py --check`, then play it.

**Where a scenario gets added from a bug report:** in the bug's own fix ticket, as a file in
`tools/wargame/scenarios/` plus its `checks/<id>.py`, named in that ticket's definition of done.
Not in a separate "corpus maintenance" ticket — a defect that leaves no scenario behind will be
found again the expensive way.
