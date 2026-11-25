# Brings up a multi-node RKE2 cluster as Harvester VMs — the substrate Rancher
# later runs on. This module installs RKE2 and nothing else; the Rancher Helm
# release is owned by platform/rancher-install so it lives in Terraform state.
#
# The cluster is assumed to sit behind a TCP load balancer (platform/nginx-lb-vm)
# that fronts 6443 (kube-apiserver) and 9345 (RKE2 supervisor). That LB address
# is the stable join target and the kube API endpoint, so it must be known up
# front — pass it as kube_api_host.
#
# Harvester-level prerequisites (namespace, VM network, StorageClasses, OS
# image) belong to platform/harvester-config; this module only consumes them.

locals {
  # Flatten the pools into one node per static IP.
  _nodes_flat = flatten([
    for pool in var.node_pools : [
      for ip_idx, ip in pool.ips : {
        key           = "${pool.name}-${ip_idx}"
        pool_name     = pool.name
        ip            = ip
        control_plane = pool.control_plane
        etcd          = pool.etcd
        worker        = pool.worker
      }
    ]
  ])

  # A node runs rke2-server when it carries control-plane or etcd roles;
  # everything else joins as an agent.
  _servers = [for n in local._nodes_flat : n if n.control_plane || n.etcd]

  # The first etcd-bearing server bootstraps the datastore (cluster-init); the
  # rest join it. It has to carry etcd — cluster-init initialises the embedded
  # etcd, so electing a control-plane-only node would be incoherent.
  _etcd_nodes = [for n in local._nodes_flat : n if n.etcd]
  _init_key   = length(local._etcd_nodes) > 0 ? local._etcd_nodes[0].key : ""

  node_map = {
    for n in local._nodes_flat : n.key => merge(n, {
      is_server = n.control_plane || n.etcd
      is_init   = n.key == local._init_key
    })
  }

  control_plane_ips = [for n in local._nodes_flat : n.ip if n.control_plane]

  # Secondary nodes dial the load balancer so the join address stays valid no
  # matter which server is currently active.
  join_url = "https://${var.kube_api_host}:9345"

  # Certificate SANs the apiserver must answer for: the LB endpoint every
  # client uses, plus each node's own address.
  tls_sans = distinct(concat(
    [var.kube_api_host],
    [for n in local._nodes_flat : n.ip],
    var.extra_tls_sans,
  ))

  ssh_key_ids = var.create_ssh_key ? [harvester_ssh_key.bootstrap_key[0].id] : var.ssh_key_ids
  # The generated key when we make one, otherwise whatever the caller supplied.
  # ssh_key_ids does not feed this: the provider does not inject VM ssh_keys into
  # cloud-init once user_data_secret_name is set, which this module always sets.
  ssh_public_keys = compact(
    var.create_ssh_key
    ? [tls_private_key.bootstrap_key[0].public_key_openssh]
    : var.ssh_public_keys
  )

  # RKE2 config per node, built as data and serialised — never string-assembled.
  #
  # Server and agent nodes are built in separate comprehensions rather than one
  # conditional. A conditional's branches must share a type, and these configs
  # legitimately differ in shape (cluster-init vs server, tls-san only on
  # servers); Terraform can only reconcile such branches by converting both to
  # a map, which fails here because the values are not all the same type.
  # Each remaining conditional therefore has a {} branch and a single-key
  # object, which unifies cleanly.
  #
  # yamlencode() is applied inside each comprehension so every value of the
  # resulting map is a string — the map itself needs a consistent value type.
  # Shared by both server variants below so the role disables cannot drift
  # between them.
  _server_common = {
    for key, node in local.node_map : key => merge(
      {
        token   = random_password.rke2_token.result
        tls-san = local.tls_sans

        # First entry becomes the cluster's default ingress class. Only valid
        # on server nodes; agents inherit the controllers from the cluster.
        ingress-controller = var.ingress_controller
      },

      # RKE2 runs every server component unless told otherwise, so the pool
      # role flags have to be expressed as explicit disables. Without these a
      # pool declared etcd-only would still serve the API, and one declared
      # control-plane-only would still join the etcd quorum.
      node.control_plane ? {} : {
        disable-apiserver          = true
        disable-controller-manager = true
        disable-scheduler          = true
      },
      node.etcd ? {} : { disable-etcd = true },

      var.extra_server_config,
    ) if node.is_server
  }

  rke2_config_yaml = merge(
    {
      for key, node in local.node_map : key => yamlencode(merge(
        local._server_common[key],

        # Two conditionals, each against {}, NOT one ternary between
        # {cluster-init=true} and {server=...}. Terraform unifies mismatched
        # conditional branches by converting both to map(string), which would
        # render cluster-init as the string "true".
        node.is_init ? { cluster-init = true } : {},
        node.is_init ? {} : { server = local.join_url },
      )) if node.is_server
    },
    {
      for key, node in local.node_map : key => yamlencode(merge(
        {
          token  = random_password.rke2_token.result
          server = local.join_url
        },
        var.extra_agent_config,
      )) if !node.is_server
    },
  )

  # The init node's alternative config: identical but joining rather than
  # initialising. cluster-init is only ever correct for the very first
  # bootstrap. If that node is later rebuilt — a static IP change replaces the
  # VM, and the replacement gets a fresh boot disk — booting it with
  # cluster-init again would start a SECOND empty datastore alongside the
  # surviving servers and split etcd. The node picks between the two at boot;
  # see the bootstrap script.
  rke2_config_join_yaml = {
    for key, node in local.node_map : key => yamlencode(merge(
      local._server_common[key],
      { server = local.join_url },
    )) if node.is_server && node.is_init
  }
}

