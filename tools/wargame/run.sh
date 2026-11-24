#!/usr/bin/env bash
# run.sh — play a whole war-game round and emit a scorecard. ONE command.
#
#   cd tools/wargame && ./run.sh                 # every scenario in the corpus
#   ./run.sh --filter money                      # only the money surface
#   ./run.sh --persona amber --surface money     # amber's money scenarios
#   ./run.sh --tag attention --round r4          # narrow to a slice
#   ./run.sh --ids a,b,c                         # exactly these scenario ids
#   ./run.sh --month 3                           # one month of the family year
#   ./run.sh --month 1 --month 2                 # a two-month slice
#   ./run.sh --season spring                     # March, April, May
#   ./run.sh --year --label family-year-r4        # the whole year, resumable
#
# Exit: 0 the round ran and nothing FAILED and no channel disagreed; 1 at least one
# scenario FAILED or reported PARITY_MISMATCH, so the round cannot gate a deploy; 2 the
# round never started (the environment gate failed, or the selection matched nothing).
#
# ## The five things this does, in order
#
# 1. **Gate on the environment.** `up.sh` runs first and the round refuses to
#    start if it exits non-zero. A round played against a half-up environment
#    produces lies, so there is no `--force`: fix the environment instead.
# 2. **Select scenarios** from the corpus through `scenarios_load.py`, the only
#    reader of `scenarios/*.yaml` — never a second YAML parser here. A month or a
#    season is a directory in the corpus, so selection cannot drift from the calendar.
# 3. **Plan, then checkpoint per month.** The plan names every scenario's month, season
#    and `channel`. Each month's results are written as its own file and marked DONE;
#    re-running the same command continues where it stopped. A year is long, and a round
#    that loses eleven months to one timeout is a round nobody runs twice.
# 4. **Execute both channels, then compare them.** For `channel: both`, the agent is
#    driven (`scenario_driver.py`: mcpcall/chat) AND the UI is rendered
#    (`ui_channel.mjs`: Chrome DevTools at 390x844 as the same persona), then
#    `parity_probe.py` compares them on the scenario's `parity` fact. A disagreement is
#    its own verdict — **PARITY_MISMATCH** — which is not the same finding as FAIL: "the
#    product is wrong" and "the two channels tell different stories" have different
#    fixes. `agent`-only and `ui`-only scenarios run just that channel and say so.
# 5. **Write `scorecard.md`**, per month and per surface, with the PARITY_MISMATCH count
#    and the delta against the previous round.
#
# ## Why parity is a separate verdict
#
# The year corpus's 36 `channel: both` scenarios each name one `parity` fact — an
# amount, a date, a count — that the agent's answer and the rendered page must state
# identically. Two channels agreeing is *not* proof they are right: both can read the
# same stale cache. So where a probe offers ground truth (`truth()`), the round also
# cross-checks the agent's value against the database or the tool payload, and reports
# TRUTH_MISMATCH separately. An agent and a UI that agree with each other and are both
# wrong is exactly the case this catches.
#
# ## A month that BLOCKS does not stop the year
#
# One broken area must not hide the other eleven. A month whose scenarios all BLOCK is
# recorded as BLOCKED in the scorecard and the round **continues**. The exit code still
# reports it (BLOCKED is not a pass), but the remaining months are played.
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
# a named directory holding `scorecard.md`, `plan.json`, `months/<NN-month>/` and
# `evidence/<scenario-id>/`. `--compare <dir>` picks the previous round explicitly;
# otherwise the newest earlier round in that directory is used. `--label` names the
# round, which is also what makes `--resume` work: re-running the same `--label`
# continues a round that was interrupted.
#
# Never prints a secret: credentials are read by up.sh and the drivers into 0600
# files or environment variables, never echoed. Never touches avware-prod or the
# GPU box.
#
# ## Width
#
# Agent turns are at most **2 wide** (R2 proved 5-wide degrades results) and that is
# honoured by running one scenario's agent channel at a time. UI drivers are not agent
# turns and may be wider; `UI_JOBS` (default 2) paces them.

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
MONTHS=""
SEASON=""
YEAR_ONLY=0
RESUME=1
NO_RESUME=0
CHANNEL=""

usage() {
  sed -n '2,80p' "$0" | sed 's/^# \{0,1\}//'
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
    --month)      MONTHS="$MONTHS${2:-} "; shift 2 ;;
    --month=*)    MONTHS="$MONTHS${1#--month=} "; shift ;;
    --season)     SEASON="${2:-}"; shift 2 ;;
    --season=*)   SEASON="${1#--season=}"; shift ;;
    --year)       YEAR_ONLY=1; shift ;;
    --channel)    CHANNEL="${2:-}"; shift 2 ;;
    --channel=*)  CHANNEL="${1#--channel=}"; shift ;;
    --no-resume)  NO_RESUME=1; shift ;;
    --skip-up)    SKIP_UP=1; shift ;;
    -h|--help)    usage ;;
    *) echo "run.sh: unknown argument: $1" >&2; usage ;;
  esac
done

