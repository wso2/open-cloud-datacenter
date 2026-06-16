# ── Standardized OIDC interface (shared by all provider modules) ──────────────

output "client_id" {
  value       = var.use_existing_app ? var.existing_client_id : asgardeo_application.this[0].client_id
  description = "OAuth2 client ID — either from the created application or the existing one."
}

output "client_secret" {
  value       = var.use_existing_app ? var.existing_client_secret : asgardeo_application.this[0].client_secret
  description = "OAuth2 client secret — either from the created application or the existing one."
  sensitive   = true
}

output "issuer_url" {
  value       = local.issuer_url
  description = "OIDC issuer URL (token endpoint used as issuer for Rancher compatibility)."
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
  description = "JSON Web Key Set URL for token signature verification."
}

# ── Asgardeo-specific outputs ─────────────────────────────────────────────────

output "application_id" {
  value       = var.use_existing_app ? "" : asgardeo_application.this[0].id
  description = "Asgardeo internal application ID. Empty when use_existing_app = true."
}

output "discovery_url" {
  value       = local.discovery_url
  description = "OIDC well-known discovery URL for manual verification."
}
