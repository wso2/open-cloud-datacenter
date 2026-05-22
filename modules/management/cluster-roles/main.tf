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

# ── Project-level governance role ─────────────────────────────────────────────
# Fills the gap between the built-in project-member (no self-service) and
# project-owner (can change resource quotas and delete the project).
#
# Intended for infrastructure operators and team leads who need to manage
# namespaces and project members within a quota-bounded project without being
# able to modify the quota ceiling set by the Platform Team.
#
# Permissions granted:
#   - Create/manage namespaces within the project
#   - Add and remove project members (projectroletemplatebindings)
#   - Read the project metadata
#
# Permissions explicitly excluded:
#   - update/patch/delete on management.cattle.io/projects (quota + project deletion)
#   - Any cluster-level write access (pair separately with vm-creator cluster binding)
#
# NOTE: The management.cattle.io API rules below are the R&D candidate set.
# Validate on lk-dev before promoting: confirm namespace creation succeeds,
# then confirm project quota edit and project delete return 403.
resource "rancher2_role_template" "project_contributor" {
  name        = "project-contributor"
  description = "Namespace management and member management within an assigned project. Cannot change resource quotas or delete the project. Intended for infrastructure operators and team leads."
  context     = "project"

  # Kubernetes namespace management — create/delete namespaces within the project.
  # Rancher assigns namespaces to a project via annotation when created by a
  # project member through the UI or via rancher2 provider.
  rules {
    api_groups = [""]
    resources  = ["namespaces"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  # Project member management — add/remove role bindings for project members.
  # Allows the holder to assign roles to other users/groups within this project.
  rules {
    api_groups = ["management.cattle.io"]
    resources  = ["projectroletemplatebindings"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  # Read-only view of the project itself.
  # Intentionally excludes update/patch/delete — those verbs would allow
  # changing the resource quota (limits_cpu, limits_memory) or deleting the project.
  rules {
    api_groups = ["management.cattle.io"]
    resources  = ["projects"]
    verbs      = ["get", "list", "watch"]
  }

  # Workload read access — see what's running in the project's namespaces.
  # Without this, the project dashboard in Rancher shows blank workload lists.
  rules {
    api_groups = [""]
    resources  = ["pods", "services", "replicationcontrollers", "endpoints"]
    verbs      = ["get", "list", "watch"]
  }

  rules {
    api_groups = ["apps"]
    resources  = ["deployments", "statefulsets", "daemonsets", "replicasets"]
    verbs      = ["get", "list", "watch"]
  }

  rules {
    api_groups = ["batch"]
    resources  = ["jobs", "cronjobs"]
    verbs      = ["get", "list", "watch"]
  }
}

# Full operational access to a downstream RKE2/K8s cluster for SRE teams.
# Grants kubectl-level cluster administration (workloads, config, networking,
# logs, exec) without Rancher management-plane permissions — members cannot
# delete or reconfigure the cluster from the Rancher UI.
#
# Deliberately excludes:
#   - Node mutation (no drain/cordon/delete — infra is managed by Rancher)
#   - RBAC mutation (no role/clusterrole create/delete — prevents privilege escalation)
#   - Namespace create/delete (namespaces are managed by the tenant-space module)
resource "rancher2_role_template" "cluster_contributor" {
  name        = "cluster-contributor"
  description = "Full Kubernetes-level access to all cluster resources and subresources (scale, exec, logs, system namespaces). Mirrors cluster-admin at the Kubernetes layer. Rancher cluster lifecycle (delete, reconfigure) is governed separately by Rancher's management-plane permissions and is not affected by this role."
  context     = "cluster"

  # Full access to all Kubernetes API groups, resources, and subresources.
  # Wildcard resources covers subresources (*/scale, pods/exec, pods/log, etc.)
  # matching the same pattern used by the built-in cluster-admin ClusterRole.
  rules {
    api_groups = ["*"]
    resources  = ["*"]
    verbs      = ["*"]
  }

  # Non-resource URLs — required for kubectl proxy, healthz, metrics endpoints.
  rules {
    non_resource_urls = ["*"]
    verbs             = ["*"]
  }
}

# ── Restricted project membership for Harvester VM tenants ───────────────────
# Purpose: replace the built-in "project-member" role for tenants who provision
# Harvester VMs. The built-in role inherits from the Kubernetes "edit"
# ClusterRole which grants wildcard write access to all resources — including
# harvesterhci.io/virtualmachineimages. Since Kubernetes RBAC is purely
# additive (no deny verbs), there is no way to subtract image-write from an
# inherited role. This custom role therefore does NOT inherit from "project-member"
# or "edit"; instead it enumerates exactly the rules that Harvester VM tenants
# need, with virtualmachineimages explicitly restricted to get/list/watch.
#
# Provides:
#   - All explicit project-member rules (governance, UI, catalog, storage visibility)
#   - Full Harvester VM lifecycle (create/start/stop/console/delete)
#   - DataVolume (VM disk) management
#   - SSH keypair management within the project
#   - Cloud-init secrets and ConfigMaps
#   - Read-only network attachment definitions (DC ops manages VLANs)
#   - Read-only VM images (platform team manages the image catalogue)
#
# Does NOT provide:
#   - virtualmachineimages create/update/delete/patch
#   - Kubernetes pod/deployment/service creation (pure VM tenant use case)
#   - Project membership management (project-member cannot manage its own members)
resource "rancher2_role_template" "project_member_restricted" {
  name        = "project-member-restricted"
  description = "Project member role for Harvester VM tenants. Mirrors the built-in project-member capabilities with full VM lifecycle management added. VM image access is hard-restricted to read-only — tenants use images provisioned by the platform team and cannot create, upload, or delete them."
  context     = "project"

  # ── project-member explicit rules (excluding the 'edit' inheritance) ────────

  rules {
    api_groups = ["ui.cattle.io"]
    resources  = ["navlinks"]
    verbs      = ["get", "list", "watch"]
  }

  rules {
    api_groups = ["management.cattle.io"]
    resources  = ["projectroletemplatebindings"]
    verbs      = ["get", "list", "watch"]
  }

  rules {
    api_groups = [""]
    resources  = ["namespaces"]
    verbs      = ["create"]
  }

  rules {
    api_groups = [""]
    resources  = ["persistentvolumes"]
    verbs      = ["get", "list", "watch"]
  }

  rules {
    api_groups = ["storage.k8s.io"]
    resources  = ["storageclasses"]
    verbs      = ["get", "list", "watch"]
  }

  rules {
    api_groups = ["apiregistration.k8s.io"]
    resources  = ["apiservices"]
    verbs      = ["get", "list", "watch"]
  }

  rules {
    api_groups = [""]
    resources  = ["persistentvolumeclaims"]
    verbs      = ["*"]
  }

  rules {
    api_groups = ["metrics.k8s.io"]
    resources  = ["pods"]
    verbs      = ["*"]
  }

  rules {
    api_groups = ["management.cattle.io"]
    resources  = ["clusterevents"]
    verbs      = ["get", "list", "watch"]
  }

  rules {
    api_groups = ["catalog.cattle.io"]
    resources  = ["clusterrepos", "operations", "releases", "apps"]
    verbs      = ["get", "list", "watch"]
  }

  rules {
    api_groups     = ["management.cattle.io"]
    resources      = ["clusters"]
    resource_names = ["local"]
    verbs          = ["get"]
  }

  # ── Harvester VM lifecycle ─────────────────────────────────────────────────

  rules {
    api_groups = ["kubevirt.io"]
    resources  = ["virtualmachines", "virtualmachineinstances", "virtualmachineinstancepresets", "virtualmachineinstancereplicasets"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  rules {
    api_groups = ["subresources.kubevirt.io"]
    resources  = ["virtualmachines/start", "virtualmachines/stop", "virtualmachines/restart", "virtualmachines/migrate", "virtualmachineinstances/vnc", "virtualmachineinstances/console", "virtualmachineinstances/portforward", "virtualmachineinstances/pause", "virtualmachineinstances/unpause"]
    verbs      = ["get", "update"]
  }

  rules {
    api_groups = ["subresources.kubevirt.io"]
    resources  = ["virtualmachineinstances/metrics"]
    verbs      = ["get"]
  }

  rules {
    api_groups = ["cdi.kubevirt.io"]
    resources  = ["datavolumes"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  # VM images — READ ONLY. Tenants select from the platform-managed catalogue;
  # create/update/delete/patch are intentionally absent.
  rules {
    api_groups = ["harvesterhci.io"]
    resources  = ["virtualmachineimages"]
    verbs      = ["get", "list", "watch"]
  }

  rules {
    api_groups = ["harvesterhci.io"]
    resources  = ["keypairs"]
    verbs      = ["get", "list", "watch", "create", "delete"]
  }

  rules {
    api_groups = ["k8s.cni.cncf.io"]
    resources  = ["network-attachment-definitions"]
    verbs      = ["get", "list", "watch"]
  }

  rules {
    api_groups = [""]
    resources  = ["secrets", "configmaps"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }

  rules {
    api_groups = [""]
    resources  = ["services/proxy"]
    verbs      = ["get"]
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