for tool in python3 jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "run.sh: missing required tool: $tool" >&2; exit 2; }
done
# The UI channel needs node; it is only required when a UI channel will run, which is
# most rounds, but a round of pure agent scenarios must not fail for want of it.
UI_JOBS="${WARGAME_UI_JOBS:-2}"

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
# 2. Plan the round. scenarios_load.py owns the corpus format; this only asks it
#    which ids to play and what each one needs.
# ---------------------------------------------------------------------------

largs=""
[ -n "$PERSONA" ] && largs="$largs --persona $PERSONA"
[ -n "$ROUND" ]   && largs="$largs --round $ROUND"
[ -n "$SEASON" ]  && largs="$largs --season $SEASON"
[ "$YEAR_ONLY" = "1" ] && largs="$largs --year"
for m in $MONTHS; do largs="$largs --month $m"; done

# The whole corpus as JSON once, with the selection applied by the loader. Narrowing
# by surface/tag/filter happens on this JSON, so every field (channel, parity, month,
# season) stays owned by the loader and this script never parses YAML.
# shellcheck disable=SC2086  # intentional word-splitting of the flag string
plan_json="$(./scenarios_load.py $largs --json)" || { echo "run.sh: corpus failed to load" >&2; exit 2; }

plan="$(printf '%s' "$plan_json" | python3 -c '
import json, sys
surface, tag, needle, ids, limit, channel = (sys.argv[1], sys.argv[2], sys.argv[3].lower(),
                                             sys.argv[4], sys.argv[5], sys.argv[6])
try:
    docs = json.load(sys.stdin)["scenarios"]
except Exception as exc:
    print("run.sh: could not read the corpus JSON: %s" % exc, file=sys.stderr)
    raise SystemExit(2)

want = [t.strip() for t in ids.split(",") if t.strip()] if ids else []
known = {d["id"] for d in docs}
if want:
    bad = [w for w in want if w not in known]
    if bad:
        print("run.sh: --ids names %s, which is not in the selection" % ", ".join(bad),
              file=sys.stderr)
        raise SystemExit(2)
    docs = [d for d in docs if d["id"] in want]

sel = []
for d in docs:
    if surface and d["surface"] != surface:
        continue
    if tag and tag not in d["tags"]:
        continue
    if channel and (d.get("channel") or "agent") != channel:
        continue
    if needle:
        hay = " ".join([d["id"], d["surface"], " ".join(d["tags"]), d["intent"],
                        d.get("month") or "", d.get("season") or ""]).lower()
        if needle not in hay:
            continue
    sel.append(d)

if limit and int(limit) > 0:
    sel = sel[:int(limit)]

# Month is the checkpoint key. A scenario outside the year corpus has no month: it
# goes in a pseudo-month named after its surface group so an ordinary corpus round
# still has one bucket and the per-month section is not a lie about a calendar.
for d in sel:
    d["_ckpt"] = d.get("month") or "_flat"
    d["_season"] = d.get("season") or ""
    d["_channel"] = d.get("channel") or "agent"
print(json.dumps(sel))
' "$SURFACE" "$TAG" "$FILTER" "$IDS" "$LIMIT" "$CHANNEL")" || exit 2

n_sel="$(printf '%s' "$plan" | jq 'length')"
total_sel="$(printf '%s' "$plan_json" | jq '.scenarios | length')"
if [ "$n_sel" -eq 0 ]; then
  echo "run.sh: the filters matched no scenario (persona='$PERSONA' surface='$SURFACE' tag='$TAG' filter='$FILTER' months='$MONTHS' season='$SEASON' channel='$CHANNEL')." >&2
  echo "run.sh: refusing to emit a scorecard for an empty round — that would read as 'nothing wrong'." >&2
  exit 2
fi

# The months this round will play, in calendar order, and how many scenarios each has.
ckpts="$(printf '%s' "$plan" | jq -r 'group_by(._ckpt) | map({k: .[0]._ckpt, s: .[0]._season, n: length}) | sort_by(.k) | .[] | "\(.k)\t\(.s)\t\(.n)"')"

mkdir -p "$ROUND_DIR/evidence" || { echo "run.sh: cannot create $ROUND_DIR" >&2; exit 2; }
printf '%s\n' "$plan" | jq '.' > "$ROUND_DIR/plan.json"

echo "SELECT: $n_sel of $total_sel matching scenario(s), in $(printf '%s\n' "$ckpts" | grep -c .) checkpoint(s)"
printf '%s' "$plan" | jq -r '.[] | "  \(._ckpt)  \(._channel)  \(.id)"'
echo

# ---------------------------------------------------------------------------
# 3. Execute by checkpoint, so a long round is resumable.
#
#    Each month writes months/<ckpt>/results.jsonl and, once every scenario in it
#    has a result, months/<ckpt>/DONE. A re-run with the same --label skips a month
#    that is DONE and re-plays one that is not — so a timeout in month 7 does not
#    throw away January to June. --no-resume replays everything.
# ---------------------------------------------------------------------------

MONTHS_DIR="$ROUND_DIR/months"
mkdir -p "$MONTHS_DIR"

