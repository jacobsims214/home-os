#!/usr/bin/env bash
# chat.sh <persona> <session> <message>        send one persona turn to a moltis agent, wait for the reply
# chat.sh <persona> <session> --stdin          ... taking the message from stdin   (SAFEST)
# chat.sh <persona> <session> --file <path>    ... taking the message from a file  (SAFEST)
# chat.sh <persona> <session> --history        dump the session's history (tool calls + replies)
# chat.sh --selftest                           run the message-guard tests and exit
#
# WHY THE MESSAGE PATH MATTERS - read this before sending anything with money in it
# --------------------------------------------------------------------------------
# A message passed on the command line is expanded by YOUR shell before this script
# starts. In double quotes, `$2` in "mortgage is $2,340" is a positional parameter:
# bash replaces it with nothing, and moltis receives "mortgage is ,340". Likewise
# `$180` arrives as `80` and `$35` arrives as `5`. Nothing inside this script can
# undo that - by the time it runs the text is already wrong, and "electric 80" is
# indistinguishable from someone who really meant 80. That is how war game R1 filed
# a $35 copay as a $5 bill (KB discoveries/war-game-r1-jacob.md G9).
#
# So send money text down a path the shell never touches:
#
#   ./chat.sh jacob bills --stdin <<'EOF'        # NOTE the single-quoted 'EOF'
#   mortgage is $2,340, electric $180, internet $75
#   EOF
#
#   printf '%s' 'copay was $35' | ./chat.sh jacob bills --stdin
#   ./chat.sh jacob bills --file msg.txt         # --file: nothing reaches a shell
#
# An UNQUOTED <<EOF heredoc expands exactly like double quotes - quote the delimiter
# or use --file.
#
# If you must pass the message as an argument, ALWAYS single-quote it:
#   ./chat.sh jacob bills 'copay was $35'        # safe
#   ./chat.sh jacob bills "copay was $35"        # BUG: arrives as 'copay was 5'
#
# The guard below does what a script honestly can (see guard_message):
#   * REFUSE a message carrying the residue of damage it can actually see - a
#     `,340` with no digit in front of the comma is an eaten `$2,340`.
#   * WARN when an expandable `$`-pattern survived on the argv path, and when a
#     message talks about money with a bare number and no `$` anywhere: the shape
#     the $35 -> 5 corruption leaves behind. Both are heuristics, not proof.
# Overrides: --allow-suspicious (send a message the guard would refuse),
#            --allow-dollar (stop warning about `$` on the argv path).
#
# Each persona is a moltis instance. Configure it with env vars, where PERSONA is the
# persona name upper-cased (jacob -> JACOB):
#   WG_<PERSONA>_URL       base URL of the moltis web UI, e.g. http://moltis-wg-jacob:13131
#   WG_<PERSONA>_PASSWORD  moltis login password
# Optional:
#   WG_TIMEOUT_S           how long to wait for a reply (default 600, the moltis run limit)
#   WG_COOKIE_DIR          where login cookies are kept (default ${TMPDIR:-/tmp})
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: chat.sh <persona> <session> <message|--stdin|--file <path>|--history>
       chat.sh --selftest

  --stdin            read the whole message from stdin (use <<'EOF', not <<EOF)
  --file <path>      read the whole message from a file
  --history          dump the session's history instead of sending a turn
  --allow-suspicious send even if the message looks like the shell ate part of it
  --allow-dollar     don't warn about `$` in a message passed as an argument

A message passed as an argument is expanded by your shell first: "$35" reaches
this script as "5". Single-quote it, or better, use --stdin / --file.
USAGE
  exit 2
}

die() { echo "chat.sh: $*" >&2; exit 2; }

# ---------------------------------------------------------------------------
# Message guard
#
# Sets G_REFUSE (provably damaged: do not send) and G_WARN (suspicious: send and
# shout). Empty means clean. $1 = message, $2 = source kind (argv|stdin|file).
#
# The honest limit, stated once: this cannot detect the general case. A message
# the shell already expanded to "electric 80 ... internet 5" has no trace of the
# `$` left in it. Only the comma residue is provable, and only an argv `$` tells
# us we are on the dangerous path at all.
# ---------------------------------------------------------------------------
G_REFUSE=""; G_WARN=""

