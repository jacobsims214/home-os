#!/usr/bin/env bash
# run.sh — play a whole war-game round and emit a scorecard. ONE command.
#
#   cd tools/wargame && ./run.sh                 # every scenario in the corpus
#   ./run.sh --filter money                      # only the money surface
#   ./run.sh --persona amber --surface money     # amber's money scenarios
#   ./run.sh --tag attention --round r4          # narrow to a slice
#   ./run.sh --filter autopay --limit 3          # a quick smoke round
#   ./run.sh --ids a,b,c                         # exactly these scenario ids
#
# Exit: 0 the round ran and nothing FAILED (PASS/BLOCKED only); 1 at least one
# scenario FAILED, so the round cannot gate a deploy; 2 the round never started
# (the environment gate failed, or the selection matched nothing).
#
# ## The four things this does, in order
#
# 1. **Gate on the environment.** `up.sh` runs first and the round refuses to
#    start if it exits non-zero. A round played against a half-up environment
#    produces lies, so there is no `--force`: fix the environment instead.
# 2. **Select scenarios** from the corpus through `scenarios_load.py`, the only
#    reader of `scenarios/*.yaml` — never a second YAML parser here.
# 3. **Execute each one** with `scenario_driver.py`, which runs the steps that
#    have a driver and records PASS/FAIL/BLOCKED plus evidence per scenario.
# 4. **Write `scorecard.md`**, including the delta against the previous round.
#
# ## The delta is the point
#
# A scorecard with no previous round to compare against is a baseline and says
# so. From the second round on, the top of the file is one line: either "no new
# findings, no regressions" — which is the signal that we are done — or the
# scenario ids that changed verdict, with their previous verdict. A round that
# cannot state its delta is noise.
#
# ## Where the evidence and the previous round live
#
# `$WARGAME_ROUNDS` (default `tools/wargame/rounds/`, git-ignored). Each round is
# a timestamped directory holding `scorecard.md` and `evidence/<scenario-id>/`.
# `--compare <dir>` picks the previous round explicitly; otherwise the newest
# earlier round in that directory is used. Running a round twice is safe: rounds
# are append-only directories and nothing outside them is written.
#
# Never prints a secret: credentials are read by up.sh and the drivers into 0600
# files or environment variables, never echoed. Never touches avware-prod or the
# GPU box.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE" || exit 2

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

ROUNDS_DIR="${WARGAME_ROUNDS:-$HERE/rounds}"
COMPARE=""
LABEL=""
FILTER=""
PERSONA=""
SURFACE=""
TAG=""
ROUND=""
IDS=""
LIMIT=0
SKIP_UP=0

usage() {
  sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --filter)     FILTER="${2:-}"; shift 2 ;;
    --filter=*)   FILTER="${1#--filter=}"; shift ;;
    --persona)    PERSONA="${2:-}"; shift 2 ;;
    --persona=*)  PERSONA="${1#--persona=}"; shift ;;
    --surface)    SURFACE="${2:-}"; shift 2 ;;
    --surface=*)  SURFACE="${1#--surface=}"; shift ;;
    --tag)        TAG="${2:-}"; shift 2 ;;
    --tag=*)      TAG="${1#--tag=}"; shift ;;
    --round)      ROUND="${2:-}"; shift 2 ;;
    --round=*)    ROUND="${1#--round=}"; shift ;;
    --ids)        IDS="${2:-}"; shift 2 ;;
    --ids=*)      IDS="${1#--ids=}"; shift ;;
    --limit)      LIMIT="${2:-0}"; shift 2 ;;
    --limit=*)    LIMIT="${1#--limit=}"; shift ;;
    --compare)    COMPARE="${2:-}"; shift 2 ;;
    --compare=*)  COMPARE="${1#--compare=}"; shift ;;
    --label)      LABEL="${2:-}"; shift 2 ;;
    --label=*)    LABEL="${1#--label=}"; shift ;;
    --rounds-dir) ROUNDS_DIR="${2:-}"; shift 2 ;;
    --skip-up)    SKIP_UP=1; shift ;;
    -h|--help)    usage ;;
    *) echo "run.sh: unknown argument: $1" >&2; usage ;;
  esac