# What a channel needs, resolved once so every scenario is judged the same way.
ui_ready=0
if command -v node >/dev/null 2>&1; then ui_ready=1; fi

# The UI channel's credential is the persona's id_token, minted by dex_login.py from
# the Secret that up.sh already proved signs in. Minted lazily, once per persona per
# round, and cached for its 24h life — Envoy limits /dex/auth* at 10/min, so a second
# sign-in inside a minute reads as a spurious 429 mid-round.
ui_token() {  # ui_token <persona> -> echoes the token FILE path, or nothing
  local p="$1" d
  d="${WARGAME_DEX_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/homeos-wargame/dex}"
  if [ -s "$d/$p.token" ]; then printf '%s' "$d/$p.token"; return 0; fi
  local f
  f="$(timeout 180 ./dex_login.py --persona "$p" --print-token-file 2>/dev/null)" || return 1
  [ -s "$f" ] && printf '%s' "$f"
}

PASSES=0
FAILS=0
BLOCKED=0
MISMATCH=0
TRUTHBAD=0
SKIPPED_MONTHS=0
REPLAYED_MONTHS=0
RESULTS="$ROUND_DIR/results.jsonl"
: > "$RESULTS"

while IFS=$'\t' read -r ckpt season n_in; do
  [ -n "$ckpt" ] || continue
  mdir="$MONTHS_DIR/$ckpt"
  mres="$mdir/results.jsonl"
  mkdir -p "$mdir/evidence"

  if [ "$NO_RESUME" != "1" ] && [ -f "$mdir/DONE" ] && [ -s "$mres" ]; then
    echo "CHECKPOINT $ckpt: DONE from an earlier run of this label — not replayed"
    REPLAYED_MONTHS=$((REPLAYED_MONTHS + 1))
    cat "$mres" >> "$RESULTS"
    continue
  fi

  echo "CHECKPOINT $ckpt ${season:+($season)}: $n_in scenario(s)"
  : > "$mres"
  m_pass=0; m_fail=0; m_block=0; m_mm=0; m_truth=0
  i=0

  # One scenario at a time is the safe default: several drivers write to the same
  # household, and a round must not race itself. This is also what keeps the AGENT
  # channel at 1 wide, inside the 2-wide cap R2 set.
  printf '%s' "$plan" | jq -c --arg c "$ckpt" '.[] | select(._ckpt == $c)' | while IFS= read -r doc; do
    sid="$(printf '%s' "$doc" | jq -r '.id')"
    persona="$(printf '%s' "$doc" | jq -r '.persona')"
    surface="$(printf '%s' "$doc" | jq -r '.surface')"
    channel="$(printf '%s' "$doc" | jq -r '._channel')"
    parity="$(printf '%s' "$doc" | jq -r '.parity // ""')"
    evid="$mdir/evidence/$sid"
    mkdir -p "$evid"
    i=$((i + 1))
    printf '  [%d/%d] %-42s %-5s ... ' "$i" "$n_in" "$sid" "$channel"

    # ---- agent channel ----------------------------------------------------
    a_verdict="SKIPPED"; a_basis=""; a_file=""
    if [ "$channel" = "agent" ] || [ "$channel" = "both" ]; then
      # --channel agent: the `open <url>` steps belong to the UI channel below. Without
      # it this driver renders them with no session, sees the login screen, and turns a
      # two-channel scenario BLOCKED for a reason that is not about the product.
      a_out="$(timeout 600 ./scenario_driver.py --id "$sid" --channel agent \
                 --evidence-dir "$mdir/evidence" 2>&1)"
      a_rc=$?
      # scenario_driver.py prints the result line last; everything before it is log.
      a_file="$mdir/evidence/$sid/result.json"
      if [ "$a_rc" -ne 0 ] || [ ! -s "$a_file" ]; then
        # A driver that crashed is a FAIL, never a silent skip (§7).
        a_verdict="FAIL"
        a_basis="scenario_driver.py exited $a_rc: $(printf '%s' "$a_out" | tail -1 | cut -c1-200)"
        printf '{"id":"%s","persona":"%s","surface":"%s","verdict":"FAIL","basis":"%s","channel":"%s","month":"%s","season":"%s","agent_ran":true}\n' \
          "$sid" "$persona" "$surface" "$(printf '%s' "$a_basis" | sed 's/"/\\"/g')" "$channel" "$ckpt" "$season" > "$a_file"
      else
        a_verdict="$(jq -r '.verdict // "FAIL"' "$a_file")"
        a_basis="$(jq -r '.basis // ""' "$a_file")"
      fi
    fi

    # ---- UI channel -------------------------------------------------------
    u_verdict="SKIPPED"; u_basis=""; u_detail=""
    if [ "$channel" = "ui" ] || [ "$channel" = "both" ]; then
      if [ "$ui_ready" != "1" ]; then
        u_verdict="BLOCKED"; u_basis="the UI channel needs node (Chrome DevTools driver); not on PATH"
      else
        # The scenario names the page a person would look at; take the first `open
        # <url>` step. Where it names none, the UI half cannot be driven and says so
        # rather than quietly comparing against nothing.
        # The page the fact is DRAWN on wins over the page the corpus's `open` step
        # names: that step is where a person would start, and for a nav hub like
        # /dashboard/settings it renders none of the data the parity fact is about. The
        # probe owns the comparison, so it owns the page; the corpus is not edited.
        ui_url="$(timeout 30 ./parity_probe.py --id "$sid" --ui-path 2>/dev/null || true)"
        if [ -n "$ui_url" ]; then
          ui_url="https://homeos.simstech.cloud${ui_url}"
          ui_note="the probe's UI_PATH"
        else
          ui_url="$(printf '%s' "$doc" | jq -r '[.steps[] | capture("open\\s+(?<u>https?://[^\\s]*)").u] | first // ""' | sed 's/[.,]$//')"
          ui_note="the scenario's first \`open\` step"
        fi
        if [ -z "$ui_url" ]; then
          u_verdict="BLOCKED"
          u_basis="the scenario names no \`open <url>\` step, so there is no page to render"
        else
          tokfile="$(ui_token "$persona")"
          if [ -z "$tokfile" ]; then
            u_verdict="BLOCKED"
            u_basis="could not mint a Dex id_token for $persona (see dex_login.py); the UI " \
                    "channel cannot reach a signed-in page"
          else
            WARGAME_TOKEN_FILE="$tokfile" timeout 120 node ./ui_channel.mjs \
              --persona "$persona" --path "$ui_url" --evidence-dir "$evid" \
              > "$evid/ui-run.json" 2>"$evid/ui-run.err"
            u_rc=$?
            if [ -s "$evid/ui-channel.json" ]; then
              u_detail="$(jq -r '.detail // ""' "$evid/ui-channel.json")"
              case "$u_rc" in
                0) u_verdict="PASS" ;;
                1) u_verdict="FAIL" ;;   # rendered, the fact was not on the page
                *) u_verdict="BLOCKED" ;; # could not tell: login screen, timeout, empty shell
              esac
              u_basis="$u_detail"
            else
              u_verdict="BLOCKED"
              u_basis="ui_channel.mjs exited $u_rc with no verdict: $(tail -1 "$evid/ui-run.err" 2>/dev/null | cut -c1-180)"
            fi
          fi
        fi
      fi
    fi

    # ---- parity: the cross-channel verdict --------------------------------
    p_verdict="N/A"; p_detail=""; p_agent=""; p_ui=""; p_truth=""
    if [ "$channel" = "both" ]; then
      p_raw="$(timeout 120 ./parity_probe.py --id "$sid" --evidence-dir "$mdir/evidence" --json 2>&1)"
      p_rc=$?
      p_verdict="$(printf '%s' "$p_raw" | jq -r '.verdict // "CANNOT_COMPARE"' 2>/dev/null || echo CANNOT_COMPARE)"
      p_detail="$(printf '%s' "$p_raw" | jq -r '.detail // ""' 2>/dev/null || true)"
      p_agent="$(printf '%s' "$p_raw" | jq -c '.agent_value // null' 2>/dev/null || echo null)"
      p_ui="$(printf '%s' "$p_raw" | jq -c '.ui_value // null' 2>/dev/null || echo null)"
      p_truth="$(printf '%s' "$p_raw" | jq -r 'if .truth_disagrees_with_agent then (.truth_detail // "ground truth differs") else "" end' 2>/dev/null || true)"
    fi

    # ---- the scenario's verdict ------------------------------------------
    # PARITY_MISMATCH outranks a plain FAIL: when the two channels disagree, that is
    # the finding worth naming, even if one channel also failed on its own terms.
    # The order is: a disagreement first, then a failure, then BLOCKED, then PASS.
    verdict="$a_verdict"
    basis="$a_basis"
    case "$channel" in
      both)
        if [ "$p_verdict" = "PARITY_MISMATCH" ]; then
          verdict="PARITY_MISMATCH"
          basis="cross-channel: $p_detail"
        elif [ "$p_verdict" = "TRUTH_MISMATCH" ]; then
          verdict="TRUTH_MISMATCH"
          basis="ground truth: $p_detail"
        elif [ "$a_verdict" = "FAIL" ] || [ "$u_verdict" = "FAIL" ]; then
          verdict="FAIL"
          basis="agent=$a_verdict${a_basis:+ ($a_basis)}; ui=$u_verdict${u_basis:+ ($u_basis)}"
        elif [ "$a_verdict" = "BLOCKED" ] || [ "$u_verdict" = "BLOCKED" ] || [ "$p_verdict" = "CANNOT_COMPARE" ]; then
          verdict="BLOCKED"
          basis="agent=$a_verdict$([ -n "$a_basis" ] && echo " ($a_basis)"); ui=$u_verdict$([ -n "$u_basis" ] && echo " ($u_basis)"); parity=$p_verdict$([ -n "$p_detail" ] && echo " ($p_detail)")"
        else
          verdict="PASS"
          basis="agent and UI agree: ${p_detail:-the parity check passed}"
        fi
        ;;
      ui)
        verdict="$u_verdict"
        basis="ui only (the scenario declares channel: ui): $u_basis"
        ;;
      *)
        verdict="$a_verdict"
        basis="agent only (the scenario declares channel: agent): $a_basis"
        ;;
    esac

    jq -nc --arg id "$sid" --arg persona "$persona" --arg surface "$surface" \
       --arg verdict "$verdict" --arg basis "$basis" --arg channel "$channel" \
       --arg month "$ckpt" --arg season "$season" --arg parity "$parity" \
       --arg agent_verdict "$a_verdict" --arg ui_verdict "$u_verdict" \
       --arg parity_verdict "$p_verdict" --arg parity_detail "$p_detail" \
       --argjson agent_value "${p_agent:-null}" --argjson ui_value "${p_ui:-null}" \
       --arg truth "$p_truth" \
       '{id:$id, persona:$persona, surface:$surface, verdict:$verdict, basis:$basis,
         channel:$channel, month:$month, season:$season, parity_fact:$parity,
         agent_verdict:$agent_verdict, ui_verdict:$ui_verdict,
         parity_verdict:$parity_verdict, parity_detail:$parity_detail,
         parity_agent_value:$agent_value, parity_ui_value:$ui_value,
         truth_note:$truth, evidence_dir:("months/" + $month + "/evidence/" + $id)}' >> "$mres"

    case "$verdict" in
      PASS)            m_pass=$((m_pass + 1)) ;;
      BLOCKED)         m_block=$((m_block + 1)) ;;
      PARITY_MISMATCH) m_mm=$((m_mm + 1)) ;;
      TRUTH_MISMATCH)  m_truth=$((m_truth + 1)) ;;
      *)               m_fail=$((m_fail + 1)) ;;
    esac
    printf '%s\n' "$verdict"
  done

  # The counters above live in the pipeline's subshell, so recount from the file. A
  # counter that disagrees with the results is worse than no counter (§6).
  m_pass=$(jq -s '[.[] | select(.verdict=="PASS")] | length' "$mres")
  m_fail=$(jq -s '[.[] | select(.verdict=="FAIL")] | length' "$mres")
  m_block=$(jq -s '[.[] | select(.verdict=="BLOCKED")] | length' "$mres")
  m_mm=$(jq -s '[.[] | select(.verdict=="PARITY_MISMATCH")] | length' "$mres")
  m_truth=$(jq -s '[.[] | select(.verdict=="TRUTH_MISMATCH")] | length' "$mres")
  got_n=$(printf '%s\n' "$(cat "$mres")" | grep -c . || true)

  echo "  -> $ckpt: PASS $m_pass, FAIL $m_fail, BLOCKED $m_block, PARITY_MISMATCH $m_mm$([ "$m_truth" -gt 0 ] && echo ", TRUTH_MISMATCH $m_truth")"
  if [ "$got_n" -ge "$n_in" ]; then
    date -u +%Y-%m-%dT%H:%M:%SZ > "$mdir/DONE"
  fi
  # A month that is entirely BLOCKED is a broken area, not a reason to stop: mark it
  # and keep playing. That is the whole point of checkpointing by month.
  if [ "$m_block" -eq "$n_in" ]; then
    echo "  !! $ckpt is BLOCKED end to end — recorded and continuing to the next month"
  fi
  cat "$mres" >> "$RESULTS"
