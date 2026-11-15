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
set -euo pipefail

KUBECTL=${KUBECTL:-"kubectl --context admin@talos-homelab"}
NS=home-os

ask() {
  local var=$1
  if [ -z "${!var:-}" ]; then
    read -r -s -p "$var: " "$var"; echo >&2
  fi
  [ -n "${!var}" ] || { echo "$var is empty" >&2; exit 1; }
}

for v in HOMEOS_JWT_SECRET HOMEOS_CALENDAR_SECRET HOMEOS_TYPESENSE_API_KEY \
         HOMEOS_ENCRYPTION_KEY HOMEOS_ADMIN_EMAIL HOMEOS_ADMIN_PASSWORD HOMEOS_DB_PASSWORD; do
  ask "$v"
done

$KUBECTL create namespace "$NS" --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null

$KUBECTL -n "$NS" create secret generic home-os-secrets \
  --from-literal=jwtSecret="$HOMEOS_JWT_SECRET" \
  --from-literal=calendarSecret="$HOMEOS_CALENDAR_SECRET" \
  --from-literal=typesenseApiKey="$HOMEOS_TYPESENSE_API_KEY" \
  --from-literal=encryptionKey="$HOMEOS_ENCRYPTION_KEY" \
  --from-literal=adminEmail="$HOMEOS_ADMIN_EMAIL" \
  --from-literal=adminPassword="$HOMEOS_ADMIN_PASSWORD" \
  --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null

$KUBECTL -n "$NS" create secret generic home-os-db-secret \
  --type=kubernetes.io/basic-auth \
  --from-literal=username=app \
  --from-literal=password="$HOMEOS_DB_PASSWORD" \
  --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null

for s in home-os-secrets home-os-db-secret; do
  $KUBECTL -n "$NS" annotate secret "$s" --overwrite \
    argocd.argoproj.io/sync-options=Prune=false >/dev/null
done

echo "home-os-secrets and home-os-db-secret are in namespace $NS."
