terraform {
  # >= 1.16 for terraform_data's store.sensitive_output block, used to safely gate
  # renewal of the Harvester cloud credential's kubeconfig token (see below).
  required_version = ">= 1.16"
  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 13.1"
    }
    http = {
      source  = "hashicorp/http"
      version = "~> 3.6"
    }
  }
}

locals {
  pools_by_name = { for p in var.machine_pools : p.name => p }

  # user_data (non-empty string) takes full precedence over template generation.
  # When not set, cloud-init is generated per-pool so enable_storage_netplan
  # reflects only that pool's storage_network field (not a cluster-wide flag).
  _using_generated_user_data = var.user_data == null || var.user_data == ""

  # Full cloud-init override per pool, resolved as: pool user_data → module
  # user_data → "" (generated template). Empty/whitespace values fall through.
  pool_user_data_override = {
    for p in var.machine_pools : p.name => (
      p.user_data != null && trimspace(p.user_data) != "" ? p.user_data : (
        var.user_data != null && trimspace(var.user_data) != "" ? var.user_data : ""
      )
    )
  }

  harvester_kubeconfig = (var.create_cloud_credential && length(data.http.harvester_cloud_provider_kubeconfig) > 0) ? (
    data.http.harvester_cloud_provider_kubeconfig[0].status_code == 200 ? jsondecode(data.http.harvester_cloud_provider_kubeconfig[0].response_body) : null
  ) : null

  machine_selector_config = var.create_cloud_credential ? jsonencode({
    "cloud-provider-config"   = local.harvester_kubeconfig
    "cloud-provider-name"     = "harvester"
    "protect-kernel-defaults" = false
    }) : (
    var.cloud_provider_config_secret != "" ? jsonencode({
      "cloud-provider-config"   = "secret://fleet-default:${var.cloud_provider_config_secret}"
      "cloud-provider-name"     = "harvester"
      "protect-kernel-defaults" = false
      }) : jsonencode({
      "cloud-provider-name"     = "harvester"
      "protect-kernel-defaults" = false
    })
  )

  # Map of registry configs that carry inline credentials (username set).
  # Keyed by hostname so each host gets one secret regardless of insecure/tls settings.
  registry_auth_configs = {
    for c in try(var.registries.configs, []) : c.hostname => c
    if c.username != null
  }

  default_chart_values = (var.enable_harvester_cloud_provider && var.create_cloud_credential) ? (<<-EOF
harvester-cloud-provider:
  clusterName: ${var.cluster_name}
  cloudConfigPath: /var/lib/rancher/rke2/etc/config-files/cloud-provider-config
  global:
    cattle:
      clusterName: ${var.cluster_name}
EOF
  ) : ""

  _base_chart_values = var.chart_values != "" ? var.chart_values : local.default_chart_values

  # gVisor pools are tainted, which would keep the harvester-csi-driver node
  # plugin DaemonSet off them (PVCs then hang in ContainerCreating). Helm
  # replaces lists, so use the broad NoSchedule toleration Canal uses rather
  # than copying the chart defaults. A harvester-csi-driver key supplied in
  # var.chart_values wins over this.
  _gvisor_chart_values = (local.gvisor_enabled && var.gvisor.harvester_csi_tolerations && var.enable_harvester_cloud_provider) ? {
    "harvester-csi-driver" = {
      tolerations = [
        { operator = "Exists", effect = "NoSchedule" },
        { key = "node-role.kubernetes.io/etcd", operator = "Exists", effect = "NoExecute" },
      ]
    }
  } : {}

  effective_chart_values = length(local._gvisor_chart_values) == 0 ? local._base_chart_values : yamlencode(merge(
    local._gvisor_chart_values,
    local._base_chart_values != "" ? yamldecode(local._base_chart_values) : {}
  ))

  # Generated machine_global_config. ingress-controller is omitted when using
  # the RKE2 default (ingress-nginx) to keep the config minimal.
  _ingress_line = var.ingress_controller != "ingress-nginx" ? "\ningress-controller: ${var.ingress_controller}" : ""

  default_machine_global_config = <<-YAML
    cni: ${var.cni}
    disable-kube-proxy: false
    etcd-expose-metrics: false${local._ingress_line}
  YAML

  # ── gVisor ──────────────────────────────────────────────────────────────────
  gvisor_pools   = { for p in var.machine_pools : p.name => p if p.gvisor }
  gvisor_enabled = length(local.gvisor_pools) > 0

  # Inputs to templates/node-cloud-init.tpl (per-pool release added there).
  _gvisor_template = {
    apt_repo_url    = var.gvisor.apt_repo_url
    apt_key_url     = var.gvisor.apt_key_url
    runtime_handler = var.gvisor.runtime_handler
    containerd_tmpl = var.gvisor.containerd_config_version == 3 ? "config-v3.toml.tmpl" : "config.toml.tmpl"
    cri_plugin      = var.gvisor.containerd_config_version == 3 ? "'io.containerd.cri.v1.runtime'" : "\"io.containerd.grpc.v1.cri\""
    runsc_config = merge({
      platform         = var.gvisor.platform
      "systemd-cgroup" = tostring(var.gvisor.systemd_cgroup)
    }, var.gvisor.runsc_config)
  }

  _gvisor_label = { (var.gvisor.node_label_key) = var.gvisor.node_label_value }
  _gvisor_taint = var.gvisor.taint ? [{
    key    = var.gvisor.node_label_key
    value  = var.gvisor.node_label_value
    effect = var.gvisor.taint_effect
  }] : []

  # Kubernetes merges scheduling into every pod that uses this RuntimeClass,
  # so workloads only need runtimeClassName.
  gvisor_runtime_class_manifest = (local.gvisor_enabled && var.gvisor.create_runtime_class) ? yamlencode(merge(
    {
      apiVersion = "node.k8s.io/v1"
      kind       = "RuntimeClass"
      metadata   = { name = var.gvisor.runtime_class_name }
      handler    = var.gvisor.runtime_handler
      scheduling = merge(
        { nodeSelector = local._gvisor_label },
        length(local._gvisor_taint) > 0 ? {
          tolerations = [for t in local._gvisor_taint : merge(t, { operator = "Equal" })]
        } : {}
      )
    },
    var.gvisor.runtime_class_overhead != null ? { overhead = { podFixed = var.gvisor.runtime_class_overhead } } : {}
  )) : ""

  effective_additional_manifest = join("\n---\n", compact([
    trimspace(var.additional_manifest),
    trimspace(local.gvisor_runtime_class_manifest),
  ]))
}

