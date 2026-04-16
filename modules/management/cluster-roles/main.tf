# Cluster-level custom role templates.
# Apply once per Rancher instance; referenced by tenant-space role bindings.

# Full lifecycle management of VMs within a tenant project.
# Covers create/update/delete of VMs and data volumes, power operations
# (start/stop/restart/migrate), and console/VNC access. Does NOT grant
# access to cluster-level resources or other tenants' namespaces.
resource "rancher2_role_template" "vm_manager" {
  name        = "vm-manager"
  description = "Full lifecycle management of VMs: create, configure, start/stop/restart, console access, and delete. Scoped to the tenant project."
  context     = "project"

  # Full CRUD on VM objects
  rules {
    api_groups = ["kubevirt.io"]
    resources  = ["virtualmachines", "virtualmachineinstances", "virtualmachineinstancepresets", "virtualmachineinstancereplicasets"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  # Power operations and console/VNC access
  rules {
    api_groups = ["subresources.kubevirt.io"]
    resources  = ["virtualmachines/start", "virtualmachines/stop", "virtualmachines/restart", "virtualmachines/migrate", "virtualmachineinstances/vnc", "virtualmachineinstances/console", "virtualmachineinstances/portforward", "virtualmachineinstances/pause", "virtualmachineinstances/unpause"]
    verbs      = ["get", "update"]
  }

  # VM metrics (for Harvester dashboard graphs)
  rules {
    api_groups = ["subresources.kubevirt.io"]
    resources  = ["virtualmachineinstances/metrics"]
    verbs      = ["get"]
  }

  # Data volumes (VM disks backed by PVCs)
  rules {
    api_groups = ["cdi.kubevirt.io"]
    resources  = ["datavolumes"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  # Read access to VM images available in the namespace
  rules {
    api_groups = ["harvesterhci.io"]
    resources  = ["virtualmachineimages"]
    verbs      = ["get", "list", "watch"]
  }

  # SSH keypairs — full CRUD so tenants can inject and remove keys via workloads/vm
  rules {
    api_groups = ["harvesterhci.io"]
    resources  = ["keypairs"]
    verbs      = ["get", "list", "watch", "create", "delete"]
  }

  # NetworkAttachmentDefinitions — project-scoped so tenants only see networks
  # within their own project's namespaces. Intentionally NOT in vm-creator
  # (cluster role) to prevent cross-tenant network visibility.
  rules {
    api_groups = ["k8s.cni.cncf.io"]
    resources  = ["network-attachment-definitions"]
    verbs      = ["get", "list", "watch"]
  }

  # Cloud-init secrets and SSH key secrets
  rules {
    api_groups = [""]
    resources  = ["secrets", "configmaps"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  # Service proxy for UI routing
  rules {
    api_groups = [""]
    resources  = ["services/proxy"]
    verbs      = ["get"]
  }
}

# Cluster-scoped role: exclusive control over Harvester VLAN infrastructure.
# context = "cluster" means rules apply at cluster (not project) scope, so this
# role can never be granted through a project role binding — it must be assigned
# via rancher2_cluster_role_template_binding, which requires operator-level access.
#
# Why consumers cannot create VLANs even as project-member:
#   a) VlanConfig and ClusterNetwork are cluster-scoped CRDs (not namespaced).
#   b) NetworkAttachmentDefinitions in harvester-public are outside their project namespace.
#   c) The built-in project-member role grants no cluster-level RBAC whatsoever.
# Consumers reference pre-created networks by name only (network_name in VM spec).
resource "rancher2_role_template" "network_manager" {
  name        = "network-manager"
  description = "Create, modify, and delete Harvester VLAN infrastructure (VlanConfig, ClusterNetwork, NodeNetwork) and NetworkAttachmentDefinitions. Restricted to DC operations group via cluster-level binding."
  context     = "cluster"

  # Harvester VLAN infrastructure — all cluster-scoped CRDs.
  # VlanConfig:    maps a VLAN ID to a ClusterNetwork interface on each node.
  # ClusterNetwork: represents a physical NIC/bond available for VLAN tagging.
  # NodeNetwork:   per-node network status and NIC inventory.
  # LinkMonitor:   monitors NIC link state across the cluster.
  rules {
    api_groups = ["network.harvesterhci.io"]
    resources  = ["vlanconfigs", "clusternetworks", "nodenetworks", "linkmonitors"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  # NetworkAttachmentDefinition: the namespace-scoped resource VMs reference by name.
  # DC ops creates these in harvester-public; consumers can list/get but not create.
  rules {
    api_groups = ["k8s.cni.cncf.io"]
    resources  = ["network-attachment-definitions"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }
}

# Cluster-scoped prerequisite for tenants who need to create VMs.
# Harvester stores shared resources (images and SSH keypairs) outside
# project namespaces — project-owner alone cannot see them. This role provides
# the minimum cluster-level read access required for the VM creation flow:
#   - VM image dropdown (VirtualMachineImage in default/harvester-public)
#   - SSH keypair dropdown (KeyPair in the tenant's namespace, but listed cluster-wide)
#
# Network dropdown: NAD read is intentionally on vm-manager (project-scoped),
# NOT here. Keeping it cluster-scoped would let tenants list NADs from all
# namespaces (default, other tenants), leaking network topology. With it
# project-scoped, the dropdown only shows networks inside their own project.
#
# Pair with a project role (vm-manager or project-owner) via a separate
# rancher2_cluster_role_template_binding for the same group.
resource "rancher2_role_template" "vm_creator" {
  name        = "vm-creator"
  description = "Cluster-level read access to shared Harvester resources (VM images, SSH keypairs) needed to create VMs. Pair with vm-manager (project role) for full VM lifecycle."
  context     = "cluster"

  # VM images are stored in the default or harvester-public namespace.
  # Without this, the image dropdown is empty when creating a VM.
  rules {
    api_groups = ["harvesterhci.io"]
    resources  = ["virtualmachineimages"]
    verbs      = ["get", "list", "watch"]
  }

  # SSH keypairs — read cluster-wide so the keypair dropdown populates.
  # The keypair itself lives in the tenant's namespace; the cluster-level
  # read is required for the Harvester UI to enumerate them.
  rules {
    api_groups = ["harvesterhci.io"]
    resources  = ["keypairs"]
    verbs      = ["get", "list", "watch"]
  }
}

# Project-scoped role for teams that operate but do not provision VMs.
# Grants power operations (start/stop/restart) and console/VNC access only.
# Intentionally excludes create, delete, migrate, and all data volume mutations
# so operators cannot provision or decommission VMs — only run them.
resource "rancher2_role_template" "vm_operator" {
  name        = "vm-operator"
  description = "Start, stop, restart, and access the console of existing VMs. No create, delete, or migrate permissions."
  context     = "project"

  # Read-only view of VM objects — operators need to see what exists
  rules {
    api_groups = ["kubevirt.io"]
    resources  = ["virtualmachines", "virtualmachineinstances"]
    verbs      = ["get", "list", "watch"]
  }

  # Power operations and console/VNC — migrate intentionally excluded
  rules {
    api_groups = ["subresources.kubevirt.io"]
    resources  = ["virtualmachines/start", "virtualmachines/stop", "virtualmachines/restart", "virtualmachineinstances/vnc", "virtualmachineinstances/console"]
    verbs      = ["get", "update"]
  }

  # VM metrics for Harvester dashboard graphs
  rules {
    api_groups = ["subresources.kubevirt.io"]
    resources  = ["virtualmachineinstances/metrics"]
    verbs      = ["get"]
  }

  # Read-only access to available VM images (needed to see disk info in UI)
  rules {
    api_groups = ["harvesterhci.io"]
    resources  = ["virtualmachineimages"]
    verbs      = ["get", "list", "watch"]
  }

  # Service proxy for Harvester UI routing
  rules {
    api_groups = [""]
    resources  = ["services/proxy"]
    verbs      = ["get"]
  }
}

# Cluster-scoped role for SREs who manage RKE2 node capacity.
# Grants the ability to scale machine pools and patch cluster specs without
# permission to create new clusters or delete existing ones.
resource "rancher2_role_template" "cluster_operator" {
  name        = "cluster-operator"
  description = "Scale and reconfigure RKE2 machine pools. No permission to create or delete clusters."
  context     = "cluster"

  # Rancher provisioning v2 cluster object — can edit (scale nodes) but not create/delete.
  # Machine pool count and nodeConfig live inside the cluster spec.
  rules {
    api_groups = ["provisioning.cattle.io"]
    resources  = ["clusters"]
    verbs      = ["get", "list", "watch", "update", "patch"]
  }

  # Read-only view of RKE2 control plane state
  rules {
    api_groups = ["rke.cattle.io"]
    resources  = ["rkecontrolplanes"]
    verbs      = ["get", "list", "watch"]
  }

  # etcd snapshots — read existing + create manual on-demand snapshots
  rules {
    api_groups = ["rke.cattle.io"]
    resources  = ["etcdsnapshots"]
    verbs      = ["get", "list", "watch", "create"]
  }

  # CAPI machine deployments and sets — needed to scale node pools
  rules {
    api_groups = ["cluster.x-k8s.io"]
    resources  = ["machinedeployments", "machinesets"]
    verbs      = ["get", "list", "watch", "update", "patch"]
  }

  # Rancher management cluster object — read-only (UI navigation, cluster health)
  rules {
    api_groups = ["management.cattle.io"]
    resources  = ["clusters"]
    verbs      = ["get", "list", "watch"]
  }
}

# Grants read-only visibility into VM status and metrics for the Harvester
# dashboard. Intentionally excludes all mutating verbs (update, patch, delete)
# and subresources that control VM power state (start, stop, restart, migrate).
resource "rancher2_role_template" "vm_metrics_observer" {
  name        = "vm-metrics-observer"
  description = "Read-only access to VM status and metrics. Allows Harvester dashboard graphs without any control-plane permissions."
  context     = "project"

  # VirtualMachine and VirtualMachineInstance status — list/watch only
  rules {
    api_groups = ["kubevirt.io"]
    resources  = ["virtualmachines", "virtualmachineinstances"]
    verbs      = ["get", "list", "watch"]
  }

  # VM instance metrics subresource — required for Harvester dashboard graphs
  rules {
    api_groups = ["subresources.kubevirt.io"]
    resources  = ["virtualmachineinstances/metrics"]
    verbs      = ["get"]
  }

  # Service proxy — allows the Harvester UI to route metric scrape requests
  # through the kube-apiserver without direct pod access
  rules {
    api_groups = [""]
    resources  = ["services/proxy"]
    verbs      = ["get"]
  }
}
