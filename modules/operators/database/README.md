# database

Deploys the **dbaas-operator** (RDS-style managed PostgreSQL via Harvester
KubeVirt VMs) onto a Harvester RKE2 cluster. Sibling of
`modules/operators/keyvault`.

## Source of truth

This module derives from the dbaas-operator's `config/` directory (kubebuilder
layout) on the `operators` branch of
`github.com/wso2/open-cloud-datacenter`. The canonical deployment path is:

```bash
kubectl kustomize database/config/default | kubectl apply -f -
# or equivalently, from database/:
make deploy
```

The kustomize build entry point is `database/config/default/kustomization.yaml`.
The `namePrefix: dbaas-` directive applies to all generated resource names.
This module re-expresses every resource in that canonical output as typed
Terraform so the same cluster state is managed without requiring kustomize
at apply time.

## How to refresh from upstream

When the operator releases a new version:

1. Check out the `operators` branch of `open-cloud-datacenter`.
2. Rebuild the canonical output:
   ```bash
   kubectl kustomize database/config/default
   ```
3. Diff each resource (by `kind` + `name`) against the corresponding TF
   resource block in `main.tf`. Common drift areas:
   - New or changed RBAC rules in `dbaas-manager-role`
   - New container args from `manager_metrics_patch.yaml` or future patches
   - Memory/CPU limit changes
   - New ports or volume mounts
4. Update `crds/crd-dbinstances.yaml` alongside the operator image tag bump
   — CRD schema changes must travel with the image version.
5. Bump `db_image_tag` in the consumer's `terraform.tfvars`.

## What it deploys (dbaas-operator)

Resource names below are the post-`dbaas-`-prefix names that appear on the
cluster. These match `kubectl kustomize database/config/default`.

| Resource | Name | Kind |
|---|---|---|
| Namespace | `dbaas-system` (var) | `Namespace` |
| CRD | `dbinstances.dbaas.opencloud.wso2.com` | `CustomResourceDefinition` |
| ServiceAccount | `dbaas-controller-manager` | `ServiceAccount` |
| Role | `dbaas-leader-election-role` | `Role` (namespaced) |
| RoleBinding | `dbaas-leader-election-rolebinding` | `RoleBinding` (namespaced) |
| ClusterRole | `dbaas-manager-role` | `ClusterRole` |
| ClusterRoleBinding | `dbaas-manager-rolebinding` | `ClusterRoleBinding` |
| ClusterRole | `dbaas-metrics-auth-role` | `ClusterRole` |
| ClusterRoleBinding | `dbaas-metrics-auth-rolebinding` | `ClusterRoleBinding` |
| ClusterRole | `dbaas-metrics-reader` | `ClusterRole` |
| ClusterRole | `dbaas-dbinstance-admin-role` | `ClusterRole` (aggregates to `admin`) |
| ClusterRole | `dbaas-dbinstance-editor-role` | `ClusterRole` (aggregates to `edit`) |
| ClusterRole | `dbaas-dbinstance-viewer-role` | `ClusterRole` (aggregates to `view`) |
| Service | `dbaas-controller-manager-metrics-service` | `Service` (port 8443/HTTPS) |
| Deployment | `dbaas-controller-manager` | `Deployment` |
| Secret (optional) | `ghcr-pull-secret` | `kubernetes.io/dockerconfigjson` |

Optional resources (gated by boolean variables):

| Resource | Name | Controlled by |
|---|---|---|
| NetworkPolicy | `dbaas-allow-metrics-traffic` | `enable_metrics_network_policy` |
| ServiceMonitor | `dbaas-controller-manager-metrics-monitor` | `enable_prometheus_servicemonitor` |

CRD files live under `crds/` inside this module and are loaded at plan time
via `file()`. They must be kept in sync with the operator image version.

## Prerequisites

- **Harvester RKE2 cluster** running Kubernetes >= 1.28. This is the workload
  cluster that hosts the dbaas-operator and the per-DBInstance VMs — not the
  dc-api management cluster.
- **KubeVirt + CDI installed.** Harvester ships with both; verify via
  `kubectl get crd virtualmachines.kubevirt.io datavolumes.cdi.kubevirt.io`.
- **A storage class** (typically `longhorn`) usable by the per-DBInstance
  DataVolumes. The class name is specified on the DBInstance CR spec
  (`storageType`), not by this module.
- **KubeOVN with a usable logical switch** for the VM management NIC. The
  default `ovn-default` works for most clusters; override
  `db_mgmt_logical_switch` for non-default deployments.
- **A pre-existing `NetworkAttachmentDefinition`** for the data VLAN each
  DBInstance attaches to (referenced by `spec.networkRef`). The controller
  does not create NADs — the platform Terraform layer (or dc-api) does.
