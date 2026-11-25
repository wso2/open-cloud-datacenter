output "node_ips" {
  value       = [for n in values(local.node_map) : n.ip]
  description = "Static IPs of every RKE2 node — feed to platform/nginx-lb-vm as rke2_node_ips"
}

output "control_plane_ips" {
  value       = local.control_plane_ips
  description = "Static IPs of the control-plane nodes; its length is the natural Rancher replica count"
}

output "server_count" {
  value       = length(local._servers)
  description = "Number of rke2-server (control-plane/etcd) nodes"
}

output "init_node_ip" {
  value       = try(local.node_map[local._init_key].ip, "")
  description = "IP of the node that bootstrapped etcd — the one to copy the admin kubeconfig from"
}

output "vm_ids" {
  value       = { for k, vm in harvester_virtualmachine.node : k => vm.id }
  description = "Harvester resource IDs of the node VMs, keyed by node"
}

output "kube_api_url" {
  value       = "https://${var.kube_api_host}:6443"
  description = "Kubernetes API endpoint through the load balancer — the server address in the fetched kubeconfig"
}

output "ssh_key_id" {
  value       = var.create_ssh_key ? harvester_ssh_key.bootstrap_key[0].id : try(var.ssh_key_ids[0], "")
  description = "Harvester SSH key ID attached to the nodes"
}

output "ssh_public_key" {
  value       = local.ssh_public_key
  description = "OpenSSH public key text — pass to platform/nginx-lb-vm so Harvester can match it against ssh_key_ids"
}

output "ssh_private_key" {
  value       = var.create_ssh_key ? tls_private_key.bootstrap_key[0].private_key_openssh : ""
  description = "Private key for the 'ubuntu' user — needed to copy the admin kubeconfig off the init node"
  sensitive   = true
}

output "kubeconfig_fetch_command" {
  value       = "ssh ubuntu@${try(local.node_map[local._init_key].ip, "<init-node>")} sudo cat /home/ubuntu/.kube/config"
  description = "How to retrieve the admin kubeconfig once the cluster is up; its server already points at kube_api_url"
}
