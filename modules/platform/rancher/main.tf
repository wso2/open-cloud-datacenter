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

  _kc_content = try(file(var.harvester_kubeconfig_path), "")
  _kc = (
    var.node_count > 1 && length(var.node_pools) == 0 && local._kc_content != ""
    ? yamldecode(local._kc_content)
    : null
  )
  harvester_api_url  = try(local._kc.clusters[0].cluster.server, "")
  harvester_ca_b64   = try(local._kc.clusters[0].cluster["certificate-authority-data"], "")
  harvester_cert_b64 = try(local._kc.users[0].user["client-certificate-data"], "")
  harvester_key_b64  = try(local._kc.users[0].user["client-key-data"], "")

  # ── Static node pool helpers ─────────────────────────────────────────────────
  _static_nodes_flat = flatten([
    for pool_idx, pool in var.node_pools : [
      for ip_idx, ip in pool.ips : {
        key           = "${pool.name}-${ip_idx}"
        pool_name     = pool.name
        pool_idx      = pool_idx
        ip            = ip
        ip_idx        = ip_idx
        control_plane = pool.control_plane
        etcd          = pool.etcd
        worker        = pool.worker
      }
    ]
  ])

  _cp_nodes     = [for n in local._static_nodes_flat : n if n.control_plane]
  _first_cp_key = length(local._cp_nodes) > 0 ? local._cp_nodes[0].key : ""
  _first_cp_ip  = length(local._cp_nodes) > 0 ? local._cp_nodes[0].ip : ""

  static_node_map = {
    for n in local._static_nodes_flat : n.key => merge(n, {
      is_init = n.key == local._first_cp_key
    })
  }

  use_static_nodes = length(var.node_pools) > 0
  cp_node_count    = length(local._cp_nodes)
  total_node_count = local.use_static_nodes ? length(local._static_nodes_flat) : var.node_count

  # Join address for secondary nodes: nginx LB or first CP node IP
  join_address = var.use_nginx_lb ? var.nginx_lb_ip : local._first_cp_ip

  # Effective LB IP for tls-san and rancher_lb_ip output
  effective_lb_ip = (
    var.use_nginx_lb ? var.nginx_lb_ip :
    var.create_lb ? var.ippool_start :
    var.static_rancher_ip
  )

  bridge_network_name = (
    var.network_type == "bridge" && var.create_bridge_network
    ? "${var.harvester_namespace}/${var.network_name}"
    : var.network_name
  )

  ssh_key_ids = var.create_ssh_key ? [harvester_ssh_key.bootstrap_key[0].id] : var.ssh_key_ids

  # Shared cloud-init template variables
  _common_ci_vars = {
    password           = var.vm_password
    cluster_dns        = var.rancher_hostname
    rancher_password   = var.bootstrap_password
    ssh_public_key     = var.create_ssh_key ? tls_private_key.bootstrap_key[0].public_key_openssh : ""
    rke2_version       = var.rke2_version
    rancher_version    = var.rancher_version
    tls_source         = var.use_nginx_lb ? "rancher" : var.tls_source
    tls_cert_b64       = (!var.use_nginx_lb && var.tls_source == "secret") ? base64encode(var.tls_cert) : ""
    tls_key_b64        = (!var.use_nginx_lb && var.tls_source == "secret") ? base64encode(var.tls_key) : ""
    rke2_cluster_token = random_password.rke2_token.result
    dns_servers        = var.dns_servers
    lb_ip              = local.effective_lb_ip
    use_nginx_lb       = var.use_nginx_lb
    use_rancher_prime  = var.use_rancher_prime
  }
}

check "image_source_required" {
  assert {
    condition     = var.image_url != "" || var.ubuntu_image_id != ""
    error_message = "Either image_url or ubuntu_image_id must be set."
  }
}

check "node_gateway_required_for_static" {
  assert {
    condition     = !local.use_static_nodes || var.node_gateway != ""
    error_message = "node_gateway is required when node_pools is set."
  }
}

resource "random_password" "rke2_token" {
  length  = 64
  special = false
}

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

check "ssh_key_required_for_cloudinit" {
  assert {
    condition     = !var.create_cloudinit_secret || var.create_ssh_key
    error_message = "create_ssh_key must be true when create_cloudinit_secret is true."
  }
}

check "existing_cloudinit_secret_name_required" {
  assert {
    condition     = var.create_cloudinit_secret || var.existing_cloudinit_secret_name != ""
    error_message = "existing_cloudinit_secret_name is required when create_cloudinit_secret = false."
  }
}

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

