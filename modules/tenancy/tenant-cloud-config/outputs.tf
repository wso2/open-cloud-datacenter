output "node_password" {
  value       = random_password.node_password.result
  sensitive   = true
  description = "Random password set for the ubuntu user in the k8s-cluster-node-with-storage-network cloud-init template. Distribute to tenant teams via a combined credentials output."
}
