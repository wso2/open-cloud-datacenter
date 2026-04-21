#!/usr/bin/env bash
# Namespace + cluster credential reconciler.
#
# Two watch loops run in parallel:
#
# 1. Namespace watch (Harvester)
#    Watches tenant namespaces on the Harvester cluster. For each new namespace
#    that belongs to a Rancher project it creates:
#      - ServiceAccount harvester-cloud-provider-<ns>
#      - RoleBinding to harvesterhci.io:cloudprovider in the tenant namespace
#      - Optional RoleBinding to view in the project's network namespace
#      - Long-lived SA token secret
#    On namespace deletion it deletes any harvesterconfig-* secrets on Rancher
#    whose kubeconfig was built from that namespace's SA token.
#
# 2. Cluster watch (Rancher)
#    Watches clusters.provisioning.cattle.io on the Rancher cluster. For each
#    new cluster with cloud-provider-name: harvester it:
#      - Resolves the vm_namespace from the machine config
#      - Checks the SA token exists (i.e. we manage that namespace)
#      - Creates harvesterconfig-<cluster-name> in Rancher's fleet-default with
#        v2prov-secret-authorized-for-cluster already set at creation time
#    On cluster deletion it removes harvesterconfig-<cluster-name>.
#
# Consumers (tenant teams) only need the rancher2 provider. No Harvester or
# Rancher kubeconfig required on their side.
#
# Environment variables (injected by the Deployment):
#   HARVESTER_API_SERVER  — Harvester Kubernetes API server URL
#   RANCHER_KUBECONFIG    — Path to kubeconfig for Rancher's local cluster

set -euo pipefail

FLEET_DEFAULT="fleet-default"
HARVESTER_API_SERVER="${HARVESTER_API_SERVER}"
RANCHER_KUBECONFIG="${RANCHER_KUBECONFIG}"

PROCESSED_NS_FILE=$(mktemp)
PROCESSED_CLUSTERS_FILE=$(mktemp)

SYSTEM_PREFIXES=(
  kube-
  cattle-
  harvester-
  longhorn-
  fleet-
  cluster-fleet-
  local-
  monitoring-
  logging-
)

# ── Utilities ─────────────────────────────────────────────────────────────────

log() { echo "[$(date -u +%H:%M:%S)] $*"; }

kubectl_rancher() { kubectl --kubeconfig "$RANCHER_KUBECONFIG" "$@"; }

is_system_namespace() {
  local ns="$1"
  for prefix in "${SYSTEM_PREFIXES[@]}"; do
    [[ "$ns" == "${prefix}"* ]] && return 0
  done
  case "$ns" in
    default|kube-node-lease|kube-public) return 0 ;;
  esac
  return 1
}

# Build and write harvesterconfig-<name> to Rancher fleet-default.
# Args: secret_name  cluster_name  vm_namespace
write_harvesterconfig() {
  local secret_name="$1" cluster_name="$2" vm_namespace="$3"
  local sa_name="harvester-cloud-provider-${vm_namespace}"

  # Get SA token from Harvester.
  local token=""
  for _ in $(seq 1 20); do
    token=$(kubectl get secret "${sa_name}-token" -n "$vm_namespace" \
      -o jsonpath='{.data.token}' 2>/dev/null || true)
    [[ -n "$token" ]] && break
    sleep 1
  done
  if [[ -z "$token" ]]; then
    log "  ERROR: token not populated for ${sa_name} in ${vm_namespace}"
    return 1
  fi

  local token_decoded ca_cert_b64
  token_decoded=$(echo "$token" | base64 -d)
  ca_cert_b64=$(kubectl get configmap kube-root-ca.crt -n kube-system \
    -o jsonpath='{.data.ca\.crt}' | base64 | tr -d '\n')

  local kubeconfig
  kubeconfig=$(cat <<EOF
apiVersion: v1
kind: Config
clusters:
- name: default
  cluster:
    certificate-authority-data: ${ca_cert_b64}
    server: ${HARVESTER_API_SERVER}
users:
- name: default
  user:
    token: ${token_decoded}
contexts:
- name: default
  context:
    cluster: default
    namespace: ${vm_namespace}
    user: default
current-context: default
EOF
)

  kubectl_rancher apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: ${secret_name}
  namespace: ${FLEET_DEFAULT}
  annotations:
    v2prov-authorized-secret-deletes-on-cluster-removal: "true"
    v2prov-secret-authorized-for-cluster: "${cluster_name}"
    platform.wso2.com/credential-source-namespace: "${vm_namespace}"
type: secret
stringData:
  credential: |
$(echo "$kubeconfig" | sed 's/^/    /')
EOF
}

