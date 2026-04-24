# Module: workloads/vm

Creates a single Harvester virtual machine within a tenant namespace. Intended for use
by product teams who have been granted a `tenant-space` project via the management phase.

This module creates:
- A `harvester_virtualmachine` with configurable CPU, memory, disk, and networking
- Optionally, a `harvester_ssh_key` (when `create_ssh_key = true`)
- Optionally, cloud-init user-data attached through the VM resource
- Optionally, a `ScheduleVMBackup` CRD for scheduled snapshots

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| harvester/harvester | ~> 1.7 |
| hashicorp/kubernetes | >= 2.0 |

## Provider setup

The `harvester` provider accepts either a kubeconfig file path or base64-encoded kubeconfig
content. For consumer teams, the inline approach requires nothing beyond values already known
at onboarding — no file handover, no scripts.

```hcl
locals {
  _harvester_kubeconfig_b64 = base64encode(yamlencode({
    apiVersion      = "v1"
    kind            = "Config"
    current-context = "harvester"
    clusters = [{
      name = "harvester"
      cluster = {
        server                   = "${var.rancher_url}/k8s/clusters/${var.harvester_cluster_id}"
        insecure-skip-tls-verify = true  # see TLS note below
      }
    }]
    users = [{
      name = "consumer"
      user = { token = var.rancher_api_token }
    }]
    contexts = [{
      name    = "harvester"
      context = { cluster = "harvester", user = "consumer" }
    }]
  }))
}

provider "harvester" {
  kubeconfig = local._harvester_kubeconfig_b64
}

provider "kubernetes" {
  host     = "${var.rancher_url}/k8s/clusters/${var.harvester_cluster_id}"
  token    = var.rancher_api_token
  insecure = true  # see TLS note below
}
```

> **TLS note:** `insecure-skip-tls-verify = true` / `insecure = true` is convenient for
> internal environments with self-signed certificates. For validated TLS, replace these with
> the Rancher CA certificate instead:
>
> ```hcl
> # Obtain the CA: openssl s_client -connect <rancher-host>:443 </dev/null 2>/dev/null \
> #   | openssl x509 -outform PEM | base64
> variable "rancher_ca_cert_b64" {
>   type      = string
>   sensitive = false
>   description = "Base64-encoded PEM CA certificate for the Rancher server."
> }
>
> locals {
>   _harvester_kubeconfig_b64 = base64encode(yamlencode({
>     # ...
>     clusters = [{
>       name = "harvester"
>       cluster = {
>         server                     = "${var.rancher_url}/k8s/clusters/${var.harvester_cluster_id}"
>         certificate-authority-data = var.rancher_ca_cert_b64
>       }
>     }]
>     # ...
>   }))
> }
>
> provider "kubernetes" {
>   host                   = "${var.rancher_url}/k8s/clusters/${var.harvester_cluster_id}"
>   token                  = var.rancher_api_token
>   cluster_ca_certificate = base64decode(var.rancher_ca_cert_b64)
> }
> ```

The three required values (`rancher_url`, `harvester_cluster_id`, `rancher_api_token`) are
provided at onboarding and should already be in your `terraform.tfvars`.

Scoping is enforced by Rancher project RBAC on the token — teams can only access namespaces
within their assigned project.

## Prerequisites

- Harvester namespace already exists (created by `tenant-space`)
- A Harvester network attachment exists for the target VLAN
- OS image is available in Harvester (downloaded by `management/storage`)

## Usage

### Minimal

```hcl
module "app_vm" {
  source = "github.com/wso2/open-cloud-datacenter//modules/workloads/vm?ref=v0.8.0"

  name         = "app-server-1"
  namespace    = "my-team-ns"
  image_name   = "default/ubuntu-22-04"
  network_name = "my-team-ns/vm-net-100"
}
```

### With cloud-init

```hcl
module "app_vm" {
  source = "github.com/wso2/open-cloud-datacenter//modules/workloads/vm?ref=v0.8.0"

  name         = "app-server-1"
  namespace    = "my-team-ns"
  cpu          = 4
  memory       = "8Gi"
  disk_size    = "80Gi"
  image_name   = "default/ubuntu-22-04"
  network_name = "my-team-ns/vm-net-100"

  user_data = <<-EOT
    #cloud-config
    password: ${var.vm_password}
    chpasswd:
      expire: false
    ssh_pwauth: true
    ssh_authorized_keys:
      - ${var.ssh_public_key}
    packages:
      - qemu-guest-agent
    runcmd:
      - systemctl enable --now qemu-guest-agent
  EOT
}
```

> **Note:** When `user_data` is set, the module's `ssh_authorized_keys` and `password` inputs
> are ignored — include them directly in your `user_data` cloud-config instead.
> Do not use a `users:` block with `password:` — on cloud-init 25.x this prevents
> the password from being applied to the default OS user. Use the top-level `password:` /
> `chpasswd:` / `ssh_authorized_keys:` keys instead.

### With scheduled backups
### Importing a brownfield VM

When a VM already exists (e.g. created via the Harvester UI) and you want to
bring it under Terraform management without forcing destructive renames, set
the `*_name` / `*_auto_delete` / `input_devices` overrides to match the VM's
current spec. Defaults target greenfield conventions; override only to match
existing state.

```hcl
module "legacy_vm" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/workloads/vm?ref=v0.x.y"

  name         = "legacy-host"
  namespace    = "team-ns"
  cpu          = 2
  memory       = "8Gi"
  disk_size    = "40Gi"
  image_name   = "default/image-abc12" # match the existing image ID
  network_name = "team-ns/vlan-600"

  # Brownfield overrides — match what the Harvester UI created
  disk_name              = "disk-0"
  disk_auto_delete       = false
  network_interface_name = "default"
  ssh_key_ids            = ["default/shared-key"]
  input_devices          = [{ name = "tablet", type = "tablet", bus = "usb" }]
}
```

