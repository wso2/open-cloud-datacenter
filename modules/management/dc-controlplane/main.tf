# ── Project + VM namespace ────────────────────────────────────────────────────
# Assigns the VM namespace to a Rancher project, which causes Rancher to
# annotate the namespace with field.cattle.io/projectId. The
# namespace-credential-provisioner watches for that annotation (on namespaces
# without the network-namespace label) and creates two things:
#   1. harvester-cloud-provider-<ns> ServiceAccount + token in the namespace
#   2. harvesterconfig-<cluster> secret in fleet-default, referenced by the
#      cluster's cloud-provider-config
module "project" {
  source = "../tenant-space"

  providers = {
    kubernetes.harvester = kubernetes.harvester
  }

  cluster_id               = var.harvester_cluster_id
  project_name             = var.project_name
  create_default_namespace = true
  group_role_bindings      = []
}

# ── Management network NAD ────────────────────────────────────────────────────
# Untagged bridge on the mgmt cluster network. Cluster VMs live on the same
# L2 segment as the LoadBalancer IPPool so kube-vip can ARP-announce VIPs.
resource "harvester_network" "mgmt" {
  name                 = "${var.project_name}-mgmt"
  namespace            = var.project_name
  vlan_id              = 0
  cluster_network_name = var.mgmt_cluster_network
  route_mode           = "auto"

  lifecycle {
    ignore_changes = [labels]

    precondition {
      condition     = var.create_local_storage_class || var.node_image_name != ""
      error_message = "node_image_name is required when create_local_storage_class is false. Pass a pre-existing image reference (typically the 00-bootstrap layer's vm_image_id output)."
    }
  }

  depends_on = [module.project]
}

# ── VM root-disk StorageClass (Longhorn, 1 replica, strict-local) ────────────
# Provisioned on the Harvester HOST cluster — that's where Longhorn actually
# runs. The downstream control-plane VMs reference this SC by name in their
# HarvesterConfig disk_info; Harvester then provisions each VM's root disk
# as a single-replica volume pinned to the same host as the VM.
#
# Trade: losing the Harvester node hosting a VM loses that VM's root disk
# (no replicated copy elsewhere). etcd quorum at the application layer is
# what makes this acceptable for control-plane clusters — losing 1 of 3
# VMs is survivable; 2 of 3 is not. With only 2 Harvester hosts available,
# at-rest layout will be unavoidably 2 VMs on one host, 1 on the other.
resource "kubernetes_storage_class_v1" "vm_local" {
  count    = var.create_local_storage_class ? 1 : 0
  provider = kubernetes.harvester

  metadata {
    name = var.local_storage_class_name
  }

  storage_provisioner    = "driver.longhorn.io"
  reclaim_policy         = "Delete"
  volume_binding_mode    = "Immediate"
  allow_volume_expansion = true

  # dataLocality: Harvester's validator webhook rejects "strict-local"
  # because it would forbid VM live-migration of any workload using this
  # SC. "best-effort" tells Longhorn to TRY to co-locate the replica with
  # the consumer, but allow migration when needed. Single-replica + best-
  # effort gets us the no-network-replication fsync benefit (the actual
  # bottleneck) without fighting Harvester's migration model.
  parameters = {
    numberOfReplicas    = "1"
    dataLocality        = "best-effort"
    fsType              = "ext4"
    migratable          = "true"
    staleReplicaTimeout = "30"
  }
}

# Resolved SC name for the VM root disks. Empty string = let Harvester use
# the host cluster's default StorageClass (the original module behaviour).
locals {
  vm_storage_class_name = (
    var.vm_storage_class_override != "" ? var.vm_storage_class_override :
    var.create_local_storage_class ? var.local_storage_class_name :
    ""
  )
}

