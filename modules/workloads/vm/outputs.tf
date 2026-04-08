output "vm_name" {
  value       = harvester_virtualmachine.this.name
  description = "Name of the created virtual machine."
}

output "vm_id" {
  value       = harvester_virtualmachine.this.id
  description = "Harvester resource ID of the virtual machine (namespace/name)."
}

output "network_interfaces" {
  value       = harvester_virtualmachine.this.network_interface
  description = "Network interface objects, including the leased IP address once the VM is running."
}

output "ssh_key_id" {
  value       = var.ssh_public_key != null ? harvester_ssh_key.this[0].id : null
  description = "Harvester SSH key ID attached to the VM, or null if no SSH key was provided."
}

output "backup_schedule_name" {
  value       = var.backup_schedule != null ? kubernetes_manifest.scheduled_backup[0].manifest.metadata.name : null
  description = "Name of the scheduled VM backup, or null if no backup schedule was configured."
}
