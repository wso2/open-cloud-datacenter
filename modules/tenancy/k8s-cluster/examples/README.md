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
  source = "github.com/wso2/open-cloud-datacenter//modules/tenancy/k8s-cluster?ref=terraform/v0.1.7"

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

Add a new object to the `machine_pools` list and apply. Existing pools are not affected.

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
