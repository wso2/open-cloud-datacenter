# keyvault

Deploys the **keyvault-operator** (OpenBao HA orchestrator) onto a
Harvester RKE2 cluster. Sibling operators (DB, cache, registry) ship as
peer modules under `modules/operators/<name>/` as they land.

## Source of truth

This module derives from the keyvault-operator's `config/` directory
(kubebuilder layout) in the upstream source repo. The canonical
deployment path is:

```bash
kubectl kustomize crds/keyvault/config/default | kubectl apply -f -
# or equivalently:
make deploy   # in crds/keyvault/
```

The kustomize build entry point is `crds/keyvault/config/default/kustomization.yaml`.
The `namePrefix: kvi-` directive applies to all generated resource names.
This module re-expresses every resource in that canonical output as typed
Terraform so the same cluster state is managed without requiring kustomize
at apply time.

## How to refresh from upstream

When the operator releases a new version:

1. Rebuild the canonical output:
   ```bash
   kubectl kustomize crds/keyvault/config/default
   ```
2. Diff each resource (by `kind` + `name`) against the corresponding TF
   resource block in `main.tf`. Common drift areas:
   - New or changed RBAC rules in the manager ClusterRole
   - New container args from patches
   - Memory/CPU limit changes
   - New ports or volume mounts
3. Update the CRD YAML files under `crds/` alongside the operator image
   tag bump — CRD schema changes must travel with the image version.
4. Bump `kv_image_tag` in the consumer's `terraform.tfvars`.

## What it deploys (keyvault-operator)

Resource names below are the post-`kvi-`-prefix names that appear on the
cluster. These match `kubectl kustomize crds/keyvault/config/default`.

| Resource | Name | Kind |
|---|---|---|
| Namespace | `keyvault-system` (var) | `Namespace` |
| CRD | `keyvaultbackends.keyvault.opencloud.wso2.com` | `CustomResourceDefinition` |
| CRD | `keyvaultinstances.keyvault.opencloud.wso2.com` | `CustomResourceDefinition` |
| ServiceAccount | `kvi-controller-manager` | `ServiceAccount` |
| Role | `kvi-leader-election-role` | `Role` (namespaced) |
| RoleBinding | `kvi-leader-election-rolebinding` | `RoleBinding` (namespaced) |
| ClusterRole | `kvi-manager-role` | `ClusterRole` |
| ClusterRoleBinding | `kvi-manager-rolebinding` | `ClusterRoleBinding` |
| ClusterRole | `kvi-metrics-auth-role` | `ClusterRole` |
| ClusterRoleBinding | `kvi-metrics-auth-rolebinding` | `ClusterRoleBinding` |
| ClusterRole | `kvi-metrics-reader` | `ClusterRole` |
| ClusterRole | `kvi-keyvaultbackend-admin-role` | `ClusterRole` (helper) |
| ClusterRole | `kvi-keyvaultbackend-editor-role` | `ClusterRole` (helper) |
| ClusterRole | `kvi-keyvaultbackend-viewer-role` | `ClusterRole` (helper) |
| ClusterRole | `kvi-keyvaultinstance-admin-role` | `ClusterRole` (helper) |
| ClusterRole | `kvi-keyvaultinstance-editor-role` | `ClusterRole` (helper) |
| ClusterRole | `kvi-keyvaultinstance-viewer-role` | `ClusterRole` (helper) |
| Service | `kvi-controller-manager-metrics-service` | `Service` (port 8443/HTTPS) |
| Deployment | `kvi-controller-manager` | `Deployment` |
| Secret (optional) | `ghcr-pull-secret` | `kubernetes.io/dockerconfigjson` |

Optional resources (gated by boolean variables):

| Resource | Name | Controlled by |
|---|---|---|
| NetworkPolicy | `kvi-allow-metrics-traffic` | `enable_metrics_network_policy` |
| ServiceMonitor | `kvi-controller-manager-metrics-monitor` | `enable_prometheus_servicemonitor` |

CRD files live under `crds/` inside this module and are loaded at plan time
via `file()`. They must be kept in sync with the operator image version.

## Prerequisites

- Harvester RKE2 cluster running Kubernetes >= 1.28. This is the workload
  cluster that hosts the kvi-operator and OpenBao StatefulSets — not the
  dc-api management cluster.
- Pod Security Admission: the `keyvault-system` namespace is compatible with
  `enforce=restricted` (controller runs as non-root, drops ALL capabilities,
  read-only root filesystem, `seccompProfile: RuntimeDefault`). No label is
  added to the namespace by this module — the caller sets the PSA level.