done

for tool in python3 jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "run.sh: missing required tool: $tool" >&2; exit 2; }
done

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
[ -n "$LABEL" ] || LABEL="$STAMP"
ROUND_DIR="$ROUNDS_DIR/$LABEL"

echo "wargame run.sh — playing a round"
echo "  corpus    $HERE/scenarios"
echo "  rounds    $ROUNDS_DIR"
echo "  round     $LABEL"
echo

# ---------------------------------------------------------------------------
# 1. The environment gate. No --force: a round against a half-up environment is
#    a lie, and the whole point of up.sh is that it cannot report green when it
#    could not tell. If it fails, the round does not start.
# ---------------------------------------------------------------------------

if [ "$SKIP_UP" = "1" ]; then
  echo "GATE: skipped (--skip-up). This round's results are NOT comparable to a gated round."
  echo
else
  echo "GATE: up.sh"
  up_out="$(timeout 420 ./up.sh 2>&1)"; up_rc=$?
  # Print the table, but never anything else up.sh might emit.
  printf '%s\n' "$up_out" | sed 's/^/  /'
  echo
  if [ "$up_rc" -ne 0 ]; then
    echo "REFUSING TO START: up.sh exited $up_rc. The environment is not verified-ready," >&2
    echo "so a round now would produce lies. Fix the rows above (README 'Verifying the" >&2
    echo "environment'), then run this again." >&2
    exit 2
  fi
  echo "GATE: passed."
  echo
fi

# ---------------------------------------------------------------------------
# 2. Select the scenarios. scenarios_load.py owns the corpus format; this only
#    asks it which ids to play.
# ---------------------------------------------------------------------------

mkdir -p "$ROUND_DIR/evidence" || { echo "run.sh: cannot create $ROUND_DIR" >&2; exit 2; }

if [ -n "$IDS" ]; then
  # Explicit ids are validated against the corpus: a typo must not silently
  # shrink a round to nothing.
  all_ids="$(./scenarios_load.py --ids)" || { echo "run.sh: corpus failed to load" >&2; exit 2; }
  sel=""
  for want in $(printf '%s' "$IDS" | tr ',' ' '); do
    if printf '%s\n' "$all_ids" | grep -qxF "$want"; then
      sel="${sel}${want}"$'\n'
    else
      echo "run.sh: --ids names '$want', which is not in the corpus" >&2
      exit 2
    fi
  done
else
  # Selection goes through scenarios_load.py — the only reader of the corpus —
  # for --persona and --round, which it owns. --surface, --tag and --filter
  # narrow the JSON it already produced, so the corpus stays the single source
  # of truth for every field and this script never parses YAML.
  #
  # NOTE (bash 3.2): an empty array cannot be expanded under `set -u`
  # ("largs[@]: unbound variable"), so the loader argv is built as a plain
  # string and only split when it is non-empty. Getting this wrong made
  # --filter silently fall into a python JSONDecodeError.
  largs=""
  [ -n "$PERSONA" ] && largs="$largs --persona $PERSONA"
  [ -n "$ROUND" ]   && largs="$largs --round $ROUND"

  if [ -n "$SURFACE" ] || [ -n "$TAG" ] || [ -n "$FILTER" ]; then
    # shellcheck disable=SC2086  # intentional word-splitting of the flag string
    sel="$(./scenarios_load.py $largs --json | python3 -c '
import json, sys
surface, tag, needle = sys.argv[1], sys.argv[2], sys.argv[3].lower()
try:
    docs = json.load(sys.stdin)["scenarios"]
