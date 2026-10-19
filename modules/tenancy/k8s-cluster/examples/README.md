# Cluster Provisioning Guide

This guide walks you through provisioning your own RKE2 Kubernetes cluster inside a tenant space on a Harvester HCI + Rancher datacenter using Terraform.

The sample Terraform files in this directory are a working starting point — copy them into your own directory, fill in your values, and follow the steps below.

---

## Table of Contents

1. [Prerequisites](#1-prerequisites)
2. [Get Your Rancher API Token](#2-get-your-rancher-api-token)
3. [Find Your Network Details](#3-find-your-network-details)
4. [Find Available OS Images](#4-find-available-os-images)
5. [Create Your Terraform Configuration](#5-create-your-terraform-configuration)
   - [versions.tf](#51-versionstf)
   - [providers.tf](#52-providerstf)
   - [variables.tf](#53-variablestf)
   - [main.tf](#54-maintf)
   - [terraform.tfvars](#55-terraformtfvars)
   - [secret.tfvars](#56-secrettfvars--secrets)
6. [Machine Pool Sizing Reference](#6-machine-pool-sizing-reference)
7. [Grant Cluster Access](#7-grant-cluster-access)
8. [Plan and Apply](#8-plan-and-apply)
9. [Get Your Kubeconfig](#9-get-your-kubeconfig)
10. [Managing Your Cluster](#10-managing-your-cluster)
11. [Troubleshooting](#11-troubleshooting)
12. [Backups](#12-backups)
13. [gVisor Sandboxed Node Pools (optional)](#13-gvisor-sandboxed-node-pools-optional)

---

## 1. Prerequisites

### Tools

Install the following on your local machine before starting:

| Tool | Minimum Version | Install |
|------|----------------|---------|
| Terraform | `>= 1.7` | https://developer.hashicorp.com/terraform/install |
| kubectl | Latest stable | https://kubernetes.io/docs/tasks/tools/ |

### Tenant space

A **tenant space** must be provisioned on the Harvester cluster before you can create a cluster. A tenant space gives your team a dedicated Rancher project, namespaces, resource quotas, RBAC, and isolated VM networks. Refer to the [tenant-space module](../../tenant-space/) for how to provision one.

Once a tenant space exists, gather the following information before proceeding. All of these values are visible in the Rancher and Harvester UIs.

| Field | Description | How to find it |
|-------|-------------|---------------|
| **Rancher URL** | Base URL of the Rancher instance | Provided when the datacenter is set up |
| **Harvester cluster name** | Name of the upstream Harvester cluster registered in Rancher | Rancher UI → Virtualization Management → cluster name |
| **Project name** | Your Rancher project | Rancher UI → your cluster → Projects/Namespaces |
| **VM namespace** | Namespace in Harvester where your node VMs will be created | Rancher UI → your project → Namespaces |
| **Network namespace** | Namespace that holds your network resources (NADs) | Harvester UI → Networks → VM Networks → Namespace column |
| **VM network name** | Full NAD reference for your primary VM network | Harvester UI → Networks → VM Networks (see section 3) |
| **Storage network name** | Full NAD reference for the storage network (if allocated) | Harvester UI → Networks → VM Networks (see section 3) |

> **Note:** The **VM namespace** and the **network namespace** are different. Node VMs are created in the VM namespace. Network resources (NADs / VLANs) live in the network namespace. You will use both when configuring machine pools.

---

## 2. Get Your Rancher API Token

Your Rancher API token is the credential Terraform uses to talk to Rancher. It is **sensitive** — treat it like a password and never commit it to git.

1. Open your Rancher URL in a browser and log in.
2. Click your **user avatar** (top-right corner) → **Account & API Keys**.
3. Click **Add API Key**.
4. Fill in:
   - **Description:** something meaningful, e.g. `terraform-my-cluster`
   - **Scope:** leave as `No Scope` (cluster-scoped tokens do not work for provisioning)
   - **Expiry:** set a reasonable expiry (e.g. 30 or 90 days)
5. Click **Create**.
6. **Copy the token immediately** — it is only shown once. The format is:

   ```text
   token-xxxxx:xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
   ```

   Store it securely (e.g. a password manager). You will put it in `secret.tfvars` in step 5.6.

---

## 3. Find Your Network Details

### Find your VM network and storage network names

1. Log in to Rancher → navigate to the **Harvester** cluster (**Virtualization Management** → select the cluster).
2. In the Harvester dashboard, go to **Networks** → **VM Networks**.
3. Filter by your **network namespace** using the namespace dropdown.
4. Note the **Name** of your VM network and storage network (if you have one).

The full NAD reference used in Terraform combines the namespace and the name:

```text
<network-namespace>/<name>
```

For example, a NAD named `my-team-vlan601` in namespace `my-team-net` becomes:

```text
my-team-net/my-team-vlan601
```

---

## 4. Find Available OS Images

Cluster node VMs are provisioned from OS images stored in the Harvester image catalog.

1. In the Harvester dashboard, go to **Images**.
2. Filter by namespace: `images` (the shared image namespace).
3. The `image_name` value for Terraform is `images/<name>` — for example `images/ubuntu-24-04`.

Common images:

| image_name | OS |
|------------|----|
| `images/ubuntu-24-04` | Ubuntu 24.04 LTS (recommended) |
| `images/ubuntu-22-04` | Ubuntu 22.04 LTS |

---

## 5. Create Your Terraform Configuration

Copy the sample files from this directory into a new directory for your cluster:

```bash
cp -r . ~/my-cluster
cd ~/my-cluster
cp terraform.tfvars.example terraform.tfvars
```

Then edit each file as described below.

```text
my-cluster/
├── versions.tf        # Terraform + provider version pins
├── providers.tf       # Rancher provider configuration
├── variables.tf       # Input variable declarations
├── main.tf            # k8s-cluster module call
├── terraform.tfvars   # Non-sensitive variable values
└── secret.tfvars      # Sensitive values (NEVER commit to git)
```

### 5.1 versions.tf

No changes needed.

```hcl
terraform {
  required_version = ">= 1.7"

  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 13.1"
    }
  }
}
```

### 5.2 providers.tf

No changes needed.

```hcl
provider "rancher2" {
  api_url   = var.rancher_api_url
  token_key = var.rancher_api_token
}
```

### 5.3 variables.tf

No changes needed.

### 5.4 main.tf

Replace every value marked with `# REPLACE` with your own values from sections 1–4.

```hcl
module "my_cluster" {
  source = "github.com/wso2/open-cloud-datacenter//modules/tenancy/k8s-cluster?ref=terraform/v0.3.0"

  # ── Cluster identity ────────────────────────────────────────────────────────
  cluster_name       = "my-cluster"        # REPLACE: your cluster name
  kubernetes_version = "v1.34.9+rke2r1"

  # ── Rancher / Harvester connection ──────────────────────────────────────────
  create_cloud_credential         = true
  enable_harvester_cloud_provider = true
  harvester_cluster_name          = var.harvester_cluster_name
  rancher_api_url                 = var.rancher_api_url
  rancher_api_token               = var.rancher_api_token

  # REPLACE: the VM namespace from your tenant space
  harvester_vm_namespace = "my-team"

  # ── Networking ──────────────────────────────────────────────────────────────
  cni                = "cilium"        # options: cilium (default), calico, canal
  ingress_controller = "ingress-nginx" # options: ingress-nginx (default), traefik, none

  # ── Node configuration ──────────────────────────────────────────────────────
  node_password       = var.node_password
  ssh_authorized_keys = var.ssh_authorized_keys
  ntp_server          = var.ntp_server
  manage_rke_config   = true

  # ── Machine pools ───────────────────────────────────────────────────────────
  machine_pools = [
    {
      name         = "controlplane-worker"
      vm_namespace = "my-team"                               # REPLACE: VM namespace
      quantity     = 3
      cpu_count    = "4"
      memory_size  = "8"
      disk_size    = 50
      image_name   = "images/ubuntu-24-04"

      # Primary NIC — gets the default route.
      # Format: "<network-namespace>/<nad-name>"
      vm_network = "my-team-net/my-team-vlan601"            # REPLACE

      # Storage NIC — optional, remove if not allocated.
      storage_network = "my-team-net/my-team-strg-vlan698"  # REPLACE or remove

      control_plane = true
      etcd          = true
      worker        = true
    }
  ]
}
```

#### Kubernetes version reference

Versions follow the format `v<k8s-version>+rke2r<patch>` — for example `v1.34.9+rke2r1`. Browse available releases at https://github.com/rancher/rke2/releases.

The table below reflects the currently supported component versions. Always verify against the official support matrices before upgrading.

| Component | Supported Versions |
|-----------|-------------------|
| RKE2 | v1.33, v1.34, v1.35 |
| K3s | v1.33, v1.34, v1.35 |
| Harvester | v1.7.x |
| Rancher CD | v0.15.2 |
| Longhorn | v1.11.2 |

Support matrix references:
- Rancher v2.14.2: https://www.suse.com/suse-rancher/support-matrix/all-supported-versions/rancher-v2-14-2/
- Harvester v1.7.x: https://www.suse.com/suse-harvester/support-matrix/all-supported-versions/harvester-v1-7-x/

#### CNI options

| CNI | Notes |
|-----|-------|
| `cilium` | Default. eBPF-based, best performance |
| `calico` | Good for policy-heavy workloads |
| `canal` | Flannel + Calico policies, legacy use |

### 5.5 terraform.tfvars

Fill in your values. You can safely commit this file to git.

```hcl
rancher_api_url        = "https://your-rancher-url"  # REPLACE
harvester_cluster_name = "your-harvester-cluster"    # REPLACE
ntp_server             = "pool.ntp.org"

# ssh_authorized_keys is optional — add your SSH public keys here if needed
```

### 5.6 secret.tfvars — Secrets

**Never commit this file to git.** Add it to `.gitignore` first:

```bash
echo "secret.tfvars" >> .gitignore
```

Then create the file:

```hcl
# secret.tfvars — DO NOT COMMIT
rancher_api_token = "token-xxxxx:xxxxxxxxxxxxxxxxxxxxxxxxxx"  # from step 2
node_password     = "choose-a-strong-password-here"
```

| Variable | Where to get it | Sensitive |
|----------|----------------|-----------|
| `rancher_api_token` | Rancher UI → Account & API Keys (step 2) | Yes |
| `node_password` | Choose yourself — used for the OS user on every node VM | Yes |

---

## 6. Machine Pool Sizing Reference

### Single combined pool (dev / small clusters)

```hcl
machine_pools = [
  {
    name          = "controlplane-worker"
    vm_namespace  = "my-team"
    quantity      = 3          # minimum 3 for etcd quorum
    cpu_count     = "2"
    memory_size   = "4"
    disk_size     = 30
    image_name    = "images/ubuntu-24-04"
    vm_network    = "my-team-net/my-team-vlan601"
    control_plane = true
    etcd          = true
    worker        = true
  }
]
```

### Separate control-plane and worker pools (production)

```hcl
machine_pools = [
  {
    name            = "control-plane"
    vm_namespace    = "my-team"
    quantity        = 3
    cpu_count       = "4"
    memory_size     = "8"
    disk_size       = 50
    image_name      = "images/ubuntu-24-04"
    vm_network      = "my-team-net/my-team-vlan601"
    storage_network = "my-team-net/my-team-strg-vlan698"
    control_plane   = true
    etcd            = true
    worker          = false
  },
  {
    name            = "worker"
    vm_namespace    = "my-team"
    quantity        = 2
    cpu_count       = "8"
    memory_size     = "16"
    disk_size       = 100
    image_name      = "images/ubuntu-24-04"
    vm_network      = "my-team-net/my-team-vlan601"
    storage_network = "my-team-net/my-team-strg-vlan698"
    control_plane   = false
    etcd            = false
    worker          = true
  }
]
```

### Pool field reference

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Unique name within the cluster |
| `vm_namespace` | string | Harvester namespace for node VMs — use your VM namespace |
| `quantity` | number | Number of nodes. Use 3+ for control-plane pools (etcd quorum) |
| `cpu_count` | string | vCPU count as a quoted string: `"4"` |
| `memory_size` | string | RAM in GiB as a quoted string: `"8"` |
| `disk_size` | number | Root disk in GiB as an integer: `50` |
| `image_name` | string | `"images/<image-name>"` — see section 4 |
| `vm_network` | string | Primary NIC with default route: `"<network-ns>/<nad-name>"` |
| `storage_network` | string | Optional storage NIC (no default route): `"<network-ns>/<storage-nad-name>"` |
| `control_plane` | bool | Node runs the RKE2 control plane |
| `etcd` | bool | Node runs etcd (set `true` on control-plane nodes) |
| `worker` | bool | Node runs workloads |
| `machine_labels` | map(string) | Optional Kubernetes node labels |
| `taints` | list(object) | Optional node taints (`key`, `value`, `effect`) |
| `user_data` | string | Optional full cloud-init override for this pool only. Replaces the generated defaults. Not allowed on gVisor pools. |
| `gvisor` | bool | Optional. `true` makes this a gVisor sandbox pool (see section 13) |
| `gvisor_release` | string | Optional per-pool gVisor release override (see section 13) |

---

## 7. Grant Cluster Access

The `cluster_members` input lets you grant Rancher users or groups a role on the cluster at provisioning time. This is the Terraform-managed equivalent of going to Rancher UI → your cluster → **Cluster Members** and adding members manually.

### Roles

| Role | Description |
|------|-------------|
| `cluster-member` | Read-only access to cluster resources (default) |
| `cluster-owner` | Full administrative access to the cluster |

### Identity lookup methods

Each entry in `cluster_members` must set **exactly one** identifier field:

| Field | Resolved as | Example |
|-------|-------------|---------|
| `email` | User by email address | `dev@example.com` |
| `user_id` | Bare Rancher user ID | `u-427g5iiyyg` |
| `user_principal_id` | Full principal ID (local or OIDC user) | `local://u-abc123` |
| `group_principal_id` | Full principal ID for an OIDC group | `genericoidc_group://platform-team` |
| `name` + `type = "group"` | Group by display name | — |

### Example

Add `cluster_members` to your `main.tf` module block:

```hcl
cluster_members = [
  # Grant a specific user cluster-owner by email
  { email = "lead@example.com", role = "cluster-owner" },

  # Grant a team's OIDC group cluster-member access (default role, can omit role field)
  { group_principal_id = "genericoidc_group://my-team", role = "cluster-member" },

  # Grant a local Rancher user by their user principal ID
  { user_principal_id = "local://u-427g5iiyyg", role = "cluster-member" },
]
```

> **Tip:** Find a user's principal ID in Rancher UI → **Users & Authentication** → click the user → copy the ID shown in the URL or the user detail page. For OIDC groups, the principal ID follows the pattern `genericoidc_group://<group-name>` where the group name matches what your identity provider sends in the groups claim.

---

## 8. Plan and Apply

```bash
# 1. Initialise — downloads the module and providers
terraform init

# 2. Review what will be created
terraform plan -var-file="secret.tfvars"

# 3. Apply — confirm the plan then type "yes"
terraform apply -var-file="secret.tfvars"
```

Provisioning takes **10–20 minutes** depending on the number of nodes. Watch progress in the Rancher UI:

1. Log in to Rancher → **Cluster Management**.
2. Your cluster will appear with status `Provisioning`.
3. Wait for the status to become `Active`.

Individual nodes progress through: `Waiting → Provisioning → Running`.

---

## 9. Get Your Kubeconfig

Once the cluster status is `Active`:

1. Log in to Rancher → **Cluster Management** → click your cluster.
2. Click the **Download KubeConfig** button (top-right of the cluster detail page).
3. Save the file, e.g. `~/.kube/my-cluster.kubeconfig`.

```bash
export KUBECONFIG=~/.kube/my-cluster.kubeconfig
kubectl get nodes
```

> **Security note:** Kubeconfig files contain cluster admin credentials. Store them securely and do not commit them to git.

---

## 10. Managing Your Cluster

### Scaling a pool

Edit `quantity` in the relevant `machine_pools` entry and re-apply:

```bash
terraform apply -var-file="secret.tfvars"
```

### Upgrading Kubernetes

Update `kubernetes_version` in `main.tf` to the new version and apply. Rancher performs a rolling upgrade automatically. Always upgrade one minor version at a time (e.g. 1.33 → 1.34, not 1.33 → 1.35).

### Adding a node pool

Add a new object to the `machine_pools` list and apply. Existing pools are not affected. To add a gVisor sandbox pool, see [section 13](#13-gvisor-sandboxed-node-pools-optional).

### Destroying the cluster

```bash
terraform destroy -var-file="secret.tfvars"
```

This removes the cluster from Rancher and deletes all node VMs. **This is irreversible** — back up any persistent data before running this.

---

## 11. Troubleshooting

### Cluster stuck in `Provisioning`

1. In the Rancher UI, click your cluster → **Machines** tab.
2. Click a machine that shows an error → **Conditions** tab for details.
3. Common causes:
   - **Wrong network reference** — verify your `vm_network` and `storage_network` values match exactly what is shown in Harvester UI → Networks → VM Networks (namespace + name, case-sensitive).
   - **Image not found** — verify your `image_name` in Harvester UI → Images.
   - **Insufficient quota** — verify your total requested resources (CPU × quantity across all pools) are within the project quota visible in Rancher → your project → resource limits.

### `Error: Provider produced inconsistent result after apply`

Re-run `terraform apply -var-file="secret.tfvars"`. This is a known transient issue with the Rancher provider when the cluster is still initialising.

### `Error: timeout waiting for cluster to become active`

Re-run `terraform apply -var-file="secret.tfvars"`. If it persists, check node VM status in the Harvester dashboard → Virtual Machines → filter by your VM namespace.

### Terraform state issues after an interrupted apply

```bash
# Refresh state without making changes
terraform refresh -var-file="secret.tfvars"
```

---

## 12. Backups

Once your cluster is `Active`, configure backups — both layers are self-service
using your own S3 buckets and the same Terraform directory:

- **etcd snapshots** protect the cluster control plane (whole-cluster restore).
- **Velero** protects workloads and PersistentVolume data (per-namespace restore).

See **[BACKUPS.md](./BACKUPS.md)** for the full guide — bucket + IAM setup,
Terraform, verification, and restore for both.

---

## 13. gVisor Sandboxed Node Pools (optional)

[gVisor](https://gvisor.dev) runs pods inside a user-space kernel (`runsc`), which gives
untrusted or multi-tenant workloads a much stronger isolation boundary than plain
containers. The module can turn any machine pool into a gVisor pool. Your other pools
are not affected.

### 13.1 Before you start

| Requirement | Detail |
|-------------|--------|
| Module version | `?ref=terraform/v0.3.0` or later |
| Kubernetes version | RKE2 with embedded containerd 2.x. On Harvester 1.8 use `v1.35.4+rke2r1` or later. |
| OS image | Ubuntu 24.04 (`images/ubuntu-24-04`) |
| Egress from node VMs | `gvisor.dev` (signing key) and `storage.googleapis.com/gvisor/releases` (apt repo) |
| A pinned gVisor release | The module pins `20261012` by default. To use another dated release from the [gVisor releases](https://gvisor.dev/docs/user_guide/install/), override it (section 13.3) and test that exact release first (July 2026 or later). |

> **Why pin?** If you track the latest release instead, every new node (scale-up, auto-replaced
> node, RKE2 upgrade) installs whatever is newest that day, and one pool ends up running
> mixed, untested versions. A dated release gives every node the same version, today or in six months.

### 13.2 Add the gVisor pool to `main.tf`

Add a new pool with `gvisor = true`. That is the only required change; the module-level `gvisor` block is optional and only needed to override defaults (section 13.3):

```hcl
module "my_cluster" {
  source = "github.com/wso2/open-cloud-datacenter//modules/tenancy/k8s-cluster?ref=terraform/v0.3.0"

  # ... everything from section 5.4 ...
  kubernetes_version = "v1.35.4+rke2r1"

  # ── gVisor (optional) ───────────────────────────────────────────────────────
  # Not needed for the defaults. Uncomment only to override, e.g. the release.
  # gvisor = {
  #   release = "20261012"
  # }

  machine_pools = [
    {
      name          = "control-plane"
      vm_namespace  = "my-team"
      quantity      = 3
      cpu_count     = "4"
      memory_size   = "8"
      disk_size     = 50
      image_name    = "images/ubuntu-24-04"
      vm_network    = "my-team-net/my-team-vlan601"
      control_plane = true
      etcd          = true
      worker        = false
    },
    {
      name          = "worker"           # general workloads (runc)
      vm_namespace  = "my-team"
      quantity      = 2
      cpu_count     = "4"
      memory_size   = "8"
      disk_size     = 80
      image_name    = "images/ubuntu-24-04"
      vm_network    = "my-team-net/my-team-vlan601"
      control_plane = false
      etcd          = false
      worker        = true
    },
    {
      name          = "gvisor"           # sandboxed workloads (runsc)
      vm_namespace  = "my-team"
      quantity      = 2
      cpu_count     = "4"
      memory_size   = "16"               # gVisor adds per-pod memory overhead
      disk_size     = 80
      image_name    = "images/ubuntu-24-04"
      vm_network    = "my-team-net/my-team-vlan601"
      control_plane = false
      etcd          = false
      worker        = true
      gvisor        = true
    },
  ]
}
```

Keep a general worker pool. The ingress controller, CoreDNS and other system pods
do not tolerate the gVisor taint and need somewhere to run.

Then plan and apply as in [section 8](#8-plan-and-apply). For a `gvisor = true` pool the
module automatically:

- installs the pinned gVisor release on every node from gVisor's signed apt repo, and holds it at that version
- registers a `runsc` runtime with RKE2's containerd
- labels the nodes `sandbox.gvisor.dev/runtime=true` and taints them `sandbox.gvisor.dev/runtime=true:NoSchedule`, so ordinary pods stay off
- creates a `gvisor` RuntimeClass that steers sandboxed pods onto those nodes
- lets the Harvester CSI driver run on gVisor nodes, so PVCs work there

### 13.3 Optional settings

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

Example: stable pools on the default release, plus a canary pool on a newer release.

```hcl
# gvisor block omitted → stable pools use 20261012
{ name = "gvisor",        gvisor = true, ... }                               # 20261012
{ name = "gvisor-canary", gvisor = true, gvisor_release = "20261101", ... }  # canary
```

Every module-level field can be overridden. Defaults follow the upstream recipe:

```hcl
gvisor = {
  release                   = "20261012"  # module default
  platform                  = "systrap"   # kvm needs nested virtualization on Harvester hosts
  systemd_cgroup            = true        # must match SystemdCgroup in RKE2's containerd config
  containerd_config_version = 3           # 2 only for older RKE2 with containerd 1.7
  runtime_class_name        = "gvisor"
  runtime_handler           = "runsc"
  node_label_key            = "sandbox.gvisor.dev/runtime"
  node_label_value          = "true"
  taint                     = true
  taint_effect              = "NoSchedule"
  runtime_class_overhead    = { memory = "64Mi", cpu = "50m" }       # tune after measuring
  apt_repo_url              = "https://storage.googleapis.com/gvisor/releases"  # or an internal mirror
  runsc_config              = {}          # extra runsc flags, e.g. { debug = "true" }
}
```

See the module [README](../README.md#gvisor-node-pools) for the full field reference.

### 13.4 Run a workload under gVisor

Add `runtimeClassName: gvisor` to the pod spec. You don't need node selectors or tolerations,
because the RuntimeClass adds them:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: untrusted-app
spec:
  replicas: 2
  selector:
    matchLabels: { app: untrusted-app }
  template:
    metadata:
      labels: { app: untrusted-app }
    spec:
      runtimeClassName: gvisor
      containers:
        - name: app
          image: nginx:1.27
          resources:                      # always set limits — the sandbox counts against the pod
            requests: { cpu: "100m", memory: "128Mi" }
            limits:   { cpu: "500m", memory: "256Mi" }
```

Never set `runtimeClassName: gvisor` on CNI pods, the Harvester CSI/cloud provider, ingress
controllers, node agents, or anything that uses `hostNetwork`, `hostPID`, privileged mode or host devices.

### 13.5 Verify

```bash
kubectl get runtimeclass gvisor
kubectl get nodes -l sandbox.gvisor.dev/runtime=true

kubectl run gv-test --image=busybox:1.36 --restart=Never \
  --overrides='{"spec":{"runtimeClassName":"gvisor"}}' -- dmesg
kubectl logs gv-test          # first line: "Starting gVisor..."
kubectl get pod gv-test -o wide   # NODE must be a gvisor-pool node
kubectl delete pod gv-test
```

On a gVisor node (SSH as `ubuntu`):

```bash
runsc --version                          # matches your pinned release
apt-mark showhold | grep runsc
cat /etc/containerd/runsc.toml
sudo grep -A4 "runtimes.runsc" /var/lib/rancher/rke2/agent/etc/containerd/config.toml
sudo grep -i SystemdCgroup /var/lib/rancher/rke2/agent/etc/containerd/config.toml  # should be true
cloud-init status --long
```

Check which DaemonSets cannot reach gVisor nodes. The ingress controller is expected in this list.
Monitoring, logging or security agents need a toleration for the taint:

```bash
kubectl get ds -A -o json | jq -r '
  .items[] | select([.spec.template.spec.tolerations[]?
    | select(((.operator == "Exists" and (.key == null or .key == "sandbox.gvisor.dev/runtime"))
              or .key == "sandbox.gvisor.dev/runtime") and (.effect == null or .effect == "NoSchedule"))
  ] | length == 0) | "\(.metadata.namespace)/\(.metadata.name)"'
```

### 13.6 Upgrading gVisor

1. Optional canary: add a second pool with `gvisor = true` and `gvisor_release = "<new-date>"`.
2. Change `gvisor.release` to the new date.
3. The pool's machine config changes, so apply in two phases:
   ```bash
   terraform apply -var-file="secret.tfvars" -target='module.my_cluster.rancher2_machine_config_v2.pool["gvisor"]'
   terraform apply -var-file="secret.tfvars"
   ```
   Rancher replaces the gVisor nodes one at a time. Other pools are untouched.
4. Re-run the checks in 13.5.

### 13.7 Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `no runtime for "runsc" is configured` | containerd template missing or wrong version | Check `/var/lib/rancher/rke2/agent/etc/containerd/config-v3.toml.tmpl` exists. For containerd 1.7 set `containerd_config_version = 2`. |
| `containerd-shim-runsc-v1: not found` | gVisor install failed or not finished | `sudo grep -i gvisor /var/log/cloud-init-output.log`; re-run `sudo /usr/local/sbin/install-gvisor.sh <release>` |
| `apt-get update` fails on the gVisor repo | Release not published as an apt suite, or no egress | Pick a newer dated release; check egress to `gvisor.dev` and `storage.googleapis.com` |
| Sandbox errors mentioning cgroup paths / `.slice` | `systemd_cgroup` doesn't match RKE2 | Set `gvisor.systemd_cgroup` to match `SystemdCgroup` in the rendered `config.toml` |
| gVisor pod stuck `Pending` | No labelled gVisor node | `kubectl get nodes -l sandbox.gvisor.dev/runtime=true` |
| gVisor pod with a PVC stuck `ContainerCreating` | CSI node plugin not on gVisor nodes (e.g. gVisor added to an existing cluster) | Add the `harvester-csi-driver` tolerations via Rancher → cluster → **Edit YAML** (see module README) |
| App fails only under gVisor | Unsupported syscall or `/proc` feature | Keep that workload on runc |
