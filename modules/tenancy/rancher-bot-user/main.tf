resource "random_password" "this" {
  count   = var.password == null ? 1 : 0
  length  = 40
  special = false
  keepers = {
    rotation_version = var.password_rotation_version
  }
}

locals {
  password = var.password != null ? var.password : random_password.this[0].result
}

resource "rancher2_user" "this" {
  name                 = var.name
  username             = var.name
  password             = local.password
  enabled              = true
  must_change_password = false
}

# user-base  → login only (no resource access)
# user       → Standard User: login + cloudcredentials + nodedrivers +
#              harvesterconfigs + cluster provisioning (rancher2_cluster_v2)

resource "rancher2_global_role_binding" "this" {
  name           = "${var.name}-global"
  global_role_id = var.can_provision_clusters ? "user" : "user-base"
  user_id        = rancher2_user.this.id
}

resource "rancher2_cluster_role_template_binding" "this" {
  for_each = toset(var.cluster_role_template_ids)

  name             = substr(replace("${var.name}-${replace(each.value, "/[^a-z0-9-]/", "-")}", "/-{2,}/", "-"), 0, 63)
  cluster_id       = var.cluster_id
  role_template_id = each.value
  user_id          = rancher2_user.this.id

  depends_on = [rancher2_global_role_binding.this]
}

resource "rancher2_project_role_template_binding" "this" {
  for_each = toset(var.project_role_template_ids)

  name             = substr(replace("${var.name}-${replace(each.value, "/[^a-z0-9-]/", "-")}", "/-{2,}/", "-"), 0, 63)
  project_id       = var.project_id
  role_template_id = each.value
  user_id          = rancher2_user.this.id

  depends_on = [rancher2_global_role_binding.this]
}

# ── Shared image access ───────────────────────────────────────────────────────
# Read-only binding to the shared images project so the bot can look up
# VirtualMachineImages during VM and cluster provisioning. Enabled by default.
# Change shared_image_project_name if your environment uses a different name.

data "rancher2_project" "shared_images" {
  count      = var.enable_shared_image_access ? 1 : 0
  cluster_id = var.cluster_id
  name       = var.shared_image_project_name
}

resource "rancher2_project_role_template_binding" "shared_image_access" {
  count = var.enable_shared_image_access ? 1 : 0

  name             = "${var.name}-shared-img"
  project_id       = data.rancher2_project.shared_images[0].id
  role_template_id = "read-only"
  user_id          = rancher2_user.this.id

  depends_on = [rancher2_global_role_binding.this]
}

# ── API token ─────────────────────────────────────────────────────────────────
# NOTE: Do NOT set cluster_id — cluster-scoped tokens return 401 with the
# Terraform provider (SUSE KB 000021440).
# Rotate token:    bump token_rotation_version
# Rotate password: bump password_rotation_version (recreates user + all bindings)

resource "rancher2_custom_user_token" "this" {
  username    = rancher2_user.this.username
  password    = local.password
  description = "${var.name} CI/CD pipeline token v${var.token_rotation_version}"
  ttl         = var.token_ttl
  renew       = true

  depends_on = [
    rancher2_global_role_binding.this,
    rancher2_cluster_role_template_binding.this,
    rancher2_project_role_template_binding.this,
    rancher2_project_role_template_binding.shared_image_access,
  ]
}
