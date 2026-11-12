# Module: harvester-bootstrap

First-touch bootstrap of a freshly installed Harvester HCI cluster using the
[`harvester_bootstrap`](https://registry.terraform.io/providers/harvester/harvester/latest/docs/resources/bootstrap)
resource. It logs in with the installer's initial admin password, sets a new
admin password, and writes an admin kubeconfig to `kubeconfig_path`.

This module does not install Harvester. Run it once after installation and
before any layer that needs a Harvester kubeconfig, such as `platform/rancher`.

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.5 |
| harvester/harvester | ~> 1.7 |

The calling layer **must** configure the provider in bootstrap mode. Without
it, the resource fails with `harvester_bootstrap just available on bootstrap mode`:

```hcl
provider "harvester" {
  bootstrap = true
}
```

Use a dedicated layer for this. A provider in bootstrap mode isn't meant for
managing other Harvester resources in the same configuration.

## Usage

```hcl
module "harvester_bootstrap" {
  source = "github.com/wso2/open-cloud-datacenter//modules/platform/harvester-bootstrap?ref=terraform/vX.Y.Z"

  api_url         = "https://192.168.10.5"
  password        = var.harvester_admin_password
  kubeconfig_path = "${path.root}/harvester.kubeconfig"
}
```

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| api_url | Harvester API URL (cluster VIP over HTTPS) | `string` | n/a | yes |
| initial_password | Admin password set by the Harvester installer | `string` (sensitive) | `"admin"` | no |
| password | New admin password to set on first login | `string` (sensitive) | n/a | yes |
| kubeconfig_path | File path the generated admin kubeconfig is written to. No default, because the provider's default (`~/.kube/config`) would overwrite the operator's kubeconfig. | `string` | n/a | yes |

## Outputs

| Name | Description |
|------|-------------|
| kubeconfig_path | Path of the generated Harvester admin kubeconfig. Feed it to the harvester and kubernetes providers of later layers. |
| should_update_password | Whether the initial password was still active and got changed |

## Notes

- Every argument is `ForceNew` and the resource has no real delete. Changing
  any input repeats the login and password change against the cluster. If
  `password` changes after the first run, the login uses `initial_password`
  and may fail, so treat these inputs as write-once.
- The kubeconfig file is written to local disk, not to Terraform state. Keep it
  out of version control.
