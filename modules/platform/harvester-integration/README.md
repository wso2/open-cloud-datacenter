# Module: management/harvester-integration

Registers the Harvester HCI cluster into Rancher so that Rancher becomes the management
plane for virtual machines, projects, and workload cluster provisioning. This is a one-time
operation per Harvester cluster.

What this module does, in order:

1. Enables the Harvester feature flag in Rancher settings
2. Installs the Harvester UI extension (Helm chart from the harvester-ui-extension catalog)
3. Creates a cloud credential for Harvester — used later when provisioning RKE2 workload clusters
4. Optionally patches Harvester's CoreDNS so that Harvester nodes can resolve an internal Rancher hostname (off by default)
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
  source = "github.com/wso2/open-cloud-datacenter//modules/platform/harvester-integration?ref=terraform/v0.1.0"

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
| `manage_coredns` | Whether the module manages the Harvester Corefile at all. Set `false` to leave it to the `rke2-coredns` chart. | `bool` | `true` | no |
| `patch_coredns` | Add a `hosts` block mapping `rancher_lb_ip` → `rancher_hostname`. Requires `manage_coredns = true`. | `bool` | `false` | no |

## Outputs

| Name | Description |
|------|-------------|
| `harvester_cluster_id` | Rancher cluster ID for the imported Harvester HCI cluster. Pass as `cluster_id` to `tenant-space` and `rbac` modules. |
| `harvester_cluster_name` | Name of the Harvester cluster as registered in Rancher. |

## Notes

- **CoreDNS.** When `patch_coredns = true`, the Corefile gets a `hosts` block so Harvester nodes
  can resolve `rancher_hostname` (an internal name not in public DNS). This must happen before the
  registration manifest is applied — the module handles the ordering via `depends_on`.
- The Corefile is written with `kubernetes_config_map_v1_data` and `force = true`, seizing the field
  from the `rke2-coredns` chart, and the "unpatched" content is a **verbatim copy of one chart
  version's output**. Harvester releases ship different `rke2-coredns` versions, so on a cluster
  whose stock Corefile differs this silently overwrites the difference. Diff before enabling:

  ```bash
  kubectl -n kube-system get cm rke2-coredns-rke2-coredns -o jsonpath='{.data.Corefile}'
  ```

  Set `manage_coredns = false` when Rancher is on a publicly resolvable domain (the patch is
  pointless) or when the stock Corefile does not match. Set it at creation time — flipping
  `true` → `false` later destroys the `kubernetes_config_map_v1_data`, which removes the
  `Corefile` key and breaks cluster DNS.

  To stop managing the Corefile on a cluster where the resource already exists, do **both**,
  in this order:

  ```bash
  # 1. Terraform forgets the object; the live Corefile is left untouched.
  # No index: this removes every instance of the resource, matching both the
  # counted address and the unindexed one left by module versions predating
  # manage_coredns. Confirm the address first with: terraform state list
  terraform state rm \
    'module.<name>.kubernetes_config_map_v1_data.harvester_coredns_patch'

  # 2. Then set manage_coredns = false so the next plan does not recreate it.
  ```

  Neither step works alone: step 2 first *is* the destroy that breaks DNS, and step 1 alone
  leaves `manage_coredns = true`, so the next apply recreates the resource and force-writes
  the Corefile again. Confirm the following plan shows neither a create nor a destroy for
  that address.
- The supported RKE2 mechanism for changing this Corefile is a `HelmChartConfig` for `rke2-coredns`
  in `kube-system` with `valuesContent`, which lets the chart keep rendering it. The k3s-style
  `*-custom` ConfigMap with `.override`/`.server` keys does **not** exist in `rke2-coredns`.
- The registration manifest is applied via a `local-exec` provisioner using `kubectl`.
  The kubeconfig is written to a temp file and cleaned up via `trap EXIT`.
- The Harvester UI extension chart version is pinned in `main.tf`. Update it when upgrading Harvester.
- On `terraform destroy`, the provisioner removes the `cattle-cluster-agent` from Harvester,
  cleanly deregistering it from Rancher.