except Exception as exc:
    print("run.sh: could not read the corpus JSON: %s" % exc, file=sys.stderr)
    raise SystemExit(2)
for d in docs:
    if surface and d["surface"] != surface:
        continue
    if tag and tag not in d["tags"]:
        continue
    hay = " ".join([d["id"], d["surface"], " ".join(d["tags"]), d["intent"]]).lower()
    if needle and needle not in hay:
        continue
    print(d["id"])
' "$SURFACE" "$TAG" "$FILTER")" || { echo "run.sh: filter failed" >&2; exit 2; }
  else
    # shellcheck disable=SC2086
    sel="$(./scenarios_load.py $largs --ids)" || {
      echo "run.sh: corpus failed to load" >&2; exit 2; }
  fi
fi

sel="$(printf '%s' "$sel" | grep -v '^$' || true)"
total_sel="$(printf '%s\n' "$sel" | grep -c . || true)"
if [ "$total_sel" -eq 0 ]; then
  echo "run.sh: the filters matched no scenario (persona='$PERSONA' surface='$SURFACE' tag='$TAG' filter='$FILTER')." >&2
  echo "run.sh: refusing to emit a scorecard for an empty round — that would read as 'nothing wrong'." >&2
  exit 2
fi
if [ "$LIMIT" -gt 0 ] 2>/dev/null; then
  sel="$(printf '%s\n' "$sel" | head -n "$LIMIT")"
fi
n_sel="$(printf '%s\n' "$sel" | grep -c . || true)"
echo "SELECT: $n_sel of $total_sel matching scenario(s)"
printf '%s\n' "$sel" | sed 's/^/  /'
echo

# ---------------------------------------------------------------------------
# 3. Execute. Agent turns are capped at 2 wide (R2 proved 5-wide degrades
#    results); UI drivers may be wider, and they are the only thing that
#    parallelises here, so the cap is honoured by the driver, not by this loop.
#    One scenario at a time is the safe default: several drivers write to the
#    same household, and a round must not race itself.
# ---------------------------------------------------------------------------

RESULTS="$ROUND_DIR/results.jsonl"
: > "$RESULTS"
FAILS=0
BLOCKED=0
PASSES=0
i=0
while IFS= read -r sid; do
  [ -n "$sid" ] || continue
  i=$((i + 1))
  printf '[%d/%d] %s ... ' "$i" "$n_sel" "$sid"
  line="$(timeout 600 ./scenario_driver.py --id "$sid" --evidence-dir "$ROUND_DIR/evidence" 2>&1)"
  drc=$?
  if [ "$drc" -ne 0 ] || [ -z "$line" ]; then
    # A driver that crashed is a FAIL, never a silent skip (§7).
    printf 'DRIVER-CRASH\n'
    printf '{"id":"%s","persona":"unknown","surface":"unknown","verdict":"FAIL","basis":"scenario_driver.py exited %d: %s","tags":[],"steps":[]}\n' \
      "$sid" "$drc" "$(printf '%s' "$line" | tail -1 | sed 's/"/\\"/g' | cut -c1-300)" >> "$RESULTS"
    FAILS=$((FAILS + 1))
    continue
  fi
  printf '%s\n' "$line" | tail -1 >> "$RESULTS"
  v="$(printf '%s' "$line" | tail -1 | jq -r '.verdict // "FAIL"')"
  case "$v" in
    PASS)    PASSES=$((PASSES + 1));    printf 'PASS\n' ;;
    BLOCKED) BLOCKED=$((BLOCKED + 1));  printf 'BLOCKED\n' ;;
    *)       FAILS=$((FAILS + 1));      printf 'FAIL\n' ;;
  esac
done <<< "$sel"

echo
echo "ROUND: $n_sel scenario(s) — PASS $PASSES, FAIL $FAILS, BLOCKED $BLOCKED"
echo

# ---------------------------------------------------------------------------
# 4. The scorecard, with its delta.
# ---------------------------------------------------------------------------

