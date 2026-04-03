# Open Cloud Data Center

The Open Cloud Data Center initiative is focused on providing a standardized, scalable, and customizable cloud datacenter infrastructure.

## Overview

The Open Cloud Data Center simplifies the deployment and management of cloud infrastructure through a modular, open-source architecture.

**Why choose Open Cloud Data Center?**
- **Sovereignty**: Complete control over your data and infrastructure.
- **Portability**: Move workloads across cloud providers or on-premises hardware.
- **Cost-Efficiency**: Optimize resource usage and avoid vendor lock-in.
- **Community-Driven**: Built on open standards and collaborative development.

---

## Quick Start

```bash
git clone https://github.com/wso2/open-cloud-datacenter.git
cd open-cloud-datacenter
```

Reference any module directly from GitHub in your Terraform configuration:

```hcl
module "bootstrap" {
  source = "github.com/wso2/open-cloud-datacenter//modules/bootstrap?ref=v0.4.5"

  ubuntu_image_id        = "default/ubuntu-22-04"
  vm_password            = var.vm_password
  rancher_hostname       = "rancher.example.internal"
  rancher_admin_password = var.rancher_admin_password
  ippool_subnet          = "192.168.10.0/24"
  ippool_gateway         = "192.168.10.1"
  ippool_start           = "192.168.10.10"
  ippool_end             = "192.168.10.10"
}
```

---

## Modules

The following reusable Terraform modules are available under `modules/`. See the architecture overview in [docs/architecture.md](docs/architecture.md) for how they relate to each other.

### Bootstrap

| Module | Description |
|--------|-------------|
| [modules/bootstrap](modules/bootstrap/README.md) | Provisions an RKE2-based Rancher server on Harvester HCI via cloud-init, with a Load Balancer and IP pool for external access. |

### Identity

| Module | Description |
|--------|-------------|
| [modules/identity/rancher-oidc](modules/identity/rancher-oidc/README.md) | Configures Rancher to use a generic OIDC provider for user authentication. |
| [modules/identity/providers/asgardeo](modules/identity/providers/asgardeo/README.md) | Presets for integrating WSO2 Asgardeo as the identity provider. |

### Management

| Module | Description |
|--------|-------------|
| [modules/management/networking](modules/management/networking/README.md) | Creates and manages VLAN-backed Harvester networks for tenant and management workloads. |
| [modules/management/storage](modules/management/storage/README.md) | Downloads and registers OS images into Harvester HCI, making them available for VM provisioning. |
| [modules/management/cluster-roles](modules/management/cluster-roles/README.md) | Defines custom Rancher role templates (e.g. `vm-metrics-observer`) shared across tenant projects. |
| [modules/management/tenant-space](modules/management/tenant-space/README.md) | Full team onboarding: creates a Rancher project, namespace, resource quotas, and role bindings. |
| [modules/management/rbac](modules/management/rbac/README.md) | Lightweight module for bulk creating projects and namespaces without advanced role bindings. |
| [modules/management/harvester-integration](modules/management/harvester-integration/README.md) | Registers the Harvester HCI cluster into Rancher, enabling the UI extension and cloud credential. |

### Monitoring

| Module | Description |
|--------|-------------|
| [modules/monitoring](modules/monitoring/README.md) | Deploys a full monitoring stack (Prometheus / Alertmanager / Calert) with Google Chat notification support. |

### Workloads

| Module | Description |
|--------|-------------|
| [modules/workloads/k8s-cluster](modules/workloads/k8s-cluster/README.md) | Provisions a tenant RKE2 Kubernetes cluster on Harvester HCI via Rancher's machine provisioning API. |
| [modules/workloads/vm](modules/workloads/vm/README.md) | Provisions standalone virtual machines on Harvester HCI with support for multiple disks and cloud-init. |

---

## Deployment Phases

The modules are designed to be applied in sequence across five phases:

1. **Phase 0 — Bootstrap** (`modules/bootstrap`): Deploy RKE2 + Rancher inside Harvester.
2. **Phase 1 — Rancher Auth**: Connect the Rancher provider using the bootstrapped endpoint and password.
3. **Phase 2 — Management** (`modules/management/*`): Register Harvester into Rancher and configure shared resources (networks, images, roles).
4. **Phase 3 — Identity & Monitoring** (`modules/identity/*`, `modules/monitoring`): Configure OIDC authentication and observability.
5. **Phase 4 — Workloads** (`modules/workloads/*`): Provision tenant Kubernetes clusters or standalone VMs on demand.

See [docs/architecture.md](docs/architecture.md) for a detailed breakdown.

---

## Reporting Product Issues

- **GitHub Issues**: [Report bugs or request features](https://github.com/wso2/open-cloud-datacenter/issues)

---

## Contributing

We welcome contributions! Please see [CONTRIBUTING.md](docs/CONTRIBUTING.md) for details on our code of conduct and the process for submitting pull requests.

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.
