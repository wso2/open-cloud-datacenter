locals {
  # Generate cloud-init from first-class variables when user_data is not provided.
  # user_data takes full precedence — when set, generation is skipped entirely.
  _generated_user_data = (var.password != null || length(var.ssh_authorized_keys) > 0) ? templatefile(
    "${path.module}/templates/cloud-init.tpl",
    {
      default_user        = var.default_user
      password            = var.password
      ssh_authorized_keys = var.ssh_authorized_keys
    }
  ) : null

  effective_user_data = var.user_data != null ? var.user_data : local._generated_user_data
}

# Optional SSH key — created only when ssh_public_key is provided.
resource "harvester_ssh_key" "this" {
  count = var.create_ssh_key ? 1 : 0

  name       = "${var.name}-key"
  namespace  = var.namespace
  public_key = var.ssh_public_key
}

resource "harvester_virtualmachine" "this" {
  name                 = var.name
  namespace            = var.namespace
  restart_after_update = true

  cpu    = var.cpu
  memory = var.memory

  run_strategy = var.run_strategy
  machine_type = "q35"

  ssh_keys = var.create_ssh_key ? [harvester_ssh_key.this[0].id] : []

  network_interface {
    name           = "nic-1"
    wait_for_lease = var.wait_for_lease
    network_name   = var.network_name
  }

  disk {
    name        = "rootdisk"
    type        = "disk"
    size        = var.disk_size
    bus         = "virtio"
    boot_order  = 1
    image       = var.image_name
    auto_delete = true
  }

  dynamic "disk" {
    for_each = var.additional_disks
    content {
      name        = disk.value.name
      size        = disk.value.size
      bus         = "virtio"
      image       = disk.value.image
      auto_delete = disk.value.auto_delete
    }
  }

  dynamic "cloudinit" {
    for_each = local.effective_user_data != null ? [1] : []
    content {
      user_data    = local.effective_user_data
      network_data = var.network_data
    }
  }

  lifecycle {
    ignore_changes = [
      # cloud-init runs only on first boot; template changes after provisioning
      # have no effect and should not trigger a VM restart.
      cloudinit,
    ]
  }
}

# Optional scheduled backup — created only when backup_schedule is set.
# Uses kubernetes_manifest because harvester_schedule_backup requires provider >= 1.8.
resource "kubernetes_manifest" "scheduled_backup" {
  count = var.backup_schedule != null ? 1 : 0

  manifest = {
    apiVersion = "harvesterhci.io/v1beta1"
    kind       = "ScheduleVMBackup"
    metadata = {
      name      = "${var.name}-backup"
      namespace = var.namespace
    }
    spec = {
      cron       = var.backup_schedule
      retain     = var.backup_retain
      maxFailure = var.backup_max_failure
      suspend    = !var.backup_enabled
      vmbackup = {
        source = {
          apiGroup = "kubevirt.io"
          kind     = "VirtualMachine"
          name     = harvester_virtualmachine.this.name
        }
        type = "backup"
      }
    }
  }
}
