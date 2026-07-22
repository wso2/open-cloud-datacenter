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

  # When cloudinit_secret_name is set, reference that
  # existing secret directly instead of creating one named
  # "${var.name}-cloud-config". Use for VMs whose cloud-init secret already
  # exists under a name that doesn't match this module's derived convention
  # (e.g. a Harvester-UI generated random suffix) — renaming it would be a
  # ForceNew replace. Harvester-UI-created VMs commonly point both the
  # user-data and network-data refs at the same secret object, so the
  # override applies to both.
  cloudinit_secret_name = var.cloudinit_secret_name != null ? var.cloudinit_secret_name : (
    length(harvester_cloudinit_secret.this) > 0 ? harvester_cloudinit_secret.this[0].name : null
  )
  has_cloudinit = local.cloudinit_secret_name != null
}

# Optional Harvester Cloud-Init Secret to hold the cloud-init data.
# Skipped when var.cloudinit_secret_name references a pre-existing secret.
resource "harvester_cloudinit_secret" "this" {
  count = local.effective_user_data != null && var.cloudinit_secret_name == null ? 1 : 0

  name      = "${var.name}-cloud-config"
  namespace = var.namespace
  user_data = local.effective_user_data
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
  restart_after_update = var.restart_after_update

  cpu    = var.cpu
  memory = var.memory

  run_strategy = var.run_strategy
  machine_type = "q35"

  ssh_keys = concat(
    var.create_ssh_key ? [harvester_ssh_key.this[0].id] : [],
    var.ssh_key_ids,
  )

  network_interface {
    name           = var.network_interface_name
    wait_for_lease = var.wait_for_lease
    network_name   = var.network_name
  }

  disk {
    name        = var.disk_name
    type        = "disk"
    size        = var.disk_size
    bus         = "virtio"
    boot_order  = 1
    image       = var.image_name
    auto_delete = var.disk_auto_delete
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

  dynamic "input" {
    for_each = var.input_devices
    content {
      name = input.value.name
      type = input.value.type
      bus  = input.value.bus
    }
  }

  dynamic "cloudinit" {
    for_each = local.has_cloudinit ? [1] : []
    content {
      user_data_secret_name = local.cloudinit_secret_name
      network_data          = var.network_data
      # Only set when referencing a pre-existing secret (brownfield override) —
      # module-created secrets only ever hold user_data.
      network_data_secret_name = var.cloudinit_secret_name
    }
  }

  # cloudinitdisk is automatically created when cloudinit is created.
  dynamic "disk" {
    for_each = local.has_cloudinit ? [1] : []
    content {
      name = "cloudinitdisk"
      type = "disk"
      bus  = "virtio"
    }
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
