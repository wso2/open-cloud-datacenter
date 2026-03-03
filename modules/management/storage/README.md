# Module: management/storage

Downloads and registers OS images into Harvester HCI, making them available for VM provisioning.

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| harvester/harvester | ~> 0.6.0 |

## Usage

```hcl
module "storage" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/storage?ref=v0.1.0"

  harvester_namespace = "default"

  managed_images = {
    "ubuntu-22-04" = {
      display_name = "Ubuntu 22.04 LTS"
      url          = "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img"
    }
  }
}
```

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| harvester_namespace | Harvester namespace to manage images within | `string` | `"default"` | no |
| managed_images | A map of OS images to sync into Harvester | `map(object({ display_name = string, url = string }))` | `{}` | no |

## Outputs

This module does not define outputs. The created `harvester_image` resources can be referenced by name using the map keys passed to `managed_images`.
