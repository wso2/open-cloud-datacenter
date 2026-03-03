# Architecture: Open Cloud Data Center on Harvester HCI

## Overview

The Open Cloud Data Center (OCDC) Terraform framework deploys and manages a full cloud-datacenter stack on top of [Harvester HCI](https://harvesterhci.io/). Harvester provides the hypervisor layer (based on KubeVirt), while Rancher provides the Kubernetes management plane. Tenant workload clusters are provisioned as RKE2 clusters running as virtual machines inside Harvester.

```
┌─────────────────────────────────────────────────────────────────┐
│                        Physical Nodes                           │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │                   Harvester HCI (KubeVirt)                │  │
│  │                                                           │  │
│  │  ┌─────────────────┐    ┌──────────────────────────────┐  │  │
│  │  │  Rancher Server  │    │    Tenant RKE2 Clusters      │  │  │
│  │  │  (RKE2 VM)       │    │  ┌──────────┐  ┌──────────┐ │  │  │
│  │  │                 │    │  │ Cluster A │  │ Cluster B │ │  │  │
│  │  │  cert-manager   │    │  │ (3 VMs)   │  │ (3 VMs)  │ │  │  │
│  │  │  Rancher UI     │    │  └──────────┘  └──────────┘ │  │  │
│  │  └────────┬────────┘    └──────────────────────────────┘  │  │
│  │           │ manages                                        │  │
│  │           └──────────────────────────────────────────────▶│  │
│  └───────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

---

## Deployment Phases

The framework is designed to be applied incrementally. Each phase depends on the outputs of the previous phase. The phases correspond directly to Terraform workspaces or directories.

### Phase 0 — Bootstrap

**Purpose**: Deploy a Rancher management server inside Harvester using cloud-init.

**Module**: `modules/bootstrap`

**What it does**:
- Generates an RSA SSH key pair and registers it as a Harvester SSH key.
- Creates a `harvester_cloudinit_secret` that embeds a cloud-init script.
- The cloud-init script installs RKE2, waits for the cluster to become ready, then installs cert-manager and Rancher via Helm — all inside the VM, without requiring external Terraform provider access to the cluster.
- Creates a Harvester VM (`harvester_virtualmachine`) on the masquerade network.
- Creates an IP pool (`harvester_ippool`) and a LoadBalancer (`harvester_loadbalancer`) that exposes ports 80 and 443 of the Rancher VM.

**Outputs used in later phases**:
- `rancher_hostname` — the FQDN to configure in DNS or `/etc/hosts`.
- `rancher_lb_ip` — the LoadBalancer IP to map to the hostname.

**Provider dependencies**: `harvester/harvester`, `hashicorp/tls`

---

### Phase 1 — Rancher Auth

**Purpose**: Establish authenticated Terraform sessions against the newly bootstrapped Rancher server.

**No dedicated module** — this phase is handled at the environment level by configuring the `rancher2` provider with the Rancher URL and bootstrap credentials.

```hcl
provider "rancher2" {
  api_url   = "https://rancher.example.internal"
  bootstrap = true
  ...
}
```

---

### Phase 2 — Management

**Purpose**: Register Harvester into Rancher and set up shared infrastructure (networks, images, RBAC).

This phase uses four modules, typically applied together:

#### 2a. harvester-integration (`modules/management/harvester-integration`)

- Enables the Harvester feature flag in Rancher settings.
- Installs the Harvester UI extension from the official Helm chart.
- Creates a `rancher2_cloud_credential` storing the Harvester kubeconfig — this credential is later used by tenant cluster provisioning.
- Creates a `rancher2_cluster` resource that imports Harvester as a virtualization management cluster.
- Patches the Harvester CoreDNS ConfigMap so Harvester nodes can resolve the internal Rancher hostname — this is required before the registration command is applied.
- Applies the Rancher registration manifest to Harvester using a `local-exec` provisioner and `kubectl`.
- Configures `harvester_setting` resources for `cluster-registration-url` and `rancher-cluster`.

**Provider dependencies**: `rancher/rancher2 ~> 8.0.0`, `harvester/harvester ~> 0.6.0`, `hashicorp/kubernetes ~> 2.30.0`

#### 2b. networking (`modules/management/networking`)

- Creates `harvester_network` resources for each VLAN defined in the `vlans` input map.
- Attaches each VLAN to the specified cluster network (e.g., `mgmt`).
- Networks created here are referenced by name when provisioning tenant clusters.

**Provider dependencies**: `harvester/harvester ~> 0.6.0`

#### 2c. storage (`modules/management/storage`)

- Downloads OS images from public URLs into Harvester using `harvester_image` resources.
- Images are stored in the specified namespace and referenced by name when provisioning tenant clusters.

**Provider dependencies**: `harvester/harvester ~> 0.6.0`

#### 2d. rbac (`modules/management/rbac`)

- Creates Rancher projects (`rancher2_project`) on the Harvester cluster with CPU, memory, and storage quotas.
- Creates a default namespace (`rancher2_namespace`) for each project, named `<team>-ns`.
- Isolates tenant teams from each other using Rancher's project-level RBAC.

**Provider dependencies**: `rancher/rancher2 ~> 3.0`

---

### Phase 3 — Tenants

**Purpose**: Provision on-demand Kubernetes clusters for tenant teams.

**Module**: `modules/workloads/k8s-cluster`

**What it does**:
- Fetches the Harvester cloud credential from Rancher (`data.rancher2_cloud_credential`).
- Defines a `rancher2_machine_config_v2` describing the VM size, image, and network for cluster nodes.
- Provisions a `rancher2_cluster_v2` RKE2 cluster using the machine config.
- Each cluster gets a dedicated machine pool combining control-plane, etcd, and worker roles.

**Provider dependencies**: `rancher/rancher2 ~> 3.0`

---

### Phase 4 — Asgardeo Auth (Future)

**Purpose**: Integrate Asgardeo as an external OIDC identity provider for Rancher and tenant clusters.

This phase configures the `asgardeo` provider (or equivalent OIDC configuration in Rancher) so that user authentication is delegated to WSO2 Asgardeo instead of local Rancher accounts.

---

## Provider Dependency Summary

| Provider | Used In | Purpose |
|----------|---------|---------|
| `harvester/harvester ~> 0.6.0` | bootstrap, networking, storage, harvester-integration | Manage Harvester VMs, networks, images, settings |
| `hashicorp/tls ~> 4.0` | bootstrap | Generate SSH key pairs |
| `hashicorp/helm ~> 2.0` | bootstrap (declared, cloud-init handles install) | Helm provider declaration |
| `rancher/rancher2 ~> 3.0` | rbac, k8s-cluster | Manage Rancher projects, namespaces, cluster provisioning |
| `rancher/rancher2 ~> 8.0.0` | harvester-integration | Rancher settings, catalogs, apps, cloud credentials, cluster import |
| `hashicorp/kubernetes ~> 2.30.0` | harvester-integration | Patch Harvester CoreDNS ConfigMap |
| `asgardeo` | Phase 4 (future) | OIDC identity provider integration |

---

## Module Dependency Graph

```
                    ┌─────────────┐
                    │  bootstrap  │
                    │  (Phase 0)  │
                    └──────┬──────┘
                           │ rancher_lb_ip, rancher_hostname
                           ▼
                    ┌─────────────┐
                    │ rancher auth│
                    │  (Phase 1)  │
                    └──────┬──────┘
                           │ rancher2 provider configured
           ┌───────────────┼───────────────┐
           ▼               ▼               ▼
    ┌──────────────┐ ┌──────────┐ ┌───────────┐
    │  harvester-  │ │networking│ │  storage  │
    │ integration  │ │(Phase 2b)│ │(Phase 2c) │
    │  (Phase 2a)  │ └──────────┘ └───────────┘
    └──────┬───────┘
           │ cloud credential, cluster registered
           │
           ▼
       ┌────────┐
       │  rbac  │
       │(Phase  │
       │  2d)   │
       └────────┘
           │
           │ projects/namespaces ready
           ▼
    ┌────────────────┐
    │  k8s-cluster   │
    │  (Phase 3)     │
    │  (per tenant)  │
    └────────────────┘
```

---

## Network Architecture

Harvester uses two network modes for VMs:

- **Masquerade**: VMs get NAT'd outbound internet access via the Harvester node. The Rancher bootstrap VM uses this mode. External access is provided via a Harvester LoadBalancer with an IP pool drawn from a routable subnet.
- **VLAN (bridge)**: VMs are directly bridged onto a physical VLAN. Tenant cluster VMs use this mode, giving them routable IPs in the datacenter network fabric.

The `management/networking` module creates VLAN-backed networks in Harvester. These are referenced by `modules/workloads/k8s-cluster` when provisioning tenant VM node pools.

---

## Security Considerations

- Sensitive variables (`vm_password`, `rancher_admin_password`, `harvester_kubeconfig`) are marked `sensitive = true` in all modules. Supply them via a `*.secret.tfvars` file or a secrets manager integration — never commit them to source control.
- The bootstrap Rancher server uses a self-signed certificate by default (managed by cert-manager). In production, configure an ACME issuer or bring your own certificate.
- Rancher RBAC (projects/namespaces with resource quotas) provides soft multi-tenancy isolation between teams. For stronger isolation, provision each tenant a dedicated cluster using `modules/workloads/k8s-cluster`.
