output "auth_config_id" {
  value       = rancher2_auth_config_generic_oidc.this.id
  description = "Rancher auth config resource ID."
}