done <<< "$ckpts"

PASSES=$(jq -s '[.[] | select(.verdict=="PASS")] | length' "$RESULTS")
FAILS=$(jq -s '[.[] | select(.verdict=="FAIL")] | length' "$RESULTS")
BLOCKED=$(jq -s '[.[] | select(.verdict=="BLOCKED")] | length' "$RESULTS")
MISMATCH=$(jq -s '[.[] | select(.verdict=="PARITY_MISMATCH")] | length' "$RESULTS")
TRUTHBAD=$(jq -s '[.[] | select(.verdict=="TRUTH_MISMATCH")] | length' "$RESULTS")
n_played=$(jq -s 'length' "$RESULTS")

echo
echo "ROUND: $n_played scenario(s) — PASS $PASSES, FAIL $FAILS, BLOCKED $BLOCKED, PARITY_MISMATCH $MISMATCH$([ "$TRUTHBAD" -gt 0 ] && echo ", TRUTH_MISMATCH $TRUTHBAD")"
echo

# ---------------------------------------------------------------------------
# 4. The scorecard: per month, per surface, per channel, the parity verdicts, and
#    the delta against the previous round.
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
PASSES="$PASSES" FAILS="$FAILS" BLOCKED="$BLOCKED" MISMATCH="$MISMATCH" TRUTHBAD="$TRUTHBAD" \
N_PLAYED="$n_played" \
FILTER="$FILTER" PERSONA="$PERSONA" SURFACE="$SURFACE" TAG="$TAG" ROUNDF="$ROUND" \
LIMIT="$LIMIT" SKIP_UP="$SKIP_UP" HERE="$HERE" IDS="$IDS" \
MONTHS="$MONTHS" SEASON="$SEASON" YEAR_ONLY="$YEAR_ONLY" CHANNEL="$CHANNEL" \
REPLAYED_MONTHS="$REPLAYED_MONTHS" \
python3 - <<'PY'
import json, os, sys, time

