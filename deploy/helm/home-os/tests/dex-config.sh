#!/usr/bin/env bash
# Asserts the rendered Dex config (#4886).
#
# The war game turned on Dex's password grant by hand-patching the LIVE
# ConfigMap. It was never in this chart, so the next Argo sync rendered over it
# and every family agent silently lost its headless auth (#4357). These checks
# exist so that class of bug fails a build instead of a production agent.
#
# Run: deploy/helm/home-os/tests/dex-config.sh    (needs helm 3.x; no cluster)
set -euo pipefail

CHART="$(cd "$(dirname "$0")/.." && pwd)"
BASE=(--set global.publicUrl=https://homeos.example.com --set global.imageTag=sha-0000000)
fails=0

fail() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }
pass() { echo "ok: $*"; }

# The Dex ConfigMap's config.yaml, rendered from the given extra --set args.
# The block scalar is indented 4 spaces under `config.yaml: |`; strip exactly that.
dex_config() {
  helm template "$CHART" "${BASE[@]}" \
      --show-only templates/dex/configmap.yaml "$@" \
    | awk '/^  config\.yaml: \|$/{p=1;next} p&&/^[^ ]/{exit} p{sub(/^    /,"");print}'
}

# A key at the 2-space indent inside the given top-level block (or empty).
yaml_val() { # <config> <block> <key>
  printf '%s\n' "$1" | awk -v blk="$2" -v key="$3" '
    $0 == blk ":" {inblk=1; next}
    inblk && /^[^ ]/ {exit}
    inblk && $0 ~ "^  " key ": " {sub("^  " key ": ",""); print; exit}'
}

# --- 1. Defaults: no password grant, but the expiry is always rendered --------
cfg="$(dex_config)"
[ -n "$cfg" ] || fail "no home-os-dex-config ConfigMap in the render"

# Match the KEY, not the comments around it (the docs above enablePasswordDB
# name the setting in prose). yaml_val only finds a `  passwordConnector: ` line
# inside oauth2:, which is the rendered setting Dex reads.
if [ -n "$(yaml_val "$cfg" oauth2 passwordConnector)" ]; then
  fail "passwordConnector is rendered by DEFAULT; the password grant must be opt-in"
else
  pass "default render has no passwordConnector (grant off)"
fi

grep -qx 'enablePasswordDB: true' <<<"$cfg" \
  || fail "enablePasswordDB is not true; the local connector and 'local' passwordConnector need it"
if [ "$(yaml_val "$cfg" expiry idTokens)" = '"24h"' ]; then
  pass "expiry.idTokens is explicit: $(yaml_val "$cfg" expiry idTokens)"
else
  fail "expiry.idTokens is not rendered as \"24h\" by default (got: '$(yaml_val "$cfg" expiry idTokens)')"
fi

# --- 2. dex.passwordConnector turns the grant on ------------------------------
cfg="$(dex_config --set dex.passwordConnector=local)"
if [ "$(yaml_val "$cfg" oauth2 passwordConnector)" = '"local"' ]; then
  pass "dex.passwordConnector=local renders oauth2.passwordConnector: \"local\""
else
  fail "dex.passwordConnector=local did not render oauth2.passwordConnector: \"local\" (got: '$(yaml_val "$cfg" oauth2 passwordConnector)')"
fi
# It must sit inside oauth2, or Dex ignores it.
if printf '%s\n' "$cfg" | awk '/^oauth2:/{o=1;next} /^[^ ]/{o=0} o&&/^  passwordConnector:/{f=1} END{exit !f}'; then
  pass "passwordConnector is nested under oauth2: (where Dex reads it)"
else
  fail "passwordConnector is not nested under oauth2:; Dex would ignore it"
fi

# --- 3. A connector id that does not exist must fail the render --------------
if helm template "$CHART" "${BASE[@]}" --set dex.passwordConnector=does-not-exist >/dev/null 2>&1; then
  fail "dex.passwordConnector=does-not-exist rendered; Dex would start fine and 400 every grant instead"
else
  pass "unknown passwordConnector id fails the render"
fi

# --- 4. dex.connectors renders, and its ids are accepted ---------------------
CONNECTOR=(
  --set 'dex.connectors[0].id=ldap'
  --set 'dex.connectors[0].type=ldap'
  --set 'dex.connectors[0].name=LDAP'
  --set 'dex.connectors[0].config.host=ldap.example.com:636'
  --set 'dex.connectors[0].config.bindDN=cn=read,dc=example,dc=com'
)
cfg="$(dex_config "${CONNECTOR[@]}")"
printf '%s\n' "$cfg" | grep -qx 'connectors:' \
  || fail "dex.connectors did not render a connectors: block"
printf '%s\n' "$cfg" | grep -q '  id: ldap' \
  || fail "dex.connectors[0].id did not render"
if helm template "$CHART" "${BASE[@]}" "${CONNECTOR[@]}" --set dex.passwordConnector=ldap >/dev/null 2>&1; then
  pass "a declared connector id is accepted by dex.passwordConnector"
else
  fail "dex.passwordConnector=ldap was refused although dex.connectors declares id ldap"
fi

# --- 5. No connectors block when unset (keep the config minimal) -------------
cfg="$(dex_config)"
if printf '%s\n' "$cfg" | grep -qx 'connectors:'; then
  fail "connectors: rendered although dex.connectors is empty"
else
  pass "no connectors: block when dex.connectors is empty"
fi

echo
if [ "$fails" -ne 0 ]; then
  echo "$fails check(s) failed" >&2
  exit 1
fi
echo "all Dex config checks passed"