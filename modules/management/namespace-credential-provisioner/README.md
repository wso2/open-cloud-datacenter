# Module: management/namespace-credential-provisioner

Deploys a long-running reconciler on the Harvester cluster that automatically provisions
credentials in every tenant namespace. This is a required part of the management phase —
deploy it after `harvester-integration` and before creating tenant workloads.

## What it does

For every namespace labelled as a tenant namespace, the provisioner creates:

1. `harvester-vm-access-<ns>` ServiceAccount with scoped RoleBindings:
   - `harvesterhci.io:edit` in the tenant namespace (VM lifecycle)
   - `edit` in the tenant namespace (generic Kubernetes resources)
   - `harvesterhci.io:view` in `harvester-public` (read shared OS images)
2. A long-lived SA token Secret
3. `harvester-vm-kubeconfig` Secret in the namespace — a namespace-scoped kubeconfig
   consumers use to authenticate the `harvester` Terraform provider

On startup the provisioner backfills any existing namespaces that are missing the
`harvester-vm-kubeconfig` Secret (upgrade path).

On namespace deletion it cleans up the cross-namespace `harvester-public` RoleBinding.

## Why this matters

Without the provisioner, consumer teams cannot authenticate to Harvester to create VMs.
The alternative — handing out admin kubeconfigs or running per-team credential setup
manually — does not scale and creates security exposure. This provisioner eliminates
both problems: credentials are created automatically, scoped per namespace, and revoked
automatically when the namespace is deleted.

## Deployment sequence

```
Phase 2a  harvester-integration   — registers Harvester with Rancher
Phase 2e  namespace-credential-provisioner  ← deploy here
Phase 5   tenant-space            — creates namespaces; provisioner reacts immediately
```

The provisioner must be running before `tenant-space` creates namespaces so that
`harvester-vm-kubeconfig` is ready by the time consumer teams run `terraform apply`.

## Usage

```hcl
module "provisioner" {
  source = "github.com/wso2/open-cloud-datacenter//modules/management/namespace-credential-provisioner?ref=v0.8.0"

  providers = {
    kubernetes = kubernetes.harvester
  }

  harvester_api_server = "https://192.168.10.6:6443"
  rancher_kubeconfig   = file(var.rancher_kubeconfig_path)

  depends_on = [module.harvester_integration]
}
```

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| `harvester_api_server` | Harvester Kubernetes API server URL (e.g. `https://192.168.10.6:6443`) | `string` | — | yes |
| `rancher_kubeconfig` | Kubeconfig for the Rancher cluster. Used to write `harvesterconfig` secrets into `fleet-default`. | `string` | — | yes |
| `namespace` | Namespace to deploy the provisioner into | `string` | `"kube-system"` | no |
| `image` | Container image for the provisioner (needs `kubectl`, `bash`, `jq`) | `string` | `"alpine/k8s:1.32.3"` | no |

## Outputs

| Name | Description |
|------|-------------|
| `deployment_name` | Name of the provisioner Deployment |
| `service_account_name` | ServiceAccount used by the provisioner pod |

## Security

The provisioner SA has cluster-wide namespace watch and cross-namespace write access for
ServiceAccounts, Secrets, and RoleBindings — this is the minimum required to manage
credentials across all tenant namespaces. The credentials it creates are namespace-scoped:
each `harvester-vm-access-<ns>` SA can only act within its own namespace (plus read-only
access to `harvester-public` for shared images).

One project per team is strongly recommended. Within a shared project, namespace isolation
is enforced by the SA RoleBindings — not Rancher project RBAC — so consumers cannot
cross namespace boundaries even if they share a project.

## Relation to `harvester-cloud-credential`

`workloads/harvester-cloud-credential` is deprecated. It served the same purpose
(creating per-cluster Harvester credentials) but required manual invocation per cluster.
The provisioner replaces it for all greenfield deployments. Retain the module only for
brownfield clusters that have existing credentials that cannot be migrated.
