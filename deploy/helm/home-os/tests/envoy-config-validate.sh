#!/usr/bin/env bash
# Asserts that Envoy can actually LOAD the config this chart renders (#5421, #5422).
#
# envoy-local-reply.sh asks what the rendered config SAYS. This asks whether
# envoy accepts it. They are different questions, and on dc01ab9 the first
# passed while the second was fatal:
#
#   text_format_source.inline_string is not a plain string. Envoy parses it as a
#   substitution-format string, where `%` starts a command (`%%` is a literal
#   percent). The 429 page's CSS contains `width: 100%;`, so the formatter hit
#   `Couldn't find valid command at position 1080` and envoy refused to start.
#   Envoy is the cluster ingress: the site returned 503 and the deploy was
#   rolled back (#5420).
#
# Grepping the render cannot see that class of defect. Only envoy can. So this
# check runs the real binary, at the exact image tag the chart pins, in
# `--mode validate` -- no cluster, no ports, a couple of seconds.
#
# It also proves itself, on every run. A harness that silently stops working
# (docker missing, image tag gone, a flag renamed, the config path moved) would
# otherwise turn this check into a green light that means nothing -- and this
# project has already shipped tests that could not fail.
#
# Run: deploy/helm/home-os/tests/envoy-config-validate.sh   (needs helm 3.x + docker; no cluster)
set -euo pipefail

CHART="$(cd "$(dirname "$0")/.." && pwd)"
BASE=(--set global.publicUrl=https://homeos.example.com --set global.imageTag=sha-0000000)
fails=0

fail() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }
pass() { echo "ok: $*"; }
die() { echo "FAIL: $*" >&2; echo; echo "cannot validate the envoy config" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
# The envoy image runs as uid 101, so the mounted file has to be readable by a
# user that is not us. mktemp -d makes the directory 0700.
chmod 755 "$tmp"

# The rendered envoy.yaml, from the ConfigMap's block scalar -- the bytes the pod
# would mount, not a re-typed copy.
#
# The indent is DETECTED (YAML defines it as the first non-empty line's), never
# assumed to be 4. Stripping a fixed 4 spaces is right only while the block keeps
# today's indentation: after a re-indent this would hand envoy a differently
# indented file than the kubelet mounts, envoy would accept it, and the check
# would pass while validating something nobody ships. A content line indented
# less than the block is an error rather than a short strip.
envoy_yaml() {
  helm template "$CHART" "${BASE[@]}" --show-only templates/envoy/configmap.yaml "$@" \
    | awk '
      BEGIN { ind = 0 }
      /^  envoy\.yaml:/ {
        if ($0 != "  envoy.yaml: |") {
          print "the envoy.yaml key is not a plain literal block scalar: " $0 > "/dev/stderr"; exit 2
        }
        p = 1; next
      }
      p && /^[^ ]/ { exit }
      p && /^[[:space:]]*$/ { print ""; next }
      p {
        if (ind == 0) { match($0, /^ */); ind = RLENGTH }
        match($0, /^ */)
        if (RLENGTH < ind) {
          print "the envoy.yaml block scalar is not uniformly indented (line " NR " is " RLENGTH \
                " spaces, the block is " ind "): cannot decode it the way Kubernetes does" > "/dev/stderr"
          exit 3
        }
        print substr($0, ind + 1)
      }'
}

# The image the chart actually deploys, taken from the render rather than from
# values.yaml, so a field rename or an override cannot make this check validate
# against a different envoy than the one that ships.
envoy_image() {
  helm template "$CHART" "${BASE[@]}" --show-only templates/envoy/deployment.yaml \
    | awk '/^[[:space:]]*image: .*envoy/{gsub(/["[:space:]]/,"",$2); print $2; exit}'
}

# Where the container reads the config. envoy starts from the image's own CMD,
# which is `envoy -c /etc/envoy/envoy.yaml` -- so that, not the mountPath, is the
# path the process opens. This check is therefore that the Deployment MOUNTS the
# ConfigMap exactly there: if the mount moves, the pod finds no config and envoy
# will not start, which must fail here rather than this check quietly following
# the mount to a file nobody reads.
ENVOY_CMD_CONFIG=/etc/envoy/envoy.yaml
envoy_mount_path() {
  local dep mount
  dep="$(helm template "$CHART" "${BASE[@]}" --show-only templates/envoy/deployment.yaml 2>/dev/null)" || return 1
  # envoy starts from the image's CMD (`envoy -c <mount>/envoy.yaml`). An override
  # would silently change the path, so refuse rather than guess.
  if printf '%s\n' "$dep" | grep -qE '^[[:space:]]+(command|args):'; then
    return 2
  fi
  mount="$(printf '%s\n' "$dep" | awk '
    /- name: config$/ {found=1; next}
    found && /mountPath:/ {sub(/^[[:space:]]*mountPath:[[:space:]]*/,""); gsub(/"/,""); print; exit}')"
  [ -n "$mount" ] || return 3
  printf '%s\n' "$mount"
}

# validate <file> -- runs envoy --mode validate on it; returns envoy's exit code.
validate() {
  local lim=()
  command -v timeout >/dev/null 2>&1 && lim=(timeout 120)
  "${lim[@]}" docker run --rm -v "$1:$config_path:ro" "$image" \
    --mode validate -c "$config_path" >"$tmp/validate.log" 2>&1
}

# --- 0. The harness itself is present ---------------------------------------
# Not a skip: a check that quietly does nothing is the bug this ticket exists to
# fix. If docker is unavailable, say so and fail.
if ! command -v docker >/dev/null 2>&1; then
  fail "docker is not installed: cannot validate the envoy config, and a silent skip would be a false pass"
  echo; echo "$fails check(s) failed" >&2; exit 1
fi
if ! docker info >/dev/null 2>&1; then
  fail "docker is not usable (daemon down or no permission): cannot validate the envoy config"
  echo; echo "$fails check(s) failed" >&2; exit 1
fi

image="$(envoy_image)"
if [ -z "$image" ]; then
  die "no envoy image in the rendered Deployment: cannot tell which envoy to validate with"
fi
case "$image" in
  *:*) ;;
  *) die "the rendered envoy image '$image' has no tag: cannot tell which envoy version ships" ;;
esac
pass "validating with the chart's pinned envoy image ($image)"

config_path="$ENVOY_CMD_CONFIG"
mount_path="$(envoy_mount_path)" || case $? in
  2) die "the envoy Deployment overrides command/args: this check assumes the image CMD, so re-derive the config path before trusting it" ;;
  3) die "the envoy Deployment mounts no 'config' volume: envoy would start with no config" ;;
  *) die "the envoy Deployment does not render" ;;
