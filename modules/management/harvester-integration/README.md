# Module: management/harvester-integration

Registers the Harvester HCI cluster into Rancher so that Rancher becomes the management
plane for virtual machines, projects, and workload cluster provisioning. This is a one-time
operation per Harvester cluster.

What this module does, in order:

1. Enables the Harvester feature flag in Rancher settings
2. Installs the Harvester UI extension (Helm chart from the harvester-ui-extension catalog)
3. Creates a cloud credential for Harvester — used later when provisioning RKE2 workload clusters
4. Patches Harvester's CoreDNS so that Harvester nodes can resolve the internal Rancher hostname
5. Applies the Rancher registration manifest to Harvester via `kubectl`
6. Sets the `cluster-registration-url` and `rancher-cluster` settings in Harvester

## When to Use

Apply this module once at the start of Phase 2 (management). All other management modules
depend on the Rancher cluster ID that this module outputs.

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| rancher/rancher2 | ~> 13.1 |
| harvester/harvester | ~> 1.7 |
| hashicorp/kubernetes | ~> 3.0 |

## Prerequisites

- Phase 0 complete: Rancher is accessible at `rancher_hostname` and the admin token is available
- Harvester kubeconfig is available (can be exported from the Harvester UI)
- The Terraform runner has network access to both the Rancher API and the Harvester cluster API
- `kubectl` is installed on the Terraform runner

## Usage

```hcl
module "harvester_integration" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/harvester-integration?ref=v0.1.x"

  providers = {
    rancher2   = rancher2
    harvester  = harvester
    kubernetes = kubernetes
  }

  harvester_kubeconfig   = file(var.harvester_kubeconfig_path)
  harvester_cluster_name = "harvester-hci"
  rancher_hostname       = "rancher.example.internal"
  rancher_lb_ip          = "192.168.10.200"
}
```

> **Provider passing**: This module uses three providers. Always pass them explicitly via the
> `providers` block so the root module controls provider configuration (API URL, credentials).

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| `harvester_kubeconfig` | Full content of the Harvester kubeconfig file (sensitive) | `string` | — | yes |
| `harvester_cluster_name` | Display name for the cluster in Rancher | `string` | `"harvester-hci"` | no |
| `rancher_hostname` | FQDN of the Rancher server (must resolve from Harvester nodes) | `string` | — | yes |
| `rancher_lb_ip` | IP address of the Rancher LoadBalancer (added to Harvester CoreDNS hosts) | `string` | — | yes |

## Outputs

| Name | Description |
|------|-------------|
| `harvester_cluster_id` | Rancher cluster ID for the imported Harvester HCI cluster. Pass as `cluster_id` to `tenant-space` and `rbac` modules. |
| `harvester_cluster_name` | Name of the Harvester cluster as registered in Rancher. |

## Notes

- CoreDNS in Harvester is patched directly so Harvester nodes can resolve `rancher_hostname`
  (an internal name not in public DNS). This must happen before the registration manifest is
  applied — the module handles the ordering via `depends_on`.
- The registration manifest is applied via a `local-exec` provisioner using `kubectl`.
  The kubeconfig is written to a temp file and cleaned up via `trap EXIT`.
- The Harvester UI extension chart version is pinned in `main.tf`. Update it when upgrading Harvester.
- On `terraform destroy`, the provisioner removes the `cattle-cluster-agent` from Harvester,
  cleanly deregistering it from Rancher.