- **Pod Security Admission compatibility:** the `dbaas-system` namespace is
  compatible with `enforce=restricted` (non-root, drops ALL capabilities,
  read-only root filesystem, `seccompProfile: RuntimeDefault`). No PSA label
  is added to the namespace by this module — the caller sets the level.
- For `enable_prometheus_servicemonitor = true`: Prometheus Operator CRDs
  (`monitoring.coreos.com/v1`) must be installed (e.g. via kube-prometheus-stack).
- For `enable_cert_manager_metrics = true`: cert-manager must be installed
  and must have issued a `Certificate` named `metrics-certs` in the
  dbaas namespace, producing a Secret named `metrics-server-cert`.

## Usage

The caller creates a `kubernetes` provider pointing at the target cluster
and passes it via the `providers` block:

```hcl
provider "kubernetes" {
  alias       = "harvester_cluster"
  config_path = "/path/to/harvester-rke2.yaml"
}

module "database_operator" {
  source = "github.com/wso2/open-cloud-datacenter//modules/operators/database?ref=terraform/v0.2.0"

  providers = {
    kubernetes = kubernetes.harvester_cluster
  }

  db_image     = "ghcr.io/wso2/dbaas-controller"
  db_image_tag = "v0.2.15-split-secrets"

  # Optional — override only when the cluster uses a non-default KubeOVN
  # logical switch for VM management interfaces.
  # db_mgmt_logical_switch = "tenant-mgmt"

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
| `db_namespace` | `string` | `"dbaas-system"` | Namespace the dbaas-operator runs in |
| `db_image` | `string` | `"ghcr.io/wso2/dbaas-controller"` | Image registry path, no tag |
| `db_image_tag` | `string` | `"v0.2.15-split-secrets"` | Pinned image tag |
| `db_mgmt_logical_switch` | `string` | `"ovn-default"` | KubeOVN logical switch for the VM mgmt NIC |
| `ghcr_username` | `string` | `""` | GHCR username; leave empty to skip pull secret |
| `ghcr_pat` | `string` (sensitive) | `""` | GHCR PAT; leave empty to skip pull secret |
| `enable_metrics_network_policy` | `bool` | `false` | Create NetworkPolicy gating /metrics ingress to `metrics: enabled` namespaces |
| `enable_prometheus_servicemonitor` | `bool` | `false` | Create ServiceMonitor (requires Prometheus Operator CRDs) |
| `enable_cert_manager_metrics` | `bool` | `false` | Mount cert-manager TLS certs into the metrics endpoint (requires cert-manager + a Certificate CR named `metrics-certs`) |

## Outputs

| Name | Description |
|---|---|
| `db_namespace` | Namespace the controller is deployed into |
| `db_deployment_name` | Name of the controller-manager Deployment |
| `db_image` | Fully-qualified `image:tag` used by the Deployment |

## Image lifecycle

The Deployment carries `lifecycle { ignore_changes = [... container[0].image] }`.
This means TF seeds the initial image on first apply, then CI owns rolling it
forward via `kubectl set image`. To force TF to pin a specific tag again,
remove the resource from state and re-apply, or explicitly bump `db_image_tag`
after removing the lifecycle block temporarily.

## Composition with upstream layers

This module sits downstream of `harvester-integration` (which registers the
cluster with Rancher) and `dc-controlplane` (which creates the RKE2 cluster).
The consumer layer wires the kubernetes provider from the cluster's kubeconfig
output, exactly as `dc-controlplane-services` and `modules/operators/keyvault`
do today.

## Deviations from kustomize build output

| kustomize | This module | Reason |
|---|---|---|
| `managed-by: kustomize` | `managed-by: terraform` | Reflects actual deployment tool |
| `imagePullSecrets` always absent | Conditional on `ghcr_username`+`ghcr_pat` | Allows pull-secret injection without altering the canonical YAML |
| Image `wso2vick/ocd-dbaas:v0.2.10` | `var.db_image:var.db_image_tag` | Caller-controlled; not pinned in module |
| `--mgmt-logical-switch=ovn-default` (hard-coded) | `--mgmt-logical-switch=${var.db_mgmt_logical_switch}` | Per-env override without forking config/manager |
| NetworkPolicy: commented out in kustomize | Optional via `enable_metrics_network_policy` | Preserves kustomize default (off) while making it available |
| ServiceMonitor: commented out in kustomize | Optional via `enable_prometheus_servicemonitor` | Avoids hard dependency on Prometheus Operator |
| cert-manager patches: commented out in kustomize | Optional via `enable_cert_manager_metrics` | Avoids hard dependency on cert-manager |
