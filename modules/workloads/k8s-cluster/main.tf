terraform {
  required_version = ">= 1.7"
  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 13.1"
    }
  }
}

locals {
  pools_by_name = { for p in var.machine_pools : p.name => p }
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
    user_data            = var.user_data

    disk_info = jsonencode({
      disks = [{
        imageName = each.value.image_name
        bootOrder = 1
        size      = each.value.disk_size
      }]
    })

    network_info = jsonencode({
      interfaces = [
        for net in each.value.networks : {
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
  cloud_credential_secret_name = var.cloud_credential_id

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
      rke_config[0].upgrade_strategy[0].control_plane_drain_options,
      rke_config[0].upgrade_strategy[0].worker_drain_options,
    ]
    precondition {
      condition     = !var.manage_rke_config || length(var.machine_pools) > 0
      error_message = "machine_pools must contain at least one entry when manage_rke_config is true."
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
        for_each = var.cloud_provider_config_secret != "" ? [1] : []
        content {
          # config is TypeString (YAML) in rancher2 v13.
          config = yamlencode({
            "cloud-provider-config"   = "secret://fleet-default:${var.cloud_provider_config_secret}"
            "cloud-provider-name"     = "harvester"
            "protect-kernel-defaults" = false
          })
        }
      }

      dynamic "machine_pools" {
        for_each = local.pools_by_name
        content {
          name                         = machine_pools.key
          cloud_credential_secret_name = var.cloud_credential_id
          control_plane_role           = machine_pools.value.control_plane
          etcd_role                    = machine_pools.value.etcd
          worker_role                  = machine_pools.value.worker
          quantity                     = machine_pools.value.quantity
          drain_before_delete          = true
          labels                       = machine_pools.value.labels
          annotations                  = machine_pools.value.annotations

          dynamic "taints" {
            for_each = machine_pools.value.taints
            content {
              key    = taints.value.key
              value  = taints.value.value
              effect = taints.value.effect
            }
          }

          machine_config {
            kind = contains(keys(var.machine_config_overrides), machine_pools.key) ? var.machine_config_overrides[machine_pools.key].kind : rancher2_machine_config_v2.pool[machine_pools.key].kind
            name = contains(keys(var.machine_config_overrides), machine_pools.key) ? var.machine_config_overrides[machine_pools.key].name : rancher2_machine_config_v2.pool[machine_pools.key].name
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
              hostname                = configs.value.hostname
              auth_config_secret_name = configs.value.auth_config_secret_name
              insecure                = configs.value.insecure
              tls_secret_name         = configs.value.tls_secret_name
              ca_bundle               = configs.value.ca_bundle
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
