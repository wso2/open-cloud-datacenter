# Module: workloads/harvester-cloud-credential

> **Deprecated.** Use `management/namespace-credential-provisioner` instead.
>
> This module is retained for brownfield clusters that already have credentials created
> outside Terraform. Do not use it for new deployments — the provisioner handles this
> automatically as part of the management phase (Phase 2e).

---

Creates the per-cluster Harvester cloud provider credential Secret (`harvesterconfig-<cluster-name>`)
in Rancher's `fleet-default` namespace. This secret is required by RKE2 node VMs so the
Harvester CSI driver and load balancer controller can authenticate back to Harvester.

## When to use

Only use this module for **brownfield clusters** that:
- Were provisioned before the `namespace-credential-provisioner` was deployed, and
- Have no existing `harvesterconfig-<cluster-name>` Secret managed by the provisioner

For all other cases, deploy `management/namespace-credential-provisioner` as part of
Phase 2 and it will create the credential automatically when the cluster is detected.

## Inputs

| Name | Description | Type | Required |
|------|-------------|------|----------|
| `cluster_name` | RKE2 cluster name (used in Secret name and SA name) | `string` | yes |
| `vm_namespace` | Harvester namespace the cluster's VMs run in | `string` | yes |
| `harvester_api_server` | Harvester API server URL (e.g. `https://192.168.10.6:6443`) | `string` | yes |

## Outputs

| Name | Description |
|------|-------------|
| `secret_name` | Name of the `harvesterconfig-<cluster-name>` Secret in `fleet-default` |
| `kubeconfig` | The generated kubeconfig YAML (sensitive) |
