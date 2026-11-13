# Module: harvester-config

Baseline configuration of a bootstrapped Harvester HCI cluster. It manages the
namespace, cluster networks and uplinks, a VM network, the storage and RWX
networks, the Longhorn StorageClasses, and an OS image. It doesn't manage host
disks (add those by hand) or SSH keys.

Run it after `platform/harvester-bootstrap`, using the kubeconfig that module
writes, and **before any VM exists**. When the storage network changes,
Harvester stops workloads and requires every Longhorn volume to be detached.

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.5 |
| harvester/harvester | ~> 1.7 |
| hashicorp/kubernetes | ~> 2.30 |
| hashicorp/null | ~> 3.0 |

If `manage_storage_network = true`, the machine running Terraform also needs
`kubectl` and `python3`.

## Usage

```hcl
module "harvester_config" {
  source = "github.com/wso2/open-cloud-datacenter//modules/platform/harvester-config?ref=terraform/vX.Y.Z"

  harvester_namespace       = "infra"
  harvester_kubeconfig_path = var.harvester_kubeconfig

  cluster_networks = ["vm-network", "strg-network"]
  vlan_configs = {
    vm-network-uplink   = { cluster_network = "vm-network", nics = ["eno2"] }
    strg-network-uplink = { cluster_network = "strg-network", nics = ["eno3"], mtu = 9000 }
  }

  create_vm_network          = true
  vm_network_name            = "vm-net"
  vm_network_cluster_network = "vm-network"
  vm_network_vlan_id         = 100

  manage_storage_network          = true
  storage_network_vlan            = 200
  storage_network_cluster_network = "strg-network"
  storage_network_range           = "10.10.0.0/19"
  storage_network_exclude_ranges  = ["10.10.0.0/21"]

  manage_rwx_network = true

  rwx_storage_class_replicas = 2

  image_url = "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img"
}
```

## Apply order inside the module

1. Namespace, cluster networks, then VLAN configs (uplinks).
2. **Wait** until every VlanStatus of the storage cluster network is `ready`.
   Harvester's webhook rejects the storage network until then.
3. Write the `storage-network` setting.
4. **Wait** until Harvester has applied that value. The wait passes only when
   the setting's hash annotation equals the sha1 of the new value **and** the
   `configured` condition is `True` with reason `Completed`. A stale
   `Completed` from the previous value can't satisfy it.
5. Write `rwx-network` = `{"share-storage-network":true}`. If the storage
   network fails, RWX is never enabled.

StorageClasses: the module first sets `harvester-longhorn`'s
`is-default-class` annotation to `"false"`, then creates the new default class.
Harvester's webhook rejects a second default class. The module deletes no
StorageClass.

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| harvester_namespace | Namespace for the VM network and image; created when not `default` | `string` | `"default"` | no |
| harvester_kubeconfig_path | Harvester kubeconfig path. Required when `manage_storage_network = true`. | `string` | `""` | no |
| cluster_networks | Cluster networks to create (not `mgmt`) | `set(string)` | `[]` | no |
| vlan_configs | Uplink per cluster network: `cluster_network`, `nics`, optional `bond_mode`, `mtu`, `node_selector` | `map(object)` | `{}` | no |
| create_vm_network | Create the VM network | `bool` | `false` | no |
| vm_network_name | VM network name | `string` | `""` | when VM network |
| vm_network_cluster_network | Cluster network for the VM network VLAN | `string` | `"mgmt"` | no |
| vm_network_vlan_id | VM network VLAN ID | `number` | `100` | no |
| vm_network_gateway | VM network route gateway; empty means auto route mode | `string` | `""` | no |
| vm_network_cidr | VM network route CIDR; required with a gateway | `string` | `""` | no |
| vm_network_dhcp_server_ip | DHCP server Harvester queries for the route in auto mode; can't be combined with gateway/CIDR | `string` | `""` | no |
| manage_storage_network | Configure the `storage-network` setting | `bool` | `false` | no |
| storage_network_vlan | Storage VLAN ID | `number` | `0` | no |
| storage_network_cluster_network | Storage cluster network | `string` | `""` | no |
| storage_network_range | Storage IP range (CIDR) | `string` | `""` | no |
| storage_network_exclude_ranges | Excluded CIDRs. With RWX sharing, include guest-cluster VM IPs. | `list(string)` | `[]` | no |
| storage_network_timeout_seconds | Wait for the storage network to apply | `number` | `1200` | no |
| network_wait_timeout_seconds | Wait for the storage VLAN status to be ready | `number` | `600` | no |
| manage_rwx_network | Set `rwx-network` to share the storage network | `bool` | `false` | no |
| manage_storage_class | Create the default class and `longhorn-rwx` | `bool` | `true` | no |
| storage_class_name | Default StorageClass name | `string` | `"harvester-longhorn-2r"` | no |
| storage_class_replicas | Default StorageClass replicas | `number` | `2` | no |
| rwx_storage_class_replicas | `longhorn-rwx` replicas | `number` | `1` | no |
| image_url | Cloud image URL; empty skips the image | `string` | `""` | no |
| image_name | Image resource name | `string` | `"ubuntu-22-04"` | no |
| image_display_name | Image display name | `string` | `"ubuntu-22.04"` | no |

## Outputs

| Name | Description |
|------|-------------|
| namespace | Namespace holding the VM network and image |
| vm_network_name | VM network reference (`<namespace>/<name>`) |
| vm_image_id | Image reference (`<namespace>/<name>`); pass to `platform/rancher` as `ubuntu_image_id` |
| storage_class_name | Cluster-default StorageClass name |
| rwx_storage_class_name | RWX StorageClass name |

## Notes

- **The StorageClasses are protected with `prevent_destroy`.** StorageClass
  parameters can't be changed after creation, so changing a replica count
  would recreate the class. The plan fails instead. To change the replica
  count, create a new class under a new name.
- **The default flag isn't restored automatically.** Removing the module
  doesn't set `harvester-longhorn` back to default. Do it by hand if needed:
  `kubectl annotate sc harvester-longhorn storageclass.kubernetes.io/is-default-class=true --overwrite`.
- **`rwx-network` changes need RWX volumes detached.** Harvester also blocks a
  new change while the previous one is still `In Progress`.
- **The storage network can't be cleared while `rwx-network` shares it.**
  Terraform destroys `rwx-network` first, so destroying the module works.
