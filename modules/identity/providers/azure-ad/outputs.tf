# ── Standardized OIDC interface (shared by all provider modules) ──────────────

output "client_id" {
  value       = var.client_id
  description = "OAuth2 client ID."
}

output "client_secret" {
  value       = var.client_secret
  description = "OAuth2 client secret."
  sensitive   = true
}

output "issuer_url" {
  value       = local.issuer_url
  description = "OIDC issuer URL (https://login.microsoftonline.com/{tenant}/v2.0)."
}

output "auth_endpoint" {
  value       = local.auth_endpoint
  description = "OAuth2 authorization endpoint."
}

output "token_endpoint" {
  value       = local.token_endpoint
  description = "OAuth2 token endpoint."
}

output "jwks_url" {
  value       = local.jwks_url
  description = "JSON Web Key Set URL."
}

# ── Azure AD-specific outputs ─────────────────────────────────────────────────

output "discovery_url" {
  value       = local.discovery_url
  description = "OIDC well-known discovery URL."
}
