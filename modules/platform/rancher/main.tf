terraform {
  required_version = ">= 1.5"
  required_providers {
    harvester = {
      source  = "harvester/harvester"
      version = "~> 1.7"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.30"
    }
    null = {
      source  = "hashicorp/null"
      version = "~> 3.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.0"
    }
  }
}

# ── Harvester namespace ───────────────────────────────────────────────────────
# Creates a dedicated namespace for bootstrap resources when harvester_namespace
# is not "default". Labelled as infrastructure so the namespace-credential-
# provisioner skips it (it only provisions credentials for tenant namespaces).
resource "kubernetes_namespace" "harvester_ns" {
  count = var.harvester_namespace != "default" ? 1 : 0
  metadata {
    name = var.harvester_namespace
    labels = {
      "platform.wso2.com/role" = "infrastructure"
    }
  }

  lifecycle {
    ignore_changes = [metadata[0].annotations]
  }
}

# ── Cloud image ───────────────────────────────────────────────────────────────
# Downloads and registers the image when image_url is provided.
# Set ubuntu_image_id instead to reference a pre-existing image (brownfield).
resource "harvester_image" "vm_image" {
  count        = var.image_url != "" ? 1 : 0
  name         = var.image_name
  namespace    = var.harvester_namespace
  display_name = var.image_display_name
  source_type  = "download"
  backend      = "backingimage"
  url          = var.image_url

  depends_on = [
    kubernetes_storage_class_v1.default,
    kubernetes_namespace.harvester_ns,
  ]
}

locals {
  image_id = var.image_url != "" ? harvester_image.vm_image[0].id : var.ubuntu_image_id

  # Parse the Harvester kubeconfig so cloud-init can query the Kubernetes API from
  # inside each VM to discover its virt-launcher pod IP.  In masquerade mode every
  # VM gets the same internal address (10.0.2.2), so without the pod IP etcd peer
  # URLs conflict and the secondary nodes can never join.  The pod IPs are unique
  # and reachable between VMs via the Calico overlay (10.52.x.x range).
  #
  # Only parsed when kubeconfig_path is set AND node_count > 1 (single-node has no
  # etcd peers so the default 10.0.2.2 node-ip is fine).
  #
  # try() guards against file("") when kubeconfig_path is empty — Terraform
  # evaluates both branches of a ternary for errors, so a plain file() call
  # with an empty-string path would fail even when the condition is false.
  _kc_content = try(file(var.harvester_kubeconfig_path), "")
  _kc = (
    var.node_count > 1 && local._kc_content != ""
    ? yamldecode(local._kc_content)
    : null
  )
  harvester_api_url  = try(local._kc.clusters[0].cluster.server, "")
  harvester_ca_b64   = try(local._kc.clusters[0].cluster["certificate-authority-data"], "")
  harvester_cert_b64 = try(local._kc.users[0].user["client-certificate-data"], "")
  harvester_key_b64  = try(local._kc.users[0].user["client-key-data"], "")
}

check "image_source_required" {
  assert {
    condition     = var.image_url != "" || var.ubuntu_image_id != ""
    error_message = "Either image_url (to download a new image) or ubuntu_image_id (to reference an existing one) must be set."
  }
}

# ── RKE2 cluster join token ───────────────────────────────────────────────────
# Generated once and stored in Terraform state. All nodes receive the same token
# via their individual cloud-init secrets so they can form a cluster.
resource "random_password" "rke2_token" {
  length  = 64
  special = false
}

# ── SSH key pair (greenfield only) ────────────────────────────────────────────
# Set create_ssh_key = false to attach existing ssh_key_ids instead.
resource "tls_private_key" "bootstrap_key" {
  count     = var.create_ssh_key ? 1 : 0
  algorithm = "RSA"
  rsa_bits  = 4096
}

