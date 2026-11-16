terraform {
  required_version = ">= 1.5"
  required_providers {
    harvester = {
      source  = "harvester/harvester"
      version = "~> 1.7"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.30"
    }
  }
}

# Baseline configuration of a bootstrapped Harvester cluster: namespace, cluster
# networks, VM network, storage + RWX network, Longhorn StorageClasses and
# an OS image. Host disks and SSH keys are out of scope.
#
# Resource names match platform/rancher where the concept is the same, so state
# can be migrated with `moved {}` blocks. Apply before any VM exists: Harvester
# stops workloads and requires detached volumes to change the storage network.

# ── Harvester namespace ───────────────────────────────────────────────────────
# Creates a dedicated namespace when harvester_namespace is not "default".
resource "kubernetes_namespace" "harvester_ns" {
  count = var.harvester_namespace != "default" ? 1 : 0
  metadata {
    name = var.harvester_namespace
  }

  lifecycle {
    ignore_changes = [metadata[0].annotations]
  }
}

# ── Cluster networks + uplinks ────────────────────────────────────────────────
# mgmt is built in and cannot be created or given VLAN configs.
resource "harvester_clusternetwork" "cluster_network" {
  for_each = var.cluster_networks
  name     = each.key
}

resource "harvester_vlanconfig" "uplink" {
  for_each = var.vlan_configs

  name = each.key
  # Reference the resource when this module creates the cluster network so the
  # VLAN config is ordered after it.
  cluster_network_name = try(harvester_clusternetwork.cluster_network[each.value.cluster_network].name, each.value.cluster_network)
  node_selector        = each.value.node_selector

  uplink {
    nics      = each.value.nics
    bond_mode = each.value.bond_mode
    mtu       = each.value.mtu
  }

  # Harvester's webhook adds the clusternetwork label and defaults bond_miimon
  # to -1; neither is set here, so ignore them to avoid a permanent diff.
  lifecycle {
    ignore_changes = [labels, uplink[0].bond_miimon]
  }
}

# ── VM network ────────────────────────────────────────────────────────────────
# Creates a VM network (L2VlanNetwork) in harvester_namespace for VMs to attach
# to. VMs reference it as "<namespace>/<name>" (vm_network_name output).
resource "harvester_network" "vm_network" {
  count = var.create_vm_network ? 1 : 0

  name                 = var.vm_network_name
  namespace            = var.harvester_namespace
  vlan_id              = var.vm_network_vlan_id
  cluster_network_name = try(harvester_clusternetwork.cluster_network[var.vm_network_cluster_network].name, var.vm_network_cluster_network)

  route_mode    = var.vm_network_gateway != "" ? "manual" : "auto"
  route_cidr    = var.vm_network_gateway != "" ? var.vm_network_cidr : null
  route_gateway = var.vm_network_gateway != "" ? var.vm_network_gateway : null
  # Auto mode only: which DHCP server Harvester asks for the route.
  route_dhcp_server_ip = var.vm_network_gateway == "" && var.vm_network_dhcp_server_ip != "" ? var.vm_network_dhcp_server_ip : null

  # A VLAN network only becomes ready once its cluster network has an uplink.
  depends_on = [
    kubernetes_namespace.harvester_ns,
    harvester_vlanconfig.uplink,
  ]
}

# ── Input validation ──────────────────────────────────────────────────────────
check "vm_network_route_cidr_required" {
  assert {
    condition     = !var.create_vm_network || var.vm_network_gateway == "" || var.vm_network_cidr != ""
    error_message = "vm_network_cidr is required when vm_network_gateway is set."
  }
}

check "vm_network_dhcp_server_ip_auto_only" {
  assert {
    condition     = var.vm_network_dhcp_server_ip == "" || (var.vm_network_gateway == "" && var.vm_network_cidr == "")
    error_message = "vm_network_dhcp_server_ip is for auto route mode; leave vm_network_gateway and vm_network_cidr empty."
  }
}

check "rwx_network_requires_storage_network" {
  assert {
    condition     = !var.manage_rwx_network || var.manage_storage_network
    error_message = "manage_rwx_network shares the storage network, so manage_storage_network must be true."
  }
}

