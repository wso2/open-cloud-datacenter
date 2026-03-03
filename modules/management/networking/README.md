# Module: management/networking

Creates and manages VLAN-backed Harvester networks for tenant and management workloads.

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| harvester/harvester | ~> 0.6.0 |

## Usage

```hcl
module "networking" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/networking?ref=v0.1.0"

  cluster_network_name = "mgmt"
  harvester_namespace  = "default"

  vlans = {
    "vlan-100" = {
      vlan_id = 100
      cidr    = "192.168.100.0/24"
      gateway = "192.168.100.1"
    }
    "vlan-200" = {
      vlan_id = 200
      cidr    = "192.168.200.0/24"
      gateway = "192.168.200.1"
    }
  }
}
```

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| harvester_namespace | Harvester namespace to manage networks within | `string` | `"default"` | no |
| cluster_network_name | The name of the cluster network in Harvester to attach these VLANs | `string` | `"mgmt"` | no |
| vlans | A map of VLAN names to their configuration | `map(object({ vlan_id = number, cidr = string, gateway = string }))` | `{}` | no |

## Outputs

This module does not define outputs. The created `harvester_network` resources can be referenced by name using `harvester_network.<key>`.
