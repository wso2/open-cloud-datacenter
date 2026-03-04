terraform {
  required_providers {
    harvester = {
      source  = "harvester/harvester"
      version = "~> 1.7"
    }
  }
}

resource "harvester_network" "vlan" {
  for_each = var.vlans

  name      = each.key
  namespace = var.harvester_namespace
  vlan_id   = each.value.vlan_id

  cluster_network_name = var.cluster_network_name

  route {
    cidr    = each.value.cidr
    gateway = each.value.gateway
  }
}