# ── Storage network ───────────────────────────────────────────────────────────
# Harvester's storage-network webhook rejects the setting until every VlanStatus
# of the cluster network is ready; harvester_vlanconfig does not wait for that.
# kubectl wait fails at once while no VlanStatus exists yet, so it is retried
# until the overall timeout.
resource "terraform_data" "wait_storage_vlan_ready" {
  count = var.manage_storage_network && var.storage_network_cluster_network != "mgmt" ? 1 : 0

  triggers_replace = {
    cluster_network = var.storage_network_cluster_network
    vlan_configs    = jsonencode(var.vlan_configs)
  }

  provisioner "local-exec" {
    environment = {
      KUBECONFIG      = var.harvester_kubeconfig_path
      CLUSTER_NETWORK = var.storage_network_cluster_network
      TIMEOUT         = tostring(var.network_wait_timeout_seconds)
    }
    command = <<-EOT
      deadline=$(( $(date +%s) + TIMEOUT ))
      until kubectl wait vlanstatuses.network.harvesterhci.io \
          -l "network.harvesterhci.io/clusternetwork=$CLUSTER_NETWORK" \
          --for=condition=ready --timeout=10s >/dev/null 2>&1; do
        if [ "$(date +%s)" -ge "$deadline" ]; then
          echo "vlan status not ready for cluster network $CLUSTER_NETWORK:" >&2
          kubectl get vlanstatuses.network.harvesterhci.io \
            -l "network.harvesterhci.io/clusternetwork=$CLUSTER_NETWORK" >&2
          exit 1
        fi
        sleep 10
      done
      echo "vlan status ready for cluster network $CLUSTER_NETWORK"
    EOT
  }

  # kubectl treats an empty KUBECONFIG as unset and falls back to the operator's
  # current context, where the wait could pass against another Harvester cluster.
  lifecycle {
    precondition {
      condition     = var.harvester_kubeconfig_path != ""
      error_message = "harvester_kubeconfig_path is required when manage_storage_network = true: the readiness waits run kubectl against it."
    }
  }

  depends_on = [harvester_vlanconfig.uplink]
}

# Isolates Longhorn replication traffic on a dedicated VLAN. Harvester pre-creates
# the storage-network Setting; harvester_setting adopts and updates it. The value
# has no "type" key — Harvester's network config has none (UI-only field).
resource "harvester_setting" "storage_network" {
  count = var.manage_storage_network ? 1 : 0

  name = "storage-network"
  value = jsonencode({
    vlan           = var.storage_network_vlan
    clusterNetwork = var.storage_network_cluster_network
    range          = var.storage_network_range
    exclude        = var.storage_network_exclude_ranges
  })

  # Inputs are known at plan time, so a bad value fails the plan before the VLAN
  # wait runs; otherwise an empty cluster network makes it wait out its timeout.
  lifecycle {
    precondition {
      condition     = var.storage_network_cluster_network != ""
      error_message = "storage_network_cluster_network is required when manage_storage_network = true."
    }
    precondition {
      condition     = var.storage_network_vlan >= 1 && var.storage_network_vlan <= 4094
      error_message = "storage_network_vlan must be a VLAN ID between 1 and 4094 when manage_storage_network = true."
    }
    precondition {
      condition     = can(cidrhost(var.storage_network_range, 0))
      error_message = "storage_network_range must be a valid CIDR (e.g. 172.23.0.0/19) when manage_storage_network = true."
    }
  }

  depends_on = [terraform_data.wait_storage_vlan_ready]
}

# The provider returns once the value is written; Harvester then reconfigures
# Longhorn asynchronously. Pass only when the controller has processed THIS value
# (hash annotation == sha1(value)) and then reports configured=True.
# Harvester writes the new hash together with configured=False/In Progress, and
# only sets True with reason Completed, so a stale Completed from the previous
# value cannot satisfy the wait. The hash is taken from the value the provider
# read back, and the annotation may not exist yet on a fresh cluster, so the
# waits are retried until the overall timeout.
resource "terraform_data" "wait_storage_network" {
  count = var.manage_storage_network ? 1 : 0

  triggers_replace = {
    value = harvester_setting.storage_network[0].value
  }

  provisioner "local-exec" {
    environment = {
      KUBECONFIG = var.harvester_kubeconfig_path
      VALUE_HASH = sha1(harvester_setting.storage_network[0].value)
      TIMEOUT    = tostring(var.storage_network_timeout_seconds)
    }
    command = <<-EOT
      deadline=$(( $(date +%s) + TIMEOUT ))
      until kubectl wait setting storage-network \
          --for=jsonpath='{.metadata.annotations.storage-network\.settings\.harvesterhci\.io/hash}'="$VALUE_HASH" \
          --timeout=15s >/dev/null 2>&1 &&
        kubectl wait setting storage-network \
          --for=condition=configured --timeout=15s >/dev/null 2>&1; do
        if [ "$(date +%s)" -ge "$deadline" ]; then
          echo "storage-network not applied:" >&2
          kubectl get setting storage-network -o jsonpath='{range .status.conditions[?(@.type=="configured")]}status={.status} reason={.reason} message={.message}{"\n"}{end}' >&2
          exit 1
        fi
        sleep 15
      done
      echo "storage-network applied"
    EOT
  }

  # Same KUBECONFIG fallback risk as wait_storage_vlan_ready; also guards the
  # mgmt path, where that wait is skipped.
  lifecycle {
    precondition {
      condition     = var.harvester_kubeconfig_path != ""
      error_message = "harvester_kubeconfig_path is required when manage_storage_network = true: the readiness waits run kubectl against it."
    }
  }
}

