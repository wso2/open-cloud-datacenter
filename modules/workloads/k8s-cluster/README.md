# Module: workloads/k8s-cluster

Provisions a tenant RKE2 Kubernetes cluster on Harvester HCI via Rancher's machine provisioning API, using a Harvester cloud credential and VM machine config.

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| rancher/rancher2 | ~> 3.0 |

## Usage

```hcl
module "k8s_cluster" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/workloads/k8s-cluster?ref=v0.1.0"

  cluster_name            = "tenant-alpha"
  harvester_image_name    = "default/ubuntu-22-04"
  harvester_network_name  = "default/vlan-100"
}
```

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| cluster_name | The name of the K8s cluster to provision | `string` | n/a | yes |
| k8s_version | The RKE2 Kubernetes version (e.g., v1.27.6+rke2r1) | `string` | `"v1.27.6+rke2r1"` | no |
| node_count | Number of control-plane/worker hybrid nodes | `number` | `3` | no |
| cloud_credential_name | Name of the Harvester cloud credential in Rancher | `string` | `"harvester-creds"` | no |
| harvester_namespace | Namespace in Harvester to deploy the VMs | `string` | `"default"` | no |
| harvester_image_name | Harvester image name for the base OS (e.g., default/ubuntu-22.04) | `string` | n/a | yes |
| harvester_network_name | Harvester network name (e.g., default/vlan-100) | `string` | n/a | yes |
| node_cpu | CPU string (e.g., '4') | `string` | `"4"` | no |
| node_memory | Memory string (e.g., '16Gi') | `string` | `"16"` | no |
| node_disk_size | Disk size string (e.g., '100Gi') | `string` | `"100"` | no |
| ssh_user | SSH username for the VM OS | `string` | `"ubuntu"` | no |

## Outputs

This module does not define explicit outputs. The provisioned cluster is accessible via `module.k8s_cluster.rancher2_cluster_v2.tenant_cluster`, which exposes attributes such as `cluster_registration_token` and `kube_config`.