env = os.environ
round_dir = env["ROUND_DIR"]
prev_dir = env.get("PREV_DIR", "")

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

# The four verdicts, in the order they matter. PARITY_MISMATCH is deliberately NOT
# summed into FAIL anywhere: "the product is wrong" and "the two channels tell
# different stories" are different findings with different fixes, and a scorecard that
# adds them together hides which one happened.
VERDICTS = ("PASS", "FAIL", "BLOCKED", "PARITY_MISMATCH", "TRUTH_MISMATCH")

def counts(docs):
    c = {v: 0 for v in VERDICTS}
    for d in docs.values():
        v = d.get("verdict", "FAIL")
        c[v] = c.get(v, 0) + 1
    return c

def section(docs, key, title, order=None):
    """A tally table. `order` fixes the row order (calendar for months)."""
    t = {}
    for d in docs.values():
        k = d.get(key) or "—"
        t.setdefault(k, {v: 0 for v in VERDICTS})
        v = d.get("verdict", "FAIL")
        t[k][v] = t[k].get(v, 0) + 1
    keys = [k for k in (order or []) if k in t] + sorted(k for k in t if k not in (order or []))
    if not keys:
        return []
    cols = ["PASS", "FAIL", "BLOCKED", "PARITY_MISMATCH"]
    lines = ["### By %s" % title, "",
             "| %s | %s | total |" % (title, " | ".join(c.replace("_", " ").title() for c in cols)),
             "|---|" + "---|" * (len(cols) + 1)]
    for k in keys:
        c = t[k]
        cells = " | ".join(str(c.get(v, 0)) for v in cols)
        lines.append("| %s | %s | %d |" % (k, cells, sum(c.get(v, 0) for v in VERDICTS)))
    lines.append("")
    return lines

