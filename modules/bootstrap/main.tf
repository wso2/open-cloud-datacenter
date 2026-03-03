terraform {
  required_providers {
    harvester = {
      source  = "harvester/harvester"
      version = "~> 0.6.0"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.0"
    }
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 3.0"
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
    rancher_password = var.rancher_admin_password,
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

  # Note: Since the VM is on a masquerade network, Terraform cannot reach it directly on port 22 via IP.
  # We instead use the K3s/RKE2 approach of installing inside cloud-init and waiting locally, OR
  # we must use a nodeport/forwarding trick if the Kubernetes cluster exposes ssh. 
  # However, wait... The previous user's config successfully used `masquerade` but ran Helm IN the cloud-init script!
  # If we must run Terraform Helm, Harvester doesn't natively expose the Masquerade IP to the external network.
  # Let's switch to the local-exec polling approach against the Harvester API.

  # Copy the kubeconfig back to the terraform runner securely
  # Wait... actually, Harvester's SSH is inaccessible on masquerade from the outside without a LoadBalancer or floating IP.
  # Let's pivot to the user's working K3s cloud-init script approach for the Phase 0 bootstrap, 
  # returning the Rancher URL and relying on that for Phase 1!

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

# Note: The Helm and Rancher2 bootstrap logic below would fail because the Helm provider cannot dynamically access the Masquerade Kubeconfig.
# Because the user explicitly pointed out their cloud-init script gracefully handled Helm inside Harvester, we will pivot to that!
# The user's cloud-init handles cert-manager and rancher installations.
# Therefore, Phase 1 and 2 will connect directly to the resulting Rancher URL.
