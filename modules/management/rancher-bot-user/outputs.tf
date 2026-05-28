output "user_id" {
  value       = rancher2_user.this.id
  description = "Rancher user ID for the bot (e.g. 'u-abc123'). Use to reference this identity in external rancher2_*_role_template_binding resources."
}

output "username" {
  value       = rancher2_user.this.username
  description = "Rancher username for the bot. Same as var.name."
}

output "token" {
  value       = rancher2_custom_user_token.this.token
  sensitive   = true
  description = "Rancher API token for the bot user. Format: 'token-xxxxx:yyyyyy'. Use as token_key in the rancher2 provider block. Prefer retrieving via the kubernetes_secret stored in the tenant namespace rather than reading this output directly."
}