resource "harvester_ssh_key" "bootstrap_key" {
  count      = var.create_ssh_key ? 1 : 0
  name       = "${var.vm_name}-ssh-key"
  namespace  = var.harvester_namespace
  public_key = tls_private_key.bootstrap_key[0].public_key_openssh
  depends_on = [kubernetes_namespace.harvester_ns]
}

# ── Cloud-init secret (greenfield only) ──────────────────────────────────────
# Set create_cloudinit_secret = false and provide existing_cloudinit_secret_name instead.
resource "harvester_cloudinit_secret" "cloudinit" {
  count      = var.create_cloudinit_secret ? var.node_count : 0
  name       = var.node_count > 1 ? "${var.vm_name}-cloudinit-${count.index}" : "${var.vm_name}-cloudinit"
  namespace  = var.harvester_namespace
  depends_on = [kubernetes_namespace.harvester_ns]

  user_data = templatefile("${path.module}/templates/cloud-init.yaml.tpl", {
    password           = var.vm_password
    cluster_dns        = var.rancher_hostname
    rancher_password   = var.bootstrap_password
    ssh_public_key     = tls_private_key.bootstrap_key[0].public_key_openssh
    node_index         = count.index
    node_count         = var.node_count
    lb_ip              = var.ippool_start
    rke2_version       = var.rke2_version
    rancher_version    = var.rancher_version
    tls_source         = var.tls_source
    tls_cert_b64       = var.tls_source == "secret" ? base64encode(var.tls_cert) : ""
    tls_key_b64        = var.tls_source == "secret" ? base64encode(var.tls_key) : ""
    rke2_cluster_token = random_password.rke2_token.result
    primary_dns        = var.primary_dns
    # Pod IP discovery (masquerade) / ConfigMap join coordination (bridge+MetalLB):
    # both paths use the Harvester Kubernetes API with the embedded kubeconfig creds.
    harvester_api_url   = local.harvester_api_url
    harvester_ca_b64    = local.harvester_ca_b64
    harvester_cert_b64  = local.harvester_cert_b64
    harvester_key_b64   = local.harvester_key_b64
    harvester_namespace = var.harvester_namespace
    # MetalLB: when use_metallb = true, MetalLB is installed on node 0 and the
    # VIP (ippool_start) is announced via L2 on the VM VLAN instead of using
    # the Harvester LB controller (which cannot reach bridge-mode VM backends
    # without inter-VLAN routing from Harvester host nodes to the guest VLAN).
    use_metallb     = var.use_metallb
    metallb_version = var.metallb_version
    metallb_ip      = var.ippool_start
    vm_name         = var.vm_name
  })
}

locals {
  # Resolve the SSH key IDs: either freshly generated or caller-supplied
  ssh_key_ids = var.create_ssh_key ? [harvester_ssh_key.bootstrap_key[0].id] : var.ssh_key_ids
}

# ── Input validation ──────────────────────────────────────────────────────────

# The cloud-init template embeds the generated SSH public key. If create_ssh_key
# is false the tls_private_key resource is empty, causing an invalid-index error.
check "ssh_key_required_for_cloudinit" {
  assert {
    condition     = !var.create_cloudinit_secret || var.create_ssh_key
    error_message = "create_ssh_key must be true when create_cloudinit_secret is true (the cloud-init template embeds the generated SSH public key)."
  }
}

# When reusing an existing cloud-init secret, the name must be provided.
check "existing_cloudinit_secret_name_required" {
  assert {
    condition     = var.create_cloudinit_secret || var.existing_cloudinit_secret_name != ""
    error_message = "existing_cloudinit_secret_name is required when create_cloudinit_secret = false."
  }
}