cur_c, prev_c = counts(cur), counts(prev)

# --- delta ---------------------------------------------------------------
# Compare only the scenarios this round played, so a filtered round is compared
# on its own slice and not blamed for the scenarios it never touched.
new_fail, regressed, fixed, still_blocked, graduated, appeared = [], [], [], [], [], []
new_mismatch, mismatch_cleared = [], []
for sid, d in cur.items():
    v = d.get("verdict", "FAIL")
    if sid not in prev:
        appeared.append(sid)
        if v in ("FAIL", "PARITY_MISMATCH", "TRUTH_MISMATCH"):
            new_fail.append(sid)
        continue
    p = prev[sid].get("verdict", "FAIL")
    if v == p:
        if v == "BLOCKED":
            still_blocked.append(sid)
        continue
    if v == "PARITY_MISMATCH" and p != "PARITY_MISMATCH":
        new_mismatch.append((sid, p))
    if p == "PARITY_MISMATCH" and v != "PARITY_MISMATCH":
        mismatch_cleared.append((sid, v))
    if v in ("FAIL", "TRUTH_MISMATCH"):
        regressed.append((sid, p))
    elif p in ("FAIL", "TRUTH_MISMATCH", "PARITY_MISMATCH") and v in ("PASS", "BLOCKED"):
        fixed.append((sid, v))
    elif p == "BLOCKED" and v == "PASS":
        graduated.append(sid)

# A scenario played for the FIRST time is not "unchanged": it was never compared.
# Calling that clean is a plausible wrong answer (§7), so `clean` requires that
# every scenario this round also appears in the previous round.
compared = [sid for sid in cur if sid in prev]
uncompared = [sid for sid in cur if sid not in prev]
clean = (not new_fail and not regressed and not fixed and not graduated
         and not new_mismatch and not uncompared)

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
if env.get("MONTHS", "").strip():
    sel_desc.append(" ".join("--month %s" % m for m in env["MONTHS"].split()))
if env.get("SEASON"):
    sel_desc.append("--season %s" % env["SEASON"])
if env.get("YEAR_ONLY") == "1":
    sel_desc.append("--year")
for k, flag in (("PERSONA", "--persona"), ("SURFACE", "--surface"),
                ("TAG", "--tag"), ("FILTER", "--filter"), ("ROUNDF", "--round"),
                ("CHANNEL", "--channel")):
    if env.get(k):
        sel_desc.append("%s %s" % (flag, env[k]))
if env.get("LIMIT") and env["LIMIT"] != "0":
    sel_desc.append("--limit %s" % env["LIMIT"])
A("Selection: %s" % (", ".join(sel_desc) if sel_desc else "the whole corpus"))
A("")
if env.get("SKIP_UP") == "1":
    A("> **The environment gate was skipped (`--skip-up`).** These results are not")
    A("> comparable to a gated round: `up.sh` never confirmed the environment.")
    A("")
if env.get("REPLAYED_MONTHS", "0") not in ("0", ""):
    A("> %s checkpoint(s) were already DONE from an earlier run of this label and were"
      % env["REPLAYED_MONTHS"])
    A("> not replayed (resume). Use `--no-resume` to play them again.")
    A("")