# Registry auth secrets — created only for configs that supply username/password.
# Stored in fleet-default (same namespace as harvester cloud credential secrets)
# so the RKE2 provisioner can read them during cluster bring-up.
resource "rancher2_secret_v2" "registry_auth" {
  for_each = var.manage_rke_config ? local.registry_auth_configs : {}

  cluster_id = "local"
  # Sanitize the secret name to a valid DNS subdomain:
  #   1. lowercase everything
  #   2. replace any char outside [a-z0-9-] with a hyphen (covers dots, colons, slashes)
  #   3. collapse consecutive hyphens into one
  #   4. append a 6-char hash of the hostname for collision safety
  name = replace(
    replace(
      lower("${var.cluster_name}-registry-${each.key}-${substr(md5(each.key), 0, 6)}"),
      "/[^a-z0-9-]/", "-"
    ),
    "/-{2,}/", "-"
  )
  namespace = "fleet-default"
  type      = "kubernetes.io/basic-auth"

  # rancher2_secret_v2 handles base64 encoding internally — pass raw values.
  data = {
    username = each.value.username
    password = each.value.password
  }
}

# One machine config per pool.
# rancher2_machine_config_v2 does not support import — set manage_rke_config = false
# for brownfield clusters where machine configs already exist.
#
# When changing machine config specs (cpu, memory, disk, image, network) on a
# pool NOT covered by machine_config_overrides, the provider recreates this
# resource with a new random name. The cluster resource cannot reference an
# unknown name in a single plan, so a two-phase apply is required:
#   Phase 1: terraform apply -target=module.<name>.rancher2_machine_config_v2.pool
#   Phase 2: terraform apply -target=module.<name>
resource "rancher2_machine_config_v2" "pool" {
  for_each = var.manage_rke_config ? { for k, v in local.pools_by_name : k => v if !contains(keys(var.machine_config_overrides), k) } : {}

  generate_name = "${var.cluster_name}-${each.key}"

  harvester_config {
    vm_namespace         = each.value.vm_namespace
    cpu_count            = each.value.cpu_count
    memory_size          = each.value.memory_size
    reserved_memory_size = "-1"
    ssh_user             = var.ssh_user
    user_data = local.pool_user_data_override[each.key] != "" ? local.pool_user_data_override[each.key] : (
      (var.node_password != null || length(var.ssh_authorized_keys) > 0 || var.ntp_server != "" || (local._using_generated_user_data && each.value.storage_network != null) || each.value.gvisor) ? templatefile(
        "${path.module}/templates/node-cloud-init.tpl",
        {
          ssh_user               = var.ssh_user
          node_password          = var.node_password
          ssh_authorized_keys    = var.ssh_authorized_keys
          ntp_server             = var.ntp_server
          enable_storage_netplan = each.value.storage_network != null
          enable_gvisor          = each.value.gvisor
          gvisor = merge(local._gvisor_template, {
            release = coalesce(each.value.gvisor_release, var.gvisor.release)
          })
        }
      ) : ""
    )

    disk_info = jsonencode({
      disks = [merge(
        {
          imageName = each.value.image_name
          bootOrder = 1
          size      = each.value.disk_size
        },
        # storageClassName is only emitted when the pool sets it. Omitting
        # the key lets Harvester fall back to its default StorageClass —
        # the original module behaviour.
        try(each.value.storage_class_name, null) != null && trimspace(try(each.value.storage_class_name, "")) != "" ? {
          storageClassName = each.value.storage_class_name
        } : {}
      )]
    })

    network_info = jsonencode({
      interfaces = [
        for net in compact(concat(
          each.value.vm_network != null ? [each.value.vm_network] : [],
          each.value.networks,
          each.value.storage_network != null ? [each.value.storage_network] : []
          )) : {
          networkName = net
          macAddress  = ""
        }
      ]
    })
  }
}

