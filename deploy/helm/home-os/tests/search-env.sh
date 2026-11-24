#!/usr/bin/env bash
# Asserts that every pod which searches the index is WIRED to it (#5621).
#
# THE DEFECT. apps/mcp/internal/config/config.go reads TYPESENSE_HOST,
# TYPESENSE_PORT and TYPESENSE_API_KEY. The api and worker deployments set
# them; the mcp deployment did not. So on the deployed stack kb_search's
# semantic search failed with "no search index configured", the handler
# swallowed the error, and a paraphrase of a document that existed came back
# as a normal empty result. The index and the query were correct the whole
# time (#5607 found 2 results — including how-to/pets/vet-after-hours —
# replaying the MCP's exact parameters at Typesense).
#
# Nobody noticed because nothing checked the render: `helm template` succeeded,
# the pod was healthy, the tool answered. A chart value that never reaches a
# container is exactly the bug that cost the family agents their headless auth
# (#4357, #4886) and this one. So the check is on the RENDERED env, per
# deployment, and it fails the PR rather than a family's search.
#
# Run: deploy/helm/home-os/tests/search-env.sh    (needs helm 3.x; no cluster)
set -euo pipefail

CHART="$(cd "$(dirname "$0")/.." && pwd)"
BASE=(--set global.publicUrl=https://homeos.example.com --set global.imageTag=sha-0000000)
fails=0

fail() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }
pass() { echo "ok: $*"; }

# The env: block of the one container in a given template's render, as a flat
# list of "- name: X" / "value:" lines. Prints nothing when the template or its
# container does not render at all.
env_block() { # <template path> <container name>
  helm template "$CHART" "${BASE[@]}" --show-only "$1" \
    | awk -v want="      - name: $2" '
        $0 == want {inc=1; next}
        inc && /^      - name: / {exit}
        inc {print}'
}

# The env var's value, following a secretKeyRef to the key it selects, so a
# check can prove the setting is a reference and not a literal.
env_value() { # <env block> <VAR>
  printf '%s\n' "$1" | awk -v v="$2" '
    $0 == "        - name: " v {f=1; next}
    f && /^        - name: / {exit}
    f && /^          value: / {sub(/^          value: /, ""); print; exit}
    f && /^          valueFrom:/ {vf=1; next}
    f && vf && /^            secretKeyRef:/ {sk=1; next}
    f && sk && /^              name: / {sub(/^              name: /, ""); n=$0; next}
    f && sk && /^              key: / {sub(/^              key: /, ""); print n "|" $0; exit}'
}

# --- 1. Every searching deployment gets the three settings -------------------
# The list is the deployments whose code reads config.Typesense*: mcp
# (kb_search, guide_find, document_find), api (search, reconciler) and worker
# (index sweep). A new one is added here when it is added to the chart.
check_deployment() { # <template> <container>
  local tmpl="$1" name="$2" block host port key
  block="$(env_block "$tmpl" "$name")"
  if [ -z "$block" ]; then
    fail "$tmpl: no '$name' container rendered; cannot check its Typesense env"
    return
  fi
  host="$(env_value "$block" TYPESENSE_HOST)"
  port="$(env_value "$block" TYPESENSE_PORT)"
  key="$(env_value "$block" TYPESENSE_API_KEY)"
  [ -n "$host" ] && pass "$name: TYPESENSE_HOST=$host" \
    || fail "$name: TYPESENSE_HOST is missing; its search would error and (before #5621) quietly degrade"
  [ -n "$port" ] && pass "$name: TYPESENSE_PORT=$port" \
    || fail "$name: TYPESENSE_PORT is missing"
  case "$key" in
    *"|typesenseApiKey")
      pass "$name: TYPESENSE_API_KEY is a secretKeyRef to the ${key%%|*} Secret" ;;
    "") fail "$name: TYPESENSE_API_KEY is missing; semantic search cannot work without it" ;;
    *)  fail "$name: TYPESENSE_API_KEY is not a secretKeyRef to typesenseApiKey (got '$key'); never render a key as a literal" ;;
  esac
  # The host has to be the Service the chart deploys, or the pod resolves
  # nothing and every search fails at runtime with the env "present".
  [ "$host" = "home-os-typesense" ] || fail "$name: TYPESENSE_HOST=$host, want the home-os-typesense Service"
  # The port has to be the one the Service exposes.
  local svc_port
  svc_port="$(helm template "$CHART" "${BASE[@]}" --show-only templates/typesense/service.yaml \
    | awk '/^    - port: /{sub(/^    - port: /,""); print; exit}')"
  if [ -n "$svc_port" ] && [ "$port" != "$svc_port" ]; then
    fail "$name: TYPESENSE_PORT=$port but the typesense Service exposes $svc_port"
  fi
}

check_deployment templates/mcp/deployment.yaml mcp
check_deployment templates/api/deployment.yaml api
check_deployment templates/worker/deployment.yaml worker

# --- 2. The key is the one the chart's Secret actually carries --------------
# A secretKeyRef to a key the Secret does not have leaves the pod in
# CreateContainerConfigError, which is loud but only on the cluster; this
# catches the typo before the sync.
if ! helm template "$CHART" "${BASE[@]}" --show-only templates/secrets.yaml 2>/dev/null \
    | grep -q '^  typesenseApiKey: '; then
  fail "home-os-secrets does not define typesenseApiKey; every TYPESENSE_API_KEY above would be unresolved"
else
  pass "the chart's Secret carries typesenseApiKey"
fi

# --- 3. Negative control: the check can fail -------------------------------
# Rendered from a chart copy with the mcp include deleted, the assertions must
# fail. Without this, a check that silently stopped matching would pass forever
# — the grep-only envoy test that took the site down (#5421).
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cp -R "$CHART/." "$TMP/chart"
# Delete the mcp deployment's include. sed (not an editor) so the control runs
# on a copy and cannot touch the chart under test.
perl -pi -e 's/^.*include "home-os\.typesenseEnv".*\n//' "$TMP/chart/templates/mcp/deployment.yaml"
if grep -q 'typesenseEnv' "$TMP/chart/templates/mcp/deployment.yaml"; then
  fail "negative control could not remove the include from the chart copy; the control below proves nothing"
elif helm template "$TMP/chart" --set global.publicUrl=https://homeos.example.com --set global.imageTag=sha-0000000 \
      --show-only templates/mcp/deployment.yaml 2>/dev/null | grep -q 'name: TYPESENSE_HOST'; then
  fail "negative control DID NOT fail: the mcp render still has TYPESENSE_HOST with the include deleted, so sections 1-2 prove nothing"
else
  pass "negative control: deleting the mcp include removes TYPESENSE_HOST from its render (this check can fail)"
fi

echo
if [ "$fails" -ne 0 ]; then
  echo "$fails check(s) failed" >&2
  exit 1
fi
echo "all search-wiring checks passed"
