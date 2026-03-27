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

  # Read access to VM images and SSH keypairs available in the namespace
  rules {
    api_groups = ["harvesterhci.io"]
    resources  = ["virtualmachineimages", "keypairs"]
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
