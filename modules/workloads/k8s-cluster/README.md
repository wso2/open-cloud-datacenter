# Module: workloads/k8s-cluster

Provisions a tenant RKE2 Kubernetes cluster on Harvester HCI via Rancher's machine provisioning API. Supports multiple machine pools, etcd S3 backups, private registry authentication, and the Harvester cloud provider (CSI + load balancer).

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.7 |
| rancher/rancher2 | ~> 13.1 |

## Usage

```hcl
module "my_rke2_cluster" {
  source = "github.com/wso2/open-cloud-datacenter//modules/workloads/k8s-cluster?ref=v0.8.0"

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
| `user_data` | cloud-init user-data applied to every node VM | `string` | `""` | no |
| `ssh_user` | SSH username for the VM OS | `string` | `"ubuntu"` | no |
| `enable_harvester_cloud_provider` | Configure `machine_selector_config` for the Harvester CSI/LB cloud provider | `bool` | `true` | no |
| `cloud_provider_config_secret` | `harvesterconfig*` secret name in `fleet-default` for brownfield clusters | `string` | `""` | no |
| `registries` | Private registry configuration (see below) | `object` | `null` | no |
| `etcd_s3` | S3 etcd backup configuration (see below) | `object` | `null` | no |

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
  source = "github.com/wso2/open-cloud-datacenter//modules/workloads/harvester-cloud-credential?ref=v0.8.0"
  # ...
}
module "my_rke2_cluster" {
  # ...
  cloud_provider_config_secret = module.cloud_credential.secret_name
  depends_on                   = [module.cloud_credential]
}
```

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