# --- the one-line delta, at the top because it is the point -----------------
A("## Delta")
A("")
if not prev_dir:
    A("**Baseline round — no previous scorecard to compare against.** Every verdict")
    A("below is new by definition; the next round is the first one that can state a delta.")
    if prev_c:
        pass
else:
    A("Against `%s`." % os.path.basename(prev_dir.rstrip("/")))
    A("")
    if clean:
        A("**No new findings, no regressions.** All %d scenario(s) this round played"
          % len(cur))
        A("verdict-for-verdict the same as last round. If that holds again, we are done.")
    elif not regressed and not new_fail and not fixed and not graduated and not new_mismatch and uncompared:
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
        if new_mismatch:
            bits.append("%d new PARITY_MISMATCH" % len(new_mismatch))
        if mismatch_cleared:
            bits.append("%d parity mismatch(es) resolved" % len(mismatch_cleared))
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
                A("- `%s` — %s → %s" % (sid, p, cur[sid].get("verdict")))
            A("")
        if new_mismatch:
            A("New cross-channel mismatches:")
            for sid, p in sorted(new_mismatch):
                A("- `%s` — %s → PARITY_MISMATCH: %s"
                  % (sid, p, (cur[sid].get("parity_detail") or "")[:200]))
            A("")
        if mismatch_cleared:
            A("Parity mismatches resolved:")
            for sid, v in sorted(mismatch_cleared):
                A("- `%s` — PARITY_MISMATCH → %s" % (sid, v))
            A("")
        if new_fail:
            A("New failures:")
            for sid in sorted(new_fail):
                A("- `%s` — %s" % (sid, cur[sid].get("basis", "")[:160]))
            A("")
        if fixed:
            A("Fixed (was FAIL):")
            for sid, v in sorted(fixed):
                A("- `%s` — %s" % (sid, v))
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
for v in ("PASS", "FAIL", "BLOCKED", "PARITY_MISMATCH"):
    A("| %s | %d | %s |" % (v, cur_c.get(v, 0),
                            prev_c.get(v, 0) if prev_dir else "—"))
if cur_c.get("TRUTH_MISMATCH") or prev_c.get("TRUTH_MISMATCH"):
    A("| TRUTH_MISMATCH | %d | %s |" % (cur_c.get("TRUTH_MISMATCH", 0),
                                        prev_c.get("TRUTH_MISMATCH", 0) if prev_dir else "—"))
A("| **total** | **%d** | **%s** |" % (len(cur), len(prev) if prev_dir else "—"))
A("")
A("`PARITY_MISMATCH` is counted on its own line on purpose. It is not a `FAIL`: a FAIL")
A("says the product is wrong in a way one channel proved, while a PARITY_MISMATCH says")
A("the agent and the UI told different stories about the same fact. Collapsing them")
A("would lose which of the two happened, and they are fixed in different places.")
A("")

# --- per month -------------------------------------------------------------
months_order = sorted({d.get("month") for d in cur.values() if d.get("month")},
                      key=lambda m: ("_flat" == str(m), str(m)))
lines.extend(section(cur, "month", "month", months_order))
lines.extend(section(cur, "season", "season"))
lines.extend(section(cur, "surface", "surface"))
lines.extend(section(cur, "channel", "channel"))

# --- parity ---------------------------------------------------------------
both = {sid: d for sid, d in cur.items() if d.get("channel") == "both"}
if both:
    A("## Cross-channel parity")
    A("")
    A("Every `channel: both` scenario names one `parity` fact. This table is what the")
    A("round actually compared, and it is the reason a disagreement cannot hide inside a")
    A("\"the agent answered\" pass.")
    A("")
    pc = {}
    for d in both.values():
        k = d.get("parity_verdict") or "CANNOT_COMPARE"
        pc[k] = pc.get(k, 0) + 1
    A("| Parity verdict | Scenarios |")
    A("|---|---|")
    for k in ("PARITY_OK", "PARITY_MISMATCH", "CANNOT_COMPARE", "TRUTH_MISMATCH"):
        if pc.get(k):
            A("| %s | %d |" % (k, pc[k]))
    A("")
    mism = sorted(sid for sid, d in both.items() if d.get("parity_verdict") == "PARITY_MISMATCH")
    if mism:
        A("### The disagreements")
        A("")
        A("| Scenario | Agent says | The UI renders | Parity fact |")
        A("|---|---|---|---|")
        for sid in mism:
            d = both[sid]
            av = json.dumps(d.get("parity_agent_value"))
            uv = json.dumps(d.get("parity_ui_value"))
            A("| `%s` | `%s` | `%s` | %s |"
              % (sid, av[:60], uv[:60], (d.get("parity_fact") or "").replace("|", "\\|")[:150]))
        A("")
    else:
        A("No cross-channel disagreement this round.")
        A("")
    uncompared = sorted(sid for sid, d in both.items()
                        if (d.get("parity_verdict") or "CANNOT_COMPARE") == "CANNOT_COMPARE")
    if uncompared:
        A("### Not compared (a probe is missing, or a channel did not produce the fact)")
        A("")
        for sid in uncompared:
            A("- `%s` — %s" % (sid, (both[sid].get("parity_detail") or "")[:180]))
        A("")
        A("%d of %d `channel: both` scenarios were not actually compared. That is not a"
          % (len(uncompared), len(both)))
        A("pass: it means nobody has written `parity/<scenario-id>.py` yet, so the round")
        A("claims nothing about whether the channels agree.")
        A("")
    truth = sorted(sid for sid, d in both.items() if d.get("truth_note"))
    if truth:
        A("### Agreed but wrong (ground truth disagrees)")
        A("")
        A("These are the cases an agent-and-UI comparison alone cannot catch: both")
        A("channels stated the same fact, and the database or the tool payload says")
        A("otherwise.")
        A("")
        for sid in truth:
            A("- `%s` — %s" % (sid, (both[sid].get("truth_note") or "")[:200]))
        A("")

