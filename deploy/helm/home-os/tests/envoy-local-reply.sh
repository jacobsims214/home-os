#!/usr/bin/env bash
# Asserts that a throttled sign-in is a page a person can read (#5407).
#
# The war game reported the literal text `local_rate_limited` on a blank page.
# That string is Envoy's own local-reply body for the /dex/auth rate limiter: the
# request never reaches Dex or the UI, so no application code runs on the
# response and no app-side change can restyle it. The only place to fix it is
# this chart's HCM `local_reply_config`.
#
# These checks exist so the regression cannot come back silently: a chart
# re-render that drops the mapper, an Envoy upgrade that renames a field, or a
# well-meaning edit that "simplifies" the local reply back to the default body
# all fail a build instead of shipping a bare string to a locked-out family
# member.
#
# Run: deploy/helm/home-os/tests/envoy-local-reply.sh    (needs helm 3.x; no cluster)
set -euo pipefail

CHART="$(cd "$(dirname "$0")/.." && pwd)"
BASE=(--set global.publicUrl=https://homeos.example.com --set global.imageTag=sha-0000000)
fails=0

fail() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }
pass() { echo "ok: $*"; }

# The rendered envoy.yaml, from the ConfigMap's block scalar.
envoy_yaml() {
  helm template "$CHART" "${BASE[@]}" --show-only templates/envoy/configmap.yaml "$@" \
    | awk '/^  envoy\.yaml: \|$/{p=1;next} p&&/^[^ ]/{exit} p{sub(/^    /,"");print}'
}

cfg="$(envoy_yaml)"
[ -n "$cfg" ] || fail "no envoy-config ConfigMap in the render"

# The HTML the mapper actually SERVES: the inline_string that follows
# body_format_override, up to the mapper's headers_to_add.
#
# Every body assertion below runs against THIS, never against the whole render.
# The config's own comments do name local_rate_limited (explaining what was
# wrong), and a check that greps everything would fail on its own documentation
# -- then get "fixed" by deleting the explanation, which is the knowledge that
# stops this coming back.
body="$(printf '%s\n' "$cfg" | awk '
  /body_format_override:/ {inmap=1}
  inmap && /inline_string: \|$/ {p=1; next}
  p && /headers_to_add:/ {exit}
  p {print}
')"
[ -n "$body" ] || fail "the local reply mapper serves no inline body at all"

body_has() { # <failure message> <extended regex> -- asserts against the SERVED body
  printf '%s\n' "$body" | grep -qE -- "$2" || fail "$1"
}
cfg_has() { # <failure message> <extended regex> -- asserts against the rendered config
  printf '%s\n' "$cfg" | grep -qE -- "$2" || fail "$1"
}

# --- 1. The mapper exists and targets 429 exactly -----------------------------
cfg_has "the HCM renders no local_reply_config: a 429 falls back to Envoy's bare text body" '^[[:space:]]*local_reply_config:'
cfg_has "no status_code_filter: the local reply mapper matches nothing" 'status_code_filter:'
cfg_has "the local reply mapper does not filter on status 429" 'value: \{ default_value: 429 \}'

# --- 2. The body is HTML, not text ------------------------------------------
cfg_has "the 429 local reply is not served as text/html (it stays a bare string)" 'content_type: "text/html; charset=utf-8"'
body_has "the 429 local reply body is not an HTML document" '<!doctype html>'
body_has "the 429 page does not carry the app's rate-limited marker" 'data-auth-page="rate-limited"'
body_has "the 429 page has no heading a person can read" '<h1>[^<]+</h1>'
body_has "the 429 page has no warning block for the explanation" 'role="alert"'

# --- 3. It explains itself and says what to do ------------------------------
body_has "the 429 page does not name the problem" '[Tt]oo many sign-in attempts'
body_has "the 429 page gives no retry guidance" '[Ww]ait a moment, then try again'
# The retry window is the chart's value, not a hard-coded second opinion.
body_has "the 429 page does not quote retryAfterSeconds (expected 60)" 'about 60 seconds'
# A way back in, so the page is not a full stop.
body_has "the 429 page offers no way to try again" 'href="/login"'

# --- 4. A person never reads the raw local-reply string ---------------------
if printf '%s\n' "$body" | grep -q 'local_rate_limited'; then
  fail "the SERVED 429 body still contains local_rate_limited"
else
  pass "the served 429 body is an HTML page, with no raw local_rate_limited text"
fi
# Not a bare text body either: Envoy's default for a local reply is plain text,
# and a one-line body is how it looks.
lines="$(printf '%s\n' "$body" | grep -c '[^[:space:]]' || true)"
[ "${lines:-0}" -gt 20 ] || fail "the served 429 body is ${lines} non-blank lines: that is a bare string, not a page"

# --- 5. A client can read the wait, without parsing the page ----------------
cfg_has "the 429 local reply carries no retry-after header" 'key: retry-after, value: "60"'
cfg_has "the 429 local reply is cacheable (a stale throttle page could outlive the pause)" 'key: cache-control, value: "no-store"'

# --- 6. The limiter itself is NOT weakened ----------------------------------
# The ticket is explicit: the limit is correct defence, only its presentation is
# broken. Token buckets stay on the route, the filter stays enforced, and the
# budget stays the chart's requestsPerMinute / burst.
cfg_has "the /dex/auth route disappeared: the Dex login throttle is no longer applied" 'prefix: "/dex/auth"'
cfg_has "the local_ratelimit filter config disappeared from the /dex/auth route" 'envoy\.filters\.http\.local_ratelimit:'
cfg_has "the rate limit is no longer enforced" 'filter_enforced:'
cfg_has "the per-client budget changed (expected requestsPerMinute 10)" 'tokens_per_fill: 10$'
cfg_has "the per-client burst changed (expected burst 20)" 'max_tokens: 20$'

# --- 7. With the limiter off, none of this renders (no dangling config) -----
off="$(envoy_yaml --set envoy.dexLoginRateLimit.enabled=false)" || {
  fail "the chart no longer renders with envoy.dexLoginRateLimit.enabled=false"; off=""; }
if [ -n "$off" ]; then
  if printf '%s\n' "$off" | grep -q 'local_reply_config:'; then
    fail "a local_reply_config renders even with the rate limiter disabled (dead config)"
  else
    pass "no local_reply_config when the rate limiter is disabled"
  fi
fi

echo
if [ "$fails" -ne 0 ]; then
  echo "$fails check(s) failed" >&2
  exit 1
fi
echo "all Envoy local-reply checks passed"
