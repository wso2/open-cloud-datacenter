#!/usr/bin/env bash
# Source this to populate DCAPI_* env vars from a running dc-api deployment's
# Secret + ConfigMap, so a locally-built dc-api uses the same backend config
# (OIDC / Harvester / Rancher / KubeOVN) as the cluster — without you copying
# any of it by hand. It then applies dev-friendly overrides: the DB URL points
# at a port-forwarded Postgres, the listen port moves off :8080, and the log
# level drops to debug.
#
# Required:
#   DCAPI_DEV_CONTEXT   kube context of the cluster dc-api runs in
# Optional (sensible defaults for the flux/platform layout):
#   DCAPI_DEV_NAMESPACE   namespace dc-api is deployed in        (default: dc-system)
#   DCAPI_DEV_SECRET      name of the dc-api Secret              (default: dc-api-secrets)
#   DCAPI_DEV_CONFIGMAP   name of the dc-api ConfigMap           (default: dc-api-config)
#
# Usage (standalone):
#   export DCAPI_DEV_CONTEXT=<your-context>
#   source docs/dev/dc-api-local-env.sh   # then run dc-api against :15432
# Normally you don't call this directly — scripts/dev-up.sh sources it for you.
#
# Companion: docs/dev/local-dc-api.md

set -euo pipefail

CTX="${DCAPI_DEV_CONTEXT:?set DCAPI_DEV_CONTEXT to the kube context of your dev cluster}"
NS="${DCAPI_DEV_NAMESPACE:-dc-system}"
SECRET="${DCAPI_DEV_SECRET:-dc-api-secrets}"
CONFIGMAP="${DCAPI_DEV_CONFIGMAP:-dc-api-config}"

ENVFILE=$(mktemp)
trap "rm -f $ENVFILE" EXIT

# Secret — base64-decode each value and emit `export KEY=<single-quoted>`.
# Single quotes preserve newlines + special chars inside multi-line values
# like an embedded kubeconfig. `'\''` escapes any literal single quote.
kubectl --context "$CTX" -n "$NS" get secret "$SECRET" -o json \
  | jq -r '.data | to_entries[] | "\(.key)\t\(.value)"' \
  | while IFS=$'\t' read -r k v; do
      decoded=$(printf '%s' "$v" | base64 -d)
      escaped=$(printf '%s' "$decoded" | sed "s/'/'\\\\''/g")
      printf "export %s='%s'\n" "$k" "$escaped" >> "$ENVFILE"
    done

# ConfigMap — same pattern, plain (non-base64) values.
kubectl --context "$CTX" -n "$NS" get configmap "$CONFIGMAP" -o json \
  | jq -r '.data | to_entries[] | "\(.key)\t\(.value)"' \
  | while IFS=$'\t' read -r k v; do
      escaped=$(printf '%s' "$v" | sed "s/'/'\\\\''/g")
      printf "export %s='%s'\n" "$k" "$escaped" >> "$ENVFILE"
    done

source "$ENVFILE"

# Dev overrides — port-forwarded Postgres, off-:8080 listen, debug logs.
# (scripts/dev-up.sh overrides these again to suit each mode.)
PG_PW=$(printf '%s' "$DCAPI_DB_URL" | sed -E 's|.*dc_api:([^@]+)@.*|\1|')
export DCAPI_DB_URL="postgres://dc_api:$PG_PW@localhost:15432/dc_api?sslmode=disable"
export DCAPI_LISTEN_ADDR=":18080"
export DCAPI_LOG_LEVEL=debug

echo "env loaded — $(env | grep -c '^DCAPI_') DCAPI_* vars from $CTX/$NS"
echo "  DB → localhost:15432 (port-forward), listen → :18080, log → debug"
