# Module: bootstrap

Provisions an RKE2-based Rancher server on Harvester HCI using cloud-init, with a Load Balancer and IP pool for external access.

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| harvester/harvester | ~> 0.6.0 |
| hashicorp/helm | ~> 2.0 |
| rancher/rancher2 | ~> 3.0 |
| hashicorp/tls | ~> 4.0 |

## Usage

```hcl
module "bootstrap" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/bootstrap?ref=v0.1.0"

  ubuntu_image_id        = "default/ubuntu-22-04"
  vm_password            = var.vm_password
  rancher_hostname       = "rancher.example.internal"
  rancher_admin_password = var.rancher_admin_password
  ippool_subnet          = "192.168.10.0/24"
  ippool_gateway         = "192.168.10.1"
  ippool_start           = "192.168.10.10"
  ippool_end             = "192.168.10.10"
}
```

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| vm_name | Name of the Rancher server VM | `string` | `"rancher-bootstrap"` | no |
| vm_memory | Memory size for the Rancher VM (e.g. '8Gi') | `string` | `"8Gi"` | no |
| node_count | Number of nodes in the bootstrap cluster | `number` | `1` | no |
| harvester_namespace | Harvester namespace to deploy into | `string` | `"default"` | no |
| cluster_network_name | Name of the base cluster network in Harvester (e.g. 'mgmt') | `string` | `"mgmt"` | no |
| cluster_vlan_id | The VLAN tag ID for the bootstrap node network | `number` | `100` | no |
| cluster_vlan_gateway | The gateway IP for the new VLAN (Optional) | `string` | `""` | no |
| ubuntu_image_id | Harvester ID of the Ubuntu Cloud Image | `string` | n/a | yes |
| vm_password | Default password for the ubuntu user | `string` | n/a | yes |
| rancher_hostname | FQDN for the Rancher UI | `string` | n/a | yes |
| rancher_admin_password | Bootstrap password for Rancher Admin user | `string` | n/a | yes |
| ippool_subnet | Subnet for the IP pool (e.g. 192.168.10.1/24) | `string` | n/a | yes |
| ippool_gateway | Gateway for the IP pool | `string` | n/a | yes |
| ippool_start | Start of the IP range for the pool | `string` | n/a | yes |
| ippool_end | End of the IP range for the pool | `string` | n/a | yes |

## Outputs

| Name | Description |
|------|-------------|
| rancher_hostname | The FQDN of the bootstrapped Rancher server |
| rancher_lb_ip | The IP address of the LoadBalancer exposing Rancher |
