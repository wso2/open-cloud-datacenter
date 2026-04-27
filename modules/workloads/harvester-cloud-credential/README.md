# Module: workloads/harvester-cloud-credential

> **This module is for infra/platform team use only.**
>
> If you are a consumer team provisioning your own RKE2 cluster, you do **not** need this
> module. The `namespace-credential-provisioner` (deployed in the management phase)
> automatically creates the `harvesterconfig-<cluster-name>` secret in Rancher's
> `fleet-default` namespace when it detects a new cluster in your namespace. See the
> [k8s-cluster module README](../k8s-cluster/README.md#harvester-cloud-provider-credential)
> for the consumer workflow.

Creates the `harvesterconfig-<cluster-name>` secret that the Harvester cloud provider
(CSI driver + load balancer controller) on RKE2 nodes uses to authenticate against the
Harvester Kubernetes API. Requires direct `kubernetes.harvester` and `kubernetes.rancher_local`
provider access — credentials that are only available to the platform team.

## When you still need this module

Use this module in environments where the `namespace-credential-provisioner` is **not**
deployed (e.g. a standalone Harvester+Rancher setup without the management phase provisioner).
In that case, call this module once per RKE2 cluster before the cluster's first `terraform apply`.

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.7 |
| hashicorp/kubernetes | ~> 2.35 |

Requires two provider aliases: `kubernetes.harvester` (Harvester kube-apiserver) and
`kubernetes.rancher_local` (Rancher local cluster kube-apiserver).

## Inputs

| Name | Description | Type | Required |
|------|-------------|------|----------|
| `cluster_name` | RKE2 cluster name (DNS-1123, max 253 chars) | `string` | yes |
| `vm_namespace` | Harvester namespace where cluster node VMs run | `string` | yes |
| `harvester_api_server` | Direct Harvester kube-apiserver URL (port 6443) | `string` | yes |

## Outputs

| Name | Description |
|------|-------------|
| `secret_name` | Secret name in `fleet-default` — pass to `k8s-cluster.cloud_provider_config_secret` |
| `service_account_name` | ServiceAccount name created in the VM namespace |
