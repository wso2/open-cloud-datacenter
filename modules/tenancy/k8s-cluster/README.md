# Module: workloads/k8s-cluster

Provisions a tenant RKE2 Kubernetes cluster on Harvester HCI via Rancher's machine provisioning API. Supports multiple machine pools, etcd S3 backups, private registry authentication, the Harvester cloud provider (CSI + load balancer), and optional [gVisor-sandboxed node pools](#gvisor-node-pools).

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.7 |
| rancher/rancher2 | ~> 13.1 |

## Usage

```hcl
module "my_rke2_cluster" {
  source = "github.com/wso2/open-cloud-datacenter//modules/tenancy/k8s-cluster?ref=terraform/v0.2.2"

  cluster_name        = "my-cluster"
  kubernetes_version  = "v1.32.13+rke2r1"
  cloud_credential_id = var.harvester_cloud_credential_id

  machine_pools = [
    {
      name          = "control-plane"
      vm_namespace  = "my-namespace"
      quantity      = 3
      cpu_count     = "4"
      memory_size   = "8"
      disk_size     = 50
      image_name    = "default/image-cwl4b"
      networks      = ["my-namespace/vm-subnet-001", "iaas/storage-network"]
      control_plane = true
      etcd          = true
      worker        = false
    },
    {
      name          = "worker"
      vm_namespace  = "my-namespace"
      quantity      = 2
      cpu_count     = "8"
      memory_size   = "16"
      disk_size     = 100
      image_name    = "default/image-cwl4b"
      networks      = ["my-namespace/vm-subnet-001", "iaas/storage-network"]
      control_plane = false
      etcd          = false
      worker        = true
    }
  ]

  manage_rke_config = true
  user_data         = local.node_user_data
}
```

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| `cluster_name` | Name of the downstream RKE2 cluster in Rancher | `string` | — | yes |
| `kubernetes_version` | RKE2 Kubernetes version (e.g. `v1.32.13+rke2r1`) | `string` | — | yes |
| `cloud_credential_id` | Harvester cloud credential secret name (`cattle-global-data:cc-xxxx`) | `string` | — | yes |
| `machine_pools` | List of machine pool definitions (see below) | `list(object)` | `[]` | yes (when `manage_rke_config = true`) |
| `manage_rke_config` | Create/manage machine configs and `rke_config`. Set `false` for brownfield clusters. | `bool` | `true` | no |
| `machine_config_overrides` | Existing machine config `kind`/`name` keyed by pool name, for brownfield pools that cannot be imported | `map(object)` | `{}` | no |
| `cni` | CNI plugin for the cluster | `string` | `"cilium"` | no |
| `machine_global_config` | Full `machine_global_config` YAML. When `null` the module generates a default from `cni`. | `string` | `null` | no |
| `user_data` | Full cloud-init override for every pool that does not set its own `user_data` (replaces the generated template) | `string` | `""` | no |
| `ssh_user` | SSH username for the VM OS | `string` | `"ubuntu"` | no |
| `enable_harvester_cloud_provider` | Configure `machine_selector_config` for the Harvester CSI/LB cloud provider | `bool` | `true` | no |
| `cloud_provider_config_secret` | `harvesterconfig*` secret name in `fleet-default` for brownfield clusters | `string` | `""` | no |
| `registries` | Private registry configuration (see below) | `object` | `null` | no |
| `etcd_s3` | S3 etcd backup configuration (see below) | `object` | `null` | no |
| `gvisor` | gVisor (runsc) settings shared by pools with `gvisor = true` (see [gVisor node pools](#gvisor-node-pools)) | `object` | `{}` (gVisor `20261012`) | no |
| `additional_manifest` | Extra multi-document YAML applied via `rke_config.additional_manifest`. The gVisor RuntimeClass is appended automatically. | `string` | `""` | no |

### `machine_pools` entries

| Field | Description | Type | Required |
|-------|-------------|------|----------|
| `name` | Unique pool name | `string` | yes |
| `vm_namespace` | Harvester namespace for the node VMs | `string` | yes |
| `quantity` | Number of nodes in the pool | `number` | yes |
| `cpu_count` | vCPU count as a string (e.g. `"4"`) | `string` | yes |
| `memory_size` | Memory in GiB as a string (e.g. `"16"`) | `string` | yes |
| `disk_size` | Root disk size in GiB | `number` | yes |
| `image_name` | Harvester image (`namespace/name`) | `string` | yes |
| `networks` | List of NAD names (`["ns/nad", ...]`) | `list(string)` | yes |
| `control_plane` | Pool has control-plane role | `bool` | yes |
| `etcd` | Pool has etcd role | `bool` | yes |
| `worker` | Pool has worker role | `bool` | yes |
| `machine_labels` | Labels applied to Kubernetes nodes | `map(string)` | no |
| `taints` | Node taints (`key`, `value`, `effect`) | `list(object)` | no |
| `user_data` | Full cloud-init override for this pool only (plain YAML or base64). Takes precedence over the module-level `user_data` and the generated template. Not allowed on `gvisor = true` pools. | `string` | no |
| `gvisor` | Install gVisor on this pool and label/taint it as a sandbox pool | `bool` | no (default `false`) |
| `gvisor_release` | Per-pool gVisor release override (falls back to `gvisor.release`) | `string` | no |

### `registries`

Configures private container registries for all nodes in the cluster.

```hcl
registries = {
  configs = [
    # New cluster — supply credentials directly, module creates the auth secret
    {
      hostname = "harbor.internal.example.com"
      username = var.registry_username
      password = var.registry_password
    },
    # Brownfield — auth secret was created outside Terraform (e.g. Rancher UI)
    {
      hostname                = "myregistry.azurecr.io"
      auth_config_secret_name = "registryconfig-auth-xxxxx"
    },
    # Insecure / self-signed TLS registry (no auth)
    {
      hostname = "internal-registry.local"
      insecure = true
    }
  ]
  mirrors = [
    {
      hostname  = "docker.io"
      endpoints = ["https://harbor.internal.example.com"]
    }
  ]
}
```

**Credential modes per config entry (mutually exclusive):**

| Mode | When to use | Fields |
|------|------------|--------|
| Inline credentials | New cluster, credentials managed by Terraform | `username` + `password` |
| Pre-existing secret | Brownfield — secret already exists in `fleet-default` | `auth_config_secret_name` |
| No auth | Public or IP-allowlisted registry | neither |

When `username`/`password` are supplied the module creates a `rancher2_secret_v2` of type `kubernetes.io/basic-auth` in the `fleet-default` namespace, named `<cluster-name>-registry-<sanitized-hostname>-<6-char-hash>`.

### `etcd_s3`

```hcl
etcd_s3 = {
  bucket              = "my-etcd-backups"
  folder              = "my-cluster"
  region              = "ap-southeast-1"
  cloud_credential_id = var.etcd_s3_credential_id
  snapshot_retention  = 5       # optional, default 3
  snapshot_schedule   = "0 2 * * *"  # optional, default "5 23 * * *"
}
```

## gVisor node pools

> **Requires `?ref=terraform/v0.2.2` or later.** Earlier releases do not have the
> `gvisor` pool field or variable.

Any pool can run pods under [gVisor](https://gvisor.dev) (`runsc`) by setting
`gvisor = true`. Typical topology: a control-plane pool, a general worker pool
(runc only) and a dedicated, tainted gVisor worker pool. Sandboxed pods then stay
on their own nodes, and you can add or roll gVisor without touching the rest of
the cluster.

```hcl
module "my_rke2_cluster" {
  source = "github.com/wso2/open-cloud-datacenter//modules/tenancy/k8s-cluster?ref=terraform/v0.2.2"
  # ...
  kubernetes_version = "v1.35.4+rke2r1"

  # Optional. Only needed to override defaults; the release is pinned to
  # 20261012 by default.
  # gvisor = {
  #   release = "20261012"
  # }

  machine_pools = [
    # ... control-plane and general worker pools ...
    {
      name          = "gvisor"
      vm_namespace  = "my-namespace"
      quantity      = 2
      cpu_count     = "4"
      memory_size   = "16" # Sentry/Gofer add per-pod memory overhead; size generously
      disk_size     = 80
      image_name    = "images/ubuntu-24-04"
      vm_network    = "my-namespace-net/vm-vlan"
      control_plane = false
      etcd          = false
      worker        = true
      gvisor        = true
    },
  ]
}
```

Workloads opt in with `runtimeClassName: gvisor`. Nothing else is needed, because the
RuntimeClass carries the node selector and toleration.

### How the gVisor settings fit together

| Setting | Where | Required | Purpose |
|---------|-------|----------|---------|
| `gvisor = true` | each pool | **yes** (the only thing you must set) | Turns that pool into a gVisor pool. Nothing gVisor-related happens without it. |
| `gvisor = { ... }` | module | no (every field has a default) | Cluster-wide gVisor settings shared by **all** gVisor pools: the default release, apt mirror, containerd/runsc options, and the node label, taint and RuntimeClass. One RuntimeClass serves the whole cluster, so these must be consistent across pools. |
| `gvisor_release` | each pool | no | Overrides the release for that one pool, e.g. to canary a newer release next to the stable pools. |

The release for a pool is resolved as **pool `gvisor_release` → `gvisor.release` → `20261012`**.
If the two are set to different values, there is no conflict: that pool installs its own
`gvisor_release`, and every other gVisor pool uses `gvisor.release`. All pools still share the
same handler, label, taint and RuntimeClass, so the same `runtimeClassName: gvisor` schedules
onto any of them.

### What the module does for a `gvisor = true` pool

| Piece | Where | Detail |
|-------|-------|--------|
| containerd runtime handler | generated cloud-init → `/var/lib/rancher/rke2/agent/etc/containerd/config-v3.toml.tmpl` | Extends `{{ template "base" . }}` (registries, CNI, runc stay as RKE2 renders them) and adds a `runsc` runtime pointing at `/etc/containerd/runsc.toml`. Written before RKE2 is installed. |
| gVisor install | generated cloud-init → `/usr/local/sbin/install-gvisor.sh <release>` | Adds gVisor's signed apt repo **pinned to the dated suite**, installs the complete `runsc` package (runsc + `containerd-shim-runsc-v1` + `gvisor-bin/`), `apt-mark hold`s it, and writes `runsc.toml` with the real `binary_name`. The script stays on disk for manual re-runs. |
| Node label | pool `machine_labels` | `sandbox.gvisor.dev/runtime=true` is merged in, and takes precedence over a clashing user label. |
| Node taint | pool `taints` | `sandbox.gvisor.dev/runtime=true:NoSchedule` is appended, unless the pool already declares a taint with that key or `gvisor.taint = false`. |
| RuntimeClass | `rke_config.additional_manifest` | `gvisor` → handler `runsc`, with `scheduling.nodeSelector` and an `Exists` toleration on the gVisor taint key, so it matches whatever value or effect the pool's taint uses. Optional `overhead.podFixed`. |
| Harvester CSI tolerations | `rke_config.chart_values` | `harvester-csi-driver.tolerations` set to "any `NoSchedule`", the etcd `NoExecute` default, and `Exists` on the gVisor taint key (any effect, including `NoExecute`), so the CSI node plugin runs on gVisor nodes and PVCs mount there. |

Pools without `gvisor = true` render exactly the same cloud-init as before, so
adding the feature never rolls existing pools.

### `gvisor` settings

Module-level settings shared by every `gvisor = true` pool. Every field is optional and
overridable, and the defaults follow the upstream recipe. Leave out the whole block to take
all defaults. To change the release for a single pool, use that pool's `gvisor_release`
instead of `release` (see [How the gVisor settings fit together](#how-the-gvisor-settings-fit-together)).

| Field | Default | Description |
|-------|---------|-------------|
| `release` | `"20261012"` | Dated apt suite installed on every gVisor pool that does not set its own `gvisor_release`. Use a July 2026 or later release (multi-file layout). Never use `release`/`latest` in production: every new node would install whatever is newest that day. |
| `apt_repo_url` | `https://storage.googleapis.com/gvisor/releases` | apt repository base, which you can point at an internal mirror |
| `apt_key_url` | `https://gvisor.dev/archive.key` | Repository signing key URL |
| `containerd_config_version` | `3` | `3` = containerd 2.x (`config-v3.toml.tmpl`, `io.containerd.cri.v1.runtime`); `2` = containerd 1.7 (`config.toml.tmpl`, `io.containerd.grpc.v1.cri`) |
| `runtime_handler` | `"runsc"` | containerd runtime name, which the RuntimeClass `handler` uses. Must be an RFC 1123 label (lowercase letters, digits, `-`). |
| `platform` | `"systrap"` | `systrap` works inside Harvester VMs; `kvm` needs nested virtualization |
| `systemd_cgroup` | `true` | Must match `SystemdCgroup` in RKE2's rendered containerd config |
| `runsc_config` | `{}` | Extra `[runsc_config]` flags, written literally (no shell expansion; quotes, backslashes and newlines are rejected) (e.g. `{ debug = "true", "debug-log" = "/var/log/runsc/%ID%/gvisor.%COMMAND%.log" }`), which override `platform`/`systemd-cgroup` on a key clash |
| `node_label_key` / `node_label_value` | `sandbox.gvisor.dev/runtime` / `true` | Label (and taint key/value) for gVisor nodes. If a gVisor pool already declares a taint with this key, the module keeps yours and skips its own. The RuntimeClass and CSI tolerate any value or effect on the key. |
| `taint` / `taint_effect` | `true` / `NoSchedule` | Taint that keeps ordinary pods off gVisor nodes |
| `create_runtime_class` | `true` | Deliver the RuntimeClass via `additional_manifest` |
| `runtime_class_name` | `"gvisor"` | Name workloads reference in `runtimeClassName` |
| `runtime_class_overhead` | `null` | `overhead.podFixed`, e.g. `{ memory = "64Mi", cpu = "50m" }` |
| `harvester_csi_tolerations` | `true` | Add the CSI driver tolerations described above |

**Constraints**
- gVisor pools need the module-generated cloud-init: a `gvisor = true` pool must not set its own `user_data`, and `var.user_data` must be empty, because gVisor pools would otherwise fall back to it. A precondition enforces this. Non-gVisor pools can still use a per-pool `user_data`.
- The CSI tolerations are merged into the module's `chart_values` on cluster create. If you
  pass your own `chart_values`, a `harvester-csi-driver` key in it wins. Because
  `chart_values` is in `ignore_changes`, adding gVisor to an **existing** cluster does not
  update them. Add the tolerations once through Rancher → cluster → **Edit YAML**
  (`spec.rkeConfig.chartValues.harvester-csi-driver.tolerations`).
- The ingress controller DaemonSet has no tolerations and stays on general workers. That is expected.
  Other node agents (monitoring, logging, security) need a toleration for the taint in their own chart values.
- CNI pods, the Harvester CCM/CSI, ingress, and anything using `hostNetwork`/`hostPID`/privileged mode must stay on runc. Never give them `runtimeClassName: gvisor`.

### Upgrading or canarying gVisor

1. Add a second pool with `gvisor = true` and `gvisor_release = "<new-date>"` (or test on one node with
   `sudo /usr/local/sbin/install-gvisor.sh <new-date>`).
2. Once validated, bump `gvisor.release` (or the pool's `gvisor_release`). The pool's cloud-init
   changes, which recreates its machine config. Use the two-phase apply described in Notes, and
   Rancher then rolls the pool one node at a time.
3. Check `runsc --version` on the new nodes and run a test pod (see `examples/README.md` §13).

gVisor itself is customer-maintained; validate workloads in a test cluster first.

## Cloud-init per pool

Each pool's cloud-init is resolved in this order:

1. **Pool `user_data`.** A full replacement for that pool only.
2. **Module `user_data`.** A full replacement for every pool that doesn't set its own.
3. **Generated template** (`templates/node-cloud-init.tpl`). Built from `node_password`,
   `ssh_authorized_keys` and `ntp_server`, plus the pool's storage netplan and gVisor sections.

`user_data` is a full replacement, not an addition. A pool that sets it does not get the
generated defaults (NTP, inotify sysctls, IPVS modules, storage netplan, `qemu-guest-agent`),
so include whatever it still needs. Changing a pool's `user_data` recreates its machine
config, so use the two-phase apply described in Notes.

```hcl
machine_pools = [
  { name = "control-plane", ... },                                     # generated template
  { name = "special",       ..., user_data = file("cloud-init/special.yaml") },  # own cloud-init
]
```

## Outputs

| Name | Description |
|------|-------------|
| `cluster_id` | Rancher v2 cluster ID (`fleet-default/<name>`) |
| `cluster_name` | Name of the provisioned downstream cluster |
| `cluster_v3_id` | Legacy v3 cluster ID (`c-m-xxxx`) for use in role bindings |

## Harvester cloud provider credential

The Harvester cloud provider (CSI driver + load balancer controller) on each RKE2 node
needs a kubeconfig secret — `harvesterconfig-<cluster-name>` — in Rancher's `fleet-default`
namespace. There are two ways this secret is created:

**Automatically (recommended):** If the platform team has deployed the
`namespace-credential-provisioner` (part of the management phase), it watches for new
clusters and creates `harvesterconfig-<cluster-name>` automatically. No action needed
beyond setting `cloud_provider_config_secret`:

```hcl
module "my_rke2_cluster" {
  # ...
  enable_harvester_cloud_provider = true
  cloud_provider_config_secret    = "harvesterconfig-${var.cluster_name}"
}
```

The secret follows the naming convention `harvesterconfig-<cluster_name>`. The provisioner
creates it within seconds of the cluster resource appearing in Rancher.

**Manually (standalone environments):** Use the `workloads/harvester-cloud-credential`
module (infra team only — requires direct Harvester kubeconfig):

```hcl
module "cloud_credential" {
  source = "github.com/wso2/open-cloud-datacenter//modules/addons/harvester-cloud-credential?ref=terraform/v0.2.2"
  # ...
}
module "my_rke2_cluster" {
  # ...
  cloud_provider_config_secret = module.cloud_credential.secret_name
  depends_on                   = [module.cloud_credential]
}
```

## Dynamic Harvester cloud credential (`create_cloud_credential`)

Separately from the cloud provider secret above, `create_cloud_credential = true` has the
module provision a `rancher2_cloud_credential` itself — the credential Rancher's Harvester
node driver uses to provision/manage VMs and disks for this cluster's machine pools. It's
built from the imported Harvester cluster's kubeconfig (`data.rancher2_cluster_v2.harvester`),
fetched dynamically via `harvester_cluster_name`/`harvester_cluster_id`, `rancher_api_url`,
and `rancher_api_token` — no kubeconfig content is ever supplied by hand.

```hcl
module "my_rke2_cluster" {
  # ...
  create_cloud_credential = true
  harvester_cluster_name  = "us-dc-harvester-cluster"
  rancher_api_url         = var.rancher_api_url
  rancher_api_token       = var.rancher_api_token
}
```

Rancher's embedded token for this credential expires periodically. To renew it, bump
`harvester_credential_rotation`:

```hcl
  harvester_credential_rotation = 1  # was 0 — fetches a fresh kubeconfig and updates
                                      # the existing credential in place
```

This requires Terraform >= 1.16 (the module's `store.sensitive_output` mechanism, which
keeps the token from ever rendering in plan/apply output).

## Brownfield import

For clusters already running in Rancher that were not provisioned by this module:

1. Set `manage_rke_config = false` — no `rancher2_machine_config_v2` resources are created and the `rke_config` block is omitted
2. Import the cluster resource:
   ```bash
   terraform import module.<name>.rancher2_cluster_v2.this fleet-default/<cluster-name>
   ```
3. For pools with existing machine configs, use `machine_config_overrides` to reference them by `kind`/`name` without Terraform trying to recreate them

## Notes

- `rke_config` changes (pool sizes, node images, etc.) are ignored after cluster creation — use Rancher UI or API for post-create pool changes to avoid triggering rolling upgrades
- Changing machine config specs on a pool causes the provider to recreate `rancher2_machine_config_v2` with a new random name. Apply in two phases: first target the machine config, then the full module
- `rancher2_machine_config_v2` does not support `terraform import`