# A thousands-separated group that starts with a comma: `,340` - what `$2,340`
# becomes when bash eats the `$2`. A comma preceded by a digit (`1,340`) is a
# number someone actually typed, so it is left alone.
money_residue() {
  printf '%s' "$1" | grep -Eq '(^|[^0-9]),[0-9][0-9][0-9]([^0-9]|$)'
}

# A `$` the shell would expand: $1 $name ${x} $(cmd) $[n] $@ $* $# $? $$ $! $-
# A lone `$` followed by whitespace ("costs $ 35") is literal and stays quiet.
dollar_expandable() {
  printf '%s' "$1" | grep -Eq '\$([A-Za-z_{0-9(\[]|\$|@|#|\?|!|-|\*)'
}

# The shape the $35 -> 5 damage leaves: a money word followed straight by a bare
# number, with no `$` anywhere in the message. Heuristic - warn only. A number
# that is properly thousands-grouped (1,340) was typed, not eaten, so it is left
# out of this shape.
money_word_bare_number() {
  printf '%s' "$1" | grep -qiE '(copay|amount|costs?|charges?|price|total|fees?|owes?|owed|paid)[[:space:]]*(is|was|were|=|:)?[[:space:]]*[0-9]+(\.[0-9]+)?([^0-9,]|$)'
}

allows_dollar=0
allows_suspicious=0

guard_message() {
  local m="$1" src="${2:-argv}"
  G_REFUSE=""; G_WARN=""

  if [ "$allows_suspicious" -eq 0 ] && money_residue "$m"; then
    G_REFUSE="this message looks like the shell already ate a \$-amount: a comma starts a 3-digit group with no number before it (\"\$2,340\" arrives here as \",340\")"
    return 0
  fi

  case "$m" in
    *'$'*)
      if dollar_expandable "$m"; then
        if [ "$src" = "argv" ] && [ "$allows_dollar" -eq 0 ]; then
          G_WARN="${G_WARN}message contains a \$-pattern and was passed as an argument. It survived THIS far, so this one was quoted - but the argv path cannot prove the rest of it did: a sibling amount the shell ate leaves no trace. Prefer --stdin with <<'EOF' or --file. "
        fi
      fi
      ;;
    *)
      # No `$` at all: the expensive case. A money word glued to a bare number is
      # what `$35` -> `5` looks like once the `$` is gone.
      if money_word_bare_number "$m"; then
        G_WARN="${G_WARN}message talks about money with a bare number and contains no \"\$\" anywhere - if you meant a dollar amount, the shell may have eaten the \"\$\" (\"copay \$35\" arrives as \"copay 35\", and \"\$35\" as \"5\"). Check what moltis stored with --history. "
      fi
      ;;
  esac
}

print_guard_warning() {
  [ -n "$G_WARN" ] || return 0
  {
    echo "chat.sh: WARNING: ${G_WARN}"
    echo "chat.sh: see tools/wargame/README.md, 'Never put a \$ through your shell'."
  } >&2
}