# Find the previous round: --compare wins, else the newest earlier round dir
# that actually has a scorecard.
prev_dir=""
if [ -n "$COMPARE" ]; then
  if [ -f "$COMPARE/scorecard.md" ] || [ -f "$COMPARE/results.jsonl" ]; then
    prev_dir="$COMPARE"
  else
    echo "run.sh: --compare $COMPARE has no scorecard.md or results.jsonl" >&2
    exit 2
  fi
else
  prev_dir="$(ls -1d "$ROUNDS_DIR"/*/ 2>/dev/null | sed 's:/*$::' | grep -v "^$ROUND_DIR\$" \
              | while read -r d; do [ -f "$d/results.jsonl" ] && echo "$d"; done | sort | tail -1)"
fi

PREV_DIR="$prev_dir" ROUND_DIR="$ROUND_DIR" LABEL="$LABEL" \
PASSES="$PASSES" FAILS="$FAILS" BLOCKED="$BLOCKED" N_SEL="$n_sel" \
FILTER="$FILTER" PERSONA="$PERSONA" SURFACE="$SURFACE" TAG="$TAG" ROUNDF="$ROUND" \
LIMIT="$LIMIT" SKIP_UP="$SKIP_UP" HERE="$HERE" IDS="$IDS" \
python3 - <<'PY'
import json, os, sys, time

env = os.environ
round_dir = env["ROUND_DIR"]
prev_dir = env.get("PREV_DIR", "")
here = env["HERE"]

def load(path):
    out = {}
    if not path or not os.path.exists(path):
        return out
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                d = json.loads(line)
            except json.JSONDecodeError:
                continue
            if d.get("id"):
                out[d["id"]] = d
    return out

cur = load(os.path.join(round_dir, "results.jsonl"))
prev = load(os.path.join(prev_dir, "results.jsonl")) if prev_dir else {}

def counts(docs):
    c = {"PASS": 0, "FAIL": 0, "BLOCKED": 0}
    for d in docs.values():
        c[d.get("verdict", "FAIL")] = c.get(d.get("verdict", "FAIL"), 0) + 1
    return c

def tally(docs, key):
    t = {}
    for d in docs.values():
        k = d.get(key) or "unknown"
        t.setdefault(k, {"PASS": 0, "FAIL": 0, "BLOCKED": 0})
        t[k][d.get("verdict", "FAIL")] = t[k].get(d.get("verdict", "FAIL"), 0) + 1
    return dict(sorted(t.items()))

cur_c, prev_c = counts(cur), counts(prev)

# --- delta ---------------------------------------------------------------
# Compare only the scenarios this round played, so a filtered round is compared
# on its own slice and not blamed for the scenarios it never touched.
new_fail, regressed, fixed, still_blocked, graduated, appeared = [], [], [], [], [], []
for sid, d in cur.items():
    v = d.get("verdict", "FAIL")
    if sid not in prev:
        appeared.append(sid)
        if v == "FAIL":
            new_fail.append(sid)
        continue
    p = prev[sid].get("verdict", "FAIL")
    if v == p:
        if v == "BLOCKED":
            still_blocked.append(sid)
        continue
    if v == "FAIL":
        regressed.append((sid, p))
    elif p == "FAIL" and v != "FAIL":
        fixed.append((sid, v))
    elif p == "BLOCKED" and v == "PASS":
        graduated.append(sid)

# A scenario played for the FIRST time is not "unchanged": it was never compared.
# Calling that clean is a plausible wrong answer (§7), so `clean` requires that
# every scenario this round also appears in the previous round.
compared = [sid for sid in cur if sid in prev]
uncompared = [sid for sid in cur if sid not in prev]
clean = (not new_fail and not regressed and not fixed and not graduated
         and not uncompared)

