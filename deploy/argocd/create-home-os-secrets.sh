#!/usr/bin/env bash
# Creates (or updates) the Secrets the home-os Argo app references via
# existingSecret. Values are read from the environment or prompted silently;
# nothing is echoed. Run against the Talos homelab only, e.g.
#   KUBECTL="kubectl --context admin@talos-homelab" ./create-home-os-secrets.sh
#
# Required env (prompted when unset):
#   HOMEOS_JWT_SECRET HOMEOS_CALENDAR_SECRET HOMEOS_TYPESENSE_API_KEY
#   HOMEOS_ENCRYPTION_KEY HOMEOS_ADMIN_EMAIL HOMEOS_ADMIN_PASSWORD
#   HOMEOS_DB_PASSWORD   (URL-safe; must equal the CNPG app user's password
#                         on an existing cluster)
#
# The Secrets are written with server-side apply, so no
# kubectl.kubernetes.io/last-applied-configuration annotation (which would
# hold a copy of every value) is created. Secrets created by an older version
# of this script (client-side apply) carry that annotation; the first run of
# this version takes over their fields and drops it (tested on k8s 1.31,
# kubectl 1.31). If it is still there afterwards, remove it by hand:
#   kubectl --context admin@talos-homelab -n home-os annotate secret home-os-secrets kubectl.kubernetes.io/last-applied-configuration-
#   kubectl --context admin@talos-homelab -n home-os annotate secret home-os-db-secret kubectl.kubernetes.io/last-applied-configuration-
set -euo pipefail

KUBECTL=${KUBECTL:-"kubectl --context admin@talos-homelab"}
NS=home-os
EXPECTED_CONTEXT=admin@talos-homelab

# Refuse to run against anything but the Talos homelab, whatever KUBECTL says.
ctx=$($KUBECTL config view --minify -o jsonpath='{.current-context}' 2>/dev/null || true)
if [ "$ctx" != "$EXPECTED_CONTEXT" ]; then
  echo "refusing to run: kube context is '${ctx:-<none>}', expected $EXPECTED_CONTEXT" >&2
  exit 1
fi

ask() {
  local var=$1
  if [ -z "${!var:-}" ]; then
    read -r -s -p "$var: " "${var?}"; echo >&2
  fi
  [ -n "${!var}" ] || { echo "$var is empty" >&2; exit 1; }
}

for v in HOMEOS_JWT_SECRET HOMEOS_CALENDAR_SECRET HOMEOS_TYPESENSE_API_KEY \
         HOMEOS_ENCRYPTION_KEY HOMEOS_ADMIN_EMAIL HOMEOS_ADMIN_PASSWORD HOMEOS_DB_PASSWORD; do
  ask "$v"
done

$KUBECTL create namespace "$NS" --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null

# Server-side apply: no last-applied-configuration annotation copying the data.
# --force-conflicts takes over fields owned by the old client-side apply; this
# script is the only writer of these Secrets.
$KUBECTL -n "$NS" create secret generic home-os-secrets \
  --from-literal=jwtSecret="$HOMEOS_JWT_SECRET" \
  --from-literal=calendarSecret="$HOMEOS_CALENDAR_SECRET" \
  --from-literal=typesenseApiKey="$HOMEOS_TYPESENSE_API_KEY" \
  --from-literal=encryptionKey="$HOMEOS_ENCRYPTION_KEY" \
  --from-literal=adminEmail="$HOMEOS_ADMIN_EMAIL" \
  --from-literal=adminPassword="$HOMEOS_ADMIN_PASSWORD" \
  --dry-run=client -o yaml |
  $KUBECTL apply --server-side --force-conflicts --field-manager=home-os-secrets -f - >/dev/null

$KUBECTL -n "$NS" create secret generic home-os-db-secret \
  --type=kubernetes.io/basic-auth \
  --from-literal=username=app \
  --from-literal=password="$HOMEOS_DB_PASSWORD" \
  --dry-run=client -o yaml |
  $KUBECTL apply --server-side --force-conflicts --field-manager=home-os-secrets -f - >/dev/null

for s in home-os-secrets home-os-db-secret; do
  $KUBECTL -n "$NS" annotate secret "$s" --overwrite \
    argocd.argoproj.io/sync-options=Prune=false >/dev/null
done

echo "home-os-secrets and home-os-db-secret are in namespace $NS."
