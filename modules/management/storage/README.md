# Module: management/storage

Downloads OS images from public URLs and registers them in Harvester HCI, making them
selectable when provisioning VMs. Run this module whenever you want to add or update
images available to product teams.

## When to Use

Apply this module during initial datacenter setup (after `harvester-integration`) and again
whenever a new OS image is needed (e.g. adding Rocky Linux alongside the existing Ubuntu image).

Images are downloaded by Harvester directly from the URL — the Terraform runner does not need
to download them.

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| harvester/harvester | ~> 1.7 |

## Prerequisites

- Harvester cluster up and accessible via kubeconfig
- `harvester` provider configured at the root level

## Usage

```hcl
module "storage" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/storage?ref=v0.1.x"

  harvester_namespace = "default"

  managed_images = {
    "ubuntu-22-04" = {
      display_name = "Ubuntu 22.04 LTS (Jammy)"
      url          = "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img"
    }
    "rocky-9" = {
      display_name = "Rocky Linux 9"
      url          = "https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud.latest.x86_64.qcow2"
    }
  }
}
```

## How Consumers Reference Images

The storage module is applied by operators in the management phase. Consumers (workload
provisioning) reference the images via Terraform remote state — using the same key they were
registered under:

```hcl
# In the management phase (operator)
module "storage" {
  source = "..."
  managed_images = {
    "ubuntu-22-04" = { ... }
    "rocky-9"      = { ... }
  }
}

# In a workload/tenant phase (consumer) — pulls image ID from management state
data "terraform_remote_state" "management" {
  backend = "local"
  config  = { path = "../02-management/terraform.tfstate" }
}

module "tenant_vm_cluster" {
  source              = "..."
  harvester_image_name = data.terraform_remote_state.management.outputs.image_ids["ubuntu-22-04"]
}
```

The value of `image_ids["ubuntu-22-04"]` is the Harvester image resource path
(`namespace/name`) that VM and cluster provisioning resources accept directly.

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| `harvester_namespace` | Harvester namespace to register images into | `string` | `"default"` | no |
| `managed_images` | Map of image key → `{ display_name, url }` | `map(object({ display_name = string, url = string }))` | `{}` | no |

The map key becomes the image resource name in Harvester. Use lowercase, hyphen-separated names
(e.g. `"ubuntu-22-04"`) — Harvester uses this as the Kubernetes resource name.

## Outputs

| Name | Description |
|------|-------------|
| `image_ids` | Map of image key → Harvester image ID (`namespace/name`). Keys match `managed_images` input. |

## Notes

- Removing an entry from `managed_images` will delete that image from Harvester. Ensure no
  VMs are using the image before removing it.
- Image download is async — Harvester starts pulling the image when the resource is created.
  Large images (>1 GB) may take several minutes to become `Active`.
- The `url` field accepts `.img`, `.qcow2`, and `.iso` formats.