# ── VirtualMachineImage on the local SC ──────────────────────────────────────
# Harvester's VM provisioner ignores the storageClassName field passed via
# HarvesterConfig.disk_info — it clones the boot disk from the source image's
# PVC and the cloned PVC inherits the SOURCE image's StorageClass, not the
# requested one. To actually land VM root disks on dcapi-controlplane-local
# we have to give the source image itself that SC. So when the operator opts
# into the local SC, the module also creates its own VirtualMachineImage in
# the project namespace, downloaded from the same upstream URL, but with
# storageClassName pinned. VMs cloned from this image inherit the local SC.
resource "harvester_image" "dcapi_node" {
  count = var.create_local_storage_class ? 1 : 0

  name         = "dcapi-controlplane-ubuntu"
  namespace    = var.project_name
  display_name = var.node_image_display_name
  source_type  = "download"
  url          = var.node_image_url

  storage_class_name = local.vm_storage_class_name

  depends_on = [
    module.project,
    kubernetes_storage_class_v1.vm_local,
  ]
}

# Resolved image reference passed to each machine_pool. When the module
# created its own image, use that. Otherwise the operator must supply
# node_image_name themselves (typically from a bootstrap layer's output).
locals {
  effective_image_name = (
    var.create_local_storage_class && length(harvester_image.dcapi_node) > 0
    ? "${var.project_name}/${harvester_image.dcapi_node[0].name}"
    : var.node_image_name
  )
}

# ── LoadBalancer IPPool ───────────────────────────────────────────────────────
# Single ingress VIP for the dc-api ingress. The API VIP is NOT in this pool —
# kube-vip claims it directly via ARP on the node side.
resource "harvester_ippool" "lb" {
  name = "${var.cluster_name}-lb"

  range {
    start   = var.ingress_vip
    end     = var.ingress_vip
    subnet  = var.lb_subnet
    gateway = var.lb_gateway
  }
}

# ── kube-vip static-pod manifest ──────────────────────────────────────────────
# Rendered once at the module level; the same manifest is shipped to every
# server node via cloud-init write_files. Each kube-vip instance uses lease-
# based leader election so only one node ARP-announces the API VIP at a time.
locals {
  kube_vip_manifest = templatefile("${path.module}/templates/kube-vip-rke2.yaml.tftpl", {
    kube_vip_image = var.kube_vip_image
    vip_interface  = var.node_interface_name
    api_vip        = var.api_vip
  })

  # write_files YAML scalar block needs every line of the manifest indented by
  # 6 spaces (4 for the list item's content + 2 for the parent key). Pre-indent
  # once here so the template just interpolates the block.
  kube_vip_manifest_indented = join("\n", [
    for line in split("\n", local.kube_vip_manifest) : "      ${line}"
  ])
}

# ── Per-node cloud-init ───────────────────────────────────────────────────────
# One rendered cloud-init per node, with the node's static IP baked into the
# bootcmd netplan block. Indexed so node_user_data[i] maps to node{i+1}.
locals {
  node_user_data = [
    for idx, ip in var.node_ips : templatefile("${path.module}/templates/node-cloud-init.yaml.tftpl", {
      interface_name             = var.node_interface_name
      node_ip                    = ip
      node_cidr_suffix           = var.node_mgmt_cidr_suffix
      default_gateway            = var.node_default_gateway
      dns_servers_json           = jsonencode(var.node_dns_servers)
      ssh_user                   = var.ssh_user
      node_password              = var.node_password
      ntp_server                 = var.ntp_server
      ssh_authorized_keys_json   = jsonencode(var.ssh_authorized_keys)
      kube_vip_manifest_indented = local.kube_vip_manifest_indented
    })
  ]

  # One pool per node, each with quantity=1. Pool naming is deterministic
  # (node1/node2/...) so per-node operations don't shuffle between applies.
  machine_pools = [
    for idx, ip in var.node_ips : {
      name               = "node${idx + 1}"
      vm_namespace       = var.project_name
      quantity           = 1
      cpu_count          = var.node_cpu_count
      memory_size        = var.node_memory_size
      disk_size          = var.node_disk_size
      image_name         = local.effective_image_name
      networks           = ["${var.project_name}/${harvester_network.mgmt.name}"]
      control_plane      = true
      etcd               = true
      worker             = true
      user_data          = local.node_user_data[idx]
      storage_class_name = local.vm_storage_class_name
    }
  ]
}