resource "rancher2_cluster_v2" "this" {
  name                         = var.cluster_name
  kubernetes_version           = var.kubernetes_version
  cloud_credential_secret_name = var.create_cloud_credential ? rancher2_cloud_credential.harvester[0].id : var.cloud_credential_id

  # rke_config is applied on CREATE (when manage_rke_config = true) but is
  # intentionally ignored on subsequent applies for both managed and brownfield
  # clusters. Reasons:
  #   1. Brownfield (manage_rke_config = false): no rke_config block is emitted;
  #      without ignore_changes TF would try to remove the server-side config.
  #   2. Managed (manage_rke_config = true): Rancher owns pool-member lifecycle
  #      after provisioning, so re-applying rke_config fields triggers rolling
  #      upgrades unnecessarily. Use Rancher UI/API for post-create pool changes.
  # Note: Terraform lifecycle blocks do not support conditional expressions, so
  # ignore_changes cannot be scoped to manage_rke_config = false only.
  lifecycle {
    ignore_changes = [
      cloud_credential_secret_name,
      cluster_agent_deployment_customization,
      fleet_agent_deployment_customization,
      rke_config[0].chart_values,
      rke_config[0].machine_selector_config,
      rke_config[0].upgrade_strategy[0].control_plane_drain_options,
      rke_config[0].upgrade_strategy[0].worker_drain_options,
      # On-demand snapshots taken via the Rancher UI/API ("Snapshot Now") set
      # etcd_snapshot_create.generation on the cluster object. It is not managed
      # here, so without this every subsequent plan shows spurious drift trying to
      # null it. Ignore it — scheduled snapshots stay Terraform-managed via etcd_s3.
      rke_config[0].etcd_snapshot_create,
    ]
    precondition {
      condition     = !var.manage_rke_config || length(var.machine_pools) > 0
      error_message = "machine_pools must contain at least one entry when manage_rke_config is true."
    }
    precondition {
      condition = length(setsubtract(
        toset(keys(var.machine_config_overrides)),
        toset([for p in var.machine_pools : p.name])
      )) == 0
      error_message = "All machine_config_overrides keys must match a pool name in machine_pools."
    }
    precondition {
      condition     = var.cloud_provider_config_secret == "" || var.enable_harvester_cloud_provider
      error_message = "cloud_provider_config_secret is set but enable_harvester_cloud_provider is false. Set enable_harvester_cloud_provider = true or clear cloud_provider_config_secret."
    }
    precondition {
      condition     = alltrue([for name, p in local.gvisor_pools : local.pool_user_data_override[name] == ""])
      error_message = "gVisor pools need the module-generated cloud-init. A pool with gvisor = true must not set its own user_data, and var.user_data must be empty (gVisor pools would otherwise fall back to it)."
    }
  }

  dynamic "rke_config" {
    for_each = var.manage_rke_config ? [1] : []
    content {
      machine_global_config = coalesce(var.machine_global_config, local.default_machine_global_config)

      dynamic "machine_selector_config" {
        for_each = var.enable_harvester_cloud_provider ? [1] : []
        content {
          # config is TypeString (YAML) in rancher2 v13.
          # cloud-provider-config is only set on brownfield clusters where the
          # harvesterconfig* secret already exists. For new clusters Rancher's
          # provisioner creates the secret automatically when cloud-provider-name
          # is harvester — no explicit cloud-provider-config key needed on create.
          #
          config = local.machine_selector_config
        }
      }

      dynamic "machine_pools" {
        for_each = var.machine_pools
        content {
          name                         = machine_pools.value.name
          cloud_credential_secret_name = var.create_cloud_credential ? rancher2_cloud_credential.harvester[0].id : var.cloud_credential_id
          control_plane_role           = machine_pools.value.control_plane
          etcd_role                    = machine_pools.value.etcd
          worker_role                  = machine_pools.value.worker
          quantity                     = machine_pools.value.quantity
          drain_before_delete          = true
          machine_labels               = machine_pools.value.gvisor ? merge(machine_pools.value.machine_labels, local._gvisor_label) : machine_pools.value.machine_labels

          dynamic "taints" {
            # gVisor pools get the sandbox taint unless the caller already declares that key.
            for_each = concat(machine_pools.value.taints, machine_pools.value.gvisor && !contains([for t in machine_pools.value.taints : t.key], var.gvisor.node_label_key) ? local._gvisor_taint : [])
            content {
              key    = taints.value.key
              value  = taints.value.value
              effect = taints.value.effect
            }
          }

          machine_config {
            kind = contains(keys(var.machine_config_overrides), machine_pools.value.name) ? var.machine_config_overrides[machine_pools.value.name].kind : rancher2_machine_config_v2.pool[machine_pools.value.name].kind
            name = contains(keys(var.machine_config_overrides), machine_pools.value.name) ? var.machine_config_overrides[machine_pools.value.name].name : rancher2_machine_config_v2.pool[machine_pools.value.name].name
          }
        }
      }

      dynamic "etcd" {
        for_each = var.etcd_s3 != null ? [var.etcd_s3] : []
        content {
          snapshot_retention     = etcd.value.snapshot_retention
          snapshot_schedule_cron = etcd.value.snapshot_schedule
          s3_config {
            bucket                = etcd.value.bucket
            cloud_credential_name = etcd.value.cloud_credential_id
            endpoint              = "s3.${etcd.value.region}.amazonaws.com"
            folder                = etcd.value.folder
            region                = etcd.value.region
          }
        }
      }

      dynamic "registries" {
        for_each = var.registries != null ? [var.registries] : []
        content {
          dynamic "configs" {
            for_each = registries.value.configs
            content {
              hostname        = configs.value.hostname
              insecure        = configs.value.insecure
              ca_bundle       = configs.value.ca_bundle
              tls_secret_name = configs.value.tls_secret_name
              auth_config_secret_name = configs.value.username != null ? (
                rancher2_secret_v2.registry_auth[configs.value.hostname].name
              ) : configs.value.auth_config_secret_name
            }
          }
          dynamic "mirrors" {
            for_each = registries.value.mirrors
            content {
              hostname  = mirrors.value.hostname
              endpoints = mirrors.value.endpoints
            }
          }
        }
      }

      upgrade_strategy {
        control_plane_concurrency = "1"
        worker_concurrency        = "1"
      }

      chart_values = local.effective_chart_values != "" ? local.effective_chart_values : null

      additional_manifest = local.effective_additional_manifest != "" ? local.effective_additional_manifest : null
    }
  }
}