# ── Bridge network (greenfield) ───────────────────────────────────────────────
# Creates a NetworkAttachmentDefinition in harvester_namespace so bridge-mode VMs
# can attach to it. The NAD name is var.network_name; VMs reference it as
# "<namespace>/<name>". Set create_bridge_network = false when the NAD already
# exists (brownfield) and pass the full "<namespace>/<name>" as network_name.
resource "harvester_network" "bridge" {
  count = var.network_type == "bridge" && var.create_bridge_network ? 1 : 0

  name                 = var.network_name
  namespace            = var.harvester_namespace
  vlan_id              = var.cluster_vlan_id
  cluster_network_name = var.cluster_network_name

  route_mode    = var.cluster_vlan_gateway != "" ? "manual" : "auto"
  route_cidr    = var.cluster_vlan_gateway != "" ? var.cluster_vlan_cidr : null
  route_gateway = var.cluster_vlan_gateway != "" ? var.cluster_vlan_gateway : null

  depends_on = [kubernetes_namespace.harvester_ns]
}

locals {
  # Full NAD reference for the VM spec. When we created the NAD ourselves the
  # name is short (e.g. "rancher-network"); we prefix the namespace. When the
  # caller owns a pre-existing NAD they must pass the full "<ns>/<name>" form.
  bridge_network_name = (
    var.network_type == "bridge" && var.create_bridge_network
    ? "${var.harvester_namespace}/${var.network_name}"
    : var.network_name
  )
}

# ── Rancher server VM ─────────────────────────────────────────────────────────
resource "harvester_virtualmachine" "rancher_server" {
  count                = var.node_count
  name                 = var.node_count > 1 ? "${var.vm_name}-${count.index}" : var.vm_name
  namespace            = var.harvester_namespace
  restart_after_update = true

  # Bridge NAD must exist before the VM; storage-network must be set before any
  # VM starts (Harvester rejects storage-network changes while VMs are running).
  depends_on = [
    null_resource.storage_network,
    harvester_network.bridge,
  ]

  cpu    = var.vm_cpu
  memory = var.vm_memory

  run_strategy = "RerunOnFailure"
  machine_type = "q35"

  ssh_keys = local.ssh_key_ids

  # Masquerade (NAT): default for greenfield; no external network required
  dynamic "network_interface" {
    for_each = var.network_type == "masquerade" ? [1] : []
    content {
      name = var.network_interface_name
      type = "masquerade"
    }
  }

  # Bridge: for VMs that need direct VLAN access (e.g. existing production VMs)
  dynamic "network_interface" {
    for_each = var.network_type == "bridge" ? [1] : []
    content {
      name         = var.network_interface_name
      type         = "bridge"
      network_name = local.bridge_network_name
      mac_address  = var.network_mac_address != "" ? var.network_mac_address : null
    }
  }

  disk {
    name       = var.vm_disk_name
    type       = "disk"
    size       = var.vm_disk_size
    bus        = "virtio"
    boot_order = 1

    image       = local.image_id
    auto_delete = var.vm_disk_auto_delete
  }

  # USB tablet input device — some VMs require this for correct cursor behaviour
  # in the Harvester console; set enable_usb_tablet = true to include it.
  dynamic "input" {
    for_each = var.enable_usb_tablet ? [1] : []
    content {
      name = "tablet"
      type = "tablet"
      bus  = "usb"
    }
  }

  cloudinit {
    user_data_secret_name    = var.create_cloudinit_secret ? harvester_cloudinit_secret.cloudinit[count.index].name : var.existing_cloudinit_secret_name
    network_data_secret_name = var.create_cloudinit_secret ? null : var.existing_cloudinit_secret_name
  }

  provisioner "local-exec" {
    command = var.create_cloudinit_secret ? "echo 'VM created — cloud-init will install RKE2/Rancher internally.'" : "echo 'VM imported — cloud-init ran at initial provision time.'"
  }
}