esac
[ "$mount_path" = "$(dirname "$config_path")" ] \
  || die "the Deployment mounts envoy-config at '$mount_path' but envoy's CMD reads '$config_path': the pod would start with no config at all"
pass "the container mounts its config at $config_path (from the rendered Deployment)"

# --- 1. Negative control: the harness can FAIL -------------------------------
# The exact defect, minimal: an unescaped `%` in an inline_string. If envoy
# accepts this, the harness is broken and every "ok" below is worthless.
cat >"$tmp/control-bad.yaml" <<'EOF'
static_resources:
  listeners:
  - name: listener_0
    address: { socket_address: { address: 0.0.0.0, port_value: 8080 } }
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: ingress_http
          local_reply_config:
            mappers:
            - filter:
                status_code_filter:
                  comparison: { op: EQ, value: { default_value: 429 } }
              body_format_override:
                content_type: "text/html; charset=utf-8"
                text_format_source:
                  inline_string: "<style>main { width: 100%; }</style>"
          route_config:
            name: local_route
            virtual_hosts:
            - name: backend
              domains: ["*"]
              routes:
              - match: { prefix: "/" }
                direct_response: { status: 200 }
          http_filters:
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
EOF
chmod 644 "$tmp/control-bad.yaml"
if validate "$tmp/control-bad.yaml"; then
  fail "envoy ACCEPTED a config with an unescaped '%' in an inline_string: this check cannot detect the #5420 defect"
else
  pass "negative control: envoy rejects an unescaped '%' in an inline_string (the #5420 defect)"
fi