lines = []
A = lines.append
A("# War-game round %s" % env["LABEL"])
A("")
A("Generated by `tools/wargame/run.sh` at %s UTC."
  % time.strftime("%Y-%m-%d %H:%M:%S", time.gmtime()))
A("")
sel_desc = []
if env.get("IDS"):
    sel_desc.append("--ids %s" % env["IDS"])
for k, flag in (("persona", "--persona"), ("surface", "--surface"),
                ("tag", "--tag"), ("filter", "--filter"), ("round", "--round")):
    if env.get(k.upper() if k != "round" else "ROUNDF"):
        sel_desc.append("%s %s" % (flag, env[k.upper() if k != "round" else "ROUNDF"]))
if env.get("LIMIT") and env["LIMIT"] != "0":
    sel_desc.append("--limit %s" % env["LIMIT"])
A("Selection: %s" % (", ".join(sel_desc) if sel_desc else "the whole corpus"))
A("")
if env.get("SKIP_UP") == "1":
    A("> **The environment gate was skipped (`--skip-up`).** These results are not")
    A("> comparable to a gated round: `up.sh` never confirmed the environment.")
    A("")

# --- the one-line delta, at the top because it is the point -----------------
A("## Delta")
A("")
if not prev_dir:
    A("**Baseline round — no previous scorecard to compare against.** Every verdict")
    A("below is new by definition; the next round is the first one that can state a delta.")
else:
    A("Against `%s`." % os.path.basename(prev_dir.rstrip("/")))
    A("")
    if clean:
        A("**No new findings, no regressions.** All %d scenario(s) this round played"
          % len(cur))
        A("verdict-for-verdict the same as last round. If that holds again, we are done.")
    elif not regressed and not new_fail and not fixed and not graduated and uncompared:
        A("**%d scenario(s) played for the first time; the other %d are unchanged.**"
          % (len(uncompared), len(compared)))
        A("")
        A("This is not a clean round: a scenario with no previous verdict cannot")
        A("regress, so it cannot contribute to \"we are done\" yet. Played first time:")
        for sid in sorted(uncompared):
            A("- `%s` — %s" % (sid, cur[sid].get("verdict")))
    else:
        bits = []
        if regressed:
            bits.append("%d regressed" % len(regressed))
        if new_fail:
            bits.append("%d new failure(s)" % len(new_fail))
        if fixed:
            bits.append("%d fixed" % len(fixed))
        if graduated:
            bits.append("%d graduated BLOCKED->PASS" % len(graduated))
        if appeared:
            bits.append("%d played for the first time" % len(appeared))
        A("**%s.**" % ", ".join(bits))
        A("")
        if regressed:
            A("Regressed (was → now):")
            for sid, p in sorted(regressed):
                A("- `%s` — %s → FAIL" % (sid, p))
            A("")
        if new_fail:
            A("New failures:")
            for sid in sorted(new_fail):
                A("- `%s` — %s" % (sid, cur[sid].get("basis", "")[:160]))
            A("")
        if fixed:
            A("Fixed (was FAIL):")
            for sid, v in sorted(fixed):
                A("- `%s` — FAIL → %s" % (sid, v))
            A("")
        if graduated:
            A("Graduated from BLOCKED to PASS (an assertion check now exists and passes):")
            for sid in sorted(graduated):
                A("- `%s`" % sid)
            A("")

# --- counts ---------------------------------------------------------------
A("## Counts")
A("")
A("| Verdict | This round | Previous |")
A("|---|---|---|")
for v in ("PASS", "FAIL", "BLOCKED"):
    A("| %s | %d | %s |" % (v, cur_c.get(v, 0),
                            prev_c.get(v, 0) if prev_dir else "—"))
A("| **total** | **%d** | **%s** |" % (len(cur), len(prev) if prev_dir else "—"))
A("")

