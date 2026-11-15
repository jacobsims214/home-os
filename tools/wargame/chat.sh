#!/usr/bin/env bash
# chat.sh <persona> <session> <message>   send one persona turn to a moltis agent, wait for the reply
# chat.sh <persona> <session> --history   dump the session's history (tool calls + replies)
#
# Each persona is a moltis instance. Configure it with env vars, where PERSONA is the
# persona name upper-cased (jacob -> JACOB):
#   WG_<PERSONA>_URL       base URL of the moltis web UI, e.g. http://moltis-wg-jacob:13131
#   WG_<PERSONA>_PASSWORD  moltis login password
# Optional:
#   WG_TIMEOUT_S           how long to wait for a reply (default 600, the moltis run limit)
#   WG_COOKIE_DIR          where login cookies are kept (default ${TMPDIR:-/tmp})
set -euo pipefail

if [ $# -lt 3 ]; then
  echo "usage: chat.sh <persona> <session> <message|--history>" >&2
  exit 2
fi
P="$1"; SESSION="wg-$1-$2"; MSG_RAW="$3"
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

if [ "$MSG_RAW" = "--history" ]; then
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
echo "== $P/$2 ($(( $(date +%s) - T0 ))s)"
echo "${OUT:-TIMEOUT (no reply in ${TIMEOUT_S}s) - run with --history to see what the agent did}"