# --- 2. Negative control, escaped: the same config with %% is accepted -------
sed 's/width: 100%;/width: 100%%;/' "$tmp/control-bad.yaml" >"$tmp/control-good.yaml"
chmod 644 "$tmp/control-good.yaml"
if validate "$tmp/control-good.yaml"; then
  pass "negative control: envoy accepts the same config with the '%' escaped as '%%'"
else
  fail "envoy rejected the escaped control config: the escape is not the fix, or the harness is wrong"
  sed 's/^/    /' "$tmp/validate.log" >&2
fi

# --- 3. The config this chart actually ships --------------------------------
envoy_yaml >"$tmp/envoy.yaml" || die "cannot decode envoy.yaml from the rendered ConfigMap"
chmod 644 "$tmp/envoy.yaml"
if [ ! -s "$tmp/envoy.yaml" ]; then
  fail "no envoy-config ConfigMap in the render: nothing to validate"
else
  # A mis-decode that still produced text would make everything below mean
  # nothing, so pin the one line a correct decode always starts with.
  head -1 "$tmp/envoy.yaml" | grep -q '^static_resources:$' \
    || die "the decoded envoy.yaml does not start with static_resources: -- the decode is wrong, so validating it would prove nothing"
  lines="$(wc -l <"$tmp/envoy.yaml" | tr -d ' ')"
  # Both the exit code AND the OK line: a wrapper, a crashed container or a
  # truncated run can leave a zero exit with no verdict.
  if validate "$tmp/envoy.yaml" && grep -q "configuration '$config_path' OK" "$tmp/validate.log"; then
    pass "envoy --mode validate accepts the rendered config ($lines lines)"
  else
    fail "envoy --mode validate REJECTS the rendered config: this chart cannot be deployed"
    # The message names the offset inside the format string, which is not an
    # offset into any file -- print it whole so the cause is readable in the log.
    sed 's/^/    /' "$tmp/validate.log" >&2
  fi
fi

# --- 3b. Negative control on the SHIPPED bytes ------------------------------
#
# Controls 1-2 are hand-written configs, so they prove the harness works but not
# that the shipped file is what is being judged. This puts one unescaped `%` into
# THIS render's own local-reply body: content-independent (it does not depend on
# what the page says), it exercises the real bytes, and it cannot rot into a
# silent pass. If it ever stops failing, either the body moved or the check lost
# its teeth, and both are worth a red build.
awk '
  { print }
  /body_format_override:/ {inmap=1}
  inmap && /inline_string: \|$/ && !done {print "                    .nc{width:100%;}"; done=1}
  ' "$tmp/envoy.yaml" >"$tmp/envoy-unescaped.yaml"
chmod 644 "$tmp/envoy-unescaped.yaml"
if ! grep -q '^ *\.nc{width:100%;}$' "$tmp/envoy-unescaped.yaml"; then
  fail "could not build the shipped-bytes control: no inline_string in the render to inject a '%' into (has the local reply moved? re-point this control, do not delete it)"
elif validate "$tmp/envoy-unescaped.yaml"; then
  fail "envoy ACCEPTED the shipped config with an unescaped '%' injected into its inline_string: this check does not catch the #5420 defect"
else
  pass "negative control: envoy rejects an unescaped '%' in this render's own inline_string"
fi

# --- 4. The config that ships with the limiter off ---------------------------
# Nothing should be validated only in its default shape: this is the render a
# values override produces in the cluster.
envoy_yaml --set envoy.dexLoginRateLimit.enabled=false >"$tmp/envoy-off.yaml" \
  || die "the chart no longer renders with envoy.dexLoginRateLimit.enabled=false"
chmod 644 "$tmp/envoy-off.yaml"
if [ ! -s "$tmp/envoy-off.yaml" ]; then
  fail "the chart no longer renders with envoy.dexLoginRateLimit.enabled=false"
elif validate "$tmp/envoy-off.yaml"; then
  pass "envoy --mode validate accepts the render with the rate limiter disabled"
else
  fail "envoy --mode validate REJECTS the render with the rate limiter disabled"
  sed 's/^/    /' "$tmp/validate.log" >&2
fi

echo
if [ "$fails" -ne 0 ]; then
  echo "$fails check(s) failed" >&2
  exit 1
fi
echo "the rendered envoy config loads in envoy $image"