# ── Legacy count-based cloud-init + VMs (when node_pools is empty) ────────────
resource "harvester_cloudinit_secret" "cloudinit" {
  count      = var.create_cloudinit_secret && !local.use_static_nodes ? var.node_count : 0
  name       = var.node_count > 1 ? "${var.vm_name}-cloudinit-${count.index}" : "${var.vm_name}-cloudinit"
  namespace  = var.harvester_namespace
  depends_on = [kubernetes_namespace.harvester_ns]

  user_data = templatefile("${path.module}/templates/cloud-init.yaml.tpl", merge(local._common_ci_vars, {
    is_init_node     = count.index == 0
    is_control_plane = true
    join_ip          = ""
    total_node_count = var.node_count
    cp_node_count    = var.node_count
  }))
}

resource "harvester_virtualmachine" "rancher_server" {
  count                = !local.use_static_nodes ? var.node_count : 0
  name                 = var.node_count > 1 ? "${var.vm_name}-${count.index}" : var.vm_name
  namespace            = var.harvester_namespace
  restart_after_update = true

  depends_on = [
    null_resource.storage_network,
    harvester_network.bridge,
  ]

  cpu    = var.vm_cpu
  memory = var.vm_memory

  run_strategy = "RerunOnFailure"
  machine_type = "q35"

  ssh_keys = local.ssh_key_ids

  dynamic "network_interface" {
    for_each = var.network_type == "masquerade" ? [1] : []
    content {
      name = var.network_interface_name
      type = "masquerade"
    }
  }

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

# ── Static node pool cloud-init + VMs (when node_pools is set) ───────────────
resource "harvester_cloudinit_secret" "cloudinit_static" {
  for_each = local.use_static_nodes && var.create_cloudinit_secret ? local.static_node_map : {}

  name       = "${var.vm_name}-cloudinit-${each.key}"
  namespace  = var.harvester_namespace
  depends_on = [kubernetes_namespace.harvester_ns]

  user_data = templatefile("${path.module}/templates/cloud-init.yaml.tpl", merge(local._common_ci_vars, {
    is_init_node     = each.value.is_init
    is_control_plane = each.value.control_plane
    join_ip          = local.join_address
    total_node_count = local.total_node_count
    cp_node_count    = local.cp_node_count
  }))

  network_data = templatefile("${path.module}/templates/network-config.yaml.tpl", {
    static_ip     = each.value.ip
    gateway       = var.node_gateway
    subnet_prefix = var.node_subnet_prefix
    dns_servers   = length(var.dns_servers) > 0 ? var.dns_servers : ["8.8.8.8"]
  })
}

resource "harvester_virtualmachine" "rancher_server_static" {
  for_each = local.use_static_nodes ? local.static_node_map : {}

  name                 = "${var.vm_name}-${each.key}"
  namespace            = var.harvester_namespace
  restart_after_update = true

  depends_on = [
    null_resource.storage_network,
    harvester_network.bridge,
  ]

  cpu    = var.vm_cpu
  memory = var.vm_memory

  run_strategy = "RerunOnFailure"
  machine_type = "q35"

  ssh_keys = local.ssh_key_ids

  dynamic "network_interface" {
    for_each = var.network_type == "masquerade" ? [1] : []
    content {
      name = var.network_interface_name
      type = "masquerade"
    }
  }

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

  dynamic "input" {
    for_each = var.enable_usb_tablet ? [1] : []
    content {
      name = "tablet"
      type = "tablet"
      bus  = "usb"
    }
  }

  cloudinit {
    user_data_secret_name    = harvester_cloudinit_secret.cloudinit_static[each.key].name
    network_data_secret_name = harvester_cloudinit_secret.cloudinit_static[each.key].name
  }
}

# ── Harvester LoadBalancer + IP Pool (for lk-dev and similar environments) ────
resource "harvester_loadbalancer" "rancher_lb" {
  count     = var.create_lb ? 1 : 0
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
  count = var.create_lb ? 1 : 0
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
resource "kubernetes_storage_class_v1" "default" {
  count      = var.manage_storage_class ? 1 : 0
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

resource "null_resource" "patch_longhorn_sc" {
  count = var.manage_storage_class ? 1 : 0

  triggers = {
    replicas = var.storage_class_replicas
  }

  provisioner "local-exec" {
    environment = {
      KUBECONFIG = var.harvester_kubeconfig_path
    }
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