# --- per-scenario ---------------------------------------------------------
A("## Scenarios")
A("")
A("| Scenario | Month | Persona | Surface | Channel | Verdict | Why | Evidence |")
A("|---|---|---|---|---|---|---|---|")
for sid in sorted(cur, key=lambda s: (months_order.index(cur[s].get("month")) if cur[s].get("month") in months_order else 99, s)):
    d = cur[sid]
    # The driver always writes evidence/<id>/ next to the month's results, whether or
    # not it produced screenshots. Name it so a reader can go and look, and say plainly
    # when the directory is missing rather than printing a bare dash.
    ev = d.get("evidence_dir") or os.path.join("months", str(d.get("month") or "_flat"),
                                               "evidence", sid)
    if not os.path.isdir(os.path.join(round_dir, ev)):
        ev = "MISSING"
    basis = (d.get("basis") or "").replace("|", "\\|")[:150]
    A("| `%s` | %s | %s | %s | %s | %s | %s | `%s` |"
      % (sid, d.get("month", "?"), d.get("persona", "?"), d.get("surface", "?"),
         d.get("channel", "?"), d.get("verdict", "?"), basis, ev))
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
    A("A month whose every scenario is BLOCKED is marked in the per-month table above and")
    A("**does not stop the round**: the remaining months are still played, so one broken")
    A("area cannot hide the other eleven.")
    A("")

with open(os.path.join(round_dir, "scorecard.md"), "w", encoding="utf-8") as fh:
    fh.write("\n".join(lines) + "\n")

# machine-readable twin, so the next round diffs data and not prose
with open(os.path.join(round_dir, "verdicts.json"), "w", encoding="utf-8") as fh:
    json.dump({sid: {"verdict": d.get("verdict"), "persona": d.get("persona"),
                     "surface": d.get("surface"), "month": d.get("month"),
                     "season": d.get("season"), "channel": d.get("channel"),
                     "parity_verdict": d.get("parity_verdict"),
                     "basis": d.get("basis")}
               for sid, d in cur.items()}, fh, indent=1, sort_keys=True)

print("scorecard: %s" % os.path.join(round_dir, "scorecard.md"))
if prev_dir:
    print("delta vs:  %s" % prev_dir)
    if clean:
        print("DELTA:     no new findings, no regressions (%d scenarios unchanged)" % len(cur))
    elif not regressed and not new_fail and not fixed and not graduated and not new_mismatch and uncompared:
        print("DELTA:     %d new scenario(s) played for the first time, %d unchanged"
              % (len(uncompared), len(compared)))
    else:
        print("DELTA:     %d regressed, %d new PARITY_MISMATCH, %d new failure(s), "
              "%d fixed, %d graduated, %d new"
              % (len(regressed), len(new_mismatch), len(new_fail), len(fixed),
                 len(graduated), len(appeared)))
else:
    print("DELTA:     baseline round (no previous scorecard)")
PY

# ---------------------------------------------------------------------------
# 5. The gate: a round with any FAIL or PARITY_MISMATCH exits non-zero so it can stop
#    a deploy. A cross-channel disagreement is a failed round by definition: the two
#    channels are telling a family different things about the same fact.
# ---------------------------------------------------------------------------

SCORECARD="$ROUND_DIR/scorecard.md"
[ -f "$SCORECARD" ] || { echo "run.sh: scorecard was not written" >&2; exit 2; }

echo
bad=$((FAILS + MISMATCH + TRUTHBAD))
if [ "$bad" -ne 0 ]; then
  echo "GATE: FAILED — $FAILS scenario(s) FAILED, $MISMATCH reported PARITY_MISMATCH$([ "$TRUTHBAD" -gt 0 ] && echo ", $TRUTHBAD reported TRUTH_MISMATCH")."
  echo "Scorecard: $SCORECARD"
  exit 1
fi
echo "GATE: passed — no scenario FAILED and no channel disagreed ($PASSES PASS, $BLOCKED BLOCKED)."
echo "Scorecard: $SCORECARD"
exit 0
