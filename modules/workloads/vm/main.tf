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
    for_each = var.user_data != null ? [1] : []
    content {
      user_data    = var.user_data
      network_data = var.network_data
    }
  }
}