After declaring the module, `terraform import module.legacy_vm.harvester_virtualmachine.this <namespace>/<name>` and run `terraform plan` — the plan should show zero changes.

### Referencing images from the management phase

```hcl
module "app_vm" {
  source = "github.com/wso2/open-cloud-datacenter//modules/workloads/vm?ref=v0.8.0"

  name         = "app-server-1"
  namespace    = "my-team-ns"
  image_name   = "default/ubuntu-22-04"
  network_name = "my-team-ns/vm-net-100"

  backup_schedule = "0 2 * * *"  # daily at 2 AM UTC
  backup_retain   = 7
}
```

### Multiple VMs across namespaces

```hcl
module "vm" {
  source   = "github.com/wso2/open-cloud-datacenter//modules/workloads/vm?ref=v0.8.0"
  for_each = var.vms

  name         = each.key
  namespace    = each.value.namespace
  cpu          = each.value.cpu
  memory       = each.value.memory
  disk_size    = each.value.disk_size
  image_name   = var.image_name
  network_name = var.network_name

  ssh_authorized_keys = var.ssh_authorized_keys
}
```

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| `name` | VM name | `string` | — | yes |
| `namespace` | Harvester namespace | `string` | — | yes |
| `cpu` | Number of vCPUs | `number` | `2` | no |
| `memory` | RAM (e.g. `"4Gi"`) | `string` | `"4Gi"` | no |
| `disk_size` | Root disk size (e.g. `"40Gi"`) | `string` | `"40Gi"` | no |
| `image_name` | Harvester image in `namespace/name` format | `string` | — | yes |
| `network_name` | Harvester network attachment in `namespace/name` format | `string` | — | yes |
| `run_strategy` | `RerunOnFailure`, `Always`, `Halted`, or `Manual` | `string` | `"RerunOnFailure"` | no |
| `create_ssh_key` | Create a `harvester_ssh_key` from `ssh_public_key` | `bool` | `false` | no |
| `ssh_public_key` | SSH public key content (requires `create_ssh_key = true`) | `string` | `null` | no |
| `ssh_authorized_keys` | SSH keys injected via cloud-init (no `create_ssh_key` needed) | `list(string)` | `[]` | no |
| `default_user` | OS username for generated cloud-init | `string` | `"ubuntu"` | no |
| `password` | Password for `default_user` injected via cloud-init | `string` | `null` | no |
| `user_data` | Full cloud-init user-data override. When set, `password`/`ssh_authorized_keys`/`default_user` are ignored. | `string` | `null` | no |
| `network_data` | Cloud-init network-data (requires `user_data`) | `string` | `""` | no |
| `wait_for_lease` | Wait for IP lease on primary NIC. Set `false` for static IPs or private VLANs. | `bool` | `true` | no |
| `additional_disks` | Extra disks to attach (name, size, optional image, auto_delete) | `list(object)` | `[]` | no |
| `backup_schedule` | Cron schedule for VM snapshots in UTC (e.g. `"0 2 * * *"`). `null` disables. | `string` | `null` | no |
| `backup_retain` | Number of snapshots to retain | `number` | `5` | no |
| `backup_enabled` | Whether the backup schedule is active | `bool` | `true` | no |
| `backup_max_failure` | Max consecutive backup failures before suspending | `number` | `4` | no |
| `ssh_public_key` | SSH public key content. Used when `create_ssh_key = true`. | `string` | `null` | no |
| `create_ssh_key` | When true, create a `harvester_ssh_key` from `ssh_public_key`. | `bool` | `false` | no |
| `wait_for_lease` | Wait for IP lease on primary NIC. Set false for static cloud-init IPs. | `bool` | `true` | no |
| `user_data` | Cloud-init user-data YAML. Creates a cloud-init secret when set. | `string` | `null` | no |
| `network_data` | Cloud-init network-data config (requires `user_data` to be set). | `string` | `""` | no |
| `disk_name` | Name of the root disk volume. Override to match brownfield VMs. | `string` | `"rootdisk"` | no |
| `disk_auto_delete` | Whether the root disk's PVC is deleted with the VM. | `bool` | `true` | no |
| `network_interface_name` | Name of the primary NIC. Override to match brownfield VMs. | `string` | `"nic-1"` | no |
| `restart_after_update` | Whether Terraform restarts the VM when its spec changes. | `bool` | `true` | no |
| `ssh_key_ids` | Existing Harvester SSH key IDs (`namespace/name`) to attach. | `list(string)` | `[]` | no |
| `input_devices` | Input devices (e.g. USB tablet) to attach. | `list(object)` | `[]` | no |

## Outputs

| Name | Description |
|------|-------------|
| `vm_name` | Name of the created VM |
| `vm_id` | Harvester resource ID (`namespace/name`) |
| `network_interfaces` | Network interface objects including leased IP once running |
| `ssh_key_id` | Harvester SSH key ID attached to the VM, or `null` |

## Notes

- `image_name` uses the Harvester format `namespace/name` — use the `image_ids` output from
  the `management/storage` module to keep this consistent across the platform.
- The VM's IP is available in `network_interfaces[0].ip_address` after the lease is obtained
  (`wait_for_lease = true`). Set `wait_for_lease = false` for static IPs via cloud-init or
  VMs on private VLAN networks where Kubernetes IPAM is unavailable.
- Install and enable `qemu-guest-agent` via cloud-init so the guest IP is visible in the
  Harvester UI for VMs on private VLAN networks.
- `terraform destroy` **deletes the VM and its root disk**. Ensure data is backed up first.