# ── State migrations from v0.1.0 ──────────────────────────────────────────────
# The cluster resource was renamed from tenant_cluster → this.
moved {
  from = rancher2_cluster_v2.tenant_cluster
  to   = rancher2_cluster_v2.this
}

# The machine config was a single resource (harvester_nodes) in v0.1.0.
# In v0.2.0 it is a per-pool map (pool[*]) created only when manage_rke_config = true.
#
# Brownfield callers (manage_rke_config = false): the old resource is removed
# from state without destroying the underlying object in Harvester/Rancher.
#
# Greenfield callers upgrading to v0.2.0 (manage_rke_config = true): run the
# following state mv before applying to avoid recreating the machine config:
#   terraform state mv \
#     'module.<name>.rancher2_machine_config_v2.harvester_nodes' \
#     'module.<name>.rancher2_machine_config_v2.pool["<pool-name>"]'
removed {
  from = rancher2_machine_config_v2.harvester_nodes
  lifecycle {
    destroy = false
  }
}

# ── Dynamic Harvester Cloud Credential Provisioning ───────────────────────────

data "rancher2_cluster" "harvester" {
  count = (var.create_cloud_credential && var.harvester_cluster_name != "") ? 1 : 0
  name  = var.harvester_cluster_name
}

data "rancher2_cluster_v2" "harvester" {
  count = var.create_cloud_credential ? 1 : 0
  name  = var.harvester_cluster_name != "" ? data.rancher2_cluster.harvester[0].id : var.harvester_cluster_id
}