# ── Input validation ──────────────────────────────────────────────────────────
# Node count, roles and IP uniqueness are enforced by node_pools' own validation
# blocks, which fail the plan. This one is advisory: check blocks emit warnings.
#
# etcd tolerates a single member, but any even count has no quorum advantage
# over count-1 and loses the cluster on a single failure.
# The ingress-controller key is only understood by newer RKE2 releases. Below
# these, the setting is ignored and the cluster silently comes up with the
# bundled ingress-nginx instead of what was asked for. Advisory: the minimums
# come from RKE2's ingress migration guide, and a release train not listed here
# cannot be judged, so this warns rather than failing the plan.
locals {
  _rke2_semver = regex("^v(?P<major>[0-9]+)\\.(?P<minor>[0-9]+)\\.(?P<patch>[0-9]+)", var.rke2_version)

  _rke2_minor = tonumber(local._rke2_semver.minor)
  _rke2_patch = tonumber(local._rke2_semver.patch)

  # minor => minimum patch that understands ingress-controller. Keys are strings
  # because that is how HCL stores map keys, so look up with tostring().
  _ingress_controller_min_patch = {
    "32" = 11
    "33" = 7
    "34" = 3
  }

  _ingress_controller_supported = (
    local._rke2_minor >= 35
    ? true
    : local._rke2_patch >= lookup(
      local._ingress_controller_min_patch, tostring(local._rke2_minor), 1000000
    )
  )
}

check "ingress_controller_supported_by_rke2_version" {
  assert {
    condition     = var.ingress_controller == ["ingress-nginx"] || local._ingress_controller_supported
    error_message = "rke2_version ${var.rke2_version} may not support the ingress-controller setting (needs v1.32.11+, v1.33.7+, v1.34.3+ or v1.35+); the cluster would fall back to the bundled ingress-nginx. Verify with 'rke2 server --help | grep ingress-controller'."
  }
}

check "etcd_member_count_is_odd" {
  assert {
    condition     = length(local._servers) % 2 == 1
    error_message = "An odd number of RKE2 server nodes is required for etcd quorum (1, 3, 5 ...)."
  }
}

# ── Cluster join token ────────────────────────────────────────────────────────
# Generated once and held in state; every node receives it via its own
# cloud-init secret.
resource "random_password" "rke2_token" {
  length  = 64
  special = false
}

# ── SSH key ───────────────────────────────────────────────────────────────────
# Needed to copy the admin kubeconfig off the init node after bootstrap.
resource "tls_private_key" "bootstrap_key" {
  count     = var.create_ssh_key ? 1 : 0
  algorithm = "RSA"
  rsa_bits  = 4096
}

