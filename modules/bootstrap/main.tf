terraform {
  required_version = ">= 1.3"
  required_providers {
    harvester = {
      source  = "harvester/harvester"
      version = "~> 1.7"
    }
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 13.1"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }
}

resource "tls_private_key" "bootstrap_key" {
  algorithm = "RSA"
  rsa_bits  = 4096
}

resource "harvester_ssh_key" "bootstrap_key" {
  name       = "${var.vm_name}-ssh-key"
  namespace  = var.harvester_namespace
  public_key = tls_private_key.bootstrap_key.public_key_openssh
}

# Removed dynamic VLAN creation as DHCP is failing in the cluster

# 1. Create the Cloud-Init Secret for the VM (Bypasses 2KB limit)
resource "harvester_cloudinit_secret" "cloudinit" {
  count     = var.node_count
  name      = var.node_count > 1 ? "${var.vm_name}-cloudinit-${count.index}" : "${var.vm_name}-cloudinit"
  namespace = var.harvester_namespace

  user_data = templatefile("${path.module}/templates/cloud-init.yaml.tpl", {
    password         = var.vm_password,
    cluster_dns      = var.rancher_hostname,
    rancher_password = var.bootstrap_password,
    ssh_public_key   = tls_private_key.bootstrap_key.public_key_openssh,
    node_index       = count.index,
    node_count       = var.node_count,
    lb_ip            = var.ippool_start # Using the LB IP for join logic
  })
}

# 2. Create the Harvester VM
resource "harvester_virtualmachine" "rancher_server" {
  count                = var.node_count
  name                 = var.node_count > 1 ? "${var.vm_name}-${count.index}" : var.vm_name
  namespace            = var.harvester_namespace
  restart_after_update = true

  cpu    = 4
  memory = "8Gi"

  run_strategy = "RerunOnFailure"
  machine_type = "q35"

  ssh_keys = [harvester_ssh_key.bootstrap_key.id]

  network_interface {
    name = "nic-1"
    type = "masquerade"
  }

  disk {
    name       = "rootdisk"
    type       = "disk"
    size       = "40Gi"
    bus        = "virtio"
    boot_order = 1

    image       = var.ubuntu_image_id
    auto_delete = true
  }

  cloudinit {
    user_data_secret_name = harvester_cloudinit_secret.cloudinit[count.index].name
    network_data          = ""
  }

  # Rancher is installed entirely by cloud-init inside the VM (RKE2 + cert-manager + Helm).
  # The VM uses a masquerade network so Terraform cannot SSH into it directly.
  provisioner "local-exec" {
    command = "echo 'Please wait for cloud-init to finish installing RKE2/K3s and Rancher internally!'"
  }
}

# 3. Expose the Rancher VM via a Load Balancer
resource "harvester_loadbalancer" "rancher_lb" {
  name      = "${var.vm_name}-lb"
  namespace = var.harvester_namespace

  depends_on = [
    harvester_virtualmachine.rancher_server,
    harvester_ippool.rancher_ips
  ]

  workload_type = "vm"
  ipam          = "pool"
  ippool        = harvester_ippool.rancher_ips.name

  listener {
    name         = "https"
    port         = 443
    protocol     = "TCP"
    backend_port = 443
  }

  listener {
    name         = "http"
    port         = 80
    protocol     = "TCP"
    backend_port = 80
  }

  backend_selector {
    key    = "harvesterhci.io/vmName"
    values = harvester_virtualmachine.rancher_server[*].name
  }

  healthcheck {
    port              = 443
    success_threshold = 1
    failure_threshold = 3
    period_seconds    = 10
    timeout_seconds   = 5
  }
}

# 4. Create an IP Pool for the Load Balancer
resource "harvester_ippool" "rancher_ips" {
  name = "${var.vm_name}-ips"

  range {
    start   = var.ippool_start
    end     = var.ippool_end
    subnet  = var.ippool_subnet
    gateway = var.ippool_gateway
  }
}

# Rancher is installed inside the VM by cloud-init (cert-manager + Helm).
# rancher2_bootstrap waits for Rancher to be reachable and sets the permanent admin
# password. Re-run `terraform apply` if Rancher is still starting up on first attempt.
resource "rancher2_bootstrap" "admin" {
  initial_password = var.bootstrap_password
  password         = var.rancher_admin_password
}