def table(title, key):
    A("### By %s" % title)
    A("")
    A("| %s | PASS | FAIL | BLOCKED | total |" % title)
    A("|---|---|---|---|---|")
    for name, c in tally(cur, key).items():
        A("| %s | %d | %d | %d | %d |"
          % (name, c.get("PASS", 0), c.get("FAIL", 0), c.get("BLOCKED", 0), sum(c.values())))
    A("")

table("persona", "persona")
table("surface", "surface")

# --- per-scenario ---------------------------------------------------------
A("## Scenarios")
A("")
A("| Scenario | Persona | Surface | Verdict | Why | Evidence |")
A("|---|---|---|---|---|---|")
for sid in sorted(cur):
    d = cur[sid]
    # The driver always writes evidence/<id>/ next to the scorecard, whether or
    # not it produced screenshots. Name it so a reader can go and look, and say
    # plainly when the directory is missing rather than printing a bare dash.
    ev = d.get("evidence_dir") or os.path.join(round_dir, "evidence", sid)
    if os.path.isdir(ev):
        rel = os.path.relpath(ev, round_dir)
    else:
        rel = "MISSING"
    basis = (d.get("basis") or "").replace("|", "\\|")[:120]
    A("| `%s` | %s | %s | %s | %s | `%s` |"
      % (sid, d.get("persona", "?"), d.get("surface", "?"),
         d.get("verdict", "?"), basis, rel))
A("")

# --- what BLOCKED means, so nobody reads it as a pass ---------------------
nb = cur_c.get("BLOCKED", 0)
if nb:
    A("## What BLOCKED means")
    A("")
    A("%d of %d scenarios are BLOCKED. That is not a pass and not a failure: it means"
      % (nb, len(cur)))
    A("the round could not judge the scenario's assertion. The usual reason is that no")
    A("assertion check exists yet at `checks/<scenario-id>.py` — the scenario's prose")
    A("assertion is real, but nothing executed it. A BLOCKED scenario is a task to")
    A("write that check, and the next round shows it graduating.")
    A("")

with open(os.path.join(round_dir, "scorecard.md"), "w", encoding="utf-8") as fh:
    fh.write("\n".join(lines) + "\n")

# machine-readable twin, so the next round diffs data and not prose
with open(os.path.join(round_dir, "verdicts.json"), "w", encoding="utf-8") as fh:
    json.dump({sid: {"verdict": d.get("verdict"), "persona": d.get("persona"),
                     "surface": d.get("surface"), "basis": d.get("basis")}
               for sid, d in cur.items()}, fh, indent=1, sort_keys=True)

print("scorecard: %s" % os.path.join(round_dir, "scorecard.md"))
if prev_dir:
    print("delta vs:  %s" % prev_dir)
    if clean:
        print("DELTA:     no new findings, no regressions (%d scenarios unchanged)" % len(cur))
    elif not regressed and not new_fail and not fixed and not graduated and uncompared:
        print("DELTA:     %d new scenario(s) played for the first time, %d unchanged"
              % (len(uncompared), len(compared)))
    else:
        print("DELTA:     %d regressed, %d new failure(s), %d fixed, %d graduated, %d new"
              % (len(regressed), len(new_fail), len(fixed), len(graduated), len(appeared)))
else:
    print("DELTA:     baseline round (no previous scorecard)")
PY

# ---------------------------------------------------------------------------
# 5. The gate: a round with any FAIL exits non-zero so it can stop a deploy.
# ---------------------------------------------------------------------------

SCORECARD="$ROUND_DIR/scorecard.md"
[ -f "$SCORECARD" ] || { echo "run.sh: scorecard was not written" >&2; exit 2; }

echo
if [ "$FAILS" -ne 0 ]; then
  echo "GATE: FAILED — $FAILS scenario(s) FAILED. Scorecard: $SCORECARD"
  exit 1
fi
echo "GATE: passed — no scenario FAILED ($PASSES PASS, $BLOCKED BLOCKED)."
echo "Scorecard: $SCORECARD"
exit 0