resource "harvester_ssh_key" "bootstrap_key" {
  count      = var.create_ssh_key ? 1 : 0
  name       = "${var.vm_name_prefix}-ssh-key"
  namespace  = var.harvester_namespace
  public_key = tls_private_key.bootstrap_key[0].public_key_openssh
}

# ── Per-node cloud-init ───────────────────────────────────────────────────────
resource "harvester_cloudinit_secret" "node" {
  for_each = local.node_map

  name      = "${var.vm_name_prefix}-cloudinit-${each.key}"
  namespace = var.harvester_namespace

  user_data = templatefile("${path.module}/templates/cloud-init.yaml.tpl", {
    password        = var.vm_password
    ssh_public_keys = local.ssh_public_keys
    rke2_version    = var.rke2_version
    is_server       = each.value.is_server
    kube_api_host   = var.kube_api_host
    node_ip         = each.value.ip
    dns_servers     = var.dns_servers
    rke2_config_b64 = base64encode(local.rke2_config_yaml[each.key])
    rke2_config_join_b64 = try(
      base64encode(local.rke2_config_join_yaml[each.key]), ""
    )
  })

  network_data = templatefile("${path.module}/templates/network-config.yaml.tpl", {
    static_ip     = each.value.ip
    gateway       = var.node_gateway
    subnet_prefix = var.node_subnet_prefix
    dns_servers   = length(var.dns_servers) > 0 ? var.dns_servers : ["8.8.8.8"]
  })
}

# ── Input validation ──────────────────────────────────────────────────────────
# A precondition rather than a variable validation block: the condition spans
# two variables, which inside a validation needs Terraform >= 1.9, and this
# module supports >= 1.5 (see versions.tf). A dedicated resource because every
# other resource here is conditional or keyed per node.
resource "terraform_data" "input_validation" {
  lifecycle {
    precondition {
      condition     = var.create_ssh_key || length(var.ssh_public_keys) > 0
      error_message = "ssh_public_keys is required when create_ssh_key = false, otherwise no key reaches the nodes and only password login works."
    }
  }
}

# ── Node address identity ─────────────────────────────────────────────────────
# A node's static IP reaches the guest only through the cloud-init secret, and
# the VM references that secret by name — a name keyed on the pool and index,
# not on the address. So changing an IP rewrites the secret in place and leaves
# every VM argument identical: no update, so restart_after_update never fires.
# A restart would not be enough anyway, since cloud-init renders network
# configuration on an instance's first boot only. The guest would keep the old
# address while node_ips and the other outputs reported the new one, and
# anything built from those outputs would point at an IP that node does not hold.
#
# Carrying the address as its own object gives the VM something to key
# replacement off. Adding this to an already-applied cluster is a no-op: the
# object is created, and a creation does not trigger replace_triggered_by.
resource "terraform_data" "node_address" {
  for_each = local.node_map

  input = each.value.ip
}

# ── Node VMs ──────────────────────────────────────────────────────────────────
resource "harvester_virtualmachine" "node" {
  for_each = local.node_map

  name                 = "${var.vm_name_prefix}-${each.key}"
  namespace            = var.harvester_namespace
  restart_after_update = true

  cpu    = var.vm_cpu
  memory = var.vm_memory

  run_strategy = "RerunOnFailure"
  machine_type = "q35"

  ssh_keys = local.ssh_key_ids

  network_interface {
    name         = var.network_interface_name
    type         = "bridge"
    network_name = var.network_name
  }

  disk {
    name       = var.vm_disk_name
    type       = "disk"
    size       = var.vm_disk_size
    bus        = "virtio"
    boot_order = 1

    image       = var.image_id
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
    user_data_secret_name    = harvester_cloudinit_secret.node[each.key].name
    network_data_secret_name = harvester_cloudinit_secret.node[each.key].name
  }

  lifecycle {
    # Rebuild the node when its static IP changes; see terraform_data
    # .node_address above for why an in-place update cannot deliver it.
    replace_triggered_by = [terraform_data.node_address[each.key]]
  }
}
