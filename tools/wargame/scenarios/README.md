# War-game scenario corpus

One YAML file per scenario, in `tools/wargame/scenarios/`. This is the **same data every
round runs against**, which is what makes R4's findings comparable to R3's — until now each
round invented its scenarios in prose inside a ticket brief, so coverage was unmeasurable and
no two rounds could be lined up.

The corpus is transcribed, not invented: every R1–R3 scenario here was actually exercised in
that round, and `origin` records where. A passing scenario stays in the corpus as a regression
guard — it is not deleted when it starts passing.

**A round can also be written before it is played.** R4 (#5505) is the first: its eight
household-reality scenarios were designed against the seeded household and the shipped rules,
so `origin.source` names the ticket (`"#5505"`) instead of a `discoveries/` doc, `origin.round`
is `r4`, and every verdict starts `untested`. The validator enforces the pairing — a ticket as
`source` is allowed *only* with `untested`, because a `pass`/`partial`/`fail` with no round
doc behind it is a claim, not evidence. When R4 is played, a scenario's result is a `reruns`
entry (or, for one of these eight, its `origin.verdict` moves off `untested` and its `source`
becomes the round's doc).

## Fields

| Field | Required | What it is |
|---|---|---|
| `id` | yes | Stable kebab-case identifier, and the filename (`amber-store-checkoff` → `amber-store-checkoff.yaml`). Never reuse or rename an id: rounds and triage refer to it. |
| `persona` | yes | `amber` \| `jacob` \| `designer`. Amber is non-technical, on a phone, in the kitchen; Jacob is technical with heavy agent use; designer is the mobile usability/visual pass. |
| `surface` | yes | The page or flow, as one token: `calendar`, `money`, `lists`, `today`, `settings`, `search`, `activity`, `documents`, `notes`, `meals`, `people`, `notifications`, `guides`, `onboarding`, `auth`, `app-shell`. |
| `intent` | yes | What the persona is trying to do, in their own words — the sentence they would actually say. This is prose on purpose: it is the thing a driver is asked to satisfy. |
| `steps` | yes | The concrete actions, in order, executable by a driver: a `chat.sh` turn, an `mcpcall.py` read, a UI tap sequence. Quote anything containing `$` (see the quoting section of `../README.md`). |
| `assertion` | yes | What must be true afterwards, checked against ground truth. **This is what makes it a test rather than a demo.** If a scenario has no assertion a driver could fail, it is not a scenario. |
| `tags` | yes | 3–6 single-token tags (`money`, `ui`, `honesty`, `write`, `search-window`, …). Used to pick a slice of the corpus for a short round. |
| `origin` | yes | `round` (`r1`\|`r2`\|`r3`\|`r4`), `source` (the `discoveries/` doc — or, for a scenario written ahead of its round, the designing ticket such as `"#5505"`, which forces `verdict: untested`), `ref` (the row/turn/finding in it, or the brief's numbered item), `ticket`, `date`, `verdict` (`pass`\|`partial`\|`fail`\|`untested`). |
| `origin.reruns` | no | Later rounds that re-ran the same scenario: `round`, `source`, `ref`, `ticket`, `date`, `verdict`, `note`. This is the regression history — R4 adds one of these per scenario it re-runs. |
| `notes` | no | Anything a driver needs that does not fit the fields above. |

A scenario re-tested in a later round is **the same file with a new rerun entry**, never a
second file. `--report` prints both the originating verdict and the latest known verdict.

## The loader

`../scenarios_load.py` is the only reader. It validates every file against the schema above,
and reports duplicate ids, missing ids, a scenario that duplicates another (same persona,
surface and intent under a different id), and unknown fields.

```bash
cd tools/wargame
./scenarios_load.py                 # validate + the count report
./scenarios_load.py --check         # validate only; exit 1 if anything is wrong
./scenarios_load.py --report        # counts by persona, surface and originating round
./scenarios_load.py --ids           # one id per line, to feed a driver
./scenarios_load.py --json          # the whole corpus, for a driver that wants assertions too
./scenarios_load.py --persona amber --report
./scenarios_load.py --round r3 --ids
./scenarios_load.py --selftest      # parser and validator tests, no corpus needed
```

Exit status: `0` ok, `1` the corpus is invalid, `2` usage or a missing corpus directory.

It is **standard library only, with no PyYAML** — `tools/wargame` runs from a throwaway pod
that has `bash`, `curl` and `python3` and nothing else. The file therefore carries a small
strict YAML-subset parser: nested maps, block lists, `|` literal blocks, quoted and bare
scalars, comments. It raises on anything outside that subset rather than guessing, so a file
that needs more YAML than this is a signal to keep the format simple. `--selftest` proves the
parser and the validator against a good sample, each failure mode, and the real corpus.

## Adding a scenario for a new round

1. If the scenario already exists, add a `reruns` entry to it — do not create a second file.
2. If it is genuinely new, copy the shape of a neighbour, give it a stable id, and set
   `origin` to the round and finding that produced it — or, when the round has not been played
   yet, to the round and the *ticket* that designed it (`source: "#5505"`, `verdict: untested`).
3. Run `./scenarios_load.py --check`. A round's write-up should cite scenario ids, so
   `--report` and the round doc can be compared directly.