# ── Namespace watch handlers ───────────────────────────────────────────────────

on_added_namespace() {
  local ns="$1" project_id="$2"
  local sa_name="harvester-cloud-provider-${ns}"

  # Skip if SA token already exists — fully provisioned.
  if kubectl get secret "${sa_name}-token" -n "$ns" \
      -o jsonpath='{.data.token}' 2>/dev/null | grep -q .; then
    log "  [ns] already provisioned: ${ns} — skipping"
    return
  fi

  log "  [ns] provisioning SA for namespace: ${ns}"

  # ServiceAccount in tenant namespace.
  kubectl create serviceaccount "$sa_name" -n "$ns" \
    --dry-run=client -o yaml | kubectl apply -f -

  # RoleBinding — write access for VM provisioning.
  kubectl create rolebinding "${sa_name}" \
    --clusterrole=harvesterhci.io:cloudprovider \
    --serviceaccount="${ns}:${sa_name}" \
    -n "$ns" --dry-run=client -o yaml | kubectl apply -f -

  # Optional: read access to NADs in the project's network namespace.
  local net_ns
  net_ns=$(kubectl get namespaces -o json 2>/dev/null \
    | jq -r --arg pid "${project_id}" '
        .items[] |
        select(.metadata.annotations["field.cattle.io/projectId"] == $pid) |
        select(.metadata.labels["platform.wso2.com/role"] == "network-namespace") |
        .metadata.name
      ' | head -1 || true)
  if [[ -n "$net_ns" ]]; then
    kubectl create rolebinding "${sa_name}-net-read" \
      --clusterrole=view \
      --serviceaccount="${ns}:${sa_name}" \
      -n "$net_ns" --dry-run=client -o yaml | kubectl apply -f -
    log "  [ns] granted read on network namespace: ${net_ns}"
  fi

  # Long-lived token secret.
  kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: ${sa_name}-token
  namespace: ${ns}
  annotations:
    kubernetes.io/service-account.name: ${sa_name}
type: kubernetes.io/service-account-token
EOF

  log "  [ns] SA ready: ${sa_name} in ${ns}"
}

on_deleted_namespace() {
  local ns="$1"
  local sa_name="harvester-cloud-provider-${ns}"

  # SA, token, and rolebindings in the deleted namespace are cleaned up by K8s.

  # Delete any harvesterconfig-* secrets on Rancher that were built from this
  # namespace's SA token. We use an exact annotation match rather than a
  # substring search of the kubeconfig body — substring matching would
  # incorrectly match prefix namespaces (e.g. "team-a" matching "team-a2").
  local stale_secrets
  stale_secrets=$(kubectl_rancher get secrets -n "$FLEET_DEFAULT" -o json 2>/dev/null \
    | jq -r --arg ns "$ns" '
        .items[] |
        select(.metadata.name | startswith("harvesterconfig-")) |
        select(.metadata.annotations["platform.wso2.com/credential-source-namespace"] == $ns) |
        .metadata.name
      ' || true)

  if [[ -n "$stale_secrets" ]]; then
    while IFS= read -r sname; do
      [[ -z "$sname" ]] && continue
      kubectl_rancher delete secret "$sname" -n "$FLEET_DEFAULT" 2>/dev/null \
        && log "  [ns] deleted stale Rancher secret: ${sname}" || true
    done <<< "$stale_secrets"
  fi

  # Delete net-read RoleBinding from any namespace that still has it.
  local rb_name="${sa_name}-net-read"
  kubectl get rolebinding "$rb_name" --all-namespaces -o json 2>/dev/null \
    | jq -r '.items[] | .metadata.namespace + " " + .metadata.name' \
    | while read -r rb_ns rb_name_found; do
        kubectl delete rolebinding "$rb_name_found" -n "$rb_ns" 2>/dev/null \
          && log "  [ns] deleted rolebinding ${rb_name_found} from ${rb_ns}"
      done || true
}

# ── Cluster watch handlers ─────────────────────────────────────────────────────

on_added_cluster() {
  local cluster_name="$1"
  local secret_name="harvesterconfig-${cluster_name}"

  if kubectl_rancher get secret "$secret_name" -n "$FLEET_DEFAULT" &>/dev/null; then
    log "  [cluster] already exists: ${secret_name} — skipping"
    return
  fi


  # Resolve vm_namespace from the first machine pool's machine config.
  local machine_config_name vm_namespace
  machine_config_name=$(kubectl_rancher get clusters.provisioning.cattle.io \
    "$cluster_name" -n "$FLEET_DEFAULT" \
    -o jsonpath='{.spec.rkeConfig.machinePools[0].machineConfigRef.name}' 2>/dev/null || true)

  if [[ -z "$machine_config_name" ]]; then
    log "  [cluster] no machine config ref for ${cluster_name} — skipping"
    return
  fi

  vm_namespace=$(kubectl_rancher get harvesterconfigs.rke-machine-config.cattle.io \
    "$machine_config_name" -n "$FLEET_DEFAULT" \
    -o jsonpath='{.vmNamespace}' 2>/dev/null || true)

  if [[ -z "$vm_namespace" ]]; then
    log "  [cluster] could not resolve vm_namespace for ${cluster_name} — skipping"
    return
  fi

  # Only provision if we manage this namespace (SA token exists on Harvester).
  local sa_name="harvester-cloud-provider-${vm_namespace}"
  if ! kubectl get secret "${sa_name}-token" -n "$vm_namespace" &>/dev/null; then
    log "  [cluster] namespace ${vm_namespace} not managed by this reconciler — skipping"
    return
  fi

  log "  [cluster] provisioning ${secret_name} for cluster ${cluster_name} (namespace: ${vm_namespace})"
  write_harvesterconfig "$secret_name" "$cluster_name" "$vm_namespace" \
    && log "  [cluster] created: ${secret_name} in Rancher ${FLEET_DEFAULT}"
}

on_deleted_cluster() {
  local cluster_name="$1"
  local secret_name="harvesterconfig-${cluster_name}"

  # Rancher may already have cleaned this up via
  # v2prov-authorized-secret-deletes-on-cluster-removal — only delete if present.
  if kubectl_rancher get secret "$secret_name" -n "$FLEET_DEFAULT" &>/dev/null; then
    kubectl_rancher delete secret "$secret_name" -n "$FLEET_DEFAULT" \
      && log "  [cluster] deleted: ${secret_name} from Rancher ${FLEET_DEFAULT}"
  fi
}

# ── Namespace watch loop ───────────────────────────────────────────────────────

namespace_watch_loop() {
  # Reconnect reconcile: clean up namespaces that disappeared while disconnected.
  # Only act on explicit "not found" — skip on API errors to avoid false deletes.
  local snap tracked_ns
  snap=$(cat "$PROCESSED_NS_FILE" 2>/dev/null || true)
  while IFS= read -r tracked_ns; do
    [[ -z "$tracked_ns" ]] && continue
    local out
    out=$(kubectl get namespace "$tracked_ns" 2>&1)
    local rc=$?
    if [[ $rc -ne 0 ]]; then
      if echo "$out" | grep -qiE '"?not found"?|NotFound'; then
        sed -i "/^${tracked_ns}$/d" "$PROCESSED_NS_FILE"
        log "DELETED namespace: ${tracked_ns} — cleaning up (reconnect)"
        on_deleted_namespace "$tracked_ns"
      else
        log "  [reconnect] cannot verify namespace ${tracked_ns} (API error) — skipping cleanup"
      fi
    fi
  done <<< "$snap"

  kubectl get namespaces --watch --request-timeout=0 -o json 2>/dev/null \
    | jq --unbuffered -r '[
        .metadata.name,
        (.metadata.annotations["field.cattle.io/projectId"] // ""),
        (.metadata.labels["platform.wso2.com/role"] // ""),
        (.metadata.deletionTimestamp // "")
      ] | join("\u0001")' \
    | while IFS=$'\x01' read -r ns project_id role deletion_ts; do

        is_system_namespace "$ns" && continue
        [[ "$role" == "network-namespace" ]] && continue

        if [[ -n "$deletion_ts" ]]; then
          if grep -qxF "$ns" "$PROCESSED_NS_FILE" 2>/dev/null; then
            sed -i "/^${ns}$/d" "$PROCESSED_NS_FILE"
            log "DELETED namespace: ${ns}"
            on_deleted_namespace "$ns"
          fi
        else
          [[ -z "$project_id" ]] && continue
          if ! grep -qxF "$ns" "$PROCESSED_NS_FILE" 2>/dev/null; then
            echo "$ns" >> "$PROCESSED_NS_FILE"
            log "ADDED namespace: ${ns} (project: ${project_id})"
            on_added_namespace "$ns" "$project_id"
          fi
        fi
      done
}

# ── Cluster watch loop ─────────────────────────────────────────────────────────

cluster_watch_loop() {
  # Reconnect reconcile: clean up clusters that disappeared while disconnected.
  # Only act when Rancher explicitly returns "not found" — any other error
  # (connection refused, timeout, server error) means the API is temporarily
  # unavailable and we must NOT delete secrets for still-running clusters.
  local snap tracked_cluster
  snap=$(cat "$PROCESSED_CLUSTERS_FILE" 2>/dev/null || true)
  while IFS= read -r tracked_cluster; do
    [[ -z "$tracked_cluster" ]] && continue
    local out
    out=$(kubectl_rancher get clusters.provisioning.cattle.io \
        "$tracked_cluster" -n "$FLEET_DEFAULT" 2>&1)
    local rc=$?
    if [[ $rc -ne 0 ]]; then
      if echo "$out" | grep -qiE '"?not found"?|NotFound'; then
        sed -i "/^${tracked_cluster}$/d" "$PROCESSED_CLUSTERS_FILE"
        log "DELETED cluster: ${tracked_cluster} — cleaning up (reconnect)"
        on_deleted_cluster "$tracked_cluster"
      else
        log "  [reconnect] cannot verify cluster ${tracked_cluster} (API error) — skipping cleanup"
      fi
    fi
  done <<< "$snap"

  kubectl_rancher get clusters.provisioning.cattle.io \
    -n "$FLEET_DEFAULT" --watch --request-timeout=0 -o json 2>/dev/null \
    | jq --unbuffered -r '[
        .metadata.name,
        (.metadata.deletionTimestamp // ""),
        ([ .spec.rkeConfig.machineSelectorConfig[]?.config
           | select(. != null)
           | select(
               (type == "string" and contains("cloud-provider-name: harvester")) or
               (type == "object" and .["cloud-provider-name"] == "harvester")
             )
         ] | length | if . > 0 then "true" else "" end)
      ] | join("\u0001")' \
    | while IFS=$'\x01' read -r cluster_name deletion_ts has_harvester; do

        [[ -z "$cluster_name" ]] && continue
        # Skip Rancher's own local/imported clusters.
        [[ "$cluster_name" == "local" ]] && continue

        if [[ -n "$deletion_ts" ]]; then
          if grep -qxF "$cluster_name" "$PROCESSED_CLUSTERS_FILE" 2>/dev/null; then
            sed -i "/^${cluster_name}$/d" "$PROCESSED_CLUSTERS_FILE"
            log "DELETED cluster: ${cluster_name}"
            on_deleted_cluster "$cluster_name"
          fi
        else
          [[ -z "$has_harvester" ]] && continue
          if ! grep -qxF "$cluster_name" "$PROCESSED_CLUSTERS_FILE" 2>/dev/null; then
            echo "$cluster_name" >> "$PROCESSED_CLUSTERS_FILE"
            log "ADDED cluster: ${cluster_name}"
            on_added_cluster "$cluster_name"
          fi
        fi
      done
}

# ── Startup ────────────────────────────────────────────────────────────────────

log "Reconciler starting (harvester API: ${HARVESTER_API_SERVER})"
log "Running initial namespace pass..."

kubectl get namespaces -o json | jq -r '
  .items[] |
  [
    .metadata.name,
    (.metadata.annotations["field.cattle.io/projectId"] // ""),
    (.metadata.labels["platform.wso2.com/role"] // "")
  ] | join("\u0001")
' | while IFS=$'\x01' read -r ns project_id role; do
  [[ -z "$project_id" ]] && continue
  is_system_namespace "$ns" && continue
  [[ "$role" == "network-namespace" ]] && continue
  echo "$ns" >> "$PROCESSED_NS_FILE"
  log "INIT namespace: ${ns} (project: ${project_id})"
  on_added_namespace "$ns" "$project_id"
done

log "Running initial cluster pass..."

kubectl_rancher get clusters.provisioning.cattle.io \
  -n "$FLEET_DEFAULT" -o json 2>/dev/null \
  | jq -r '
      .items[] |
      select(.metadata.name != "local") |
      select([ .spec.rkeConfig.machineSelectorConfig[]?.config
               | select(. != null)
               | select(
                   (type == "string" and contains("cloud-provider-name: harvester")) or
                   (type == "object" and .["cloud-provider-name"] == "harvester")
                 )
             ] | length > 0) |
      .metadata.name
    ' \
  | while IFS= read -r cluster_name; do
      echo "$cluster_name" >> "$PROCESSED_CLUSTERS_FILE"
      log "INIT cluster: ${cluster_name}"
      on_added_cluster "$cluster_name"
    done

log "Initial passes complete. Starting watch loops..."

while true; do
  log "Starting namespace watch loop..."
  namespace_watch_loop || true
  log "Namespace watch loop exited, restarting in 5s..."
  sleep 5
done &
NAMESPACE_WATCH_PID=$!

while true; do
  log "Starting cluster watch loop..."
  cluster_watch_loop || true
  log "Cluster watch loop exited, restarting in 5s..."
  sleep 5
done &
CLUSTER_WATCH_PID=$!

wait $NAMESPACE_WATCH_PID $CLUSTER_WATCH_PID
