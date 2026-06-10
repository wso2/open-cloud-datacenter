resource "harvester_cloudinit_secret" "cloudinit" {
  name      = "${var.vm_name}-cloudinit"
  namespace = var.harvester_namespace

  user_data = templatefile("${path.module}/templates/cloud-init.yaml.tpl", {
    password = var.vm_password
    nginx_conf_b64 = base64encode(templatefile("${path.module}/templates/nginx.conf.tpl", {
      rancher_hostname = var.rancher_hostname
      node_ips         = var.rke2_node_ips
    }))
    tls_cert_b64        = base64encode(var.tls_cert)
    tls_key_b64         = base64encode(var.tls_key)
    ssh_authorized_keys = var.ssh_authorized_keys
  })

  network_data = templatefile("${path.module}/templates/network-config.yaml.tpl", {
    static_ip     = var.static_ip
    gateway       = var.gateway
    subnet_prefix = var.subnet_prefix
    dns_servers   = length(var.dns_servers) > 0 ? var.dns_servers : ["8.8.8.8"]
  })
}

resource "harvester_virtualmachine" "nginx_lb" {
  name                 = var.vm_name
  namespace            = var.harvester_namespace
  restart_after_update = true

  cpu    = var.vm_cpu
  memory = var.vm_memory

  run_strategy = "RerunOnFailure"
  machine_type = "q35"

  ssh_keys = var.ssh_key_ids

  network_interface {
    name         = var.network_interface_name
    type         = "bridge"
    network_name = var.network_name
  }

  disk {
    name       = "disk-0"
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

  # Harvester auto-creates a cloudinitdisk when a cloud-init secret is attached.
  # Declare it here so the provider does not try to remove it on every plan.
  dynamic "disk" {
    for_each = [1]
    content {
      name = "cloudinitdisk"
      type = "disk"
      bus  = "virtio"
      size = "0Gi"
    }
  }

  cloudinit {
    user_data_secret_name    = harvester_cloudinit_secret.cloudinit.name
    network_data_secret_name = harvester_cloudinit_secret.cloudinit.name
  }

  lifecycle {
    ignore_changes = [
      # Harvester manages the cloudinitdisk size automatically; ignore it to prevent perpetual drift.
      disk[1].size,
    ]
  }
}