# ── RWX network ───────────────────────────────────────────────────────────────
# Shares the storage network for Longhorn RWX (NFS) traffic. Enabled only after
# the storage network has been applied successfully. Harvester requires RWX
# volumes to be detached for any rwx-network change.
resource "harvester_setting" "rwx_network" {
  count = var.manage_rwx_network && var.manage_storage_network ? 1 : 0

  name  = "rwx-network"
  value = jsonencode({ "share-storage-network" = true })

  depends_on = [terraform_data.wait_storage_network]
}

# ── Storage class ─────────────────────────────────────────────────────────────
# Creates a Longhorn StorageClass with a reduced replica count and makes it the
# cluster default. No StorageClass is deleted: the built-in harvester-longhorn
# only loses its default annotation.
# Set manage_storage_class = false to skip (brownfield clusters).

# Harvester's admission webhook rejects a second default ("default storage class
# ... already exists, please reset it first"), so this must run first.
resource "kubernetes_annotations" "harvester_longhorn_not_default" {
  count       = var.manage_storage_class ? 1 : 0
  api_version = "storage.k8s.io/v1"
  kind        = "StorageClass"

  metadata {
    name = "harvester-longhorn"
  }

  annotations = {
    "storageclass.kubernetes.io/is-default-class" = "false"
  }

  force = true
}

resource "harvester_storageclass" "default" {
  count = var.manage_storage_class ? 1 : 0

  name                   = var.storage_class_name
  is_default             = true
  volume_provisioner     = "driver.longhorn.io"
  allow_volume_expansion = true
  reclaim_policy         = "Delete"
  volume_binding_mode    = "Immediate"

  parameters = {
    numberOfReplicas    = tostring(var.storage_class_replicas)
    staleReplicaTimeout = "30"
    fromBackup          = ""
    migratable          = "true"
  }

  # Parameters are immutable, so a replica change would delete + recreate the
  # class. Fail the plan instead; create a new class under a new name.
  lifecycle {
    prevent_destroy = true
  }

  depends_on = [kubernetes_annotations.harvester_longhorn_not_default]
}

# Matches Harvester's documented RWX StorageClass; only the replica count is
# configurable. Not a default SC, so no ordering against the annotation.
resource "harvester_storageclass" "longhorn_rwx" {
  count = var.manage_storage_class ? 1 : 0

  name                   = "longhorn-rwx"
  volume_provisioner     = "driver.longhorn.io"
  allow_volume_expansion = true
  reclaim_policy         = "Delete"
  volume_binding_mode    = "Immediate"

  parameters = {
    numberOfReplicas    = tostring(var.rwx_storage_class_replicas)
    staleReplicaTimeout = "2880"
    fromBackup          = ""
    fsType              = "ext4"
    nfsOptions          = "vers=4.2,noresvport,softerr,timeo=600,retrans=5"
  }

  lifecycle {
    prevent_destroy = true
  }
}

# ── Cloud image ───────────────────────────────────────────────────────────────
# Downloads and registers the image when image_url is provided.
resource "harvester_image" "vm_image" {
  count        = var.image_url != "" ? 1 : 0
  name         = var.image_name
  namespace    = var.harvester_namespace
  display_name = var.image_display_name
  source_type  = "download"
  backend      = "backingimage"
  url          = var.image_url

  # Images take the cluster default StorageClass at creation time.
  depends_on = [
    harvester_storageclass.default,
    kubernetes_namespace.harvester_ns,
  ]
}