# ── Load Balancer + IP Pool (greenfield only) ─────────────────────────────────
# Set create_lb = false when the Rancher VM is reachable directly via its
# bridge IP (no dedicated LB/IP-pool needed).
resource "harvester_loadbalancer" "rancher_lb" {
  count     = var.create_lb && !var.use_metallb ? 1 : 0
  name      = "${var.vm_name}-lb"
  namespace = var.harvester_namespace

  depends_on = [
    harvester_virtualmachine.rancher_server,
    harvester_ippool.rancher_ips,
  ]

  workload_type = "vm"
  ipam          = "pool"
  ippool        = harvester_ippool.rancher_ips[0].name

  listener {
    name         = "https"
    port         = 443
    protocol     = "TCP"
    backend_port = 443
  }

  listener {
    name         = "http"
    port         = 80
    protocol     = "TCP"
    backend_port = 80
  }

  # Required for HA (node_count > 1): secondary nodes join by dialling the LB
  # supervisor port (9345) so the join address is stable regardless of which
  # node is currently the active RKE2 init server.
  listener {
    name         = "rke2-supervisor"
    port         = 9345
    protocol     = "TCP"
    backend_port = 9345
  }

  backend_selector {
    key    = "harvesterhci.io/vmName"
    values = harvester_virtualmachine.rancher_server[*].name
  }

  # Health check on port 9345 is safe for masquerade mode: the VIP lives on
  # mgmt-br which shares L2 with masquerade VM traffic, so probes succeed.
  # In bridge mode the VIP is on mgmt-br (192.168.11.x) but backends are on
  # the VLAN (172.24.0.x). The kube-vip probe goes out mgmt-br, hits the
  # datacenter router, and reaches the VM — but only if inter-VLAN routing is
  # configured at the switch/router level. When it is not, the probe returns
  # no response and healthy=0, preventing ANY traffic from being forwarded.
  # Omitting the healthcheck lets kube-vip treat all backends as healthy and
  # forward traffic unconditionally, which is safe once the cluster is up, and
  # lets secondary nodes reach node-0 for the initial join even before 443 is up.
  dynamic "healthcheck" {
    for_each = var.network_type == "masquerade" ? [1] : []
    content {
      port              = 9345
      success_threshold = 1
      failure_threshold = 3
      period_seconds    = 10
      timeout_seconds   = 5
    }
  }
}

resource "harvester_ippool" "rancher_ips" {
  count = var.create_lb && !var.use_metallb ? 1 : 0
  name  = "${var.vm_name}-ips"

  range {
    start   = var.ippool_start
    end     = var.ippool_end
    subnet  = var.ippool_subnet
    gateway = var.ippool_gateway
  }

  dynamic "selector" {
    for_each = var.ippool_network_name != "" ? [1] : []
    content {
      network = var.ippool_network_name
    }
  }
}

# ── Storage class ─────────────────────────────────────────────────────────────
# Creates a Longhorn StorageClass with a reduced replica count and marks it as
# the cluster default. The built-in harvester-longhorn (3 replicas) is unset as
# default so only one default exists at a time.
# Set manage_storage_class = false to skip (brownfield clusters where the SC
# already exists, or single-node setups where 3 replicas cannot be satisfied).
resource "kubernetes_storage_class_v1" "default" {
  count = var.manage_storage_class ? 1 : 0

  # Must run after harvester-longhorn loses its default annotation, otherwise
  # the Harvester admission webhook rejects creating a second default SC.
  depends_on = [kubernetes_annotations.harvester_longhorn_not_default]

  metadata {
    name = var.storage_class_name
    annotations = {
      "storageclass.kubernetes.io/is-default-class" = "true"
    }
  }

  storage_provisioner    = "driver.longhorn.io"
  allow_volume_expansion = true
  reclaim_policy         = "Delete"
  volume_binding_mode    = "Immediate"

  parameters = {
    numberOfReplicas    = tostring(var.storage_class_replicas)
    staleReplicaTimeout = "30"
    fromBackup          = ""
    fsType              = "ext4"
    migratable          = "true"
  }
}

# Remove the default annotation from the built-in harvester-longhorn StorageClass
# so there is exactly one cluster default after the new SC is created.
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