- Longhorn (or another CSI) available on the cluster. The controller creates
  per-tenant `KeyVaultBackend` StatefulSets with PVCs; the storage class is
  specified on the `KeyVaultBackend` CR spec, not by this module.
- For `enable_prometheus_servicemonitor = true`: Prometheus Operator CRDs
  (`monitoring.coreos.com/v1`) must be installed (e.g. via kube-prometheus-stack).
- For `enable_cert_manager_metrics = true`: cert-manager must be installed
  and must have issued a `Certificate` named `metrics-certs` in the
  keyvault namespace, producing a Secret named `metrics-server-cert`.

## Usage

The caller creates a `kubernetes` provider pointing at the target cluster
and passes it via the `providers` block:

```hcl
provider "kubernetes" {
  alias       = "harvester_cluster"
  config_path = "/path/to/harvester-rke2.yaml"
}

module "keyvault_operator" {
  source = "github.com/wso2/open-cloud-datacenter//modules/operators/keyvault?ref=terraform/v0.2.0"

  providers = {
    kubernetes = kubernetes.harvester_cluster
  }

  kv_image     = "ghcr.io/wso2/keyvault-operator"
  kv_image_tag = "v0.1.0"
  ghcr_username = var.ghcr_username
  ghcr_pat      = var.ghcr_pat

  # Optional — off by default; enable only when the cluster has these operators
  # enable_metrics_network_policy    = true
  # enable_prometheus_servicemonitor = true
  # enable_cert_manager_metrics      = true
}
```

## Inputs

| Name | Type | Default | Description |
|---|---|---|---|
| `kv_namespace` | `string` | `"keyvault-system"` | Namespace the keyvault-operator runs in |
| `kv_image` | `string` | `"ghcr.io/wso2/keyvault-operator"` | Image registry path, no tag |
| `kv_image_tag` | `string` | `"v0.0.1"` | Pinned image tag |
| `ghcr_username` | `string` | `""` | GHCR username; leave empty to skip pull secret |
| `ghcr_pat` | `string` (sensitive) | `""` | GHCR PAT; leave empty to skip pull secret |
| `enable_metrics_network_policy` | `bool` | `false` | Create NetworkPolicy gating /metrics ingress to `metrics: enabled` namespaces |
| `enable_prometheus_servicemonitor` | `bool` | `false` | Create ServiceMonitor (requires Prometheus Operator CRDs) |
| `enable_cert_manager_metrics` | `bool` | `false` | Mount cert-manager TLS certs into the metrics endpoint (requires cert-manager + a Certificate CR named `metrics-certs`) |

## Outputs

| Name | Description |
|---|---|
| `kv_namespace` | Namespace the controller is deployed into |
| `kv_deployment_name` | Name of the controller-manager Deployment |
| `kv_image` | Fully-qualified `image:tag` used by the Deployment |

## Image lifecycle

The Deployment carries `lifecycle { ignore_changes = [... container[0].image] }`.
This means TF seeds the initial image on first apply, then CI owns rolling it
forward via `kubectl set image`. To force TF to pin a specific tag again,
remove the resource from state and re-apply, or explicitly bump `kv_image_tag`
after removing the lifecycle block temporarily.

## Composition with upstream layers

This module sits downstream of `harvester-integration` (which registers the
cluster with Rancher) and `dc-controlplane` (which creates the RKE2 cluster).
The consumer layer wires the kubernetes provider from the cluster's kubeconfig
output, exactly as `dc-controlplane-services` does today.

## Deviations from kustomize build output

| kustomize | This module | Reason |
|---|---|---|
| `managed-by: kustomize` | `managed-by: terraform` | Reflects actual deployment tool |
| `imagePullSecrets` always absent | Conditional on `ghcr_username`+`ghcr_pat` | Allows pull-secret injection without altering the canonical YAML |
| Image `ghcr.io/wso2/keyvault-operator:v0.0.1` | `var.kv_image:var.kv_image_tag` | Caller-controlled; not pinned in module |
| `kvi-manager-role` rules: no `namespaces` resource | `namespaces` added | KVI controller creates per-tenant namespaces; this is an intentional addition to the kubebuilder scaffold |
| `memory: 128Mi` limit (canonical) | `128Mi` | Aligned with canonical; prior module had 256Mi |
| NetworkPolicy: commented out in kustomize | Optional via `enable_metrics_network_policy` | Preserves kustomize default (off) while making it available |
| ServiceMonitor: commented out in kustomize | Optional via `enable_prometheus_servicemonitor` | Avoids hard dependency on Prometheus Operator |
| cert-manager patches: commented out in kustomize | Optional via `enable_cert_manager_metrics` | Avoids hard dependency on cert-manager |