# ── RKE2 cluster ─────────────────────────────────────────────────────────────
# cloud_provider_config_secret references the secret that the
# namespace-credential-provisioner creates automatically once the VM namespace
# is assigned to a project (via module.project above).
#
# Default machine_global_config tolerates the multi-hundred-ms etcd fsync
# latencies seen on Harvester/Longhorn-backed VM disks. Three adjustments
# vs. RKE2 defaults:
#
#   * tls-san: includes the API VIP (and any extra hostnames the operator
#     passes) so the apiserver cert is valid for client connections through
#     kube-vip's announced VIP.
#
#   * kube-apiserver: extend `etcd-healthcheck-timeout` from 2s → 10s so the
#     readyz etcd probe absorbs WAL fsync spikes instead of marking the
#     apiserver NotReady and oscillating Rancher between bootstrapping/active.
#
#   * kube-controller-manager / kube-scheduler: extend leader-election
#     lease/renew/retry from the 15s/10s/2s defaults to 60s/45s/10s so that
#     leader-renewal API calls (which write to etcd) tolerate slow fsyncs
#     instead of losing the lease and crashlooping every ~80 seconds.
#
# Note: the apiserver flag is `etcd-healthcheck-timeout` (one word).
# `etcd-health-check-timeout` is a typo and the binary rejects it on startup.
locals {
  tls_san_entries = distinct(concat([var.api_vip], var.tls_san_extra))

  machine_global_config = var.machine_global_config != null ? var.machine_global_config : <<-YAML
    cni: cilium
    disable-kube-proxy: false
    etcd-expose-metrics: false
    tls-san:
    %{~for san in local.tls_san_entries~}
      - ${san}
    %{~endfor~}
    kube-apiserver-arg:
      - etcd-healthcheck-timeout=10s
    kube-controller-manager-arg:
      - leader-elect-lease-duration=60s
      - leader-elect-renew-deadline=45s
      - leader-elect-retry-period=10s
    kube-scheduler-arg:
      - leader-elect-lease-duration=60s
      - leader-elect-renew-deadline=45s
      - leader-elect-retry-period=10s
  YAML
}

module "cluster" {
  source = "../../workloads/k8s-cluster"

  cluster_name        = var.cluster_name
  kubernetes_version  = var.kubernetes_version
  cloud_credential_id = var.cloud_credential_id

  cloud_provider_config_secret    = "harvesterconfig-${var.cluster_name}"
  enable_harvester_cloud_provider = true

  machine_global_config = local.machine_global_config

  machine_pools = local.machine_pools

  # No module-level user_data — every pool carries its own (each baked with
  # its node's static IP).
  user_data         = ""
  manage_rke_config = var.manage_rke_config

  depends_on = [
    harvester_network.mgmt,
    harvester_ippool.lb,
  ]
}

# ── IPPool scope patch (workaround) ──────────────────────────────────────────
# The IPPool is created without selector.scope. Without it, Harvester's LB
# controller times out with "no matched IPPool with requirement
# {Project:..., Namespace:<ns>, Cluster:kubernetes}".
#
# Format note: rancher2 outputs project_id as "cluster:project" (colon) but
# Harvester's LB matcher expects "cluster/project" (slash). Without the
# conversion below the patch is silently dropped by admission and only the
# namespace field survives, leaving the LB unable to match the pool.
#
# TODO: replace with a declarative harvester_ippool attribute once the
# Harvester provider exposes selector.scope.
locals {
  dcapi_project_id_slash = replace(module.project.project_id, ":", "/")
}

resource "null_resource" "ippool_scope_patch" {
  triggers = {
    cluster_id = module.cluster.cluster_id
    project_id = local.dcapi_project_id_slash
  }

  provisioner "local-exec" {
    command = <<-EOT
      kubectl --kubeconfig "${var.harvester_kubeconfig_path}" \
        patch ippool.loadbalancer.harvesterhci.io "${var.cluster_name}-lb" \
        --type=merge \
        -p '{"spec":{"selector":{"scope":[{"namespace":"${var.project_name}","project":"${local.dcapi_project_id_slash}"}]}}}'
    EOT
  }

  depends_on = [
    module.cluster,
    harvester_ippool.lb,
  ]
}