# ---------------------------------------------------------------------------
# Self test: the guard, over a table of real messages. No network, no moltis.
# ---------------------------------------------------------------------------
selftest() {
  local fails=0 n=0
  t() { # t <expected: clean|warn|refuse> <source> <message>
    local want="$1" src="$2" msg="$3" got
    guard_message "$msg" "$src"
    if [ -n "$G_REFUSE" ]; then got=refuse
    elif [ -n "$G_WARN" ]; then got=warn
    else got=clean; fi
    n=$((n + 1))
    if [ "$got" = "$want" ]; then
      printf 'ok   %-6s %-5s %s\n' "$got" "$src" "$msg"
    else
      printf 'FAIL want %-6s got %-6s %-5s %s\n' "$want" "$got" "$src" "$msg" >&2
      fails=$((fails + 1))
    fi
  }

  # Clean: ordinary turns, and `$` text that never met a shell (stdin/file).
  t clean  argv  'Kid B soccer every Tue/Thu 5:30-6:30 through mid Dec, family calendar'
  t clean  argv  'mark the electric bill paid, due day 18'
  t clean  argv  'it costs $ 35 (no amount given)'
  t clean  stdin 'copay was $35, mortgage $2,340'
  t clean  file  'copay was $35, mortgage $2,340'
  # Warn on the argv path even when the `$` survived: this one was quoted, but the
  # path is the dangerous one and a sibling amount could still have been eaten.
  t warn   argv  'copay was $35.00, due 12/10'
  t warn   argv  'mortgage is $2,340 and electric $180'
  # Warn: an argv message with an expandable `$` (it survived, so the rest may not).
  t warn   argv  'copay was ${COPAY}'
  t warn   argv  'run $(date) for me'
  t warn   argv  'home is $HOME'
  # Warn: money word + bare number and no `$` at all - the $35 -> 5 shape.
  t warn   argv  'copay is 5 for the well visit'
  t warn   argv  'the amount is 35 dollars'
  t warn   stdin 'copay is 5'
  # Refuse: the comma residue of `$2,340`.
  t refuse argv  'mortgage is ,340 and electric 80'
  t refuse argv  'mortgage is ,340 ... electric 80 ... internet is 5'
  t refuse stdin ',340'
  t refuse file  'rent ,250'
  # A money word + bare number warns on every path: `printf "copay $35" | ... --stdin`
  # expands before the pipe, so --stdin is not proof either.
  t clean  argv  'the total was 1,340 dollars'
  # `$`-patterns bash does expand, on the argv path.
  t warn   argv  'took $[3] and ${HOME}'
  t warn   argv  'ping $? and $$'

  # The overrides.
  allows_suspicious=1
  guard_message 'mortgage is ,340' argv
  n=$((n + 1))
  if [ -z "$G_REFUSE" ]; then echo 'ok   allow-suspicious suppresses refuse'
  else echo 'FAIL allow-suspicious did not suppress refuse' >&2; fails=$((fails + 1)); fi
  allows_suspicious=0
  allows_dollar=1
  guard_message 'home is $HOME' argv
  n=$((n + 1))
  if [ -z "$G_WARN" ]; then echo 'ok   allow-dollar suppresses the $-warning'
  else echo 'FAIL allow-dollar did not suppress the warning' >&2; fails=$((fails + 1)); fi
  allows_dollar=0

  echo "--- $((n - fails))/$n passed"
  [ "$fails" -eq 0 ]
}

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
FROM_STDIN=0; FILE_IN=""; HISTORY=0; POS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --selftest)         selftest; exit $? ;;
    --history)          HISTORY=1; shift ;;
    --stdin)            FROM_STDIN=1; shift ;;
    --file)             [ $# -ge 2 ] || die "--file needs a path"; FILE_IN="$2"; shift 2 ;;
    --file=*)           FILE_IN="${1#--file=}"; shift ;;
    --allow-dollar)     allows_dollar=1; shift ;;
    --allow-suspicious) allows_suspicious=1; shift ;;
    -h|--help)          usage ;;
    -*)                 echo "chat.sh: unknown option: $1" >&2; usage ;;
    *)                  POS[${#POS[@]}]="$1"; shift ;;
  esac
done

if [ "${#POS[@]}" -lt 2 ]; then usage; fi
P="${POS[0]}"; SESSION="wg-$P-${POS[1]}"

# Where the message comes from: exactly one of the third argument, --stdin, --file.
if [ "$HISTORY" -eq 1 ]; then
  [ "${#POS[@]}" -le 2 ] && [ "$FROM_STDIN" -eq 0 ] && [ -z "$FILE_IN" ] \
    || die "--history does not take a message"
  MSG_RAW="--history"; SRC=history
else
  srcs=0
  [ "${#POS[@]}" -ge 3 ] && srcs=$((srcs + 1))
  [ "$FROM_STDIN" -eq 1 ] && srcs=$((srcs + 1))
  [ -n "$FILE_IN" ] && srcs=$((srcs + 1))
  [ "$srcs" -eq 1 ] || die "give exactly one message: an argument, --stdin, or --file <path>"
  if [ "${#POS[@]}" -ge 3 ]; then
    MSG_RAW="${POS[2]}"; SRC='argv'
  elif [ "$FROM_STDIN" -eq 1 ]; then
    MSG_RAW=$(cat); SRC='stdin'
  else
    [ -r "$FILE_IN" ] || die "cannot read message file: $FILE_IN"
    MSG_RAW=$(cat "$FILE_IN"); SRC='file'
  fi
  [ -n "$MSG_RAW" ] || die "the message is empty"
  guard_message "$MSG_RAW" "$SRC"
  if [ -n "$G_REFUSE" ]; then
    {
      echo "chat.sh: REFUSING to send this message."
      echo "chat.sh:   reason: $G_REFUSE"
      echo "chat.sh:   message: $MSG_RAW"
      echo "chat.sh:   Re-send it down a path the shell cannot touch:"
      echo "chat.sh:     ./chat.sh $P ${POS[1]} --stdin <<'EOF'"
      echo "chat.sh:     <the message, exactly as you want it>"
      echo "chat.sh:     EOF"
      echo "chat.sh:   or --file <path>.  Nothing was sent; the household is untouched."
      echo "chat.sh:   Override with --allow-suspicious only if you are certain."
    } >&2
    exit 2
  fi
  print_guard_warning
fi

# ---------------------------------------------------------------------------
# moltis transport
# ---------------------------------------------------------------------------
UP=$(printf '%s' "$P" | tr '[:lower:]-' '[:upper:]_')
URL_VAR="WG_${UP}_URL"; PW_VAR="WG_${UP}_PASSWORD"
B="${!URL_VAR:-}"; PW="${!PW_VAR:-}"
[ -n "$B" ]  || { echo "set $URL_VAR" >&2; exit 2; }
[ -n "$PW" ] || { echo "set $PW_VAR" >&2; exit 2; }
J="${WG_COOKIE_DIR:-${TMPDIR:-/tmp}}/wg-$P.cookies"
TIMEOUT_S="${WG_TIMEOUT_S:-600}"

gq() { curl -s -m 30 -b "$J" -H 'Content-Type: application/json' -H "Origin: $B" "$B/graphql" -d "$1"; }
hist_q() { printf '{"query":"{ chat { history(sessionKey: \\"%s\\") } }"}' "$SESSION"; }

# Log in only when the cookie no longer works. The password goes via stdin so it never
# shows up in the process list.
if ! gq '{"query":"{ chat { history(sessionKey: \"ping\") } }"}' | grep -q '"history"'; then
  PW="$PW" python3 -c 'import json,os; print(json.dumps({"password": os.environ["PW"]}))' |
    curl -s -m 10 -c "$J" -H 'Content-Type: application/json' -H "Origin: $B" "$B/api/auth/login" -d @- >/dev/null
fi

if [ "$SRC" = "history" ]; then
  gq "$(hist_q)" | python3 -c '
import sys, json
h = json.load(sys.stdin)["data"]["chat"]["history"]
print("TOTAL", len(h))
for i, m in enumerate(h):
    r = m.get("role")
    if r == "tool_result":
        a = {k: v for k, v in (m.get("arguments") or {}).items() if not k.startswith("_")}
        err = "" if m.get("success") else " -> " + str(m.get("error"))[:250]
        print(i, "  tool", "ok " if m.get("success") else "ERR", m.get("tool_name"), json.dumps(a)[:300] + err)
    elif r == "assistant" and m.get("content") and not m.get("tool_calls"):
        print(i, "REPLY:", m["content"])
    elif r == "user":
        print(i, "USER:", m.get("content"))
'
  exit 0
fi

N0=$(gq "$(hist_q)" | python3 -c 'import sys,json; print(len(json.load(sys.stdin)["data"]["chat"]["history"]))')
MSG=$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$MSG_RAW")
T0=$(date +%s)
R=$(gq "{\"query\":\"mutation(\$m:String!){ chat { send(message: \$m, sessionKey: \\\"$SESSION\\\") { ok } } }\",\"variables\":{\"m\":$MSG}}")
if echo "$R" | grep -q '"errors"'; then echo "SEND FAILED: $R"; exit 1; fi

OUT=""
while [ $(( $(date +%s) - T0 )) -lt "$TIMEOUT_S" ]; do
  sleep 4
  OUT=$(gq "$(hist_q)" | N0=$N0 python3 -c '
import sys, json, os
h = json.load(sys.stdin)["data"]["chat"]["history"][int(os.environ["N0"]):]
fin = [m for m in h if m.get("role") == "assistant" and m.get("content") and not m.get("tool_calls")]
if not fin:
    sys.exit(0)
for m in h:
    if m.get("role") == "tool_result":
        a = {k: v for k, v in (m.get("arguments") or {}).items() if not k.startswith("_")}
        res = "" if m.get("success") else " -> " + str(m.get("error"))[:200]
        print("  tool", "ok " if m.get("success") else "ERR", m.get("tool_name"), json.dumps(a)[:220] + res)
print("REPLY:", fin[-1]["content"])
') || true
  [ -n "$OUT" ] && break
done
echo "== $P/${POS[1]} ($(( $(date +%s) - T0 ))s)"
echo "${OUT:-TIMEOUT (no reply in ${TIMEOUT_S}s) - run with --history to see what the agent did}"
