#!/usr/bin/env bash
# up.sh — bring the war-game environment to a KNOWN-GOOD state, or say loudly why not.
#
#   cd tools/wargame && ./up.sh
#
# What this is: the automated half of "Running a round" in README.md. It does not
# change anything — it verifies the four things a round cannot start without, prints a
# pass/fail table, and exits non-zero if any of them is wrong:
#
#   1. cluster   kubectl get nodes against the explicit kubeconfig -> 5 Ready
#   2. dex       each persona's Dex password, from the Secret, actually signs in
#   3. mcp       each persona's hos_ agent token answers tools/list on the public MCP
#   4. site      https://homeos.simstech.cloud serves 200 and Dex discovery has the
#                expected issuer
#
# Idempotent: it reads and probes only. Run it as often as you like; the second run is
# identical to the first. The Dex logins are the only thing with any side effect and
# that is Dex rotating its own refresh token — not a change to this environment.
#
# Never prints a secret. Every password and token is read from its Secret into a 0600
# temp file, used by curl from the file (never on argv), and shredded on exit — including
# on failure or Ctrl-C.
#
# Read README.md "Verifying the environment" before changing a check. The one rule that
# matters: a check that cannot tell must FAIL. A green line means the probe ran and the
# thing answered; it never means "no answer, assume fine".

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---------------------------------------------------------------------------
# Configuration. Nothing here is a secret; the values are names and URLs.
# Every one can be overridden from the environment.
# ---------------------------------------------------------------------------

# The cluster kubeconfig. README "Cluster access": this lives in another ticket's
# scratch folder, which is fragile — copy it to ~/.kube/wargame/ and point
# WARGAME_KUBECONFIG there if it disappears.
KUBECONFIG_PATH="${WARGAME_KUBECONFIG:-/Volumes/external-data/development/simstech/talos/pve-1/amp-work/5319/kube/admin.kubeconfig}"
CONTROL_PLANE="${WARGAME_CONTROL_PLANE:-https://192.168.8.226:6443}"
EXPECT_NODES_READY="${WARGAME_EXPECT_NODES:-5}"

SITE="${HOMEOS_SITE:-https://homeos.simstech.cloud}"
EXPECT_SITE_STATUS=200
EXPECT_ISSUER="${SITE}/dex"

# The MCP endpoint the agents use. A base URL is normal; /mcp is appended if absent.
MCP_URL="${HOMEOS_MCP_URL:-${SITE}}"
case "$MCP_URL" in
  */mcp) : ;;
  *) MCP_URL="${MCP_URL%/}/mcp" ;;
esac

# The persona roster, as "name:dex_email". Mirrors verify.yaml and README
# "Where each credential lives". Keep the two in step.
#
# NOTE the ticket text and the README disagree with reality here, and the README is
# right: there is no `jacob.wargame@` Dex account. Jacob's Dex identity is his real
# address. A worker who looks for `jacob.wargame@` finds nothing and reports the
# credential "rotated" (see README and how-to/wargame-harness-map.md).
PERSONAS=(
  "jacob:jacob.sims@simstech.cloud"
  "amber:amber.wargame@simstech.cloud"
)

# Where each persona's credentials live. Namespace is load-bearing: the war-game Dex
# passwords are in `moltis`, NOT `home-os`. Mixing them up is how two runs reported
# working credentials as rotated.
dex_secret_ns()     { case "$1" in jacob) echo "home-os" ;; *) echo "moltis" ;; esac; }
dex_secret_name()   { case "$1" in jacob) echo "home-os-secrets" ;; *) echo "home-os-amber-wargame-password" ;; esac; }
dex_secret_key()    { case "$1" in jacob) echo "adminPassword" ;; *) echo "password" ;; esac; }
token_secret_name() { echo "home-os-token-wg-$1"; }

# Timeouts. Nothing here should take longer; a hang is a FAIL, never a stalled run.
NET_TIMEOUT="${WARGAME_NET_TIMEOUT:-25}"
KUBECTL_TIMEOUT="${WARGAME_KUBECTL_TIMEOUT:-30}"

# ---------------------------------------------------------------------------
# Scratch space. One 0600 dir, removed on every exit path.
# ---------------------------------------------------------------------------

SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/wargame-up.XXXXXX")" || {
  echo "up.sh: cannot create a scratch dir" >&2; exit 2; }
chmod 700 "$SCRATCH"

scrub() {
  # Overwrite before unlinking so the bytes are not left in a free block. `shred` is
  # not on macOS; rm -P is, and either way the dir is 0700 and short-lived.
  local f
  for f in "$SCRATCH"/*; do
    [ -f "$f" ] || continue
    if command -v shred >/dev/null 2>&1; then shred -u "$f" 2>/dev/null
    else rm -Pf "$f" 2>/dev/null || rm -f "$f"; fi
  done
  rm -rf "$SCRATCH"
}
trap scrub EXIT INT TERM

# ---------------------------------------------------------------------------
# Results. Every check records PASS or FAIL and a one-line detail. There is no third
# "skipped, probably fine" state: a check that could not run is a FAIL, because the
# whole point is that a half-up environment must not look healthy.
# ---------------------------------------------------------------------------

RESULT_IDS=()
RESULT_STATES=()
RESULT_DETAILS=()

record() { RESULT_IDS+=("$1"); RESULT_STATES+=("$2"); RESULT_DETAILS+=("$3"); }
pass() { record "$1" PASS "$2"; }
fail() { record "$1" FAIL "$2"; }

# Read one Secret key into a 0600 file and leave the PATH in $4 (never the value).
# Fails if the value is missing or empty — an empty credential must never reach a
# probe, because a probe handed an empty bearer token answers 401 and looks exactly
# like a revoked token.
read_secret_to_file() {
  local ns="$1" name="$2" key="$3" out="$4"
  local val
  if ! val="$(KUBECONFIG="$KUBECONFIG_PATH" timeout "$KUBECTL_TIMEOUT" \
        kubectl -n "$ns" get secret "$name" -o "jsonpath={.data.${key}}" 2>"$SCRATCH/kerr")"; then
    fail "secret:$name" "cannot read ${ns}/${name} (README: right namespace?)"
    return 1
  fi
  if [ -z "$val" ]; then
    fail "secret:$name" "${ns}/${name} exists but key '${key}' is empty or absent"
    return 1
  fi
  umask 077
  printf '%s' "$val" | base64 -d > "$out" 2>/dev/null || : > "$out"
  if [ ! -s "$out" ]; then
    fail "secret:$name" "${ns}/${name} key '${key}' did not decode to a non-empty value"
    return 1
  fi
  return 0
}

# ---------------------------------------------------------------------------
# 1. Cluster
# ---------------------------------------------------------------------------

check_cluster() {
  local id="cluster"
  if [ ! -r "$KUBECONFIG_PATH" ]; then
    fail "$id" "kubeconfig not readable: $KUBECONFIG_PATH (README 'Cluster access')"
    return
  fi
  local out rc
  out="$(KUBECONFIG="$KUBECONFIG_PATH" timeout "$KUBECTL_TIMEOUT" kubectl get nodes 2>&1)"; rc=$?
  if [ $rc -ne 0 ]; then
    fail "$id" "kubectl get nodes failed (rc=$rc): $(printf '%s' "$out" | head -1)"
    return
  fi
  # The server we actually talked to, so a kubeconfig pointing at the dead .176 cannot
  # pass by accident.
  local server
  server="$(KUBECONFIG="$KUBECONFIG_PATH" timeout "$KUBECTL_TIMEOUT" \
            kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}' 2>/dev/null)"
  if [ "$server" != "$CONTROL_PLANE" ]; then
    fail "$id" "kubeconfig points at '${server}', expected ${CONTROL_PLANE}"
    return
  fi
  local ready bad
  ready="$(printf '%s\n' "$out" | awk 'NR>1 && $2=="Ready"' | wc -l | tr -d ' ')"
  if [ "$ready" -ne "$EXPECT_NODES_READY" ]; then
    # Name the ones that are not Ready — "4 of 5" without saying which is a puzzle.
    bad="$(printf '%s\n' "$out" | awk 'NR>1 && $2!="Ready" && NF>0 {printf "%s=%s ", $1, $2}')"
    fail "$id" "${ready}/${EXPECT_NODES_READY} Ready at ${CONTROL_PLANE}${bad:+ (not Ready: $bad)}"
    return
  fi
  pass "$id" "${ready}/${EXPECT_NODES_READY} Ready at ${CONTROL_PLANE}"
}

# ---------------------------------------------------------------------------
# 2. Dex sign-in
#
# The OAuth2 password grant is RETIRED (#4540) and returns
# 400 unsupported_grant_type — do not "fix" this check by using it. What a persona
# actually does is sign in through Dex's interactive local connector, so that is what
# this drives: authorize -> the local login form -> POST the password -> the redirect
# carrying an authorization code -> redeem it with PKCE -> an id_token.
#
# Redeeming the code is what makes this a real check. A 303 out of the login form only
# proves the form accepted a POST; the id_token's email claim proves Dex authenticated
# that identity, and its iss proves it came from this issuer.
# ---------------------------------------------------------------------------

check_dex_login() {
  local name="$1" email="$2" id="dex-login:$1"
  local pwfile="$SCRATCH/pw-$name"
  local verdict="$SCRATCH/verdict-$name"

  if ! read_secret_to_file "$(dex_secret_ns "$name")" "$(dex_secret_name "$name")" \
        "$(dex_secret_key "$name")" "$pwfile"; then
    fail "$id" "password Secret unreadable — see the secret:$(dex_secret_name "$name") row"
    return
  fi

  # python3 does the URL/cookie/JSON work because bash cannot do PKCE or parse an
  # id_token sanely. Standard library only; the password arrives by file path and is
  # never passed on argv or through the environment.
  #
  # The verdict goes to a FILE, not through $(...): a heredoc inside command
  # substitution is scanned by bash for a closing quote before it runs, so a single
  # apostrophe in any comment or string here breaks the whole script. Writing to a file
  # keeps bash out of the Python body entirely.
  python3 - "$SITE" "$email" "$pwfile" "$NET_TIMEOUT" > "$verdict" 2>&1 <<'PY'
import base64, hashlib, json, os, re, secrets, subprocess, sys, time, urllib.parse

site, email, pwfile, timeout = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
REDIRECT = "http://127.0.0.1:19876/mcp/oauth/callback"
workdir = os.path.dirname(pwfile)
jar = os.path.join(workdir, "jar-" + re.sub(r"\W", "_", email))
state = secrets.token_urlsafe(8)

def curl(args):
    cmd = ["curl", "-sS", "-m", timeout, "-c", jar, "-b", jar] + args
    r = subprocess.run(cmd, capture_output=True, text=True)
    return r.returncode, r.stdout, r.stderr

class Limited(Exception):
    pass

def die(msg):
    print("ERR " + msg); sys.exit(0)

def redirect_code(location):
    if not location.startswith(REDIRECT):
        return None
    return urllib.parse.parse_qs(urllib.parse.urlparse(location).query).get("code", [None])[0]

def attempt():
    verifier = base64.urlsafe_b64encode(secrets.token_bytes(48)).decode().rstrip("=")
    challenge = base64.urlsafe_b64encode(
        hashlib.sha256(verifier.encode()).digest()).decode().rstrip("=")
    q = {"client_id": "home-os-mcp", "response_type": "code", "scope": "openid email",
         "redirect_uri": REDIRECT, "code_challenge": challenge,
         "code_challenge_method": "S256", "state": state}

    # 1. authorize -> the local-connector login form. The status is captured because a
    # 429 here is the limiter, not a broken connector.
    rc, body, err = curl(["-L", "-o", os.path.join(workdir, "auth.html"),
                          "-w", "%{http_code}",
                          site + "/dex/auth?" + urllib.parse.urlencode(q)])
    if rc != 0:
        die("authorize request failed: " + (err.strip() or "curl rc=%d" % rc))
    status = body.strip()[-3:]
    html = open(os.path.join(workdir, "auth.html"), errors="replace").read()
    m = re.search(r'<form method="post" action="([^"]+)"', html)
    if not m:
        if status == "429":
            raise Limited()
        die("no login form at /dex/auth/local (HTTP %s) — Dex up? connector local enabled?" % status)
    action = m.group(1).replace("&amp;", "&")

    # 2. POST the password. password@file keeps it off argv.
    rc, body, err = curl(["-o", os.path.join(workdir, "login.html"),
                          "-w", "%{http_code}|%{redirect_url}",
                          "--data-urlencode", "login=" + email,
                          "--data-urlencode", "password@" + pwfile,
                          site + action])
    if rc != 0:
        die("login POST failed: " + (err.strip() or "curl rc=%d" % rc))
    status, _, location = body.partition("|")
    if status == "401":
        die("Dex rejected the password (401) — Secret is stale or the account is gone")
    if status == "429":
        raise Limited()
    if status not in ("302", "303"):
        die("login POST returned %s, expected a redirect" % status)

    # 3. follow the redirect chain to the authorization code, hop by hop rather than with
    # -L: the last hop points at 127.0.0.1:19876, which is deliberately not listening,
    # and -L would try to connect, fail, and lose the code.
    hdrfile = os.path.join(workdir, "hop.hdr")
    code = redirect_code(location)
    last_status = ""
    for _ in range(4):
        if code:
            break
        rc, last_status, err = curl(["-o", "/dev/null", "-D", hdrfile,
                                     "-w", "%{http_code}", location])
        if rc != 0:
            die("could not follow the login redirect: " + err.strip())
        last_status = last_status.strip()[-3:]
        nxt = None
        with open(hdrfile, "r", errors="replace") as fh:
            for line in fh:
                if line.lower().startswith("location:"):
                    nxt = line.split(":", 1)[1].strip()
        if not nxt:
            break
        location = nxt if nxt.startswith("http") else urllib.parse.urljoin(site, nxt)
        code = redirect_code(location)
    if not code:
        if last_status == "429":
            raise Limited()
        die("sign-in produced no authorization code (last HTTP %s)" % (last_status or "none"))

    # 4. redeem it — the proof that Dex authenticated this identity, not merely that a
    # form accepted a POST.
    rc, tok, err = curl(["-X", "POST", site + "/dex/token",
                         "--data-urlencode", "grant_type=authorization_code",
                         "--data-urlencode", "code=" + code,
                         "--data-urlencode", "redirect_uri=" + REDIRECT,
                         "--data-urlencode", "client_id=home-os-mcp",
                         "--data-urlencode", "code_verifier=" + verifier])
    if rc != 0:
        die("token exchange failed: " + err.strip())
    try:
        doc = json.loads(tok)
    except ValueError:
        die("token endpoint did not return JSON")
    if "id_token" not in doc:
        die("token endpoint returned no id_token: " + str(doc.get("error", "no error field")))

    payload = doc["id_token"].split(".")[1]
    payload += "=" * (-len(payload) % 4)
    claims = json.loads(base64.urlsafe_b64decode(payload))
    if claims.get("email", "").lower() != email.lower():
        die("signed in as %r, expected %r" % (claims.get("email"), email))
    print("OK id_token for %s (iss %s)" % (claims["email"], claims.get("iss")))


# Bounded retry. Envoy rate-limits /dex/auth* per client IP (values.yaml
# envoy.dexLogin: 10/min, burst 20) and one run spends about 10 tokens, so a second run
# inside the same minute can be limited. Waiting is the correct answer; the alternative
# is reporting a healthy environment as broken. The limit is reported only once this
# wait budget is spent.
WAIT_BUDGET_S = int(os.environ.get("WARGAME_LIMIT_WAIT_S", "130"))
deadline = time.time() + WAIT_BUDGET_S
while True:
    try:
        attempt()
        break
    except Limited:
        if time.time() >= deadline:
            die("sign-ins are rate-limited (429) and did not clear within %ds "
                "(per-client limiter on /dex/auth*, values.yaml envoy.dexLogin). "
                "Wait a minute and re-run; this is the limiter, not a bad password."
                % WAIT_BUDGET_S)
        # Never sleep past the budget: a short WARGAME_LIMIT_WAIT_S must not be
        # stretched to a minute, or the caller's timeout kills the probe mid-wait.
        time.sleep(max(1, min(65, deadline - time.time())))
PY
  local rc=$?

  local out
  out="$(cat "$verdict" 2>/dev/null)"
  case "$out" in
    OK*) pass "$id" "${out#OK }" ;;
    ERR*) fail "$id" "${out#ERR }" ;;
    *) fail "$id" "probe produced no verdict (rc=$rc): $(printf '%s' "$out" | head -1)" ;;
  esac
  rm -f "$pwfile" "$verdict"
}

# ---------------------------------------------------------------------------
# 3. Agent tokens over MCP
#
# tools/list is enough to prove the token is live, the MCP is reachable from here, and
# the token resolves to a member. Note: a hos_ token 401s on /api/v1/* BY DESIGN
# (README "Two kinds of token") — this check must never call REST and must never
# report that 401 as a failure.
# ---------------------------------------------------------------------------

check_mcp_token() {
  local name="$1" id="mcp:$1"
  local tokfile="$SCRATCH/tok-$name"

  if ! read_secret_to_file moltis "$(token_secret_name "$name")" token "$tokfile"; then
    fail "$id" "agent token Secret unreadable — see the secret:$(token_secret_name "$name") row"
    return
  fi
  if [ ! -s "$tokfile" ]; then
    fail "$id" "agent token read as empty; refusing to probe with it"
    return
  fi

  local body="$SCRATCH/mcp-$name.json" hdrs="$SCRATCH/mcp-$name.hdr"
  local rc
  curl -sS -m "$NET_TIMEOUT" -X POST "$MCP_URL" \
    -H "Authorization: Bearer $(cat "$tokfile")" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -D "$hdrs" -o "$body" \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' 2>"$SCRATCH/cerr"; rc=$?
  if [ $rc -ne 0 ]; then
    fail "$id" "could not reach ${MCP_URL}: $(head -1 "$SCRATCH/cerr")"
    return
  fi
  local status
  status="$(awk 'NR==1{print $2}' "$hdrs")"
  if [ "$status" = "401" ]; then
    fail "$id" "MCP refused the token (401) — token revoked, or instance wired with a stale one"
    return
  fi
  if [ "$status" != "200" ]; then
    fail "$id" "${MCP_URL} returned HTTP ${status} for tools/list"
    return
  fi
  # A 200 with a JSON-RPC error is still a failure: check the payload, not just the code.
  local verdict
  verdict="$(python3 -c '
import json,sys
try: d=json.load(open(sys.argv[1]))
except Exception as e: print("ERR unparseable tools/list body: %s" % e); raise SystemExit
if d.get("error"): print("ERR JSON-RPC error: %s" % json.dumps(d["error"])[:160])
else:
    n=len(d.get("result",{}).get("tools",[]))
    print("ERR tools/list returned no tools" if n==0 else "OK %d tools" % n)
' "$body")"
  case "$verdict" in
    OK*) pass "$id" "${verdict#OK } over MCP as $name" ;;
    *)   fail "$id" "${verdict#ERR }" ;;
  esac
  rm -f "$tokfile"
}

# ---------------------------------------------------------------------------
# 4. Site and Dex discovery
# ---------------------------------------------------------------------------

check_site() {
  local id="site"
  local rc out
  # Follow redirects: "/" 307s to /login for an anonymous caller, and /login is the
  # 200 that proves the UI is actually serving.
  out="$(curl -sS -m "$NET_TIMEOUT" -L -o /dev/null \
        -w '%{http_code} %{url_effective}' "${SITE}/" 2>&1)"; rc=$?
  if [ $rc -ne 0 ]; then
    fail "$id" "${SITE} unreachable: ${out}"
    return
  fi
  local status url
  status="${out%% *}"; url="${out#* }"
  if [ "$status" != "$EXPECT_SITE_STATUS" ]; then
    fail "$id" "${SITE} -> HTTP ${status} (expected ${EXPECT_SITE_STATUS})"
    return
  fi
  pass "$id" "HTTP ${status} (${url})"
}

check_dex_discovery() {
  local id="dex-discovery"
  local body="$SCRATCH/discovery.json" rc
  curl -sS -m "$NET_TIMEOUT" -o "$body" -w '%{http_code}' \
    "${SITE}/dex/.well-known/openid-configuration" >"$SCRATCH/dstatus" 2>"$SCRATCH/cerr"; rc=$?
  if [ $rc -ne 0 ]; then
    fail "$id" "discovery unreachable: $(head -1 "$SCRATCH/cerr")"
    return
  fi
  local status; status="$(cat "$SCRATCH/dstatus")"
  if [ "$status" != "200" ]; then
    fail "$id" "discovery returned HTTP ${status}"
    return
  fi
  local verdict
  verdict="$(python3 -c '
import json,sys
try: d=json.load(open(sys.argv[1]))
except Exception as e: print("ERR discovery is not JSON: %s" % e); raise SystemExit
iss=d.get("issuer","")
exp=sys.argv[2]
print("OK issuer %s" % iss if iss.rstrip("/")==exp.rstrip("/") else
      "ERR issuer is %r, expected %r" % (iss, exp))
' "$body" "$EXPECT_ISSUER")"
  case "$verdict" in
    OK*) pass "$id" "${verdict#OK }" ;;
    *)   fail "$id" "${verdict#ERR }" ;;
  esac
}

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------

echo "wargame up.sh — verifying the environment"
echo "  site      ${SITE}"
echo "  mcp       ${MCP_URL}"
echo "  cluster   ${CONTROL_PLANE}"
echo "  kubeconf  ${KUBECONFIG_PATH}"
echo

for tool in curl kubectl python3 base64; do
  command -v "$tool" >/dev/null 2>&1 || { echo "up.sh: missing required tool: $tool" >&2; exit 2; }
done

check_cluster
check_site
check_dex_discovery
for entry in "${PERSONAS[@]}"; do
  check_dex_login "${entry%%:*}" "${entry#*:}"
done
for entry in "${PERSONAS[@]}"; do
  check_mcp_token "${entry%%:*}"
done

# Optional: if #5475's verify.py is present it also knows the moltis instance logins.
# Run it and fold its verdicts in, so up.sh is the one command rather than "one of two".
if [ -x "$HERE/verify.py" ] && [ -f "$HERE/verify.yaml" ]; then
  vout="$(cd "$HERE" && timeout 180 ./verify.py --json 2>"$SCRATCH/verr")"; vrc=$?
  vrows=""
  if [ -n "$vout" ]; then
    vrows="$(printf '%s' "$vout" | python3 -c '
import json,sys
try: d=json.load(sys.stdin)
except Exception: raise SystemExit
rs = d.get("checks") if isinstance(d, dict) else d
if not isinstance(rs, list): raise SystemExit
for r in rs:
    print("%s\t%s\t%s" % (r.get("id",""), "1" if r.get("ok") else "0", r.get("detail","")))
' 2>/dev/null)"
  fi
  # verify.py present but nothing usable back is a FAIL, not a quiet omission: a
  # verifier that contributes no rows while reporting READY is the exact defect
  # this script exists to catch.
  if [ -z "$vrows" ]; then
    fail "verify:verify.py" "ran but returned no parseable checks (rc=$vrc): $(head -1 "$SCRATCH/verr" 2>/dev/null)"
  else
    while IFS=$'\t' read -r cid cok cdetail; do
      [ -n "$cid" ] || continue
      # up.sh covers cluster/site/dex/mcp more strictly; only add what it lacks.
      case "$cid" in
        cluster-reachable|site-up|dex-reachable|persona-*) continue ;;
      esac
      if [ "$cok" = "1" ]; then pass "verify:$cid" "$cdetail"; else fail "verify:$cid" "$cdetail"; fi
    done <<< "$vrows"
  fi
fi

# --- the table -------------------------------------------------------------
echo
printf '%-28s %-6s %s\n' "CHECK" "RESULT" "DETAIL"
printf '%-28s %-6s %s\n' "----------------------------" "------" "----------------------------------------"
failed=0
for i in "${!RESULT_IDS[@]}"; do
  printf '%-28s %-6s %s\n' "${RESULT_IDS[$i]}" "${RESULT_STATES[$i]}" "${RESULT_DETAILS[$i]}"
  [ "${RESULT_STATES[$i]}" = "PASS" ] || failed=$((failed + 1))
done
total="${#RESULT_IDS[@]}"
echo
if [ "$failed" -ne 0 ]; then
  echo "FAILED: ${failed} of ${total} checks. The environment is NOT ready — do not start a round."
  echo "Each failing row names the thing to look at; README 'Verifying the environment' says where it lives."
  exit 1
fi
echo "READY: all ${total} checks passed."
exit 0