data "http" "harvester_cloud_provider_kubeconfig" {
  count = var.create_cloud_credential ? 1 : 0

  url      = "${var.rancher_api_url}/k8s/clusters/${data.rancher2_cluster_v2.harvester[0].cluster_v1_id}/v1/harvester/kubeconfig"
  method   = "POST"
  insecure = true

  request_headers = {
    "Content-Type"  = "application/json"
    "Authorization" = "Basic ${base64encode(var.rancher_api_token)}"
  }

  request_body = jsonencode({
    clusterRoleName    = "harvesterhci.io:cloudprovider"
    namespace          = coalesce(var.harvester_vm_namespace, var.cluster_name)
    serviceAccountName = coalesce(var.harvester_service_account_name, var.harvester_cluster_name, data.rancher2_cluster_v2.harvester[0].name)
  })

  lifecycle {
    postcondition {
      condition     = self.status_code == 200
      error_message = "Failed to retrieve Harvester cloud provider kubeconfig from Rancher. Status code: ${self.status_code}. Response: ${self.response_body}"
    }
  }
}

# Rancher mints a new token in data.rancher2_cluster_v2.harvester[0].kube_config on
# every refresh, so referencing it directly would rotate the credential on every apply.
# terraform_data's store.sensitive_output (Terraform >= 1.16) gates that: the stored
# value only recomputes when store.version changes, and — unlike the legacy input/output
# attributes — sensitive_output correctly preserves the sensitivity mark, so the token
# never renders in plain text in plan/apply/state output. Bump
# harvester_credential_rotation to pull a fresh kubeconfig and update the existing
# credential's kubeconfig_content in place (same secret ID, no cluster/machine_pool
# cascade) — the same dynamic-fetch mechanism used on initial creation.
resource "terraform_data" "harvester_kubeconfig_rotation" {
  count = var.create_cloud_credential ? 1 : 0

  store {
    input     = data.rancher2_cluster_v2.harvester[0].kube_config
    sensitive = true
    version   = var.harvester_credential_rotation
  }
}

