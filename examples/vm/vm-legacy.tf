# terraform {
#   required_providers {
#     dcapi = {
#       source  = "registry.terraform.io/wso2/dcapi"
#       version = "~> 0.1.0"
#     }
#   }
# }

# provider "dcapi" {}

# resource "dcapi_project" "example" {
#   tenant_id  = "my-org"
#   project_id = "vm-project-s87"

#   name        = "Infrastructure Team"
#   description = "Core infrastructure resources: VNets, clusters, and shared VMs."

# }

# # ── Option B: Legacy bridge mode ─────────────────────────────────────────────────────
# #
# # In legacy bridge mode, the VM is attached to a shared L2 bridge network that predates
# # the VNet/Subnet model. Use this only when VPC mode is not available or not suitable.
# # network_name is mutually exclusive with vnet_id and subnet_id.

# resource "dcapi_virtual_machine" "web_legacy" {
  
#   tenant_id  = dcapi_project.example.tenant_id   
#   project_id = dcapi_project.example.project_id  

#   name = "vm-legacy-s87"
#   size = "medium"
#   image_name = "rancher-infra/ubuntu-22-04"
#   network_name = "iaas/vm-network-001"
# }


# output "vm_ip" {
#   value       = dcapi_virtual_machine.web_legacy.ip_address
#   description = "IP address assigned to the legacy-mode VM by DC-API."
# }

# output "vm_private_key" {
#   value       = dcapi_virtual_machine.web_legacy.private_key
#   sensitive   = true
#   description = "SSH private key for web_legacy VM. Only available right after terraform apply."
# }

# output "vm_console_password" {
#   value       = dcapi_virtual_machine.web_legacy.console_password
#   sensitive   = true
#   description = "Web-console password for web_legacy VM. Only available right after terraform apply."
# }

