output "kubeconfig_path" {
  value       = harvester_bootstrap.this.kubeconfig
  description = "Path of the generated Harvester admin kubeconfig — feed it to the harvester/kubernetes providers of later layers"
}

output "should_update_password" {
  value       = harvester_bootstrap.this.should_update_password
  description = "Whether the provider found the initial password still active and changed it"
}