resource "kubernetes_storage_class_v1" "longhorn_rwx" {
  count      = var.manage_storage_class ? 1 : 0
  depends_on = [kubernetes_annotations.harvester_longhorn_not_default]

  metadata {
    name = "longhorn-rwx"
  }

  storage_provisioner    = "driver.longhorn.io"
  allow_volume_expansion = true
  reclaim_policy         = "Delete"
  volume_binding_mode    = "Immediate"

  parameters = {
    numberOfReplicas    = "1"
    staleReplicaTimeout = "2880"
    fromBackup          = ""
    fsType              = "ext4"
    nfsOptions          = "vers=4.2,noresvport,softerr,timeo=600,retrans=5"
  }
}

# ── Storage network ───────────────────────────────────────────────────────────
# Configures the Harvester storage-network Setting so Longhorn replication
# traffic is isolated on a dedicated VLAN rather than sharing the management NIC.
# Harvester stores the value as a JSON string inside the Setting CRD.
resource "null_resource" "storage_network" {
  count = var.manage_storage_network ? 1 : 0

  triggers = {
    config = jsonencode({
      vlan           = var.storage_network_vlan
      clusterNetwork = var.storage_network_cluster_network
      range          = var.storage_network_range
      exclude        = var.storage_network_exclude_ranges
    })
  }

  provisioner "local-exec" {
    environment = {
      KUBECONFIG = var.harvester_kubeconfig_path
      STORAGE_CONFIG = jsonencode({
        vlan           = var.storage_network_vlan
        clusterNetwork = var.storage_network_cluster_network
        range          = var.storage_network_range
        exclude        = var.storage_network_exclude_ranges
      })
    }
    # Harvester pre-creates an empty storage-network Setting; patch rather than create.
    command = <<-EOT
      python3 -c "
import subprocess, json, os
config = json.loads(os.environ['STORAGE_CONFIG'])
patch = json.dumps({'value': json.dumps(config)})
subprocess.run(
  ['kubectl', 'patch', 'setting', 'storage-network', '--type=merge', '-p', patch],
  env={**os.environ}, check=True)
print('storage-network patched:', patch)
"
    EOT
  }
}

# Patch the built-in longhorn StorageClass replica count.
# StorageClass parameters are immutable in the Kubernetes API, so the only way
# to change numberOfReplicas is delete + recreate. kubectl handles this; the
# kubernetes provider cannot update parameters in-place.
resource "null_resource" "patch_longhorn_sc" {
  count = var.manage_storage_class ? 1 : 0

  triggers = {
    replicas = var.storage_class_replicas
  }

  provisioner "local-exec" {
    environment = {
      KUBECONFIG = var.harvester_kubeconfig_path
    }
    # The longhorn SC is owned by the Longhorn operator, which reconciles it from
    # the longhorn-storageclass ConfigMap. Patching the SC directly fails because:
    #   1. parameters are immutable in the Kubernetes API
    #   2. Longhorn recreates the SC faster than a delete+apply can run
    # Correct approach: patch the ConfigMap (source of truth), then delete the SC
    # so Longhorn recreates it from the updated ConfigMap with the new replica count.
    command = <<-EOT
      python3 -c "
import subprocess, json, re, os
env = {**os.environ}
result = subprocess.run(
  ['kubectl','get','configmap','longhorn-storageclass','-n','longhorn-system','-o','json'],
  capture_output=True, text=True, env=env, check=True)
cm = json.loads(result.stdout)
new_yaml = re.sub(
  r'numberOfReplicas: \"[0-9]+\"',
  'numberOfReplicas: \"${var.storage_class_replicas}\"',
  cm['data']['storageclass.yaml'])
patch = json.dumps({'data': {'storageclass.yaml': new_yaml}})
subprocess.run(
  ['kubectl','patch','configmap','longhorn-storageclass','-n','longhorn-system','--type=merge','-p', patch],
  env=env, check=True)
subprocess.run(
  ['kubectl','delete','storageclass','longhorn','--ignore-not-found=true'],
  env=env, check=True)
print('longhorn ConfigMap patched to ${var.storage_class_replicas} replicas; SC deleted for Longhorn reconciliation')
"
    EOT
  }

  depends_on = [kubernetes_storage_class_v1.default]
}
