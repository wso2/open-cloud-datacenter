terraform {
  required_version = ">= 1.7"
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
EOF
  ) : ""

  effective_chart_values = var.chart_values != "" ? var.chart_values : local.default_chart_values
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
    user_data = (var.user_data != null && var.user_data != "") ? var.user_data : (
      (var.node_password != null || length(var.ssh_authorized_keys) > 0 || var.ntp_server != "" || (local._using_generated_user_data && each.value.storage_network != null)) ? templatefile(
        "${path.module}/templates/node-cloud-init.tpl",
        {
          ssh_user               = var.ssh_user
          node_password          = var.node_password
          ssh_authorized_keys    = var.ssh_authorized_keys
          ntp_server             = var.ntp_server
          enable_storage_netplan = each.value.storage_network != null
        }
      ) : ""
    )

    disk_info = jsonencode({
      disks = [{
        imageName = each.value.image_name
        bootOrder = 1
        size      = each.value.disk_size
      }]
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
  }

  dynamic "rke_config" {
    for_each = var.manage_rke_config ? [1] : []
    content {
      machine_global_config = var.machine_global_config != null ? var.machine_global_config : <<-YAML
        cni: ${var.cni}
        disable-kube-proxy: false
        etcd-expose-metrics: false
      YAML

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
          machine_labels               = machine_pools.value.machine_labels

          dynamic "taints" {
            for_each = machine_pools.value.taints
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

resource "rancher2_cloud_credential" "harvester" {
  count = var.create_cloud_credential ? 1 : 0
  name  = "harvester-${var.cluster_name}-credential"

  harvester_credential_config {
    cluster_id         = data.rancher2_cluster_v2.harvester[0].cluster_v1_id
    cluster_type       = "imported"
    kubeconfig_content = data.rancher2_cluster_v2.harvester[0].kube_config
  }

  lifecycle {
    ignore_changes = [harvester_credential_config[0].kubeconfig_content]
  }
}
