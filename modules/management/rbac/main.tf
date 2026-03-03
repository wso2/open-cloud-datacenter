terraform {
  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 3.0"
    }
  }
}

data "rancher2_cluster" "harvester_local" {
  name = var.harvester_cluster_name
}

resource "rancher2_project" "team_projects" {
  for_each = var.projects

  name       = each.key
  cluster_id = data.rancher2_cluster.harvester_local.id

  resource_quota {
    project_limit {
      limits_cpu       = each.value.cpu_limit
      limits_memory    = each.value.memory_limit
      requests_storage = each.value.storage_limit
    }
    namespace_default_limit {
      limits_cpu       = each.value.cpu_limit
      limits_memory    = each.value.memory_limit
      requests_storage = each.value.storage_limit
    }
  }
}

resource "rancher2_namespace" "team_namespaces" {
  for_each = var.projects

  name       = "${each.key}-ns"
  project_id = rancher2_project.team_projects[each.key].id
}
