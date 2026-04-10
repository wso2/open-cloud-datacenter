# Module: workloads/vm

Creates a single Harvester virtual machine within a tenant namespace. Intended for use
by product teams who have been granted a `tenant-space` with the `vm-manager` role.

This module creates:
- A `harvester_virtualmachine` with configurable CPU, memory, disk, and networking
- Optionally, a `harvester_ssh_key` (when `ssh_public_key` is set)
- Optionally, cloud-init user-data/network-data attached through the VM resource (when `user_data` is set)

## When to Use

Use this module in a workloads-phase root module (e.g. `04-workloads`) after the
management phase has:
1. Provisioned the tenant namespace via `management/tenant-space`
2. Granted the team the `vm-manager` role via `management/cluster-roles`
3. Downloaded OS images via `management/storage` (use `image_ids` output)

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| harvester/harvester | ~> 1.7 |

## Prerequisites

- Harvester namespace already exists (created by `tenant-space`)
- A Harvester network attachment exists for the target VLAN (created by `management/networking`)
- OS image is available in Harvester (downloaded by `management/storage`)

## Usage

### Minimal (image name only, no SSH key, no cloud-init)

```hcl
module "app_vm" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/workloads/vm?ref=v0.1.x"

  name         = "app-server-1"
  namespace    = "iam-team-ns"
  image_name   = data.terraform_remote_state.management.outputs.image_ids["ubuntu-22-04"]
  network_name = "iam-team-vlan"
}
```

### With SSH key

```hcl
module "app_vm" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/workloads/vm?ref=v0.1.x"

  name           = "app-server-1"
  namespace      = "iam-team-ns"
  cpu            = 4
  memory         = "8Gi"
  disk_size      = "80Gi"
  image_name     = data.terraform_remote_state.management.outputs.image_ids["ubuntu-22-04"]
  network_name   = "iam-team-vlan"
  ssh_public_key = file(pathexpand("~/.ssh/id_rsa.pub"))
}
```

### With cloud-init

Inject SSH keys, set a password, and install `qemu-guest-agent` so the VM's IP
address is visible in the Harvester UI. For VMs on private VLAN networks (where
Kubernetes IPAM is not available), `qemu-guest-agent` is the only mechanism
Harvester uses to report the guest IP.

> **Note:** Do not use a `users:` block with `password:` — on cloud-init 25.x
> this prevents the password from being applied to the default OS user. Use the
> top-level `password:` / `chpasswd:` / `ssh_authorized_keys:` keys instead.
>
> **Note:** Avoid `package_update: true`. On Ubuntu 22.04 the `apt-daily` and
> `apt-daily-upgrade` timers run on first boot and hold the dpkg lock, causing
> package installs to fail silently if triggered at the same time.

The snippet below uses `var.vm_password` and `var.ssh_authorized_keys` — these
are **root-module variables** that you define in your own `variables.tf`; they
are not inputs to this module.

```hcl
# Example root-module variables (define in your own variables.tf)
variable "vm_password" {
  type      = string
  sensitive = true
}

variable "ssh_authorized_keys" {
  type    = list(string)
  default = []
}

module "app_vm" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/workloads/vm?ref=v0.1.x"

  name           = "app-server-1"
  namespace      = "iam-team-ns"
  image_name     = data.terraform_remote_state.management.outputs.image_ids["ubuntu-22-04"]
  network_name   = "iam-team-vlan"
  wait_for_lease = false

  user_data = <<-EOT
    #cloud-config
    password: ${var.vm_password}
    chpasswd:
      expire: false
    ssh_pwauth: true
    ssh_authorized_keys:
%{~ for key in var.ssh_authorized_keys }
      - ${key}
%{~ endfor }
    packages:
      - qemu-guest-agent
    runcmd:
      - systemctl enable --now qemu-guest-agent
  EOT
}
```

### Referencing images from the management phase

```hcl
data "terraform_remote_state" "management" {
  backend = "local"
  config = {
    path = "../02-management/terraform.tfstate"
  }
}

# Use the storage module's image_ids output
locals {
  ubuntu_image = data.terraform_remote_state.management.outputs.image_ids["ubuntu-22-04"]
}
```

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| `name` | VM name | `string` | — | yes |
| `namespace` | Harvester namespace (tenant project namespace) | `string` | — | yes |
| `cpu` | Number of vCPUs | `number` | `2` | no |
| `memory` | RAM (e.g. `"4Gi"`) | `string` | `"4Gi"` | no |
| `disk_size` | Root disk size (e.g. `"40Gi"`) | `string` | `"40Gi"` | no |
| `image_name` | Harvester image in `namespace/name` format | `string` | — | yes |
| `network_name` | Harvester network attachment name | `string` | — | yes |
| `run_strategy` | `RerunOnFailure`, `Always`, `Halted`, or `Manual` | `string` | `"RerunOnFailure"` | no |
| `ssh_public_key` | SSH public key content. Used when `create_ssh_key = true`. | `string` | `null` | no |
| `create_ssh_key` | When true, create a `harvester_ssh_key` from `ssh_public_key`. | `bool` | `false` | no |
| `wait_for_lease` | Wait for IP lease on primary NIC. Set false for static cloud-init IPs. | `bool` | `true` | no |
| `user_data` | Cloud-init user-data YAML. Creates a cloud-init secret when set. | `string` | `null` | no |
| `network_data` | Cloud-init network-data config (requires `user_data` to be set). | `string` | `""` | no |

## Outputs

| Name | Description |
|------|-------------|
| `vm_name` | Name of the created VM. |
| `vm_id` | Harvester resource ID (`namespace/name`). |
| `network_interfaces` | Network interface objects, including the leased IP once the VM is running. |
| `ssh_key_id` | Harvester SSH key ID attached to the VM, or `null` if not provided. |

## Notes

- `image_name` accepts the Harvester image ID format `namespace/name` — use the
  `image_ids` output from the `management/storage` module to keep this consistent.
- `network_name` must match a network attachment definition in the same Harvester cluster
  (created by `management/networking`).
- The VM's IP address is available in `network_interfaces[0].ip_address` after the
  lease is obtained (requires `wait_for_lease = true`, which is the default).
  Set `wait_for_lease = false` when using static IPs via cloud-init `network_data`,
  or when the VM is on a private VLAN where Kubernetes IPAM is not available.
- For VMs on private VLAN networks, Harvester cannot obtain the guest IP from
  Kubernetes IPAM. Install and enable `qemu-guest-agent` via cloud-init so the
  IP reported by the guest is visible in the Harvester UI.
- The `vm-manager` custom role from `management/cluster-roles` must be bound to the
  team's group in their `tenant-space` before they can create VMs in the namespace.
- Removing this module or running `terraform destroy` **deletes the VM and its disk**.
  Ensure data is backed up before destroying.