resource "rancher2_cloud_credential" "harvester" {
  count = var.create_cloud_credential ? 1 : 0
  name  = "harvester-${var.cluster_name}-credential"

  # kubeconfig_content intentionally uses the full imported-cluster kube_config (not
  # the narrower harvesterhci.io:cloudprovider ServiceAccount kubeconfig fetched below
  # for machine_selector_config). Rancher's Harvester node driver needs this credential
  # to provision/manage VMs and disks, which the CCM-scoped ServiceAccount cannot do.
  harvester_credential_config {
    cluster_id         = data.rancher2_cluster_v2.harvester[0].cluster_v1_id
    cluster_type       = "imported"
    kubeconfig_content = terraform_data.harvester_kubeconfig_rotation[0].store.sensitive_output
  }
}

# ── Cluster membership ────────────────────────────────────────────────────────

locals {
  # Stable map key encodes which identity field is set so that switching identity
  # type (e.g. email → user_id) produces a different key and triggers destroy+create
  # rather than a silent in-place update with stale state.
  _members = {
    for m in var.cluster_members :
    "${coalesce(
      m.email != null ? "email:${m.email}" : null,
      m.name != null ? "name:${m.type}:${m.name}" : null,
      m.user_id != null ? "user_id:${m.user_id}" : null,
      m.user_principal_id != null ? "user_principal_id:${m.user_principal_id}" : null,
      m.group_principal_id != null ? "group_principal_id:${m.group_principal_id}" : null,
    )}:${m.role}" => m
  }
}

# Resolves email or name → full Rancher principal ID.
# email entries always use type = "user"; name entries use the configured type.
# Not created for user_id / user_principal_id / group_principal_id entries.
data "rancher2_principal" "member" {
  for_each = { for k, m in local._members : k => m if m.email != null || m.name != null }
  name     = each.value.email != null ? each.value.email : each.value.name
  type     = each.value.email != null ? "user" : each.value.type
}

resource "rancher2_cluster_role_template_binding" "member" {
  for_each = local._members

  name = substr(
    replace(
      replace(lower("${var.cluster_name}-${replace(each.key, ":", "-")}"), "/[^a-z0-9-]/", "-"),
      "/-{2,}/", "-"
    ),
    0, 63
  )

  cluster_id       = rancher2_cluster_v2.this.cluster_v1_id
  role_template_id = each.value.role

  # user_principal_id covers:
  #   - email lookup (data source result, always type = "user")
  #   - name-based user lookup (data source result)
  #   - explicit user_principal_id (e.g. "local://u-427g5iiyyg")
  #   - bare user_id, wrapped to "local://<id>"
  user_principal_id = (
    each.value.user_principal_id != null ? each.value.user_principal_id :
    each.value.user_id != null ? "local://${each.value.user_id}" :
    each.value.email != null ? data.rancher2_principal.member[each.key].id :
    each.value.name != null && each.value.type == "user" ? data.rancher2_principal.member[each.key].id :
    null
  )

  # group_principal_id covers:
  #   - explicit group_principal_id (e.g. "genericoidc_group://my-group")
  #   - name-based group lookup (data source result)
  group_principal_id = (
    each.value.group_principal_id != null ? each.value.group_principal_id :
    each.value.name != null && each.value.type == "group" ? data.rancher2_principal.member[each.key].id :
    null
  )

  depends_on = [rancher2_cluster_v2.this]
}
