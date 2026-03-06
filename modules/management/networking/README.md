# Module: management/networking

Creates VLAN-backed Harvester networks that segment traffic between tenants and workloads.
Each VLAN maps to a Harvester `NetworkAttachmentDefinition` that VMs can attach to.

## When to Use

Apply this module after `harvester-integration` to define the networks available for VM
deployment. Typically run once during initial setup, then extended as new VLANs are needed
(e.g. adding a DMZ network for a new tenant).

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| harvester/harvester | ~> 1.7 |

## Prerequisites

- Harvester cluster up and accessible via kubeconfig
- A Harvester cluster network (`ClusterNetwork`) already configured in Harvester UI or via
  Harvester settings — this is the uplink (e.g. `mgmt`, `bond0`) that VLANs are created on.
- `harvester` provider configured at the root level

## Usage

```hcl
module "networking" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/networking?ref=v0.1.x"

  cluster_network_name = "mgmt"
  harvester_namespace  = "default"

  vlans = {
    "iam-team-vlan" = {
      vlan_id = 100
      cidr    = "192.168.100.0/24"
      gateway = "192.168.100.1"
    }
    "middleware-vlan" = {
      vlan_id = 200
      cidr    = "192.168.200.0/24"
      gateway = "192.168.200.1"
    }
  }
}
```

Each entry creates a `harvester_network` resource. VMs provisioned in the corresponding
tenant space can attach to the network by referencing its name (e.g. `"iam-team-vlan"`).

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| `harvester_namespace` | Harvester namespace to create networks in | `string` | `"default"` | no |
| `cluster_network_name` | Harvester cluster network (uplink) to attach VLANs to | `string` | `"mgmt"` | no |
| `vlans` | Map of network name → `{ vlan_id, cidr, gateway }` | `map(object({ vlan_id = number, cidr = string, gateway = string }))` | `{}` | no |

## Outputs

This module does not expose outputs. Networks are referenced by their map key name in VM
provisioning configurations.

## Notes

- VLAN IDs must be unique within the cluster network and match your physical switch configuration.
- The `cidr` and `gateway` fields configure Harvester's DHCP server for that VLAN. Ensure
  they do not overlap with other VLANs or management networks.
- Removing a VLAN entry will delete the Harvester network. VMs attached to it will lose
  network connectivity. Detach VMs before removing.
