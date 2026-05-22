# keyvault-operator

Terraform module that deploys the
[keyvault-operator](https://github.com/wso2/sovereign-cloud/tree/main/crds/keyvault)
controller on a Kubernetes cluster. The operator watches the
`KeyVaultBackend` and `KeyVaultInstance` CRDs (also installed by this
module) and reconciles per-tenant OpenBao clusters, KV-v2 mounts, and
AppRoles per their `spec`.

## What this module manages

| Kind                       | Scope        | Name                                       |
|----------------------------|--------------|--------------------------------------------|
| Namespace                  | cluster      | `var.namespace` (default `keyvault-system`) |
| CustomResourceDefinition × 2 | cluster      | `keyvaultbackends`, `keyvaultinstances`    |
| ServiceAccount             | namespaced   | `kvi-controller-manager`                   |
| Role + RoleBinding         | namespaced   | leader-election                            |
| ClusterRole + ClusterRoleBinding | cluster | manager (watches own CRDs, mutates StatefulSets in tenant namespaces) |
| ClusterRole + ClusterRoleBinding | cluster | metrics-auth (TokenReview / SubjectAccessReview) |
| ClusterRole                | cluster      | metrics-reader (anchor for Prometheus scrape SAs) |
| ClusterRole × 6            | cluster      | stub admin/editor/viewer roles for kubectl users |
| Service                    | namespaced   | metrics-service (ClusterIP `:8443`)        |
| Deployment                 | namespaced   | manager (single replica, leader-elected)   |
| Secret (optional)          | namespaced   | image-pull (when `image_pull_secret` set)  |

## Provider config

The module declares NO provider blocks. The caller layer supplies a
`kubernetes` provider configured against the cluster where the operator
should run. Typical pattern in a consumer:

```hcl
provider "kubernetes" {
  alias       = "tenant_workload"
  config_path = var.tenant_workload_kubeconfig
}

module "kvi" {
  source    = "../../modules/management/keyvault-operator"
  providers = { kubernetes = kubernetes.tenant_workload }
  image     = "ghcr.io/<owner>/keyvault-operator:v0.0.2"
  image_pull_secret = {
    server   = "ghcr.io"
    username = var.ghcr_user
    password = var.ghcr_token
  }
}
```

## Inputs

| Name                | Type    | Default              | Description |
|---------------------|---------|----------------------|-------------|
| `image`             | string  | (required)           | Full image ref incl. tag/digest |
| `namespace`         | string  | `keyvault-system`    | Where the manager Deployment + RBAC live |
| `name_prefix`       | string  | `kvi-`               | Prefix for every Kubernetes object name |
| `replicas`          | number  | `1`                  | Manager replicas (leader-elected; >1 supported) |
| `leader_elect`      | bool    | `true`               | Pass `--leader-elect` to the manager |
| `image_pull_secret` | object  | `null`               | Docker-registry credentials for private images |
| `resources`         | object  | kubebuilder defaults | Container resources.limits / .requests |
| `common_labels`     | map     | `{}`                 | Extra labels on every created object |

## Outputs

| Name                  | Description                                        |
|-----------------------|----------------------------------------------------|
| `namespace`           | Resolved namespace name                            |
| `deployment_name`     | Manager Deployment name (for kubectl rollout)      |
| `service_account_name`| Manager SA name (for tenant-ns RoleBindings)       |
| `metrics_service`     | `{namespace, name, port}` for Prometheus scrape    |
| `crd_groups`          | `{group, plurals}` for CR creation                 |

## Versioning

The RBAC rules emitted by `kubernetes_cluster_role.manager` are a verbatim
port of the operator's `+kubebuilder:rbac` markers at the time of writing.
When bumping the operator image tag (`image` variable), confirm whether
the operator's `config/rbac/role.yaml` shifted — if so, update the rules
in `main.tf` to match before applying.

The CRD YAML files under `crds/` are likewise pinned. Refresh them from
the operator's `config/crd/bases/` when bumping the image tag.

## Image-pull authentication

When pulling from a private registry, supply `image_pull_secret`:

```hcl
image_pull_secret = {
  server   = "ghcr.io"
  username = "<gh-user-or-bot>"
  password = "<personal-access-token-with-read:packages>"
}
```

The module creates a `kubernetes.io/dockerconfigjson` Secret named
`${name_prefix}image-pull` and references it in the manager Deployment's
`imagePullSecrets`. For public images, leave `image_pull_secret = null`.

## What this module does NOT do

- No tenant-namespace creation. Tenant namespaces (where the
  `KeyVaultBackend` CRs live) are created by the platform's API server
  (dc-api) on tenant registration, not by this module.
- No Prometheus ServiceMonitor / PodMonitor — left to the platform's
  monitoring stack. The metrics Service is created so the scrape can
  target it.
- No NetworkPolicy. The manager calls the kube API server only;
  consumers running a deny-all default policy should add an egress
  allow rule to the apiserver themselves.
